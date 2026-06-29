package main

import "testing"

// Drift fix — the Oracle receiver metrics stayed flat (read only the static
// inst.load, never the load model), so at the 03:00 trough Oracle still emitted
// peak throughput and the oracle-row-lock-contention incident moved the traces
// but not the oracledb deadlock/wait metrics. oracleDeadlockMult is the pure
// piece that re-couples deadlocks to the model. Pin: baseline is 1.0, any
// incident's error-bump raises it, and the row-lock incident DOMINATES — even
// over a higher-error-bump incident — because deadlocks are ITS symptom.
func TestOracleDeadlockMult(t *testing.T) {
	const eps = 1e-9

	if m := oracleDeadlockMult(0, false); m < 1-eps || m > 1+eps {
		t.Fatalf("baseline (no incident) = %v, want 1.0", m)
	}

	// Monotonic in error-bump for a non-thematic incident.
	low := oracleDeadlockMult(0.04, false)  // jvm-gc-pause-storm
	high := oracleDeadlockMult(0.18, false) // downstream-dependency-degraded
	if !(high > low && low > 1) {
		t.Fatalf("error-bump must raise the deadlock mult monotonically: low=%v high=%v", low, high)
	}

	// Thematic correctness: oracle-row-lock-contention (errBump 0.10, rowLock)
	// must drive deadlocks HARDER than the higher-errBump downstream incident
	// (errBump 0.18, not rowLock) — deadlocks are the row-lock symptom.
	rowLock := oracleDeadlockMult(0.10, true)
	downstream := oracleDeadlockMult(0.18, false)
	if rowLock <= downstream {
		t.Fatalf("row-lock-contention (%v) must dominate downstream-degraded (%v) for deadlocks",
			rowLock, downstream)
	}
}
