package api

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// v0.10.508 (C6) — taban penceresi hemen önceki eşit uzunluk; anahtar
// baseline bayrağını taşır; taban tek sayfa (500) ve tam grup listesi.
func TestLogsPatternsBaselineWindow(t *testing.T) {
	to := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	from := to.Add(-6 * time.Hour)
	bf, bt, ok := logsPatternsBaselineWindow(from, to)
	if !ok || !bf.Equal(from.Add(-6*time.Hour)) || !bt.Equal(from) {
		t.Fatalf("baseline = %v..%v ok=%v", bf, bt, ok)
	}
	if _, _, ok := logsPatternsBaselineWindow(to, to); ok {
		t.Fatal("sıfır pencerede taban olmamalı")
	}
}

func TestLogsPatternsKeyCarriesBaseline(t *testing.T) {
	a := logsPatternsKey(logsFilterForKeyTest(), "1", "2", 50, 500, false)
	b := logsPatternsKey(logsFilterForKeyTest(), "1", "2", 50, 500, true)
	if a == b {
		t.Fatal("baseline anahtara girmeli (aksi hâlde tabansız gövde tabanlıya döner)")
	}
	src, err := os.ReadFile("api_logs_patterns.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"logstore.GroupBySignatureN(bctx, s.logs, bf, logstore.PatternsMaxGroups, logsPatternsBaselineSample)",
		"logstore.JoinPatternBaseline(res, base)",
		"res.Baseline.Degraded, res.Baseline.Reason = true",
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("handler missing %q", want)
		}
	}
	if logsPatternsBaselineSample != 500 {
		t.Fatalf("taban örneklemesi tek ES sayfası (500) kalmalı, %d", logsPatternsBaselineSample)
	}
}

func logsFilterForKeyTest() logstore.Filter { return logstore.Filter{Service: "svc"} }
