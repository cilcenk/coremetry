package chstore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ServiceMapNode is one node in the global topology graph. The
// frontend renders these as nodes in a force-directed layout; node
// size scales with span count so visually-heavy nodes sit at the
// top of the operator's attention.
//
// Kind discriminates real OTel services (Kind="") from synthetic
// "infrastructure" nodes (Kind="db" / "queue" / "external") that
// represent the things services talk to but that don't emit OTel
// data themselves. The frontend renders the two kinds with
// distinct shapes so an operator can tell at a glance whether a
// node is "your code" or "your dependency".
type ServiceMapNode struct {
	Service   string `json:"service"`
	SpanCount int    `json:"spanCount"`
	// ErrorRate is computed across all spans of this service in the
	// sampled traces. Used to colour the node — green=healthy,
	// red=error-heavy — without re-querying per-node.
	ErrorRate float64 `json:"errorRate"`
	// Kind: "" = real service emitting OTel data; "db" =
	// db.system synthesised dependency (redis / oracle / mysql /
	// …); "queue" = messaging.system synthesised dependency
	// (kafka / rabbitmq / …); "external" = peer.service'd HTTP
	// endpoint that isn't an OTel service.
	Kind      string `json:"kind,omitempty"`
	// DBSystem / Subkind carries the underlying type so the UI
	// can show "redis" or "postgresql" rather than just "db".
	Subkind   string `json:"subkind,omitempty"`
	// DbName (v0.8.297) — dominant db.name for this node's db.system
	// (db_summary_5m via DbNamesBySystem), best-effort read-time
	// enrichment: the pill shows WHICH database ("COREBANK"), not just
	// the engine ("oracle"). db nodes only; empty when the system
	// never reports db.name.
	DbName    string `json:"dbName,omitempty"`
	// IsNew is set by GetServiceMapWithDiff when this node didn't
	// appear in the baseline window (e.g. yesterday's same slot).
	// Frontend pulses these green so a freshly-deployed service or
	// newly-discovered dependency stands out at a glance.
	IsNew     bool   `json:"isNew,omitempty"`
	// Cluster — the k8s/openshift cluster this service ran in
	// during the sampled window. Populated server-side via
	// GetServiceClusterMap as a read-time enrichment so the
	// frontend can group / colour / filter the map by cluster
	// without an N+1 lookup. Empty when the SDK didn't ship a
	// cluster resource attribute. "multi" when the service
	// spans more than one cluster in the window — frontend
	// renders these with a distinct chip so an operator
	// scanning a topology hairball still spots the boundary
	// crossings.
	Cluster   string `json:"cluster,omitempty"`
}

// ServiceMapEdge is a directed call: caller → callee. Weight =
// number of distinct sampled traces in which the edge appeared
// (so a one-off edge from a single trace doesn't visually equal
// a hot path that runs every request). ErrorCount is the count
// of CALLEE spans on this edge that returned an error status.
type ServiceMapEdge struct {
	Caller     string `json:"caller"`
	Callee     string `json:"callee"`
	TraceCount int    `json:"traceCount"`
	SpanCount  int    `json:"spanCount"`
	ErrorCount int    `json:"errorCount"`
	// IsNew is set when the (caller, callee) pair didn't appear
	// in the baseline window. A new edge typically signals either
	// a feature deploy that wired up a previously-decoupled service
	// or a regression where a code path started talking to an
	// unintended dependency.
	IsNew      bool   `json:"isNew,omitempty"`
}

// ServiceMap is the wire format returned to the frontend.
//
// RemovedNodes / RemovedEdges are populated by
// GetServiceMapWithDiff when a baseline window is supplied: they
// list services and dependencies that were active in the baseline
// but have stopped appearing in the current window. Useful for
// catching "we silently dropped a downstream call" regressions.
type ServiceMap struct {
	Nodes         []ServiceMapNode `json:"nodes"`
	Edges         []ServiceMapEdge `json:"edges"`
	RemovedNodes  []ServiceMapNode `json:"removedNodes,omitempty"`
	RemovedEdges  []ServiceMapEdge `json:"removedEdges,omitempty"`
	SampledFrom   int              `json:"sampledFrom"`  // traces actually inspected
	TotalSpans    int              `json:"totalSpans"`   // span count across them
	BaselineAgo   string           `json:"baselineAgo,omitempty"` // e.g. "24h" — echoed for UI labelling
	// TotalNodes / ShownNodes (v0.8.215) — set by pruneServiceMapTopN. When the
	// overview top-N cap trims a large graph, ShownNodes < TotalNodes and the UI
	// shows "showing X of Y services" so the operator knows the map is pruned,
	// not the whole truth.
	TotalNodes    int              `json:"totalNodes"`
	ShownNodes    int              `json:"shownNodes"`
}

// pruneServiceMapTopN bounds the overview graph to the topN heaviest nodes so a
// 1000s-service production map renders as a readable graph instead of a hairball
// (topology-viz research: don't draw the whole power-law graph — show the heavy
// subgraph). Nodes are ranked by SpanCount desc with ErrorRate as the tiebreak,
// so a high-error node survives the cut even when it isn't the busiest. Edges are
// kept only when BOTH endpoints survive. TotalNodes/ShownNodes are always set so
// the UI can render "showing X of Y". topN<=0 or an already-within-budget graph
// is returned unpruned (existing /service-map behaviour). Pure + order-stable so
// it's unit-tested. v0.8.215.
func pruneServiceMapTopN(m *ServiceMap, topN int) {
	if m == nil {
		return
	}
	m.TotalNodes = len(m.Nodes)
	if topN <= 0 || len(m.Nodes) <= topN {
		m.ShownNodes = len(m.Nodes)
		return
	}
	ranked := append([]ServiceMapNode(nil), m.Nodes...)
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].SpanCount != ranked[j].SpanCount {
			return ranked[i].SpanCount > ranked[j].SpanCount
		}
		if ranked[i].ErrorRate != ranked[j].ErrorRate {
			return ranked[i].ErrorRate > ranked[j].ErrorRate
		}
		return ranked[i].Service < ranked[j].Service
	})
	kept := ranked[:topN]
	keepSet := make(map[string]bool, topN)
	for _, n := range kept {
		keepSet[n.Service] = true
	}
	edges := make([]ServiceMapEdge, 0, len(m.Edges))
	for _, e := range m.Edges {
		if keepSet[e.Caller] && keepSet[e.Callee] {
			edges = append(edges, e)
		}
	}
	m.Nodes = kept
	m.Edges = edges
	m.ShownNodes = topN
}

// PruneServiceMapTopN is the exported wrapper the api package calls; the logic
// lives in the pure pruneServiceMapTopN so the cap is unit-tested without a Store.
func (s *Store) PruneServiceMapTopN(m *ServiceMap, topN int) { pruneServiceMapTopN(m, topN) }

// annotateDbNames stamps each db node with the dominant db.name for its
// db.system (v0.8.297 — the same DbNamesBySystem enrichment the MV-backed
// /api/servicegraph has had since v0.8.37, applied to the sampled map).
// Pure: only Kind=="db" nodes, never overwrites, nil/unknown are no-ops.
func annotateDbNames(nodes []ServiceMapNode, dbNames map[string]string) {
	if len(dbNames) == 0 {
		return
	}
	for i := range nodes {
		if nodes[i].Kind != "db" || nodes[i].DbName != "" {
			continue
		}
		if dn := dbNames[nodes[i].Subkind]; dn != "" {
			nodes[i].DbName = dn
		}
	}
}

// AnnotateDbNames is the exported wrapper the api package calls.
func (s *Store) AnnotateDbNames(nodes []ServiceMapNode, dbNames map[string]string) {
	annotateDbNames(nodes, dbNames)
}

// spanStatusIsError reports whether a span's status_code column value
// denotes an error. The ingest path (internal/otlp/convert.go) maps
// the OTLP STATUS_CODE_ERROR enum to the lowercase token "error"
// before the CH write, so that — case-insensitively — is the ONLY
// value the topology walk must treat as a failure. The legacy
// predicate here compared against the OTLP enum names
// ("STATUS_CODE_ERROR" / "ERROR" / "Error") which never appear in the
// column, so every node errorRate and edge errorCount came back 0
// while services ran 1-6% errors (red-edge/amber-node rendering on the
// /service-map preview was dead). Kept as a pure func so the
// regression test can pin every observed token.
func spanStatusIsError(statusCode string) bool {
	return strings.EqualFold(statusCode, "error")
}

// GetServiceMap derives the global service-level topology from a
// bounded sample of recent traces. Mirrors the ServiceNeighbors
// approach but globally — no anchor service. Two queries:
//
//  1. Pick the heaviest N traces by span-count over the last
//     `since` window, ORDER BY count() DESC. This biases the
//     map toward the request paths that actually drive load,
//     not edge-case 1-span traces.
//  2. Pull only the four columns the edge walk needs
//     (trace_id, span_id, parent_id, service_name, status_code)
//     for those traces. Skips event blobs / attributes — the
//     edge walk doesn't read them.
//
// In-memory walk: for every span S whose parent's service ≠ S's
// service, emit an edge (parent.service → S.service). Errors are
// counted on the callee side. The status_code column stores the
// lowercase token the ingest path writes (otlp/convert.go maps
// STATUS_CODE_ERROR → "error"), so the predicate compares against
// "error" — NOT the OTLP enum name. Result is bounded by the sample
// size so a billion-span/day deployment still answers in <2s.
//
// The IN (?,...) construct holds N=200-ish trace IDs; ClickHouse
// happily plans this against the partition key + bloom-filter on
// trace_id, granule pruning keeps the second query cheap.
func (s *Store) GetServiceMap(
	ctx context.Context, since time.Duration, sampleCount int,
) (*ServiceMap, error) {
	end := time.Now()
	return s.getServiceMapAt(ctx, end.Add(-since), end, sampleCount)
}

// GetServiceMapWithDiff returns the current map annotated against a
// baseline window taken `baselineAgo` earlier. New nodes/edges (in the
// current window but not the baseline) carry IsNew=true; nodes/edges
// in the baseline that have disappeared land in RemovedNodes /
// RemovedEdges so the operator can spot silent regressions ("the
// payment service stopped calling fraud-check this morning").
//
// Baseline failure is non-fatal: the current map is returned without
// diff annotations. The two queries are sequential rather than
// parallel because the cache key is shared and the second query is
// served from the cache 99% of the time anyway.
func (s *Store) GetServiceMapWithDiff(
	ctx context.Context, since time.Duration, sampleCount int,
	baselineAgo time.Duration, baselineLabel string,
) (*ServiceMap, error) {
	end := time.Now()
	cur, err := s.getServiceMapAt(ctx, end.Add(-since), end, sampleCount)
	if err != nil {
		return nil, err
	}
	if baselineAgo <= 0 {
		return cur, nil
	}
	bEnd := end.Add(-baselineAgo)
	base, err := s.getServiceMapAt(ctx, bEnd.Add(-since), bEnd, sampleCount)
	if err != nil {
		// Surface the current map even if the baseline window
		// returned an error — a partial view beats a 500.
		return cur, nil
	}
	annotateDiff(cur, base, baselineLabel)
	return cur, nil
}

func annotateDiff(cur, base *ServiceMap, baselineLabel string) {
	cur.BaselineAgo = baselineLabel

	baseSvc := make(map[string]bool, len(base.Nodes))
	for _, n := range base.Nodes {
		baseSvc[n.Service] = true
	}
	type ek struct{ a, b string }
	baseEdge := make(map[ek]bool, len(base.Edges))
	for _, e := range base.Edges {
		baseEdge[ek{e.Caller, e.Callee}] = true
	}

	curSvc := make(map[string]bool, len(cur.Nodes))
	for i := range cur.Nodes {
		n := &cur.Nodes[i]
		curSvc[n.Service] = true
		if !baseSvc[n.Service] {
			n.IsNew = true
		}
	}
	curEdge := make(map[ek]bool, len(cur.Edges))
	for i := range cur.Edges {
		e := &cur.Edges[i]
		k := ek{e.Caller, e.Callee}
		curEdge[k] = true
		if !baseEdge[k] {
			e.IsNew = true
		}
	}
	for _, n := range base.Nodes {
		if !curSvc[n.Service] {
			cur.RemovedNodes = append(cur.RemovedNodes, n)
		}
	}
	for _, e := range base.Edges {
		k := ek{e.Caller, e.Callee}
		if !curEdge[k] {
			cur.RemovedEdges = append(cur.RemovedEdges, e)
		}
	}
}

// getServiceMapAt is the shared core: build a map for an explicit
// [winStart, winEnd] window. The public GetServiceMap is the "current
// window" wrapper; GetServiceMapWithDiff calls this twice (once for
// "now", once for "baselineAgo back") to compute the topology delta.
func (s *Store) getServiceMapAt(
	ctx context.Context, winStart, winEnd time.Time, sampleCount int,
) (*ServiceMap, error) {
	if sampleCount <= 0 || sampleCount > 500 {
		sampleCount = 200
	}

	tr, err := s.conn.Query(ctx, `
		SELECT trace_id FROM spans
		WHERE time >= ? AND time <= ?
		GROUP BY trace_id
		ORDER BY count() DESC
		LIMIT ?
		SETTINGS max_execution_time = 30`,
		winStart, winEnd, sampleCount)
	if err != nil {
		return nil, err
	}
	var traceIDs []string
	for tr.Next() {
		var id string
		if err := tr.Scan(&id); err != nil {
			tr.Close()
			return nil, err
		}
		traceIDs = append(traceIDs, id)
	}
	tr.Close()
	if len(traceIDs) == 0 {
		// Non-nil empty slices — Go marshals a nil []T as JSON null,
		// which makes the SPA's `for (const n of data.nodes)` blow up
		// ("i.nodes is not iterable") on empty windows. Same shape as
		// the populated path so the frontend can stay defensive-free.
		return &ServiceMap{
			Nodes: []ServiceMapNode{},
			Edges: []ServiceMapEdge{},
		}, nil
	}

	holders := make([]string, len(traceIDs))
	args := make([]any, len(traceIDs))
	for i, id := range traceIDs {
		holders[i] = "?"
		args[i] = id
	}

	rows, err := s.conn.Query(ctx, fmt.Sprintf(`
		SELECT trace_id, span_id, parent_id, service_name, status_code,
		       db_system, peer_service, kind
		FROM spans
		WHERE trace_id IN (%s)
		SETTINGS max_execution_time = 30`, strings.Join(holders, ",")), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type spanInfo struct {
		parent     string
		svc        string
		errSp      bool
		dbSystem   string // populated → infrastructure dep edge
		peerSvc    string // populated → external dep edge
		kind       string // span kind (client/server/producer/…)
	}
	byTrace := map[string]map[string]spanInfo{}
	nodeSpan := map[string]int{}
	nodeErr  := map[string]int{}
	// Track Kind/Subkind of each node so the frontend can render
	// real services and dep nodes differently. Real services
	// keep Kind="" — only synthesised dep nodes carry a Kind.
	nodeKind    := map[string]string{}
	nodeSubkind := map[string]string{}
	totalSpans := 0
	for rows.Next() {
		var traceID, spanID, parentID, svc, statusCode string
		var dbSystem, peerSvc, spanKind string
		if err := rows.Scan(&traceID, &spanID, &parentID, &svc, &statusCode,
			&dbSystem, &peerSvc, &spanKind); err != nil {
			return nil, err
		}
		isErr := spanStatusIsError(statusCode)
		m, ok := byTrace[traceID]
		if !ok {
			m = map[string]spanInfo{}
			byTrace[traceID] = m
		}
		m[spanID] = spanInfo{
			parent: parentID, svc: svc, errSp: isErr,
			dbSystem: dbSystem, peerSvc: peerSvc, kind: spanKind,
		}
		nodeSpan[svc]++
		if isErr {
			nodeErr[svc]++
		}
		totalSpans++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	type edgeKey struct{ caller, callee string }
	edgeSpan := map[edgeKey]int{}
	edgeErr  := map[edgeKey]int{}
	edgeTraces := map[edgeKey]map[string]struct{}{}

	// Helper: ensure a synthetic dep node exists in the
	// counters and emit an edge from caller → dep.
	depEdge := func(caller, dep, kind, subkind string, sp spanInfo, traceID string) {
		nodeSpan[dep]++
		if sp.errSp {
			nodeErr[dep]++
		}
		// Last-write-wins is fine for kind/subkind — every span
		// for the same dep should agree.
		nodeKind[dep] = kind
		nodeSubkind[dep] = subkind
		k := edgeKey{caller: caller, callee: dep}
		edgeSpan[k]++
		if sp.errSp {
			edgeErr[k]++
		}
		if edgeTraces[k] == nil {
			edgeTraces[k] = map[string]struct{}{}
		}
		edgeTraces[k][traceID] = struct{}{}
	}

	for traceID, spans := range byTrace {
		for _, sp := range spans {
			// 1) Service-to-service edge: a span whose parent
			//    lives in a different OTel service.
			parent, ok := spans[sp.parent]
			if ok && parent.svc != sp.svc {
				k := edgeKey{caller: parent.svc, callee: sp.svc}
				edgeSpan[k]++
				if sp.errSp {
					edgeErr[k]++
				}
				if edgeTraces[k] == nil {
					edgeTraces[k] = map[string]struct{}{}
				}
				edgeTraces[k][traceID] = struct{}{}
			}

			// 2) Infra dep edges. db_system / peer_service show
			//    up on CLIENT-kind spans (the call-out side), so
			//    the calling service is sp.svc itself, not the
			//    parent. Synthesised target nodes are namespaced
			//    so a "redis" db doesn't collide with a real
			//    OTel service literally named "redis".
			switch {
			case sp.dbSystem != "":
				depName := "db:" + sp.dbSystem
				depEdge(sp.svc, depName, "db", sp.dbSystem, sp, traceID)
			case sp.peerSvc != "" && (sp.kind == "client" || sp.kind == "producer"):
				// Synthesised external service — only for
				// outbound-shaped spans so we don't double-
				// count the server side of an in-cluster RPC
				// (where peer.service may also be set on the
				// receiver side).
				// Skip if peerSvc actually IS an OTel service
				// in this map already — it's a real edge then.
				if _, isReal := nodeSpan[sp.peerSvc]; isReal && sp.peerSvc != sp.svc {
					continue
				}
				depEdge(sp.svc, "ext:"+sp.peerSvc, "external", sp.peerSvc, sp, traceID)
			}
		}
	}

	out := &ServiceMap{
		Nodes:       make([]ServiceMapNode, 0, len(nodeSpan)),
		Edges:       make([]ServiceMapEdge, 0, len(edgeSpan)),
		SampledFrom: len(traceIDs),
		TotalSpans:  totalSpans,
	}
	for svc, n := range nodeSpan {
		rate := 0.0
		if n > 0 {
			rate = float64(nodeErr[svc]) / float64(n)
		}
		out.Nodes = append(out.Nodes, ServiceMapNode{
			Service: svc, SpanCount: n, ErrorRate: rate,
			Kind:    nodeKind[svc],
			Subkind: nodeSubkind[svc],
		})
	}
	for k, n := range edgeSpan {
		out.Edges = append(out.Edges, ServiceMapEdge{
			Caller:     k.caller,
			Callee:     k.callee,
			SpanCount:  n,
			ErrorCount: edgeErr[k],
			TraceCount: len(edgeTraces[k]),
		})
	}
	// Cluster enrichment — one batch query against the spans
	// table grouped by (service_name, cluster) over the same
	// window. Pinned services get the single cluster name,
	// services spanning > 1 cluster get "multi" so the
	// frontend can chip them differently. Soft-fails on CH
	// error — the map still renders, just without cluster
	// chips.
	if cm, err := s.GetServiceClusterMap(ctx, winEnd.Sub(winStart)); err == nil {
		for i := range out.Nodes {
			cs := cm[out.Nodes[i].Service]
			switch len(cs) {
			case 0:
				// no cluster info
			case 1:
				out.Nodes[i].Cluster = cs[0]
			default:
				out.Nodes[i].Cluster = "multi"
			}
		}
	}

	// Stable order so the frontend layout doesn't jitter between
	// 30s polls when nodes are tied.
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].Service < out.Nodes[j].Service })
	sort.Slice(out.Edges, func(i, j int) bool {
		if out.Edges[i].Caller != out.Edges[j].Caller {
			return out.Edges[i].Caller < out.Edges[j].Caller
		}
		return out.Edges[i].Callee < out.Edges[j].Callee
	})
	return out, nil
}
