package agent

import (
	"context"
	"log"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/notify"
)

// terminalStep / agentKind classify step statuses + kinds. Manual + query
// steps are resolved by the human runner (query shows a "run in Explore"
// hint); only http/javascript/bash are the agent's to execute.
var terminalStep = map[string]bool{
	chstore.StepCompleted: true, chstore.StepSkipped: true, chstore.StepFailed: true,
}
var agentKind = map[string]bool{"http": true, "javascript": true, "bash": true}

// Runner is the COREMETRY_MODE=agent loop. It polls running runbook
// executions for a claimable automated step (the next non-terminal step, when
// its kind is http/javascript/bash), claims it via a Redis lock so only one
// agent pod runs it (HA-safe), executes it in-process (sandboxed), and writes
// the result back — advancing the execution. Multiple agent pods parallelize
// across executions; each runs at most one step per tick.
type Runner struct {
	store    *chstore.Store
	lock     cache.Lock
	notifier *notify.Notifier // nil = no completion notifications
	agentID  string
	interval time.Duration
}

func NewRunner(store *chstore.Store, lock cache.Lock, notifier *notify.Notifier, agentID string, interval time.Duration) *Runner {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Runner{store: store, lock: lock, notifier: notifier, agentID: agentID, interval: interval}
}

func (r *Runner) Start(ctx context.Context) {
	log.Printf("[agent] runbook agent started (id=%s, poll=%s)", r.agentID, r.interval)
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tick(ctx)
		}
	}
}

func (r *Runner) tick(ctx context.Context) {
	execs, err := r.store.ListExecutions(ctx, chstore.ExecutionFilter{Status: chstore.RunExecRunning, Limit: 200})
	if err != nil {
		log.Printf("[agent] list executions: %v", err)
		return
	}
	for _, e := range execs {
		idx := firstActionable(e.StepStates)
		if idx < 0 {
			continue
		}
		st := e.StepStates[idx]
		if !agentKind[st.Kind] || st.Status == chstore.StepRunning {
			continue // human-owned (manual/query) or already being executed
		}
		key := "rb:claim:" + e.ID
		ok, err := r.lock.TryAcquire(ctx, key, 5*time.Minute)
		if err != nil || !ok {
			continue // another agent owns this execution
		}
		r.runStep(ctx, e.ID, st)
		_ = r.lock.Release(ctx, key)
		return // one step per tick — keeps each agent's blast radius small
	}
}

func (r *Runner) runStep(ctx context.Context, execID string, st chstore.StepState) {
	// Mark running (own update, not ApplyStepResult — that stamps EndedAt).
	if e, err := r.store.GetExecution(ctx, execID); err == nil && e != nil {
		for i := range e.StepStates {
			if e.StepStates[i].StepID == st.StepID {
				e.StepStates[i].Status = chstore.StepRunning
				e.StepStates[i].By = r.agentID
				e.StepStates[i].StartedAt = time.Now().UnixNano()
				break
			}
		}
		_ = r.store.UpsertExecution(ctx, *e)
	}

	res := Execute(ctx, AutomatedStep{
		Kind: st.Kind, URL: st.URL, Method: st.Method, Headers: st.Headers,
		Body: st.Body, Script: st.Script, Command: st.Command, TimeoutMs: st.TimeoutMs,
	})
	status := chstore.StepCompleted
	if res.Error != "" {
		status = chstore.StepFailed
	}

	e, err := r.store.GetExecution(ctx, execID)
	if err != nil || e == nil {
		return
	}
	// The operator may have cancelled the run while the step executed — don't
	// resurrect a cancelled execution.
	if e.Status == chstore.RunExecCancelled {
		return
	}
	states, _ := chstore.ApplyStepResult(e.StepStates, st.StepID, status, r.agentID, "", res.Output, res.Error, time.Now().UnixNano())
	e.StepStates = states
	e.Status = chstore.DeriveExecStatus(states)
	if e.Status == chstore.RunExecCompleted || e.Status == chstore.RunExecFailed {
		e.CompletedAt = time.Now().UnixNano()
	}
	if err := r.store.UpsertExecution(ctx, *e); err != nil {
		log.Printf("[agent] persist result for %s/%s: %v", execID, st.StepID, err)
		return
	}
	// Completion notification (opt-in per runbook) — the agent path covers
	// fully-automated runbooks that never pass through the manual API tick.
	if r.notifier != nil && (e.Status == chstore.RunExecCompleted || e.Status == chstore.RunExecFailed) {
		if rb, err := r.store.GetRunbook(ctx, e.RunbookID); err == nil && rb != nil && rb.NotifyOnComplete {
			r.notifier.SendRunbookComplete(context.Background(), *e)
		}
	}
}

// firstActionable returns the index of the first non-terminal step (the one
// the run is currently waiting on), or -1 if every step is terminal.
func firstActionable(states []chstore.StepState) int {
	for i, s := range states {
		if !terminalStep[s.Status] {
			return i
		}
	}
	return -1
}
