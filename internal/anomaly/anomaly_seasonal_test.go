package anomaly

import (
	"math"
	"testing"
)

// v0.8.46 (anomaly intelligence Phase 4) — time-of-day seasonal baseline. A
// flat 24h baseline mixes low off-peak/night traffic with the daytime peak,
// so the daily morning ramp looks anomalous every day (false positive) and a
// real off-peak dip hides. The seasonal baseline compares the current slot to
// the SAME time-of-day on prior days. These tests pin the baseline choice +
// prove the diurnal false positive disappears (the fetch itself is CH-bound +
// exercised live; the math is what's unit-tested).

func TestChooseBaseline(t *testing.T) {
	consec := []float64{1, 2, 3}
	// Enough seasonal samples → seasonal wins (same slice header returned).
	enough := []float64{10, 11, 12, 13} // len == seasonalMinSamples
	if got := chooseBaseline(enough, consec); &got[0] != &enough[0] {
		t.Error("with >= seasonalMinSamples seasonal samples, seasonal must be used")
	}
	// Too few → fall back to the consecutive baseline.
	if got := chooseBaseline([]float64{10, 11}, consec); &got[0] != &consec[0] {
		t.Error("with too few seasonal samples, must fall back to consecutive")
	}
	// Nil seasonal → consecutive.
	if got := chooseBaseline(nil, consec); &got[0] != &consec[0] {
		t.Error("nil seasonal must fall back to consecutive")
	}
}

func TestSeasonalBaseline_NoFalsePositiveOnDailyRamp(t *testing.T) {
	const current = 50.0 // a value that recurs at this slot every day

	// 24h-CONSECUTIVE baseline: mostly low off-peak/night traffic → low median,
	// tight spread → the recurring morning value looks like a huge spike.
	consecutive := make([]float64, 0, 280)
	for i := 0; i < 280; i++ {
		consecutive = append(consecutive, 8+float64(i%3)) // 8,9,10 …
	}
	cm, cmad := medianMAD(consecutive)
	if cmad < 1e-9 {
		t.Fatal("degenerate consecutive baseline")
	}
	consecZ := math.Abs(madScale * (current - cm) / cmad)
	if consecZ < openZ {
		t.Fatalf("precondition: flat-24h baseline should make the daily ramp look anomalous; z=%.2f", consecZ)
	}

	// SEASONAL baseline: the SAME morning slot over the last 7 days, all ~50.
	seasonal := []float64{48, 50, 52, 49, 51, 50, 48}
	if got := chooseBaseline(seasonal, consecutive); &got[0] != &seasonal[0] {
		t.Fatal("chooseBaseline should select the seasonal samples here")
	}
	sm, smad := medianMAD(seasonal)
	seasZ := math.Abs(madScale * (current - sm) / smad)
	if seasZ >= openZ {
		t.Errorf("seasonal baseline must NOT fire on the daily ramp; z=%.2f (median=%.1f mad=%.1f)", seasZ, sm, smad)
	}
}
