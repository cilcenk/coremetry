package api

// endpoint_traces_test.go — v0.10.688 (operatör: "paymentApprove isteklerini
// getir" → yolunda /paymentapprove geçen endpoint'leri bul, SOR, evet'te
// listele ve trace'lerini getir). Adlar SENTETİK.
//
// SÖZLEŞME:
//   1. Yönlendirme: istek/endpoint/çağrı/request ipucu + tanımlayıcı token →
//      endpoint_candidates (SearchText küçük harf token); ham metinde '/yol' +
//      trace kökü → endpoint_traces (SearchText yol, harfi korunur). Soru
//      sözcüğü, canlı servis adı, aile parçası, çift şekli → bu kademe DEĞİL.
//   2. isAffirmative: evet/tamam/hepsi/olur/ok/getir ile başlayan kısa mesaj;
//      yabancı sözcük ("ama") bozar.
//   3. Onay: LastRoute endpoint_candidates + evet → endpoint_traces, aynı sorgu.
//   4. Çipler deterministik yönlenir (tur-arası durum yok): "Evet, hepsinin
//      trace'lerini getir" affirmative; "<yol> trace'lerini getir" endpoint_traces.
//   5. Trace süzgeci: Search=sorgu, time desc, Limit 20, CountMode skip.
//   6. trace_list yükü: satırlar + pencere + deepLink + truncated (limit dolu).

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestRouteEndpointRequest(t *testing.T) {
	cases := []struct {
		q      string
		intent guidedIntent
		query  string
		errs   bool
	}{
		{"paymentApprove isteklerini getir", guidedEndpointCandidates, "paymentapprove", false},
		{"paymentApprove istekleri", guidedEndpointCandidates, "paymentapprove", false},
		{"paymentapprove endpointlerini bul", guidedEndpointCandidates, "paymentapprove", false},
		{"paymentApprove hatalı istekleri", guidedEndpointCandidates, "paymentapprove", true},
		{"/api/paymentApprove isteklerini getir", guidedEndpointCandidates, "/api/paymentApprove", false},
		{"/api/paymentApprove trace'lerini getir", guidedEndpointTraces, "/api/paymentApprove", false},
	}
	for _, c := range cases {
		r := routeGuidedIntent(c.q, feServices, feEnvs, nil, "")
		if r.Intent != c.intent || r.SearchText != c.query || r.TraceErrorsOnly != c.errs {
			t.Errorf("%q → intent=%s q=%q errs=%v; want %s/%q/%v", c.q, r.Intent, r.SearchText, r.TraceErrorsOnly, c.intent, c.query, c.errs)
		}
	}
	for _, q := range []string{
		"istek sayısı arttı mı",
		"son 1 saatte kaç istek geldi",
		"checkout-service istekleri",
		"mobile bff hatalı istekleri",
		"api-gateway'dan checkout-service'e giden istekler",
	} {
		if r := routeGuidedIntent(q, feServices, feEnvs, nil, ""); r.Intent == guidedEndpointCandidates || r.Intent == guidedEndpointTraces {
			t.Errorf("%q endpoint rotasına DÜŞMEMELİ: %+v", q, r)
		}
	}
	if !hasGuidedSignal("paymentapprove isteklerini getir") {
		t.Error("istek ipucu sıfır-maliyet kapısından geçmeli")
	}
}

func TestIsAffirmative(t *testing.T) {
	for _, q := range []string{"evet", "Evet.", "tamam", "hepsi", "olur", "ok", "getir", "evet hepsini getir", "Evet, hepsinin trace'lerini getir"} {
		if !isAffirmative(normalizeGuidedMsg(q)) {
			t.Errorf("%q evet sayılmalı", q)
		}
	}
	for _, q := range []string{"evet ama hataları", "hayır", "checkout sağlığı nasıl", "neden yavaş", ""} {
		if isAffirmative(normalizeGuidedMsg(q)) {
			t.Errorf("%q evet SAYILMAMALI", q)
		}
	}
}

func TestEndpointAffirmativeRoute(t *testing.T) {
	last := guidedRoute{Intent: guidedEndpointCandidates, SearchText: "paymentapprove", Env: "prod"}
	r, ok := endpointAffirmativeRoute(ChatContext{LastRoute: &last})
	if !ok || r.Intent != guidedEndpointTraces || r.SearchText != "paymentapprove" || r.Env != "prod" {
		t.Fatalf("evet → endpoint_traces: %+v %v", r, ok)
	}
	if _, ok := endpointAffirmativeRoute(ChatContext{LastRoute: &guidedRoute{Intent: guidedSlowTraces}}); ok {
		t.Fatal("başka rotada evet anlamsız")
	}
	if _, ok := endpointAffirmativeRoute(ChatContext{}); ok {
		t.Fatal("bağlam yokken evet anlamsız")
	}
	// "sadece hatalı olanlar" onay sayılır (kip → endpoint_traces + hatalı).
	m := contextMutation{Kind: "errors"}
	mr, _, _, mok := applyContextMutation(ChatContext{LastRoute: &last, RangeS: 3600}, m)
	if !mok || mr.Intent != guidedEndpointTraces || !mr.TraceErrorsOnly {
		t.Fatalf("hatalı kipi: %+v %v", mr, mok)
	}
}

func TestEndpointChipsAndLinks(t *testing.T) {
	r := guidedRoute{Intent: guidedEndpointCandidates, SearchText: "paymentapprove", EndpointOptions: []endpointCandidate{
		{Service: "shop-payment", Method: "POST", Path: "/api/paymentApprove", Calls: 1200, P99Ms: 340},
		{Service: "shop-legacy", Method: "POST", Path: "/v1/paymentapprove", Calls: 12, P99Ms: 90},
	}}
	chips := guidedSuggestions(r)
	if len(chips) != 3 || !strings.HasPrefix(chips[0], "Evet") || chips[1] != "/api/paymentApprove trace'lerini getir" {
		t.Fatalf("çipler: %v", chips)
	}
	if rr := routeGuidedIntent(chips[1], feServices, feEnvs, nil, ""); rr.Intent != guidedEndpointTraces || rr.SearchText != "/api/paymentApprove" {
		t.Fatalf("aday çipi yönlenmeli: %+v", rr)
	}
	if !isAffirmative(normalizeGuidedMsg(chips[0])) {
		t.Fatalf("evet çipi affirmative olmalı: %q", chips[0])
	}
	tr := guidedRoute{Intent: guidedEndpointTraces, SearchText: "/api/paymentApprove", TraceErrorsOnly: true}
	links := guidedAnswerLinkTargets(tr)
	if len(links) != 1 || !strings.Contains(links[0].Href, "search=%2Fapi%2FpaymentApprove") || !strings.Contains(links[0].Href, "hasError=true") || !strings.Contains(links[0].Href, "sort=time") {
		t.Fatalf("link: %+v", links)
	}
}

func TestEndpointTraceFilterAndPayload(t *testing.T) {
	from, to := time.Now().Add(-time.Hour), time.Now()
	f := endpointTraceFilter("paymentapprove", false, "prod", from, to)
	if f.Search != "paymentapprove" || f.Sort != "time" || f.Order != "desc" || f.Limit != endpointTracesLimit || f.CountMode != "skip" || f.HasError || f.Env != "prod" {
		t.Fatalf("süzgeç: %+v", f)
	}
	rows := make([]chstore.TraceRow, endpointTracesLimit)
	for i := range rows {
		rows[i] = chstore.TraceRow{TraceID: "t", RootName: "POST /api/paymentApprove", ServiceName: "shop-payment", DurationMs: 12, SpanCount: 3, StartTime: 1_700_000_000_000_000_000}
	}
	p := endpointTraceListPayload("paymentapprove", rows, from, to, "/traces?x")
	if p.Query != "paymentapprove" || len(p.Traces) != endpointTracesLimit || !p.Truncated || p.DeepLink != "/traces?x" || p.Window.FromNs != from.UnixNano() || p.Traces[0].SpanCount != 3 {
		t.Fatalf("yük: %+v", p)
	}
	if p2 := endpointTraceListPayload("x", rows[:3], from, to, "/traces"); p2.Truncated {
		t.Fatal("3 satır kesik değil")
	}
}

func TestRenderEndpointTR(t *testing.T) {
	c := []endpointCandidate{{Service: "shop-payment", Method: "POST", Path: "/api/paymentApprove", Calls: 1200, P99Ms: 340}}
	txt := renderEndpointCandidatesTR("paymentapprove", c, 3600)
	for _, want := range []string{"1 endpoint", "shop-payment", "POST /api/paymentApprove", "1.200", "340", "getireyim mi"} {
		if !strings.Contains(txt, want) {
			t.Errorf("%q yok:\n%s", want, txt)
		}
	}
	if e := renderEndpointCandidatesTR("zzz", nil, 3600); !strings.Contains(e, "endpoint yok") {
		t.Errorf("boş: %s", e)
	}
	rows := []chstore.TraceRow{{TraceID: "a1", RootName: "POST /api/paymentApprove", ServiceName: "shop-payment", DurationMs: 812, SpanCount: 14, HasError: true, StartTime: 1_700_000_000_000_000_000}}
	tt := renderEndpointTracesTR("paymentapprove", c, rows, 3600, true)
	for _, want := range []string{"1 hatalı trace", "en yeni önce", "shop-payment", "trace=a1"} {
		if !strings.Contains(tt, want) {
			t.Errorf("%q yok:\n%s", want, tt)
		}
	}
	if e := renderEndpointTracesTR("zzz", nil, nil, 3600, false); !strings.Contains(e, "bulunamadı") {
		t.Errorf("boş trace: %s", e)
	}
}
