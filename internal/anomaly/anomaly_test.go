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
