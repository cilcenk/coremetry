package correlator

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// directSetup builds a Correlator without a backing store and
// populates the directed graph from a plain caller→[callees] map
// (each edge gets a nominal weight of 1 call). The public API path
// (Neighbors / AreNeighbors / Downstream / Upstream) is what
// consumers depend on, so exercising it bypassing refresh is the
// high-signal test.
func directSetup(t *testing.T, edges map[string][]string) *Correlator {
	t.Helper()
	c := &Correlator{
		out: map[string]map[string]EdgeStat{},
		in:  map[string]map[string]EdgeStat{},
	}
	for svc, ns := range edges {
		ensureEdge(c.out, svc) // register the caller even when it's a leaf
		for _, n := range ns {
			ensureEdge(c.out, svc)[n] = EdgeStat{Calls: 1}
			ensureEdge(c.in, n)[svc] = EdgeStat{Calls: 1}
		}
	}
	return c
}

// wedge is a directed, weighted edge for the weighted-graph tests.
type wedge struct {
	from, to string
	stat     EdgeStat
}

func weightedSetup(t *testing.T, edges []wedge) *Correlator {
	t.Helper()
	c := &Correlator{
		out: map[string]map[string]EdgeStat{},
		in:  map[string]map[string]EdgeStat{},
	}
	for _, e := range edges {
		ensureEdge(c.out, e.from)[e.to] = e.stat
		ensureEdge(c.in, e.to)[e.from] = e.stat
	}
	return c
}

// ── Legacy 1-hop set API (Faz 1-4) — preserved verbatim through the
// directed refactor. These pin the backward-compat contract the
// incident auto-attach path and fusion evidence bundle depend on. ──

func TestNeighborsReturnsAll(t *testing.T) {
	c := directSetup(t, map[string][]string{
		"api":      {"checkout", "auth"},
		"checkout": {"api", "payment"},
	})
	got := c.Neighbors("api")
	if len(got) != 2 {
		t.Fatalf("expected 2 neighbours, got %d (%v)", len(got), got)
	}
	// Order is map-iteration-dependent — collect into a set for the check.
	seen := map[string]bool{}
	for _, n := range got {
		seen[n] = true
	}
	if !seen["checkout"] || !seen["auth"] {
		t.Errorf("missing neighbour: got %v", got)
	}
}

func TestNeighborsUnknownService(t *testing.T) {
	c := directSetup(t, map[string][]string{"api": {"x"}})
	if got := c.Neighbors("ghost"); got != nil {
		t.Errorf("unknown service should return nil, got %v", got)
	}
}

func TestNeighborsLeafReturnsNil(t *testing.T) {
	c := directSetup(t, map[string][]string{"leaf": {}})
	if got := c.Neighbors("leaf"); got != nil {
		t.Errorf("empty set should return nil (caller uses len(nil)==0), got %v", got)
	}
}

func TestAreNeighborsSelfPair(t *testing.T) {
	c := directSetup(t, map[string][]string{})
	// A service is "near" itself — incident-attach uses this as the
	// single "consider related" predicate, so same-service incidents
	// must group.
	if !c.AreNeighbors("api", "api") {
		t.Fatal("self-pair must be neighbours")
	}
}

func TestAreNeighborsKnownEdge(t *testing.T) {
	c := directSetup(t, map[string][]string{
		"api":      {"checkout"},
		"checkout": {"api"},
	})
	if !c.AreNeighbors("api", "checkout") {
		t.Fatal("api ↔ checkout must be neighbours")
	}
	if !c.AreNeighbors("checkout", "api") {
		t.Fatal("relationship must be bidirectional via the map")
	}
}

func TestAreNeighborsUnrelated(t *testing.T) {
	c := directSetup(t, map[string][]string{
		"api":       {"checkout"},
		"checkout":  {"api"},
		"reports":   {"warehouse"},
		"warehouse": {"reports"},
	})
	if c.AreNeighbors("api", "warehouse") {
		t.Fatal("two-hop services must not register as neighbours (1-hop only by design)")
	}
}

// AreNeighbors must hold even when the edge exists in ONE direction
// only — the legacy set was symmetric, so the directed graph's
// union(in ∪ out) lookup has to reproduce that.
func TestAreNeighborsSingleDirection(t *testing.T) {
	c := weightedSetup(t, []wedge{
		{"gateway", "api", EdgeStat{Calls: 10}}, // gateway → api only
	})
	if !c.AreNeighbors("gateway", "api") {
		t.Fatal("caller→callee must register as neighbours")
	}
	if !c.AreNeighbors("api", "gateway") {
		t.Fatal("callee must also see its caller as a neighbour (symmetric legacy contract)")
	}
}

func TestEnsureEdgeCreatesAndReuses(t *testing.T) {
	m := map[string]map[string]EdgeStat{}
	a := ensureEdge(m, "x")
	a["y"] = EdgeStat{Calls: 1}
	b := ensureEdge(m, "x")
	if _, ok := b["y"]; !ok {
		t.Fatal("ensureEdge must return the same map on repeat key, not a fresh one")
	}
}

// ── Directed, weighted API (Faz 5, v0.8.67) ──

// Direction must not be conflated: Downstream returns only callees,
// Upstream only callers.
func TestDownstreamUpstreamDirection(t *testing.T) {
	c := weightedSetup(t, []wedge{
		{"api", "checkout", EdgeStat{Calls: 100, Errors: 2}},
		{"api", "auth", EdgeStat{Calls: 50, Errors: 0}},
		{"gateway", "api", EdgeStat{Calls: 200, Errors: 5}},
	})

	down := c.Downstream("api")
	if len(down) != 2 {
		t.Fatalf("api has 2 downstream deps, got %d (%v)", len(down), down)
	}
	for _, e := range down {
		if e.Service == "gateway" {
			t.Fatal("upstream caller leaked into Downstream — direction conflated")
		}
	}

	up := c.Upstream("api")
	if len(up) != 1 || up[0].Service != "gateway" {
		t.Fatalf("api's only upstream is gateway, got %v", up)
	}
	if up[0].Calls != 200 || up[0].Errors != 5 {
		t.Errorf("upstream edge weight not carried: got %+v", up[0])
	}

	// A leaf in one direction returns nil.
	if got := c.Downstream("gateway"); len(got) != 1 || got[0].Service != "api" {
		t.Errorf("gateway downstream should be [api], got %v", got)
	}
	if got := c.Upstream("checkout"); len(got) != 1 || got[0].Service != "api" {
		t.Errorf("checkout upstream should be [api], got %v", got)
	}
	if got := c.Downstream("checkout"); got != nil {
		t.Errorf("checkout has no downstream, want nil, got %v", got)
	}
}

// Downstream/Upstream are sorted error-first: errors desc, then calls
// desc, then service name asc.
func TestDownstreamSortByErrors(t *testing.T) {
	c := weightedSetup(t, []wedge{
		{"api", "low", EdgeStat{Calls: 1000, Errors: 1}},
		{"api", "high", EdgeStat{Calls: 10, Errors: 9}},
		{"api", "mid", EdgeStat{Calls: 100, Errors: 5}},
	})
	down := c.Downstream("api")
	want := []string{"high", "mid", "low"}
	for i, w := range want {
		if down[i].Service != w {
			t.Fatalf("sort by errors desc: position %d want %s, got %s (%v)", i, w, down[i].Service, down)
		}
	}
}

// Tiebreak: equal errors → calls desc; equal errors+calls → name asc.
func TestDownstreamSortTiebreak(t *testing.T) {
	c := weightedSetup(t, []wedge{
		{"api", "b", EdgeStat{Calls: 50, Errors: 3}},
		{"api", "a", EdgeStat{Calls: 50, Errors: 3}}, // equal errors+calls → name asc → before b
		{"api", "c", EdgeStat{Calls: 80, Errors: 3}}, // equal errors, more calls → first
	})
	down := c.Downstream("api")
	want := []string{"c", "a", "b"}
	for i, w := range want {
		if down[i].Service != w {
			t.Fatalf("tiebreak: position %d want %s, got %s (%v)", i, w, down[i].Service, down)
		}
	}
}

func TestEdgeLookup(t *testing.T) {
	c := weightedSetup(t, []wedge{
		{"api", "checkout", EdgeStat{Calls: 100, Errors: 7}},
	})
	st, ok := c.Edge("api", "checkout")
	if !ok {
		t.Fatal("api → checkout edge should exist")
	}
	if st.Errors != 7 || st.Calls != 100 {
		t.Errorf("edge weight wrong: got %+v", st)
	}
	// Edge is directed — the reverse must not resolve.
	if _, ok := c.Edge("checkout", "api"); ok {
		t.Error("reverse edge must not exist (directed graph)")
	}
	if _, ok := c.Edge("ghost", "x"); ok {
		t.Error("unknown edge must not resolve")
	}
}

func TestEdgeStatErrorRate(t *testing.T) {
	cases := []struct {
		stat EdgeStat
		want float64
	}{
		{EdgeStat{Calls: 0, Errors: 0}, 0},   // no traffic → 0, not NaN
		{EdgeStat{Calls: 0, Errors: 5}, 0},   // guard divide-by-zero even with stray errors
		{EdgeStat{Calls: 100, Errors: 25}, 0.25},
		{EdgeStat{Calls: 4, Errors: 1}, 0.25},
		{EdgeStat{Calls: 10, Errors: 10}, 1.0},
	}
	for _, tc := range cases {
		if got := tc.stat.ErrorRate(); got != tc.want {
			t.Errorf("ErrorRate(%+v) = %v, want %v", tc.stat, got, tc.want)
		}
	}
}

// Neighbors is the union of both directions — a service that both
// calls and is called by neighbours sees all of them once.
func TestNeighborsUnionBothDirections(t *testing.T) {
	c := weightedSetup(t, []wedge{
		{"api", "checkout", EdgeStat{Calls: 100}}, // api calls checkout
		{"gateway", "api", EdgeStat{Calls: 200}},  // gateway calls api
		{"api", "gateway", EdgeStat{Calls: 5}},    // api also calls gateway (both directions)
	})
	got := c.Neighbors("api")
	seen := map[string]bool{}
	for _, n := range got {
		seen[n] = true
	}
	if len(got) != 2 || !seen["checkout"] || !seen["gateway"] {
		t.Fatalf("union(in ∪ out) should dedup to {checkout, gateway}, got %v", got)
	}
}

// ── Graph-build seam (buildGraph / applyEdges, v0.8.67) ──
// These exercise the part of refresh() that the rewrite touched most:
// the direction of the in/out maps and the weight carry-through from
// ServiceEdgePair into EdgeStat. directSetup/weightedSetup build the
// maps by hand, so without these a build-time out↔in swap or a dropped
// weight field would pass the whole suite.

func TestBuildGraphDirectionAndWeights(t *testing.T) {
	out, in := buildGraph([]chstore.ServiceEdgePair{
		{Caller: "api", Callee: "checkout", Calls: 100, Errors: 2, SumDurationNs: 5000},
		{Caller: "gateway", Callee: "api", Calls: 200, Errors: 5, SumDurationNs: 9000},
	})
	c := &Correlator{out: out, in: in}

	down := c.Downstream("api") // api → checkout
	if len(down) != 1 || down[0].Service != "checkout" {
		t.Fatalf("Downstream(api) should be [checkout] (out map), got %v", down)
	}
	if down[0].Calls != 100 || down[0].Errors != 2 || down[0].SumDurationNs != 5000 {
		t.Errorf("edge weight not carried ServiceEdgePair→EdgeStat: got %+v", down[0].EdgeStat)
	}

	up := c.Upstream("api") // gateway → api
	if len(up) != 1 || up[0].Service != "gateway" {
		t.Fatalf("Upstream(api) should be [gateway] (in map), got %v", up)
	}
	if up[0].Calls != 200 || up[0].Errors != 5 {
		t.Errorf("upstream weight not carried: got %+v", up[0].EdgeStat)
	}
	// A swap of the out/in maps in buildGraph would flip both of the
	// above — this pins caller→Downstream, callee→Upstream.
}

// The empty-result preservation invariant (correlator.go: "never
// replace with empty") is the single most important regression-class
// behaviour of this package — pin it.
func TestApplyEdgesEmptyPreservesGraph(t *testing.T) {
	c := New(nil) // store is untouched on the in-memory paths
	if replaced, _ := c.applyEdges([]chstore.ServiceEdgePair{
		{Caller: "api", Callee: "checkout", Calls: 10},
	}); !replaced {
		t.Fatal("seed: non-empty edges should replace")
	}
	if got := c.Downstream("api"); len(got) != 1 {
		t.Fatalf("seed failed, got %v", got)
	}

	// A cold/quiet window returns no edges — the live graph must survive.
	if replaced, _ := c.applyEdges(nil); replaced {
		t.Fatal("empty edge list must NOT replace the graph")
	}
	if got := c.Downstream("api"); len(got) != 1 || got[0].Service != "checkout" {
		t.Fatalf("empty refresh clobbered the live graph: got %v", got)
	}
}

func TestApplyEdgesReplacesAndCounts(t *testing.T) {
	c := New(nil)
	c.applyEdges([]chstore.ServiceEdgePair{{Caller: "api", Callee: "old", Calls: 1}})
	replaced, callers := c.applyEdges([]chstore.ServiceEdgePair{
		{Caller: "api", Callee: "new", Calls: 1},
		{Caller: "auth", Callee: "db", Calls: 1},
	})
	if !replaced || callers != 2 {
		t.Fatalf("non-empty edges should replace; replaced=%v callers=%d (want true,2)", replaced, callers)
	}
	down := c.Downstream("api")
	if len(down) != 1 || down[0].Service != "new" {
		t.Fatalf("replace must swap in the new graph (not merge), got %v", down)
	}
	if got := c.Downstream("old"); got != nil {
		t.Errorf("stale caller should be gone after replace, got %v", got)
	}
}
