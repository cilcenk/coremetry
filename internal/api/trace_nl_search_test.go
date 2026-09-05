package api

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// trace_nl_search_test.go — v0.10.436 (CoSRE router boşlukları D2).

func TestSplitPairFragments(t *testing.T) {
	cases := []struct {
		msg, from, to string
		ok            bool
	}{
		{"checkout-service'den payment-service'e giden isteklerin tamamını göster", "checkout-service", "payment-service", true},
		{"checkout'tan payment'a giden istekler", "checkout", "payment", true},
		{"login external servisinden osbprod (osbprod.example.com) giden istekleri bul", "login external", "osbprod", true},
		{"checkout servisinden payment servisine atılan istekler", "checkout", "payment", true},
		{"from checkout to payment requests", "checkout", "payment", true},
		{"checkout -> payment istekleri", "checkout", "payment", true},
		{"checkout servisi nasıl", "", "", false},
		{"payment'a gelen istekler", "", "", false},
	}
	for _, c := range cases {
		from, to, ok := splitPairFragments(c.msg)
		if ok != c.ok || from != c.from || to != c.to {
			t.Errorf("%q → (%q,%q,%v), want (%q,%q,%v)", c.msg, from, to, ok, c.from, c.to, c.ok)
		}
	}
}

func TestRoutePairRequests(t *testing.T) {
	services := []string{"checkout-service", "payment-service", "bsa-login-external-prod", "bsa-login-internal-prod"}
	r := routeGuidedIntent("checkout-service'den payment-service'e giden isteklerin tamamını göster", services, nil, nil, "")
	if r.Intent != guidedPairRequests || r.PairFrom != "checkout-service" || r.PairTo != "payment-service" || r.PairToKind != "service" || r.Service != "checkout-service" {
		t.Fatalf("servis çifti: %+v", r)
	}
	// İki ad + istek sözcüğü aileye/kıyasa DÜŞMEZ.
	if r.Intent == guidedFamilyHealth {
		t.Fatal("aile rotasına kaçmamalı")
	}
	r = routeGuidedIntent("login external servisinden osbprod (osbprod.example.com) giden istekleri bul", services, nil, nil, "")
	if r.Intent != guidedPairRequests || r.PairFrom != "bsa-login-external-prod" || r.PairTo != "osbprod" || r.PairToKind != "node" {
		t.Fatalf("dış düğüm hedefi: %+v", r)
	}
	// Belirsiz kaynak → sor; çipler diğer yarıyı taşır ve yeniden çözülür.
	r = routeGuidedIntent("login'den payment-service'e giden istekler", services, nil, nil, "")
	if r.Intent != guidedAskService || r.AskIntent != guidedPairRequests || len(r.ServiceOptions) != 2 || r.PairTo != "payment-service" || r.PairMissing != "from" {
		t.Fatalf("belirsiz kaynak sormalı: %+v", r)
	}
	chips := guidedSuggestions(r)
	if len(chips) != 2 || !strings.Contains(chips[0], "'dan payment-service'ye giden istekler") {
		t.Fatalf("çipler: %v", chips)
	}
	for _, c := range chips {
		if rr := routeGuidedIntent(c, services, nil, nil, ""); rr.Intent != guidedPairRequests || rr.PairTo != "payment-service" {
			t.Errorf("çip %q → %+v", c, rr)
		}
	}
	// Linkler eş-görünüm: services=A,B; düğümde search=.
	links := guidedAnswerLinkTargets(guidedRoute{Intent: guidedPairRequests, PairFrom: "checkout-service", PairTo: "payment-service", PairToKind: "service"})
	if len(links) != 2 || links[0].Href != "/traces?services=checkout-service,payment-service&view=list&rootOnly=false" || !strings.Contains(links[0].Label, "birlikte içeren") {
		t.Fatalf("eş-görünüm linki: %+v", links)
	}
	links = guidedAnswerLinkTargets(guidedRoute{Intent: guidedPairRequests, PairFrom: "checkout-service", PairTo: "osbprod.example.com", PairToKind: "node"})
	if links[0].Href != "/traces?service=checkout-service&search=osbprod.example.com" {
		t.Fatalf("düğüm linki: %+v", links)
	}
	for _, sg := range guidedSuggestions(guidedRoute{Intent: guidedPairRequests, PairFrom: "checkout-service", PairTo: "payment-service", PairToKind: "service"}) {
		if routeGuidedIntent(sg, services, nil, nil, "").Intent == guidedNone {
			t.Errorf("öneri yönlenmeli: %q", sg)
		}
	}
}

func TestExtractTraceSearchAndRoute(t *testing.T) {
	services := []string{"checkout-service", "payment-service"}
	cases := []struct {
		msg, frag string
		sql, ok   bool
	}{
		{`checkout-service servisinden içinde osb.example.com geçen trace'leri getir`, "osb.example.com", false, true},
		{`içinde "/api/pay" geçen trace'ler`, "/api/pay", false, true},
		{`checkout servisinde içinde select * from orders geçen trace'ler`, "select * from orders", true, true},
		{`traces containing osb.example.com`, "osb.example.com", false, true},
		{`içinde "/x" geçen loglar`, "", false, false}, // log → D5
		{`checkout en yavaş trace'ler`, "", false, false},
		// v0.10.443 — tırnaksız düz sözcük literal arama olmaz; değer şekli şart.
		{`içinde hata olan trace'ler`, "", false, false},
		{`traces with errors`, "", false, false},
		{`içinde order-123 geçen trace'ler`, "order-123", false, true},
	}
	for _, c := range cases {
		frag, sql, ok := extractTraceSearch(c.msg, guidedTokens(normalizeGuidedMsg(c.msg)))
		if ok != c.ok || frag != c.frag || (ok && sql != c.sql) {
			t.Errorf("%q → (%q,%v,%v), want (%q,%v,%v)", c.msg, frag, sql, ok, c.frag, c.sql, c.ok)
		}
	}
	r := routeGuidedIntent(`checkout-service servisinden içinde osb.example.com geçen trace'leri getir`, services, nil, nil, "")
	if r.Intent != guidedTraceSearch || r.Service != "checkout-service" || r.SearchText != "osb.example.com" || r.SearchSQL {
		t.Fatalf("trace araması: %+v", r)
	}
	// Bulanık servis ("checkout") tek aday → çözülür; servissiz → filo geneli.
	r = routeGuidedIntent(`checkout servisinde içinde "/api/pay" geçen trace'ler`, services, nil, nil, "")
	if r.Intent != guidedTraceSearch || r.Service != "checkout-service" {
		t.Fatalf("bulanık servis: %+v", r)
	}
	r = routeGuidedIntent(`içinde "/api/pay" geçen trace'ler`, services, nil, nil, "")
	if r.Intent != guidedTraceSearch || r.Service != "" {
		t.Fatalf("servissiz: %+v", r)
	}
	// Belirsiz servis ("checkout" iki adı da kapsar) → sor, çip parçayı taşır.
	twin := []string{"checkout-service", "checkout-legacy"}
	r = routeGuidedIntent(`checkout servisinde içinde "/api/pay" geçen trace'ler`, twin, nil, nil, "")
	if r.Intent != guidedAskService || r.AskIntent != guidedTraceSearch || r.SearchText != "/api/pay" || len(r.ServiceOptions) != 2 {
		t.Fatalf("belirsiz servis sormalı: %+v", r)
	}
	for _, c := range guidedSuggestions(r) {
		if rr := routeGuidedIntent(c, twin, nil, nil, ""); rr.Intent != guidedTraceSearch || rr.SearchText != "/api/pay" || rr.Service == "" {
			t.Errorf("çip %q → %+v", c, rr)
		}
	}
	f := traceSearchFilter("checkout-service", "", "select * from orders", true, time.Unix(0, 0), time.Unix(60, 0))
	if len(f.Filters) != 1 || f.Filters[0].Key != "db.statement" || f.Filters[0].Op != "LIKE" || f.Search != "" {
		t.Fatalf("SQL parçası db.statement LIKE: %+v", f)
	}
	if f := traceSearchFilter("", "", "osb", false, time.Unix(0, 0), time.Unix(60, 0)); f.Search != "osb" || f.Limit != guidedTraceSearchLimit {
		t.Fatalf("haystack araması: %+v", f)
	}
	links := guidedAnswerLinkTargets(guidedRoute{Intent: guidedTraceSearch, Service: "checkout-service", SearchText: "osb"})
	if links[0].Href != "/traces?search=osb&service=checkout-service" {
		t.Fatalf("link: %+v", links)
	}
	if l := guidedAnswerLinkTargets(guidedRoute{Intent: guidedTraceSearch, SearchText: "select 1", SearchSQL: true}); !strings.Contains(l[0].Href, "/traces?filters=") || !strings.Contains(l[0].Href, "db.statement") {
		t.Fatalf("SQL linki filters= taşımalı: %+v", l)
	}
	if rr, _, ok := parseIntentJSON(`{"intent":"trace_search","service":"checkout","searchText":"osb.example.com"}`, services, nil, nil, ""); !ok || rr.Intent != guidedTraceSearch || rr.Service != "checkout-service" || rr.SearchText != "osb.example.com" {
		t.Fatalf("sınıflandırıcı trace_search: ok=%v %+v", ok, rr)
	}
	if _, _, ok := parseIntentJSON(`{"intent":"trace_search","searchText":""}`, services, nil, nil, ""); ok {
		t.Fatal("boş parça none")
	}
	// v0.10.443 — belirsiz servis + parça: ask rotası parçayı taşır, çip yeniden çözülür.
	ar, _, ok := parseIntentJSON(`{"intent":"trace_search","service":"checkout-svc","searchText":"osb"}`, services, nil, nil, "")
	if !ok || ar.Intent != guidedAskService || ar.SearchText != "osb" || len(ar.ServiceOptions) == 0 {
		t.Fatalf("ask parçayı taşımalı: ok=%v %+v", ok, ar)
	}
	for _, c := range guidedSuggestions(ar) {
		if rr := routeGuidedIntent(c, services, nil, nil, ""); rr.Intent != guidedTraceSearch || rr.SearchText != "osb" {
			t.Errorf("çip %q → %+v", c, rr)
		}
	}
	lr, _, ok := parseIntentJSON(`{"intent":"log_field","service":"checkout-svc","logField":"url.full","logValue":"/api/pay"}`, services, nil, nil, "")
	if !ok || lr.Intent != guidedAskService || lr.LogField != "url.full" {
		t.Fatalf("log_field ask alanı taşımalı: ok=%v %+v", ok, lr)
	}
	for _, c := range guidedSuggestions(lr) {
		if rr := routeGuidedIntent(c, services, nil, nil, ""); rr.Intent != guidedLogField || rr.LogField != "url.full" || rr.LogValue != "/api/pay" || rr.Service == "" {
			t.Errorf("log_field çipi %q → %+v", c, rr)
		}
	}
}

func TestPairEdgesAndRender(t *testing.T) {
	edges := []chstore.ServiceTopologyEdge{
		{ParentService: "checkout-service", ChildNode: "payment-service", NodeKind: "service", Protocol: "http", Calls: 900, Errors: 9, ErrorRate: 1, AvgMs: 40, P99Ms: 300},
		{ParentService: "checkout-service", ChildNode: "ext:osbprod.example.com", NodeKind: "external", Protocol: "http", Calls: 100, Errors: 0, AvgMs: 80, P99Ms: 500},
		{ParentService: "checkout-service", ChildNode: "db:orders", NodeKind: "db", Protocol: "db", Calls: 5000},
		{ParentService: "payment-service", ChildNode: "checkout-service", NodeKind: "service", Calls: 3},
	}
	m, o := matchPairEdges(edges, "checkout-service", "payment-service", true)
	if len(m) != 1 || m[0].Calls != 900 || len(o) != 2 {
		t.Fatalf("servis eşi: %+v / %+v", m, o)
	}
	m, _ = matchPairEdges(edges, "checkout-service", "osbprod", false)
	if len(m) != 1 || m[0].ChildNode != "ext:osbprod.example.com" {
		t.Fatalf("düğüm parça eşi: %+v", m)
	}
	m, o = matchPairEdges(edges, "checkout-service", "ghost", false)
	if len(m) != 0 || len(o) != 3 || o[0].ChildNode != "db:orders" {
		t.Fatalf("eşleşme yok → hedefler çağrıya göre: %+v", o)
	}
	route := guidedRoute{PairFrom: "checkout-service", PairTo: "payment-service", PairToKind: "service"}
	rows := []chstore.TraceRow{{TraceID: "abc", RootName: "POST /checkout", ServiceName: "checkout-service", DurationMs: 120, SpanCount: 7, HasError: true}}
	ev := renderPairEvidenceTR(route, edges[:1], nil, rows, 3600)
	for _, want := range []string{"900 çağrı, 9 hata (1.0%)", "Toplam: 900 yönlü çağrı", "BİRLİKTE içeren", "doğrudan kenar garantisi değil", "120ms — checkout-service / POST /checkout (7 span, HATA) trace=abc"} {
		if !strings.Contains(ev, want) {
			t.Errorf("kanıt %q içermeli:\n%s", want, ev)
		}
	}
	empty := renderPairEvidenceTR(guidedRoute{PairFrom: "checkout-service", PairTo: "ghost", PairToKind: "node"}, nil, o, nil, 3600)
	if !strings.Contains(empty, "çağrı kenarı YOK") || !strings.Contains(empty, "orders (5000)") {
		t.Fatalf("boş kenar dürüst + hedef listesi:\n%s", empty)
	}
	ts := renderTraceSearchEvidenceTR(rows, guidedRoute{Service: "checkout-service", SearchText: "osb"}, 3600)
	if !strings.Contains(ts, `"osb" geçen trace'ler`) || !strings.Contains(ts, "trace=abc") {
		t.Fatalf("arama kanıtı:\n%s", ts)
	}
	if !strings.Contains(renderTraceSearchEvidenceTR(nil, guidedRoute{SearchText: "x"}, 60), "eşleşen trace yok") {
		t.Fatal("boş sonuç dürüst")
	}
}
