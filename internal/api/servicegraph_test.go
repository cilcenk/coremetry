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
		// v0.9.972 — was "queue:settlements", which put a TOPIC-shaped name in
		// the SYSTEM position. The aggregator never emits that: the first
		// segment is always msg_system (chstore/topology.go
		// `concat('queue:', msg_system, ':', msg_dest)`, v0.7.31 topic-aware
		// naming). The old fixture therefore taught the wrong shape and let
		// decodeNodeName's dropped system look intentional.
		sgEdge("payments", "queue:kafka:settlements", "queue", "kafka", 200, 0, 5), // broker is first-class
		sgEdge("orders", "ext:stripe.com", "external", "http", 60, 6, 400),
	}
}

func TestBuildServiceGraph_GlobalDecodesOTelKinds(t *testing.T) {
	g := buildServiceGraph(sampleEdges(), "", "global", 0, nil, 60)
	byID := map[string]GraphNode{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}

	// db / queue / external are first-class nodes with OTel-native kinds and
	// prefix-decoded names — no "db:"/"queue:" leaking to the client.
	cases := []struct{ id, wantKind, wantName, wantSystem string }{
		{"db:postgresql", "database", "postgresql", "postgresql"},
		// v0.9.972 — system is no longer dropped: it is what the
		// topology→/messaging catalogue bridge narrows on. Shape coverage
		// (all three queue spellings) lives in servicegraph_nodename_test.go.
		{"queue:kafka:settlements", "queue", "kafka:settlements", "kafka"},
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
	g := buildServiceGraph(edges, "", "global", 0, nil, 60)
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
	g := buildServiceGraph(sampleEdges(), "payments", "neighborhood", 1, nil, 60)
	if g.Scope != "neighborhood" || g.Focus != "payments" {
		t.Fatalf("scope/focus = %q/%q", g.Scope, g.Focus)
	}
	ids := map[string]bool{}
	for _, n := range g.Nodes {
		ids[n.ID] = true
	}
	// payments' neighborhood: gateway (caller), ledger + db + queue (callees).
	for _, want := range []string{"payments", "gateway", "ledger", "db:postgresql", "queue:kafka:settlements"} {
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
	g := buildServiceGraph(sampleEdges(), "", "global", 0, dbNames, 60)
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
	for _, n := range buildServiceGraph(sampleEdges(), "", "global", 0, nil, 60).Nodes {
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
	}, "", "global", 0, nil, 60) // 1h window → 1200/60 = 20 calls/min
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
	}, "", "global", 0, nil, 0)
	if g0.Edges[0].Rate != 5 {
		t.Errorf("windowMin=0 must floor to 1 → rate=calls=5, got %v", g0.Edges[0].Rate)
	}
}

// v0.8.324 — the final node ordering lands verbatim in the cached response;
// a non-stable bare-Calls sort let equal-Calls nodes swap between cache
// rebuilds / pods. Pin the full tiebreak (Calls desc → ErrorRate desc →
// ID asc), mirroring pruneServiceGraphTopN's contract.
func TestBuildServiceGraph_DeterministicNodeOrder(t *testing.T) {
	edges := []chstore.ServiceTopologyEdge{
		sgEdge("root", "beta", "service", "http", 100, 0, 10),
		sgEdge("root", "alpha", "service", "http", 100, 0, 10),
		sgEdge("root", "hot", "service", "http", 100, 50, 10),
	}
	g := buildServiceGraph(edges, "", "global", 0, nil, 60)
	var order []string
	for _, n := range g.Nodes {
		if n.ID != "root" {
			order = append(order, n.ID)
		}
	}
	// all three targets have 100 inbound calls: hot wins on ErrorRate,
	// then alpha < beta by ID.
	want := []string{"hot", "alpha", "beta"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("node order = %v, want %v", order, want)
		}
	}
}

// v0.9.367 regression — the inbound→outbound fallback was silent: an entry
// service (no instrumented caller) surfaced its DEPENDENCIES' totals as its
// own Calls/ERR%, and the health dot followed. callsBasis names the
// population so the UI can label ↓ vs ↑ (and the fallback stays auditable).
func TestBuildServiceGraph_CallsBasis(t *testing.T) {
	g := buildServiceGraph(sampleEdges(), "", "global", 0, nil, 60)
	byID := map[string]GraphNode{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	// orders has NO inbound edge → outbound fallback, dependencies' totals.
	if got := byID["orders"].CallsBasis; got != "outbound" {
		t.Errorf("orders callsBasis = %q, want outbound", got)
	}
	// payments is called by gateway → inbound basis.
	if got := byID["payments"].CallsBasis; got != "inbound" {
		t.Errorf("payments callsBasis = %q, want inbound", got)
	}
	// gateway sits in a cycle (ledger→gateway) so even the estate's entry
	// point has inbound here — the fixture pins that inbound WINS whenever
	// any caller exists, however small (50 calls in vs 1000 out).
	if got := byID["gateway"]; got.CallsBasis != "inbound" || got.Calls != 50 {
		t.Errorf("gateway = basis %q calls %d, want inbound/50", got.CallsBasis, got.Calls)
	}
}

// v0.9.1026 — kuyruk düğümünün cluster'ı, kenarın HANGİ TARAFINDA
// olduğuna bakılmaksızın düğüme yerleşmeli.
//
// Kuyruk düğümü İKİ farklı pass'ten doğuyor ve iki farklı tarafta
// duruyor: producer kenarında CHILD (svc → queue:…), consumer kenarında
// PARENT (queue:… → svc). Tek tarafa bakan bir yerleştirme, grafiğin
// hangi kenarı önce gördüğüne göre cluster'ı olan/olmayan düğüm üretir
// — deep-link ARALIKLI çalışır, ki bu hiç çalışmamaktan daha kötü
// teşhis edilir.
//
// Ayrıca: parent tarafı `ensure(..., "service")` ile yaratılıyor (parent
// daima servis varsayılıyor), yani ayraç node_kind DEĞİL ham id'nin
// 'queue:' öneki olmak ZORUNDA.
func TestBuildServiceGraph_QueueClusterFromEitherSide(t *testing.T) {
	producer := sgEdge("payments", "queue:kafka:settlements", "queue", "kafka", 200, 0, 5)
	producer.Cluster = "broker-1:9092"
	// Tüketici kenarı cluster taşımıyor (ör. kolonun inmediği bir kovadan
	// geldi): taşıyan kenarın verdiği cluster SİLİNMEMELİ.
	consumer := sgEdge("queue:kafka:settlements", "settlement-worker", "service", "kafka", 190, 0, 7)

	for _, tc := range []struct {
		name  string
		edges []chstore.ServiceTopologyEdge
	}{
		{"cluster child tarafında", []chstore.ServiceTopologyEdge{producer, consumer}},
		{"kenar sırası ters", []chstore.ServiceTopologyEdge{consumer, producer}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := buildServiceGraph(tc.edges, "", "global", 0, nil, 60)
			var q *GraphNode
			for i := range g.Nodes {
				if g.Nodes[i].ID == "queue:kafka:settlements" {
					q = &g.Nodes[i]
				}
			}
			if q == nil {
				t.Fatal("kuyruk düğümü grafikte yok")
			}
			if q.Cluster != "broker-1:9092" {
				t.Errorf("kuyruk düğümünün cluster'ı %q, beklenen %q — bu alan olmadan derin link kurulamaz",
					q.Cluster, "broker-1:9092")
			}
			// Servis düğümleri cluster TAŞIMAMALI: alan kuyruk kimliğinin
			// parçası, servisin değil. Sızarsa /messaging linki olmayan bir
			// yerde "varmış gibi" durur.
			for _, n := range g.Nodes {
				if n.ID != "queue:kafka:settlements" && n.Cluster != "" {
					t.Errorf("servis düğümü %q cluster taşıyor (%q) — alan yalnız kuyruk düğümünün", n.ID, n.Cluster)
				}
			}
		})
	}

	// Tüketici kenarı cluster taşıyorsa, kuyruk PARENT tarafındayken de
	// yerleşmeli (asıl regresyon: yalnız child'a bakan bir yerleştirme).
	t.Run("cluster yalnız consumer kenarında", func(t *testing.T) {
		c := consumer
		c.Cluster = "broker-2:9092"
		g := buildServiceGraph([]chstore.ServiceTopologyEdge{c}, "", "global", 0, nil, 60)
		for _, n := range g.Nodes {
			if n.ID == "queue:kafka:settlements" && n.Cluster != "broker-2:9092" {
				t.Errorf("PARENT tarafındaki kuyruk cluster almadı: %q", n.Cluster)
			}
		}
	})
}

// v0.9.1028 regresyon testi — kuyruk düğümünün KIND'ı kenar sırasından
// BAĞIMSIZ olmalı.
//
// Orijinal belirti (v0.9.1026 canlı doğrulamasında bulundu, operatör
// bildirimi değil): /api/servicegraph 17 kuyruk topic'inin 6'sını
// `kind:"service"` döndürüyordu — id'leri doğruyken
// (`queue:kafka:payment.settled`). Kök neden buildServiceGraph'ın
// `ensure(e.ParentService, "service")` çağrısı: kuyruk düğümü consumer
// kenarında PARENT tarafında duruyor ve o kenar producer kenarından önce
// işlenirse düğüm "service" olarak doğuyor; ensure var olanı döndürdüğü
// için kind bir daha değerlendirilmiyordu.
//
// Belirti bir HATA olarak görünmüyordu: düğüm grafikte duruyor, sayıları
// doğru. Yalnız frontend'de nodeDetailHref servis dalına düşüyor ve
// `/service?name=queue:kafka:payment.settled` — var olmayan bir sayfa —
// linkleniyordu.
//
// Test SIRAYI iki yönde de veriyor; sıra-bağımlı bir düzeltme (ör.
// "daha spesifik kind gelirse yükselt") tek yönde geçip diğerinde
// düşerdi, saf-tesadüf bir yeşil vermesin diye.
func TestBuildServiceGraph_QueueKindIsOrderIndependent(t *testing.T) {
	producer := sgEdge("payments", "queue:kafka:payment.settled", "queue", "kafka", 200, 0, 5)
	// Consumer pass'i parent'a kuyruğu, node_kind'a 'service' yazıyor
	// (child GERÇEKTEN bir servis) — yalan söyleyen tam olarak bu alan.
	consumer := sgEdge("queue:kafka:payment.settled", "webhook-dispatcher", "service", "kafka", 190, 0, 7)

	for _, tc := range []struct {
		name  string
		edges []chstore.ServiceTopologyEdge
	}{
		{"producer önce", []chstore.ServiceTopologyEdge{producer, consumer}},
		{"consumer ÖNCE — canlıdaki 6 düğümün hâli", []chstore.ServiceTopologyEdge{consumer, producer}},
		{"yalnız consumer kenarı — kuyruk SADECE parent tarafında", []chstore.ServiceTopologyEdge{consumer}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := buildServiceGraph(tc.edges, "", "global", 0, nil, 60)
			byID := map[string]GraphNode{}
			for _, n := range g.Nodes {
				byID[n.ID] = n
			}
			q, ok := byID["queue:kafka:payment.settled"]
			if !ok {
				t.Fatal("kuyruk düğümü grafikte yok")
			}
			if q.Kind != "queue" {
				t.Errorf("kind = %q, beklenen \"queue\" — bu düğüm frontend'de servis dalına düşer ve /service?name=queue:… (var olmayan sayfa) linklenir", q.Kind)
			}
			// Sistem de önekten çözülmeli; kind düzelip system boş kalsaydı
			// derin link yine kurulamazdı (üçlü kimlik eksik).
			if q.System != "kafka" {
				t.Errorf("system = %q, beklenen \"kafka\"", q.System)
			}
			// Karşı yön: gerçek servisler kuyruk sanılmamalı.
			if n, ok := byID["webhook-dispatcher"]; ok && n.Kind != "service" {
				t.Errorf("gerçek servis kind = %q, beklenen \"service\"", n.Kind)
			}
		})
	}
}

// TestNodeKindFromIDMatchesOTel — iki kimlik kaynağının AYNILIK pini.
//
// nodeKindFromID (önekten) ve nodeKindToOTel (MV'nin node_kind
// kolonundan) aynı düğüm için aynı etiketi üretmek ZORUNDA. Ayrışsalar,
// düğüm hangi taraftan görüldüğüne göre iki farklı kind alırdı — yani
// v0.9.1028'in kapattığı bug'ın ta kendisi, yalnız başka bir kapıdan.
func TestNodeKindFromIDMatchesOTel(t *testing.T) {
	cases := []struct {
		id       string
		mvKind   string // aggregator'ın aynı düğüm için yazdığı node_kind
		wantKind string
		wantPfx  bool
	}{
		{"queue:kafka:payment.settled", "queue", "queue", true},
		{"queue:rabbitmq@broker-1", "queue", "queue", true},
		{"queue:sqs", "queue", "queue", true},
		{"db:postgresql@10.0.1.5", "db", "database", true},
		{"db:h2", "db", "database", true},
		{"ext:stripe.com", "external", "external", true},
		// Öneksiz = düz servis adı: önek KARAR VERMEZ, çağıranın ipucu kalır.
		{"payments", "service", "service", false},
		// Tuzak: 'database' ile BAŞLAYAN bir servis adı 'db:' öneki DEĞİL.
		{"database-proxy", "service", "service", false},
		{"queueing-service", "service", "service", false},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			got, ok := nodeKindFromID(c.id)
			if ok != c.wantPfx {
				t.Fatalf("önek tanıma = %v, beklenen %v", ok, c.wantPfx)
			}
			if !ok {
				return // öneksiz: nodeKindToOTel tek otorite
			}
			if got != c.wantKind {
				t.Errorf("önekten kind = %q, beklenen %q", got, c.wantKind)
			}
			if mv := nodeKindToOTel(c.mvKind); mv != got {
				t.Errorf("İKİ KAYNAK AYRIŞTI: önek %q, MV node_kind %q → %q — aynı düğüm iki kind alır", got, c.mvKind, mv)
			}
		})
	}
}
