package anomaly

import "testing"

// v0.9.1052 (Faz 0.4) regresyon pinleri — mevsimsel/ardışık baseline
// hijyeni. Üç kontaminasyon kanalı kapatıldı:
//   Q1 — üst zaman sınırı (SQL-shape testi anomaly_seasonal_test.go'da),
//   Q2 — gün-çeşitliliği kapısı (tek/iki günün örnekleri "mevsim" değil),
//   Q3 — padlenmiş sıfır kuyruğu baseline'a taşamaz.

func TestPruneSeasonalByDayDiversity(t *testing.T) {
	days := func(ds ...int64) map[int64]struct{} {
		m := map[int64]struct{}{}
		for _, d := range ds {
			m[d] = struct{}{}
		}
		return m
	}

	out := map[string][]float64{
		"three-days": {1, 2, 3, 4},
		"two-days":   {1, 2, 3, 4, 5, 6, 7}, // bol örnek ama 2 günden
		"no-days":    {1},
	}
	seen := map[string]map[int64]struct{}{
		"three-days": days(100, 101, 102),
		"two-days":   days(100, 101),
	}
	pruneSeasonalByDayDiversity(out, seen, 3)

	if _, ok := out["three-days"]; !ok {
		t.Fatal("3 günlü servis düşürüldü — kapı fazla sıkı")
	}
	if _, ok := out["two-days"]; ok {
		t.Fatal("2 günlü servis mevsimselde kaldı — v0.9.957 sınıfı (tek günün gürültüsü baseline oldu)")
	}
	if _, ok := out["no-days"]; ok {
		t.Fatal("günü hiç izlenmemiş servis mevsimselde kaldı")
	}
}

func TestTrimTrailingSilent(t *testing.T) {
	cases := []struct {
		name   string
		values []float64
		rates  []float64
		want   int // kalan uzunluk
	}{
		{"kuyruk sıfır koşusu kırpılır", []float64{5, 6, 7, 0, 0}, []float64{1, 1, 1, 0, 0}, 3},
		{"içerideki gerçek sıfır KALIR", []float64{5, 0, 7, 8}, []float64{1, 0, 1, 1}, 4},
		{"pad yoksa aynen", []float64{5, 6}, []float64{1, 1}, 2},
		{"tamamen sessiz seri boşa iner", []float64{0, 0, 0}, []float64{0, 0, 0}, 0},
		{"rates kısa gelirse savunmacı kesim", []float64{5, 6, 7}, []float64{1, 1}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := trimTrailingSilent(tc.values, tc.rates)
			if len(got) != tc.want {
				t.Fatalf("len=%d, want %d (%v)", len(got), tc.want, got)
			}
		})
	}
}
