package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.8.10 (topology rebuild Stage 1) — the OTel-native {nodes,edges} model is
// built purely from topology_edges_5m MV rows. These assertions pin the
// node-kind decode (from the structured node_kind column, NOT name prefixes),
// the prefix-stripped display names, RED edge metrics, broker/db as first-class
// nodes, cycle handling, and neighborhood scoping — the exact behaviours the
// hand-rolled tangle got wrong.

func sgEdge(parent, child, kind, proto string, calls, errs uint64, p99 float64) chstore.ServiceTopologyEdge {
	e := chstore.ServiceTopologyEdge{
		ParentService: parent, ChildNode: child, NodeKind: kind, Protocol: proto,
		Calls: calls, Errors: errs, P99Ms: p99,
	}
	if calls > 0 {
		e.ErrorRate = float64(errs) / float64(calls) * 100
	}
	return e
}

func sampleEdges() []chstore.ServiceTopologyEdge {
	return []chstore.ServiceTopologyEdge{
		sgEdge("gateway", "payments", "service", "http", 1000, 10, 120),
		sgEdge("payments", "ledger", "service", "grpc", 800, 40, 90),
		sgEdge("ledger", "gateway", "service", "grpc", 50, 0, 30), // cycle gateway→payments→ledger→gateway
		sgEdge("payments", "db:postgresql", "db", "db", 1500, 3, 8),
		sgEdge("payments", "queue:settlements", "queue", "kafka", 200, 0, 5), // broker is first-class
		sgEdge("orders", "ext:stripe.com", "external", "http", 60, 6, 400),
	}
}

func TestBuildServiceGraph_GlobalDecodesOTelKinds(t *testing.T) {
	g := buildServiceGraph(sampleEdges(), "", "global", nil, 60)
	byID := map[string]GraphNode{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}

	// db / queue / external are first-class nodes with OTel-native kinds and
	// prefix-decoded names — no "db:"/"queue:" leaking to the client.
	cases := []struct{ id, wantKind, wantName, wantSystem string }{
		{"db:postgresql", "database", "postgresql", "postgresql"},
		{"queue:settlements", "queue", "settlements", ""},
		{"ext:stripe.com", "external", "stripe.com", ""},
		{"payments", "service", "payments", ""},
	}
	for _, c := range cases {
		n, ok := byID[c.id]
		if !ok {
			t.Fatalf("node %q missing", c.id)
		}
		if n.Kind != c.wantKind {
			t.Errorf("%s kind = %q, want %q (must come from node_kind, not a prefix guess)", c.id, n.Kind, c.wantKind)
		}
		if n.Name != c.wantName {
			t.Errorf("%s name = %q, want %q (prefix must be decoded)", c.id, n.Name, c.wantName)
		}
		if n.System != c.wantSystem {
			t.Errorf("%s system = %q, want %q", c.id, n.System, c.wantSystem)
		}
	}

	// Cycle renders without special-casing: all 3 services + their edge present.
	if len(g.Edges) != 6 {
		t.Errorf("edges = %d, want 6 (cycle edge must survive)", len(g.Edges))
	}

	// Node health from inbound traffic: ledger gets 800 calls / 40 errors inbound.
	if l := byID["ledger"]; l.Calls != 800 || l.Errors != 40 || l.ErrorRate != 5 {
		t.Errorf("ledger health = {calls:%d errors:%d rate:%.1f}, want {800 40 5.0}", l.Calls, l.Errors, l.ErrorRate)
	}

	// Sample JSON for the Stage-1 deliverable.
	if b, err := json.MarshalIndent(g, "", "  "); err == nil {
		t.Logf("global sample:\n%s", b)
	}
}

func TestBuildServiceGraph_MergeExtIntoService(t *testing.T) {
	edges := []chstore.ServiceTopologyEdge{
		sgEdge("gateway", "payments", "service", "http", 500, 0, 50),     // payments is a real service
		sgEdge("mobile", "ext:payments", "external", "http", 100, 5, 80), // same service, seen via peer.service
		sgEdge("orders", "ext:stripe.com", "external", "http", 60, 6, 400), // true 3rd party
	}
	g := buildServiceGraph(edges, "", "global", nil, 60)
	byID := map[string]GraphNode{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	if _, dup := byID["ext:payments"]; dup {
		t.Error("ext:payments must merge into the payments service node, not stay a duplicate")
	}
	p, ok := byID["payments"]
	if !ok || p.Kind != "service" {
		t.Fatalf("payments node missing or wrong kind: %+v", p)
	}
	if p.Calls != 600 || p.Errors != 5 { // 500 + merged 100, errors 0 + 5
		t.Errorf("merged payments health = {calls:%d errors:%d}, want {600 5}", p.Calls, p.Errors)
	}
	if s := byID["ext:stripe.com"]; s.Kind != "external" {
		t.Errorf("a true third party (stripe.com) must stay external, got %q", s.Kind)
	}
}

func TestBuildServiceGraph_NeighborhoodScope(t *testing.T) {
	g := buildServiceGraph(sampleEdges(), "payments", "neighborhood", nil, 60)
	if g.Scope != "neighborhood" || g.Focus != "payments" {
		t.Fatalf("scope/focus = %q/%q", g.Scope, g.Focus)
	}
	ids := map[string]bool{}
	for _, n := range g.Nodes {
		ids[n.ID] = true
	}
	// payments' neighborhood: gateway (caller), ledger + db + queue (callees).
	for _, want := range []string{"payments", "gateway", "ledger", "db:postgresql", "queue:settlements"} {
		if !ids[want] {
			t.Errorf("neighborhood missing %q", want)
		}
	}
	// orders/stripe are NOT in payments' neighborhood.
	if ids["ext:stripe.com"] || ids["orders"] {
		t.Errorf("neighborhood leaked an unrelated node: %v", ids)
	}
}

// v0.8.37 (topology renewed delta 2) — database nodes (keyed on db.system)
// are enriched with the dominant db.name from db_summary_5m. The map is
// looked up by GraphNode.System; a nil map or unknown system is a no-op, and
// only database nodes (which carry a System) pick one up.
func TestBuildServiceGraph_DbNameEnrichment(t *testing.T) {
	dbNames := map[string]string{"postgresql": "core_txn"}
	g := buildServiceGraph(sampleEdges(), "", "global", dbNames, 60)
	byID := map[string]GraphNode{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	if db := byID["db:postgresql"]; db.DbName != "core_txn" {
		t.Errorf("db:postgresql DbName = %q, want %q", db.DbName, "core_txn")
	}
	// A service node (System == "") never gets a db.name.
	if p := byID["payments"]; p.DbName != "" {
		t.Errorf("service node payments must not carry db.name, got %q", p.DbName)
	}
	// nil map → no enrichment, no panic.
	for _, n := range buildServiceGraph(sampleEdges(), "", "global", nil, 60).Nodes {
		if n.DbName != "" {
			t.Errorf("nil dbNames must leave DbName empty, %s got %q", n.ID, n.DbName)
		}
	}
}

// v0.8.x (Uptrace service-graph adaptation, slice 1) — calls/min Rate. The
// window-minutes divide is unit-mixing-prone: a sub-minute or zero window must
// floor to 1 (no divide-by-zero, no inflated rate). Per the unit-mixing
// regression-test discipline, exercise every branch.
func TestServiceGraphWindowMinutes(t *testing.T) {
	base := time.Now()
	cases := []struct {
		name     string
		from, to time.Time
		want     float64
	}{
		{"one hour", base, base.Add(time.Hour), 60},
		{"five minutes", base, base.Add(5 * time.Minute), 5},
		{"exactly one minute", base, base.Add(time.Minute), 1},
		{"sub-minute floors to 1", base, base.Add(30 * time.Second), 1},
		{"zero window floors to 1", base, base, 1},
		{"inverted window floors to 1", base.Add(time.Hour), base, 1},
	}
	for _, c := range cases {
		if got := serviceGraphWindowMinutes(c.from, c.to); got != c.want {
			t.Errorf("%s: serviceGraphWindowMinutes = %v, want %v", c.name, got, c.want)
		}
	}
}

// rate = calls / windowMinutes must ride every node + edge, and buildServiceGraph
// must independently floor windowMin so a bad caller can't divide-by-zero.
func TestBuildServiceGraph_RatePerMinute(t *testing.T) {
	g := buildServiceGraph([]chstore.ServiceTopologyEdge{
		sgEdge("gateway", "payments", "service", "http", 1200, 0, 50),
	}, "", "global", nil, 60) // 1h window → 1200/60 = 20 calls/min
	var edgeRate float64
	for _, e := range g.Edges {
		if e.Source == "gateway" && e.Target == "payments" {
			edgeRate = e.Rate
		}
	}
	if edgeRate != 20 {
		t.Errorf("edge rate = %v, want 20 (1200 calls / 60 min)", edgeRate)
	}
	for _, n := range g.Nodes {
		if n.ID == "payments" && n.Rate != 20 { // payments: 1200 inbound calls
			t.Errorf("payments node rate = %v, want 20", n.Rate)
		}
	}
	// guard: windowMin<1 floored to 1 inside buildServiceGraph (rate = calls).
	g0 := buildServiceGraph([]chstore.ServiceTopologyEdge{
		sgEdge("a", "b", "service", "http", 5, 0, 1),
	}, "", "global", nil, 0)
	if g0.Edges[0].Rate != 5 {
		t.Errorf("windowMin=0 must floor to 1 → rate=calls=5, got %v", g0.Edges[0].Rate)
	}
}
