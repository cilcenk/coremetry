package anomaly

import "testing"

// v0.8.43 (anomaly intelligence Phase 2) — directional thresholds + relative
// magnitude floor. A symmetric |z| opened a "critical" anomaly on a 3σ p99
// DROP (good news), and one absolute floor couldn't fit error_rate(%),
// p99(ms) and rps. decideAnomaly is now metric-aware (direction + floorPct +
// direction-aware severity). These tests pin both directions + the floor.

func TestDecideAnomaly_Directional(t *testing.T) {
	cases := []struct {
		name                string
		metric              string
		z, current, median  float64
		wantOpen            bool
		wantSeverity        string
		wantDirection       string
	}{
		// p99 is "up"-only: a 3σ DROP is good news → must NOT open.
		{"p99 drop ignored", "p99_ms", -4.0, 50, 100, false, "", ""},
		{"p99 spike opens", "p99_ms", 4.0, 200, 100, true, "warning", "spiked"},
		{"p99 huge spike critical", "p99_ms", 6.0, 400, 100, true, "critical", "spiked"},
		// error_rate "up"-only.
		{"error_rate spike opens", "error_rate", 3.5, 5, 1, true, "warning", "spiked"},
		{"error_rate drop ignored", "error_rate", -3.5, 0.1, 1, false, "", ""},
		// request_rate "both": spike → warning, drop → critical (traffic loss).
		{"rps spike opens warning", "request_rate", 3.5, 1500, 1000, true, "warning", "spiked"},
		{"rps drop opens critical", "request_rate", -3.5, 500, 1000, true, "critical", "dropped"},
		// Relative floor: a 3σ move that's proportionally tiny must NOT open
		// (error_rate 1.00 → 1.05 = 5% < 10% floor, even at z=4).
		{"below relative floor", "error_rate", 4.0, 1.05, 1.0, false, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := decideAnomaly(c.metric, c.z, c.current, c.median)
			if d.open != c.wantOpen {
				t.Fatalf("open=%v, want %v", d.open, c.wantOpen)
			}
			if c.wantOpen {
				if d.severity != c.wantSeverity {
					t.Errorf("severity=%q, want %q", d.severity, c.wantSeverity)
				}
				if d.direction != c.wantDirection {
					t.Errorf("direction=%q, want %q", d.direction, c.wantDirection)
				}
			}
		})
	}
}

func TestResolvedFor_Directional(t *testing.T) {
	// "up" metric: resolved once z falls back to/below resolveZ — including a
	// drop into negative z (an up-only anomaly is over once it's not high).
	if !resolvedFor("p99_ms", 1.0) {
		t.Error("p99 z=1.0 should be resolved (<= resolveZ)")
	}
	if resolvedFor("p99_ms", 2.0) {
		t.Error("p99 z=2.0 should NOT be resolved (> resolveZ)")
	}
	if !resolvedFor("p99_ms", -3.0) {
		t.Error("p99 z=-3.0 (a drop) should be resolved for an up-only metric")
	}
	// "both" metric: resolved only inside the band on both sides.
	if !resolvedFor("request_rate", 1.0) {
		t.Error("rps z=1.0 should be resolved")
	}
	if resolvedFor("request_rate", -2.0) {
		t.Error("rps z=-2.0 should NOT be resolved (|z| > resolveZ)")
	}
}

func TestPolicyFor_DefaultsToBoth(t *testing.T) {
	p := policyFor("some_unmapped_metric")
	if p.direction != "both" || p.floorPct <= 0 {
		t.Errorf("default policy = %+v, want {both, >0}", p)
	}
}
