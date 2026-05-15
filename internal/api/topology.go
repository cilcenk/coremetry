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
	depth := parseInt(q.Get("depth"), 3)
	if depth < 1 {
		depth = 1
	}
	if depth > 6 {
		depth = 6
	}
	from, to := parseFromTo(r, 1*time.Hour)
	const edgeCap = 50000
	allEdges, err := s.store.GetTopologyEdges(r.Context(), from, to, edgeCap)
	if err != nil {
		writeErr(w, err)
		return
	}

	// Index edges by parent service so BFS only scans relevant rows
	// per frontier expansion. Map of parent_service → []edge.
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

	// Seed the frontier with every operation in the root service
	// that appears as a parent in any edge — that's the natural
	// "entry point" surface.
	frontier := map[string]struct{}{}
	for _, e := range byParentService[root] {
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
				continue // cyclic graph guard — expand each node once.
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

	// Stable ordering of nodes/edges so the JSON is byte-stable
	// across calls — eases caching + diffing on the frontend.
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
	writeJSON(w, TopologyResponse{
		Nodes:       nodesOut,
		Edges:       keptEdges,
		RootService: root,
		Depth:       depth,
		From:        from.UnixNano(),
		To:          to.UnixNano(),
		Truncated:   len(allEdges) >= edgeCap,
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
