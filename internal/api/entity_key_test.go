package api

import (
	"github.com/cilcenk/coremetry/internal/chstore"
	"testing"
	"time"
)

// v0.10.130 — entity uçlarının cache anahtarları TÜM girdileri taşır
// (v0.5.187 kuralı; cache_key_test.go emsali): cluster, tip, namespace,
// arama, limit, "at" anı ve pencere. Eksik girdi = bir operatörün
// pod listesi başkasının cluster'ına servis edilir.

func TestEntityCacheKeysHashAllInputs(t *testing.T) {
	t0 := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	base := entityListKey("c-1", "pod", "pay", "api", 50, t0)
	variants := []string{
		entityListKey("c-2", "pod", "pay", "api", 50, t0),
		entityListKey("c-1", "node", "pay", "api", 50, t0),
		entityListKey("c-1", "pod", "other", "api", 50, t0),
		entityListKey("c-1", "pod", "pay", "db", 50, t0),
		entityListKey("c-1", "pod", "pay", "api", 20, t0),
		entityListKey("c-1", "pod", "pay", "api", 50, t0.Add(time.Hour)),
		entityListKey("c-1", "pod", "pay", "api", 50, time.Time{}),
	}
	seen := map[string]bool{base: true}
	for _, v := range variants {
		if seen[v] {
			t.Fatalf("anahtar çakışması: %s", v)
		}
		seen[v] = true
	}
	// Kararlılık.
	if base != entityListKey("c-1", "pod", "pay", "api", 50, t0) {
		t.Fatal("aynı girdi aynı anahtar")
	}
	// Serbest metin ayraç saldırısı: "a|b" + "c" ≠ "a" + "b|c" (dizeler FNV ile ayrı ayrı).
	if entityListKey("c", "pod", "a|b", "c", 1, t0) == entityListKey("c", "pod", "a", "b|c", 1, t0) {
		t.Fatal("ayraç saldırısı: namespace/arama karışıyor")
	}
	k1 := entityPivotKey("services", "pod:c/ns/a", t0, t0.Add(time.Hour))
	k2 := entityPivotKey("services", "pod:c/ns/b", t0, t0.Add(time.Hour))
	k3 := entityPivotKey("metrics", "pod:c/ns/a", t0, t0.Add(time.Hour))
	k4 := entityPivotKey("services", "pod:c/ns/a", t0, t0.Add(2*time.Hour))
	if k1 == k2 || k1 == k3 || k1 == k4 {
		t.Fatal("pivot anahtarı id/tür/pencereyi ayırmalı")
	}
	s1 := servicePodsKey("api", "c-1", t0, t0.Add(time.Hour))
	s2 := servicePodsKey("api", "", t0, t0.Add(time.Hour))
	if s1 == s2 {
		t.Fatal("cluster'sız ve cluster'lı servis→pod anahtarı ayrı olmalı")
	}
}

// v0.10.139 (inceleme) — çoklu değerli kayıtta aynı pod'un iki satırı birleşir:
// sayımlar toplanır, ortalama ağırlıklı, uç görülmeler, node boşsa öbüründen.
func TestMergeSeenAgg(t *testing.T) {
	t1 := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	a := chstore.EntitySeenAgg{Cluster: "prod-eu-west", Namespace: "pay", Pod: "api-1", Spans: 100, Errors: 1, AvgMs: 10, FirstSeen: t1.Add(time.Hour), LastSeen: t1.Add(2 * time.Hour)}
	b := chstore.EntitySeenAgg{Cluster: "eu-legacy", Namespace: "pay", Pod: "api-1", Node: "w1", Spans: 300, Errors: 3, AvgMs: 30, FirstSeen: t1, LastSeen: t1.Add(3 * time.Hour)}
	m := mergeSeenAgg(a, b)
	if m.Cluster != "prod-eu-west" || m.Spans != 400 || m.Errors != 4 || m.AvgMs != 25 || m.Node != "w1" || !m.FirstSeen.Equal(t1) || !m.LastSeen.Equal(t1.Add(3*time.Hour)) {
		t.Fatalf("birleşim: %+v", m)
	}
}
