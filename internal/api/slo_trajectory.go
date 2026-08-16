package api

import (
	"fmt"
	"strings"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// slo_trajectory.go — F3.4 (v0.9.1083): SLO ✨ anlatımının yörünge
// kanıtı. /api/slos/{id}/forecast ve /burn-series uçları v0.5.150'den
// beri deterministik yörünge üretiyordu ama explain beslemesi bunları
// hiç kullanmıyordu; prompt "hours to exhaustion" isteyince model
// sayıyı uyduruyordu. Bu dosya iki kaynağı düz metne çevirir — model
// yalnız buradaki rakamları anlatır.

// sloTrajectoryEvidence — saf, tablo testli. nil/boş girdiler dürüstçe
// itiraf edilir ("not available"), sıfır ya da uydurma değer basılmaz.
func sloTrajectoryEvidence(fc *chstore.SLOForecast, series []chstore.BurnPoint) string {
	var b strings.Builder
	switch {
	case fc == nil:
		b.WriteString("Exhaustion forecast: not available (do NOT invent one)\n")
	case fc.SafeBurn:
		b.WriteString("Exhaustion forecast: burn ≤ 1 — budget NOT being eaten; no exhaustion projected\n")
	case fc.BudgetRemaining <= 0:
		b.WriteString("Exhaustion forecast: budget ALREADY exhausted\n")
	default:
		fmt.Fprintf(&b, "Exhaustion forecast: %s at current burn (rate=%.2f over %dm window, breach within 24h: %v)\n",
			fmtHoursEN(fc.HoursToExhaust), fc.BurnRate, fc.BurnWindowSec/60, fc.WillBreachWithin24h)
	}
	if len(series) == 0 {
		b.WriteString("Daily burn trend (7d): not available\n")
		return b.String()
	}
	over := 0
	parts := make([]string, 0, len(series))
	for _, p := range series {
		parts = append(parts, fmt.Sprintf("%.2f", p.BurnRate))
		if p.BurnRate > 1 {
			over++
		}
	}
	fmt.Fprintf(&b, "Daily burn trend (oldest→newest): %s — %d/%d day(s) above 1.0\n",
		strings.Join(parts, ", "), over, len(series))
	return b.String()
}

// fmtHoursEN — tek birimli süre (saf; HER dal testli — unit-mixing
// dersi). <1 saat dakikaya, ≥48 saat güne devreder.
func fmtHoursEN(h float64) string {
	switch {
	case h < 1:
		return fmt.Sprintf("%.0f minutes", h*60)
	case h < 48:
		return fmt.Sprintf("%.1f hours", h)
	default:
		return fmt.Sprintf("%.1f days", h/24)
	}
}
