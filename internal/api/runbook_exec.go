package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

// Runbook executions (v0.7.0) — start a run, step through it, cancel.
// Manual steps are ticked here (complete/skip/fail + note); automated
// steps (http/javascript/bash) are dispatched to the coremetry-agent in a
// later increment and sit pending until then (operators can skip them to
// proceed). Every transition is audited (audit-layer 1); the execution
// record itself is the durable audit (layer 2).

var stepActionStatus = map[string]string{
	"complete": chstore.StepCompleted,
	"skip":     chstore.StepSkipped,
	"fail":     chstore.StepFailed,
}

func (s *Server) executeRunbook(w http.ResponseWriter, r *http.Request) {
	rb, err := s.store.GetRunbook(r.Context(), r.PathValue("id"))
	if err != nil || rb == nil {
		http.Error(w, `{"error":"runbook not found"}`, http.StatusNotFound)
		return
	}
	if !rb.Enabled {
		http.Error(w, `{"error":"runbook is disabled — enable it before running"}`, http.StatusConflict)
		return
	}
	var body struct {
		ProblemID string `json:"problemId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // body optional
	startedBy := ""
	if c := auth.FromContext(r.Context()); c != nil {
		startedBy = c.Email
	}
	exec, err := s.store.StartExecution(r.Context(), *rb, "rbx-"+newID(8), startedBy, body.ProblemID)
	if err != nil {
		writeErr(w, err)
		return
	}
	details, _ := json.Marshal(map[string]any{
		"runbookId": rb.ID, "executionId": exec.ID,
		"problemId": body.ProblemID, "steps": len(exec.StepStates),
	})
	s.audit(r, "runbook.execute", "runbook", rb.ID, string(details))
	writeJSON(w, exec)
}

func (s *Server) listExecutions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	execs, err := s.store.ListExecutions(r.Context(), chstore.ExecutionFilter{
		RunbookID: q.Get("runbookId"),
		Status:    q.Get("status"),
		ProblemID: q.Get("problemId"),
		Limit:     limit,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, execs)
}

func (s *Server) getExecution(w http.ResponseWriter, r *http.Request) {
	e, err := s.store.GetExecution(r.Context(), r.PathValue("id"))
	if err != nil || e == nil {
		http.Error(w, `{"error":"execution not found"}`, http.StatusNotFound)
		return
	}
	writeJSON(w, e)
}

// execStepAction ticks a manual step: complete | skip | fail (+ note). The
// step's status, the actor, and a timestamp are recorded; the execution
// status is re-derived and completedAt stamped when it reaches a terminal
// state.
func (s *Server) execStepAction(w http.ResponseWriter, r *http.Request) {
	execID := r.PathValue("id")
	stepID := r.PathValue("stepId")
	var body struct {
		Action string `json:"action"` // complete | skip | fail
		Note   string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	status, ok := stepActionStatus[body.Action]
	if !ok {
		http.Error(w, `{"error":"action must be complete|skip|fail"}`, http.StatusBadRequest)
		return
	}
	e, err := s.store.GetExecution(r.Context(), execID)
	if err != nil || e == nil {
		http.Error(w, `{"error":"execution not found"}`, http.StatusNotFound)
		return
	}
	switch e.Status {
	case chstore.RunExecCancelled, chstore.RunExecCompleted, chstore.RunExecFailed:
		http.Error(w, `{"error":"execution already finished"}`, http.StatusConflict)
		return
	}
	by := ""
	if c := auth.FromContext(r.Context()); c != nil {
		by = c.Email
	}
	now := time.Now().UnixNano()
	states, found := chstore.ApplyStepResult(e.StepStates, stepID, status, by, body.Note, "", "", now)
	if !found {
		http.Error(w, `{"error":"step not found"}`, http.StatusNotFound)
		return
	}
	e.StepStates = states
	e.Status = chstore.DeriveExecStatus(states)
	if e.Status == chstore.RunExecCompleted || e.Status == chstore.RunExecFailed {
		e.CompletedAt = now
	}
	if err := s.store.UpsertExecution(r.Context(), *e); err != nil {
		writeErr(w, err)
		return
	}
	details, _ := json.Marshal(map[string]any{"executionId": execID, "stepId": stepID, "status": status})
	s.audit(r, "runbook.step."+body.Action, "runbook_execution", execID, string(details))
	s.maybeNotifyRunbookComplete(r.Context(), e)
	writeJSON(w, e)
}

// maybeNotifyRunbookComplete fires a completion notification iff the execution
// just reached a terminal state AND its runbook opted in (NotifyOnComplete).
// Shared by the manual-tick path here and reused conceptually by the agent.
func (s *Server) maybeNotifyRunbookComplete(ctx context.Context, e *chstore.RunbookExecution) {
	if e == nil || (e.Status != chstore.RunExecCompleted && e.Status != chstore.RunExecFailed) {
		return
	}
	rb, err := s.store.GetRunbook(ctx, e.RunbookID)
	if err != nil || rb == nil || !rb.NotifyOnComplete {
		return
	}
	go s.notify.SendRunbookComplete(context.Background(), *e, rb.NotifyChannels)
}

func (s *Server) cancelExecution(w http.ResponseWriter, r *http.Request) {
	execID := r.PathValue("id")
	e, err := s.store.GetExecution(r.Context(), execID)
	if err != nil || e == nil {
		http.Error(w, `{"error":"execution not found"}`, http.StatusNotFound)
		return
	}
	e.Status = chstore.RunExecCancelled
	e.CompletedAt = time.Now().UnixNano()
	if err := s.store.UpsertExecution(r.Context(), *e); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "runbook.cancel", "runbook_execution", execID, "")
	writeJSON(w, e)
}
