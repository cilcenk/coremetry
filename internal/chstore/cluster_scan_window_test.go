package chstore

import (
	"testing"
	"time"
)

// v0.8.188 — operator-reported: the `clusters` cache warmer logged
// `code: 159 ... Timeout exceeded` every tick on the external
// Distributed cluster. Root cause: ListClusters scanned a ~24h window
// of raw spans, and with cluster_name unset (no materialized `cluster`
// column) clusterExpr() falls to the per-row res/attr indexOf derive —
// that derive over 24h of a billion-span/day cluster blows
// max_execution_time = 8. The fix clamps the enumeration scan to the
// most recent clusterScanWindow regardless of the caller's range
// (warmer asks for 24h; the live /api/clusters handler defaults to 24h
// too). This guards the clamp so a future widening can't silently
// re-introduce the unbounded scan.
func TestClampClusterFrom(t *testing.T) {
	to := time.Date(2026, 6, 16, 11, 20, 0, 0, time.UTC)
	earliest := to.Add(-clusterScanWindow)

	cases := []struct {
		name string
		from time.Time
		want time.Time
	}{
		{
			// The warmer (from.Add(-23h) over a to-1h base) and the
			// live handler's 24h default both land here — this is the
			// branch that timed out in prod.
			name: "24h window clamps to the recent bound",
			from: to.Add(-24 * time.Hour),
			want: earliest,
		},
		{
			name: "6h window clamps to the recent bound",
			from: to.Add(-6 * time.Hour),
			want: earliest,
		},
		{
			name: "exactly one window is left untouched",
			from: earliest,
			want: earliest,
		},
		{
			name: "narrow window is left untouched",
			from: to.Add(-15 * time.Minute),
			want: to.Add(-15 * time.Minute),
		},
		{
			// Degenerate from > to (shouldn't happen, but must not
			// produce a window wider than the cap).
			name: "from after to is left untouched",
			from: to.Add(5 * time.Minute),
			want: to.Add(5 * time.Minute),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := clampClusterFrom(c.from, to)
			if !got.Equal(c.want) {
				t.Fatalf("clampClusterFrom(%v, %v) = %v, want %v", c.from, to, got, c.want)
			}
			// Invariant the timeout fix depends on: the scanned window
			// is never wider than clusterScanWindow.
			if to.Sub(got) > clusterScanWindow {
				t.Fatalf("scan window %v exceeds cap %v", to.Sub(got), clusterScanWindow)
			}
		})
	}
}
