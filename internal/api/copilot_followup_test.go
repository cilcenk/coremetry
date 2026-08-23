package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/copilot"
)

// copilot_followup_test.go — v0.9.410 konuşma bağlamı devralma pinleri.
// Şekil A (yönlenemeyen takip sorusu önceki rotayı devralır), Şekil B
// (yönlenmiş ama konusuz soru önceki servisi doldurur), filo-kaçışı,
// range devralma kuralı ve isFollowUpCue sınırları.

var fuServices = []string{"checkout-service", "payments", "mobile-bff"}
var fuEnvs = []string{"prod", "uat"}

func TestApplyFollowUpContext(t *testing.T) {
	cases := []struct {
		name     string
		question string
		prior    []string
		wantInt  guidedIntent
		wantSvc  string
		wantRng  int64
		wantChg  bool
	}{
		// Şekil A: intent'siz takip, mevcut soru servis adlandırıyor →
		// önceki intent + yeni servis; range önceki sorunun AÇIK penceresi.
		{"A servis değişimi", "peki payments?",
			[]string{"checkout-service son 6 saatte nasıl?"},
			guidedServiceHealth, "payments", 21600, true},
		// Şekil A: salt-range takip → önceki intent+servis, yeni pencere.
		{"A range değişimi", "peki son 24 saatte?",
			[]string{"checkout-service nasıl?"},
			guidedServiceHealth, "checkout-service", 86400, true},
		// Şekil A: katkısız takip ("peki bu ne demek?") devralMAZ —
		// RAG/serbest döngüye kalmalı.
		{"A katkısız", "peki bu ne demek?",
			[]string{"checkout-service nasıl?"},
			guidedNone, "", 0, false},
		// Şekil A: önceki mesajların hiçbiri yönlenemiyorsa devralma yok.
		{"A yönlenemeyen geçmiş", "peki payments?",
			[]string{"merhaba", "bugün hava nasıl"},
			guidedNone, "", 0, false},
		// Uzun mesaj takip değildir.
		{"uzun mesaj", "peki payments servisinin son günlerdeki genel durumunu, hatalarını ve yavaşlamalarını bütün detaylarıyla anlatır mısın",
			[]string{"checkout-service nasıl?"},
			guidedNone, "", 0, false},
	}
	for _, c := range cases {
		route := routeGuidedIntent(c.question, fuServices, fuEnvs, nil, "")
		got, rng, _, chg := applyFollowUpContext(route, c.question, c.prior, fuServices, fuEnvs, nil)
		if chg != c.wantChg {
			t.Errorf("%s: changed=%v, want %v", c.name, chg, c.wantChg)
			continue
		}
		if !chg {
			continue
		}
		if got.Intent != c.wantInt || got.Service != c.wantSvc || rng != c.wantRng {
			t.Errorf("%s: intent=%q svc=%q rng=%d, want %q/%q/%d",
				c.name, got.Intent, got.Service, rng, c.wantInt, c.wantSvc, c.wantRng)
		}
	}
}

// Şekil B: intent VAR ama konu boş — önceki turun servisi dolar;
// açık filo-kapsam kelimesi doldurmayı iptal eder.
func TestApplyFollowUpContextFill(t *testing.T) {
	prior := []string{"payments son 2 saatte nasıl?"}

	route := routeGuidedIntent("peki hata logları?", fuServices, fuEnvs, nil, "")
	if route.Intent != guidedLogErrors || route.Service != "" {
		t.Fatalf("ön-koşul bozuk: %+v", route)
	}
	got, rng, base, chg := applyFollowUpContext(route, "peki hata logları?", prior, fuServices, fuEnvs, nil)
	if !chg || got.Service != "payments" || got.Intent != guidedLogErrors {
		t.Errorf("fill: %+v changed=%v, want payments/log_errors", got, chg)
	}
	// range: mevcut soru açık pencere taşımıyor → önceki sorunun 2 saati.
	if rng != 7200 {
		t.Errorf("fill range=%d, want 7200 (önceki sorudan)", rng)
	}
	if base == "" {
		t.Errorf("fill base boş — operasyon çözümü için temel mesaj dönmeli")
	}

	// Filo kaçışı: "tüm" doldurmayı iptal eder, rota olduğu gibi kalır.
	fleet := routeGuidedIntent("peki tüm serviste problemler?", fuServices, fuEnvs, nil, "")
	_, _, _, chg2 := applyFollowUpContext(fleet, "peki tüm serviste problemler?", prior, fuServices, fuEnvs, nil)
	if chg2 {
		t.Errorf("filo kaçışı: 'tüm' içeren soru önceki servisle doldurulmamalı")
	}
}

func TestIsFollowUpCue(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"peki payments?", true},
		{"son 24 saatte?", true}, // açık range tek başına cue
		{"aynı soruyu uat için", true},
		{"bunun sebebi ne", true},
		{"checkout-service nasıl?", false},  // bağımsız soru, cue yok
		{"bu sistem nasıl çalışıyor", false}, // tek başına "bu" cue DEĞİL
	}
	for _, c := range cases {
		if got := isFollowUpCue(normalizeGuidedMsg(c.msg)); got != c.want {
			t.Errorf("isFollowUpCue(%q)=%v, want %v", c.msg, got, c.want)
		}
	}
}

func TestPriorUserTexts(t *testing.T) {
	msgs := []copilot.ChatMessage{
		{Role: "user", Text: "ilk soru"},
		{Role: "assistant", Text: "cevap"},
		{Role: "user", Text: ""}, // tool-result turu — atlanır
		{Role: "user", Text: "ikinci soru"},
		{Role: "assistant", Text: "cevap 2"},
		{Role: "user", Text: "aktif soru"},
	}
	got := priorUserTexts(msgs)
	if len(got) != 2 || got[0] != "ikinci soru" || got[1] != "ilk soru" {
		t.Errorf("priorUserTexts=%v, want [ikinci soru, ilk soru]", got)
	}
}

// v0.9.411 pini: HER takip önerisi guided router'da kendi başına
// yönlenmeli — çipe tıklamak asla serbest tool döngüsüne düşürmez.
// Router kelime kökleri değişirse bu test öneri metnini de değişmeye
// zorlar.
func TestGuidedSuggestionsRoute(t *testing.T) {
	routes := []guidedRoute{
		{Intent: guidedServiceHealth, Service: "payments"},
		{Intent: guidedProblems, Service: "payments"},
		{Intent: guidedProblems},
		{Intent: guidedSlowTraces, Service: "payments"},
		{Intent: guidedSlowTraces},
		{Intent: guidedDeployImpact, Service: "payments"},
		{Intent: guidedDeployImpact},
		{Intent: guidedLogErrors, Service: "payments"},
		{Intent: guidedLogErrors},
		{Intent: guidedFamilyHealth},
		{Intent: guidedMyServices},
		{Intent: guidedMyProblems},
		{Intent: guidedPodHealth, Service: "payments"},
		{Intent: guidedPodHealth},
		{Intent: guidedShiftSummary},
		{Intent: guidedDBHealth},
		{Intent: guidedMessagingHealth},
	}
	for _, r := range routes {
		sugg := guidedSuggestions(r)
		if len(sugg) == 0 {
			t.Errorf("rota %s/%s: öneri boş", r.Intent, r.Service)
		}
		for _, q := range sugg {
			if got := routeGuidedIntent(q, fuServices, fuEnvs, nil, ""); got.Intent == guidedNone {
				t.Errorf("öneri %q yönlenemiyor (rota %s) — serbest döngüye düşer", q, r.Intent)
			}
		}
	}
}

// v0.9.419 pini: derin linkler daima uygulama-köklü (/ ile başlar) ve
// servisli rotalarda servis query-escape'li taşınır.
func TestGuidedAnswerLinks(t *testing.T) {
	routes := []guidedRoute{
		{Intent: guidedServiceHealth, Service: "päy ments"},
		{Intent: guidedProblems, Service: "payments"},
		{Intent: guidedProblems},
		{Intent: guidedSlowTraces},
		{Intent: guidedLogErrors, Service: "payments"},
		{Intent: guidedMyServices},
		{Intent: guidedMyProblems},
		{Intent: guidedPodHealth, Service: "payments"},
		{Intent: guidedShiftSummary, Service: "payments"},
		{Intent: guidedFamilyHealth},
		{Intent: guidedDBHealth},
		{Intent: guidedMessagingHealth},
	}
	for _, r := range routes {
		links := guidedAnswerLinks(r, noLinkWindow())
		if len(links) == 0 {
			t.Errorf("rota %s/%s: link boş", r.Intent, r.Service)
		}
		for _, l := range links {
			if !strings.HasPrefix(l.Href, "/") {
				t.Errorf("%s: href uygulama-köklü değil: %s", r.Intent, l.Href)
			}
			if l.Label == "" {
				t.Errorf("%s: boş label", r.Intent)
			}
		}
	}
	// Escape pini: boşluklu/aksanlı servis adı href'te ham geçmez.
	sl := guidedAnswerLinks(guidedRoute{Intent: guidedServiceHealth, Service: "päy ments"}, noLinkWindow())
	if !strings.Contains(sl[0].Href, "p%C3%A4y+ments") {
		t.Errorf("servis adı escape edilmeli: %s", sl[0].Href)
	}
}

// guidedRangeSExplicit — refactor pini: açık pencere bayrağı doğru,
// guidedRangeS davranışı birebir korunmuş (varsayılan 1800).
func TestGuidedRangeSExplicit(t *testing.T) {
	cases := []struct {
		msg      string
		want     int64
		explicit bool
	}{
		{"son 2 saatte hatalar", 7200, true},
		{"son 45 dakika", 2700, true},
		{"bugün neler oldu", 86400, true},
		{"checkout nasıl?", 1800, false},
	}
	for _, c := range cases {
		got, exp := guidedRangeSExplicit(normalizeGuidedMsg(c.msg))
		if got != c.want || exp != c.explicit {
			t.Errorf("guidedRangeSExplicit(%q)=(%d,%v), want (%d,%v)",
				c.msg, got, exp, c.want, c.explicit)
		}
	}
}
