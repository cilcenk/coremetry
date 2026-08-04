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
	// Bind sayısı, anahtarın SQL'de kaç kez geçtiğiyle BİREBİR olmalı.
	// Sabit bir sayı yazmak yapıyı çivilerdi, sözleşmeyi değil — v0.9.634'te
	// öyle yazmıştım ve v0.9.635 düşüş yolunu ekleyince (değer ifadesi 3 kez
	// geçiyor) test davranış bozulmadığı hâlde kırıldı.
	want := strings.Count(sql, "indexOf(attr_keys, ?)") + strings.Count(sql, "indexOf(res_keys, ?)")
	n := 0
	for _, a := range args {
		if s, ok := a.(string); ok && s == "k8s.namespace.name" {
			n++
		}
	}
	if n != want {
		t.Fatalf("bind sayısı SQL'deki geçiş sayısıyla uyuşmalı: bind %d, geçiş %d\n%s", n, want, sql)
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

// v0.9.635 — kök-yalnız kovalama SESSİZ VERİ KAYBIYDI.
//
// Kök span anahtarı taşımıyorsa group_key='' oluyor ve trace
// `HAVING group_key != ''` ile TAMAMEN eleniyordu — yanlış kovaya
// düşmüyor, yok oluyordu. Hiçbir trace'in kökünde yoksa tablo komple
// boş dönüyor ve operatör "bu attribute yok" diye okuyordu.
//
// Tipik şekil tam bunu tetikliyor: kök span gateway'in generic HTTP
// span'i, iş kodu bir alt serviste set ediliyor.
//
// DAVRANIŞ GERÇEK ClickHouse 24.8'de DOĞRULANDI (üç trace):
//   A kök 030101 taşıyor              → eski ✅  yeni ✅ 030101
//   B kök boş, orta 020202(t+1)/010101(t+2) → eski ❌ ELENİYOR  yeni ✅ 020202
//   C hiçbir span taşımıyor           → eski eleniyor  yeni eleniyor
// argMin en ERKEN span'i seçiyor (010101 değil 020202).

func TestAggregateFallsBackBeyondRootSpan(t *testing.T) {
	sql, _, args := aggregateGroupExpr(AggregateFilter{GroupAttr: "channel_code"})

	if !strings.Contains(sql, "argMinIf(") {
		t.Fatalf("kök taşımıyorsa düşüş yolu olmalı:\n%s", sql)
	}
	// DETERMİNİSTİK olmalı: anyIf ile aynı trace iki koşuda farklı
	// kovaya düşebilirdi.
	if strings.Contains(sql, "anyMinIf") || !strings.Contains(sql, "argMinIf(") {
		t.Fatalf("düşüş yolu argMin olmalı (deterministik):\n%s", sql)
	}
	// Kök YİNE DE öncelikli — sıralama bozulursa semantik değişir.
	rootIdx := strings.Index(sql, "anyIf(")
	fallIdx := strings.Index(sql, "argMinIf(")
	if rootIdx == -1 || fallIdx == -1 || rootIdx > fallIdx {
		t.Fatalf("kök anyIf, argMin düşüşünden ÖNCE gelmeli:\n%s", sql)
	}
	// Dizi yolunda değer ifadesi ÜÇ kez geçiyor → anahtar 6 kez bind.
	n := 0
	for _, a := range args {
		if s, ok := a.(string); ok && s == "channel_code" {
			n++
		}
	}
	if n != 6 {
		t.Fatalf("bind sayısı SQL'deki geçiş sayısıyla uyuşmalı, alınan %d (beklenen 6): %v", n, args)
	}
}

// Terfi kolonu dalında da düşüş yolu olmalı — kolon bind almıyor ama
// aynı kök-öncelikli/argMin yapısını kurmalı.
func TestAggregatePromotedBranchAlsoFallsBack(t *testing.T) {
	withPromotedCols(t, map[string]string{"channel_code": "attr_channel_code"})

	sql, _, args := aggregateGroupExpr(AggregateFilter{GroupAttr: "channel_code"})
	if !strings.Contains(sql, "argMinIf(attr_channel_code, time,") {
		t.Fatalf("terfi kolonu dalı da düşüş yolu kurmalı:\n%s", sql)
	}
	if len(args) != 0 {
		t.Fatalf("kolon yolunda bind olmamalı: %v", args)
	}
}

// Yerleşik gruplamalar (operation/service/kind…) kök semantiğinde
// KALMALI: oradaki değerler her span'de var, düşüş yolu anlamsız ve
// gruplamayı bozar.
func TestAggregateBuiltinsKeepRootOnly(t *testing.T) {
	for _, g := range []string{"operation", "service", "kind", "http_route"} {
		sql, _, _ := aggregateGroupExpr(AggregateFilter{GroupBy: g})
		if strings.Contains(sql, "argMinIf(") {
			t.Errorf("%s: yerleşik gruplama kök semantiğinde kalmalı:\n%s", g, sql)
		}
	}
}
