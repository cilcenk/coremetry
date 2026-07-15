package api

import (
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// problem_counts_cache_test.go — v0.8.533 (getServices audit fix B).
// TTL freshness/miss contract for the page-invariant open-problem-counts
// cache. The singleflight-deduped fetch is exercised via the live path;
// here we pin the pure TTL semantics.
func TestProblemCountsCache(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	c := newProblemCountsCache(30 * time.Second)

	if c.get(t0) != nil {
		t.Fatal("boş cache nil vermeli")
	}
	m := map[string]chstore.OpenProblemCounts{"svc": {Critical: 2, Warning: 1}}
	c.put(m, t0)

	if got := c.get(t0.Add(29 * time.Second)); got == nil || got["svc"].Critical != 2 {
		t.Fatal("TTL içinde hit beklenirdi")
	}
	if c.get(t0.Add(30 * time.Second)) != nil {
		t.Fatal("tam TTL sınırında (>= ttl) miss beklenirdi")
	}
	if c.get(t0.Add(31 * time.Second)) != nil {
		t.Fatal("TTL sonrası miss beklenirdi")
	}
	// Yeni put pencereyi tazeler.
	c.put(m, t0.Add(31*time.Second))
	if c.get(t0.Add(40 * time.Second)) == nil {
		t.Fatal("tazeleme sonrası hit beklenirdi")
	}
}
