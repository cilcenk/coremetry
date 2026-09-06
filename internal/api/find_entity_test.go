package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// find_entity_test.go — v0.10.463 (CoSRE sohbet paritesi D1): "mobile bff"
// yazınca servisler arasında bulma. Operatör örneği; adlar SENTETİK.

var feServices = []string{"mobile-commercial-bff-prod", "mobile-retail-bff-prod", "checkout-service", "payment-service", "inventory", "api-gateway"}
var feEnvs = []string{"prod", "uat"}

func TestRouteFindEntity(t *testing.T) {
	cases := []struct {
		q       string
		intent  guidedIntent
		service string
		opts    int
		list    bool
	}{
		{"mobile bff", guidedFindEntity, "", 2, false},
		{"Mobile bff", guidedFindEntity, "", 2, false},
		{"mobile bff'yi bul", guidedFindEntity, "", 2, false},
		{"mobile bff servisini bul", guidedFindEntity, "", 2, false},
		{"mobile bff'yi bulabilir misin lütfen", guidedFindEntity, "", 2, false},
		{"mobile retail bff", guidedFindEntity, "mobile-retail-bff-prod", 0, false},
		{"mobile-retail-bff-prod", guidedFindEntity, "mobile-retail-bff-prod", 0, false},
		{"checkout", guidedFindEntity, "checkout-service", 0, false},
		{"checkout servisini göster", guidedFindEntity, "checkout-service", 0, false},
		{"checkout sahibi kim", guidedFindEntity, "checkout-service", 0, false},
		{"checkout-service hakkında bilgi", guidedFindEntity, "checkout-service", 0, false},
		{"servisleri listele", guidedFindEntity, "", 0, true},
		{"hangi servisler var", guidedFindEntity, "", 0, true},
		{"servis listesi", guidedFindEntity, "", 0, true},
		{"kaç servis var", guidedFindEntity, "", 0, true},
		// Açıklanamayan jeton → none (LLM'e bırak).
		{"checkout müşterisi kim", guidedNone, "", 0, false},
		{"bugün hava nasıl", guidedNone, "", 0, false},
		{"prod", guidedNone, "", 0, false},
		{"ok", guidedNone, "", 0, false},
		{"bul", guidedNone, "", 0, false},
		{"zzqx servisini bul", guidedNone, "", 0, false},
		// Mevcut rotalar DEĞİŞMEDİ.
		{"mobile bff hataları", guidedFamilyHealth, "", 0, false},
		{"mobile bff yavaş traceler", guidedAskService, "", 2, false},
		{"checkout servisi nasıl", guidedServiceHealth, "checkout-service", 0, false},
		{"checkout sayfasını aç", guidedOpenPage, "checkout-service", 0, false},
	}
	for _, c := range cases {
		r := routeGuidedIntent(c.q, feServices, feEnvs, nil, "")
		if r.Intent != c.intent || r.Service != c.service || len(r.ServiceOptions) != c.opts || r.FindList != c.list {
			t.Errorf("%q → %+v; want intent=%s service=%q opts=%d list=%v", c.q, r, c.intent, c.service, c.opts, c.list)
		}
	}
	// Servis sayfasında açık ad yine kazanır; yalnız fiil (özne yok) none kalır.
	if r := routeGuidedIntent("checkout", feServices, feEnvs, nil, "payment-service"); r.Service != "checkout-service" {
		t.Errorf("bağlam açık adı ezmemeli: %+v", r)
	}
	if r := routeGuidedIntent("servisi göster", feServices, feEnvs, nil, "payment-service"); r.Intent != guidedNone {
		t.Errorf("öznesiz fiil bağlamla kart üretmemeli: %+v", r)
	}
}

// Aday çipleri ÇIPLAK adlar: tıklanınca aynı kademede tek servise çözülür.
func TestFindEntityChipsRoundTrip(t *testing.T) {
	r := routeGuidedIntent("mobile bff", feServices, feEnvs, nil, "")
	chips := guidedSuggestions(r)
	if len(chips) != 2 || chips[0] != "mobile-commercial-bff-prod" {
		t.Fatalf("aday çipleri: %v", chips)
	}
	for _, c := range chips {
		got := routeGuidedIntent(c, feServices, feEnvs, nil, "")
		if got.Intent != guidedFindEntity || got.Service != c {
			t.Errorf("çip %q → %+v", c, got)
		}
	}
	// Kart çipleri: her biri kendi rotasına gider (serbest döngüye düşmez).
	card := routeGuidedIntent("checkout servisini göster", feServices, feEnvs, nil, "")
	for _, c := range guidedSuggestions(card) {
		if got := routeGuidedIntent(c, feServices, feEnvs, nil, ""); got.Intent == guidedNone || got.Service != "checkout-service" {
			t.Errorf("kart çipi %q → %+v", c, got)
		}
	}
	// Liste çipleri: bulunan adlar (çıplak) → kart.
	list := guidedRoute{Intent: guidedFindEntity, FindList: true, TeamServices: []string{"inventory", "api-gateway"}}
	for _, c := range guidedSuggestions(list) {
		if got := routeGuidedIntent(c, feServices, feEnvs, nil, ""); got.Intent != guidedFindEntity || got.Service != c {
			t.Errorf("liste çipi %q → %+v", c, got)
		}
	}
}

// Katalog okuma kapısı: bulma fiili taşıyan 6 jetonlu cümle geçer.
func TestFindEntityGate(t *testing.T) {
	norm := normalizeGuidedMsg("mobile bff'yi bulabilir misin lütfen")
	if mayNameTeam(norm) {
		t.Fatal("test varsayımı: 6 jeton mayNameTeam'den geçmemeli")
	}
	if !hasFindSignal(guidedTokens(norm)) {
		t.Fatal("bulma fiili kapıyı açmalı")
	}
}

func TestParseIntentFindEntity(t *testing.T) {
	r, _, ok := parseIntentJSON(`{"intent":"find_entity","service":"mobile bff"}`, feServices, feEnvs, nil, "")
	if !ok || r.Intent != guidedFindEntity || len(r.ServiceOptions) != 2 || r.FindQuery != "mobile bff" {
		t.Fatalf("yaklaşık ad: ok=%v %+v", ok, r)
	}
	r, _, ok = parseIntentJSON(`{"intent":"find_entity","service":"checkout-service"}`, feServices, feEnvs, nil, "")
	if !ok || r.Service != "checkout-service" || len(r.ServiceOptions) != 0 {
		t.Fatalf("tam ad: ok=%v %+v", ok, r)
	}
	r, _, ok = parseIntentJSON(`{"intent":"find_entity","service":""}`, feServices, feEnvs, nil, "")
	if !ok || !r.FindList {
		t.Fatalf("boş slot liste olmalı: ok=%v %+v", ok, r)
	}
	if _, _, ok = parseIntentJSON(`{"intent":"find_entity","service":"zzqx"}`, feServices, feEnvs, nil, ""); ok {
		t.Fatal("uydurma ad none olmalı")
	}
}

func TestRenderFindEntity(t *testing.T) {
	row := &chstore.ServiceSummary{Name: "checkout-service", SpanCount: 3600, ErrorCount: 36, ErrorRate: 1, P99Ms: 420}
	txt := renderFindEntityCard(findEntityCard{Service: "checkout-service", MetaOK: true, OwnerTeam: "SY-XYZ", SRETeam: "UG", Row: row, RangeS: 1800, ProbOK: true, Problems: 2})
	for _, want := range []string{"**checkout-service**", "SY-XYZ", "SRE: UG", "2.0 span/s", "hata %1.00 (36)", "p99 420 ms", "Açık problem: 2"} {
		if !strings.Contains(txt, want) {
			t.Errorf("kart %q içermiyor:\n%s", want, txt)
		}
	}
	if txt := renderFindEntityCard(findEntityCard{Service: "x", RangeS: 60}); !strings.Contains(txt, "RED okunamadı") || !strings.Contains(txt, "Sahip takım: okunamadı") {
		t.Errorf("okunamayan veri dürüst söylenmeli:\n%s", txt)
	}
	if txt := renderFindEntityCard(findEntityCard{Service: "x", MetaOK: true, Row: &chstore.ServiceSummary{}, RangeS: 60}); !strings.Contains(txt, "span verisi yok") || !strings.Contains(txt, "atanmamış") {
		t.Errorf("boş pencere + atanmamış takım:\n%s", txt)
	}
	list := renderFindEntityList(120, []chstore.ServiceSummary{{Name: "inventory", SpanCount: 600, P99Ms: 12}}, 60)
	for _, want := range []string{"**120 servis**", "| Servis | span/s |", "| inventory | 10.0 |"} {
		if !strings.Contains(list, want) {
			t.Errorf("liste %q içermiyor:\n%s", want, list)
		}
	}
	if !strings.Contains(renderFindEntityList(3, nil, 60), "span verisi olan servis yok") {
		t.Error("boş liste dürüst olmalı")
	}
	if ask := renderFindEntityAsk("mobile bff", []string{"a", "b"}); !strings.Contains(ask, "2 aday") || !strings.Contains(ask, `"mobile bff"`) {
		t.Errorf("aday metni: %s", ask)
	}
}
