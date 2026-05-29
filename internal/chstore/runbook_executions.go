package chstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// RunbookExecution is one tracked RUN of a runbook (v0.7.0). It is the
// durable audit record — "who ran what when, which steps executed". Steps
// are SNAPSHOTTED onto StepStates at start so template edits never rewrite
// a historical or in-flight run.
type RunbookExecution struct {
	ID            string      `json:"id"`
	RunbookID     string      `json:"runbookId"`
	TitleSnapshot string      `json:"titleSnapshot"`
	Status        string      `json:"status"`
	StartedBy     string      `json:"startedBy,omitempty"`
	StartedAt     int64       `json:"startedAt"`            // unix ns
	CompletedAt   int64       `json:"completedAt,omitempty"` // unix ns; 0 = running
	ProblemID     string      `json:"problemId,omitempty"`
	StepStates    []StepState `json:"stepStates"`
	UpdatedAt     int64       `json:"updatedAt"` // unix ns (version source)
}

// StepState is a step's snapshot + live status within an execution.
type StepState struct {
	StepID       string `json:"stepId"`
	Order        int    `json:"order"`
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	Instructions string `json:"instructions,omitempty"`
	Status       string `json:"status"`
	By           string `json:"by,omitempty"`     // user (manual) or agent id (automated)
	Note         string `json:"note,omitempty"`   // operator note on tick
	Output       string `json:"output,omitempty"` // stdout / returnValue / HTTP body
	Error        string `json:"error,omitempty"`
	StartedAt    int64  `json:"startedAt,omitempty"`
	EndedAt      int64  `json:"endedAt,omitempty"`
	// Executable payload — snapshotted from the step at execution start so the
	// coremetry-agent runs exactly what the runbook said at run time (template
	// edits never change an in-flight run). Only the fields for Kind are set.
	URL       string            `json:"url,omitempty"`
	Method    string            `json:"method,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	Script    string            `json:"script,omitempty"`
	Command   string            `json:"command,omitempty"`
	TimeoutMs int               `json:"timeoutMs,omitempty"`
}

// Execution statuses.
const (
	RunExecRunning   = "running"
	RunExecWaiting   = "waiting_for_user"
	RunExecCompleted = "completed"
	RunExecFailed    = "failed"
	RunExecCancelled = "cancelled"
)

// Step statuses.
const (
	StepPending   = "pending"
	StepRunning   = "running"
	StepWaiting   = "waiting_for_user"
	StepCompleted = "completed"
	StepSkipped   = "skipped"
	StepFailed    = "failed"
)

// snapshotSteps copies a runbook's ordered steps onto fresh StepStates,
// all pending. Pure — unit-tested.
func snapshotSteps(steps []RunbookStep) []StepState {
	out := make([]StepState, len(steps))
	for i, st := range steps {
		id := st.ID
		if id == "" {
			// Defensive: a step missing an id (e.g. created via the API
			// without one) would collide with other empty-id steps in
			// ApplyStepResult (matched by stepId), corrupting which step a
			// result lands on. The snapshot is frozen, so an index-based id is
			// unique + stable for the run. (v0.7.6)
			id = fmt.Sprintf("st%d", i+1)
		}
		out[i] = StepState{
			StepID:       id,
			Order:        i,
			Kind:         st.Kind,
			Title:        st.Title,
			Instructions: st.Instructions,
			Status:       StepPending,
			URL:          st.URL,
			Method:       st.Method,
			Headers:      st.Headers,
			Body:         st.Body,
			Script:       st.Script,
			Command:      st.Command,
			TimeoutMs:    st.TimeoutMs,
		}
	}
	return out
}

// DeriveExecStatus computes the execution status from its step states:
// any failed step ⇒ failed; all steps terminal (completed|skipped) ⇒
// completed; otherwise running. Cancelled is set explicitly, never
// derived. Pure — unit-tested. Exported: the API runner composes it.
func DeriveExecStatus(states []StepState) string {
	if len(states) == 0 {
		return RunExecCompleted
	}
	allTerminal := true
	for _, s := range states {
		switch s.Status {
		case StepFailed:
			return RunExecFailed
		case StepCompleted, StepSkipped:
			// terminal-ok
		default:
			allTerminal = false
		}
	}
	if allTerminal {
		return RunExecCompleted
	}
	return RunExecRunning
}

// ApplyStepResult sets the status (+ optional by/note/output/error) on the
// matching step and stamps EndedAt. Returns the updated slice and whether
// the step was found. Pure — unit-tested. Exported: the API runner + the
// agent-result path both compose it.
func ApplyStepResult(states []StepState, stepID, status, by, note, output, errStr string, nowNs int64) ([]StepState, bool) {
	found := false
	for i := range states {
		if states[i].StepID == stepID {
			states[i].Status = status
			if by != "" {
				states[i].By = by
			}
			if note != "" {
				states[i].Note = note
			}
			if output != "" {
				states[i].Output = output
			}
			if errStr != "" {
				states[i].Error = errStr
			}
			states[i].EndedAt = nowNs
			found = true
			break
		}
	}
	return states, found
}

// ── Execution store (RMT(version), FINAL reads — mirrors runbooks) ──────

func (s *Store) StartExecution(ctx context.Context, rb Runbook, execID, startedBy, problemID string) (*RunbookExecution, error) {
	now := time.Now().UnixNano()
	exec := RunbookExecution{
		ID:            execID,
		RunbookID:     rb.ID,
		TitleSnapshot: rb.Title,
		Status:        RunExecRunning,
		StartedBy:     startedBy,
		StartedAt:     now,
		ProblemID:     problemID,
		StepStates:    snapshotSteps(rb.Steps),
		UpdatedAt:     now,
	}
	if len(exec.StepStates) == 0 {
		exec.Status = RunExecCompleted
		exec.CompletedAt = now
	}
	if err := s.UpsertExecution(ctx, exec); err != nil {
		return nil, err
	}
	return &exec, nil
}

func (s *Store) UpsertExecution(ctx context.Context, e RunbookExecution) error {
	statesJSON, err := json.Marshal(e.StepStates)
	if err != nil {
		return err
	}
	startedAt := time.Unix(0, e.StartedAt).UTC()
	completedAt := time.Unix(0, 0).UTC()
	if e.CompletedAt > 0 {
		completedAt = time.Unix(0, e.CompletedAt).UTC()
	}
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO runbook_executions
		(id, runbook_id, title_snapshot, status, started_by, started_at,
		 completed_at, problem_id, step_states_json, updated_at, version)`)
	if err != nil {
		return err
	}
	if err := batch.Append(e.ID, e.RunbookID, e.TitleSnapshot, e.Status,
		e.StartedBy, startedAt, completedAt, e.ProblemID, string(statesJSON),
		time.Now().UTC(), uint64(time.Now().UnixNano())); err != nil {
		return err
	}
	return batch.Send()
}

func (s *Store) GetExecution(ctx context.Context, id string) (*RunbookExecution, error) {
	var e RunbookExecution
	var statesJSON string
	err := s.conn.QueryRow(ctx, `
		SELECT id, runbook_id, title_snapshot, status, started_by,
		       toUnixTimestamp64Nano(started_at), toUnixTimestamp64Nano(completed_at),
		       problem_id, step_states_json
		FROM runbook_executions FINAL WHERE id = ? LIMIT 1`, id).
		Scan(&e.ID, &e.RunbookID, &e.TitleSnapshot, &e.Status, &e.StartedBy,
			&e.StartedAt, &e.CompletedAt, &e.ProblemID, &statesJSON)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(statesJSON), &e.StepStates)
	return &e, nil
}

// ExecutionFilter narrows the executions list.
type ExecutionFilter struct {
	RunbookID string
	Status    string
	ProblemID string
	SinceNs   int64 // v0.7.8 — >0 → started_at >= this (partition-prunes; see executionWhere)
	Limit     int
}

// executionWhere builds the WHERE clause for ListExecutions. Extracted as a
// pure helper so the v0.7.8 partition-prune predicate stays unit-testable.
//
// runbook_executions is the audit trail — no TTL, grows forever — and is
// ORDER BY id, so a status filter is NOT an index-prefix prune. The agent
// polls Status=running every 5s per pod; without a bound that FINAL-scans the
// whole all-time table every tick to find a handful of live runs (work grows
// O(total executions)). SinceNs adds `started_at >= ?`; with PARTITION BY
// toYYYYMM(started_at) that prunes the scan to recent partitions. The agent
// passes a 30-day window — generous enough to cover any run still legitimately
// progressing (incl. ones blocked on a slow human between a manual and an
// agent step); a run open longer than that is abandoned. The UI list leaves
// SinceNs=0 (no bound) so operators always see full history.
func executionWhere(f ExecutionFilter) whereClause {
	var wc whereClause
	if f.RunbookID != "" {
		wc.add("runbook_id = ?", f.RunbookID)
	}
	if f.Status != "" {
		wc.add("status = ?", f.Status)
	}
	if f.ProblemID != "" {
		wc.add("problem_id = ?", f.ProblemID)
	}
	if f.SinceNs > 0 {
		wc.add("started_at >= ?", time.Unix(0, f.SinceNs).UTC())
	}
	return wc
}

func (s *Store) ListExecutions(ctx context.Context, f ExecutionFilter) ([]RunbookExecution, error) {
	wc := executionWhere(f)
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.conn.Query(ctx, `
		SELECT id, runbook_id, title_snapshot, status, started_by,
		       toUnixTimestamp64Nano(started_at), toUnixTimestamp64Nano(completed_at),
		       problem_id, step_states_json
		FROM runbook_executions FINAL `+wc.sql()+`
		ORDER BY started_at DESC
		LIMIT ? SETTINGS max_execution_time = 10`, append(wc.args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunbookExecution
	for rows.Next() {
		var e RunbookExecution
		var statesJSON string
		if err := rows.Scan(&e.ID, &e.RunbookID, &e.TitleSnapshot, &e.Status,
			&e.StartedBy, &e.StartedAt, &e.CompletedAt, &e.ProblemID, &statesJSON); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(statesJSON), &e.StepStates)
		out = append(out, e)
	}
	return out, rows.Err()
}
