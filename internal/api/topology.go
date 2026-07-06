package api

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"hash/fnv"
	"html"
	"net/http"
	"regexp"
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
	// v0.8.241 — hidden-pattern policy rides the cache key (an edit
	// must not serve the pre-edit graph for the TTL).
	hidPats := s.topologyHiddenPatterns(r.Context())
	key := fmt.Sprintf("topology-op:root=%s:op=%s:depth=%d:from=%d:to=%d:hid=%s", root, rootOp, depth, from.UnixNano()/int64(time.Minute), to.UnixNano()/int64(time.Minute), hiddenDigest(hidPats))
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		// Read from the pre-aggregated op-edges table instead of
		// the live spans self-join. Same shape, 1000× cheaper.
		allEdges, err := s.store.ReadTopologyOpEdgesAgg(ctx, from, to, edgeCap)
		if err != nil {
			return nil, err
		}
		if hidden := hiddenNodeMatcher(hidPats); len(hidPats) > 0 {
			kept := allEdges[:0]
			for _, e := range allEdges {
				if hidden(e.ParentService) || hidden(e.ChildService) {
					continue
				}
				kept = append(kept, e)
			}
			allEdges = kept
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
	// v0.6.48 — server-side scoping for thousand-service fabrics.
	// TotalServices is the distinct service count BEFORE the top-N /
	// focus bound was applied, so the UI can show "showing N of M
	// services — search or focus to refine". Scoped is true when the
	// returned graph is a bounded subset (top-N by call volume, or a
	// focus neighbourhood) rather than the full fabric.
	TotalServices int    `json:"totalServices"`
	Scoped        bool   `json:"scoped"`
	ScopeReason   string `json:"scopeReason,omitempty"` // "top-50 by call volume" | "focus: <svc> +2 hops"
	// v0.7.32 — number of broadcast queue topics whose consumer fan-out was
	// collapsed (a topic with >threshold distinct consumers, e.g. a kafka
	// cache-refresh broadcast). The UI shows a "N broadcast topics collapsed —
	// show" affordance that flips ?broadcast=show. 0 when none / ?broadcast=show.
	BroadcastCollapsed int `json:"broadcastCollapsed,omitempty"`
}

// ServiceTopologyNode is one node in the service-level graph.
// Kind distinguishes a real service from synthetic infra nodes
// (db, queue, cache, external) so the renderer can paint them
// differently without per-node lookup.
type ServiceTopologyNode struct {
	ID   string `json:"id"`   // canonical id used by edges (service name OR "db:postgresql")
	Name string `json:"name"` // display label, sans prefix for infra ("postgresql" not "db:postgresql")
	Kind string `json:"kind"` // "service" | "db" | "queue" | "external"
	// v0.5.312 — Phase 2 enrichment for the topology redux:
	// soft-cluster the diagram by k8s.namespace / service.namespace
	// and paint each node with a health badge from open-problems
	// count. Both are read-time-enriched (no schema change), nil-
	// safe (omitempty), so older frontends keep working.
	Namespace    string `json:"namespace,omitempty"`
	Health       string `json:"health,omitempty"`       // "" | "green" | "yellow" | "red"
	HealthReason string `json:"healthReason,omitempty"` // short "2 open criticals" etc.
	OpenCritical int    `json:"openCritical,omitempty"`
	OpenWarning  int    `json:"openWarning,omitempty"`
	// v0.5.409 — known 3rd-party SaaS / cloud annotation for
	// external nodes. Populated from the edge's ExtDisplay /
	// ExtKind (set by external_catalogue lookup). UI renders a
	// human-readable display name + category badge instead of
	// the raw `ext:api.stripe.com` hostname.
	ExtDisplay string `json:"extDisplay,omitempty"`
	ExtKind    string `json:"extKind,omitempty"`
	// v0.5.410 — display-only environment annotation
	// (deployment.environment / service.namespace /
	// k8s.namespace.name). Populated from the edge that
	// brought this node into the graph. UI surfaces it as a
	// small chip ("prod" / "stage") next to the service name
	// so multi-env installs distinguish at-a-glance.
	Env        string `json:"env,omitempty"`
	// v0.7.32 — for a collapsed broadcast queue node, the real number of
	// distinct consumer services its fan-out was hidden behind. The renderer
	// shows "→ N services (broadcast)" on the node instead of N edges. Only set
	// on queue nodes whose consumer count exceeded the broadcast threshold.
	BroadcastFanout int `json:"broadcastFanout,omitempty"`
}

// ── Topology hidden patterns (v0.8.241, operator-requested) ────────────────
// Nodes matching an operator-defined glob list ("kafka:log*",
// "kafka:bsa*", …) never render in any topology view — a GLOBAL
// policy, not a per-user toggle ("hiç gözükmesin"). Persisted under
// system_settings "topology_hidden"; unset installs use the seeded
// defaults below (the operator's two standing noise sources).

type topologyHidden struct {
	Patterns []string `json:"patterns"`
}

var defaultTopologyHiddenPatterns = []string{"kafka:log*", "kafka:bsa*"}

// topologyHiddenPatterns loads the persisted list; unset or corrupt →
// defaults. One tiny FINAL read per topology request — the graph reads
// beside it are orders of magnitude heavier.
func (s *Server) topologyHiddenPatterns(ctx context.Context) []string {
	raw, err := s.store.GetTopologyHiddenRaw(ctx)
	if err != nil || len(raw) == 0 {
		return defaultTopologyHiddenPatterns
	}
	var th topologyHidden
	if json.Unmarshal(raw, &th) != nil || th.Patterns == nil {
		return defaultTopologyHiddenPatterns
	}
	return th.Patterns
}

// hiddenNodeMatcher compiles the glob list (*, ? wildcards) into one
// predicate over a topology node identifier. The "queue:" prefix is
// stripped before matching so patterns are written the way the UI
// labels queue nodes ("kafka:log.orders"). Pure for tests.
func hiddenNodeMatcher(patterns []string) func(string) bool {
	res := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		expr := "^" + strings.ReplaceAll(strings.ReplaceAll(regexp.QuoteMeta(p), `\*`, ".*"), `\?`, ".") + "$"
		if re, err := regexp.Compile(expr); err == nil {
			res = append(res, re)
		}
	}
	if len(res) == 0 {
		return func(string) bool { return false }
	}
	return func(id string) bool {
		id = strings.TrimPrefix(id, "queue:")
		for _, re := range res {
			if re.MatchString(id) {
				return true
			}
		}
		return false
	}
}

// hiddenDigest is the cache-key fragment for the active pattern set —
// every topology cache key MUST carry it (v0.5.187 rule) or a pattern
// edit would keep serving the pre-edit graph for the TTL.
func hiddenDigest(patterns []string) string {
	m := make(map[string]bool, len(patterns))
	for _, p := range patterns {
		m[p] = true
	}
	return excludeKeyDigest(m)
}

// getTopologyHidden returns the active pattern list (any authenticated
// role — the Topology page shows it read-only to viewers).
func (s *Server) getTopologyHidden(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, topologyHidden{Patterns: s.topologyHiddenPatterns(r.Context())})
}

// putTopologyHidden persists a new pattern list (editor+). An empty
// list is valid — it disables hiding entirely (including the seeded
// defaults, which only apply while NOTHING has ever been saved).
func (s *Server) putTopologyHidden(w http.ResponseWriter, r *http.Request) {
	var in topologyHidden
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if in.Patterns == nil {
		in.Patterns = []string{}
	}
	if len(in.Patterns) > 100 {
		http.Error(w, "at most 100 patterns", http.StatusBadRequest)
		return
	}
	cleaned := make([]string, 0, len(in.Patterns))
	for _, p := range in.Patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if len(p) > 200 {
			http.Error(w, "pattern too long (max 200 chars)", http.StatusBadRequest)
			return
		}
		cleaned = append(cleaned, p)
	}
	raw, _ := json.Marshal(topologyHidden{Patterns: cleaned})
	if err := s.store.PutTopologyHiddenRaw(r.Context(), raw); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	details, _ := json.Marshal(map[string]any{"patterns": cleaned})
	s.audit(r, "topology.hidden.update", "settings", "topology_hidden", string(details))
	writeJSON(w, topologyHidden{Patterns: cleaned})
}

// excludeKeyDigest produces a short stable cache-key fragment for a
// set of strings (v0.5.187). Previously we keyed only on the set
// LENGTH which caused cache poisoning: two different 1-element sets
// ({"foo"} and {"bar"}) collided and served each other's cached
// data. Now we sort + FNV the entries so any two distinct sets
// produce distinct digests while the same set across calls produces
// a stable one. Used by the inbox priority-set hash (api.go).
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

// jsonSetDigest hashes a FilterExpr[]-JSON string into a stable, distinct
// cache-key fragment (sorted + FNV-64a, per the v0.5.187 rule). Used by the
// relations handler for the parent/child predicate sets. It parses the JSON
// to a canonical per-predicate string and SORTS before hashing so that two
// requests whose predicate arrays differ only in element ORDER (a conjunction
// — order is semantically irrelevant) share a cache entry, while any genuine
// difference (a changed op/value/key) produces a distinct digest. Falls back
// to hashing the raw bytes when the JSON doesn't parse, so a malformed input
// still keys distinctly rather than collapsing to a shared key.
func jsonSetDigest(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "0"
	}
	exprs := parseFilters(raw)
	h := fnv.New64a()
	if len(exprs) == 0 {
		// Unparseable / empty-after-parse — hash the raw bytes so distinct
		// malformed inputs don't cross-poison each other.
		h.Write([]byte(raw))
		return fmt.Sprintf("r%x", h.Sum64())
	}
	canon := make([]string, 0, len(exprs))
	for _, e := range exprs {
		canon = append(canon, e.Key+"\x1f"+e.Op+"\x1f"+strings.Join(e.Values, "\x1e"))
	}
	sort.Strings(canon)
	for _, c := range canon {
		h.Write([]byte(c))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum64())
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
	// v0.5.414 — opt-in prior-window comparison drives the
	// what-changed banner. Frontend sends ?compare=prior when
	// the live view is on; backend fetches the same-length
	// immediately-preceding window and merges values onto the
	// existing rows.
	comparePrior := r.URL.Query().Get("compare") == "prior"
	// v0.6.48 — server-side scoping. At thousand-service scale the
	// old "ship all 5k edges, let the client rank + lay out" path
	// froze the browser, drew an unreadable hairball, and surfaced
	// whichever services happened to win the client-side top-N. Now
	// the SERVER bounds the graph:
	//   • focus=<svc>  → return that service + `hops` of neighbours
	//                    (the operator's "what does X talk to" view).
	//   • else         → top-N services by call volume + the edges
	//                    among them. N from ?top (default 60).
	// Either way the payload is bounded so the client renders a small
	// connected set instead of processing the whole fabric. The
	// client's own focus/top controls still refine within the
	// returned set, but the heavy bound is now server-side.
	topN := parseInt(r.URL.Query().Get("top"), 60)
	if topN < 10 {
		topN = 10
	}
	if topN > 300 {
		topN = 300
	}
	focusSvc := strings.TrimSpace(r.URL.Query().Get("focus"))
	focusHops := parseInt(r.URL.Query().Get("hops"), 1)
	if focusHops < 1 {
		focusHops = 1
	}
	if focusHops > 4 {
		focusHops = 4
	}
	// v0.7.32 — broadcast collapse (default ON; ?broadcast=show reveals the full
	// fan-out mesh). A queue topic node with >broadcastFanoutMin distinct
	// consumers (e.g. config-server's kafka cache.refresh to thousands of
	// services) renders as one collapsed hub instead of N edges.
	broadcastShow := r.URL.Query().Get("broadcast") == "show"
	hidPats := s.topologyHiddenPatterns(r.Context())
	key := fmt.Sprintf("topology-service:from=%d:to=%d:noise=%v:mp=%.2f:cmp=%v:top=%d:focus=%s:hops=%d:bc=%v:hid=%s",
		from.UnixNano()/int64(time.Minute), to.UnixNano()/int64(time.Minute),
		noiseShow, minCallPct, comparePrior, topN, focusSvc, focusHops, broadcastShow, hiddenDigest(hidPats))
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		// Read from the topology_edges_5m pre-aggregated table
		// (filled by the background aggregator goroutine every
		// 5 min). The live self-join path used to run here is
		// untenable at production scale — see WriteTopologyBucket
		// for the actual aggregation query.
		edges, err := s.store.ReadServiceTopologyAgg(ctx, from, to, edgeCap)
		if err != nil {
			return nil, err
		}
		// v0.8.241 — hidden-pattern policy: drop edges touching a
		// hidden node BEFORE noise/topN/broadcast processing so a
		// hidden hub can't influence ranking either.
		if hidden := hiddenNodeMatcher(hidPats); len(hidPats) > 0 {
			kept := edges[:0]
			for _, e := range edges {
				if hidden(e.ParentService) || hidden(e.ChildNode) {
					continue
				}
				kept = append(kept, e)
			}
			edges = kept
		}
		// v0.5.414 — prior-window enrichment. Same query against
		// the immediately-preceding equal-length window; values
		// merged onto current rows by (parent, child, protocol).
		// Prior fetch failure is non-fatal: page renders current
		// edges without trend data rather than 500'ing.
		if comparePrior && len(edges) > 0 {
			dur := to.Sub(from)
			priorEdges, perr := s.store.ReadServiceTopologyAgg(ctx, from.Add(-dur), from, edgeCap)
			if perr == nil {
				type k struct{ p, c, pr string }
				idx := make(map[k]*chstore.ServiceTopologyEdge, len(priorEdges))
				for i := range priorEdges {
					idx[k{priorEdges[i].ParentService, priorEdges[i].ChildNode, priorEdges[i].Protocol}] = &priorEdges[i]
				}
				for i := range edges {
					if pe, ok := idx[k{edges[i].ParentService, edges[i].ChildNode, edges[i].Protocol}]; ok {
						edges[i].PriorCalls = pe.Calls
						edges[i].PriorErrors = pe.Errors
						edges[i].PriorAvgMs = pe.AvgMs
						edges[i].PriorP99Ms = pe.P99Ms
					}
				}
			}
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

		// v0.7.32 — collapse broadcast queue fan-out BEFORE the scope bound so a
		// single cache.refresh topic to thousands of consumers doesn't eat the
		// top-N budget + inflate the service count. Returns the consumer count
		// per collapsed topic for the node badge. ?broadcast=show bypasses it.
		var bcFanout map[string]int
		if !broadcastShow {
			edges, bcFanout = collapseBroadcastQueues(edges, broadcastFanoutMin)
		}

		// v0.6.48 — server-side scope bound. Count distinct
		// services across the noise-filtered edge set FIRST so the
		// "showing N of M" banner reflects the real fabric size,
		// then narrow to a bounded subgraph the browser can render
		// without freezing.
		totalServices := countDistinctServices(edges)
		scoped := false
		scopeReason := ""
		if focusSvc != "" {
			edges = focusNeighborhood(edges, focusSvc, focusHops)
			scoped = true
			scopeReason = fmt.Sprintf("focus: %s +%d hop%s", focusSvc, focusHops, plural(focusHops))
		} else if totalServices > topN {
			edges = topNServiceEdges(edges, topN)
			scoped = true
			scopeReason = fmt.Sprintf("top-%d by call volume", topN)
		}

		nodes := map[string]ServiceTopologyNode{}
		addNode := func(id, kind, extDisp, extKind, env string) {
			if existing, ok := nodes[id]; ok {
				if existing.ExtDisplay == "" && extDisp != "" {
					existing.ExtDisplay = extDisp
					existing.ExtKind = extKind
				}
				if existing.Env == "" && env != "" {
					existing.Env = env
				}
				nodes[id] = existing
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
			nodes[id] = ServiceTopologyNode{
				ID: id, Name: name, Kind: kind,
				ExtDisplay: extDisp, ExtKind: extKind,
				Env: env,
			}
		}
		for _, e := range edges {
			addNode(e.ParentService, "service", "", "", e.ParentEnv)
			addNode(e.ChildNode, e.NodeKind, e.ExtDisplay, e.ExtKind, e.ChildEnv)
		}
		nodesOut := make([]ServiceTopologyNode, 0, len(nodes))
		for _, n := range nodes {
			nodesOut = append(nodesOut, n)
		}
		sort.Slice(nodesOut, func(i, j int) bool { return nodesOut[i].ID < nodesOut[j].ID })
		// v0.7.32 — stamp the collapsed-consumer count onto each broadcast
		// queue node so the renderer shows "→ N services (broadcast)" instead
		// of the (dropped) fan-out edges.
		for i := range nodesOut {
			if n := bcFanout[nodesOut[i].ID]; n > 0 {
				nodesOut[i].BroadcastFanout = n
			}
		}
		// v0.5.312 — Phase 2 enrichment: namespace (for soft
		// cluster grouping in the renderer) + open-problem
		// counts (for health colouring). Both done after the
		// node set is final so we look up only what's actually
		// rendered. Soft-fail: a CH error on either lookup
		// leaves nodes un-enriched but doesn't blank the
		// diagram.
		nsMap, _ := s.store.GetServiceNamespaces(ctx, time.Hour)
		probMap, _ := s.store.GetOpenProblemCountsByService(ctx)
		for i := range nodesOut {
			if nodesOut[i].Kind != "service" {
				continue
			}
			if ns, ok := nsMap[nodesOut[i].Name]; ok {
				nodesOut[i].Namespace = ns
			}
			if p, ok := probMap[nodesOut[i].Name]; ok {
				nodesOut[i].OpenCritical = p.Critical
				nodesOut[i].OpenWarning = p.Warning
				switch {
				case p.Critical > 0:
					nodesOut[i].Health = "red"
					nodesOut[i].HealthReason = fmtCount(p.Critical, "open critical")
				case p.Warning > 0:
					nodesOut[i].Health = "yellow"
					nodesOut[i].HealthReason = fmtCount(p.Warning, "open warning")
				default:
					nodesOut[i].Health = "green"
				}
			} else {
				nodesOut[i].Health = "green"
			}
		}
		// Empty-window safety: nil edge slice marshals as JSON
		// `null` which makes the SPA's `data.edges.forEach` crash.
		// Same shape every call so the frontend can stay defensive-
		// free.
		if edges == nil {
			edges = []chstore.ServiceTopologyEdge{}
		}
		return ServiceTopologyResponse{
			Nodes:         nodesOut,
			Edges:         edges,
			From:          from.UnixNano(),
			To:            to.UnixNano(),
			Truncated:     len(edges) >= edgeCap,
			TotalServices:      totalServices,
			Scoped:             scoped,
			ScopeReason:        scopeReason,
			BroadcastCollapsed: len(bcFanout),
		}, nil
	})
}

// broadcastFanoutMin is the distinct-consumer count above which a queue topic
// node is treated as a BROADCAST and its fan-out collapsed by default. 50 is
// well above normal pub/sub (a handful of consumers per topic) but far below a
// cache-invalidation broadcast (config-server → thousands). Fixed for now;
// promote to a system_setting if operators need to tune per install.
const broadcastFanoutMin = 50

// collapseBroadcastQueues hides the consumer fan-out of broadcast queue topics.
// After v0.7.31 each Kafka topic is its own node (queue:<system>:<topic>), so a
// broadcast shows up as one queue parent with a huge distinct-consumer count.
// Any such node above `threshold` has its queue→consumer edges dropped and its
// consumer count returned in the map (keyed by the queue node id) for the
// "→ N services (broadcast)" badge. The producer→queue edge and every
// non-broadcast queue are untouched. Pure + allocation-light (one pass to
// count, one to filter) — runs on the 60s-cached read path. ?broadcast=show
// skips this entirely (the caller gates the call).
func collapseBroadcastQueues(edges []chstore.ServiceTopologyEdge, threshold int) ([]chstore.ServiceTopologyEdge, map[string]int) {
	// Distinct consumers per queue parent (queue→consumer edges have
	// ParentService = "queue:<system>:<topic>").
	consumers := map[string]map[string]struct{}{}
	for _, e := range edges {
		if !strings.HasPrefix(e.ParentService, "queue:") {
			continue
		}
		set := consumers[e.ParentService]
		if set == nil {
			set = map[string]struct{}{}
			consumers[e.ParentService] = set
		}
		set[e.ChildNode] = struct{}{}
	}
	broadcast := map[string]int{}
	for q, set := range consumers {
		if len(set) > threshold {
			broadcast[q] = len(set)
		}
	}
	if len(broadcast) == 0 {
		return edges, nil
	}
	kept := make([]chstore.ServiceTopologyEdge, 0, len(edges))
	for _, e := range edges {
		if _, isBroadcast := broadcast[e.ParentService]; isBroadcast {
			continue // drop the collapsed topic's queue→consumer fan-out edge
		}
		kept = append(kept, e)
	}
	return kept, broadcast
}

// countDistinctServices counts the unique service-kind endpoints in
// an edge set — the parent is always a service; the child is a
// service only when NodeKind == "service" (db / queue / external
// children don't count toward the fabric size the banner reports).
func countDistinctServices(edges []chstore.ServiceTopologyEdge) int {
	seen := map[string]struct{}{}
	for _, e := range edges {
		seen[e.ParentService] = struct{}{}
		if e.NodeKind == "service" {
			seen[e.ChildNode] = struct{}{}
		}
	}
	return len(seen)
}

// topNServiceEdges keeps the N highest-call-volume SERVICES and
// returns only the edges whose endpoints are both in that set. This
// is the default-view bound: at thousand-service scale the busiest N
// (gateways + core services) are what the operator wants to see
// first; everything else is reachable via focus/search. v0.6.48.
//
// Ranking is by total in+out call volume per service, matching the
// client-side ranking that used to live in Topology.tsx — but doing
// it server-side means the browser never receives the full fabric.
func topNServiceEdges(edges []chstore.ServiceTopologyEdge, n int) []chstore.ServiceTopologyEdge {
	score := map[string]uint64{}
	for _, e := range edges {
		score[e.ParentService] += e.Calls
		if e.NodeKind == "service" {
			score[e.ChildNode] += e.Calls
		}
	}
	type sv struct {
		name  string
		calls uint64
	}
	ranked := make([]sv, 0, len(score))
	for k, v := range score {
		ranked = append(ranked, sv{k, v})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].calls != ranked[j].calls {
			return ranked[i].calls > ranked[j].calls
		}
		return ranked[i].name < ranked[j].name // stable tie-break
	})
	keep := map[string]struct{}{}
	for i := 0; i < n && i < len(ranked); i++ {
		keep[ranked[i].name] = struct{}{}
	}
	out := edges[:0]
	for _, e := range edges {
		// Parent must be kept. Child must be kept when it's a
		// service; infra children (db/queue/external) ride along
		// with their kept parent so the operator still sees "kept
		// service → its postgres".
		if _, ok := keep[e.ParentService]; !ok {
			continue
		}
		if e.NodeKind == "service" {
			if _, ok := keep[e.ChildNode]; !ok {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// focusNeighborhood returns the subgraph within `hops` layers of
// `focus`, following edges in BOTH directions (callers + callees).
// Moved server-side (v0.6.48) so a focus on a service outside the
// default top-N still resolves. Bidirectional on purpose: the
// payload is a SUPERSET so the client's own down/both direction
// filter narrows correctly within it — a downstream-only server
// bound would strand the "show me who calls X" (dir=both) view with
// no upstream data to filter.
func focusNeighborhood(edges []chstore.ServiceTopologyEdge, focus string, hops int) []chstore.ServiceTopologyEdge {
	keepNodes := map[string]struct{}{focus: {}}
	frontier := map[string]struct{}{focus: {}}
	for h := 0; h < hops && len(frontier) > 0; h++ {
		next := map[string]struct{}{}
		add := func(id string) {
			if _, seen := keepNodes[id]; !seen {
				next[id] = struct{}{}
				keepNodes[id] = struct{}{}
			}
		}
		for _, e := range edges {
			if _, ok := frontier[e.ParentService]; ok {
				add(e.ChildNode) // outgoing (callee)
			}
			if _, ok := frontier[e.ChildNode]; ok {
				add(e.ParentService) // incoming (caller)
			}
		}
		frontier = next
	}
	out := edges[:0]
	for _, e := range edges {
		_, p := keepNodes[e.ParentService]
		_, c := keepNodes[e.ChildNode]
		if p && c {
			out = append(out, e)
		}
	}
	return out
}

// plural — "" for n==1, "s" otherwise. Inline so the scope-reason
// string reads naturally ("+1 hop" / "+2 hops").
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// fmtCount — short pluralised count helper for HealthReason
// strings ("1 open critical" / "3 open criticals"). Inline to
// avoid pulling a full pluralization package for this one site.
func fmtCount(n int, label string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", label)
	}
	return fmt.Sprintf("%d %ss", n, label)
}

// FlowsResponse lists the top root-anchored business flows in a
// window. Each entry pairs a root signature with its trace count
// and the unique set of services those traces touched.
type FlowsResponse struct {
	Flows []chstore.RootFlow `json:"flows"`
	From  int64              `json:"from"`
	To    int64              `json:"to"`
	// v0.7.39 — total distinct flows in the window (the list is capped at
	// ?top). >len(Flows) → the UI shows "showing N of M flows — raise top".
	TotalFlows int `json:"totalFlows,omitempty"`
}

// getRootFlows surfaces the top business-level entry points by
// trace volume. Used by the /topology page's "Flows" section to
// list flows like POST /login or POST /payment with a chip list
// of involved services.
func (s *Server) getRootFlows(w http.ResponseWriter, r *http.Request) {
	from, to := parseFromTo(r, 1*time.Hour)
	top := parseInt(r.URL.Query().Get("top"), 20)
	key := fmt.Sprintf("topology-flows:from=%d:to=%d:top=%d", from.UnixNano()/int64(time.Minute), to.UnixNano()/int64(time.Minute), top)
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		// Read from the pre-aggregated table (v0.5.112). The live
		// self-join path used to run here would intermittently
		// timeout on high-cardinality installs; the agg table is
		// filled by the same 5-min topology-agg goroutine.
		flows, err := s.store.ReadRootFlowsAgg(ctx, from, to, top)
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
			if lat, err := s.store.ComputeFlowsLatencyP99(ctx, from, to, sigs); err == nil {
				for i := range flows {
					flows[i].P99Ns = lat[flows[i].RootService+"\x00"+flows[i].RootOp]
				}
			}
		}
		// v0.7.39 — total distinct flows in the window so the UI can show
		// "showing N of M flows" and offer to raise ?top. Soft-fail: a count
		// error just leaves TotalFlows 0 (no banner) rather than 500'ing.
		total, _ := s.store.CountRootFlows(ctx, from, to)
		return FlowsResponse{Flows: flows, From: from.UnixNano(), To: to.UnixNano(), TotalFlows: total}, nil
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
	hidPats := s.topologyHiddenPatterns(r.Context())
	key := fmt.Sprintf("topology-flow:rs=%s:ro=%s:from=%d:to=%d:hid=%s",
		rootService, rootOp,
		from.UnixNano()/int64(time.Minute), to.UnixNano()/int64(time.Minute), hiddenDigest(hidPats))
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		edges, err := s.store.GetFlowTopology(ctx, from, to, rootService, rootOp, 5000)
		if err != nil {
			return nil, err
		}
		// v0.8.241 — hidden-pattern policy (same as the global views).
		if hidden := hiddenNodeMatcher(hidPats); len(hidPats) > 0 {
			kept := edges[:0]
			for _, e := range edges {
				if hidden(e.ParentService) || hidden(e.ChildNode) {
					continue
				}
				kept = append(kept, e)
			}
			edges = kept
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
			filtered = append(filtered, e)
		}
		edges = filtered
		nodes := map[string]ServiceTopologyNode{}
		addNode := func(id, kind, extDisp, extKind, env string) {
			if existing, ok := nodes[id]; ok {
				if existing.ExtDisplay == "" && extDisp != "" {
					existing.ExtDisplay = extDisp
					existing.ExtKind = extKind
				}
				if existing.Env == "" && env != "" {
					existing.Env = env
				}
				nodes[id] = existing
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
			nodes[id] = ServiceTopologyNode{
				ID: id, Name: name, Kind: kind,
				ExtDisplay: extDisp, ExtKind: extKind,
				Env: env,
			}
		}
		addNode(rootService, "service", "", "", "")
		for _, e := range edges {
			addNode(e.ParentService, "service", "", "", e.ParentEnv)
			addNode(e.ChildNode, e.NodeKind, e.ExtDisplay, e.ExtKind, e.ChildEnv)
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
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		instances, err := s.store.GetEdgeInstances(ctx, parent, system, kind, from, to, 50)
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
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		ops, err := s.store.ListOpsForService(ctx, service, from, to)
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
