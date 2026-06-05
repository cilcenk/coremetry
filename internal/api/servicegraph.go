package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// servicegraph.go — the OTel-native service-graph endpoint (v0.8.10, topology
// rebuild Stage 1). ONE compact {nodes, edges} payload built ENTIRELY from the
// pre-aggregated topology_edges_5m MV via ReadServiceTopologyAgg — the client
// never scans raw spans, and the node TYPE comes from the MV's structured
// node_kind column (derived from db.system / messaging.system / peer.service at
// MV-build time), decoded here into a clean model so the frontend drops the old
// "db:h2" → strip-prefix string hacks. Nothing in the existing topology UI is
// touched; this is purely additive (Stage 1 deletes nothing).

// GraphNode is one node in the OTel-native service map.
type GraphNode struct {
	ID        string  `json:"id"`               // canonical id (the MV's raw name, e.g. "payments" or "db:h2")
	Name      string  `json:"name"`             // display name, prefix-decoded ("payments", "h2")
	Kind      string  `json:"kind"`             // service | database | queue | external | internal
	System    string  `json:"system,omitempty"` // db.system / messaging.system when applicable
	Env       string  `json:"env,omitempty"`    // deployment.environment
	Calls     uint64  `json:"calls"`            // node throughput (inbound preferred, else outbound)
	Errors    uint64  `json:"errors"`
	ErrorRate float64 `json:"errorRate"` // (errors/calls)*100 — drives health color
}

// GraphEdge is one directed caller→callee edge carrying RED metrics + protocol.
type GraphEdge struct {
	Source    string  `json:"source"`
	Target    string  `json:"target"`
	Calls     uint64  `json:"calls"`
	Errors    uint64  `json:"errors"`
	ErrorRate float64 `json:"errorRate"`
	AvgMs     float64 `json:"avgMs"`
	P99Ms     float64 `json:"p99Ms"`
	Protocol  string  `json:"protocol,omitempty"` // http | grpc | db | kafka — SpanKind proxy
}

// ServiceGraphResponse is the compact payload the canonical renderer consumes.
type ServiceGraphResponse struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
	Scope string      `json:"scope"`
	Focus string      `json:"focus,omitempty"`
}

// nodeKindToOTel maps the MV's node_kind to the clean OTel-native kind label.
func nodeKindToOTel(k string) string {
	switch k {
	case "service":
		return "service"
	case "db":
		return "database"
	case "queue", "kafka", "messaging":
		return "queue"
	case "external":
		return "external"
	default:
		return "internal"
	}
}

// decodeNodeName strips the aggregator's "db:"/"queue:"/"ext:" name prefix and
// returns (display name, system). The KIND is taken from node_kind, never the
// prefix — the prefix only encodes the display name.
func decodeNodeName(raw string) (name, system string) {
	switch {
	case strings.HasPrefix(raw, "db:"):
		rest := strings.TrimPrefix(raw, "db:")
		sys := rest
		if at := strings.IndexByte(rest, '@'); at >= 0 { // "db:postgresql@host"
			sys = rest[:at]
		}
		return rest, sys
	case strings.HasPrefix(raw, "queue:"):
		return strings.TrimPrefix(raw, "queue:"), ""
	case strings.HasPrefix(raw, "ext:"):
		return strings.TrimPrefix(raw, "ext:"), ""
	default:
		return raw, ""
	}
}

// getServiceGraph serves GET /api/servicegraph?focus=<svc>&scope=neighborhood|global&from=&to=.
// 30s cache, key hashes all inputs (window bucketed to the minute).
func (s *Server) getOtelServiceGraph(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to := parseFromTo(r, time.Hour)
	focus := strings.TrimSpace(q.Get("focus"))
	scope := strings.TrimSpace(q.Get("scope"))
	if scope == "" {
		if focus != "" {
			scope = "neighborhood"
		} else {
			scope = "global"
		}
	}
	key := fmt.Sprintf("servicegraph:focus=%s:scope=%s:from=%d:to=%d",
		focus, scope, from.Unix()/60, to.Unix()/60)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		edges, err := s.store.ReadServiceTopologyAgg(r.Context(), from, to, 20000)
		if err != nil {
			return nil, err
		}
		return buildServiceGraph(edges, focus, scope), nil
	})
}

// buildServiceGraph is the pure transform from MV edge rows to the OTel-native
// {nodes, edges} model. Extracted so it's unit-testable without ClickHouse.
func buildServiceGraph(edges []chstore.ServiceTopologyEdge, focus, scope string) ServiceGraphResponse {
	// v0.8.11 — merge ext:<name> peers into the real service node when <name>
	// is a known service.name. A service referenced via peer.service (and seen
	// as ext:<name>) otherwise splits into a duplicate service + external node.
	// After the merge "external" means only true third parties (stripe, s3,
	// sendgrid, twilio).
	known := map[string]bool{}
	for _, e := range edges {
		known[e.ParentService] = true
		if e.NodeKind == "service" {
			known[e.ChildNode] = true
		}
	}
	merged := make([]chstore.ServiceTopologyEdge, len(edges))
	copy(merged, edges)
	for i := range merged {
		if name, ok := strings.CutPrefix(merged[i].ChildNode, "ext:"); ok && known[name] {
			merged[i].ChildNode = name
			merged[i].NodeKind = "service"
		}
	}
	edges = merged

	// Neighborhood scope: keep only edges whose BOTH endpoints are the focus or
	// a direct neighbor of the focus (callers + callees + the links among them).
	if scope == "neighborhood" && focus != "" {
		neigh := map[string]bool{focus: true}
		for _, e := range edges {
			if e.ParentService == focus {
				neigh[e.ChildNode] = true
			}
			if e.ChildNode == focus {
				neigh[e.ParentService] = true
			}
		}
		kept := make([]chstore.ServiceTopologyEdge, 0, len(edges))
		for _, e := range edges {
			if neigh[e.ParentService] && neigh[e.ChildNode] {
				kept = append(kept, e)
			}
		}
		edges = kept
	}

	nodes := map[string]*GraphNode{}
	ensure := func(id, kind string) *GraphNode {
		if n := nodes[id]; n != nil {
			return n
		}
		name, sys := decodeNodeName(id)
		n := &GraphNode{ID: id, Name: name, Kind: kind, System: sys}
		nodes[id] = n
		return n
	}

	inCalls := map[string]uint64{}
	inErrs := map[string]uint64{}
	outCalls := map[string]uint64{}
	outErrs := map[string]uint64{}

	graphEdges := make([]GraphEdge, 0, len(edges))
	for _, e := range edges {
		src := ensure(e.ParentService, "service") // a parent is always a service
		tgt := ensure(e.ChildNode, nodeKindToOTel(e.NodeKind))
		if e.ParentEnv != "" {
			src.Env = e.ParentEnv
		}
		if e.ChildEnv != "" {
			tgt.Env = e.ChildEnv
		}
		outCalls[src.ID] += e.Calls
		outErrs[src.ID] += e.Errors
		inCalls[tgt.ID] += e.Calls
		inErrs[tgt.ID] += e.Errors
		graphEdges = append(graphEdges, GraphEdge{
			Source: e.ParentService, Target: e.ChildNode,
			Calls: e.Calls, Errors: e.Errors, ErrorRate: e.ErrorRate,
			AvgMs: e.AvgMs, P99Ms: e.P99Ms, Protocol: e.Protocol,
		})
	}

	out := make([]GraphNode, 0, len(nodes))
	for _, n := range nodes {
		// Health reflects inbound traffic (errors observed calling INTO the
		// node); a root with no inbound falls back to its outbound totals.
		if c := inCalls[n.ID]; c > 0 {
			n.Calls, n.Errors = c, inErrs[n.ID]
		} else {
			n.Calls, n.Errors = outCalls[n.ID], outErrs[n.ID]
		}
		if n.Calls > 0 {
			n.ErrorRate = float64(n.Errors) / float64(n.Calls) * 100
		}
		out = append(out, *n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Calls > out[j].Calls })

	return ServiceGraphResponse{Nodes: out, Edges: graphEdges, Scope: scope, Focus: focus}
}
