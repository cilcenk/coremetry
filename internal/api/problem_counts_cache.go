package api

import (
	"context"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// problem_counts_cache.go — v0.8.533 (getServices audit fix B).
//
// GetOpenProblemCountsByService returns per-service open-problem tallies
// for EVERY service — the result is identical regardless of the caller's
// page / filter / env (it's a whole-table FINAL scan, not scoped to the
// returned rows). Yet it was re-run inside every distinct-key services
// recompute AND on every /service-map render. That's redundant CH load:
// N distinct services/topology cache-misses in a 30s window = N identical
// FINAL scans.
//
// This wraps it in ONE process-wide 30s cache. A cold burst of distinct
// keys now collapses to a SINGLE scan (singleflight dedupes the miss),
// pushing total CH load BELOW the pre-v0.8.530 serial baseline — the
// audit's option B. Staleness ≤30s is fine: the counts feed health
// scoring, and the services/topology endpoints are themselves 30s-cached.

type problemCountsCache struct {
	mu   sync.Mutex
	ttl  time.Duration
	at   time.Time
	data map[string]chstore.OpenProblemCounts
}

func newProblemCountsCache(ttl time.Duration) *problemCountsCache {
	return &problemCountsCache{ttl: ttl}
}

// get returns the cached map when still fresh (now - at < ttl), else nil.
func (c *problemCountsCache) get(now time.Time) map[string]chstore.OpenProblemCounts {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data != nil && now.Sub(c.at) < c.ttl {
		return c.data
	}
	return nil
}

func (c *problemCountsCache) put(m map[string]chstore.OpenProblemCounts, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data, c.at = m, now
}

// openProblemCountsCached returns the per-service open-problem counts,
// served from the 30s cache when warm. On a miss the CH scan is deduped
// across concurrent callers via singleflight (fixed key), so a cold-cache
// burst of many services/topology recomputes issues exactly ONE scan.
func (s *Server) openProblemCountsCached(ctx context.Context) (map[string]chstore.OpenProblemCounts, error) {
	if m := s.problemCounts.get(time.Now()); m != nil {
		return m, nil
	}
	// Distinct singleflight key — won't collide with serveCached's
	// per-endpoint keys; concurrent misses here collapse to one scan.
	v, err, _ := s.sf.Do("open-problem-counts", func() (any, error) {
		// Re-check under the singleflight winner: a sibling may have
		// just populated the cache while we queued.
		if m := s.problemCounts.get(time.Now()); m != nil {
			return m, nil
		}
		m, err := s.store.GetOpenProblemCountsByService(ctx)
		if err != nil {
			return nil, err
		}
		s.problemCounts.put(m, time.Now())
		return m, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(map[string]chstore.OpenProblemCounts), nil
}
