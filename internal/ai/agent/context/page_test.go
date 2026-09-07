package agentctx

import (
	"strings"
	"testing"
)

// v0.10.539 — sayfa bağlamı önsözü: boş alan yazılmaz, pin önce, açık sayfa
// yalnız yeni boyutları yazar (servis/aralık eski önsözde), sanitize sınırlar.
func TestPreambleTR(t *testing.T) {
	if PreambleTR(nil, nil) != "" || PreambleTR(&PageContext{Page: "traces"}, nil) != "" {
		t.Fatal("boş bağlam önsöz üretmez")
	}
	cur := &PageContext{Page: "traces", Path: "/traces", Service: "api", Cluster: "c1", Namespace: "pay", Env: "prod",
		TimeRange: &PageRange{Preset: "6h"}, Search: "UPDATE",
		Filters: []PageFilter{{K: "http.route", Op: "=", V: []string{"/pay"}}, {K: "status", Op: "=", V: []string{"error"}}}}
	out := PreambleTR(cur, nil)
	for _, want := range []string{"AÇIK SAYFA", "- sayfa: traces (/traces)", "- cluster: c1", "- namespace: pay", "- arama: UPDATE", "- filtreler: http.route = /pay; status = error"} {
		if !strings.Contains(out, want) {
			t.Errorf("eksik %q:\n%s", want, out)
		}
	}
	for _, no := range []string{"- servis:", "- ortam:", "- zaman aralığı:", "SABİTLENMİŞ", "(bilinmiyor)"} {
		if strings.Contains(out, no) {
			t.Errorf("açık sayfa bloğunda olmamalı %q:\n%s", no, out)
		}
	}
	pinned := &PageContext{Page: "problems", Service: "checkout", ProblemID: "p1", TimeRange: &PageRange{Preset: "custom", FromMs: 1_700_000_000_000, ToMs: 1_700_003_600_000}}
	out = PreambleTR(cur, pinned)
	i, j := strings.Index(out, "SABİTLENMİŞ BAĞLAM"), strings.Index(out, "AÇIK SAYFA")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("pin açık sayfadan ÖNCE yazılmalı:\n%s", out)
	}
	for _, want := range []string{"- sayfa: problems", "- servis: checkout", "- problem: p1", "- zaman aralığı: 2023-11-14 22:13 → 2023-11-14 23:13 UTC"} {
		if !strings.Contains(out[:j], want) {
			t.Errorf("pin bloğunda eksik %q:\n%s", want, out[:j])
		}
	}
}

func TestSanitize(t *testing.T) {
	if Sanitize(nil) != nil || Sanitize(&PageContext{Page: "  "}) != nil {
		t.Fatal("nil / boş sayfa → nil")
	}
	long := strings.Repeat("x", 500)
	fs := make([]PageFilter, 0, 25)
	for i := 0; i < 25; i++ {
		fs = append(fs, PageFilter{K: "k", Op: "=", V: []string{"v"}})
	}
	fs[0].V = make([]string, 15)
	fs[1].K = " "
	p := Sanitize(&PageContext{Page: " traces\n", Service: "a\x00b\nc", Search: long, Filters: fs, TimeRange: &PageRange{Preset: " 1h "}})
	if p.Page != "traces" || p.Service != "abc" || p.TimeRange.Preset != "1h" {
		t.Fatalf("kırpma/kontrol karakteri: %+v", p)
	}
	if n := len([]rune(p.Search)); n != maxValueRunes+1 {
		t.Fatalf("rune tavanı %d, got %d", maxValueRunes+1, n)
	}
	if len(p.Filters) != maxFilters-1 || len(p.Filters[0].V) != maxFilterVals {
		t.Fatalf("filtre tavanları: %d filtre, ilkinde %d değer", len(p.Filters), len(p.Filters[0].V))
	}
}
