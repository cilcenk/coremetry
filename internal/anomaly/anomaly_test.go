package anomaly

import (
	"math"
	"testing"
)

// v0.8.42 (anomaly intelligence Phase 1) — checkOne switched from mean +
// population stdev to a modified z-score (median + MAD). The mean and the
// population stdev are each dragged by their OWN outliers, so a single
// contaminated baseline bucket (e.g. yesterday's spike) inflates the stdev
// and MASKS a real current spike (z shrinks below openZ). Median + MAD are
// outlier-robust. These tests pin the masking fix + the helper's correctness.

func TestMedianMAD(t *testing.T) {
	cases := []struct {
		name             string
		xs               []float64
		wantMed, wantMAD float64
	}{
		{"odd", []float64{1, 2, 3, 4, 5}, 3, 1},  // |dev|=2,1,0,1,2 → MAD=median=1
		{"even", []float64{1, 2, 3, 4}, 2.5, 1},  // dev=1.5,.5,.5,1.5 → MAD=1
		{"constant", []float64{7, 7, 7}, 7, 0},   // robust analogue of stdev=0
		{"empty", nil, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			med, mad := medianMAD(c.xs)
			if med != c.wantMed || mad != c.wantMAD {
				t.Errorf("medianMAD(%v) = (%v, %v), want (%v, %v)", c.xs, med, mad, c.wantMed, c.wantMAD)
			}
		})
	}
}

func TestMedianMAD_DoesNotMutateInput(t *testing.T) {
	xs := []float64{5, 1, 3, 2, 4}
	medianMAD(xs)
	if xs[0] != 5 || xs[1] != 1 {
		t.Errorf("medianMAD mutated its input slice: %v", xs)
	}
}

// The acceptance criterion from the task: a baseline contaminated by one
// outlier bucket must NOT mask a real current spike under the modified z.
func TestModifiedZScore_NotMaskedByContaminatedBaseline(t *testing.T) {
	// Clean baseline jittering tightly around 10 (median 10, MAD 2) plus ONE
	// contaminated bucket at 80 (yesterday's spike). current=30 is a real
	// spike the operator wants flagged.
	baseline := make([]float64, 0, 31)
	for i := 0; i < 10; i++ {
		baseline = append(baseline, 8, 10, 12)
	}
	baseline = append(baseline, 80) // the contaminant
	const current = 30.0

	// Classic mean + population stdev: the 80 inflates stdev → spike MASKED.
	mean, stdev := meanStdev(baseline)
	classicZ := math.Abs((current - mean) / stdev)
	if classicZ >= openZ {
		t.Fatalf("precondition: classic z=%.2f should be masked (<openZ=%.1f) by the contaminated baseline", classicZ, openZ)
	}

	// Modified z (median + MAD): the contaminant doesn't move median/MAD, so
	// the spike clears openZ — exactly what checkOne now computes.
	median, mad := medianMAD(baseline)
	if mad < 1e-9 {
		t.Fatal("MAD unexpectedly ~0; the baseline should have spread")
	}
	modZ := math.Abs(madScale * (current - median) / mad)
	if modZ < openZ {
		t.Errorf("modified z=%.2f must clear openZ=%.1f — the real spike must NOT be masked (median=%.1f mad=%.1f)",
			modZ, openZ, median, mad)
	}
}

// v0.9.48 — düz-baseline kör noktası: MAD=0 servisler (tarihi hiç
// hata görmemiş) skip ediliyordu; %0→%30 hata Problem AÇMIYORDU
// (operatör vakası). flatMADFloor MAD'e taban koyar; bu tablo hem
// taban değerlerini hem de floored-MAD ile evalWindow'un açık/sessiz
// kararlarını pinler.
func TestFlatBaselineFloor(t *testing.T) {
	if got := flatMADFloor("error_rate", 0); got != 0.5 {
		t.Errorf("error_rate taban 0.5 olmalı, got %v", got)
	}
	if got := flatMADFloor("p99_ms", 2); got != 1 {
		t.Errorf("p99_ms küçük medyanda 1ms tabanı, got %v", got)
	}
	if got := flatMADFloor("p99_ms", 500); got != 25 {
		t.Errorf("p99_ms 500ms medyanda %%5 = 25, got %v", got)
	}
	if got := flatMADFloor("request_rate", 0); got != 0.1 {
		t.Errorf("request_rate taban 0.1, got %v", got)
	}

	mad := flatMADFloor("error_rate", 0)
	cases := []struct {
		name     string
		window   []float64
		wantOpen bool
		wantSev  string
	}{
		{"%0 → sürekli %30: critical açılır", []float64{30, 30, 30}, true, "critical"},
		// v0.9.193 P1-only: %3 (z≈4, warning-grade) artık AÇILMAZ; flat-0
		// tabanda critical eşiği ≈ %4.45 (0.5 taban × 6σ / 0.6745).
		{"%0 → sürekli %3: artık sessiz (P1-only)", []float64{3, 3, 3}, false, ""},
		{"%0 → sürekli %5: critical açılır", []float64{5, 5, 5}, true, "critical"},
		{"%0 → %1 kıpırtısı: sessiz", []float64{1, 1, 1}, false, ""},
		{"tek bucket blip: dwell açtırmaz", []float64{0, 30, 0}, false, ""},
		{"gerçekten düz seri: sessiz", []float64{0, 0, 0}, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			allOpen, _, cur := evalWindow("error_rate", 0, mad, c.window)
			if allOpen != c.wantOpen {
				t.Fatalf("allOpen=%v bekleniyordu, got %v (cur=%+v)", c.wantOpen, allOpen, cur)
			}
			if c.wantOpen && cur.severity != c.wantSev {
				t.Fatalf("severity=%s bekleniyordu, got %s", c.wantSev, cur.severity)
			}
		})
	}
}
