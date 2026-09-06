package mcptools

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.473 (Faz 3, F3-2) — search_traces süzgeç/kapı/deep-link saf parçaları.

func TestToFilterExpr(t *testing.T) {
	cases := []struct {
		in  SearchFilter
		op  string
		v0  string
		bad bool
	}{
		{SearchFilter{Key: "http.route", Value: "/pay"}, "=", "/pay", false},
		{SearchFilter{Key: "http.route", Op: "ne", Value: "/health"}, "!=", "/health", false},
		{SearchFilter{Key: "server.address", Op: "in", Values: []string{"a", "b"}}, "IN", "a", false},
		{SearchFilter{Key: "url.full", Op: "contains", Value: "gw.example.com"}, "LIKE", "gw.example.com", false},
		{SearchFilter{Key: "url.path", Op: "prefix", Value: "/payment/"}, "=~", `/payment/.*`, false},
		{SearchFilter{Key: "url.path", Op: "regex", Value: "/pay(ment)?/.*"}, "=~", "/pay(ment)?/.*", false},
		{SearchFilter{Key: "CHANNEL_CODE", Op: "exists"}, "EXISTS", "", false},
		{SearchFilter{Key: "", Value: "x"}, "", "", true},
		{SearchFilter{Key: "k", Op: "eq"}, "", "", true},
		{SearchFilter{Key: "k", Op: "regex", Value: "("}, "", "", true},
		{SearchFilter{Key: "k", Op: "fuzzy", Value: "x"}, "", "", true},
	}
	for _, c := range cases {
		fe, err := toFilterExpr(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("%+v hata beklenir", c.in)
			}
			continue
		}
		if err != nil || fe.Op != c.op || (c.v0 != "" && fe.Values[0] != c.v0) {
			t.Errorf("%+v → %+v %v; want op=%s v0=%s", c.in, fe, err, c.op, c.v0)
		}
		if err := fe.Validate(); err != nil {
			t.Errorf("%+v derleyici için geçersiz: %v", fe, err)
		}
	}
}

func TestApplySearchGate(t *testing.T) {
	col := []chstore.FilterExpr{{Key: "http.route", Op: "=", Values: []string{"/pay"}}}
	arr := []chstore.FilterExpr{{Key: "server.address", Op: "=", Values: []string{"gw"}}}
	if g, err := applySearchGate(604800, 100, nil, false, false, false); err != nil || g.RangeS != 604800 || g.Limit != 100 || g.Reason != "" {
		t.Fatalf("süzgeçsiz kapı dokunmaz: %+v %v", g, err)
	}
	if _, err := applySearchGate(1800, 20, col, false, false, false); err == nil {
		t.Fatal("süzgeç + kapsamsız → hata")
	}
	if g, _ := applySearchGate(86400, 100, col, true, false, false); g.RangeS != searchWindowIndexedMax || g.Limit != 50 || !strings.Contains(g.Reason, "6 saat") {
		t.Errorf("indeksli anahtar 6 s + 50 satır: %+v", g)
	}
	if g, _ := applySearchGate(7200, 20, arr, true, false, false); g.RangeS != searchWindowUnindexedMax || !strings.Contains(g.Reason, "indekssiz") {
		t.Errorf("indekssiz anahtar 1 s: %+v", g)
	}
	if g, _ := applySearchGate(7200, 20, arr, true, false, true); g.RangeS != 7200 || g.Reason != "" {
		t.Errorf("kvh varken dizi anahtarı indeksli: %+v", g)
	}
	if g, _ := applySearchGate(7200, 20, col, true, true, false); g.RangeS != searchWindowUnindexedMax || !strings.Contains(g.Reason, "süre sıralaması") {
		t.Errorf("süre sıralaması + süzgeç 1 s: %+v", g)
	}
	if g, _ := applySearchGate(600, 10, col, true, true, false); g.RangeS != 600 || g.Reason != "" {
		t.Errorf("tavanın altı dokunulmaz: %+v", g)
	}
}

func TestTracesDeepLink(t *testing.T) {
	from := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	to := from.Add(30 * time.Minute)
	f := chstore.TraceFilter{Service: "checkout", Filters: []chstore.FilterExpr{{Key: "http.route", Op: "=", Values: []string{"/pay"}}}, HasError: true, Sort: "duration", Env: "prod", MinMs: 250}
	h := TracesDeepLink(f, from, to)
	u, err := url.Parse(h)
	if err != nil || u.Path != "/traces" {
		t.Fatalf("href: %s %v", h, err)
	}
	q := u.Query()
	if q.Get("service") != "checkout" || q.Get("hasError") != "true" || q.Get("sort") != "duration" || q.Get("order") != "desc" || q.Get("env") != "prod" || q.Get("minMs") != "250" {
		t.Errorf("paramlar: %s", h)
	}
	if !strings.Contains(q.Get("filters"), `"k":"http.route"`) || !strings.Contains(q.Get("filters"), `"op":"="`) {
		t.Errorf("filters JSON: %s", q.Get("filters"))
	}
	if q.Get("range") != "custom:"+"1788696000000-1788697800000" {
		t.Errorf("range: %s", q.Get("range"))
	}
}
