package chstore

// spanmetric_root_predicate_test.go — v0.10.611 regresyonu.
//
// Operatör-bildirimi (dogfood exception, coremetry-api): batch span-metrik
// sorgusu `parent_span_id = ''` yazıyordu; spans tablosunda kolon `parent_id`
// → prod CH code 47 "Unknown expression or function identifier
// `parent_span_id`". v0.10.484 /traces hacim şeridinin Root bayrağını böyle
// yazmış, saf seam olmadığı için hiçbir test görememişti (6 Eylül → 10 Eylül).
//
// Üç pin: (1) saf WHERE kurucusu RootOnly'de gerçek kolonu kullanır ve yalnız
// RootOnly'de; (2) histogram ile tablo (repo.go GetTraces) aynı kök yazımını
// paylaşır; (3) hiçbir SQL kuran kaynak dosya `parent_span_id` sözcüğünü
// yorum dışında anmaz — olmayan kolon adı tek yazımla da geri gelmesin.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestSpanMetricBatchWhereRootPredicate(t *testing.T) {
	from := time.Date(2026, 9, 10, 11, 20, 0, 0, time.UTC)
	to := from.Add(30 * time.Minute)
	base := SpanMetricBatchFilter{From: from, To: to, Filters: []FilterExpr{{Key: "service.name", Op: "=", Values: []string{"shop"}}}}

	root := base
	root.RootOnly = true
	wc := spanMetricBatchWhere(root, 0, 0)
	joined := strings.Join(wc.conds, " AND ")
	if !strings.Contains(joined, rootSpanPredicate) {
		t.Fatalf("RootOnly kök yüklemini eklemeli: %q", joined)
	}
	if strings.Contains(joined, "parent_span_id") {
		t.Fatalf("parent_span_id kolonu YOK (prod CH 47): %q", joined)
	}
	if !strings.Contains(rootSpanPredicate, "parent_id = ''") || !strings.Contains(rootSpanPredicate, "'0000000000000000'") {
		t.Fatalf("kök = boş YA DA sıfır id (iki tel biçimi): %q", rootSpanPredicate)
	}
	if !strings.Contains(joined, "time >= ?") || !strings.Contains(joined, "time <= ?") {
		t.Fatalf("zaman sınırı zorunlu: %q", joined)
	}

	plain := spanMetricBatchWhere(base, 0, 0)
	if strings.Contains(strings.Join(plain.conds, " AND "), "parent_id") {
		t.Fatalf("RootOnly kapalıyken kök yüklemi olmamalı: %v", plain.conds)
	}

	// winK > 0: tarama başlangıcı pencere kadar geriye çekilir.
	wide := spanMetricBatchWhere(base, 3, 60)
	if len(wide.args) == 0 {
		t.Fatal("from arg yok")
	}
	if got, ok := wide.args[0].(time.Time); !ok || !got.Equal(from.Add(-60*time.Second)) {
		t.Fatalf("winK>0'da scanFrom from−effWin olmalı: %v", wide.args[0])
	}
}

// Histogram (spanmetric) ile tablo (repo.go GetTraces RootOnly) aynı kök
// yazımını paylaşmalı — iki yüzey aynı kümeyi daraltır.
func TestRootPredicateSharedWithTraceTable(t *testing.T) {
	src, err := os.ReadFile("repo.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), rootSpanPredicate) {
		t.Fatalf("repo.go kök yüklemi %q yazımını içermeli (histogram ↔ tablo aynı küme)", rootSpanPredicate)
	}
}

// Olmayan kolon adı SQL kuran hiçbir kaynak dosyada yorum dışında geçmez.
// Yorumlar süzülür (kapı kendi açıklamasını ısırmasın — v0.9.1375 dersi).
func TestNoParentSpanIDColumnSpelling(t *testing.T) {
	lineComment := regexp.MustCompile(`(?m)//.*$`)
	for _, dir := range []string{".", "../api"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			code := lineComment.ReplaceAllString(string(raw), "")
			if strings.Contains(code, "parent_span_id") {
				t.Errorf("%s: `parent_span_id` yorum dışında geçiyor — spans kolonu `parent_id` (prod CH 47, v0.10.611)", f)
			}
		}
	}
}
