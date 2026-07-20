package chstore

import "testing"

// TestClampHistogramStep pins the v0.9.114 memory guard (PromQL Phase-2 review
// CRITICAL): a caller-pinned tiny step over a wide window must be raised so the
// Go-side accum[nTime][nBuckets] allocation stays bounded. The attack was
// ?step=1&from=<years-ago> → nTime ~1e9 → OOM of the single binary.
func TestClampHistogramStep(t *testing.T) {
	cases := []struct {
		name    string
		spanSec float64
		stepSec int
		want    int
	}{
		{"1h/60s safe", 3600, 60, 60},
		{"1h/1s under cap (3600≤5000)", 3600, 1, 1},
		{"1d/1s over cap → raised", 86400, 1, 18}, // ceil(86400/5000)=18
		{"1y/1s over cap → raised", 365 * 24 * 3600, 1, 6308},
		{"55y/1s (from=1 attack) → raised", 1.75e9, 1, 350000},
		{"zero span unchanged", 0, 60, 60},
		{"auto step (0) untouched", 3600, 0, 0},
		{"auto step (neg) untouched", 3600, -5, -5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := clampHistogramStep(c.spanSec, c.stepSec)
			if got != c.want {
				t.Errorf("clampHistogramStep(%g, %d) = %d, want %d", c.spanSec, c.stepSec, got, c.want)
			}
			// Invariant: after clamping (for a real span + step), nTime ≤ cap.
			if c.spanSec > 0 && got > 0 {
				if nTime := int(c.spanSec/float64(got)) + 1; nTime > maxHistogramBuckets+1 {
					t.Errorf("post-clamp nTime %d exceeds cap %d", nTime, maxHistogramBuckets)
				}
			}
		})
	}
}
