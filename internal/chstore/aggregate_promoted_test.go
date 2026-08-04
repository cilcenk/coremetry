package chstore

import (
	"strings"
	"testing"
)

// v0.9.634 — /traces "Aggregated" sekmesi terfi etmiş attribute kolonuna
// HİÇ bakmıyordu.
//
// Haritayı danışan üç kardeşi vardı — traceExtrasProjection (gösterim),
// businessDimExpr (iş kırılımı), FilterExpr.sql (filtre, v0.9.622) — ve
// bu dal TEK istisnaydı. Sonuç: aynı sayfada aynı anahtar, filtrede
// indeksli LowCardinality kolondan, kırılımda dört şişman diziden
// okunuyordu.

func TestAggregateGroupAttrUsesPromotedColumn(t *testing.T) {
	withPromotedCols(t, map[string]string{"channel_code": "attr_channel_code"})

	sql, _, args := aggregateGroupExpr(AggregateFilter{GroupAttr: "channel_code"})
	if !strings.Contains(sql, "anyIf(attr_channel_code,") {
		t.Fatalf("terfi kolonuna yönlenmeliydi:\n%s", sql)
	}
	if strings.Contains(sql, "indexOf(attr_keys") {
		t.Fatalf("dizi aramasında kalmış — düzeltmenin tamamı bu:\n%s", sql)
	}
	// Anahtar artık bind edilmiyor.
	for _, a := range args {
		if s, ok := a.(string); ok && s == "channel_code" {
			t.Fatalf("kolon yolunda anahtar bind edilmemeli: %v", args)
		}
	}
}

// Harita ıskalarsa resource fallback'li coalesce OLDUĞU GİBİ kalmalı:
// terfi kolonu yalnız SPAN attribute'unu taşıyor, resource anahtarını
// kolona yönlendirmek sessizce boş kırılım verirdi.
func TestAggregateGroupAttrKeepsResourceFallback(t *testing.T) {
	sql, _, args := aggregateGroupExpr(AggregateFilter{GroupAttr: "k8s.namespace.name"})
	if !strings.Contains(sql, "res_values[indexOf(res_keys") {
		t.Fatalf("resource fallback korunmalı:\n%s", sql)
	}
	n := 0
	for _, a := range args {
		if s, ok := a.(string); ok && s == "k8s.namespace.name" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("dizi yolunda anahtar İKİ kez bind edilir (attr + res), sayı: %d", n)
	}
}

// GroupAttr KULLANICI girdisi → harf DUYARLI kalmalı. v0.9.624'ün
// ayrımı: kod içi sabit liste bir KAVRAMI ifade eder ve harf duyarsız
// çözülür; kullanıcının yazdığı anahtar bir ANAHTARDIR ve sessizce
// başkasına eşlenmemelidir.
func TestAggregateGroupAttrIsCaseSensitive(t *testing.T) {
	withPromotedCols(t, map[string]string{"channel_code": "attr_channel_code"})

	sql, _, _ := aggregateGroupExpr(AggregateFilter{GroupAttr: "CHANNEL_CODE"})
	if strings.Contains(sql, "attr_channel_code") {
		t.Fatalf("kullanıcı girdisi harf duyarsız eşlenmemeli:\n%s", sql)
	}
	if !strings.Contains(sql, "indexOf(attr_keys") {
		t.Fatalf("bilinmeyen yazım dizi yolunda kalmalı:\n%s", sql)
	}
}

// Kök-span semantiği (anyIf … parent_id kökü) HER dalda korunmalı.
// Bu BİLİNÇLİ bir tasarım ("Uptrace-style group traces by root
// attributes"), hata değil — terfi kolonu dalı onu düşürmemeli.
func TestAggregateKeepsRootSpanSemantics(t *testing.T) {
	withPromotedCols(t, map[string]string{"channel_code": "attr_channel_code"})

	for _, key := range []string{"channel_code", "some.other.attr"} {
		sql, _, _ := aggregateGroupExpr(AggregateFilter{GroupAttr: key})
		if !strings.Contains(sql, "parent_id = ''") {
			t.Errorf("%s: kök-span koşulu düşmüş:\n%s", key, sql)
		}
	}
}

// ── yardımcılar ───────────────────────────────────────────────────────

func withPromotedCols(t *testing.T, m map[string]string) {
	t.Helper()
	prev := promotedColsPtr.Load()
	registerTraceAttrMaterialized(m)
	t.Cleanup(func() { promotedColsPtr.Store(prev) })
}
