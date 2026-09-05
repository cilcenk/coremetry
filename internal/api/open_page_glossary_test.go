package api

import (
	"os"
	"strings"
	"testing"
)

// open_page_glossary_test.go — v0.10.434 (CoSRE router boşlukları D7).

func TestGlossaryLookup(t *testing.T) {
	cases := map[string]string{
		"requestid nedir":             "requestid",
		"request id nedir?":           "requestid",
		"Request-ID ne demek":         "requestid",
		"span nedir":                  "span",
		"trace'in anlamı nedir":       "", // ek + "anlamı": şekil dışı, RAG'a
		"trace'i nedir":               "trace",
		"p95 ne demek":                "p95",
		"p99 nasıl hesaplanır?":       "p99",
		"what is apdex":               "apdex",
		"what does error budget mean": "hatabutcesi",
		"peki slo nedir":              "slo",
		"baz çizgisi ne anlama gelir": "bazcizgisi",
		"checkout nedir":              "", // katalog sorusu değil sözlük: bilinmeyen terim → alt katman
		"checkout servisi nasıl":      "",
		"bugün hava nasıl":            "",
	}
	for msg, want := range cases {
		term, entry, ok := glossaryLookup(normalizeGuidedMsg(msg))
		if (want == "") != !ok || term != want {
			t.Errorf("%q → term=%q ok=%v, want %q", msg, term, ok, want)
			continue
		}
		if ok && (entry.Text == "" || len(entry.Text) > 600) {
			t.Errorf("%q tanımı boş ya da çok uzun (%d)", msg, len(entry.Text))
		}
	}
	ans := glossaryAnswer(glossaryTerms["p95"])
	if _, has := ans["exchangeId"]; has || ans["text"] == "" || ans["links"] == nil {
		t.Fatalf("sözlük cevabı: exchangeId olmamalı, metin + link olmalı: %v", ans)
	}
	for _, sg := range ans["suggestions"].([]string) {
		if routeGuidedIntent(sg, []string{"checkout"}, nil, nil, "").Intent == guidedNone {
			t.Errorf("öneri yönlenmeli: %q", sg)
		}
	}
	// Her takma ad kanonik bir terime iner.
	for a, k := range glossaryAliases {
		if _, ok := glossaryTerms[k]; !ok {
			t.Errorf("takma ad %q kanonik terim %q yok", a, k)
		}
	}
}

// Sözlük linkleri gerçek rotalara gider (App.tsx path="…").
func TestGlossaryLinksAreLiveRoutes(t *testing.T) {
	app, err := os.ReadFile("../../frontend/src/App.tsx")
	if err != nil {
		t.Skip("frontend kaynağı yok")
	}
	src := string(app)
	for k, e := range glossaryTerms {
		for _, l := range e.Links {
			path := l.Href
			if i := strings.IndexByte(path, '?'); i >= 0 {
				path = path[:i]
			}
			if !strings.Contains(src, `path="`+path+`"`) {
				t.Errorf("%s: %s rotası App.tsx'te yok", k, path)
			}
		}
	}
	for _, p := range []string{"/service", "/problems", "/logs", "/traces", "/endpoints"} {
		if !strings.Contains(src, `path="`+p+`"`) {
			t.Errorf("open_page rotası %s App.tsx'te yok", p)
		}
	}
}

func TestRouteOpenPage(t *testing.T) {
	services := []string{"checkout-service", "payment-service"}
	r := routeGuidedIntent("checkout-service sayfasını aç", services, nil, nil, "")
	if r.Intent != guidedOpenPage || r.Service != "checkout-service" || r.Page != "overview" {
		t.Fatalf("servis overview: %+v", r)
	}
	r = routeGuidedIntent("payment-service loglar sayfasını göster", services, nil, nil, "")
	if r.Intent != guidedOpenPage || r.Service != "payment-service" || r.Page != "logs" {
		t.Fatalf("log sayfası: %+v", r)
	}
	r = routeGuidedIntent("problemler sayfasına git", services, nil, nil, "")
	if r.Intent != guidedOpenPage || r.Service != "" || r.Page != "problems" {
		t.Fatalf("filo geneli problemler: %+v", r)
	}
	r = routeGuidedIntent("open the checkout-service page", services, nil, nil, "")
	if r.Intent != guidedOpenPage || r.Service != "checkout-service" {
		t.Fatalf("EN: %+v", r)
	}
	// Sayfa bağlamı özneyi verir.
	r = routeGuidedIntent("sayfasını aç", services, nil, nil, "payment-service")
	if r.Intent != guidedOpenPage || r.Service != "payment-service" {
		t.Fatalf("bağlam servisi: %+v", r)
	}
	// Öznesiz overview router'dan open_page/boş çıkar; runGuidedRoute sorar.
	r = routeGuidedIntent("sayfasını aç", services, nil, nil, "")
	if r.Intent != guidedOpenPage || r.Service != "" {
		t.Fatalf("öznesiz: %+v", r)
	}
	if got := newestPriorService([]string{"checkout-service nasıl?", "peki hata logları?"}, services, nil); got != "checkout-service" {
		t.Fatalf("önceki tur servisi: %q", got)
	}
	// "açık problemler neler" sayfa isteği DEĞİL.
	if r := routeGuidedIntent("açık problemler neler", services, nil, nil, ""); r.Intent == guidedOpenPage {
		t.Fatalf("sayfa sözcüğü yok → open_page olmamalı: %+v", r)
	}
	// Çip gidiş-dönüşü + linkler.
	chip := askServiceChip(guidedOpenPage, "checkout-service")
	if r := routeGuidedIntent(chip, services, nil, nil, ""); r.Intent != guidedOpenPage || r.Service != "checkout-service" {
		t.Fatalf("çip %q → %+v", chip, r)
	}
	links := guidedAnswerLinkTargets(guidedRoute{Intent: guidedOpenPage, Service: "checkout-service", Page: "overview"})
	if len(links) != 1 || links[0].Href != "/service?name=checkout-service" {
		t.Fatalf("overview linki: %+v", links)
	}
	if links := guidedAnswerLinkTargets(guidedRoute{Intent: guidedOpenPage, Page: "problems"}); len(links) != 1 || links[0].Href != "/problems" {
		t.Fatalf("problems linki: %+v", links)
	}
	if links := guidedAnswerLinkTargets(guidedRoute{Intent: guidedOpenPage, Page: "overview"}); links != nil {
		t.Fatalf("öznesiz overview link üretmez: %+v", links)
	}
	for _, sg := range guidedSuggestions(guidedRoute{Intent: guidedOpenPage, Service: "checkout-service"}) {
		if routeGuidedIntent(sg, services, nil, nil, "").Intent == guidedNone {
			t.Errorf("öneri yönlenmeli: %q", sg)
		}
	}
	if openPageAnswerTR(guidedRoute{Service: "checkout-service", Page: "logs"}) != "checkout-service · Loglar sayfası açılıyor." {
		t.Fatal("cevap metni")
	}
}

// D7a — öznesiz kök-neden sorusu sorar; hata/problem şekilli olan filo
// geneli problems'ta kalır; sayfa bağlamı varsa kök-nedene gider.
func TestMissingSubjectAsks(t *testing.T) {
	services := []string{"checkout-service", "payment-service"}
	for _, msg := range []string{"yavaşlığın sebebi ne?", "neden yavaş", "niye yavaşladı"} {
		r := routeGuidedIntent(msg, services, nil, nil, "")
		if r.Intent != guidedAskService || r.AskIntent != guidedRootCause {
			t.Errorf("%q → %+v, want ask_service/root_cause", msg, r)
		}
	}
	if r := routeGuidedIntent("neden bu kadar çok hata alıyoruz", services, nil, nil, ""); r.Intent == guidedAskService {
		t.Fatalf("hata şekilli öznesiz soru filo geneli kalmalı: %+v", r)
	}
	if r := routeGuidedIntent("neden yavaş", services, nil, nil, "checkout-service"); r.Intent != guidedRootCause || r.Service != "checkout-service" {
		t.Fatalf("sayfa bağlamı kök-nedene götürmeli: %+v", r)
	}
}
