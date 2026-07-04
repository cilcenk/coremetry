package api

import (
	"testing"
	"time"
)

// v0.8.270 — ES-cost guard for /api/anomalies/log-patterns. The
// window rides the cache key; before the snap, every distinct
// ?window= value minted its own key and paid its own _msearch
// against the external ES cluster (the v0.5.187 key-cardinality
// class, expressed as backend load instead of poisoning). Pins the
// rung semantics: cover-don't-shrink, bounded top, tiny cardinality.
func TestSnapAnomalyWindow(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want time.Duration
	}{
		{30 * time.Second, 1 * time.Minute},   // sub-rung → smallest rung
		{1 * time.Minute, 1 * time.Minute},    // exact rung passes through
		{2 * time.Minute, 5 * time.Minute},    // covers, never shrinks
		{5 * time.Minute, 5 * time.Minute},    // the default
		{7 * time.Minute, 15 * time.Minute},
		{15 * time.Minute, 15 * time.Minute},
		{29 * time.Minute, 30 * time.Minute},
		{30 * time.Minute, 30 * time.Minute},
		{6 * time.Hour, 30 * time.Minute},     // capped — no long-range scans
		{0, 1 * time.Minute},                  // degenerate input still bounded
	}
	for _, c := range cases {
		if got := snapAnomalyWindow(c.in); got != c.want {
			t.Errorf("snapAnomalyWindow(%s) = %s, want %s", c.in, got, c.want)
		}
	}

	// The whole point: the reachable value set stays tiny.
	if len(anomalyWindowRungs) > 4 {
		t.Fatalf("rung set grew to %d — every rung is a distinct cache key paying its own ES _msearch batch", len(anomalyWindowRungs))
	}
}
