package api

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// family_traces_test.go — v0.10.465 (CoSRE sohbet paritesi D2): "hatalı /
// yavaş trace'ler" + aile → trace LİSTESİ (aile sağlığı değil). Adlar SENTETİK.

func TestRouteFamilyTraces(t *testing.T) {
	cases := []struct {
		q      string
		intent guidedIntent
		fam    int
		errs   bool
	}{
		{"mobile bff son hatalı traceler", guidedFamilyTraces, 2, true},
		{"mobile bff'nin son 1 saatteki hatalı trace'lerini getir", guidedFamilyTraces, 2, true},
		{"mobile bff yavaş traceler", guidedFamilyTraces, 2, false},
		{"mobile bff en yavaş trace'ler", guidedFamilyTraces, 2, false},
		{"checkout-service ve payment-service hatalı trace'leri", guidedFamilyTraces, 2, true},
		// Tek servis + hatalı → hatalı liste (eskiden service_health'e çöküyordu).
		{"checkout hatalı traceler", guidedFamilyTraces, 1, true},
		// Tek servis + yalnız yavaş → mevcut rota AYNEN.
		{"checkout-service en yavaş trace'ler?", guidedSlowTraces, 0, false},
		// Trace kökü yok → aile sağlığı AYNEN.
		{"mobile bff hataları", guidedFamilyHealth, 2, false},
		// Kimse yok → bu kademe girmez.
		{"zzqx hatalı traceler", guidedProblems, 0, false},
	}
	for _, c := range cases {
		r := routeGuidedIntent(c.q, feServices, feEnvs, nil, "")
		if r.Intent != c.intent || len(r.Family) != c.fam || r.TraceErrorsOnly != c.errs {
			t.Errorf("%q → intent=%s fam=%v errs=%v; want %s/%d/%v", c.q, r.Intent, r.Family, r.TraceErrorsOnly, c.intent, c.fam, c.errs)
		}
	}
	// Sayfa bağlamı: adsız "hatalı traceler" o servisin hatalı listesi.
	r := routeGuidedIntent("hatalı traceler", feServices, feEnvs, nil, "payment-service")
	if r.Intent != guidedFamilyTraces || r.Service != "payment-service" || !r.TraceErrorsOnly {
		t.Errorf("bağlam: %+v", r)
	}
}

func TestFamilyTraceFilterAndHref(t *testing.T) {
	fam := []string{"mobile-commercial-bff-prod", "mobile-retail-bff-prod"}
	f := familyTraceFilter(fam, true, "prod", time.Now().Add(-time.Hour), time.Now())
	if f.Service != "" || len(f.Filters) != 1 || f.Filters[0].Op != "IN" || len(f.Filters[0].Values) != 2 || !f.HasError || f.Sort != "time" || f.Limit != 10 || f.Env != "prod" {
		t.Fatalf("aile süzgeci: %+v", f)
	}
	if err := chstore.ValidateFilters(f.Filters); err != nil {
		t.Fatalf("çip geçersiz: %v", err)
	}
	f1 := familyTraceFilter([]string{"checkout-service"}, false, "", time.Now().Add(-time.Hour), time.Now())
	if f1.Service != "checkout-service" || len(f1.Filters) != 0 || f1.HasError || f1.Sort != "duration" {
		t.Fatalf("tek servis süzgeci: %+v", f1)
	}
	h := familyTracesHref(fam, true)
	u, err := url.Parse(h)
	if err != nil || u.Path != "/traces" {
		t.Fatalf("href: %s %v", h, err)
	}
	q := u.Query()
	if q.Get("hasError") != "true" || q.Get("sort") != "time" || q.Get("order") != "desc" || !strings.Contains(q.Get("filters"), `"op":"IN"`) || !strings.Contains(q.Get("filters"), "mobile-retail-bff-prod") {
		t.Fatalf("href paramları: %s", h)
	}
	if h1 := familyTracesHref([]string{"checkout-service"}, false); !strings.Contains(h1, "service=checkout-service") || strings.Contains(h1, "hasError") || !strings.Contains(h1, "sort=duration") {
		t.Fatalf("tek servis href: %s", h1)
	}
}

func TestRenderFamilyTraces(t *testing.T) {
	rows := []chstore.TraceRow{{TraceID: "a1", RootName: "POST /pay", ServiceName: "mobile-retail-bff-prod", DurationMs: 812, SpanCount: 14, HasError: true, StartTime: 1_700_000_000_000_000_000}}
	txt := renderFamilyTracesEvidenceTR(rows, []string{"mobile-commercial-bff-prod", "mobile-retail-bff-prod"}, true, "", 3600)
	for _, want := range []string{"Hatalı trace'ler", "en yeniden eskiye", "812ms", "HATA", "trace=a1", "Kapsam 2 servis"} {
		if !strings.Contains(txt, want) {
			t.Errorf("%q yok:\n%s", want, txt)
		}
	}
	if e := renderFamilyTracesEvidenceTR(nil, []string{"x"}, false, "prod", 60); !strings.Contains(e, "En yavaş trace'ler") || !strings.Contains(e, "bulunamadı") || !strings.Contains(e, "ortam: prod") {
		t.Errorf("boş liste:\n%s", e)
	}
}

// Çipler ve linkler kendi rotalarına gider.
func TestFamilyTracesChipsAndLinks(t *testing.T) {
	r := routeGuidedIntent("mobile bff son hatalı traceler", feServices, feEnvs, nil, "")
	chips := guidedSuggestions(r)
	if len(chips) != 2 {
		t.Fatalf("çipler: %v", chips)
	}
	for _, c := range chips {
		if got := routeGuidedIntent(c, feServices, feEnvs, nil, ""); got.Intent != guidedLogErrors || got.Service == "" {
			t.Errorf("çip %q → %+v", c, got)
		}
	}
	links := guidedAnswerLinks(r, noLinkWindow())
	if len(links) != 3 || !strings.HasPrefix(links[0].Href, "/traces?") || !strings.Contains(links[0].Label, "hatalı") {
		t.Fatalf("linkler: %+v", links)
	}
}
