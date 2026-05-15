package api

import (
	"encoding/xml"
	"fmt"
	"html"
	"net/http"
	"sort"
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

// getServiceTopology delivers the full service-level interaction
// graph for a time window. No depth bound — the entire backend
// fabric is small enough (tens to low hundreds of services) to
// render in one view; the SVG layout below handles overflow via
// scrolling. The depth-bounded op-level view at /api/topology
// remains in place for "what does service X talk to" deep dives.
func (s *Server) getServiceTopology(w http.ResponseWriter, r *http.Request) {
	const edgeCap = 5000
	from, to := parseFromTo(r, 1*time.Hour)
	// Cache key includes the rounded window so concurrent requests
	// over the same minute share one CH round-trip. Topology view
	// is a "what does the fabric look like" surface — sub-minute
	// freshness isn't load-bearing, but the underlying self-join
	// is heavy enough at production scale that caching is the
	// difference between "snappy" and "30s timeouts".
	key := fmt.Sprintf("topology-service:from=%d:to=%d", from.UnixNano()/int64(time.Minute), to.UnixNano()/int64(time.Minute))
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
	key := fmt.Sprintf("topology-flow:rs=%s:ro=%s:from=%d:to=%d", rootService, rootOp, from.UnixNano()/int64(time.Minute), to.UnixNano()/int64(time.Minute))
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
		return ServiceTopologyResponse{
			Nodes:     nodesOut,
			Edges:     edges,
			From:      from.UnixNano(),
			To:        to.UnixNano(),
			Truncated: false,
		}, nil
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
	allEdges, err := s.store.GetTopologyEdges(r.Context(), from, to, 50000)
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
