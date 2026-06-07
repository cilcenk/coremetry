// Package correlator builds and refreshes a service-to-neighbors
// adjacency map from sampled trace topology. The incident
// auto-attach path consults this map so a downstream-failure
// incident on payment-service and an upstream-saturation incident
// on api-gateway end up grouped instead of creating two separate
// incidents that page the oncall twice for the same outage.
//
// Design:
//   - One background goroutine refreshes the map every 5 min from
//     the topology_edges_5m MV. The map is bounded by however many
//     service→service edges the MV holds (LIMIT 10000), so memory
//     is small.
//   - Lookups are read-locked and return copies so callers don't
//     race the next refresh.
//   - When the source returns nothing (cold start, no traffic yet),
//     the previous graph is preserved — never replace with empty,
//     since that would break correlation during a quiet window.
//
// v0.8.67 (Faz 5) — the graph is now DIRECTED and WEIGHTED. Through
// Faz 4 the correlator kept a single symmetric set (svc → neighbour
// set): enough to answer "are A and B topologically close?" but not
// "which of payment-service's downstream deps carries the error
// traffic?". Now two maps are kept — `out` (caller → downstream
// callees) and `in` (callee → upstream callers) — each edge tagged
// with calls/errors/duration. The legacy Neighbors / AreNeighbors API
// is preserved exactly (union of in ∪ out), so the incident
// auto-attach path and the fusion evidence bundle are unchanged.
package correlator

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// EdgeStat is the weight carried by one directed edge over the
// refresh window.
type EdgeStat struct {
	Calls         uint64
	Errors        uint64
	SumDurationNs uint64
}

// ErrorRate returns the fraction of calls on this edge that failed,
// in [0,1]. Zero calls → 0 (no traffic means no observed failure
// signal, not a divide-by-zero).
func (e EdgeStat) ErrorRate() float64 {
	if e.Calls == 0 {
		return 0
	}
	return float64(e.Errors) / float64(e.Calls)
}

// WeightedEdge is a neighbour service paired with the edge weight to
// it. Direction is implied by the accessor that produced it
// (Downstream → the service is a callee; Upstream → a caller).
type WeightedEdge struct {
	Service string
	EdgeStat
}

// Correlator surfaces service topology relationships — 1-hop only
// (direct caller or callee). Two-hop would catch transitive
// dependencies but is deferred to Faz 6 (decayed 2-hop); at 1 hop
// the recall is fine and false-positives are the bigger risk.
type Correlator struct {
	store *chstore.Store

	mu        sync.RWMutex
	out       map[string]map[string]EdgeStat // caller → callee → weight (downstream deps)
	in        map[string]map[string]EdgeStat // callee → caller → weight (upstream callers)
	updatedAt time.Time
}

func New(store *chstore.Store) *Correlator {
	return &Correlator{
		store: store,
		out:   map[string]map[string]EdgeStat{},
		in:    map[string]map[string]EdgeStat{},
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
	// v0.5.304 — read from the pre-aggregated topology_edges_5m MV
	// instead of walking a sample of trace IDs over raw spans (which
	// at billion-span scale tripped the 30s max_execution_time on
	// boot). v0.8.67 — switched to the weighted variant so each edge
	// carries calls/errors/duration. 1h window keeps the original
	// recall — a low-RPS service with a single bucket still shows up.
	edges, err := c.store.GetServiceAdjacencyWeighted(ctx, time.Hour)
	if err != nil {
		return err
	}
	if replaced, callers := c.applyEdges(edges); replaced {
		log.Printf("[correlator] adjacency refreshed (%d callers, %d edges)",
			callers, len(edges))
	}
	return nil
}

// applyEdges swaps in a freshly built directed graph under the write
// lock, returning whether it replaced the previous one and the new
// caller count. An empty edge list is preserved-not-clobbered (cold
// start / quiet window — blanking the live graph would break
// correlation exactly when an incident is least expected). Split out
// of refresh() so the empty-preservation guard and the build are
// unit-testable without a live ClickHouse (v0.8.67).
func (c *Correlator) applyEdges(edges []chstore.ServiceEdgePair) (replaced bool, callers int) {
	if len(edges) == 0 {
		return false, 0 // keep previous graph; never replace with empty
	}
	out, in := buildGraph(edges)
	c.mu.Lock()
	c.out = out
	c.in = in
	c.updatedAt = time.Now()
	c.mu.Unlock()
	return true, len(out)
}

// buildGraph turns a weighted edge list into the two directed
// adjacency maps. Pure (no lock, no store) so the direction and
// weight carry-through (ServiceEdgePair → EdgeStat) are unit-testable
// — a build-time out/in swap would otherwise pass the whole suite.
func buildGraph(edges []chstore.ServiceEdgePair) (out, in map[string]map[string]EdgeStat) {
	out = map[string]map[string]EdgeStat{}
	in = map[string]map[string]EdgeStat{}
	for _, e := range edges {
		st := EdgeStat{Calls: e.Calls, Errors: e.Errors, SumDurationNs: e.SumDurationNs}
		// Directed: caller knows its downstream callee, callee knows
		// its upstream caller. The source GROUP BYs (caller, callee)
		// so each directed pair is unique — last write is a no-op
		// dedup, not a silent overwrite of distinct weights.
		ensureEdge(out, e.Caller)[e.Callee] = st
		ensureEdge(in, e.Callee)[e.Caller] = st
	}
	return out, in
}

// Neighbors returns a copy of the 1-hop neighbour set for svc
// (callers ∪ callees), or nil when svc is unknown / a leaf. Order is
// undefined (map iteration). Preserved verbatim from Faz 4 so the
// incident auto-attach path is unchanged by the directed refactor.
func (c *Correlator) Neighbors(svc string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	seen := make(map[string]struct{}, len(c.out[svc])+len(c.in[svc]))
	for n := range c.out[svc] {
		seen[n] = struct{}{}
	}
	for n := range c.in[svc] {
		seen[n] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	res := make([]string, 0, len(seen))
	for n := range seen {
		res = append(res, n)
	}
	return res
}

// AreNeighbors reports whether a and b are within 1 hop (in either
// direction) in the last refreshed graph. Self-pair returns true so
// the incident-attach call site can use this as the single "consider
// these problems related" predicate.
func (c *Correlator) AreNeighbors(a, b string) bool {
	if a == b {
		return true
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if _, ok := c.out[a][b]; ok { // a calls b
		return true
	}
	if _, ok := c.in[a][b]; ok { // b calls a
		return true
	}
	return false
}

// Downstream returns svc's direct callees (the deps it calls), each
// with its edge weight, sorted by error-carrying volume: errors desc,
// then calls desc, then name asc for a stable order. This is the
// "which downstream dep most likely caused my failure" ranking the
// root-cause panel (and Faz 6's conditional probability) build on.
func (c *Correlator) Downstream(svc string) []WeightedEdge {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return sortedEdges(c.out[svc])
}

// Upstream returns svc's direct callers (the deps that call it), each
// with its edge weight, sorted by the same error-first key. This is
// the "which upstream am I taking down" / blast-radius direction.
func (c *Correlator) Upstream(svc string) []WeightedEdge {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return sortedEdges(c.in[svc])
}

// Edge returns the weight of the directed edge a → b (a calls b) and
// whether such an edge exists in the last refresh.
func (c *Correlator) Edge(a, b string) (EdgeStat, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	st, ok := c.out[a][b]
	return st, ok
}

// sortedEdges flattens an adjacency row into a deterministically
// ordered slice (errors desc, calls desc, service asc). Caller holds
// the read lock; the returned slice is a fresh copy.
func sortedEdges(row map[string]EdgeStat) []WeightedEdge {
	if len(row) == 0 {
		return nil
	}
	res := make([]WeightedEdge, 0, len(row))
	for svc, st := range row {
		res = append(res, WeightedEdge{Service: svc, EdgeStat: st})
	}
	sort.Slice(res, func(i, j int) bool {
		if res[i].Errors != res[j].Errors {
			return res[i].Errors > res[j].Errors
		}
		if res[i].Calls != res[j].Calls {
			return res[i].Calls > res[j].Calls
		}
		return res[i].Service < res[j].Service
	})
	return res
}

// UpdatedAt reports the last successful refresh — surfaced on
// /api/admin/system-stats so the operator can see correlation
// is live.
func (c *Correlator) UpdatedAt() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.updatedAt
}

func ensureEdge(m map[string]map[string]EdgeStat, k string) map[string]EdgeStat {
	if v, ok := m[k]; ok {
		return v
	}
	v := map[string]EdgeStat{}
	m[k] = v
	return v
}
