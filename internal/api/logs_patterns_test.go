package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// logs_patterns_test.go — v0.10.296: degrade sözleşmesi (v0.8.350 deseni),
// gerçek hata 5xx, anahtar tüm girdileri taşır, limit basamağı.

type patternsStore struct {
	logstore.Store
	err  error
	rows []*logstore.LogRecord
}

func (p *patternsStore) Search(context.Context, logstore.Filter) (*logstore.Page, error) {
	if p.err != nil {
		return nil, p.err
	}
	return &logstore.Page{Total: len(p.rows), Logs: p.rows}, nil
}
func (p *patternsStore) Backend() string { return "clickhouse" }

func patternsServer(st logstore.Store) *Server {
	return &Server{logs: st, cache: &fakeCache{}, l1: newL1Cache(8), stats: newCacheStats()}
}

func TestGetLogsPatterns_DegradesOn200(t *testing.T) {
	s := patternsServer(&patternsStore{err: dialRefused()})
	w := httptest.NewRecorder()
	s.getLogsPatterns(w, httptest.NewRequest("GET", "/api/logs/patterns?service=checkout", nil))
	if w.Code != 200 {
		t.Fatalf("status %d; degrade sözleşmesi 200 — %s", w.Code, w.Body.String())
	}
	var body struct {
		Degraded bool                      `json:"degraded"`
		Reason   string                    `json:"reason"`
		Groups   []logstore.SignatureGroup `json:"groups"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Degraded || body.Reason == "" || body.Groups == nil {
		t.Errorf("degraded=%v reason=%q groups=%v", body.Degraded, body.Reason, body.Groups)
	}
}

func TestGetLogsPatterns_GenuineErrorStays5xx(t *testing.T) {
	s := patternsServer(&patternsStore{err: errors.New("bad field")})
	w := httptest.NewRecorder()
	s.getLogsPatterns(w, httptest.NewRequest("GET", "/api/logs/patterns", nil))
	if w.Code == 200 {
		t.Errorf("gerçek hata degraded diye maskelenmemeli: %d", w.Code)
	}
}

func TestGetLogsPatterns_GroupsAndSyntaxGate(t *testing.T) {
	s := patternsServer(&patternsStore{rows: []*logstore.LogRecord{
		{Timestamp: 1, ServiceName: "a", Body: "disk 91% full on /dev/sda1"},
		{Timestamp: 2, ServiceName: "a", Body: "disk 92% full on /dev/sda1"},
		{Timestamp: 3, ServiceName: "b", Body: "boot ok"},
	}})
	w := httptest.NewRecorder()
	s.getLogsPatterns(w, httptest.NewRequest("GET", "/api/logs/patterns?limit=1", nil))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var res logstore.PatternsResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	// limit=1 basamağa yuvarlanır (20) → iki grup da döner; sıra sayıya göre.
	if len(res.Groups) != 2 || res.Groups[0].Count != 2 || res.Distinct != 2 || res.Sampled != 3 {
		t.Errorf("groups=%+v distinct=%d sampled=%d", res.Groups, res.Distinct, res.Sampled)
	}
	// Sözdizimi kapısı burada da (CH): kapanmamış tırnak 400.
	w2 := httptest.NewRecorder()
	s.getLogsPatterns(w2, httptest.NewRequest("GET", "/api/logs/patterns?search=%22disk", nil))
	if w2.Code != 400 {
		t.Errorf("sözdizimi hatası 400 olmalı: %d", w2.Code)
	}
}

func TestLogsPatternsKeyAndLimit(t *testing.T) {
	base := logstore.Filter{Service: "a", Search: "x"}
	k1 := logsPatternsKey(base, "1", "2", 50)
	for _, v := range []struct {
		name string
		f    logstore.Filter
		from string
		lim  int
	}{
		{"service", logstore.Filter{Service: "b", Search: "x"}, "1", 50},
		{"search", logstore.Filter{Service: "a", Search: "y"}, "1", 50},
		{"cluster", logstore.Filter{Service: "a", Search: "x", Cluster: "c"}, "1", 50},
		{"env", logstore.Filter{Service: "a", Search: "x", Env: "prod"}, "1", 50},
		{"severity", logstore.Filter{Service: "a", Search: "x", SeverityMin: 17}, "1", 50},
		{"hasTrace", logstore.Filter{Service: "a", Search: "x", HasTrace: true}, "1", 50},
		{"from", base, "9", 50},
		{"limit", base, "1", 100},
	} {
		if logsPatternsKey(v.f, v.from, "2", v.lim) == k1 {
			t.Errorf("%s anahtara girmiyor", v.name)
		}
	}
	if !strings.HasPrefix(k1, "logs-patterns:v1:") {
		t.Errorf("anahtar öneki %q", k1)
	}
	for _, tc := range []struct{ want, rung int }{{0, 50}, {1, 20}, {20, 20}, {21, 50}, {50, 50}, {51, 100}, {100, 100}, {999, 100}} {
		if got := logsPatternsLimit(tc.want); got != tc.rung {
			t.Errorf("logsPatternsLimit(%d) = %d; want %d", tc.want, got, tc.rung)
		}
	}
}
