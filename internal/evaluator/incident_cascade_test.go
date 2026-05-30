package evaluator

import "testing"

// v0.7.33 — Operator-reported: Problems auto-resolve but Incidents stay open
// forever (CH ground truth: 214 problems resolved / 0 open, yet 57 incidents
// open / 0 resolved). cascadeResolveIncidents closes an incident once every
// attached problem has cleared, ending it at the last clear time so the
// started→ended interval reflects the real impact window. incidentCascadeDecision
// is the pure core; this table-driven test pins it (CLAUDE.md #11).
func TestIncidentCascadeDecision(t *testing.T) {
	const now = int64(1_000)
	tests := []struct {
		name         string
		problemCount int
		unresolved   int
		maxResolved  int64
		wantResolve  bool
		wantEnded    int64
	}{
		{"all cleared → resolve at last clear", 3, 0, 900, true, 900},
		{"one still open → keep open", 3, 1, 800, false, 0},
		{"all open → keep open", 2, 2, 0, false, 0},
		{"no attached problems → never resolve (guard)", 0, 0, 0, false, 0},
		{"cleared but no clear timestamp → end at now", 1, 0, 0, true, now},
		{"single problem cleared → resolve", 1, 0, 500, true, 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotResolve, gotEnded := incidentCascadeDecision(tt.problemCount, tt.unresolved, tt.maxResolved, now)
			if gotResolve != tt.wantResolve || gotEnded != tt.wantEnded {
				t.Errorf("incidentCascadeDecision(%d,%d,%d,now=%d) = (%v,%d), want (%v,%d)",
					tt.problemCount, tt.unresolved, tt.maxResolved, now,
					gotResolve, gotEnded, tt.wantResolve, tt.wantEnded)
			}
		})
	}
}
