// impact_window_test.go — v0.9.365 regression. GetServiceRollouts returns
// buckets ASC (oldest first); the impact loop capped at maxImpact from the
// HEAD, so with >8 rollouts in the window the newest rows — the ones the
// Details strip and DeployHistoryPanel actually surface — always rendered
// "—" deltas. impactStart pins the newest-window arithmetic.
package api

import "testing"

func TestImpactStart(t *testing.T) {
	cases := []struct {
		name string
		n    int
		cap  int
		want int
	}{
		{"empty", 0, 8, 0},
		{"fewer than cap", 3, 8, 0},
		{"exactly cap", 8, 8, 0},
		{"one over cap → oldest dropped", 9, 8, 1},
		{"7d window, twice-daily deploys", 14, 8, 6},
		{"cap 1 keeps only newest", 5, 1, 4},
	}
	for _, c := range cases {
		if got := impactStart(c.n, c.cap); got != c.want {
			t.Errorf("%s: impactStart(%d,%d) = %d, want %d", c.name, c.n, c.cap, got, c.want)
		}
	}
}
