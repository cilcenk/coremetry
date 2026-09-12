package correlator

import (
	"math"
	"reflect"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// hypothesis_temporal_test.go — v0.10.700 (parite #2, dilim 1 / GÖLGE).
// Gölgede sıra ve skor BAYT-ÖZDEŞ kalır, yalnız yeni alanlar yazılır;
// anahtar açıkken Score = Structural × (0.5 + 0.5·t) ve sıra buna göre.
func TestSynthesizeTemporalShadowKeepsOrder(t *testing.T) {
	nbs := []ScoredCause{
		{Service: "shop-db", Score: 0.6, Hops: 1, Path: []string{"svc", "shop-db"}},
		{Service: "shop-cache", Score: 0.3, Hops: 1, Path: []string{"svc", "shop-cache"}},
	}
	factors := map[string]TemporalFactor{
		"shop-db":    {Factor: 0, Lag: 0, Reason: "no co-movement with trigger (ρ=-0.10)"},
		"shop-cache": {Factor: 1, Lag: 1, Reason: "co-moves with trigger (ρ=1.00, leads by 1×5m)"},
	}
	base := Synthesize("problem", "p1", "svc", 1, SynthesisInput{Neighbours: nbs})
	shadow := Synthesize("problem", "p1", "svc", 1, SynthesisInput{Neighbours: nbs, TemporalFactors: factors})

	if shadow.TopSuspect != base.TopSuspect || shadow.TopScore != base.TopScore || shadow.Confidence != base.Confidence {
		t.Fatalf("gölge özeti değiştirdi: %+v vs %+v", shadow, base)
	}
	if len(shadow.Candidates) != len(base.Candidates) {
		t.Fatal("aday sayısı değişti")
	}
	for i := range shadow.Candidates {
		sc, bc := shadow.Candidates[i], base.Candidates[i]
		if sc.TemporalReason == "" || sc.Structural != bc.Score {
			t.Fatalf("gölge alanları yazılmalı: %+v", sc)
		}
		sc.Structural, sc.Temporal, sc.TemporalReason = 0, 0, ""
		if !reflect.DeepEqual(sc, bc) {
			t.Fatalf("gölgede aday değişti:\n %+v\n %+v", sc, bc)
		}
	}
}

func candByName(h chstore.RootCauseHypothesis, name string) (chstore.ScoredCause, bool) {
	for _, c := range h.Candidates {
		if c.Service == name {
			return c, true
		}
	}
	return chstore.ScoredCause{}, false
}

func TestSynthesizeTemporalApplyRescores(t *testing.T) {
	nbs := []ScoredCause{
		{Service: "shop-db", Score: 0.5, Hops: 1},
		{Service: "shop-cache", Score: 0.4, Hops: 1},
		{Service: "node:w1", Score: 0.9, Hops: 0, Kind: chstore.NodeKindNode},
	}
	factors := map[string]TemporalFactor{
		"shop-db":    {Factor: 0, Reason: "no co-movement with trigger (ρ=-0.10)"},
		"shop-cache": {Factor: 1, Reason: "co-moves with trigger (ρ=1.00, in step)"},
		"node:w1":    {Factor: 1, Reason: "must be ignored"},
	}
	h := Synthesize("problem", "p1", "svc", 1, SynthesisInput{Neighbours: nbs, TemporalFactors: factors, TemporalApply: true})

	db, _ := candByName(h, "shop-db")
	if math.Abs(db.Score-0.7*0.5*0.5) > 1e-9 || db.Structural != 0.7*0.5 || db.Temporal != 0 {
		t.Fatalf("uyumsuz aday yapısalın yarısı: %+v", db)
	}
	cache, _ := candByName(h, "shop-cache")
	if math.Abs(cache.Score-0.7*0.4) > 1e-9 || cache.Temporal != 1 {
		t.Fatalf("tam uyumlu aday yapısalın tamamı: %+v", cache)
	}
	node, _ := candByName(h, "node:w1")
	if node.TemporalReason != "" || node.Structural != 0 || math.Abs(node.Score-0.7*0.9) > 1e-9 {
		t.Fatalf("node adayına zamansal çarpan uygulanmamalı: %+v", node)
	}
	// Sıra: node 0.63 > cache 0.28 > db 0.175 — yapısalda db cache'in önündeydi.
	if len(h.Candidates) != 3 || h.Candidates[1].Service != "shop-cache" || h.Candidates[2].Service != "shop-db" {
		t.Fatalf("anahtar açıkken sıra zamansal skora göre olmalı: %+v", h.Candidates)
	}
	// Ölçülmemiş servis (haritada yok) dokunulmaz.
	h2 := Synthesize("problem", "p1", "svc", 1, SynthesisInput{Neighbours: nbs[:1], TemporalFactors: map[string]TemporalFactor{}, TemporalApply: true})
	if c, _ := candByName(h2, "shop-db"); c.TemporalReason != "" || c.Score != 0.7*0.5 {
		t.Fatalf("ölçülmemiş aday yapısal kalmalı: %+v", c)
	}
}
