package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.8.359 (perf P2-C) — the /api/problems warm recompute measured
// 145-580ms, dominated by three enrichment lookups re-run on every 5s
// poll. The fixes are TTL caches (cluster map, service catalog, deploys
// fetch). These tests pin the pure key-building rule for the deploys
// cache: FULL-INPUT keys (sorted service set digest + exact window),
// never length-only — the v0.5.187 cross-poisoning class.

func TestDeploysCacheKeyFullInputs(t *testing.T) {
	from := time.Unix(1000, 0)
	to := time.Unix(2000, 0)
	set := func(names ...string) map[string]struct{} {
		m := make(map[string]struct{}, len(names))
		for _, n := range names {
			m[n] = struct{}{}
		}
		return m
	}

	base := deploysCacheKey(set("checkout", "payments"), from, to)

	// Deterministic regardless of map iteration order.
	for i := 0; i < 10; i++ {
		if k := deploysCacheKey(set("payments", "checkout"), from, to); k != base {
			t.Fatalf("key not deterministic: %q vs %q", k, base)
		}
	}

	// Same COUNT, different members → different key (the len(set) trap).
	if k := deploysCacheKey(set("checkout", "billing"), from, to); k == base {
		t.Fatalf("distinct service sets collided: %q", k)
	}

	// Concatenation ambiguity: {"a","bc"} vs {"ab","c"} must differ.
	if deploysCacheKey(set("a", "bc"), from, to) == deploysCacheKey(set("ab", "c"), from, to) {
		t.Fatal("service-name concatenation is ambiguous — keys collided")
	}

	// Window is part of the key at ns precision.
	if k := deploysCacheKey(set("checkout", "payments"), from, to.Add(time.Nanosecond)); k == base {
		t.Fatal("window shift did not change the key")
	}
	if k := deploysCacheKey(set("checkout", "payments"), from.Add(time.Nanosecond), to); k == base {
		t.Fatal("window start shift did not change the key")
	}
}

// The deploys cache must be bounded: expired entries are swept on every
// store, and a burst of distinct keys evicts the oldest instead of
// growing without limit.
func TestDeploysCacheBounded(t *testing.T) {
	s := &Store{}
	now := time.Now()

	// A burst of distinct live keys must never grow past the cap.
	for i := 0; i < deploysCacheMax+8; i++ {
		key := deploysCacheKey(map[string]struct{}{strings.Repeat("x", i+1): {}}, now, now)
		s.storeDeploysCacheEntry(key, deploysCacheEntry{at: now.Add(time.Duration(i))}, now)
	}
	if len(s.deploysCache) > deploysCacheMax {
		t.Fatalf("cache grew past its bound: %d > %d", len(s.deploysCache), deploysCacheMax)
	}

	// Oldest-first eviction: the first 8 inserts (at=now+0..now+7) are the
	// victims; the i=8 entry holds the minimum surviving stamp.
	if _, ok := s.deploysCache[deploysCacheKey(map[string]struct{}{"x": {}}, now, now)]; ok {
		t.Fatal("the oldest entry survived eviction")
	}
	if _, ok := s.deploysCache[deploysCacheKey(map[string]struct{}{strings.Repeat("x", 9): {}}, now, now)]; !ok {
		t.Fatal("a young entry was evicted instead of the oldest")
	}

	// Expired entries are swept wholesale on the next store.
	later := now.Add(deploysCacheTTL + time.Second)
	s.storeDeploysCacheEntry("fresh", deploysCacheEntry{at: later}, later)
	if len(s.deploysCache) != 1 {
		t.Fatalf("expired entries survived the sweep: %d left, want 1", len(s.deploysCache))
	}
	if _, ok := s.deploysCache["fresh"]; !ok {
		t.Fatal("the fresh entry itself was dropped")
	}
}
