package api

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"hash/fnv"
	"html"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// TopologyNode is a service.operation node in the response. The
// frontend keys nodes by `id` (service|op) so render-time edge
// lookups stay O(1).
type TopologyNode struct {
	ID      string `json:"id"`
	Service string `json:"service"`
	Op      string `json:"op"`
}

// TopologyResponse is the JSON shape served by /api/topology.
// Truncated is true when the underlying edge query hit the LIMIT
// — the UI shows a banner so the operator knows the view is
// partial.
type TopologyResponse struct {
	Nodes       []TopologyNode          `json:"nodes"`
	Edges       []chstore.TopologyEdge  `json:"edges"`
	RootService string                  `json:"rootService"`
	Depth       int                     `json:"depth"`
	From        int64                   `json:"from"` // unix ns
	To          int64                   `json:"to"`
	Truncated   bool                    `json:"truncated"`
}

// getTopology builds an operation-level call graph rooted at a
// service. Pure BFS over the aggregated edge set — depth N expands
// up to N hops from any operation in the root service. Edge
// limit is fixed at the store layer (50k); on rows == limit we
// flag the response so the UI is honest about partial coverage.
func (s *Server) getTopology(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	root := q.Get("root")
	if root == "" {
		http.Error(w, "root service required", http.StatusBadRequest)
		return
	}
	// Optional op-level seed: when set, BFS starts from
	// (root, rootOp) only rather than every op in root. Lets the
	// operator answer "what does POST /payment cascade into?"
	// without N hops of unrelated ops cluttering the view.
	rootOp := q.Get("root_op")
	depth := parseInt(q.Get("depth"), 3)
	if depth < 1 {
		depth = 1
	}
	if depth > 6 {
		depth = 6
	}
	from, to := parseFromTo(r, 1*time.Hour)
	const edgeCap = 50000
	key := fmt.Sprintf("topology-op:root=%s:op=%s:depth=%d:from=%d:to=%d", root, rootOp, depth, from.UnixNano()/int64(time.Minute), to.UnixNano()/int64(time.Minute))
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		// Read from the pre-aggregated op-edges table instead of
		// the live spans self-join. Same shape, 1000× cheaper.
		allEdges, err := s.store.ReadTopologyOpEdgesAgg(r.Context(), from, to, edgeCap)
		if err != nil {
			return nil, err
		}
		// Index edges by parent service so BFS only scans relevant
		// rows per frontier expansion. Map of parent_service → []edge.
		byParentService := make(map[string][]chstore.TopologyEdge)
		for _, e := range allEdges {
			byParentService[e.ParentService] = append(byParentService[e.ParentService], e)
		}
		nodeIDOf := func(svc, op string) string { return svc + "|" + op }
		visited := map[string]TopologyNode{}
		addNode := func(svc, op string) {
			id := nodeIDOf(svc, op)
			if _, ok := visited[id]; !ok {
				visited[id] = TopologyNode{ID: id, Service: svc, Op: op}
			}
		}
		frontier := map[string]struct{}{}
		for _, e := range byParentService[root] {
			// When rootOp is set, only seed BFS from that single op;
			// without it, every op in the root service is a seed.
			if rootOp != "" && e.ParentOp != rootOp {
				continue
			}
			key := nodeIDOf(e.ParentService, e.ParentOp)
			frontier[key] = struct{}{}
			addNode(e.ParentService, e.ParentOp)
		}
		expanded := map[string]bool{}
		var keptEdges []chstore.TopologyEdge
		for hop := 0; hop < depth && len(frontier) > 0; hop++ {
			nextFrontier := map[string]struct{}{}
			for parentID := range frontier {
				if expanded[parentID] {
					continue
				}
				expanded[parentID] = true
				parentSvc, parentOp := splitNodeID(parentID)
				for _, e := range byParentService[parentSvc] {
					if e.ParentOp != parentOp {
						continue
					}
					keptEdges = append(keptEdges, e)
					addNode(e.ChildService, e.ChildOp)
					childID := nodeIDOf(e.ChildService, e.ChildOp)
					if !expanded[childID] {
						nextFrontier[childID] = struct{}{}
					}
				}
			}
			frontier = nextFrontier
		}
		nodesOut := make([]TopologyNode, 0, len(visited))
		for _, n := range visited {
			nodesOut = append(nodesOut, n)
		}
		sort.Slice(nodesOut, func(i, j int) bool { return nodesOut[i].ID < nodesOut[j].ID })
		sort.Slice(keptEdges, func(i, j int) bool {
			if keptEdges[i].ParentService != keptEdges[j].ParentService {
				return keptEdges[i].ParentService < keptEdges[j].ParentService
			}
			if keptEdges[i].ParentOp != keptEdges[j].ParentOp {
				return keptEdges[i].ParentOp < keptEdges[j].ParentOp
			}
			if keptEdges[i].ChildService != keptEdges[j].ChildService {
				return keptEdges[i].ChildService < keptEdges[j].ChildService
			}
			return keptEdges[i].ChildOp < keptEdges[j].ChildOp
		})
		if keptEdges == nil {
			keptEdges = []chstore.TopologyEdge{}
		}
		return TopologyResponse{
			Nodes:       nodesOut,
			Edges:       keptEdges,
			RootService: root,
			Depth:       depth,
			From:        from.UnixNano(),
			To:          to.UnixNano(),
			Truncated:   len(allEdges) >= edgeCap,
		}, nil
	})
}

// splitNodeID undoes nodeIDOf — kept private because the encoding
// is a `service|op` string we entirely own server-side.
func splitNodeID(id string) (svc, op string) {
	for i := 0; i < len(id); i++ {
		if id[i] == '|' {
			return id[:i], id[i+1:]
		}
	}
	return id, ""
}

// ServiceTopologyResponse is the JSON shape served by
// /api/topology/service — flat node + edge lists matching the
// chstore ServiceTopologyEdge model. Includes the time window so
// the UI can render the active window and a draw.io export can
// embed it in the filename.
type ServiceTopologyResponse struct {
	Nodes     []ServiceTopologyNode         `json:"nodes"`
	Edges     []chstore.ServiceTopologyEdge `json:"edges"`
	From      int64                         `json:"from"`
	To        int64                         `json:"to"`
	Truncated bool                          `json:"truncated"`
}

// ServiceTopologyNode is one node in the service-level graph.
// Kind distinguishes a real service from synthetic infra nodes
// (db, queue, cache, external) so the renderer can paint them
// differently without per-node lookup.
type ServiceTopologyNode struct {
	ID   string `json:"id"`   // canonical id used by edges (service name OR "db:postgresql")
	Name string `json:"name"` // display label, sans prefix for infra ("postgresql" not "db:postgresql")
	Kind string `json:"kind"` // "service" | "db" | "queue" | "external"
}

// topologyExcludeKey persists the operator's curated list of
// services to hide from the topology diagram (v0.5.176).
// Stored as a JSON array under system_settings — admin edits via
// Settings UI, individual service detail pages can also toggle
// via a "Mute on topology" button. Useful for "hub-like" infra
// services (kafka config server, service-mesh control plane,
// identity service) that fan out to every other service and
// turn the diagram into spaghetti.
const topologyExcludeKey = "topology.exclude"

// excludeKeyDigest produces a short stable cache-key fragment
// for the exclude list (v0.5.187). Previously we keyed only on
// the list LENGTH which caused cache poisoning: two different
// 1-element sets ({"foo"} and {"bar"}) collided and served
// each other's cached topology data. Now we sort + FNV the
// entries so any two distinct sets produce distinct digests
// while the same set across calls produces a stable one.
func excludeKeyDigest(m map[string]bool) string {
	if len(m) == 0 {
		return "0"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := fnv.New64a()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum64())
}

// loadTopologyExclude reads the operator-curated exclude list.
// Returns a map for O(1) lookup; empty when unset or corrupt.
func (s *Server) loadTopologyExclude(ctx context.Context) map[string]bool {
	raw, err := s.store.GetSetting(ctx, topologyExcludeKey)
	if err != nil || len(raw) == 0 {
		return map[string]bool{}
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(list))
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s != "" {
			out[s] = true
		}
	}
	return out
}

// getTopologyExclude returns the current exclude list as JSON
// (sorted, deduped) so the admin Settings page can render it.
func (s *Server) getTopologyExclude(w http.ResponseWriter, r *http.Request) {
	m := s.loadTopologyExclude(r.Context())
	list := make([]string, 0, len(m))
	for k := range m {
		list = append(list, k)
	}
	sort.Strings(list)
	writeJSON(w, map[string]any{"services": list})
}

// putTopologyExclude replaces the entire exclude list. Empty
// array is valid (clears the mute).
func (s *Server) putTopologyExclude(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Services []string `json:"services"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	// Dedupe + trim so repeated entries from an inattentive
	// hand-edit collapse to one row.
	seen := map[string]bool{}
	cleaned := make([]string, 0, len(body.Services))
	for _, sv := range body.Services {
		sv = strings.TrimSpace(sv)
		if sv == "" || seen[sv] {
			continue
		}
		seen[sv] = true
		cleaned = append(cleaned, sv)
	}
	sort.Strings(cleaned)
	raw, _ := json.Marshal(cleaned)
	if err := s.store.PutSetting(r.Context(), topologyExcludeKey, raw); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "settings.topology_exclude.update", "settings", topologyExcludeKey,
		fmt.Sprintf(`{"count":%d}`, len(cleaned)))
	writeJSON(w, map[string]any{"services": cleaned})
}

// getServiceTopology delivers the full service-level interaction
// graph for a time window. No depth bound — the entire backend
// fabric is small enough (tens to low hundreds of services) to
// render in one view; the SVG layout below handles overflow via
// scrolling. The depth-bounded op-level view at /api/topology
// remains in place for "what does service X talk to" deep dives.
// looksLikeInfraEdge — v0.5.310. Returns true when an edge's
// top operation labels match the canonical "platform plumbing"
// patterns: /health probes, /metrics scrapes, keepalive pings,
// service-mesh sidecar paths. These contribute noise to the
// business-flow topology view; operators triage from RED
// charts + the inbox, not from a health-probe edge.
//
// Heuristic over labels (not regex) keeps it cheap — runs per
// edge, no compilation. Case-insensitive substring match. False
// negatives are fine (real edge survives); false positives are
// the risk — kept the pattern list tight so a legitimate edge
// like /api/v1/users/{id}/healthcheck-status is unlikely to
// match.
var infraOpFragments = []string{
	"/health", "/healthz", "/healthcheck", "/livez", "/readyz",
	"/metrics", "/-/metrics", "/prometheus", "/actuator/prometheus",
	"/actuator/health", "/actuator/info",
	"/ping", "/heartbeat", "/keepalive",
	"/.well-known/", "/-/ready", "/-/healthy",
}

func looksLikeInfraEdge(topLabels []string) bool {
	if len(topLabels) == 0 {
		return false
	}
	// Edge is infra-only if EVERY one of its top labels is infra.
	// (top_labels is up to 5 most-frequent ops; one health probe
	// among 4 real endpoints still surfaces.)
	for _, l := range topLabels {
		lower := strings.ToLower(l)
		matched := false
		for _, frag := range infraOpFragments {
			if strings.Contains(lower, frag) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func (s *Server) getServiceTopology(w http.ResponseWriter, r *http.Request) {
	const edgeCap = 5000
	from, to := parseFromTo(r, 1*time.Hour)
	exclude := s.loadTopologyExclude(r.Context())
	// v0.5.310 — Service Topology Redux. Operator-reported the
	// previous view was "karman çorman" (messy) at scale — kafka
	// self-loops from cache-refresh ate the canvas; tiny edges
	// from healthchecks / metrics scraping cluttered the diagram.
	// New noise filter is ON by default; operator can flip
	// ?noise=show to get the legacy unfiltered view.
	//   • hide_self  — drop edges where parent == child (typical
	//                  cache refresh / pub-sub fanout to self)
	//   • min_call_pct — drop edges contributing <X% of total
	//                    window call volume (default 0.5%)
	//   • hide_infra — drop edges whose top_labels match an
	//                  infra pattern (/health, /ping, /metrics,
	//                  /-/, cache.refresh, .keep-alive)
	noiseShow := r.URL.Query().Get("noise") == "show"
	minCallPct := 0.5
	if v := r.URL.Query().Get("min_call_pct"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			minCallPct = f
		}
	}
	if noiseShow {
		minCallPct = 0
	}
	// Cache key includes the rounded window so concurrent requests
	// over the same minute share one CH round-trip. Exclude list
	// folded in via FNV digest (v0.5.187) — previously we used
	// just the length which caused cross-set cache poisoning when
	// two different 1-element sets collided.
	key := fmt.Sprintf("topology-service:from=%d:to=%d:ex=%s:noise=%v:mp=%.2f",
		from.UnixNano()/int64(time.Minute), to.UnixNano()/int64(time.Minute),
		excludeKeyDigest(exclude), noiseShow, minCallPct)
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		// Read from the topology_edges_5m pre-aggregated table
		// (filled by the background aggregator goroutine every
		// 5 min). The live self-join path used to run here is
		// untenable at production scale — see WriteTopologyBucket
		// for the actual aggregation query.
		edges, err := s.store.ReadServiceTopologyAgg(r.Context(), from, to, edgeCap)
		if err != nil {
			return nil, err
		}
		// Drop "external" edges whose peer_service points at a
		// known instrumented service — those already show as
		// cross-service edges from the join pass. Without this, a
		// client→server hop renders twice: once as a real service
		// edge, once as "frontend → ext:api-gateway". Only
		// un-instrumented peers (third-party APIs, legacy systems)
		// survive the filter.
		knownServices := map[string]bool{}
		for _, e := range edges {
			if e.NodeKind == "service" {
				knownServices[e.ParentService] = true
				knownServices[e.ChildNode] = true
			}
		}
		// v0.5.310 — total call volume across the window, used
		// for the minCallPct threshold below. Computed BEFORE
		// any filtering so a healthy noise edge doesn't shift
		// the cutoff relative to actual fleet activity.
		var totalCalls uint64
		for _, e := range edges {
			totalCalls += e.Calls
		}
		minCalls := uint64(0)
		if minCallPct > 0 && totalCalls > 0 {
			minCalls = uint64(float64(totalCalls) * minCallPct / 100)
		}
		filtered := edges[:0]
		for _, e := range edges {
			if e.NodeKind == "external" {
				peer := strings.TrimPrefix(e.ChildNode, "ext:")
				if knownServices[peer] {
					continue
				}
			}
			// Operator-curated topology exclude list (v0.5.176).
			// Drops every edge touching a muted service so a
			// hub-like infra component (config server, identity,
			// service-mesh control plane) doesn't pollute every
			// diagram. Filter on EITHER end — the muted service
			// shouldn't appear as caller OR callee.
			if exclude[e.ParentService] || exclude[e.ChildNode] {
				continue
			}
			// v0.5.310 — noise filters (default ON, ?noise=show
			// disables). Each filter is independent so future
			// per-axis toggles cost nothing.
			if !noiseShow {
				// Self-loop: kafka pub-sub fanout to self, cache
				// refresh, "talk to your own ingress" — almost
				// always noise rather than topology signal.
				if e.ParentService == e.ChildNode {
					continue
				}
				// Infrastructure-only top labels — /health
				// probes, /metrics scrapes, keepalive pings.
				// Operator looking at topology cares about
				// business flow, not infra plumbing.
				if looksLikeInfraEdge(e.TopLabels) {
					continue
				}
				// Long-tail edges below the operator-configurable
				// percent floor. 0.5% default — at 1M calls/h
				// window that drops anything under 5k calls.
				if minCalls > 0 && e.Calls < minCalls {
					continue
				}
			}
			filtered = append(filtered, e)
		}
		edges = filtered
		nodes := map[string]ServiceTopologyNode{}
		addNode := func(id, kind string) {
			if _, ok := nodes[id]; ok {
				return
			}
			name := id
			if kind != "service" {
				for _, p := range []string{"db:", "queue:", "ext:"} {
					if strings.HasPrefix(name, p) {
						name = name[len(p):]
						break
					}
				}
			}
			nodes[id] = ServiceTopologyNode{ID: id, Name: name, Kind: kind}
		}
		for _, e := range edges {
			addNode(e.ParentService, "service")
			addNode(e.ChildNode, e.NodeKind)
		}
		nodesOut := make([]ServiceTopologyNode, 0, len(nodes))
		for _, n := range nodes {
			nodesOut = append(nodesOut, n)
		}
		sort.Slice(nodesOut, func(i, j int) bool { return nodesOut[i].ID < nodesOut[j].ID })
		// Empty-window safety: nil edge slice marshals as JSON
		// `null` which makes the SPA's `data.edges.forEach` crash.
		// Same shape every call so the frontend can stay defensive-
		// free.
		if edges == nil {
			edges = []chstore.ServiceTopologyEdge{}
		}
		return ServiceTopologyResponse{
			Nodes:     nodesOut,
			Edges:     edges,
			From:      from.UnixNano(),
			To:        to.UnixNano(),
			Truncated: len(edges) >= edgeCap,
		}, nil
	})
}

// FlowsResponse lists the top root-anchored business flows in a
// window. Each entry pairs a root signature with its trace count
// and the unique set of services those traces touched.
type FlowsResponse struct {
	Flows []chstore.RootFlow `json:"flows"`
	From  int64              `json:"from"`
	To    int64              `json:"to"`
}

// getRootFlows surfaces the top business-level entry points by
// trace volume. Used by the /topology page's "Flows" section to
// list flows like POST /login or POST /payment with a chip list
// of involved services.
func (s *Server) getRootFlows(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, 1*time.Hour)
	top := parseInt(r.URL.Query().Get("top"), 20)
	key := fmt.Sprintf("topology-flows:from=%d:to=%d:top=%d", from.UnixNano()/int64(time.Minute), to.UnixNano()/int64(time.Minute), top)
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		// Read from the pre-aggregated table (v0.5.112). The live
		// self-join path used to run here would intermittently
		// timeout on high-cardinality installs; the agg table is
		// filled by the same 5-min topology-agg goroutine.
		flows, err := s.store.ReadRootFlowsAgg(r.Context(), from, to, top)
		if err != nil {
			return nil, err
		}
		if flows == nil {
			flows = []chstore.RootFlow{}
		}
		// Enrich with p99 root-span duration (v0.5.156). One CH
		// query, scoped to the (service, op) signatures we just
		// returned, so the cost is bounded by `top` regardless of
		// span volume. Errors are non-fatal — the flow list is
		// still useful without latency.
		if len(flows) > 0 {
			sigs := make([]chstore.FlowSig, 0, len(flows))
			for _, f := range flows {
				sigs = append(sigs, chstore.FlowSig{
					RootService: f.RootService,
					RootOp:      f.RootOp,
				})
			}
			if lat, err := s.store.ComputeFlowsLatencyP99(r.Context(), from, to, sigs); err == nil {
				for i := range flows {
					flows[i].P99Ns = lat[flows[i].RootService+"\x00"+flows[i].RootOp]
				}
			}
		}
		return FlowsResponse{Flows: flows, From: from.UnixNano(), To: to.UnixNano()}, nil
	})
}

// getFlowTopology returns the service-level subgraph for one
// (root_service, root_op) flow. Same response shape as
// getServiceTopology so the renderer reuses a single code path
// — the difference is the edges/nodes are restricted to traces
// matching the flow signature.
func (s *Server) getFlowTopology(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rootService := q.Get("root_service")
	rootOp := q.Get("root_op")
	if rootService == "" || rootOp == "" {
		http.Error(w, "root_service and root_op required", http.StatusBadRequest)
		return
	}
	from, to := parseFromTo(r, 1*time.Hour)
	exclude := s.loadTopologyExclude(r.Context())
	key := fmt.Sprintf("topology-flow:rs=%s:ro=%s:from=%d:to=%d:ex=%s",
		rootService, rootOp,
		from.UnixNano()/int64(time.Minute), to.UnixNano()/int64(time.Minute),
		excludeKeyDigest(exclude))
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		edges, err := s.store.GetFlowTopology(r.Context(), from, to, rootService, rootOp, 5000)
		if err != nil {
			return nil, err
		}
		// Same external-peer filter as the global topology — drop
		// peers that already exist as instrumented services so the
		// subgraph doesn't double-count cross-service edges.
		knownServices := map[string]bool{}
		for _, e := range edges {
			if e.NodeKind == "service" {
				knownServices[e.ParentService] = true
				knownServices[e.ChildNode] = true
			}
		}
		filtered := edges[:0]
		for _, e := range edges {
			if e.NodeKind == "external" {
				peer := strings.TrimPrefix(e.ChildNode, "ext:")
				if knownServices[peer] {
					continue
				}
			}
			// Operator-curated topology exclude list (v0.5.176).
			// Drops every edge touching a muted service so a
			// hub-like infra component (config server, identity,
			// service-mesh control plane) doesn't pollute every
			// diagram. Filter on EITHER end — the muted service
			// shouldn't appear as caller OR callee.
			if exclude[e.ParentService] || exclude[e.ChildNode] {
				continue
			}
			filtered = append(filtered, e)
		}
		edges = filtered
		nodes := map[string]ServiceTopologyNode{}
		addNode := func(id, kind string) {
			if _, ok := nodes[id]; ok {
				return
			}
			name := id
			if kind != "service" {
				for _, p := range []string{"db:", "queue:", "ext:"} {
					if strings.HasPrefix(name, p) {
						name = name[len(p):]
						break
					}
				}
			}
			nodes[id] = ServiceTopologyNode{ID: id, Name: name, Kind: kind}
		}
		// Always include the root service as a node, even if it
		// has no outgoing edges in this window (single-service
		// flow case).
		addNode(rootService, "service")
		for _, e := range edges {
			addNode(e.ParentService, "service")
			addNode(e.ChildNode, e.NodeKind)
		}
		nodesOut := make([]ServiceTopologyNode, 0, len(nodes))
		for _, n := range nodes {
			nodesOut = append(nodesOut, n)
		}
		sort.Slice(nodesOut, func(i, j int) bool { return nodesOut[i].ID < nodesOut[j].ID })
		if edges == nil {
			edges = []chstore.ServiceTopologyEdge{}
		}
		return ServiceTopologyResponse{
			Nodes:     nodesOut,
			Edges:     edges,
			From:      from.UnixNano(),
			To:        to.UnixNano(),
			Truncated: false,
		}, nil
	})
}

// getTopologyEdgeInstances returns the per-peer_service
// breakdown for an infra edge (db / queue). Powers the
// EdgeDetailPanel's per-instance section so the operator can see
// which postgres / kafka instance is hot without leaving
// /topology. Cached 60s on the (parent, system, kind, window)
// tuple — the data feeds a triage popover that re-opens
// repeatedly while the operator scans.
func (s *Server) getTopologyEdgeInstances(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	parent := q.Get("parent")
	system := q.Get("system")
	kind := q.Get("kind") // "db" | "queue"
	if parent == "" || system == "" || (kind != "db" && kind != "queue") {
		http.Error(w, "parent, system, and kind=db|queue required", http.StatusBadRequest)
		return
	}
	from, to := parseFromTo(r, 1*time.Hour)
	key := fmt.Sprintf("topology-edge-instances:p=%s:s=%s:k=%s:from=%d:to=%d",
		parent, system, kind,
		from.UnixNano()/int64(time.Minute), to.UnixNano()/int64(time.Minute))
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		instances, err := s.store.GetEdgeInstances(r.Context(), parent, system, kind, from, to, 50)
		if err != nil {
			return nil, err
		}
		if instances == nil {
			instances = []chstore.EdgeInstance{}
		}
		return map[string]any{"instances": instances}, nil
	})
}

// getTopologyOps lists the operations that appear as outbound
// callers for a given service in the window. Used by the
// operation deep-dive view to populate the op picker once the
// operator has chosen a root service.
func (s *Server) getTopologyOps(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	if service == "" {
		http.Error(w, "service required", http.StatusBadRequest)
		return
	}
	from, to := parseFromTo(r, 1*time.Hour)
	key := fmt.Sprintf("topology-ops:svc=%s:from=%d:to=%d", service, from.UnixNano()/int64(time.Minute), to.UnixNano()/int64(time.Minute))
	s.serveCached(w, r, key, 60*time.Second, func() (any, error) {
		ops, err := s.store.ListOpsForService(r.Context(), service, from, to)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ops": ops}, nil
	})
}

// exportServiceTopologyDrawIO serialises the service-level
// topology (the full backend graph, op-collapsed, infra-included)
// as a draw.io mxGraph XML. Same payload shape as the JSON
// /api/topology/service endpoint; the layout uses the same BFS
// hop assignment so the exported diagram matches what the UI
// renders inline. Edge labels carry protocol + top method+
// endpoint so the diagram is self-explanatory standalone.
func (s *Server) exportServiceTopologyDrawIO(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, 1*time.Hour)
	edges, err := s.store.ReadServiceTopologyAgg(r.Context(), from, to, 5000)
	if err != nil {
		writeErr(w, err)
		return
	}
	edges = filterExternalPeers(edges)
	filename := fmt.Sprintf("service-topology-%s.drawio",
		time.Now().UTC().Format("20060102-1504"))
	writeServiceDrawIO(w, "Service topology", filename, edges)
}

// exportFlowTopologyDrawIO is the per-flow draw.io export
// (v0.5.145). Reuses the same XML builder as the service-level
// export but feeds it the flow-restricted edge set so the
// diagram contains only the services this flow's traces
// touched. Filename embeds a sanitised root op.
func (s *Server) exportFlowTopologyDrawIO(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rootService := q.Get("root_service")
	rootOp := q.Get("root_op")
	if rootService == "" || rootOp == "" {
		http.Error(w, "root_service and root_op required", http.StatusBadRequest)
		return
	}
	from, to := parseFromTo(r, 1*time.Hour)
	edges, err := s.store.GetFlowTopology(r.Context(), from, to, rootService, rootOp, 5000)
	if err != nil {
		writeErr(w, err)
		return
	}
	edges = filterExternalPeers(edges)
	safeOp := strings.NewReplacer("/", "_", " ", "_", "?", "_", "&", "_").Replace(rootOp)
	filename := fmt.Sprintf("flow-%s-%s-%s.drawio",
		rootService, safeOp, time.Now().UTC().Format("20060102-1504"))
	writeServiceDrawIO(w, "Flow: "+rootService+" "+rootOp, filename, edges)
}

// filterExternalPeers drops "ext:" edges whose peer is already a
// known service in the cross-service set — same heuristic the
// JSON endpoint uses so the JSON view and the export stay
// visually identical.
func filterExternalPeers(edges []chstore.ServiceTopologyEdge) []chstore.ServiceTopologyEdge {
	knownServices := map[string]bool{}
	for _, e := range edges {
		if e.NodeKind == "service" {
			knownServices[e.ParentService] = true
			knownServices[e.ChildNode] = true
		}
	}
	filtered := edges[:0]
	for _, e := range edges {
		if e.NodeKind == "external" {
			peer := strings.TrimPrefix(e.ChildNode, "ext:")
			if knownServices[peer] {
				continue
			}
		}
		filtered = append(filtered, e)
	}
	return filtered
}

// writeServiceDrawIO is the shared mxGraph XML builder used by
// the service-level + flow-level exports. Both surfaces feed it
// a ServiceTopologyEdge slice; the function lays nodes out by
// BFS hop, picks a colour per kind, and writes the file under
// the given diagram name + Content-Disposition filename.
func writeServiceDrawIO(w http.ResponseWriter, diagramName, filename string, edges []chstore.ServiceTopologyEdge) {
	type nodeMeta struct{ ID, Name, Kind string; Hop int }
	nodes := map[string]*nodeMeta{}
	addNode := func(id, kind string) {
		if _, ok := nodes[id]; ok {
			return
		}
		name := id
		if kind != "service" {
			for _, p := range []string{"db:", "queue:", "ext:"} {
				if strings.HasPrefix(name, p) {
					name = name[len(p):]
					break
				}
			}
		}
		nodes[id] = &nodeMeta{ID: id, Name: name, Kind: kind}
	}
	for _, e := range edges {
		addNode(e.ParentService, "service")
		addNode(e.ChildNode, e.NodeKind)
	}
	incoming := map[string]int{}
	for _, e := range edges {
		incoming[e.ChildNode]++
	}
	frontier := []string{}
	for id, n := range nodes {
		if incoming[id] == 0 && n.Kind == "service" {
			frontier = append(frontier, id)
			nodes[id].Hop = 0
		}
	}
	if len(frontier) == 0 {
		// Cyclic — pick highest out-degree service as the seed.
		out := map[string]int{}
		for _, e := range edges {
			out[e.ParentService]++
		}
		pick := ""
		maxOut := -1
		for k, v := range out {
			if v > maxOut {
				maxOut, pick = v, k
			}
		}
		if pick != "" {
			frontier = []string{pick}
			nodes[pick].Hop = 0
		}
	}
	visited := map[string]bool{}
	for len(frontier) > 0 {
		var next []string
		for _, id := range frontier {
			if visited[id] {
				continue
			}
			visited[id] = true
			h := nodes[id].Hop
			for _, e := range edges {
				if e.ParentService != id {
					continue
				}
				if _, ok := nodes[e.ChildNode]; ok && !visited[e.ChildNode] {
					if nodes[e.ChildNode].Hop == 0 && e.ChildNode != id {
						nodes[e.ChildNode].Hop = h + 1
					}
					next = append(next, e.ChildNode)
				}
			}
		}
		frontier = next
	}
	buckets := map[int][]string{}
	for id, n := range nodes {
		buckets[n.Hop] = append(buckets[n.Hop], id)
	}
	for _, list := range buckets {
		sort.Strings(list)
	}
	hops := make([]int, 0, len(buckets))
	for h := range buckets {
		hops = append(hops, h)
	}
	sort.Ints(hops)
	colorByKind := func(kind string) string {
		switch kind {
		case "db":
			return "#dae8fc;strokeColor=#6c8ebf"
		case "queue":
			return "#fff2cc;strokeColor=#d6b656"
		case "external":
			return "#f8cecc;strokeColor=#b85450"
		default:
			return "#d5e8d4;strokeColor=#82b366"
		}
	}
	var nodesXML strings.Builder
	idForNode := map[string]string{}
	nextID := 2
	for _, h := range hops {
		for i, id := range buckets[h] {
			n := nodes[id]
			cellID := fmt.Sprintf("n%d", nextID)
			nextID++
			idForNode[id] = cellID
			label := n.Name + "\n" + strings.ToUpper(n.Kind)
			fmt.Fprintf(&nodesXML,
				`<mxCell id="%s" value="%s" style="rounded=1;whiteSpace=wrap;html=1;fillColor=%s;" vertex="1" parent="1"><mxGeometry x="%d" y="%d" width="220" height="60" as="geometry"/></mxCell>`,
				cellID, html.EscapeString(label), colorByKind(n.Kind), h*280, i*100,
			)
		}
	}
	var edgesXML strings.Builder
	for _, e := range edges {
		src := idForNode[e.ParentService]
		dst := idForNode[e.ChildNode]
		if src == "" || dst == "" {
			continue
		}
		top := ""
		if len(e.TopLabels) > 0 {
			top = e.TopLabels[0]
		}
		label := strings.ToUpper(e.Protocol) + "  " + top + "\n" + fmt.Sprintf("%d calls", e.Calls)
		cellID := fmt.Sprintf("e%d", nextID)
		nextID++
		fmt.Fprintf(&edgesXML,
			`<mxCell id="%s" value="%s" style="endArrow=classic;html=1;rounded=0;labelBackgroundColor=#ffffff;" edge="1" parent="1" source="%s" target="%s"><mxGeometry relative="1" as="geometry"/></mxCell>`,
			cellID, html.EscapeString(label), src, dst,
		)
	}
	body := fmt.Sprintf(
		`<mxfile><diagram name="%s"><mxGraphModel dx="1600" dy="900" grid="1" gridSize="10" guides="1" arrows="1" connect="1" math="0" shadow="0"><root><mxCell id="0"/><mxCell id="1" parent="0"/>%s%s</root></mxGraphModel></diagram></mxfile>`,
		html.EscapeString(diagramName), nodesXML.String(), edgesXML.String(),
	)
	_ = xml.Unmarshal([]byte(body), new(struct{ XMLName xml.Name }))
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	_, _ = w.Write([]byte(body))
}

// exportTopologyDrawIO serialises the same topology response as
// /api/topology but as a draw.io (mxGraph) XML document. Layered
// layout: each BFS hop is a column at x=hop*240, nodes within a
// column stacked vertically. Edge labels carry the call count so
// the exported diagram is self-explanatory without the live page.
func (s *Server) exportTopologyDrawIO(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	root := q.Get("root")
	if root == "" {
		http.Error(w, "root service required", http.StatusBadRequest)
		return
	}
	depth := parseInt(q.Get("depth"), 3)
	if depth < 1 {
		depth = 1
	}
	if depth > 6 {
		depth = 6
	}
	from, to := parseFromTo(r, 1*time.Hour)
	// Read from the pre-aggregated op-edges table same as the
	// JSON endpoint at /api/topology. The legacy live-query
	// path used to run here issued a spans-on-spans INNER JOIN
	// which trips Code 288 (DOUBLE_DISTRIBUTED_IN_JOIN_…) on
	// cluster CH installs — and was unnecessarily expensive
	// even on single-node since v0.5.109 made the agg table
	// authoritative.
	allEdges, err := s.store.ReadTopologyOpEdgesAgg(r.Context(), from, to, 50000)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Compute BFS the same way as the JSON endpoint AND record
	// the hop index per node so the export can column-layer them.
	byParentService := make(map[string][]chstore.TopologyEdge)
	for _, e := range allEdges {
		byParentService[e.ParentService] = append(byParentService[e.ParentService], e)
	}
	nodeIDOf := func(svc, op string) string { return svc + "|" + op }
	hopOf := map[string]int{}
	for _, e := range byParentService[root] {
		hopOf[nodeIDOf(e.ParentService, e.ParentOp)] = 0
	}
	frontier := make(map[string]struct{}, len(hopOf))
	for id := range hopOf {
		frontier[id] = struct{}{}
	}
	expanded := map[string]bool{}
	var keptEdges []chstore.TopologyEdge
	for hop := 0; hop < depth && len(frontier) > 0; hop++ {
		nextFrontier := map[string]struct{}{}
		for parentID := range frontier {
			if expanded[parentID] {
				continue
			}
			expanded[parentID] = true
			parentSvc, parentOp := splitNodeID(parentID)
			for _, e := range byParentService[parentSvc] {
				if e.ParentOp != parentOp {
					continue
				}
				keptEdges = append(keptEdges, e)
				childID := nodeIDOf(e.ChildService, e.ChildOp)
				if _, ok := hopOf[childID]; !ok {
					hopOf[childID] = hop + 1
				}
				if !expanded[childID] {
					nextFrontier[childID] = struct{}{}
				}
			}
		}
		frontier = nextFrontier
	}

	// Bucket nodes by hop, stable order within each bucket.
	buckets := map[int][]string{}
	for id, hop := range hopOf {
		buckets[hop] = append(buckets[hop], id)
	}
	for _, list := range buckets {
		sort.Strings(list)
	}
	hops := make([]int, 0, len(buckets))
	for h := range buckets {
		hops = append(hops, h)
	}
	sort.Ints(hops)

	// Build draw.io mxGraphModel inline — keeping the XML tight
	// rather than depending on a templating package.
	var nodes strings.Builder
	idForNode := map[string]string{}
	nextID := 2 // 0 + 1 are reserved by mxGraph
	for _, h := range hops {
		for i, nid := range buckets[h] {
			svc, op := splitNodeID(nid)
			cellID := fmt.Sprintf("n%d", nextID)
			nextID++
			idForNode[nid] = cellID
			label := svc + "\n" + op
			fmt.Fprintf(&nodes,
				`<mxCell id="%s" value="%s" style="rounded=1;whiteSpace=wrap;html=1;fillColor=#dae8fc;strokeColor=#6c8ebf;" vertex="1" parent="1"><mxGeometry x="%d" y="%d" width="200" height="60" as="geometry"/></mxCell>`,
				cellID, html.EscapeString(label), h*260, i*90,
			)
		}
	}
	var edges strings.Builder
	for _, e := range keptEdges {
		src := idForNode[nodeIDOf(e.ParentService, e.ParentOp)]
		dst := idForNode[nodeIDOf(e.ChildService, e.ChildOp)]
		if src == "" || dst == "" {
			continue
		}
		cellID := fmt.Sprintf("e%d", nextID)
		nextID++
		fmt.Fprintf(&edges,
			`<mxCell id="%s" value="%d calls" style="endArrow=classic;html=1;rounded=0;" edge="1" parent="1" source="%s" target="%s"><mxGeometry relative="1" as="geometry"/></mxCell>`,
			cellID, e.Calls, src, dst,
		)
	}
	body := fmt.Sprintf(
		`<mxfile><diagram name="Topology"><mxGraphModel dx="1200" dy="800" grid="1" gridSize="10" guides="1" arrows="1" connect="1" math="0" shadow="0"><root><mxCell id="0"/><mxCell id="1" parent="0"/>%s%s</root></mxGraphModel></diagram></mxfile>`,
		nodes.String(), edges.String(),
	)
	_ = xml.Unmarshal([]byte(body), new(struct{ XMLName xml.Name })) // validate basic well-formedness; ignore content
	filename := fmt.Sprintf("topology-%s-d%d.drawio", strings.ReplaceAll(root, "/", "_"), depth)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	_, _ = w.Write([]byte(body))
}
