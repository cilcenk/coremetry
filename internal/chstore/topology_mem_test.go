package chstore

// v0.9.1190 regresyon testleri — topoloji JOIN pass'lerinin bellek
// disiplini (operatör-bildirimli prod 241).
//
// Canlı arıza: cross-service pass remote shard'da öldü —
//
//	Query memory limit exceeded: would use 8.00 GiB
//	(attempt to allocate chunk of 2.50 GiB), maximum: 7.45 GiB
//	GraceHashJoin → FillingRightJoinSide, While executing Remote
//
// 7.45 GiB bizim kendi tavanımız (heavyScanMemory = 8e9). Kusur, spill
// eşiğinin (max_bytes_in_join = 4e9) tavana fazla yakın olmasıydı: join
// 3.7 GiB'a kadar büyümeye "haklı" sayılıyor, ama ~2 GiB'lık tablonun
// bir sonraki RESIZE'ı tek seferde ~2.5 GiB isteyince toplam tavan
// deliniyor — grace'in sigortası hiç ateşlenemeden. query_memory.go'nun
// kendi cümlesi: "a spill threshold at or above the cap can never fire".
//
// Sonuç ürün diliyle: topology_edges_5m o kovayı YAZAMIYOR, servis
// haritası prod'da tam yoğun saat kovalarında delik. Retry yok — düşen
// kova kalıcı boşluk.

import (
	"os"
	"strings"
	"testing"
)

// TestTopoJoinMemBudgetRatio — EŞİK/TAVAN oranı. Mutlak değeri pinlemek
// yetmez (v0.9.1188'in dersi: sabite çakılı test, sabit değişince ya
// kırılır ya da anlamsızlaşır); korunması gereken şey ORAN: spill eşiği
// tavanın en fazla ÇEYREĞİ. Bu payla en kötü tek resize bile (~eşik
// boyunda bir chunk) sol blokların + aggregation state'lerinin üstüne
// binse dahi tavana çarpmaz.
func TestTopoJoinMemBudgetRatio(t *testing.T) {
	if topoJoinMemBudget*4 > heavyScanMemory {
		t.Fatalf("max_bytes_in_join (%d) tavanın (%d) çeyreğini aşıyor — spill "+
			"tavana yaklaşınca grace'in sigortası ateşlenemeden query ölür "+
			"(prod 241, v0.9.1190)", topoJoinMemBudget, heavyScanMemory)
	}
	if topoJoinMemBudget <= 0 {
		t.Fatal("eşik pozitif olmalı")
	}
}

// TestTopoJoinMemSettingsShape — üretilen SETTINGS parçası. nil-Store
// üzerinden çağrılabilir (queryMemSetting fail-open) — gerçek üreticiyi
// koşturuyoruz, kaynak metnini taramıyoruz.
func TestTopoJoinMemSettingsShape(t *testing.T) {
	var s *Store
	// nil receiver: queryMemSetting nil'de istenen değeri aynen döner.
	// Yöntem pointer-receiver'lı olduğu için panic yok; panic olursa bu
	// test zaten onu yakalar — sözleşmenin parçası.
	got := s.topoJoinMemSettings()
	for _, want := range []string{
		"join_algorithm = 'grace_hash'",
		"grace_hash_join_initial_buckets = 16",
		"max_bytes_in_join = 1500000000",
		"max_memory_usage = 8000000000",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("SETTINGS parçasında %q yok:\n%s", want, got)
		}
	}
}

// TestTopologyWritePassesShareMemSettings — üç JOIN pass'i TEK kaynağı
// kullanmalı. Bu vakada olan tam şuydu: aggregator ilk hatada erken
// döndüğü için cross-service düşünce op-bucket hiç koşmamıştı; ilki
// düzelip diğerlerinde eski üçlü kalsa, aynı 241 bir sonraki pass'ten
// gelirdi. Sayım ==3: yeni bir JOIN pass'i eklenirse bu test onu da
// buraya bağlamaya zorlar.
func TestTopologyWritePassesShareMemSettings(t *testing.T) {
	b, err := os.ReadFile("topology.go")
	if err != nil {
		t.Fatalf("topology.go okunamadı: %v", err)
	}
	src := string(b)
	if n := strings.Count(src, "s.topoJoinMemSettings()"); n != 3 {
		t.Errorf("topoJoinMemSettings %d yerde çağrılıyor, beklenen 3 "+
			"(cross-service · op bucket · root flows) — yeni bir JOIN pass'i "+
			"eklendiyse aynı disipline bağla", n)
	}
	// Eski üçlünün hiçbir parçası geri gelemez.
	if strings.Contains(src, "max_bytes_in_join = 4000000000") {
		t.Error("eski 4e9 spill eşiği geri gelmiş — v0.9.1190'ın düzelttiği kusur")
	}
}

// TestTopologyWritersUseTDigest — yazıcı pass'lerde quantileExact
// KALAMAZ. Exact, grup başına TÜM süreleri bellekte tutar; prod
// ölçeğinde sıcak bir kenarın 5 dakikası milyonlarca değer demek ve
// hepsi JOIN'le AYNI 8 GB zarfın içinde. /clickhouse-schema kuralı:
// ~1M satır üstünde TDigest (≤%2 hata). Üç pass'in üçü de değişti;
// dosya-geneli sıfır sayımı, dördüncü bir pass'in de Exact ile
// açılamamasını garanti eder.
func TestTopologyWritersUseTDigest(t *testing.T) {
	b, err := os.ReadFile("topology.go")
	if err != nil {
		t.Fatalf("topology.go okunamadı: %v", err)
	}
	// Yorum satırları HARİÇ (trace_window_test.go kapısının deseni):
	// kararı ANLATAN satır ("quantileExact'ten TDigest'e") kapıyı
	// kırmamalı — sözleşmeyi açıklayan yorumun sözleşme testini bozması
	// saçma bir kırılganlık olurdu.
	tdigest := 0
	for i, ln := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "--") {
			continue
		}
		if strings.Contains(ln, "quantileExact") {
			t.Errorf("topology.go:%d — quantileExact: yazıcı pass'ler TDigest "+
				"kullanmalı (grup başına tüm değerleri bellekte tutmak, "+
				"v0.9.1190 prod 241'inin ikinci bombasıydı)", i+1)
		}
		tdigest += strings.Count(ln, "quantileTDigest(0.99)")
	}
	if tdigest != 3 {
		t.Errorf("quantileTDigest(0.99) %d yerde, beklenen 3", tdigest)
	}
}
