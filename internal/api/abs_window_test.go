package api

import (
	"strings"
	"testing"
	"time"
)

// abs_window_test.go — v0.10.437 (CoSRE router boşlukları D6).

func TestExtractAbsoluteWindows(t *testing.T) {
	ist := time.FixedZone("UTC+3", 3*3600)
	now := time.Date(2026, 9, 6, 15, 0, 0, 0, ist)
	at := func(y int, m time.Month, d, h, mi int) time.Time { return time.Date(y, m, d, h, mi, 0, 0, ist) }
	cases := []struct {
		msg  string
		want []absWindow
	}{
		{"08/08/2026 saat 04-08 ile 08-09 arası checkout servis süreleri", []absWindow{{at(2026, 8, 8, 4, 0), at(2026, 8, 8, 8, 0)}, {at(2026, 8, 8, 8, 0), at(2026, 8, 8, 9, 0)}}},
		{"2026-08-08 04:00-08:30 arası hatalar", []absWindow{{at(2026, 8, 8, 4, 0), at(2026, 8, 8, 8, 30)}}},
		{"8 ağustos 2026 tüm gün nasıldı", []absWindow{{at(2026, 8, 8, 0, 0), at(2026, 8, 9, 0, 0)}}},
		{"bugün 09 ile 10 arası", []absWindow{{at(2026, 9, 6, 9, 0), at(2026, 9, 6, 10, 0)}}},
		// gelecekteki saat → dün; gece sarması → ertesi gün
		{"22-02 arası neler oldu", []absWindow{{at(2026, 9, 5, 22, 0), at(2026, 9, 6, 2, 0)}}},
		{"07/08/2026 22-02 arası", []absWindow{{at(2026, 8, 7, 22, 0), at(2026, 8, 8, 2, 0)}}},
		{"08/08/2026 04-08 ile 09/08/2026 04-08 kıyas", []absWindow{{at(2026, 8, 8, 4, 0), at(2026, 8, 8, 8, 0)}, {at(2026, 8, 9, 4, 0), at(2026, 8, 9, 8, 0)}}},
		{"son 2 saatte checkout nasıl", nil},
		{"checkout p95 nedir", nil},
	}
	for _, c := range cases {
		got, ok := extractAbsoluteWindows(c.msg, now, ist)
		if (c.want == nil) != !ok {
			t.Errorf("%q → ok=%v %+v", c.msg, ok, got)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("%q → %d pencere, want %d: %+v", c.msg, len(got), len(c.want), got)
			continue
		}
		for i := range got {
			if !got[i].From.Equal(c.want[i].From) || !got[i].To.Equal(c.want[i].To) {
				t.Errorf("%q [%d] → %s–%s, want %s–%s", c.msg, i, got[i].From, got[i].To, c.want[i].From, c.want[i].To)
			}
		}
	}
	if !looksLikeAbsoluteWindow("08/08/2026 04-08") || looksLikeAbsoluteWindow("checkout nasıl") {
		t.Fatal("kapı")
	}
	if chatLocation(180).String() != "UTC+3" || chatLocation(0) != time.UTC || chatLocation(9999) != time.UTC {
		t.Fatal("konum")
	}
	if l := absWindowLabel(absWindow{at(2026, 8, 8, 4, 0), at(2026, 8, 8, 8, 0)}, ist); l != "08/08 04:00–08:00" {
		t.Fatalf("etiket: %s", l)
	}
	if txt := absWindowText("08/08/2026 saat 04-08 ile 08-09 arası"); txt != "08/08/2026 ile 04-08 ile 08-09" {
		t.Fatalf("pencere metni: %q", txt)
	}
}

func TestApplyAbsoluteWindowsAndRender(t *testing.T) {
	ist := time.FixedZone("UTC+3", 3*3600)
	w1 := absWindow{time.Date(2026, 8, 8, 4, 0, 0, 0, ist), time.Date(2026, 8, 8, 8, 0, 0, 0, ist)}
	w2 := absWindow{time.Date(2026, 8, 8, 8, 0, 0, 0, ist), time.Date(2026, 8, 8, 9, 0, 0, 0, ist)}
	now := time.Now()
	// Tek pencere: rota aynı, çıpa ve uzunluk pencere.
	r, to, rs, label := applyAbsoluteWindows(guidedRoute{Intent: guidedServiceHealth, Service: "checkout"}, []absWindow{w1}, "", now, 1800, ist, "x")
	if r.Intent != guidedServiceHealth || !to.Equal(w1.To) || rs != 4*3600 || !strings.HasPrefix(label, "pencere: 08/08 04:00–08:00") {
		t.Fatalf("tek pencere: %+v %s %d %q", r, to, rs, label)
	}
	// İki pencere + servis → window_compare; servissiz + bağlam → bağlam; hiçbiri → sor.
	r, _, _, label = applyAbsoluteWindows(guidedRoute{Intent: guidedNone, Service: "checkout"}, []absWindow{w1, w2}, "", now, 1800, ist, "08/08/2026 ile 04-08 ile 08-09")
	if r.Intent != guidedWindowCompare || r.Service != "checkout" || len(r.Windows) != 2 || !strings.HasPrefix(label, "kıyas: ") {
		t.Fatalf("kıyas: %+v %q", r, label)
	}
	r, _, _, _ = applyAbsoluteWindows(guidedRoute{}, []absWindow{w1, w2}, "payments", now, 1800, ist, "t")
	if r.Intent != guidedWindowCompare || r.Service != "payments" {
		t.Fatalf("bağlam servisi: %+v", r)
	}
	r, _, _, _ = applyAbsoluteWindows(guidedRoute{}, []absWindow{w1, w2}, "", now, 1800, ist, "08/08/2026 ile 04-08 ile 08-09")
	if r.Intent != guidedAskService || r.AskIntent != guidedWindowCompare || r.WindowText == "" {
		t.Fatalf("servissiz sor: %+v", r)
	}
	// Çip pencere metnini taşır ve pencereler yeniden çıkarılır.
	r.ServiceOptions = []string{"checkout-service"}
	chips := guidedSuggestions(r)
	if len(chips) != 1 || !strings.Contains(chips[0], "08/08/2026 ile 04-08 ile 08-09") {
		t.Fatalf("çip: %v", chips)
	}
	if wins, ok := extractAbsoluteWindows(chips[0], now, ist); !ok || len(wins) != 2 {
		t.Fatalf("çipten pencereler: %v %+v", ok, wins)
	}
	// Linkler kendi range'iyle; render deterministik.
	links := guidedAnswerLinkTargets(guidedRoute{Intent: guidedWindowCompare, Service: "checkout", Windows: []absWindow{w1, w2}})
	if len(links) != 2 || !strings.HasPrefix(links[0].Href, "/service?name=checkout&range=custom:") || !strings.Contains(links[1].Label, "08/08 08:00–09:00") {
		t.Fatalf("linkler: %+v", links)
	}
	ev := renderWindowCompareTR("checkout", []absWindow{w1, w2}, []aiRED{{Spans: 1000, Rate: 2, ErrorRate: 1.5, ErrorCount: 15, P50Ms: 40, P95Ms: 200, P99Ms: 900}, {Spans: 500, Rate: 1, ErrorRate: 3, ErrorCount: 15, P50Ms: 60, P95Ms: 400, P99Ms: 1500}}, ist)
	for _, want := range []string{"Pencere 1 08/08 04:00–08:00: 1000 span", "p95 200 ms", "p99 1.50 s", "trafik -50%", "p95 +100%", "hata oranı 1.50 → 3.00 puan"} {
		if !strings.Contains(ev, want) {
			t.Errorf("kanıt %q içermeli:\n%s", want, ev)
		}
	}
	if !strings.Contains(renderWindowCompareTR("x", []absWindow{w1, w2}, []aiRED{{}, {}}, ist), "span verisi yok") {
		t.Fatal("boş pencereler dürüst")
	}
	for _, sg := range guidedSuggestions(guidedRoute{Intent: guidedWindowCompare, Service: "checkout"}) {
		if routeGuidedIntent(sg, []string{"checkout"}, nil, nil, "").Intent == guidedNone {
			t.Errorf("öneri yönlenmeli: %q", sg)
		}
	}
}
