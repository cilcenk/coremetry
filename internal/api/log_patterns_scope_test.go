package api

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// v0.10.507 — log arama denetimi C7: "✨ Desenleri anlat" sayfa süzgecini ve
// seçili pencereyi yok sayıyordu (servissiz 30 dk tavan, windowSec atılıyor).
// Paketin ilk bölümü artık BAKILAN KAPSAM: süzgeç + gerçek pencere + o
// kapsamın kendi desen örneklemesi; filo geneli bölümler ayrı etiketli.
func TestRenderLogPatternsScopeEvidence(t *testing.T) {
	from := time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)
	to := from.Add(6 * time.Hour)
	sc := logPatternsScope{Service: "payments", Env: "prod", Search: `level:error AND "timeout"`, From: from, To: to, Severity: 17}
	pats := &logstore.PatternsResult{Sampled: 500, Distinct: 12, Truncated: true,
		CoveredFromNs: from.Add(5 * time.Hour).UnixNano(), CoveredToNs: to.UnixNano(),
		Groups: []logstore.SignatureGroup{
			{Template: "connection refused to <*>", Count: 210, Severity: 17, SeverityText: "ERROR", Services: []string{"payments", "cart"}, ServiceCount: 5},
			{Template: "retrying <*>", Count: 40, Severity: 13, Services: []string{"payments"}, ServiceCount: 1},
		}}
	got := renderLogPatternsScopeEvidence(sc, pats, true)
	for _, want := range []string{
		"BAKILAN KAPSAM",
		"servis=payments", "env=prod", "severity≥17", `arama=level:error AND "timeout"`,
		"Pencere: son 6 saat (2026-09-06 10:00 → 2026-09-06 16:00 UTC)",
		"500 örnek satırdan 12 farklı desen, ilk 2",
		"Örnekleme tavanı doldu: sayımlar yalnız 15:00 → 16:00 UTC",
		`1. "connection refused to <*>" — 210 satır, ERROR; servisler: payments, cart +3`,
		`2. "retrying <*>" — 40 satır, seviye 13; servisler: payments`,
		"FİLO GENELİ (süzgeçten bağımsız)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("pakette %q yok:\n%s", want, got)
		}
	}

	t.Run("süzgeç yok + örnekleme okunamadı", func(t *testing.T) {
		got := renderLogPatternsScopeEvidence(logPatternsScope{From: from, To: to}, nil, false)
		for _, want := range []string{"Süzgeç: yok (tüm servisler)", "OKUNAMADI", "sıfır DEĞİL"} {
			if !strings.Contains(got, want) {
				t.Errorf("pakette %q yok:\n%s", want, got)
			}
		}
	})
	t.Run("boş örnekleme dürüst", func(t *testing.T) {
		got := renderLogPatternsScopeEvidence(logPatternsScope{Service: "x", From: from, To: to}, &logstore.PatternsResult{}, true)
		if !strings.Contains(got, "örneklenen satır yok") {
			t.Errorf("boş örnekleme söylenmeli:\n%s", got)
		}
	})
	t.Run("label kısa kapsam metni", func(t *testing.T) {
		if l := sc.label(); !strings.Contains(l, "payments") || !strings.Contains(l, "prod") || !strings.Contains(l, "son 6 saat") {
			t.Errorf("label = %q", l)
		}
	})
}

// Kaynak pini: handler kapsam örneklemesini sayfa süzgeciyle çeker (tek ES
// sayfası), şablon kataloğunu servise daraltır, windowSec gerçek pencere.
func TestExplainLogPatternsUsesScope(t *testing.T) {
	b, err := os.ReadFile("log_patterns_explain.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		"logstore.GroupBySignatureN(pctx, s.logs, sf, logPatternsExplainScopeTop, logsPatternsSample(500))",
		"Service: scope.Service, // v0.10.507",
		"renderLogPatternsScopeEvidence(scope, pats, patsErr == nil) +",
		`"windowSec": int(scope.To.Sub(scope.From).Seconds())`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("explainLogPatterns missing %q", want)
		}
	}
}
