package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
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
	DbName    string  `json:"dbName,omitempty"` // db.name (schema/instance) — database nodes only
	Env       string  `json:"env,omitempty"`    // deployment.environment
	Calls     uint64  `json:"calls"`            // node throughput (inbound preferred, else outbound)
	Errors    uint64  `json:"errors"`
	ErrorRate float64 `json:"errorRate"` // (errors/calls)*100 — drives health color
	Rate      float64 `json:"rate"`      // calls per minute over the window — node-size encoding (v0.8.x)
	// v0.9.367 — which population Calls/Errors/ErrorRate came from:
	// "inbound" (calls INTO the node — the normal basis) or "outbound"
	// (the entry-service fallback: no instrumented caller, so the totals
	// are what the node's DEPENDENCIES returned). The UI labels the two
	// differently; without this the fallback was silent and a gateway's
	// health dot read as its own error rate.
	CallsBasis string `json:"callsBasis,omitempty"`
	// v0.9.1026 — kuyruk düğümünün messaging cluster'ı. YALNIZ
	// kind=="queue" düğümlerde dolu olabilir; /messaging çekmecesinin
	// kimliği (system, cluster, destination) üçlüsü olduğu için bu alan
	// gelmeden derin link kurulamıyordu (v0.9.972'nin gerçek blokörü).
	//
	// Boş kalması meşru üç hâl: kolonun inmediği kurulum/boot
	// (hasTopoClusterCol false), v0.9.1025 öncesi yazılmış kovalar, ve
	// kuyruk olmayan düğümler. İstemci bu hâlde daraltılmış KATALOG
	// köprüsüne düşer; '(default)' varsaymaz.
	Cluster string `json:"cluster,omitempty"`
}

// GraphEdge is one directed caller→callee edge carrying RED metrics + protocol.
type GraphEdge struct {
	Source    string  `json:"source"`
	Target    string  `json:"target"`
	Calls     uint64  `json:"calls"`
	Errors    uint64  `json:"errors"`
	ErrorRate float64 `json:"errorRate"`
	Rate      float64 `json:"rate"` // calls per minute over the window (v0.8.x)
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
	// TotalNodes / ShownNodes (v0.8.295, re-land of v0.8.277) — set by
	// pruneServiceGraphTopN. When the global render budget trims a large
	// graph, ShownNodes < TotalNodes and the UI can show "showing X of Y
	// services" (same contract as the v0.8.215 cap on sampled
	// /api/service-map).
	TotalNodes int `json:"totalNodes"`
	ShownNodes int `json:"shownNodes"`
}

// serviceGraphTopNClamp resolves the ?topN= overview cap. v0.8.295 (re-land
// of v0.8.278; operator-reported: the full topology is unreadable on the
// 1000+-service install): GLOBAL scope is NEVER uncapped — absent/zero/
// negative/garbage all mean the 500-node render budget, and requests above
// the budget clamp down to it. Neighborhood scope returns 0 (never pruned —
// it's already focus-scoped, and pruning would silently hide direct
// dependencies). Pure + table-tested.
func serviceGraphTopNClamp(raw, scope string) int {
	if scope != "global" {
		return 0
	}
	n, _ := strconv.Atoi(raw)
	if n <= 0 || n > 500 {
		return 500
	}
	return n
}

// pruneServiceGraphTopN bounds the overview graph to the topN heaviest nodes
// so a 1000s-service production map returns readably instead of a hairball
// (the v0.8.215 rule, ported to the MV-backed path; re-landed v0.8.295).
// Nodes rank by Calls desc with ErrorRate desc as the tiebreak (a high-error
// node survives a throughput tie) and ID asc as the stable final tiebreak;
// edges are kept only when BOTH endpoints survive. TotalNodes/ShownNodes are
// always set. topN<=0 or a within-budget graph = no prune. Pure +
// order-stable so it's unit-tested without ClickHouse.
func pruneServiceGraphTopN(g *ServiceGraphResponse, topN int) {
	if g == nil {
		return
	}
	g.TotalNodes = len(g.Nodes)
	if topN <= 0 || len(g.Nodes) <= topN {
		g.ShownNodes = len(g.Nodes)
		return
	}
	ranked := append([]GraphNode(nil), g.Nodes...)
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Calls != ranked[j].Calls {
			return ranked[i].Calls > ranked[j].Calls
		}
		if ranked[i].ErrorRate != ranked[j].ErrorRate {
			return ranked[i].ErrorRate > ranked[j].ErrorRate
		}
		return ranked[i].ID < ranked[j].ID
	})
	kept := ranked[:topN]
	keepSet := make(map[string]bool, topN)
	for _, n := range kept {
		keepSet[n.ID] = true
	}
	edges := make([]GraphEdge, 0, len(g.Edges))
	for _, e := range g.Edges {
		if keepSet[e.Source] && keepSet[e.Target] {
			edges = append(edges, e)
		}
	}
	g.Nodes = kept
	g.Edges = edges
	g.ShownNodes = topN
}

// serviceGraphHopsClamp resolves the ?hops= neighborhood radius (v0.8.294).
// Neighborhood scope defaults to 1 and clamps to [1,3] — 3 hops on a dense
// graph already approaches the whole estate, and the client caps the render
// at ~40 nearest nodes anyway. Any other scope returns 0: hops is meaningless
// on a global read and must not fragment the cache key. Pure + table-tested.
func serviceGraphHopsClamp(raw, scope string) int {
	if scope != "neighborhood" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 1
	}
	if n > 3 {
		return 3
	}
	return n
}

// neighborhoodKeepSet returns the node ids within `hops` of the focus,
// walking downstream via OUT edges only and upstream via IN edges only —
// each direction with its own seen-set. This MIRRORS the client's
// assignFocusColumns walk (the v0.8.39 "won't branch at 2 hops" fix): a
// caller's OTHER dependency (a "sibling") mixes directions and is never
// included. Extracted pure (v0.8.294) so the service-detail Topology tab can
// stop downloading the entire global graph to BFS ≤40 nodes client-side.
func neighborhoodKeepSet(edges []chstore.ServiceTopologyEdge, focus string, hops int) map[string]bool {
	if hops < 1 {
		hops = 1
	}
	out := map[string][]string{} // parent → children (downstream adjacency)
	in := map[string][]string{}  // child → parents (upstream adjacency)
	for _, e := range edges {
		out[e.ParentService] = append(out[e.ParentService], e.ChildNode)
		in[e.ChildNode] = append(in[e.ChildNode], e.ParentService)
	}
	keep := map[string]bool{focus: true}
	for dir := 0; dir < 2; dir++ {
		adj := out
		if dir == 1 {
			adj = in
		}
		frontier := []string{focus}
		seen := map[string]bool{focus: true} // per-direction so a cycle can reach both sides
		for h := 0; h < hops && len(frontier) > 0; h++ {
			var next []string
			for _, id := range frontier {
				for _, nb := range adj[id] {
					if !seen[nb] {
						seen[nb] = true
						keep[nb] = true
						next = append(next, nb)
					}
				}
			}
			frontier = next
		}
	}
	return keep
}

// nodeKindFromID — düğümün kind'ı HAM ID ÖNEKİNDEN (v0.9.1028).
//
// ─── Hangi bug'ı kapatıyor ──────────────────────────────────────────
// Kuyruk düğümü kenarın İKİ tarafında da durabiliyor: producer
// kenarında CHILD (svc → queue:…), consumer kenarında PARENT
// (queue:… → svc). buildServiceGraph parent'ı `ensure(…, "service")`
// ile yaratıyordu ("a parent is always a service") ve ensure var olan
// düğümü DÖNDÜRÜP kind'ı bir daha değerlendirmiyordu. Sonuç: consumer
// kenarı producer kenarından ÖNCE işlenen her topic kind="service"
// olarak donuyordu.
//
// Bu yüzden hata SIRA-BAĞIMLIYDI — yani sabit değil, kenar sırasına
// göre gelip giden bir kimlik. Canlıda ölçüldü (2026-08-14, lokal
// küme): 17 kuyruk topic'inin 6'sı kind="service" dönüyordu, id'leri
// doğruyken (`queue:kafka:payment.settled`). O 6 düğüm frontend'de
// nodeDetailHref'in servis dalına düşüyor ve `/service?name=queue:…`
// gibi VAR OLMAYAN bir sayfaya link veriyordu — tam da v0.9.958'in
// kapattığı çıkmazın kuyruk tarafında açık kalmış hâli.
//
// ─── Neden ÖNEK, neden "yükseltme" değil ────────────────────────────
// İki çözüm vardı: (a) ensure daha spesifik bir kind geldiğinde
// yükseltsin, (b) kimlik ham id'den türesin. (a) "daha spesifik"in
// sıralamasını tanımlamayı gerektirir (service < queue mi?) ve hatayı
// SIRA-BAĞIMLI bırakır — yalnız pencereyi daraltır. (b) sırayı
// tamamen denklemden çıkarır: ilk yaratımda zaten doğru kind'ı verir,
// dolayısıyla yükseltme yoluna hiç ihtiyaç kalmaz. Ayrıca v0.9.1026'da
// cluster'ı yerleştirirken AYNI tercihi yapmıştık (node_kind consumer
// kenarında yalan söylüyor, ham id söylemiyor) — bu commit o tercihi
// düğümün kimliğinin tamamına genelliyor.
//
// Önek AGGREGATOR tarafından yazılıyor (chstore/topology.go: 'db:',
// 'queue:', 'ext:') ve altyapı düğümleri için TEK otoriter kaynak.
// Öneksiz id'ler (düz servis adları) çağıranın ipucunu korur.
//
// Dönüş değerleri nodeKindToOTel'in ürettikleriyle BİREBİR aynı olmak
// zorunda — ayrışırlarsa aynı düğüm hangi taraftan görüldüğüne göre iki
// farklı kind alır ve bug'ın kendisi geri gelir. TestNodeKindFromIDMatchesOTel
// bunu çiviliyor.
func nodeKindFromID(id string) (string, bool) {
	switch {
	case strings.HasPrefix(id, "queue:"):
		return "queue", true
	case strings.HasPrefix(id, "db:"):
		return "database", true
	case strings.HasPrefix(id, "ext:"):
		return "external", true
	}
	return "", false
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
		// v0.9.972 — the system used to be dropped on the floor, so a queue
		// node reached the client with kind="queue" and system="". That was
		// the whole reason the topology→/messaging bridge could not be built:
		// not missing DATA, just an identity the server already had and threw
		// away. Three name shapes are in the wild and all three are handled
		// here, on the server, ONCE — re-deriving the system in the client
		// would make a fourth mirror of a rule that already exists in three
		// places (aggregator, this decoder, the graph renderer).
		//   "queue:<sys>:<dest>"  → kafka:payment.settled
		//   "queue:<sys>@<host>"  → rabbitmq@broker-1
		//   "queue:<sys>"         → sqs
		rest := strings.TrimPrefix(raw, "queue:")
		sys := rest
		if i := strings.IndexAny(rest, ":@"); i >= 0 {
			sys = rest[:i]
		}
		return rest, sys
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
	// v0.8.264 (operator-reported): the hidden-pattern policy
	// (v0.8.241, defaults kafka:log*/kafka:bsa*) was applied to
	// /api/topology and /api/service-map but NOT here — and THIS
	// endpoint feeds FocusedNeighborhood, i.e. both /topology's
	// focused view AND the service-detail Topology tab. The
	// config-server drops a kafka client into every project, so
	// unfiltered kafka:log*/kafka:bsa* queue nodes drowned those
	// graphs. Same matcher as the other two endpoints; digest in
	// the cache key per the v0.5.187 rule.
	// Neighborhood radius (v0.8.294) — server-side hops so the service-detail
	// Topology tab stops downloading the whole global graph for a ≤40-node
	// view. 0 outside neighborhood scope; in the cache key.
	hops := serviceGraphHopsClamp(q.Get("hops"), scope)
	// Global render budget (v0.8.295, re-land of v0.8.277/278): the global
	// map is NEVER uncapped — see serviceGraphTopNClamp. In the cache key.
	topN := serviceGraphTopNClamp(q.Get("topN"), scope)
	hidPats := s.topologyHiddenPatterns(r.Context())
	key := fmt.Sprintf("servicegraph:focus=%s:scope=%s:from=%d:to=%d:top=%d:hops=%d:hid=%s",
		focus, scope, from.Unix()/60, to.Unix()/60, topN, hops, hiddenDigest(hidPats))
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		// v0.9.366 — neighborhood scope'ta okuma odak-kapsamlı: eski yol
		// tüm filonun calls-DESC ilk 20k kenarını çekip Go'da hop-yürüyordu;
		// 20k'yı aşan filoda odağın SESSİZ bağımlılıkları LIMIT penceresinden
		// düşüp sekmeden kayboluyordu. IN-filtreli hop yürüyüşü kesmeyi
		// komşuluğun İÇİNE taşır. Global scope aynı kaldı.
		var edges []chstore.ServiceTopologyEdge
		var err error
		if scope == "neighborhood" && focus != "" {
			edges, err = s.store.ReadServiceTopologyAggForFocus(ctx, from, to, focus, hops, 20000)
		} else {
			edges, err = s.store.ReadServiceTopologyAgg(ctx, from, to, 20000)
		}
		if err != nil {
			return nil, err
		}
		edges = filterHiddenTopologyEdges(edges, hidPats)
		// Best-effort db.name enrichment for database nodes; a lookup failure
		// leaves nodes unannotated rather than failing the whole graph.
		dbNames, _ := s.store.DbNamesBySystem(ctx, from, to)
		g := buildServiceGraph(edges, focus, scope, hops, dbNames, serviceGraphWindowMinutes(from, to))
		pruneServiceGraphTopN(&g, topN)
		return g, nil
	})
}

// filterHiddenTopologyEdges drops every edge that touches a hidden
// node (either endpoint). Pure so the kafka:log*/kafka:bsa* class of
// operator-hidden noise is unit-testable without ClickHouse.
func filterHiddenTopologyEdges(edges []chstore.ServiceTopologyEdge, patterns []string) []chstore.ServiceTopologyEdge {
	if len(patterns) == 0 {
		return edges
	}
	hidden := hiddenNodeMatcher(patterns)
	kept := edges[:0]
	for _, e := range edges {
		if hidden(e.ParentService) || hidden(e.ChildNode) {
			continue
		}
		kept = append(kept, e)
	}
	return kept
}

// serviceGraphWindowMinutes returns the window length in minutes used to derive
// each node/edge calls-per-minute Rate, FLOORED at 1 so a sub-minute or zero
// window can't divide-by-zero or inflate the rate. (v0.8.x — the one metric
// Uptrace's service graph derives that Coremetry lacked; p99/avg/errors already
// ride every edge. Pure + table-driven tested per the unit-mixing discipline.)
func serviceGraphWindowMinutes(from, to time.Time) float64 {
	if m := to.Sub(from).Minutes(); m >= 1 {
		return m
	}
	return 1
}

// buildServiceGraph is the pure transform from MV edge rows to the OTel-native
// {nodes, edges} model. Extracted so it's unit-testable without ClickHouse.
// hops bounds the neighborhood walk (v0.8.294); ignored outside that scope.
func buildServiceGraph(edges []chstore.ServiceTopologyEdge, focus, scope string, hops int, dbNames map[string]string, windowMin float64) ServiceGraphResponse {
	if windowMin < 1 {
		windowMin = 1
	}
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

	// Neighborhood scope: keep only edges whose BOTH endpoints sit within
	// `hops` of the focus — direction-separated walk (neighborhoodKeepSet,
	// v0.8.294) so callers stay upstream, dependencies downstream, and a
	// caller's other dependency never leaks in. hops=1 is the pre-v0.8.294
	// behavior (focus + direct callers/callees + links among them).
	if scope == "neighborhood" && focus != "" {
		neigh := neighborhoodKeepSet(edges, focus, hops)
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
		// v0.9.1028 — ham id öneki çağıranın ipucunu EZER. Böylece
		// düğümün kind'ı hangi kenarın önce işlendiğinden bağımsız
		// olur; bkz. nodeKindFromID'deki gerekçe.
		if k, ok := nodeKindFromID(id); ok {
			kind = k
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
		// v0.9.1026 — kenarın cluster'ı, o kenardaki KUYRUK düğümüne ait.
		// Hangi taraf olduğu kenarı üreten pass'e göre değişir: producer
		// kenarında kuyruk CHILD (svc → queue:…), consumer kenarında
		// PARENT (queue:… → svc). Ham id'nin 'queue:' öneki tek güvenilir
		// ayraç — node_kind consumer kenarında parent'ı "service" diye
		// etiketliyor (ensure() parent'ı daima servis varsayar).
		//
		// Boş atanmıyor: aynı düğüme dokunan kenarlardan biri cluster
		// taşımıyorsa (eski kova), taşıyan bir diğeri onu SİLMESİN.
		if e.Cluster != "" {
			if strings.HasPrefix(src.ID, "queue:") {
				src.Cluster = e.Cluster
			}
			if strings.HasPrefix(tgt.ID, "queue:") {
				tgt.Cluster = e.Cluster
			}
		}
		outCalls[src.ID] += e.Calls
		outErrs[src.ID] += e.Errors
		inCalls[tgt.ID] += e.Calls
		inErrs[tgt.ID] += e.Errors
		graphEdges = append(graphEdges, GraphEdge{
			Source: e.ParentService, Target: e.ChildNode,
			Calls: e.Calls, Errors: e.Errors, ErrorRate: e.ErrorRate,
			Rate:  float64(e.Calls) / windowMin,
			AvgMs: e.AvgMs, P99Ms: e.P99Ms, Protocol: e.Protocol,
		})
	}

	out := make([]GraphNode, 0, len(nodes))
	for _, n := range nodes {
		// Health reflects inbound traffic (errors observed calling INTO the
		// node); a root with no inbound falls back to its outbound totals.
		if c := inCalls[n.ID]; c > 0 {
			n.Calls, n.Errors = c, inErrs[n.ID]
			n.CallsBasis = "inbound"
		} else {
			n.Calls, n.Errors = outCalls[n.ID], outErrs[n.ID]
			n.CallsBasis = "outbound"
		}
		if n.Calls > 0 {
			n.ErrorRate = float64(n.Errors) / float64(n.Calls) * 100
		}
		n.Rate = float64(n.Calls) / windowMin
		// Enrich a database node with the dominant db.name for its db.system
		// (db_summary_5m). A nil/empty map or unknown system is a no-op.
		if dn := dbNames[n.System]; dn != "" && n.System != "" {
			n.DbName = dn
		}
		out = append(out, *n)
	}
	// v0.8.324 — stable + fully-tiebroken (same contract as
	// pruneServiceGraphTopN): the order lands verbatim in the cached
	// response, and a bare non-stable Calls sort made equal-Calls nodes
	// swap between cache rebuilds/pods.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		if out[i].ErrorRate != out[j].ErrorRate {
			return out[i].ErrorRate > out[j].ErrorRate
		}
		return out[i].ID < out[j].ID
	})

	return ServiceGraphResponse{Nodes: out, Edges: graphEdges, Scope: scope, Focus: focus}
}
