package api

import (
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
