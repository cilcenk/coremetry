package chstore

import "testing"

// F3 (v0.9.107) — pickPercentile histogram_quantile yolunun tek saf parçası:
// agg etiketini HistogramSeries'in ilgili percentile dizisine eşler. Yanlış
// eşleme = operatör p99 isterken p50 grafiği görür (sessiz, tehlikeli).
func TestPickPercentile(t *testing.T) {
	hs := &HistogramSeries{
		P50: []float64{1, 2, 3},
		P95: []float64{10, 20, 30},
		P99: []float64{100, 200, 300},
	}
	cases := []struct {
		agg    string
		want   []float64
		wantOk bool
	}{
		{"p50", hs.P50, true},
		{"p95", hs.P95, true},
		{"p99", hs.P99, true},
		{"avg", nil, false},
		{"rate", nil, false},
		{"", nil, false},
	}
	for _, c := range cases {
		got, ok := pickPercentile(hs, c.agg)
		if ok != c.wantOk {
			t.Errorf("pickPercentile(%q) ok = %v, want %v", c.agg, ok, c.wantOk)
			continue
		}
		if ok && &got[0] != &c.want[0] {
			t.Errorf("pickPercentile(%q) returned wrong slice", c.agg)
		}
	}

	// nil güvenliği — probe boş metrik döndürebilir.
	if _, ok := pickPercentile(nil, "p95"); ok {
		t.Errorf("pickPercentile(nil) must not report ok")
	}
}
