package anomaly

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.1068 (F1.6-R1) regresyon pini — checkOne'ın karar fazı saf
// evaluateAnomaly'de. Bu tablo davranış-birebirlik sözleşmesini mühürler:
// yeterlilik kapıları skip döner, sıçrama open döner (yön/σ ile), açık
// problem bant-içi son kovada resolve döner ve LatestHasData sessizlik
// dürüstlüğünü taşır. Ayrım kümeleme (spec dilim 2-3) için: karar
// toplanabilir olmalı ki scan iki-faza geçebilsin.
func TestEvaluateAnomaly(t *testing.T) {
	cfg := chstore.DefaultAnomalySensitivity()
	steady := func(n int, v float64) []float64 {
		out := make([]float64, n)
		for i := range out {
			out[i] = v
		}
		return out
	}

	t.Run("kısa geçmiş → skip", func(t *testing.T) {
		oc := evaluateAnomaly("p99_ms", steady(4, 100), nil, steady(4, 5), 4, false, cfg)
		if oc.Action != "skip" {
			t.Fatalf("action=%q, want skip", oc.Action)
		}
	})

	t.Run("sürekli sıçrama → open (up)", func(t *testing.T) {
		buckets := steady(40, 100)
		for i := len(buckets) - cfg.DwellBuckets; i < len(buckets); i++ {
			buckets[i] = 900 // dwell penceresi boyunca 9×
		}
		oc := evaluateAnomaly("p99_ms", buckets, nil, steady(40, 5), 4, false, cfg)
		if oc.Action != "open" || oc.Direction != "spiked" {
			t.Fatalf("action=%q dir=%q, want open/spiked (z=%.1f)", oc.Action, oc.Direction, oc.Z)
		}
		if oc.Current != 900 || oc.Median == 0 {
			t.Fatalf("outcome sayıları taşınmadı: %+v", oc)
		}
	})

	t.Run("açık problem + bant-içi son kova → resolve, LatestHasData=true", func(t *testing.T) {
		oc := evaluateAnomaly("p99_ms", steady(40, 100), nil, steady(40, 5), 4, true, cfg)
		if oc.Action != "resolve" || !oc.LatestHasData {
			t.Fatalf("action=%q latestHasData=%v, want resolve/true", oc.Action, oc.LatestHasData)
		}
	})

	t.Run("susan kuyruk: resolve ama LatestHasData=false (dürüst gerekçe girdisi)", func(t *testing.T) {
		buckets := steady(40, 100)
		rates := steady(40, 5)
		for i := 37; i < 40; i++ { // padlenmiş sessiz kuyruk
			buckets[i], rates[i] = 0, 0
		}
		oc := evaluateAnomaly("p99_ms", buckets, nil, rates, 4, true, cfg)
		if oc.Action != "resolve" || oc.LatestHasData {
			t.Fatalf("action=%q latestHasData=%v, want resolve/false", oc.Action, oc.LatestHasData)
		}
	})

	t.Run("açık yokken bant-içi → none", func(t *testing.T) {
		oc := evaluateAnomaly("p99_ms", steady(40, 100), nil, steady(40, 5), 4, false, cfg)
		if oc.Action != "none" {
			t.Fatalf("action=%q, want none", oc.Action)
		}
	})
}
