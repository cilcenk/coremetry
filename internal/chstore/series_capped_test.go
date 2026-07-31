package chstore

import (
	"testing"
	"time"
)

// v0.9.458 (dürüstlük A1) — spans/metric_points GROUP BY'ları
// SpanMetricRowCap satırda kesilir ve ORDER BY gk ALFABETİK olduğundan
// geç-harfli seriler komple düşer. SeriesRowsCapped bunun tespitidir:
// toplam nokta == tavan → LIMIT ısırdı (tam-tavan meşru sonuç da
// işaretlenir — zararsız yön: "eksik olabilir" der, eksiği tam gibi
// GÖSTERMEZ; inbox len==cap sözleşmesi).
func TestSeriesRowsCapped(t *testing.T) {
	mk := func(n int) SpanMetricSeries {
		return SpanMetricSeries{Points: make([]SpanMetricPoint, n)}
	}
	cases := []struct {
		name   string
		series []SpanMetricSeries
		want   bool
	}{
		{"boş", nil, false},
		{"tavanın altı", []SpanMetricSeries{mk(100), mk(200)}, false},
		{"tam tavan → capped", []SpanMetricSeries{mk(SpanMetricRowCap - 5), mk(5)}, true},
		{"tavan üstü (teorik) → capped", []SpanMetricSeries{mk(SpanMetricRowCap), mk(1)}, true},
		{"tavan-1 → değil", []SpanMetricSeries{mk(SpanMetricRowCap - 1)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SeriesRowsCapped(tc.series); got != tc.want {
				t.Errorf("SeriesRowsCapped = %v, want %v", got, tc.want)
			}
		})
	}
}

// v0.9.460 (dürüstlük A8) — tekil span-metric yolunda EXPLICIT step de
// nokta bütçesine kelepçelenir: step=1s + 7g pencere ≈ 600k bucket, satır
// tavanını aşıp chart'ın pencerenin yalnız BAŞINI çizmesine yol açıyordu.
// Bütçe içindeki explicit step aynen kalır (operatör seçimi kutsal).
func TestClampExplicitStepSinglePath(t *testing.T) {
	from := time.Unix(1_753_000_000, 0)
	cases := []struct {
		name     string
		stepSec  int
		spanSec  int64
		wantSame bool // explicit step bütçe içinde → dokunma
	}{
		{"1h pencerede 10s step aynen", 10, 3600, true},
		{"1g pencerede 60s step aynen", 60, 86400, true},
		{"7g pencerede 1s step kabalaşır", 1, 7 * 86400, false},
		{"30g pencerede 60s step kabalaşır", 60, 30 * 86400, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			to := from.Add(time.Duration(tc.spanSec) * time.Second)
			got := clampSpanMetricStep(tc.stepSec, from, to, 0)
			if tc.wantSame && got != tc.stepSec {
				t.Errorf("bütçe içi explicit step değişti: %d → %d", tc.stepSec, got)
			}
			if !tc.wantSame {
				if got <= tc.stepSec {
					t.Errorf("bütçe aşan step kabalaşmadı: %d → %d", tc.stepSec, got)
				}
				if int(tc.spanSec)/got > 2000 {
					t.Errorf("kelepçe sonrası nokta sayısı hâlâ bütçe üstü: %d", int(tc.spanSec)/got)
				}
			}
		})
	}
}
