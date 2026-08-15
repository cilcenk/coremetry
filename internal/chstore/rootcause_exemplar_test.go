package chstore

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// v0.9.1057 (Faz 1.2) regresyon pini — exemplar_trace_id kolonu üç SQL
// noktasında da (INSERT + iki SELECT) taşınmak zorunda. Eksik kalan bir
// SELECT, scan kolon sayısı uyuşmazlığıyla RUNTIME'da patlar — derleyici
// görmez. Ayrıca DDL'de hem CREATE hem boot-ALTER olmalı (v0.9.516
// deep_evidence deseni: taze kurulum CREATE'ten, mevcut kurulum
// ALTER'dan alır).
func TestHypothesisExemplarColumnEverywhere(t *testing.T) {
	src, err := os.ReadFile("rootcause_hypothesis.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(src), "exemplar_trace_id"); n < 3 {
		t.Fatalf("rootcause_hypothesis.go exemplar_trace_id %d yerde, en az 3 olmalı (INSERT + 2 SELECT)", n)
	}
	ddl, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ddl), "exemplar_trace_id String    DEFAULT '',  -- v0.9.1057") {
		t.Fatal("CREATE TABLE exemplar_trace_id taşımıyor")
	}
	if !strings.Contains(string(ddl), "ADD COLUMN IF NOT EXISTS exemplar_trace_id") {
		t.Fatal("boot-ALTER exemplar_trace_id yok — mevcut kurulumlar kolonu alamaz")
	}
}

// JSON sözleşmesi: boş exemplar alan basmaz (omitempty), dolu olan
// exemplarTraceId anahtarıyla çıkar — MCP/HTTP tüketicileri bu ada
// bağlanacak.
func TestHypothesisExemplarJSONShape(t *testing.T) {
	var empty RootCauseHypothesis
	b, _ := json.Marshal(empty)
	if strings.Contains(string(b), "exemplarTraceId") {
		t.Fatalf("boş exemplar JSON'a sızdı: %s", b)
	}
	full := RootCauseHypothesis{ExemplarTraceID: "abc123"}
	b, _ = json.Marshal(full)
	if !strings.Contains(string(b), `"exemplarTraceId":"abc123"`) {
		t.Fatalf("exemplarTraceId anahtarı yanlış: %s", b)
	}
}
