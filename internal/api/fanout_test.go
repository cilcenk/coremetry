package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// fanout_test.go — v0.10.439 (CoSRE router boşlukları D4).

func TestSplitFanoutFragments(t *testing.T) {
	cases := []struct {
		msg, a, b, c string
		ok           bool
	}{
		{"checkout'tan payment'a gidenlerin hepsi ledger'a gidiyor mu", "checkout", "payment", "ledger", true},
		{"checkout-service'den payment-service'e giden isteklerin hepsi ledger-service'e de gidiyor mu, istek başına ortalama kaç ledger çağrısı", "checkout-service", "payment-service", "ledger-service", true},
		{"checkout servisinden payment servisine giden isteklerin tamamı ledger servisine gidiyor mu", "checkout", "payment", "ledger", true},
		{"from checkout to payment requests all go to ledger too?", "checkout", "payment", "ledger", true},
		{"checkout'tan payment'a giden istekler", "", "", "", false},
		// v0.10.443 — "to" sonrası istek sözcüğü şekilli C: panik yok, ad şekli kapısı.
		{"from checkout to payment requests all go to trace-collector too?", "checkout", "payment", "trace-collector", true},
		{"from checkout to payment requests all go to traces too?", "", "", "", false},
		{"checkout'tan payment'a gidenlerin hepsi mu'ya gidiyor", "", "", "", false},
	}
	for _, c := range cases {
		a, b, cc, ok := splitFanoutFragments(c.msg)
		if ok != c.ok || a != c.a || b != c.b || cc != c.c {
			t.Errorf("%q → (%q,%q,%q,%v), want (%q,%q,%q,%v)", c.msg, a, b, cc, ok, c.a, c.b, c.c, c.ok)
		}
	}
}

func TestRouteFanout(t *testing.T) {
	services := []string{"checkout-service", "payment-service", "ledger-service", "checkout-legacy"}
	r := routeGuidedIntent("checkout-service'den payment-service'e gidenlerin hepsi ledger-service'e gidiyor mu, istek başına ortalama kaç", services, nil, nil, "")
	if r.Intent != guidedFanout || r.PairFrom != "checkout-service" || r.PairTo != "payment-service" || r.FanoutTo != "ledger-service" || r.FanoutToKind != "service" {
		t.Fatalf("fanout: %+v", r)
	}
	r = routeGuidedIntent("payment-service'den ledger-service'e gidenlerin hepsi osbprod'a gidiyor mu", services, nil, nil, "")
	if r.Intent != guidedFanout || r.FanoutTo != "osbprod" || r.FanoutToKind != "node" {
		t.Fatalf("düğüm C: %+v", r)
	}
	// Belirsiz A → sor, çip üçlüyü taşır ve yeniden çözülür.
	r = routeGuidedIntent("checkout'tan payment-service'e gidenlerin hepsi ledger-service'e gidiyor mu", services, nil, nil, "")
	if r.Intent != guidedAskService || r.AskIntent != guidedFanout || len(r.ServiceOptions) != 2 {
		t.Fatalf("belirsiz A: %+v", r)
	}
	for _, c := range guidedSuggestions(r) {
		if rr := routeGuidedIntent(c, services, nil, nil, ""); rr.Intent != guidedFanout || rr.FanoutTo != "ledger-service" {
			t.Errorf("çip %q → %+v", c, rr)
		}
	}
	// Fan-out sözcüğü yoksa çift rotası (D2) kalır.
	if r := routeGuidedIntent("checkout-service'den payment-service'e giden istekler", services, nil, nil, ""); r.Intent != guidedPairRequests {
		t.Fatalf("D2 korunmalı: %+v", r)
	}
	links := guidedAnswerLinkTargets(guidedRoute{Intent: guidedFanout, PairFrom: "a", PairTo: "b", FanoutTo: "c", FanoutToKind: "service"})
	if len(links) != 2 || links[0].Href != "/traces?services=a,b,c&view=list&rootOnly=false" || links[1].Href != "/service-map?focus=b" {
		t.Fatalf("linkler: %+v", links)
	}
	for _, sg := range guidedSuggestions(guidedRoute{Intent: guidedFanout, PairFrom: "checkout-service", PairTo: "payment-service", FanoutTo: "ledger-service"}) {
		if routeGuidedIntent(sg, services, nil, nil, "").Intent == guidedNone {
			t.Errorf("öneri yönlenmeli: %q", sg)
		}
	}
}

func TestComputeAndRenderFanout(t *testing.T) {
	E := func(p, c string) chstore.TraceEdge { return chstore.TraceEdge{Parent: p, Child: c} }
	edges := map[string]map[chstore.TraceEdge]int{
		"t1": {E("a", "b"): 1, E("b", "c"): 2},
		"t2": {E("a", "b"): 1, E("b", "c"): 1, E("a", "c"): 1},
		"t3": {E("a", "b"): 1},
		"t4": {E("a", "x"): 1, E("x", "b"): 1}, // dolaylı
	}
	st := computeFanout(edges, "a", "b", "c")
	if st.Sampled != 4 || st.WithAB != 3 || st.WithABC != 2 || st.SumBC != 3 || st.MaxBC != 2 || st.WithAC != 1 || st.NoABList != 1 {
		t.Fatalf("stats: %+v", st)
	}
	if st.avgBC() != 1 {
		t.Fatalf("avg: %v", st.avgBC())
	}
	ev := renderFanoutTR(guidedRoute{PairFrom: "a", PairTo: "b", FanoutTo: "c"}, st, 3600)
	for _, want := range []string{"A→B doğrudan kenarı olan trace: 3/4", "1 trace'te ikisi de var ama doğrudan a→b çağrısı yok", "c'ye de gidenleri: 2/3 (%67)", "bir kısmı gidiyor", "ortalama 1.00, en çok 2", "1 trace'te a doğrudan c'yi de çağırıyor"} {
		if !strings.Contains(ev, want) {
			t.Errorf("kanıt %q içermeli:\n%s", want, ev)
		}
	}
	all := computeFanout(map[string]map[chstore.TraceEdge]int{"t1": {E("a", "b"): 1, E("b", "c"): 1}}, "a", "b", "c")
	if !strings.Contains(renderFanoutTR(guidedRoute{PairFrom: "a", PairTo: "b", FanoutTo: "c"}, all, 60), "HEPSİ gidiyor") {
		t.Fatal("hepsi")
	}
	if !strings.Contains(renderFanoutTR(guidedRoute{PairFrom: "a", PairTo: "b", FanoutTo: "c"}, fanoutStats{}, 60), "Örnekte trace yok") {
		t.Fatal("boş dürüst")
	}
	if !hasFanoutSignal(guidedTokens("gidenlerin hepsi c'ye gidiyor mu")) || !hasFanoutSignal(guidedTokens("istek başına ortalama kaç")) || hasFanoutSignal(guidedTokens("checkout nasıl")) {
		t.Fatal("sinyal")
	}
}
