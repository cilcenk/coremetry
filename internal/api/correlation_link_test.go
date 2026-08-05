package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.655 — operatör: "Request Id bulduğunda prod'ta log izleme
// linkini de versin parametrik olarak."
//
// v0.9.580 örnek request_id'leri zaten buluyordu; operatör onları
// KOPYALAYIP kurumun log arayüzüne elle yapıştırıyordu.

const tpl = "https://logs.example.com/masterlog?requestId={value}"

var tpls = correlationTemplates{"default": tpl}

func TestValidCorrelationTemplate(t *testing.T) {
	ok := []string{
		tpl,
		"http://h/x?id={value}",
		"https://h/{value}",
		"  https://h/?q={value}  ", // baştaki/sondaki boşluk
	}
	for _, s := range ok {
		if !validCorrelationTemplate(s) {
			t.Errorf("geçerli sayılmalıydı: %q", s)
		}
	}

	bad := map[string]string{
		"boş":            "",
		"yer tutucu yok": "https://logs.example.com/masterlog?requestId=",
		"şema yok":       "logs.example.com/?id={value}",
		"host yok":       "https:///?id={value}",
		// GÜVENLİK: javascript: şablonu cevabı tıklanabilir bir betiğe
		// çevirirdi.
		"javascript":     "javascript:alert(1)/*{value}*/",
		"data":           "data:text/html,{value}",
		"file":           "file:///tmp/{value}",
	}
	for name, s := range bad {
		if validCorrelationTemplate(s) {
			t.Errorf("%s: geçersiz sayılmalıydı: %q", name, s)
		}
	}
}

func TestBuildCorrelationLinkEncodesValue(t *testing.T) {
	// Kurum request id'lerinde & # boşluk bulunabiliyor; ham yapıştırma
	// linki bozar ya da SESSİZCE başka bir sorgu üretir.
	got := buildCorrelationLink(tpl, "SPE 250&x#y")
	if strings.Contains(got, " ") || strings.Contains(got, "#") {
		t.Fatalf("değer encode edilmemiş: %s", got)
	}
	if !strings.HasPrefix(got, "https://logs.example.com/masterlog?requestId=") {
		t.Fatalf("şablon bozulmuş: %s", got)
	}
}

func TestBuildCorrelationLinkRefusesBadInput(t *testing.T) {
	if buildCorrelationLink(tpl, "") != "" {
		t.Error("boş değerde link üretilmemeli")
	}
	if buildCorrelationLink("javascript:x{value}", "abc") != "" {
		t.Error("geçersiz şablonda link üretilmemeli")
	}
	if buildCorrelationLink("", "abc") != "" {
		t.Error("boş şablonda link üretilmemeli")
	}
}

func TestCorrelationLinksCapsPerKey(t *testing.T) {
	samples := []chstore.CorrelationSample{
		{Key: "request_id", Values: []string{"a1", "b2", "c3"}},
		{Key: "correlation_id", Values: []string{"d4", "e5", "f6"}},
	}
	got := correlationLinks(samples, "checkout", tpls)
	// Anahtar başına EN FAZLA iki: üç anahtar × üç değer bir çip yığını
	// olurdu.
	if len(got) != 4 {
		t.Fatalf("anahtar başına 2 link beklenir (toplam 4), alınan %d: %+v", len(got), got)
	}
	for _, l := range got {
		if !strings.HasPrefix(l.Href, "https://") {
			t.Errorf("href şemasız: %+v", l)
		}
		if l.Label == "" {
			t.Errorf("etiket boş: %+v", l)
		}
	}
}

// Şablon YAPILANDIRILMAMIŞSA hiçbir şey çizilmemeli — kırık bir link,
// link yokluğundan kötüdür.
func TestCorrelationLinksSilentWhenUnconfigured(t *testing.T) {
	samples := []chstore.CorrelationSample{{Key: "request_id", Values: []string{"a1"}}}
	if got := correlationLinks(samples, "checkout", nil); got != nil {
		t.Errorf("şablonsuz link üretilmemeli: %+v", got)
	}
}

func TestShortCorrValue(t *testing.T) {
	if shortCorrValue("kisa") != "kisa" {
		t.Error("kısa değer kırpılmamalı")
	}
	long := "SPE02500302010lfv00306137532026080508"
	got := shortCorrValue(long)
	if len([]rune(got)) > 15 {
		t.Errorf("çip etiketi çok uzun: %q", got)
	}
}

// v0.9.655 (operatör: test ortamlarının log adresleri ayrı; servis
// adının sonunda -int/-uat/-prep görünce o adreslere yönlendir) —
// ORTAM çözümlemesi.

func TestEnvFromServiceName(t *testing.T) {
	cases := map[string]string{
		"bsa-mobile-login-int":  "int",
		"bsa-mobile-login-uat":  "uat",
		"bsa-mobile-login-prep": "prep",
		"bsa-mobile-login-prod": "",
		"bsa-mobile-login":      "",
		"BSA-MOBILE-LOGIN-INT":  "int", // büyük harf
		// SONEK araması, alt dize DEĞİL: "integration-service" içinde
		// "int" geçiyor ama sonu "-int" değil. Alt dize araması burayı
		// sessizce test ortamına yönlendirirdi.
		"integration-service":   "",
		"int-gateway":           "",
		"":                      "",
	}
	for svc, want := range cases {
		if got := envFromServiceName(svc); got != want {
			t.Errorf("%q → %q, beklenen %q", svc, got, want)
		}
	}
}

func TestTemplateForServicePicksEnv(t *testing.T) {
	m := correlationTemplates{
		"default": "https://prod/?id={value}",
		"int":     "https://int/?id={value}",
		"uat":     "https://uat/?id={value}",
	}
	if got := templateForService("svc-int", m); got != m["int"] {
		t.Errorf("-int ortam şablonunu almalı, alınan %q", got)
	}
	if got := templateForService("svc-uat", m); got != m["uat"] {
		t.Errorf("-uat ortam şablonunu almalı, alınan %q", got)
	}
	// prep TANIMSIZ → default'a düşer (operatör yalnız bazı ortamları
	// doldurmuş olabilir).
	if got := templateForService("svc-prep", m); got != m["default"] {
		t.Errorf("tanımsız ortam default'a düşmeli, alınan %q", got)
	}
	// Soneksiz = prod = default.
	if got := templateForService("svc-prod", m); got != m["default"] {
		t.Errorf("soneksiz ad default almalı, alınan %q", got)
	}
}

// Link ETİKETİ ortamı söylemeli: operatör test ve prod sekmelerini yan
// yana açıyor, hangi linkin nereye gittiği çipten okunmalı.
func TestCorrelationLinkLabelCarriesEnv(t *testing.T) {
	m := correlationTemplates{"default": "https://prod/?id={value}", "int": "https://int/?id={value}"}
	samples := []chstore.CorrelationSample{{Key: "request_id", Values: []string{"abc123"}}}

	intLinks := correlationLinks(samples, "svc-int", m)
	if len(intLinks) != 1 || !strings.Contains(intLinks[0].Label, "int") {
		t.Fatalf("int etiketinde ortam yok: %+v", intLinks)
	}
	if !strings.HasPrefix(intLinks[0].Href, "https://int/") {
		t.Errorf("int servisi prod adresine gitmiş: %s", intLinks[0].Href)
	}

	prodLinks := correlationLinks(samples, "svc-prod", m)
	if len(prodLinks) != 1 || !strings.HasPrefix(prodLinks[0].Href, "https://prod/") {
		t.Fatalf("prod servisi yanlış adrese gitmiş: %+v", prodLinks)
	}
	// Prod etiketinde ortam eki OLMAMALI — gürültü.
	if strings.Contains(prodLinks[0].Label, "(") {
		t.Errorf("prod etiketinde gereksiz ortam eki: %q", prodLinks[0].Label)
	}
}
