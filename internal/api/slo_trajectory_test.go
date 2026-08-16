// v0.9.1083 (F3.4) regresyon testleri — SLO yörünge kanıtı.
// Korunan şey: model "N saat sonra biter"i ancak paket verirse
// söyleyebilir; paket yoksa yokluk AÇIKÇA yazılır (uydurma yasağı).
package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestFmtHoursEN(t *testing.T) {
	// Unit-mixing dersi: her birim dalı ayrı vaka.
	cases := []struct {
		h    float64
		want string
	}{
		{0.5, "30 minutes"},
		{18.4, "18.4 hours"},
		{47.9, "47.9 hours"},
		{72, "3.0 days"},
	}
	for _, c := range cases {
		if got := fmtHoursEN(c.h); got != c.want {
			t.Errorf("fmtHoursEN(%v) = %q, beklenen %q", c.h, got, c.want)
		}
	}
}

func TestSLOTrajectoryEvidence(t *testing.T) {
	pt := func(rate float64) chstore.BurnPoint { return chstore.BurnPoint{BurnRate: rate} }

	t.Run("forecast yok → uydurma yasağı satırı", func(t *testing.T) {
		got := sloTrajectoryEvidence(nil, nil)
		if !strings.Contains(got, "not available (do NOT invent one)") {
			t.Errorf("yokluk itirafı eksik: %q", got)
		}
	})

	t.Run("safe burn → tükenme projeksiyonu YOK", func(t *testing.T) {
		got := sloTrajectoryEvidence(&chstore.SLOForecast{SafeBurn: true}, nil)
		if !strings.Contains(got, "no exhaustion projected") || strings.Contains(got, "hours") {
			t.Errorf("safe dalı yanlış: %q", got)
		}
	})

	t.Run("bütçe bitmiş", func(t *testing.T) {
		got := sloTrajectoryEvidence(&chstore.SLOForecast{BudgetRemaining: 0}, nil)
		if !strings.Contains(got, "ALREADY exhausted") {
			t.Errorf("bitmiş dal yanlış: %q", got)
		}
	})

	t.Run("gerçek projeksiyon + seri özeti", func(t *testing.T) {
		fc := &chstore.SLOForecast{
			BurnRate: 3.5, BurnWindowSec: 3600, BudgetRemaining: 0.4,
			HoursToExhaust: 18.4, WillBreachWithin24h: true,
		}
		got := sloTrajectoryEvidence(fc, []chstore.BurnPoint{pt(0.5), pt(1.2), pt(3.5)})
		for _, want := range []string{
			"18.4 hours at current burn",
			"rate=3.50 over 60m window",
			"breach within 24h: true",
			"0.50, 1.20, 3.50",
			"2/3 day(s) above 1.0",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("kanıtta %q yok:\n%s", want, got)
			}
		}
	})
}
