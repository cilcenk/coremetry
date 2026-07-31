package chstore

import "testing"

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
