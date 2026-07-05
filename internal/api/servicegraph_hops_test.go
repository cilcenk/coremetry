package api

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.8.294 — the service-detail Topology tab (FocusedNeighborhood) used to
// download the ENTIRE global graph (up to 20k MV edges on a 1000+-service
// install) just to BFS a ≤40-node neighborhood client-side. /api/servicegraph
// now walks the neighborhood server-side: scope=neighborhood gains hops=1..3.
//
// Contract of neighborhoodKeepSet — MIRRORS the client's assignFocusColumns
// walk (FocusedNeighborhood.test.ts, the v0.8.39 "won't branch at 2 hops"
// fix), so the server returns exactly the subgraph the client walk would
// have selected from the global graph:
//   - downstream reached via OUT edges only, upstream via IN edges only,
//     each direction walked to `hops` levels with its own seen-set;
//   - a caller's OTHER dependency (a "sibling") is NEVER included — that
//     path mixes directions;
//   - a node on a cycle is reachable from both directions and kept once;
//   - hops < 1 behaves as 1 (the focus's direct callers + callees).
func tedge(parent, child string) chstore.ServiceTopologyEdge {
	return chstore.ServiceTopologyEdge{ParentService: parent, ChildNode: child, NodeKind: "service", Calls: 1}
}

func keepIDs(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for id, ok := range m {
		if ok {
			out = append(out, id)
		}
	}
	return out
}

func TestNeighborhoodKeepSet(t *testing.T) {
	// a → b → focus → c → d   plus sibling: a → x
	chain := []chstore.ServiceTopologyEdge{
		tedge("a", "b"), tedge("b", "focus"), tedge("focus", "c"), tedge("c", "d"),
		tedge("a", "x"),
	}

	t.Run("hops=1 keeps focus + direct callers + direct callees only", func(t *testing.T) {
		keep := neighborhoodKeepSet(chain, "focus", 1)
		for _, want := range []string{"focus", "b", "c"} {
			if !keep[want] {
				t.Fatalf("hops=1 must keep %q; got %v", want, keepIDs(keep))
			}
		}
		for _, not := range []string{"a", "d", "x"} {
			if keep[not] {
				t.Fatalf("hops=1 must NOT keep %q; got %v", not, keepIDs(keep))
			}
		}
	})

	t.Run("hops=2 extends both directions one more level", func(t *testing.T) {
		keep := neighborhoodKeepSet(chain, "focus", 2)
		for _, want := range []string{"focus", "b", "a", "c", "d"} {
			if !keep[want] {
				t.Fatalf("hops=2 must keep %q; got %v", want, keepIDs(keep))
			}
		}
		if keep["x"] {
			t.Fatalf("a caller's other dependency (sibling) must never be kept — direction-mixing path; got %v", keepIDs(keep))
		}
	})

	t.Run("cycle node reachable both ways is kept once", func(t *testing.T) {
		keep := neighborhoodKeepSet([]chstore.ServiceTopologyEdge{tedge("focus", "a"), tedge("a", "focus")}, "focus", 1)
		if !keep["focus"] || !keep["a"] || len(keep) != 2 {
			t.Fatalf("cycle: want exactly {focus, a}; got %v", keepIDs(keep))
		}
	})

	t.Run("hops<1 behaves as 1", func(t *testing.T) {
		keep := neighborhoodKeepSet(chain, "focus", 0)
		if !keep["b"] || !keep["c"] || keep["a"] || keep["d"] {
			t.Fatalf("hops=0 must equal hops=1; got %v", keepIDs(keep))
		}
	})

	t.Run("focus absent from edges keeps only the focus", func(t *testing.T) {
		keep := neighborhoodKeepSet(chain, "ghost", 2)
		if len(keep) != 1 || !keep["ghost"] {
			t.Fatalf("unknown focus: want {ghost}; got %v", keepIDs(keep))
		}
	})
}

// serviceGraphHopsClamp resolves ?hops= the same way serviceGraphTopNClamp
// resolves ?topN=: pure, garbage-tolerant, scope-aware. Neighborhood scope
// defaults to 1 and clamps to [1,3]; any other scope returns 0 (hops is
// meaningless on a global read and must not fragment the cache key).
func TestServiceGraphHopsClamp(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		scope string
		want  int
	}{
		{"absent on neighborhood = 1 hop", "", "neighborhood", 1},
		{"explicit 1", "1", "neighborhood", 1},
		{"explicit 2", "2", "neighborhood", 2},
		{"ceiling 3", "3", "neighborhood", 3},
		{"above ceiling clamps to 3", "9", "neighborhood", 3},
		{"zero = 1", "0", "neighborhood", 1},
		{"negative = 1", "-2", "neighborhood", 1},
		{"garbage = 1", "abc", "neighborhood", 1},
		{"global scope = 0 (no cache-key fragmentation)", "2", "global", 0},
		{"global absent = 0", "", "global", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := serviceGraphHopsClamp(c.raw, c.scope); got != c.want {
				t.Fatalf("serviceGraphHopsClamp(%q, %q) = %d, want %d", c.raw, c.scope, got, c.want)
			}
		})
	}
}

// buildServiceGraph end-to-end at hops=2: the neighborhood filter must use the
// direction-separated keep-set, so a 2-hop chain survives while the sibling
// branch is dropped, and edge rollups only cover kept edges.
func TestBuildServiceGraph_NeighborhoodTwoHops(t *testing.T) {
	edges := []chstore.ServiceTopologyEdge{
		tedge("a", "b"), tedge("b", "focus"), tedge("focus", "c"), tedge("c", "d"),
		tedge("a", "x"),
	}
	g := buildServiceGraph(edges, "focus", "neighborhood", 2, nil, 1)
	got := map[string]bool{}
	for _, n := range g.Nodes {
		got[n.ID] = true
	}
	for _, want := range []string{"a", "b", "focus", "c", "d"} {
		if !got[want] {
			t.Fatalf("hops=2 build must keep %q; got %v", want, g.Nodes)
		}
	}
	if got["x"] {
		t.Fatalf("sibling x must be dropped; got %v", g.Nodes)
	}
	if len(g.Edges) != 4 {
		t.Fatalf("want the 4 chain edges, got %d: %v", len(g.Edges), g.Edges)
	}
}
