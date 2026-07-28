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
		// v0.9.332 — this used to read "never resolve (guard)". It is still
		// false HERE, but only because the compat wrapper passes startedAt ==
		// now, i.e. the incident was created this instant and its problem has
		// not bound yet. Past the grace window the answer flips — see
		// TestIncidentCascadeOrphan.
		{"no attached problems, just created → keep open (create→bind gap)", 0, 0, 0, false, 0},
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

// v0.9.332 — operator (prod): "Çok fazla incident var, yanıltıcı oluyor."
//
// Measured locally at the time of the fix: 32 open incidents, 26 of them with
// NO row in incident_problems, the oldest four days old. Two layers made them
// immortal:
//
//  1. OpenIncidentRollups joined incident_problems and problems with INNER
//     JOINs, so an incident with nothing attached produced no rollup row at
//     all — the cascade never even considered it.
//  2. Even if it had, problemCount == 0 returned "never resolve".
//
// So they accumulated forever and came to dominate the triage queue. The
// grace window is what keeps the fix safe: AttachProblemToIncident creates the
// incident and binds the problem in two separate statements, and between them
// a rollup honestly sees zero.
func TestIncidentCascadeOrphan(t *testing.T) {
	const hour = int64(3_600_000_000_000) // 1h in ns
	now := int64(10) * hour

	tests := []struct {
		name         string
		problemCount int
		unresolved   int
		maxResolved  int64
		startedAt    int64
		wantResolve  bool
		wantEnded    int64
	}{
		// The create→bind gap: seconds old, nothing attached yet. Resolving
		// here would close every incident the instant it opened.
		{"orphan, seconds old → keep open", 0, 0, 0, now - 5, false, 0},
		{"orphan, just under the grace window → keep open", 0, 0, 0, now - hour + 1, false, 0},
		// Past the window, "not attached yet" stops being an honest
		// explanation. `now` is the only end time available: an incident that
		// never had a problem has no problem-clear timestamp.
		{"orphan, past the grace window → resolve at now", 0, 0, 0, now - hour, true, now},
		{"orphan, four days old → resolve", 0, 0, 0, now - 96*hour, true, now},
		// Missing startedAt must NOT be read as "infinitely old" — an unknown
		// age is not evidence that the incident is stale.
		{"orphan with no startedAt → keep open", 0, 0, 0, 0, false, 0},
		// An attached-but-unresolved problem still holds it open regardless
		// of age: the orphan rule must not leak into the normal path.
		{"old incident with a live problem → keep open", 2, 1, 0, now - 96*hour, false, 0},
		// The v0.7.33 behaviour is unchanged where problems exist.
		{"attached and all cleared → resolve at last clear", 3, 0, now - hour, now - 2*hour, true, now - hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotResolve, gotEnded := incidentCascadeDecisionAt(
				tt.problemCount, tt.unresolved, tt.maxResolved, tt.startedAt, now)
			if gotResolve != tt.wantResolve || gotEnded != tt.wantEnded {
				t.Errorf("= (%v,%d), want (%v,%d)", gotResolve, gotEnded, tt.wantResolve, tt.wantEnded)
			}
		})
	}
}
