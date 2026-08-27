package api

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.8.264 — operator-reported: kafka:log*/kafka:bsa* queue nodes
// (the config-server drops a kafka client into every project)
// drowned the service-detail Topology tab and /topology's focused
// view. Root cause: /api/servicegraph never applied the v0.8.241
// hidden-pattern policy that /api/topology and /api/service-map
// already enforced. Pins the edge filter both ways (hidden parent
// AND hidden child) plus the queue:-prefix strip the shared
// matcher performs.
func TestFilterHiddenTopologyEdges(t *testing.T) {
	edge := func(parent, child string) chstore.ServiceTopologyEdge {
		return chstore.ServiceTopologyEdge{ParentService: parent, ChildNode: child, Calls: 1}
	}
	patterns := []string{"kafka:log*", "kafka:bsa*"}

	in := []chstore.ServiceTopologyEdge{
		edge("bsa-mgts-smssender", "oracle@orabcore-prod"),             // kept
		edge("bsa-mgts-smssender", "kafka:log.service.bsa.stat"),       // hidden child
		edge("kafka:bsa.kafka.core.cfg", "bsa-customer-maininq-prod"),  // hidden parent
		edge("bsa-framework-batchjob", "queue:kafka:bsa.log.core.svc"), // queue: prefix stripped → hidden
		edge("bsa-callcenter-core", "bsa-bpm-kbds"),                    // kept
		edge("bsa-loan-consumer", "kafka:payments.orders"),             // kafka but NOT log*/bsa* → kept
	}

	// No patterns → passthrough. Checked FIRST because the filter
	// compacts in place (edges[:0]) — the handler always hands it a
	// fresh slice, but reusing `in` after a filtering call would
	// assert against mutated data.
	if got := filterHiddenTopologyEdges(in, nil); len(got) != len(in) {
		t.Fatalf("empty pattern set must keep every edge (got %d/%d)", len(got), len(in))
	}

	out := filterHiddenTopologyEdges(in, patterns)
	if len(out) != 3 {
		t.Fatalf("kept %d edges, want 3: %+v", len(out), out)
	}
	wantChildren := map[string]bool{
		"oracle@orabcore-prod": true, "bsa-bpm-kbds": true, "kafka:payments.orders": true,
	}
	for _, e := range out {
		if !wantChildren[e.ChildNode] {
			t.Fatalf("unexpected surviving edge %q → %q", e.ParentService, e.ChildNode)
		}
	}
}
