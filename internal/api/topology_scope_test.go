package api

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.6.48 — guards the server-side topology scope bound that
// replaced the "ship all 5k edges, let the browser rank" path.
// Operator-reported: topology froze + drew an unreadable hairball
// at thousand-service scale. These pin the three pure helpers that
// bound the graph before it leaves the server.

func edge(parent, child, kind string, calls uint64) chstore.ServiceTopologyEdge {
	return chstore.ServiceTopologyEdge{
		ParentService: parent,
		ChildNode:     child,
		NodeKind:      kind,
		Calls:         calls,
	}
}

func TestCountDistinctServices(t *testing.T) {
	edges := []chstore.ServiceTopologyEdge{
		edge("a", "b", "service", 10),
		edge("b", "c", "service", 5),
		edge("a", "db:pg", "db", 99),    // db child not counted
		edge("c", "queue:k", "queue", 3), // queue child not counted
	}
	// distinct services: a, b, c = 3 (db:pg / queue:k excluded)
	if got := countDistinctServices(edges); got != 3 {
		t.Errorf("countDistinctServices = %d; want 3", got)
	}
}

func TestTopNServiceEdges(t *testing.T) {
	// Volume: hub=300 (100+200), a=100, b=200+? Let's build a clear
	// ranking. hub calls a(100) and b(200); a calls b(50).
	//   hub: out 300                       → 300
	//   a:   in 100 + out 50               → 150
	//   b:   in 200 + 50                   → 250
	edges := []chstore.ServiceTopologyEdge{
		edge("hub", "a", "service", 100),
		edge("hub", "b", "service", 200),
		edge("a", "b", "service", 50),
		edge("a", "db:pg", "db", 9), // infra child rides along with kept parent a
	}
	// top-2 services by volume: hub(300), b(250). a(150) dropped.
	kept := topNServiceEdges(edges, 2)
	// Only edges with both endpoints in {hub,b} survive among
	// service edges: hub→b. hub→a dropped (a not kept). a→b dropped
	// (a not kept, it's the parent). a→db dropped (a not kept).
	if len(kept) != 1 {
		t.Fatalf("topNServiceEdges kept %d edges; want 1 (hub→b). got %+v", len(kept), kept)
	}
	if kept[0].ParentService != "hub" || kept[0].ChildNode != "b" {
		t.Errorf("kept edge = %s→%s; want hub→b", kept[0].ParentService, kept[0].ChildNode)
	}
}

func TestTopNServiceEdges_InfraChildRidesAlong(t *testing.T) {
	// A kept service's db/queue child should survive even though
	// it's not a "service" in the top-N ranking.
	edges := []chstore.ServiceTopologyEdge{
		edge("a", "b", "service", 100),
		edge("a", "db:pg", "db", 5),
	}
	kept := topNServiceEdges(edges, 5) // n > services, keep all
	if len(kept) != 2 {
		t.Errorf("expected both edges (incl a→db:pg infra child); got %d", len(kept))
	}
}

func TestFocusNeighborhood(t *testing.T) {
	// Chain: x→y→z→w, plus a side edge p→q unrelated to x.
	edges := []chstore.ServiceTopologyEdge{
		edge("x", "y", "service", 1),
		edge("y", "z", "service", 1),
		edge("z", "w", "service", 1),
		edge("p", "q", "service", 1),
	}
	// focus=x, 1 hop → only x→y
	h1 := focusNeighborhood(edges, "x", 1)
	if len(h1) != 1 || h1[0].ChildNode != "y" {
		t.Errorf("focus x +1hop = %+v; want [x→y]", h1)
	}
	// focus=x, 2 hops → x→y, y→z
	h2 := focusNeighborhood(edges, "x", 2)
	if len(h2) != 2 {
		t.Errorf("focus x +2hop kept %d edges; want 2 (x→y, y→z)", len(h2))
	}
	// focus=x, 3 hops → x→y, y→z, z→w (p→q never reached)
	h3 := focusNeighborhood(edges, "x", 3)
	if len(h3) != 3 {
		t.Errorf("focus x +3hop kept %d edges; want 3", len(h3))
	}
	for _, e := range h3 {
		if e.ParentService == "p" {
			t.Errorf("focus x reached unrelated edge p→q: %+v", e)
		}
	}
}

func TestFocusNeighborhood_Bidirectional(t *testing.T) {
	// caller → focus → callee. Bidirectional BFS at 1 hop should
	// pull BOTH the upstream caller and the downstream callee so the
	// client's dir=both ("who calls X") view has data to render.
	edges := []chstore.ServiceTopologyEdge{
		edge("caller", "focus", "service", 1),
		edge("focus", "callee", "service", 1),
		edge("stranger", "elsewhere", "service", 1),
	}
	got := focusNeighborhood(edges, "focus", 1)
	if len(got) != 2 {
		t.Fatalf("focus +1hop bidirectional kept %d edges; want 2 (caller→focus, focus→callee). got %+v", len(got), got)
	}
	sawCaller, sawCallee := false, false
	for _, e := range got {
		if e.ParentService == "caller" {
			sawCaller = true
		}
		if e.ChildNode == "callee" {
			sawCallee = true
		}
	}
	if !sawCaller || !sawCallee {
		t.Errorf("bidirectional focus missing a side: caller=%v callee=%v", sawCaller, sawCallee)
	}
}
