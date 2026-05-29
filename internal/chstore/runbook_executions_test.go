package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.7.0 — Runbook execution state machine. snapshotSteps freezes the
// template at start (audit integrity); DeriveExecStatus + ApplyStepResult
// drive the runner. Table-driven so the lifecycle can't silently regress —
// a wrong status would leave a run stuck or falsely "completed" in the
// audit trail.

func TestSnapshotSteps(t *testing.T) {
	steps := []RunbookStep{
		{ID: "s1", Kind: "manual", Title: "a"},
		{ID: "s2", Kind: "http", Title: "b"},
	}
	got := snapshotSteps(steps)
	if len(got) != 2 {
		t.Fatalf("len=%d, want 2", len(got))
	}
	for i, ss := range got {
		if ss.Status != StepPending {
			t.Errorf("step %d status=%q, want pending", i, ss.Status)
		}
		if ss.Order != i {
			t.Errorf("step %d order=%d, want %d", i, ss.Order, i)
		}
	}
	if got[0].StepID != "s1" || got[1].Kind != "http" {
		t.Errorf("snapshot mismatch: %+v", got)
	}
	if len(snapshotSteps(nil)) != 0 {
		t.Error("nil steps should snapshot to empty")
	}

	// v0.7.6 regression — empty-id steps (e.g. created via the API without
	// ids) must get unique, non-empty snapshot ids. Otherwise ApplyStepResult
	// (matched by stepId) writes every result onto the first empty-id step,
	// so one step shows another's output and the rest stick pending. (This is
	// the bug behind "I ran `date` but saw no output".)
	noid := snapshotSteps([]RunbookStep{{Kind: "bash"}, {Kind: "http"}})
	if noid[0].StepID == "" || noid[1].StepID == "" {
		t.Fatal("empty-id steps must get assigned snapshot ids")
	}
	if noid[0].StepID == noid[1].StepID {
		t.Fatalf("assigned snapshot ids must be unique, got %q twice", noid[0].StepID)
	}
}

func TestDeriveExecStatus(t *testing.T) {
	cases := []struct {
		name string
		in   []StepState
		want string
	}{
		{"empty completes", nil, RunExecCompleted},
		{"all pending = running", []StepState{{Status: StepPending}, {Status: StepPending}}, RunExecRunning},
		{"one pending = running", []StepState{{Status: StepCompleted}, {Status: StepPending}}, RunExecRunning},
		{"all terminal = completed", []StepState{{Status: StepCompleted}, {Status: StepSkipped}}, RunExecCompleted},
		{"any failed = failed", []StepState{{Status: StepCompleted}, {Status: StepFailed}, {Status: StepPending}}, RunExecFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DeriveExecStatus(c.in); got != c.want {
				t.Fatalf("DeriveExecStatus = %q, want %q", got, c.want)
			}
		})
	}
}

func TestApplyStepResult(t *testing.T) {
	states := []StepState{{StepID: "s1", Status: StepPending}, {StepID: "s2", Status: StepPending}}
	out, ok := ApplyStepResult(states, "s2", StepCompleted, "alice", "looks good", "", "", 12345)
	if !ok {
		t.Fatal("step s2 should be found")
	}
	if out[1].Status != StepCompleted || out[1].By != "alice" || out[1].Note != "looks good" || out[1].EndedAt != 12345 {
		t.Errorf("s2 not updated correctly: %+v", out[1])
	}
	if out[0].Status != StepPending {
		t.Error("s1 should be untouched")
	}
	if _, ok := ApplyStepResult(states, "nope", StepCompleted, "", "", "", "", 1); ok {
		t.Error("unknown stepID should return ok=false")
	}
}

// v0.7.8 regression — the agent polls ListExecutions(Status=running) every 5s.
// runbook_executions is ORDER BY id with no TTL (the audit trail), so a status
// filter scans the whole forever-growing table under FINAL. SinceNs must emit
// `started_at >= ?` so PARTITION BY toYYYYMM(started_at) prunes the poll to
// recent partitions — but ONLY when set, so the UI's full-history list
// (SinceNs=0) stays unbounded. A regression that dropped the predicate would
// silently reintroduce the O(all-time-rows) scan.
func TestExecutionWhere(t *testing.T) {
	const startedPred = "started_at >= ?"
	cases := []struct {
		name      string
		f         ExecutionFilter
		wantConds []string // substrings that MUST appear in the WHERE sql
		wantArgs  int
		noStarted bool // started_at predicate must be ABSENT
	}{
		{"empty = no where", ExecutionFilter{}, nil, 0, true},
		{"status only, no time bound", ExecutionFilter{Status: "running"}, []string{"status = ?"}, 1, true},
		{"agent poll prunes by started_at", ExecutionFilter{Status: "running", SinceNs: 1}, []string{"status = ?", startedPred}, 2, false},
		{"runbook + problem + since", ExecutionFilter{RunbookID: "rb1", ProblemID: "p1", SinceNs: 1}, []string{"runbook_id = ?", "problem_id = ?", startedPred}, 3, false},
		{"SinceNs<=0 stays unbounded (UI history)", ExecutionFilter{RunbookID: "rb1", SinceNs: 0}, []string{"runbook_id = ?"}, 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wc := executionWhere(c.f)
			sql := wc.sql()
			for _, want := range c.wantConds {
				if !strings.Contains(sql, want) {
					t.Errorf("sql %q missing %q", sql, want)
				}
			}
			if len(wc.args) != c.wantArgs {
				t.Errorf("args=%d, want %d (sql=%q)", len(wc.args), c.wantArgs, sql)
			}
			if c.noStarted && strings.Contains(sql, startedPred) {
				t.Errorf("started_at predicate must be absent for %+v, got %q", c.f, sql)
			}
			if len(c.wantConds) == 0 && sql != "" {
				t.Errorf("empty filter must produce empty WHERE, got %q", sql)
			}
		})
	}

	// The agent's 30-day window must resolve to a real, recent lower bound
	// (not the zero time, which would scan all partitions).
	wc := executionWhere(ExecutionFilter{Status: "running", SinceNs: time.Now().Add(-30 * 24 * time.Hour).UnixNano()})
	bound, ok := wc.args[len(wc.args)-1].(time.Time)
	if !ok {
		t.Fatalf("started_at arg should be time.Time, got %T", wc.args[len(wc.args)-1])
	}
	if bound.IsZero() || time.Since(bound) < 29*24*time.Hour || time.Since(bound) > 31*24*time.Hour {
		t.Errorf("30-day window resolved to %v (since=%v), want ~30d ago", bound, time.Since(bound))
	}
}
