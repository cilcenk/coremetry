package chstore

import (
	"encoding/json"
	"testing"
)

// v0.9.247 regression pin — the anomaly promoter's critical cut-off used to
// be a hard-coded `peakRatio >= 20` in the evaluator. It sat invisibly ABOVE
// the configurable promotion gate, which made tightening backfire: an
// operator raising MinPeakRatio to 20+ to get FEWER pages silently turned
// every surviving promotion into a critical one.
//
// Two things must hold forever after:
//  1. A config row written BEFORE this field existed must read back as 20,
//     not 0 — a zero would make every promoted anomaly critical, the exact
//     failure this change removes.
//  2. CriticalPeakRatio can never end up below MinPeakRatio, however the
//     value arrives.
//
// applyPromotionDefaults mirrors GetAnomalyPromotion's patch block so the
// rules are testable without a live ClickHouse.
func applyPromotionDefaults(c AnomalyPromotionConfig) AnomalyPromotionConfig {
	d := DefaultAnomalyPromotion()
	if c.MinPeakRatio <= 0 {
		c.MinPeakRatio = d.MinPeakRatio
	}
	if c.MinSustainedSec <= 0 {
		c.MinSustainedSec = d.MinSustainedSec
	}
	if c.MinCount == 0 {
		c.MinCount = d.MinCount
	}
	if c.CriticalPeakRatio <= 0 {
		c.CriticalPeakRatio = d.CriticalPeakRatio
	}
	if c.CriticalPeakRatio < c.MinPeakRatio {
		c.CriticalPeakRatio = c.MinPeakRatio
	}
	return c
}

func TestAnomalyPromotionCriticalRatio(t *testing.T) {
	t.Run("pre-v0.9.247 row inherits the historical 20", func(t *testing.T) {
		// Exactly what a config saved before this field existed looks
		// like on disk — no criticalPeakRatio key at all.
		var c AnomalyPromotionConfig
		legacy := `{"enabled":true,"minPeakRatio":12,"minSustainedSec":300,"minCount":100}`
		if err := json.Unmarshal([]byte(legacy), &c); err != nil {
			t.Fatalf("unmarshal legacy row: %v", err)
		}
		got := applyPromotionDefaults(c)
		if got.CriticalPeakRatio != 20 {
			t.Errorf("CriticalPeakRatio = %v, want 20 (historical hard-coded cut-off)", got.CriticalPeakRatio)
		}
		// The operator's other values must survive untouched.
		if got.MinPeakRatio != 12 || got.MinSustainedSec != 300 || got.MinCount != 100 {
			t.Errorf("legacy values mutated: %+v", got)
		}
	})

	t.Run("never lands below the promotion gate", func(t *testing.T) {
		for _, tc := range []struct {
			name            string
			minR, critR     float64
			wantCritAtLeast float64
		}{
			{"critical below gate is clamped up", 30, 10, 30},
			{"critical equal to gate is allowed", 30, 30, 30},
			{"critical above gate is kept", 12, 25, 25},
			{"zero critical falls back to 20", 12, 0, 20},
			// A gate ABOVE the 20 default must drag the fallback up with
			// it — otherwise the clamp would silently reproduce the old
			// "everything is critical" behaviour at gate=25.
			{"gate above default drags fallback up", 25, 0, 25},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got := applyPromotionDefaults(AnomalyPromotionConfig{
					MinPeakRatio: tc.minR, CriticalPeakRatio: tc.critR,
				})
				if got.CriticalPeakRatio != tc.wantCritAtLeast {
					t.Errorf("CriticalPeakRatio = %v, want %v", got.CriticalPeakRatio, tc.wantCritAtLeast)
				}
				if got.CriticalPeakRatio < got.MinPeakRatio {
					t.Errorf("critical %v < gate %v — every promotion would page",
						got.CriticalPeakRatio, got.MinPeakRatio)
				}
			})
		}
	})

	t.Run("default keeps the shipped severity split", func(t *testing.T) {
		d := DefaultAnomalyPromotion()
		if d.CriticalPeakRatio != 20 {
			t.Errorf("default CriticalPeakRatio = %v, want 20", d.CriticalPeakRatio)
		}
		if d.CriticalPeakRatio <= d.MinPeakRatio {
			t.Errorf("default must leave a warning band: gate %v, critical %v",
				d.MinPeakRatio, d.CriticalPeakRatio)
		}
	})

	t.Run("round-trips through JSON", func(t *testing.T) {
		in := applyPromotionDefaults(AnomalyPromotionConfig{MinPeakRatio: 12, CriticalPeakRatio: 40})
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var out AnomalyPromotionConfig
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if out.CriticalPeakRatio != 40 {
			t.Errorf("round-trip lost the value: %v", out.CriticalPeakRatio)
		}
	})
}
