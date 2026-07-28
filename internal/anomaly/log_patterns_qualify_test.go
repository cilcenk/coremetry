package anomaly

import "testing"

// log_patterns_qualify_test.go — v0.9.327.
//
// Operator (prod): "Active anomalyler daha sıkı kuralları olsun, prodta çok
// daha az tetiklensin. Anomaly olmasa da şu an event oluşuyor."
//
// Measured before the change, over 24h of live events:
//
//	kind         n    p50 peak   min peak   p50 count   min count
//	log_pattern  109  12         2.15       3           3
//	trace_op      82   9         6          4           3
//
// The median event fired on THREE occurrences. The floor was the trigger.
// A 12× ratio over a baseline of a quarter-line per window is small-number
// arithmetic wearing a big multiplier — not a spike.
//
// Floors are numerically identical to the trace_op ones so the two detectors
// cannot disagree about what "enough to matter" means.
func TestQualifyLogPattern(t *testing.T) {
	cases := []struct {
		name      string
		cur       uint64
		base      float64 // already normalized to the current window
		wantKind  string  // "" = kalifiye değil
		wantRatio float64
	}{
		// ── count floor ──────────────────────────────────────────────────
		// 3 is exactly what prod was firing on; it must now be silent.
		{"ölçülen gürültü: yeni desen, 3 satır", 3, 0, "", 0},
		{"tabanın hemen altı: 9 satır", 9, 0, "", 0},
		{"yeni desen, tam eşikte", 10, 0, "new", 10},
		{"yeni desen, bol hacim", 250, 0, "new", 250},

		// The count floor binds on the spike branch too — that symmetry is
		// what kept drifting when the logic lived inside a closure.
		{"spike ama 4 satır (base 1 → 4×)", 4, 1, "", 0},
		{"spike, 12 satır base 1 → 12×", 12, 1, "spike", 12},

		// ── ratio floor ──────────────────────────────────────────────────
		{"2× artık yetmiyor: 20 satır base 10", 20, 10, "", 0},
		{"2.9× yetmiyor", 29, 10, "", 0},
		{"3.0× tam eşikte", 30, 10, "spike", 3},
		{"10×", 100, 10, "spike", 10},

		// Steady-state volume must NOT fire just because it is large: a
		// pattern running at its own baseline is normal, however loud.
		{"hacimli ama tabanında: 5000 satır base 5000", 5000, 5000, "", 0},

		{"hiç satır yok", 0, 0, "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, ratio := qualifyLogPattern(c.cur, c.base)
			if kind != c.wantKind {
				t.Fatalf("kind=%q bekleniyordu, got=%q (ratio %v)", c.wantKind, kind, ratio)
			}
			if kind != "" && ratio != c.wantRatio {
				t.Fatalf("ratio=%v bekleniyordu, got=%v", c.wantRatio, ratio)
			}
		})
	}
}

// The two detectors must agree on what "enough to matter" means. If one is
// loosened without the other, the queue fills from whichever side is laxer
// and the operator sees exactly the inconsistency this release removed.
func TestDetectorFloorsAgree(t *testing.T) {
	if logPatternMinCount != traceOpMinErrs {
		t.Errorf("count floors diverged: log=%d trace=%d", logPatternMinCount, traceOpMinErrs)
	}
	if logPatternMinRatio != traceOpMinRatio {
		t.Errorf("ratio floors diverged: log=%v trace=%v", logPatternMinRatio, traceOpMinRatio)
	}
}
