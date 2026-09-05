package api

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
)

// log_field_search_test.go — v0.10.433 (CoSRE router boşlukları D5).

func TestExtractLogFieldQuery(t *testing.T) {
	cases := []struct {
		msg          string
		field, value string
		contains, ok bool
	}{
		{`url.full field'ında "/api/x/y" geçen loglar`, "url.full", "/api/x/y", true, true},
		{`url.full alanında "/api/x/y" geçen loglar`, "url.full", "/api/x/y", true, true},
		{`checkout loglarında message alanında 'timeout' geçen kayıtlar`, "message", "timeout", true, true},
		{`http.route attribute'unda “/pay” olan loglar`, "http.route", "/pay", false, true},
		{`url.full:"/api/x" geçen loglar`, "url.full", "/api/x", true, true},
		{`k8s.pod.name:checkout-7f loglar`, "k8s.pod.name", "checkout-7f", true, true},
		{`url.full:*/api/x* loglar`, "url.full", "/api/x", true, true},
		// log kökü yok → D2'nin işi (trace araması)
		{`url.full alanında "/api/x" geçen istekler`, "", "", false, false},
		// Türkçe sözcük alan olamaz
		{`hata alanında "x" geçen loglar`, "", "", false, false},
		// tırnaklı değer yok
		{`url.full alanında bir şeyler geçen loglar`, "", "", false, false},
		{`log hataları neler`, "", "", false, false},
	}
	for _, c := range cases {
		f, v, contains, ok := extractLogFieldQuery(c.msg, guidedTokens(normalizeGuidedMsg(c.msg)))
		if ok != c.ok || f != c.field || v != c.value || (ok && contains != c.contains) {
			t.Errorf("%q → (%q,%q,%v,%v), want (%q,%q,%v,%v)", c.msg, f, v, contains, ok, c.field, c.value, c.contains, c.ok)
		}
	}
}

func TestLogFieldSearchQueryPerBackend(t *testing.T) {
	if q, note := logFieldSearchQuery("url.full", "/api/x", true, "clickhouse"); q != "url.full:*/api/x*" || note != "" {
		t.Fatalf("CH içeren: %q %q", q, note)
	}
	if q, note := logFieldSearchQuery("url.full", "/api/x", true, "elasticsearch"); q != `url.full:"/api/x"` || !strings.Contains(note, "TAM İFADE") {
		t.Fatalf("ES içeren tam ifade + not: %q %q", q, note)
	}
	if q, note := logFieldSearchQuery("message", "time out", true, "clickhouse"); q != `message:"time out"` || note == "" {
		t.Fatalf("boşluklu değer CH'de de ifade: %q %q", q, note)
	}
	if q, note := logFieldSearchQuery("message", `a"b`, false, "clickhouse"); q != `message:"a\"b"` || note != "" {
		t.Fatalf("tam eşleşme kaçışlı: %q %q", q, note)
	}
	// CH derleyicisi tırnaksız jokeri LIKE'a çevirir (alt-dize gerçekten koşar).
	sql, _ := chstore.LogSearchConjunct("url.full:*/api/x*")
	if !strings.Contains(strings.ToLower(sql), "like") {
		t.Fatalf("CH joker LIKE'a derlenmeli: %q", sql)
	}
}

func TestRouteGuidedLogField(t *testing.T) {
	services := []string{"checkout-service", "message-broker", "payment-service"}
	r := routeGuidedIntent(`url.full field'ında "/api/x/y" geçen loglar`, services, nil, nil, "")
	if r.Intent != guidedLogField || r.LogField != "url.full" || r.LogValue != "/api/x/y" || !r.LogContains || r.Service != "" {
		t.Fatalf("alan rotası: %+v", r)
	}
	r = routeGuidedIntent(`checkout-service loglarında message alanında 'timeout' geçen loglar`, services, nil, nil, "")
	if r.Intent != guidedLogField || r.Service != "checkout-service" || r.LogField != "message" {
		t.Fatalf("servisli alan rotası: %+v", r)
	}
	// "message" jetonu message-broker'a aday olarak oturmamalı (D1 aday üreticisi alan sorgusunda kapalı).
	r = routeGuidedIntent(`message alanında 'timeout' geçen loglar`, services, nil, nil, "")
	if r.Intent != guidedLogField || r.Service != "" {
		t.Fatalf("alan adı servis adayı olmamalı: %+v", r)
	}
	// Tırnaklı değer "slow" slow_traces'ı tetiklememeli.
	r = routeGuidedIntent(`message alanında "slow query" geçen loglar`, services, nil, nil, "")
	if r.Intent != guidedLogField {
		t.Fatalf("değerdeki içerik sözcüğü rotayı kaçırmamalı: %+v", r)
	}
	// Alan sorgusu olmayan log-hata sorusu eski rotada.
	if r := routeGuidedIntent("checkout-service loglarında hata var mı", services, nil, nil, ""); r.Intent != guidedLogErrors {
		t.Fatalf("log_errors korunmalı: %+v", r)
	}
	if !hasGuidedSignal(`url.full field'ında "/api/x" geçen loglar`) {
		t.Fatal("kılavuz kapısı log kökünü geçirmeli")
	}
}

func TestLogFieldLinksAndSuggestions(t *testing.T) {
	route := guidedRoute{Intent: guidedLogField, Service: "checkout-service", LogField: "url.full", LogValue: "/api/x", LogContains: true, LogQuery: "url.full:*/api/x*"}
	links := guidedAnswerLinkTargets(route)
	if len(links) != 1 || links[0].Href != "/logs?q=url.full%3A%2A%2Fapi%2Fx%2A&service=checkout-service" || links[0].Label != "Loglar (url.full)" {
		t.Fatalf("link: %+v", links)
	}
	// LogQuery boşsa backend'siz şekil (tam ifade).
	route.LogQuery = ""
	if links := guidedAnswerLinkTargets(route); !strings.Contains(links[0].Href, "url.full%3A%22%2Fapi%2Fx%22") {
		t.Fatalf("backend'siz link: %+v", links)
	}
	for _, sgs := range [][]string{guidedSuggestions(route), guidedSuggestions(guidedRoute{Intent: guidedLogField})} {
		for _, sg := range sgs {
			if r := routeGuidedIntent(sg, []string{"checkout-service"}, nil, nil, ""); r.Intent == guidedNone {
				t.Errorf("öneri yönlenmeli: %q", sg)
			}
		}
	}
	if !followUpFillable[guidedLogField] {
		t.Fatal("takip turunda servis doldurulabilmeli")
	}
}

func TestParseIntentLogField(t *testing.T) {
	services := []string{"checkout-service"}
	r, _, ok := parseIntentJSON(`{"intent":"log_field","service":"checkout","logField":"url.full","logValue":"/api/pay"}`, services, nil, nil, "")
	if !ok || r.Intent != guidedLogField || r.Service != "checkout-service" || r.LogField != "url.full" || r.LogValue != "/api/pay" || !r.LogContains {
		t.Fatalf("log_field: ok=%v %+v", ok, r)
	}
	for _, raw := range []string{
		`{"intent":"log_field","logField":"","logValue":"x"}`,
		`{"intent":"log_field","logField":"url.full","logValue":""}`,
		`{"intent":"log_field","logField":"hata","logValue":"x"}`,
		`{"intent":"log_field","logField":"a b","logValue":"x"}`,
	} {
		if _, _, ok := parseIntentJSON(raw, services, nil, nil, ""); ok {
			t.Errorf("geçersiz alan/değer none olmalı: %s", raw)
		}
	}
	if s := intentSummary(r, 0, true); !strings.Contains(s, `alan: url.full="/api/pay"`) {
		t.Fatalf("özet: %s", s)
	}
}

func TestRenderLogFieldEvidenceTR(t *testing.T) {
	route := guidedRoute{LogField: "url.full", LogValue: "/api/x", LogContains: true}
	page := &logstore.Page{Total: 42, Logs: []*logstore.LogRecord{
		{Timestamp: time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC).UnixNano(), Severity: 17, ServiceName: "checkout", Body: "upstream timeout", Attributes: map[string]string{"url.full": "https://h/api/x?id=1"}},
		{Timestamp: time.Date(2026, 9, 6, 9, 59, 0, 0, time.UTC).UnixNano(), Severity: 9, ServiceName: "payments", Body: strings.Repeat("y", 300)},
	}}
	ev := renderLogFieldEvidenceTR(page, route, "not-x", 3600)
	for _, want := range []string{"toplam 42 kayıt", "⚠ not-x", "ERROR 1, INFO 1", "checkout 1, payments 1", "[url.full=https://h/api/x?id=1]", "10:00:00 ERROR checkout"} {
		if !strings.Contains(ev, want) {
			t.Errorf("kanıt %q içermeli:\n%s", want, ev)
		}
	}
	if strings.Contains(ev, strings.Repeat("y", 200)) {
		t.Fatal("gövde 160 rune'a kırpılmalı")
	}
	empty := renderLogFieldEvidenceTR(&logstore.Page{}, route, "", 3600)
	if !strings.Contains(empty, "eşleşen log yok") {
		t.Fatalf("boş sonuç dürüst: %s", empty)
	}
	if logSeverityNameTR(0) != "UNSET" || logSeverityNameTR(24) != "FATAL" || logSeverityNameTR(13) != "WARN" {
		t.Fatal("severity adları")
	}
}
