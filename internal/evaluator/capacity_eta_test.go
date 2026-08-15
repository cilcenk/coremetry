package evaluator

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.1065 (Faz 2.4 / G4) regresyon pinleri — kapasite ETA tahmini.
// Sözleşme: temiz doğrusal büyümede doğru saat; kısa seri / düşen eğim /
// gürültülü uyum (R²<0.6) / uzak ufuk (>24h) tahmin ÜRETMEZ; erken-açma
// kapısı yalnız %70+ ve ≤6h.
func TestCapacityETA(t *testing.T) {
	series := func(startUsage, perHour float64, hours int) []chstore.CapacityTrendPoint {
		var out []chstore.CapacityTrendPoint
		for i := 0; i <= hours*12; i++ { // 5dk adım
			out = append(out, chstore.CapacityTrendPoint{
				TSec:  int64(i * 300),
				Usage: startUsage + perHour*float64(i)/12,
			})
		}
		return out
	}

	t.Run("temiz büyüme: %78'den saatte 4 puan → ~5.5h", func(t *testing.T) {
		eta, r2, ok := capacityETA(series(78, 4, 2), 100)
		if !ok || r2 < 0.99 {
			t.Fatalf("ok=%v r2=%.3f", ok, r2)
		}
		if eta < 3.2 || eta > 3.8 { // (100-86.x)/4 ≈ 3.5h (son uydurulmuş değerden)
			t.Fatalf("eta=%.2fh, want ~3.5h", eta)
		}
	})

	t.Run("düşen seri tahmin üretmez", func(t *testing.T) {
		if _, _, ok := capacityETA(series(90, -3, 2), 100); ok {
			t.Fatal("düşen eğim ETA üretti")
		}
	})

	t.Run("düz seri tahmin üretmez", func(t *testing.T) {
		if _, _, ok := capacityETA(series(80, 0, 2), 100); ok {
			t.Fatal("düz seri ETA üretti")
		}
	})

	t.Run("uzak ufuk (>24h) sessiz", func(t *testing.T) {
		if _, _, ok := capacityETA(series(50, 0.5, 2), 100); ok {
			t.Fatal(">24h ufuk ETA üretti")
		}
	})

	t.Run("kısa seri sessiz", func(t *testing.T) {
		pts := series(78, 4, 2)[:4]
		if _, _, ok := capacityETA(pts, 100); ok {
			t.Fatal("4 nokta ETA üretti")
		}
	})

	t.Run("gürültülü uyum (R² düşük) sessiz", func(t *testing.T) {
		pts := series(78, 2, 2)
		for i := range pts { // ±büyük zikzak
			if i%2 == 0 {
				pts[i].Usage += 8
			} else {
				pts[i].Usage -= 8
			}
		}
		if _, r2, ok := capacityETA(pts, 100); ok || r2 >= capacityEtaMinR2 {
			t.Fatalf("gürültülü seri geçti: r2=%.2f ok=%v", r2, ok)
		}
	})
}

func TestCapacityPredictiveOpen(t *testing.T) {
	cases := []struct {
		pct, eta float64
		want     bool
	}{
		{78, 4, true},   // erken-açma penceresi
		{60, 2, false},  // doluluk tabanı altı
		{78, 10, false}, // ufuk uzak
		{92, 1, true},   // eşik dalı zaten açar ama kapı da doğru
	}
	for _, c := range cases {
		if got := capacityPredictiveOpen(c.pct, c.eta); got != c.want {
			t.Fatalf("capacityPredictiveOpen(%.0f, %.0f) = %v, want %v", c.pct, c.eta, got, c.want)
		}
	}
}
