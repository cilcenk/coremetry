package chstore

import "testing"

// v0.8.215 — /service-map rendered the whole sampled graph, which is an
// unreadable hairball at 1000s of services (topology-viz research: don't draw
// the whole power-law graph). pruneServiceMapTopN is the pure overview cap: keep
// the topN heaviest nodes (SpanCount desc, ErrorRate tiebreak so a high-error
// node survives a tie), drop edges whose endpoints didn't survive, and set
// TotalNodes/ShownNodes for the "X of Y" UI. Pin the contract.
func node(svc string, span int, err float64) ServiceMapNode {
	return ServiceMapNode{Service: svc, SpanCount: span, ErrorRate: err}
}

func TestPruneServiceMapTopN(t *testing.T) {
	t.Run("topN 0 = no cap, counts still set", func(t *testing.T) {
		m := &ServiceMap{Nodes: []ServiceMapNode{node("a", 5, 0), node("b", 3, 0)}}
		pruneServiceMapTopN(m, 0)
		if m.TotalNodes != 2 || m.ShownNodes != 2 {
			t.Fatalf("counts = %d/%d, want 2/2", m.ShownNodes, m.TotalNodes)
		}
		if len(m.Nodes) != 2 {
			t.Fatalf("topN 0 must not prune; got %d nodes", len(m.Nodes))
		}
	})

	t.Run("topN >= len = no prune", func(t *testing.T) {
		m := &ServiceMap{Nodes: []ServiceMapNode{node("a", 5, 0), node("b", 3, 0)}}
		pruneServiceMapTopN(m, 5)
		if len(m.Nodes) != 2 || m.ShownNodes != 2 {
			t.Fatalf("graph within budget must not prune")
		}
	})

	t.Run("keeps heaviest + filters dangling edges", func(t *testing.T) {
		m := &ServiceMap{
			Nodes: []ServiceMapNode{node("a", 100, 0), node("b", 50, 0), node("c", 1, 0)},
			Edges: []ServiceMapEdge{
				{Caller: "a", Callee: "b"}, // both survive top-2
				{Caller: "b", Callee: "c"}, // c is dropped → edge dropped
			},
		}
		pruneServiceMapTopN(m, 2)
		if m.ShownNodes != 2 || m.TotalNodes != 3 {
			t.Fatalf("counts = %d/%d, want 2/3", m.ShownNodes, m.TotalNodes)
		}
		got := map[string]bool{}
		for _, n := range m.Nodes {
			got[n.Service] = true
		}
		if !got["a"] || !got["b"] || got["c"] {
			t.Fatalf("kept set wrong: %v (want a,b not c)", got)
		}
		if len(m.Edges) != 1 || m.Edges[0].Callee != "b" {
			t.Fatalf("dangling edge to dropped node must be filtered; got %v", m.Edges)
		}
	})

	t.Run("error-rate breaks a span-count tie", func(t *testing.T) {
		m := &ServiceMap{Nodes: []ServiceMapNode{
			node("calm", 10, 0.0),
			node("hot", 10, 0.5), // same span count, higher errors → survives
			node("filler", 10, 0.1),
		}}
		pruneServiceMapTopN(m, 1)
		if len(m.Nodes) != 1 || m.Nodes[0].Service != "hot" {
			t.Fatalf("tie should keep the highest-error node; got %v", m.Nodes)
		}
	})
}
