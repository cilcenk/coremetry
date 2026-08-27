package chstore

// trace_backfill_test.go — v0.10.103 sihirbaz sözleşmeleri.

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

func flatWSBF(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// Backfill SELECT'i MV DDL'inin state listesinin AYNASI olmak zorunda:
// ayrışırsa geri doldurulan günler farklı şekle sahip olur ve hiçbir
// tip denetimi görmez. Her parça DDL metninde aranır (whitespace-düz).
func TestTraceBackfillMirrorsMVStates(t *testing.T) {
	b, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	// DDL bloğunu daralt: MV tanımından sonraki ilk `FROM spans`e kadar.
	src := string(b)
	i := strings.Index(src, "CREATE MATERIALIZED VIEW IF NOT EXISTS trace_summary_5m")
	if i < 0 {
		t.Fatal("MV DDL bulunamadı")
	}
	j := strings.Index(src[i:], "FROM spans")
	ddl := flatWSBF(src[i : i+j])
	// AS alias'larını düş (DDL'de var, INSERT SELECT'te yok).
	ddl = regexp.MustCompile(`AS \w+`).ReplaceAllString(ddl, "")
	ddl = flatWSBF(ddl)

	frags := TraceBackfillStateFragments()
	if len(frags) != 8 {
		t.Fatalf("state parçası %d, 8 bekleniyordu (DDL'e kolon mu eklendi? aynayı da güncelle): %v",
			len(frags), frags)
	}
	for _, f := range frags {
		if !strings.Contains(ddl, flatWSBF(f)) {
			t.Errorf("backfill parçası DDL'de yok — ayna ayrışmış:\n  %s", f)
		}
	}
}

// İdempotens tasarımı: gün koşusu ÖNCE partition düşürür, SONRA kurar
// (AggregatingMergeTree'de çifte-insert sayı şişirir). Sıra kaynak
// metinden pinlenir; ayrıca INSERT'in kolon listesi DDL kolon adlarını
// birebir taşımalı.
func TestTraceBackfillDropsBeforeInsert(t *testing.T) {
	b, err := os.ReadFile("trace_backfill.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	iDrop := strings.Index(src, "DROP PARTITION")
	iIns := strings.Index(src, "INSERT INTO trace_summary_5m")
	if iDrop < 0 || iIns < 0 || iDrop > iIns {
		t.Fatalf("sıra bozuk: DROP@%d INSERT@%d — önce partition düşmeli", iDrop, iIns)
	}
	// KISMİ-YAZIM pini (canlı provada ölçüldü: 159 sonrası MV'de kısmi
	// satır kaldı): her merdiven denemesi TEMİZ zeminde başlamalı —
	// düşürme merdiven DÖNGÜSÜNÜN İÇİNDE olmalı, dışında değil.
	iLadder := strings.Index(src, "range backfillSliceLadder")
	iDropCall := strings.Index(src, "s.dropTraceDayPartition(ctx, day)")
	if iLadder < 0 || iDropCall < 0 || iDropCall < iLadder {
		t.Fatal("yeniden-düşürme merdiven döngüsünün içinde değil — kısmi yazım üstüne ekleme çifte sayım üretir")
	}
	for _, col := range []string{"root_service_state", "entry_route_state", "entry_service_state"} {
		if !strings.Contains(src, col) {
			t.Errorf("INSERT kolon listesinde %s yok", col)
		}
	}
	if !strings.Contains(src, "distributed_product_mode = 'global'") {
		t.Error("backfill SELECT'i product_mode=global taşımıyor — dağıtıkta 288 sınıfı")
	}
}

func TestTraceBackfillDayValidation(t *testing.T) {
	s := &Store{}
	if err := s.TraceBackfillDayRun(nil, "26-08-2026"); err == nil {
		t.Error("bozuk gün biçimi kabul edildi")
	}
	if err := s.TraceBackfillDayRun(nil, "2026-08-26'; DROP TABLE spans;--"); err == nil {
		t.Error("enjeksiyon denemesi tarih doğrulamasından geçti")
	}
}

// v0.10.104 — merdiveni indiren hata sınıfı İKİ kaynak türünü de
// kapsamalı (prod ilk koşusu 241'de durdu): 159 VE 241 iner; yapısal
// hata inmez. Her iki dal da sürülür ([[feedback-unit-mixing-needs-
// both-branches]] sınıfı: tek dalı test etmek öbürünü görünmez kılar).
func TestBackfillLadderDescendsOnResourceErrors(t *testing.T) {
	for _, tc := range []struct {
		msg  string
		want bool
	}{
		{"code: 159, message: Timeout exceeded: elapsed 25087 ms", true},
		{"code: 241, message: Query memory limit exceeded: would use 3.74 GiB", true},
		{"context deadline exceeded", true},
		{"code: 62, message: Syntax error", false},
		{"code: 47, message: Unknown identifier entry_service_state", false},
	} {
		if got := isBackfillTimeout(fmt.Errorf("%s", tc.msg)); got != tc.want {
			t.Errorf("isBackfillTimeout(%q)=%v, istenen %v", tc.msg, got, tc.want)
		}
	}
}

// v0.10.106 — preflight MV'nin GİZLİ iç tablosunu saymak zorunda:
// TO'suz MV'nin system.parts satırı yoktur; MV adını saymak mv_rows'u
// ebediyen 0 gösterir ve sihirbaz dolu günleri de yıkıcı yeniden-doluma
// önerir (lokal smoke prod-öncesi yakaladı). Kaynak pini: çözüm
// concat('.inner_id.' üzerinden ve parts eşlemesi IN listesiyle.
func TestPreflightCountsHiddenInnerTable(t *testing.T) {
	b, err := os.ReadFile("trace_backfill.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for needle, why := range map[string]string{
		"concat('.inner_id.', toString(uuid))": "iç tablo adı uuid'den çözülmüyor",
		"traceMVInnerNames(ctx)":               "preflight iç-ad çözümünü çağırmıyor",
		"table IN (":                           "parts eşlemesi iç-ad listesine bakmıyor",
	} {
		if !strings.Contains(src, needle) {
			t.Errorf("%s (aranan: %q)", why, needle)
		}
	}
	// MV adının DOĞRUDAN parts'ta sayılması yalnız dürüst-düşüş dalında
	// kalabilir; sumIf'in MV adıyla eşleşen bir sabiti KALMAMALI.
	if strings.Contains(src, "sumIf(rows, table = 'trace_summary_5m") {
		t.Error("parts sayımı hâlâ MV adına sabitlenmiş — mv_rows 0 yalanı geri gelir")
	}
}
