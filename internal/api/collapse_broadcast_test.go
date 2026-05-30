package api

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.7.32 — broadcast collapse. A queue topic node with >threshold distinct
// consumers (config-server's kafka cache.refresh fanned out to thousands) has
// its queue→consumer edges dropped + its real consumer count returned for the
// node badge. The producer→queue edge, non-broadcast topics, and real service
// edges must all survive. CLAUDE.md #11.
func TestCollapseBroadcastQueues(t *testing.T) {
	edge := func(parent, child, kind string) chstore.ServiceTopologyEdge {
		return chstore.ServiceTopologyEdge{ParentService: parent, ChildNode: child, NodeKind: kind}
	}
	var edges []chstore.ServiceTopologyEdge
	// Broadcast: config-server → queue:kafka:cache.refresh → 5 consumers.
	edges = append(edges, edge("config-server", "queue:kafka:cache.refresh", "queue"))
	for _, c := range []string{"c1", "c2", "c3", "c4", "c5"} {
		edges = append(edges, edge("queue:kafka:cache.refresh", c, "service"))
	}
	// Small (non-broadcast) topic: 2 consumers.
	edges = append(edges, edge("orders", "queue:kafka:orders.created", "queue"))
	edges = append(edges, edge("queue:kafka:orders.created", "billing", "service"))
	edges = append(edges, edge("queue:kafka:orders.created", "shipping", "service"))
	// A real service→service edge.
	edges = append(edges, edge("frontend", "orders", "service"))

	kept, fanout := collapseBroadcastQueues(edges, 3)

	if fanout["queue:kafka:cache.refresh"] != 5 {
		t.Errorf("broadcast fanout = %d, want 5", fanout["queue:kafka:cache.refresh"])
	}
	if _, ok := fanout["queue:kafka:orders.created"]; ok {
		t.Errorf("orders.created (2 consumers ≤ threshold 3) must NOT be collapsed")
	}
	for _, e := range kept {
		if e.ParentService == "queue:kafka:cache.refresh" {
			t.Errorf("broadcast consumer edge survived: %+v", e)
		}
	}
	// Kept = producer→cache.refresh + producer→orders.created + 2 orders
	// consumers + frontend→orders = 5.
	if len(kept) != 5 {
		t.Errorf("kept %d edges, want 5: %+v", len(kept), kept)
	}
	// The producer→broadcast-queue edge survives so config-server stays visible.
	var producerKept bool
	for _, e := range kept {
		if e.ParentService == "config-server" && e.ChildNode == "queue:kafka:cache.refresh" {
			producerKept = true
		}
	}
	if !producerKept {
		t.Errorf("producer → broadcast-queue edge should survive (config-server stays on the graph)")
	}

	// No broadcast → input passes through unchanged, nil map.
	small := []chstore.ServiceTopologyEdge{edge("a", "b", "service")}
	k2, f2 := collapseBroadcastQueues(small, 3)
	if f2 != nil || len(k2) != 1 {
		t.Errorf("no-broadcast case should pass through: kept=%d map=%v", len(k2), f2)
	}
}
