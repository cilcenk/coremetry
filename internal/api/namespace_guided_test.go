package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/entity"
	"github.com/cilcenk/coremetry/internal/mcptools"
)

// v0.10.470 (Faz 2, F2-3) — namespace rotası + ucuz varlık taraması. Adlar SENTETİK.

func TestRouteNamespaceAsk(t *testing.T) {
	cases := []struct {
		q      string
		intent guidedIntent
		query  string
		list   bool
	}{
		{"shop namespace'indeki servisleri getir", guidedNamespaceServices, "shop", false},
		{"shop-payment namespace'i", guidedNamespaceServices, "shop-payment", false},
		{"namespace shop", guidedNamespaceServices, "shop", false},
		{"shop projesindeki servisler", guidedNamespaceServices, "shop", false},
		{"namespace'leri listele", guidedNamespaceServices, "", true},
		{"hangi namespace'ler var", guidedNamespaceServices, "", true},
		// namespace sözcüğü yok → eski rotalar.
		{"checkout servisini göster", guidedFindEntity, "", false},
		{"bugün hava nasıl", guidedNone, "", false},
	}
	for _, c := range cases {
		r := routeGuidedIntent(c.q, feServices, feEnvs, nil, "")
		if r.Intent != c.intent || r.FindQuery != c.query || r.FindList != c.list {
			t.Errorf("%q → %s/%q/%v; want %s/%q/%v", c.q, r.Intent, r.FindQuery, r.FindList, c.intent, c.query, c.list)
		}
	}
}

func TestEntityScanRoute(t *testing.T) {
	for _, ok := range []string{"shop-payment", "shop payment", "core-auth"} {
		if r, got := entityScanRoute(normalizeGuidedMsg(ok)); !got || r.Intent != guidedFindEntity || r.FindQuery == "" {
			t.Errorf("%q taranmalı: %+v %v", ok, r, got)
		}
	}
	for _, no := range []string{"bugün hava nasıl acaba", "neden yavaş", "ok", "servisi göster", "a b c d"} {
		if _, got := entityScanRoute(normalizeGuidedMsg(no)); got {
			t.Errorf("%q taranmamalı", no)
		}
	}
}

func TestNamespaceChipsRoundTrip(t *testing.T) {
	cands := []mcptools.EntityCandidate{
		{Kind: entity.TypeNamespace, Name: "shop", Cluster: "eu-west"},
		{Kind: entity.TypeNamespace, Name: "shop", Cluster: "eu-central"},
		{Kind: entity.TypeService, Name: "checkout-service"},
		{Kind: entity.TypeWorkload, Name: "api", Namespace: "shop"},
	}
	chips := entityCandidateChips(cands)
	if len(chips) != 2 || chips[0] != "shop namespace'i" || chips[1] != "checkout-service" {
		t.Fatalf("çipler: %v", chips)
	}
	if r := routeGuidedIntent(chips[0], feServices, feEnvs, nil, ""); r.Intent != guidedNamespaceServices || r.FindQuery != "shop" {
		t.Errorf("namespace çipi: %+v", r)
	}
	if r := routeGuidedIntent(chips[1], feServices, feEnvs, nil, ""); r.Intent != guidedFindEntity || r.Service != "checkout-service" {
		t.Errorf("servis çipi: %+v", r)
	}
	if r := routeGuidedIntent("Namespace'leri listele", feServices, feEnvs, nil, ""); r.Intent != guidedNamespaceServices || !r.FindList {
		t.Errorf("liste çipi: %+v", r)
	}
}

func TestRenderNamespace(t *testing.T) {
	ov := mcptools.NamespaceOverview{
		Namespace: "shop",
		Workloads: []mcptools.WorkloadRow{
			{Cluster: "eu-west", Workload: "api", Kind: "Deployment", Pods: 2, Telemetry: true, Spans: 150, Errors: 2, Services: []string{"shop-api"}},
			{Cluster: "eu-west", Workload: "db", Kind: "StatefulSet", Pods: 1},
		},
		Services:   []mcptools.NamespaceServiceRow{{Cluster: "eu-west", Service: "shop-api", Pods: 2, Spans: 150, Errors: 2}},
		OrphanPods: 1,
	}
	txt := renderNamespaceCard(ov, 1800)
	for _, want := range []string{"**shop** namespace'i", "cluster: eu-west", "| eu-west | api | Deployment | 2 | var (150 span, 2 hata) | shop-api |", "| eu-west | db | StatefulSet | 1 | yok |", "| eu-west | shop-api | 2 | 150 | 2 |", "1 pod yalnız span'den"} {
		if !strings.Contains(txt, want) {
			t.Errorf("%q yok:\n%s", want, txt)
		}
	}
	empty := renderNamespaceCard(mcptools.NamespaceOverview{Namespace: "core", OrphanPods: 3}, 1800)
	if !strings.Contains(empty, "Katalogda workload yok") || !strings.Contains(empty, "span kaynaklı 3 pod") || !strings.Contains(empty, "telemetri yok ≠ namespace yok") {
		t.Errorf("boş kart dürüst:\n%s", empty)
	}
	list := renderNamespaceList([]mcptools.NamespaceRow{{Cluster: "eu-west", Namespace: "shop", Workloads: 2, Pods: 3}}, []string{"eu-west"})
	if !strings.Contains(list, "**1 namespace**") || !strings.Contains(list, "| eu-west | shop | 2 | 3 |") {
		t.Errorf("liste:\n%s", list)
	}
	cs := renderEntityCandidates("shop", []mcptools.EntityCandidate{{Kind: entity.TypeWorkload, WlKind: "Deployment", Name: "api", Cluster: "eu-west", Namespace: "shop"}})
	if !strings.Contains(cs, "| workload (Deployment) | api | eu-west | shop |") {
		t.Errorf("adaylar:\n%s", cs)
	}
}

func TestParseIntentNamespaceServices(t *testing.T) {
	r, _, ok := parseIntentJSON(`{"intent":"namespace_services","namespace":"shop"}`, feServices, feEnvs, nil, "")
	if !ok || r.Intent != guidedNamespaceServices || r.FindQuery != "shop" {
		t.Fatalf("namespace slotu: ok=%v %+v", ok, r)
	}
	r, _, ok = parseIntentJSON(`{"intent":"namespace_services","namespace":""}`, feServices, feEnvs, nil, "")
	if !ok || !r.FindList {
		t.Fatalf("boş slot liste: ok=%v %+v", ok, r)
	}
}
