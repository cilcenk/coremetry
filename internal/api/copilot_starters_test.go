package api

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// copilot_starters_test.go — v0.10.702. İki sözleşme: (1) saf çip üretici
// hatasız/uygunsuz girdide çip üretmez; (2) üretilen HER cümle (veri
// çipleri + FE'deki statik çipler) guided router'da LLM'siz beklenen
// rotaya düşer — çip bir menü değil, çalışan bir soru.
func TestStarterChips(t *testing.T) {
	worst := &chstore.ServiceSummary{Name: "checkout-service", ErrorCount: 12, ErrorRate: 3.1}
	route := &chstore.EndpointRow{Service: "checkout-service", Path: "/api/checkout", Errors: 7}
	if got := starterChips(nil, nil); len(got) != 0 {
		t.Fatalf("girdi yok → çip yok: %+v", got)
	}
	if got := starterChips(&chstore.ServiceSummary{Name: "x", ErrorCount: 0}, route); len(got) != 0 {
		t.Fatalf("hatasız servis → çip yok: %+v", got)
	}
	got := starterChips(worst, route)
	if len(got) != 2 || got[0].Kind != "service_health" || got[1].Kind != "endpoint_errors" {
		t.Fatalf("iki çip beklenir: %+v", got)
	}
	if got[1].Question != "/api/checkout hatalı trace'lerini getir" {
		t.Fatalf("endpoint cümlesi: %q", got[1].Question)
	}
	for _, bad := range []string{"pkg.Svc/Method", "orders process", "/", ""} {
		if g := starterChips(worst, &chstore.EndpointRow{Path: bad, Errors: 3}); len(g) != 1 {
			t.Errorf("uygunsuz yol %q endpoint çipi üretmemeli: %+v", bad, g)
		}
	}
	if g := starterChips(worst, &chstore.EndpointRow{Path: "/api/ok", Errors: 0}); len(g) != 1 {
		t.Errorf("hatasız yol endpoint çipi üretmemeli: %+v", g)
	}
	rows := []chstore.ServiceSummary{{Name: "a", ErrorCount: 0}, {Name: "b", ErrorCount: 2}, {Name: "c", ErrorCount: 9}}
	if w := worstTeamService(rows); w == nil || w.Name != "b" {
		t.Fatalf("sıralı listede hata taşıyan İLK servis: %+v", w)
	}
	if worstTeamService([]chstore.ServiceSummary{{Name: "a"}}) != nil {
		t.Fatal("hatasız liste → nil")
	}
	eps := []chstore.EndpointRow{{Path: "orders process", Errors: 9}, {Path: "/api/x", Errors: 0}, {Path: "/api/y", Errors: 4}}
	if e := worstEndpoint(eps); e == nil || e.Path != "/api/y" {
		t.Fatalf("uygun ve hatalı ilk yol: %+v", e)
	}
}

// Üretilen cümleler router'da beklenen rotaya düşer. FE'deki statik çipler
// de burada pinli (copilotStarters.test.ts listeyi, bu test yönlenmeyi).
func TestStarterQuestionsRoute(t *testing.T) {
	worst := &chstore.ServiceSummary{Name: "checkout-service", ErrorCount: 12}
	route := &chstore.EndpointRow{Path: "/api/checkout", Errors: 7}
	chips := starterChips(worst, route)
	r0 := routeGuidedIntent(chips[0].Question, feServices, feEnvs, nil, "")
	if r0.Intent != guidedServiceHealth || r0.Service != "checkout-service" {
		t.Errorf("%q → %s/%q; want service_health/checkout-service", chips[0].Question, r0.Intent, r0.Service)
	}
	r1 := routeGuidedIntent(chips[1].Question, feServices, feEnvs, nil, "")
	if r1.Intent != guidedEndpointTraces || r1.SearchText != "/api/checkout" || !r1.TraceErrorsOnly {
		t.Errorf("%q → %s/%q errs=%v; want endpoint_traces//api/checkout/true", chips[1].Question, r1.Intent, r1.SearchText, r1.TraceErrorsOnly)
	}
	static := []struct {
		q      string
		intent guidedIntent
	}{
		{"Takımımın servisleri nasıl?", guidedMyServices},
		{"Takımımın açık problemleri?", guidedMyProblems},
		{"Takımımın exception'ları?", guidedMyExceptions},
		{"En yavaş trace'ler?", guidedSlowTraces},
		{"Son 1 saatteki log hataları?", guidedLogErrors},
	}
	for _, c := range static {
		if got := routeGuidedIntent(c.q, feServices, feEnvs, nil, ""); got.Intent != c.intent {
			t.Errorf("statik çip %q → %s, beklenen %s", c.q, got.Intent, c.intent)
		}
	}
}
