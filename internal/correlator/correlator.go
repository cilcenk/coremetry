// Package correlator builds and refreshes a service-to-neighbors
// adjacency map from sampled trace topology. The incident
// auto-attach path consults this map so a downstream-failure
// incident on payment-service and an upstream-saturation incident
// on api-gateway end up grouped instead of creating two separate
// incidents that page the oncall twice for the same outage.
//
// Design:
//   - One background goroutine refreshes the map every 5 min from
//     chstore.GetServiceMap. The map is bounded by however many
//     services the sampled traces touch (sample cap = 200 traces),
//     so memory is small.
//   - The Neighbors lookup is read-locked, returns a copy so
//     callers don't race the next refresh.
//   - When chstore returns nothing (cold start, no traffic yet),
//     the previous map is preserved — never replace with empty,
//     since that would break correlation during a quiet window.
package correlator

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// Correlator surfaces "is service A topologically close to
// service B" — currently 1-hop only (direct caller or callee).
// Two-hop would catch transitive dependencies but at a sampling
// cap of 200 traces the recall is fine and false-positives are
// the bigger risk.
type Correlator struct {
	store *chstore.Store

	mu        sync.RWMutex
	neighbors map[string]map[string]struct{} // svc → set of neighbour svcs
	updatedAt time.Time
}

func New(store *chstore.Store) *Correlator {
	return &Correlator{
		store:     store,
		neighbors: map[string]map[string]struct{}{},
	}
}

// Start launches the refresh loop. Runs an immediate refresh so
// the first incident attach after boot has data, then re-runs
// every 5 minutes. Returns when ctx is cancelled.
func (c *Correlator) Start(ctx context.Context) {
	if err := c.refresh(ctx); err != nil {
		log.Printf("[correlator] initial refresh: %v", err)
	}
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := c.refresh(ctx); err != nil {
				log.Printf("[correlator] refresh: %v", err)
			}
		}
	}
}

func (c *Correlator) refresh(ctx context.Context) error {
	// v0.5.304 — read from the pre-aggregated topology_edges_5m
	// MV instead of GetServiceMap. The previous path walked a
	// sample of trace IDs over a 1h window of raw `spans`,
	// which at billion-span scale tripped the CH 30s
	// max_execution_time on boot. The MV is already
	// 5-min-bucketed by (parent_service, child_node), so the
	// adjacency read is sub-second regardless of underlying
	// volume. 1h window keeps the original recall — services
	// with low-RPS traffic still show up if they had a single
	// bucket in that window.
	edges, err := c.store.GetServiceAdjacency(ctx, time.Hour)
	if err != nil {
		return err
	}
	if len(edges) == 0 {
		return nil // keep previous map; never replace with empty
	}
	next := map[string]map[string]struct{}{}
	for _, e := range edges {
		// 1-hop bidirectional — caller knows callee, callee
		// knows caller.
		ensure(next, e.Caller)[e.Callee] = struct{}{}
		ensure(next, e.Callee)[e.Caller] = struct{}{}
	}
	c.mu.Lock()
	c.neighbors = next
	c.updatedAt = time.Now()
	c.mu.Unlock()
	log.Printf("[correlator] adjacency refreshed (%d services, %d edges)",
		len(next), len(edges))
	return nil
}

// Neighbors returns a copy of the 1-hop neighbour set for svc, or
// an empty slice when svc is unknown / leaf.
func (c *Correlator) Neighbors(svc string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	set := c.neighbors[svc]
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	return out
}

// AreNeighbors reports whether a and b are within 1 hop in the
// last refreshed adjacency. Self-pair returns true so the
// incident-attach call site can use this as the single
// "consider these problems related" predicate.
func (c *Correlator) AreNeighbors(a, b string) bool {
	if a == b {
		return true
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if set, ok := c.neighbors[a]; ok {
		if _, hit := set[b]; hit {
			return true
		}
	}
	return false
}

// UpdatedAt reports the last successful refresh — surfaced on
// /api/admin/system-stats so the operator can see correlation
// is live.
func (c *Correlator) UpdatedAt() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.updatedAt
}

func ensure(m map[string]map[string]struct{}, k string) map[string]struct{} {
	if v, ok := m[k]; ok {
		return v
	}
	v := map[string]struct{}{}
	m[k] = v
	return v
}
