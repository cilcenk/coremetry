package chstore

import "testing"

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
