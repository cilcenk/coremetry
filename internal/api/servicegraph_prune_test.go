package api

import "testing"

// v0.8.277 — T1 of the /service-map promotion: the MV-backed /api/servicegraph
// becomes the GLOBAL topology source, so it needs the same overview cap the
// sampled /api/service-map got in v0.8.215 (don't draw the whole power-law
// graph). Contract of pruneServiceGraphTopN:
//   - rank nodes by Calls desc, ErrorRate desc tiebreak (a high-error node
//     survives a throughput tie), ID asc as the stable final tiebreak;
//   - keep the topN heaviest, drop edges unless BOTH endpoints survive;
//   - ALWAYS set TotalNodes/ShownNodes (the "showing X of Y" UI reads them);
//   - topN <= 0 or a within-budget graph = no prune.
func gnode(id string, calls uint64, errRate float64) GraphNode {
	return GraphNode{ID: id, Name: id, Kind: "service", Calls: calls, ErrorRate: errRate}
}

func TestPruneServiceGraphTopN(t *testing.T) {
	t.Run("topN 0 = no cap, counts still set", func(t *testing.T) {
		g := ServiceGraphResponse{Nodes: []GraphNode{gnode("a", 5, 0), gnode("b", 3, 0)}}
		pruneServiceGraphTopN(&g, 0)
		if g.TotalNodes != 2 || g.ShownNodes != 2 {
			t.Fatalf("counts = %d/%d, want 2/2", g.ShownNodes, g.TotalNodes)
		}
		if len(g.Nodes) != 2 {
			t.Fatalf("topN 0 must not prune; got %d nodes", len(g.Nodes))
		}
	})

	t.Run("topN >= len = no prune", func(t *testing.T) {
		g := ServiceGraphResponse{Nodes: []GraphNode{gnode("a", 5, 0), gnode("b", 3, 0)}}
		pruneServiceGraphTopN(&g, 5)
		if len(g.Nodes) != 2 || g.ShownNodes != 2 {
			t.Fatalf("graph within budget must not prune")
		}
	})

	t.Run("keeps heaviest by calls + filters dangling edges", func(t *testing.T) {
		g := ServiceGraphResponse{
			Nodes: []GraphNode{gnode("a", 100, 0), gnode("b", 50, 0), gnode("c", 1, 0)},
			Edges: []GraphEdge{
				{Source: "a", Target: "b"}, // both survive top-2
				{Source: "b", Target: "c"}, // c dropped → edge dropped
			},
		}
		pruneServiceGraphTopN(&g, 2)
		if g.ShownNodes != 2 || g.TotalNodes != 3 {
			t.Fatalf("counts = %d/%d, want 2/3", g.ShownNodes, g.TotalNodes)
		}
		kept := map[string]bool{}
		for _, n := range g.Nodes {
			kept[n.ID] = true
		}
		if !kept["a"] || !kept["b"] || kept["c"] {
			t.Fatalf("kept set wrong: %v (want a,b not c)", kept)
		}
		if len(g.Edges) != 1 || g.Edges[0].Target != "b" {
			t.Fatalf("dangling edge to dropped node must be filtered; got %v", g.Edges)
		}
	})

	t.Run("error-rate breaks a calls tie", func(t *testing.T) {
		g := ServiceGraphResponse{Nodes: []GraphNode{
			gnode("calm", 10, 0.0),
			gnode("hot", 10, 50), // same calls, higher error rate → survives
			gnode("filler", 10, 10),
		}}
		pruneServiceGraphTopN(&g, 1)
		if len(g.Nodes) != 1 || g.Nodes[0].ID != "hot" {
			t.Fatalf("tie should keep the highest-error node; got %v", g.Nodes)
		}
	})

	t.Run("id is the stable final tiebreak", func(t *testing.T) {
		g := ServiceGraphResponse{Nodes: []GraphNode{
			gnode("zeta", 10, 1), gnode("alpha", 10, 1), gnode("mid", 10, 1),
		}}
		pruneServiceGraphTopN(&g, 2)
		if len(g.Nodes) != 2 || g.Nodes[0].ID != "alpha" || g.Nodes[1].ID != "mid" {
			t.Fatalf("full tie must resolve by ID asc (deterministic cache-safe order); got %v", g.Nodes)
		}
	})

	t.Run("nil response is a no-op", func(t *testing.T) {
		pruneServiceGraphTopN(nil, 10) // must not panic
	})
}

// v0.8.278 (operator-reported: "full topology bankada çok zor gözükmez") — the
// GLOBAL map is NEVER uncapped. topN<=0 / absent / garbage now clamps to the
// 500-node render budget: at the 1000+-service bank install an uncapped
// "All services" request stalls the dagre layout and draws an unreadable
// hairball. Neighborhood scope stays 0 (already focus-scoped, never pruned).
func TestServiceGraphTopNClamp(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		scope string
		want  int
	}{
		{"absent param on global = render budget", "", "global", 500},
		{"explicit 0 (old All-services links) = render budget", "0", "global", 500},
		{"negative = render budget", "-5", "global", 500},
		{"garbage = render budget", "abc", "global", 500},
		{"in-range passes through", "100", "global", 100},
		{"above budget clamps down", "9999", "global", 500},
		{"exactly the budget", "500", "global", 500},
		{"neighborhood is never pruned", "50", "neighborhood", 0},
		{"neighborhood absent", "", "neighborhood", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := serviceGraphTopNClamp(c.raw, c.scope); got != c.want {
				t.Fatalf("serviceGraphTopNClamp(%q, %q) = %d, want %d", c.raw, c.scope, got, c.want)
			}
		})
	}
}
