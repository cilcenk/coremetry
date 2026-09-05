package api

import (
	"math"
	"strings"
	"testing"
	"time"
)

// call_period_test.go — v0.10.438 (CoSRE router boşlukları D3).

func TestDetectPeriod(t *testing.T) {
	// 5 dk periyot, 1 dk adım: her 5. dakikada tepe.
	var v []float64
	for i := 0; i < 120; i++ {
		x := 1.0
		if i%5 == 0 {
			x = 10
		}
		v = append(v, x)
	}
	r := detectPeriod(v, 60)
	if !r.OK || r.PeriodS != 300 || r.Cycles < 20 || r.Strength < 0.9 {
		t.Fatalf("5 dk periyot: %+v", r)
	}
	// Sinüs, periyot 12 kova.
	v = v[:0]
	for i := 0; i < 96; i++ {
		v = append(v, 50+40*math.Sin(2*math.Pi*float64(i)/12))
	}
	if r := detectPeriod(v, 300); !r.OK || r.PeriodS != 3600 {
		t.Fatalf("sinüs 12×5 dk = 1 sa: %+v", r)
	}
	// Sabit → yok; kısa → yok; yapısız → yok.
	if r := detectPeriod([]float64{3, 3, 3, 3, 3, 3, 3, 3, 3, 3}, 60); r.OK || r.Reason == "" {
		t.Fatalf("sabit: %+v", r)
	}
	if r := detectPeriod([]float64{1, 2, 3}, 60); r.OK {
		t.Fatalf("kısa: %+v", r)
	}
	noise := []float64{5, 1, 9, 2, 7, 3, 8, 0, 6, 4, 9, 1, 3, 8, 2, 7, 0, 5, 4, 6, 1, 9, 3, 2}
	if r := detectPeriod(noise, 60); r.OK && r.Strength > 0.8 {
		t.Fatalf("gürültü güçlü periyot vermemeli: %+v", r)
	}
	if fmtPeriodTR(300) != "5 dk" || fmtPeriodTR(7200) != "2 sa" || fmtPeriodTR(90) != "90 sn" {
		t.Fatal("periyot biçimi")
	}
}

// v0.10.444 — boşluk doldurma: 30 dk'da bir ateşlenen cron, GROUP BY'dan
// yalnız 48 kova olarak gelir (hepsi ≈N → sabit seri, periyot yok);
// ızgaraya oturtulunca 288 nokta ve 1800 sn periyot.
func TestFillSeriesRevealsSparseCron(t *testing.T) {
	from := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	var ts []int64
	var vs []float64
	for i := 0; i < 48; i++ {
		ts = append(ts, from.Add(time.Duration(i)*30*time.Minute).UnixNano())
		vs = append(vs, 12)
	}
	if r := detectPeriod(vs, 300); r.OK {
		t.Fatalf("boşluksuz seri sabit görünmeli (hata sınıfı): %+v", r)
	}
	ft, fv := fillSeries(ts, vs, 300, from, to)
	if len(ft) != 288 || len(fv) != 288 || fv[0] != 12 || fv[1] != 0 || fv[6] != 12 {
		t.Fatalf("ızgara: n=%d v0=%v v1=%v v6=%v", len(ft), fv[0], fv[1], fv[6])
	}
	if r := detectPeriod(fv, 300); !r.OK || r.PeriodS != 1800 {
		t.Fatalf("30 dk cron bulunmalı: %+v", r)
	}
	// Hizasız damgalar aynı kovaya toplanır; pencere dışı düşer; boş girdi aynen.
	ft2, fv2 := fillSeries([]int64{from.Add(10 * time.Second).UnixNano(), from.Add(20 * time.Second).UnixNano(), to.Add(time.Hour).UnixNano()}, []float64{1, 2, 99}, 300, from, to)
	if len(ft2) != 288 || fv2[0] != 3 {
		t.Fatalf("toplama/pencere dışı: %v", fv2[:2])
	}
	if t3, v3 := fillSeries(nil, nil, 0, from, to); t3 != nil || v3 != nil {
		t.Fatal("adım 0 → aynen")
	}
}

func TestRouteCallPeriod(t *testing.T) {
	services := []string{"checkout-service", "payment-service"}
	r := routeGuidedIntent("checkout-service'den payment-service'e atılan isteklerde her 5 dk gibi bir periyot var mı", services, nil, nil, "")
	if r.Intent != guidedCallPeriod || r.PairFrom != "checkout-service" || r.PairTo != "payment-service" || r.PairToKind != "service" {
		t.Fatalf("çift periyot: %+v", r)
	}
	r = routeGuidedIntent("payment-service isteklerinde periyot var mı", services, nil, nil, "")
	if r.Intent != guidedCallPeriod || r.Service != "payment-service" || r.PairTo != "" {
		t.Fatalf("tek servis: %+v", r)
	}
	r = routeGuidedIntent("bu servise düzenli aralıklarla istek geliyor mu", services, nil, nil, "checkout-service")
	if r.Intent != guidedCallPeriod || r.Service != "checkout-service" {
		t.Fatalf("bağlam servisi: %+v", r)
	}
	r = routeGuidedIntent("isteklerde periyot var mı", services, nil, nil, "")
	if r.Intent != guidedAskService || r.AskIntent != guidedCallPeriod {
		t.Fatalf("servissiz sor: %+v", r)
	}
	r.ServiceOptions = services
	for _, c := range guidedSuggestions(r) {
		if rr := routeGuidedIntent(c, services, nil, nil, ""); rr.Intent != guidedCallPeriod || rr.Service == "" {
			t.Errorf("çip %q → %+v", c, rr)
		}
	}
	if !hasGuidedSignal("checkout every 5 minutes pattern?") || !hasPeriodSignal(guidedTokens("her 5 dakikada bir")) || hasPeriodSignal(guidedTokens("checkout nasıl")) {
		t.Fatal("periyot sinyali/kapı")
	}
	// v0.10.443 — periyot dalı hata/log/deploy/problem şekillerini yutmaz;
	// öznesiz ve istek sözcüksüz cümle sormaz (düşer).
	if r := routeGuidedIntent("cron loglarında hata var mı", services, nil, nil, ""); r.Intent != guidedLogErrors {
		t.Fatalf("log_errors korunmalı: %+v", r)
	}
	if r := routeGuidedIntent("checkout-service'ta düzenli olarak hata alıyoruz", services, nil, nil, ""); r.Intent == guidedCallPeriod || r.Intent == guidedAskService {
		t.Fatalf("sağlık/hata yolu korunmalı: %+v", r)
	}
	if r := routeGuidedIntent("her gün saat 09:00'da deploy oluyor mu", services, nil, nil, ""); r.Intent != guidedDeployImpact {
		t.Fatalf("deploy korunmalı: %+v", r)
	}
	if r := routeGuidedIntent("periyodik bir şey var mı", services, nil, nil, ""); r.Intent == guidedAskService {
		t.Fatalf("istek sözcüksüz öznesiz periyot sormaz: %+v", r)
	}
	links := guidedAnswerLinkTargets(guidedRoute{Intent: guidedCallPeriod, PairFrom: "checkout-service", PairTo: "payment-service", PairToKind: "service"})
	if len(links) != 3 || links[0].Href != "/service-map?focus=checkout-service" {
		t.Fatalf("linkler: %+v", links)
	}
	for _, sg := range guidedSuggestions(guidedRoute{Intent: guidedCallPeriod, PairFrom: "checkout-service", PairTo: "payment-service", PairToKind: "service"}) {
		if routeGuidedIntent(sg, services, nil, nil, "").Intent == guidedNone {
			t.Errorf("öneri yönlenmeli: %q", sg)
		}
	}
}

func TestRenderCallPeriodTR(t *testing.T) {
	base := time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)
	var ts []int64
	var vals []float64
	for i := 0; i < 60; i++ {
		ts = append(ts, base.Add(time.Duration(i)*time.Minute).UnixNano())
		x := 2.0
		if i%5 == 0 {
			x = 20
		}
		vals = append(vals, x)
	}
	series := []periodSeries{
		{Label: "checkout → payment yönlü çağrı", StepS: 300, Note: "(5 dk kova)"},
		{Label: "checkout giden client span/dk", StepS: 60, Times: ts, Values: vals, Note: "(tüm hedefler)"},
	}
	ev := renderCallPeriodTR(guidedRoute{PairFrom: "checkout", PairTo: "payment"}, series, 6*time.Hour, 24*time.Hour)
	for _, want := range []string{"checkout → payment çağrı periyodu", "yönlü çağrı: veri yok", "PERİYOT ~5 dk", "Tepeler (UTC): 10:00 20", "(tüm hedefler)", "Sonuç: periyot bulunan"} {
		if !strings.Contains(ev, want) {
			t.Errorf("kanıt %q içermeli:\n%s", want, ev)
		}
	}
	if !strings.Contains(renderCallPeriodTR(guidedRoute{Service: "x"}, nil, time.Hour, time.Hour), "Seri okunamadı") {
		t.Fatal("boş dürüst")
	}
}
