package chstore

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// v0.9.516 — SELECT kolon sayısı ile rows.Scan argüman sayısı EŞİT olmalı.
//
// Bu bug sınıfı derlemede yakalanmaz: Scan variadic'tir, uyuşmazlık ancak
// ilk okumada çalışma zamanı hatası verir. deep_evidence kolonu eklenirken
// tam bu oldu — iki SELECT güncellendi ama toplu okumanın Scan'i 9
// argümanda kaldı. Elle yakalandı; bir dahakine test yakalasın.
//
// Kaynak tarayan test (conn_strategy_test.go deseninin aynısı): DB
// gerektirmez, çalışma zamanı sözleşmesini metinden pinler.
func TestRootCauseSelectScanArity(t *testing.T) {
	b, err := os.ReadFile("rootcause_hypothesis.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)

	selectRe := regexp.MustCompile(`(?s)SELECT (anchor_kind.*?)\n\s*FROM root_cause_hypotheses`)
	scanRe := regexp.MustCompile(`(?s)Scan\(\s*(&h\.AnchorKind.*?)\)\s*;\s*err != nil`)

	selects := selectRe.FindAllStringSubmatch(src, -1)
	scans := scanRe.FindAllStringSubmatch(src, -1)

	if len(selects) == 0 || len(scans) == 0 {
		t.Fatalf("okuma yolları bulunamadı (select=%d scan=%d) — test deseni kodla uyumsuz",
			len(selects), len(scans))
	}
	if len(selects) != len(scans) {
		t.Fatalf("SELECT sayısı (%d) ile Scan sayısı (%d) tutmuyor — bir okuma yolu eksik",
			len(selects), len(scans))
	}

	countFields := func(s string) int {
		n := 0
		for _, part := range strings.Split(s, ",") {
			if strings.TrimSpace(part) != "" {
				n++
			}
		}
		return n
	}

	for i := range selects {
		cols := countFields(selects[i][1])
		args := countFields(scans[i][1])
		if cols != args {
			t.Errorf("okuma yolu #%d: SELECT %d kolon, Scan %d argüman — çalışma zamanında patlar\nSELECT: %s\nScan:   %s",
				i+1, cols, args,
				strings.Join(strings.Fields(selects[i][1]), " "),
				strings.Join(strings.Fields(scans[i][1]), " "))
		}
	}

	// deep_evidence HER okuma yolunda olmalı — birinde unutulursa o yol
	// sessizce izsiz hipotez döndürür ve explainer sığ prompt üretir.
	if got := strings.Count(src, "deep_evidence"); got < len(selects)+1 {
		t.Errorf("deep_evidence %d kez geçiyor; %d okuma yolu + 1 INSERT bekleniyor", got, len(selects))
	}
}
