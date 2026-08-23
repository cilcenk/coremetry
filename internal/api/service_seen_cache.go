package api

import (
	"context"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// service_seen_cache.go — v0.9.1317, entity-model slice A2.
//
// Same shape and same reasoning as problem_counts_cache.go (v0.8.533):
// GetServiceSeen returns the lifecycle pair for EVERY service, and the
// result does not vary by page, filter, range or env. Re-running it inside
// every distinct-key /api/services recompute would be N identical
// whole-table scans in a 30s window for one unchanging answer.
//
// One process-wide 5-minute snapshot instead, with the miss deduped by
// singleflight. The TTL is longer than the problem-counts 30s on purpose:
// last_seen moves at telemetry speed but is READ as "roughly how long ago",
// and first_seen for an established service does not move at all. A stale
// minute costs the operator nothing here, whereas an open-problem count
// flipping a health badge is worth 30s.
//
// The floor is computed ONCE per fill rather than per request. It is a min
// over the whole snapshot, so recomputing it per caller would be both
// wasted work and — if a caller ever passed a filtered map — a correctness
// trap, since the floor of a subset censors the wrong services.

type serviceSeenSnapshot struct {
	byService map[string]chstore.ServiceSeen
	// floor — the MV's own earliest datum. See chstore.ServiceSeenFloor.
	floor time.Time
}

type serviceSeenCache struct {
	mu   sync.Mutex
	ttl  time.Duration
	at   time.Time
	data *serviceSeenSnapshot
}

func newServiceSeenCache(ttl time.Duration) *serviceSeenCache {
	return &serviceSeenCache{ttl: ttl}
}

// get returns the cached snapshot when still fresh (now - at < ttl), else nil.
func (c *serviceSeenCache) get(now time.Time) *serviceSeenSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data != nil && now.Sub(c.at) < c.ttl {
		return c.data
	}
	return nil
}

func (c *serviceSeenCache) put(s *serviceSeenSnapshot, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data, c.at = s, now
}

// serviceSeenCached returns the per-service lifecycle snapshot, served from
// the cache when warm. On a miss the CH scan is deduped across concurrent
// callers via singleflight, so a cold-cache burst issues exactly one scan.
func (s *Server) serviceSeenCached(ctx context.Context) (*serviceSeenSnapshot, error) {
	if snap := s.serviceSeen.get(time.Now()); snap != nil {
		return snap, nil
	}
	// Distinct singleflight key — won't collide with serveCached's
	// per-endpoint keys or with open-problem-counts.
	v, err, _ := s.sf.Do("service-seen", func() (any, error) {
		if snap := s.serviceSeen.get(time.Now()); snap != nil {
			return snap, nil
		}
		m, err := s.store.GetServiceSeen(ctx)
		if err != nil {
			return nil, err
		}
		snap := &serviceSeenSnapshot{byService: m, floor: chstore.ServiceSeenFloor(m)}
		s.serviceSeen.put(snap, time.Now())
		return snap, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*serviceSeenSnapshot), nil
}

// applyServiceSeen stamps the lifecycle pair onto a page of service rows.
//
// Soft-fail by design, matching how the sibling enrichments on this handler
// behave (open-problem counts, the compare=prior window): a service list
// that loses its "last seen" column is degraded, a service list that
// returns 500 because a decorative MV is missing is broken. The MV is also
// absent for the first minutes after an upgrade and on any install that
// has not migrated yet, which is a routine state, not an error.
func (s *Server) applyServiceSeen(ctx context.Context, rows []chstore.ServiceSummary) {
	if len(rows) == 0 {
		return
	}
	snap, err := s.serviceSeenCached(ctx)
	if err != nil || snap == nil || len(snap.byService) == 0 {
		return
	}
	for i := range rows {
		sv, ok := snap.byService[rows[i].Name]
		if !ok {
			continue
		}
		if !sv.LastSeen.IsZero() {
			rows[i].LastSeen = sv.LastSeen.UnixNano()
		}
		// The honest branch. When the MV cannot prove this is a birth —
		// which is EVERY service until the MV outlives the fleet it
		// watches — FirstSeen stays zero and omitempty drops the key.
		if chstore.FirstSeenIsKnown(sv.FirstSeen, snap.floor, chstore.ServiceSeenGrace) {
			rows[i].FirstSeen = sv.FirstSeen.UnixNano()
		}
	}
}
