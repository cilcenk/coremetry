package chstore

import (
	"testing"
	"time"
)

// v0.9.105 (F1 display fidelity) — pixel-adaptive bucket step. Pins the
// Grafana model: step ≈ rangeSec/maxDataPoints snapped UP to the ladder, with
// a fixed-ladder fallback when px is unknown. The step drives bucket count →
// display resolution, so a drift here silently coarsens (or explodes) charts.
func TestMetricAutoStepPx(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	span := func(d time.Duration) (time.Time, time.Time) { return base, base.Add(d) }

	tests := []struct {
		name    string
		dur     time.Duration
		maxDP   int
		want    int
	}{
		// px unknown → fixed ladder (metricAutoStep, pre-F1 behavior)
		{"px0 → ladder 1h=30s", time.Hour, 0, 30},
		{"px0 → ladder 5m=5s", 5 * time.Minute, 0, 5},
		{"px0 → ladder 7d=1800s", 7 * 24 * time.Hour, 0, 1800},

		// px-adaptive: 1h(3600s)/1200 = 3 → snap 5s (vs ladder 30s = 6× finer)
		{"1h @ 1200px → 5s", time.Hour, 1200, 5},
		// 1h/720 = 5 → snap 5s
		{"1h @ 720px → 5s", time.Hour, 720, 5},
		// 6h(21600)/1200 = 18 → snap 20s (vs ladder 60s)
		{"6h @ 1200px → 20s", 6 * time.Hour, 1200, 20},
		// 5m(300)/1200 = 0.25 → ceil 1 → snap 1s (vs ladder 5s)
		{"5m @ 1200px → 1s", 5 * time.Minute, 1200, 1},
		// 24h(86400)/1200 = 72 → snap 120s
		{"24h @ 1200px → 120s", 24 * time.Hour, 1200, 120},
		// 2m(120)/1200 = 0.1 → 1s (never below 1)
		{"2m @ 1200px → 1s floor", 2 * time.Minute, 1200, 1},
		// narrow panel: 1h/300 = 12 → snap 15s
		{"1h @ 300px → 15s", time.Hour, 300, 15},
		// beyond ladder top: 60d/1 → huge raw → clamp to ladder max 86400
		{"60d @ 1px → ladder max", 60 * 24 * time.Hour, 1, 86400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			from, to := span(tt.dur)
			got := metricAutoStepPx(from, to, tt.maxDP)
			if got != tt.want {
				t.Errorf("metricAutoStepPx(%s, maxDP=%d) = %d; want %d", tt.dur, tt.maxDP, got, tt.want)
			}
		})
	}
}

// px-adaptive result must always snap to a ladder value (clean gridlines +
// bounded cache keys) and never go below 1s.
func TestMetricAutoStepPxSnapAndFloor(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	ladder := map[int]bool{}
	for _, s := range metricStepLadder {
		ladder[s] = true
	}
	for dp := 100; dp <= 4000; dp += 137 {
		for _, dur := range []time.Duration{time.Minute, time.Hour, 24 * time.Hour, 7 * 24 * time.Hour} {
			got := metricAutoStepPx(base, base.Add(dur), dp)
			if got < 1 {
				t.Fatalf("step < 1s: dur=%s dp=%d → %d", dur, dp, got)
			}
			if !ladder[got] {
				t.Fatalf("step not on ladder: dur=%s dp=%d → %d", dur, dp, got)
			}
		}
	}
}
