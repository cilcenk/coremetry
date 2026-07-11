package api

import (
	"math"
	"testing"
)

// slo_autocreate_test.go — v0.8.505 regression. ServiceSummary.ErrorRate
// YÜZDE ölçeğindedir (chstore iki üretici de ×100 yazar); autocreate onu
// kesir gibi kullanıp saçma baseline'lar üretiyordu. Vakalar canlı
// gözlemden: %4.46 hata → SLI 0 (clamp), %0.62 → 0.377.
func TestAvailabilityFromErrorRatePct(t *testing.T) {
	cases := []struct {
		name string
		pct  float64
		want float64
	}{
		{"hatasız servis", 0, 1},
		{"%0.62 hata → 0.9938 (0.377 DEĞİL)", 0.62, 0.9938},
		{"%4.46 hata → 0.9554 (0 DEĞİL)", 4.46, 0.9554},
		{"%100 hata → 0", 100, 0},
		{"bozuk girdi >100 → 0'a clamp", 250, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := availabilityFromErrorRatePct(c.pct); math.Abs(got-c.want) > 1e-9 {
				t.Fatalf("pct=%v: got %v, want %v", c.pct, got, c.want)
			}
		})
	}
}
