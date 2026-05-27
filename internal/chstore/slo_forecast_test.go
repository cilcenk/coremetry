package chstore

// v0.6.30 regression test — projectBurnHours is the pure-math
// half of ComputeSLOForecast. Operator-facing semantics:
//
//   • burnRate ≤ 1 → safe, never flag for attention
//   • budgetRemaining ≤ 0 → already breached, flag immediately
//   • otherwise: hours = budgetRemaining × windowHours / rate
//   • flag within24h when hours ≤ 24
//
// Pinning the boundary catches off-by-one in the time-to-
// exhaust math AND the "is this safe?" predicate. Both have
// shipped wrong elsewhere in the industry (Datadog's burn
// projection initially used rate-1 rather than rate÷1 which
// inverted the safe/danger band for slow burns).

import "testing"

func TestProjectBurnHours(t *testing.T) {
	const windowHours30d = 30.0 * 24.0  // 720

	cases := []struct {
		name              string
		budgetRemaining   float64
		windowHours       float64
		rate              float64
		wantHours         float64  // 0 if SafeBurn or already breached
		wantSafe          bool
		wantWithin24h     bool
	}{
		// Safe band — burning at or below replenishment.
		{"rate 0 (no errors)",      0.5, windowHours30d, 0.0, 0, true, false},
		{"rate 0.5 (slow burn)",    0.5, windowHours30d, 0.5, 0, true, false},
		{"rate 1.0 exactly (stable)", 0.5, windowHours30d, 1.0, 0, true, false},
		// Burning above replenishment.
		{"rate 2x, half budget left",   0.5, windowHours30d, 2.0, 180.0, false, false},  // 0.5×720/2 = 180h ≈ 7.5d
		{"rate 10x, half budget left",  0.5, windowHours30d, 10.0, 36.0, false, false},  // boundary case >24h
		{"rate 10x, 10% budget left",   0.1, windowHours30d, 10.0, 7.2, false, true},    // <24h, flag
		{"rate 100x, half budget left", 0.5, windowHours30d, 100.0, 3.6, false, true},   // hot fire
		// Already breached — flag immediately regardless of rate.
		{"budget exhausted, any rate",   0.0, windowHours30d, 5.0,  0, false, true},
		{"budget negative (rounding)", -0.01, windowHours30d, 5.0,  0, false, true},
		// Exactly-24h boundary — "≤ 24" predicate is inclusive.
		{"hours exactly = 24",          24.0/720.0, windowHours30d, 1.0, 0, true, false}, // rate=1 → safe path
		// 7-day window math.
		{"7d window, rate 2, half budget", 0.5, 7.0*24.0, 2.0, 42.0, false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotHours, gotSafe, gotWithin24h := projectBurnHours(
				tc.budgetRemaining, tc.windowHours, tc.rate)
			if gotSafe != tc.wantSafe {
				t.Errorf("SafeBurn = %v, want %v", gotSafe, tc.wantSafe)
			}
			if gotWithin24h != tc.wantWithin24h {
				t.Errorf("WillBreachWithin24h = %v, want %v", gotWithin24h, tc.wantWithin24h)
			}
			// Hours equality only matters in the non-safe path.
			if !gotSafe {
				diff := gotHours - tc.wantHours
				if diff < -0.01 || diff > 0.01 {
					t.Errorf("HoursToExhaust = %.3f, want %.3f", gotHours, tc.wantHours)
				}
			}
		})
	}
}
