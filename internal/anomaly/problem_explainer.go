package anomaly

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
)

// ProblemExplainer is a background goroutine that fills the
// AISummary column for newly-opened critical problems (v0.5.254).
// Operator opens /problems or /inbox 30s after a fire, sees a
// pre-baked "why fired + first checks" blurb instead of having
// to click "✨ Explain" themselves.
//
// Design notes:
//
//   - HA-gated via a per-tick Redis lock (same pattern as every
//     other Coremetry worker). Multiple replicas don't
//     duplicate-call the Copilot.
//   - Critical severity only by default — info / warning rules
//     are far more numerous and don't justify the AI cost.
//   - Per-tick cap (16 by default) so a thundering herd of
//     opens doesn't burn the Copilot quota in one minute.
//   - Surface = "problem-auto-explain" so the /ai page shows
//     a dedicated row for this background traffic alongside
//     operator-clicked Explain calls.
//   - Configurable: AI Copilot Configured() must return true.
//     We don't gate on a separate "auto-explain enabled"
//     setting yet — once the operator has an API key wired,
//     they almost certainly want the auto-explain. A toggle is
//     a follow-up if anyone asks.
const explainerLockKey = "problem-explainer:lock"
const explainerBatch = 16

type ProblemExplainer struct {
	store    *chstore.Store
	copilot  *copilot.Service
	lock     cache.Lock
	interval time.Duration
	batch    int
}

func NewProblemExplainer(store *chstore.Store, cop *copilot.Service, lock cache.Lock) *ProblemExplainer {
	return &ProblemExplainer{
		store:    store,
		copilot:  cop,
		lock:     lock,
		interval: 30 * time.Second,
		batch:    explainerBatch,
	}
}

// Start runs the explainer loop until ctx is cancelled. Initial
// tick fires immediately so a problem opened during pod startup
// gets explained without waiting a full interval.
func (e *ProblemExplainer) Start(ctx context.Context) {
	if e == nil || e.copilot == nil {
		return
	}
	t := time.NewTicker(e.interval)
	defer t.Stop()
	// Initial tick — also serves as a "Copilot config drift catch":
	// if the operator wires an API key mid-day, the first tick will
	// retroactively explain anything still missing a summary.
	e.tickIfLeader(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.tickIfLeader(ctx)
		}
	}
}

func (e *ProblemExplainer) tickIfLeader(ctx context.Context) {
	if !e.copilot.Configured() {
		return // No API key wired — silently noop.
	}
	ok, err := e.lock.TryAcquire(ctx, explainerLockKey, 2*e.interval)
	if err != nil {
		log.Printf("[problem-explainer] lock: %v — running anyway", err)
	} else if !ok {
		return // Another replica is running this tick.
	} else {
		defer e.lock.Release(ctx, explainerLockKey)
	}
	e.run(ctx)
}

// run grabs the candidate problems + calls the Copilot per row.
// Critical severity, status=open, ai_summary still empty. Skips
// resolved / acknowledged / info-warning rows — they're either
// already-actioned or low-value-to-explain.
func (e *ProblemExplainer) run(ctx context.Context) {
	problems, err := e.store.ListProblems(ctx, chstore.ProblemFilter{
		Status:   "open",
		Severity: "critical",
		Limit:    200,
	})
	if err != nil {
		log.Printf("[problem-explainer] list: %v", err)
		return
	}
	filled := 0
	for _, p := range problems {
		if filled >= e.batch {
			break
		}
		if strings.TrimSpace(p.AISummary) != "" {
			continue
		}
		summary, err := e.explain(ctx, p)
		if err != nil {
			log.Printf("[problem-explainer] %s: %v", p.ID, err)
			continue
		}
		if strings.TrimSpace(summary) == "" {
			continue
		}
		if err := e.store.UpsertProblemAISummary(ctx, p.ID, summary); err != nil {
			log.Printf("[problem-explainer] write %s: %v", p.ID, err)
			continue
		}
		filled++
	}
	if filled > 0 {
		log.Printf("[problem-explainer] filled %d summary/ies", filled)
	}
}

// explain composes the user prompt for one Problem + runs through
// the Copilot with surface "problem-auto-explain". Same system
// prompt as the operator-clicked /api/copilot/explain-problem
// endpoint — keeps the AI's tone consistent across surfaces.
func (e *ProblemExplainer) explain(ctx context.Context, p chstore.Problem) (string, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Rule: %s\n", p.RuleName)
	fmt.Fprintf(&sb, "Service: %s\n", p.Service)
	fmt.Fprintf(&sb, "Severity: %s\n", p.Severity)
	fmt.Fprintf(&sb, "Metric: %s\n", p.Metric)
	fmt.Fprintf(&sb, "Value: %.4g (threshold %.4g)\n", p.Value, p.Threshold)
	fmt.Fprintf(&sb, "Started: %s\n", time.Unix(0, p.StartedAt).UTC().Format(time.RFC3339))
	if p.Description != "" {
		fmt.Fprintf(&sb, "Description: %s\n", p.Description)
	}
	// Background context — surface = "problem-auto-explain" so the
	// /ai page can break out the auto-explain volume from operator-
	// clicked Explains.
	ctx = copilot.WithMeta(ctx, copilot.CallMeta{
		Surface:   "problem-auto-explain",
		UserID:    "system",
		UserEmail: "",
	})
	return e.copilot.Explain(ctx, copilot.SystemPromptProblem(), sb.String())
}
