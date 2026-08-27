package chstore

// trace_backfill_test.go — v0.10.103 sihirbaz sözleşmeleri.

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
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
	iLadder := strings.Index(src, "range backfillLadderFor(") // v0.10.108: hacme-göre merdiven
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
	if err := s.TraceBackfillDayRun(nil, "26-08-2026", 0, nil); err == nil {
		t.Error("bozuk gün biçimi kabul edildi")
	}
	if err := s.TraceBackfillDayRun(nil, "2026-08-26'; DROP TABLE spans;--", 0, nil); err == nil {
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

// v0.10.107 — ham yolun rootOnly'si: servis-daraltılmış şekilde kök
// sorusu DARALTILMAMIŞ kaynaktan (MV üyeliği, GLOBAL) sorulmalı; span
// içi countIf servis süzgecinin ardında kökü göremez ve kökü başka
// serviste olan her trace sessizce düşer. İki dal da pinli
// ([[feedback-unit-mixing-needs-both-branches]]).
func TestRawRootOnlyLooksBeyondServiceFilter(t *testing.T) {
	b, err := os.ReadFile("repo.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "v0.10.107 — SERVİS-DARALTILMIŞ ham şekilde")
	if i < 0 {
		t.Fatal("ham rootOnly düzeltme bloğu yok")
	}
	block := src[i : i+1600]
	if !strings.Contains(block, "trace_id GLOBAL IN (") ||
		!strings.Contains(block, "argMaxIfMerge(root_service_state) != ''") {
		t.Error("daraltılmış dal MV üyeliğine (GLOBAL) bakmıyor")
	}
	if !strings.Contains(block, `countIf((parent_id = '' OR parent_id = '0000000000000000')`) {
		t.Error("daraltmasız dalın span-içi kök koşulu düşmüş — o dalda doğru ve ucuz olan buydu")
	}
	if !strings.Contains(block, "f.Service != \"\" || len(f.RequireServices) > 0") {
		t.Error("dallanma koşulu değişmiş — hangi şekil hangi kaynağa bakıyor belirsizleşir")
	}
}

// v0.10.108 — hacme-göre başlangıç basamağı ("yavaş gidiyor"): mahkûm
// basamaklar atlanır, bilinmeyen hacim tam merdivendir.
func TestBackfillLadderForVolume(t *testing.T) {
	d := func(v time.Duration) string { return v.String() }
	cases := []struct {
		rows  uint64
		first time.Duration
	}{
		{0, 24 * time.Hour},              // bilinmiyor → tam merdiven
		{100_000_000, 24 * time.Hour},    // küçük gün → tek atış
		{600_000_000, 6 * time.Hour},     // orta → 6h'den başla
		{4_000_000_000, time.Hour},       // büyük → 1h
		{8_000_000_000, 15 * time.Minute}, // prod günü → 15m
		{200_000_000_000, 5 * time.Minute}, // uç → taban
	}
	for _, tc := range cases {
		got := backfillLadderFor(tc.rows)
		if len(got) == 0 || got[0] != tc.first {
			t.Errorf("rows=%d → ilk basamak %s, istenen %s", tc.rows, d(got[0]), d(tc.first))
		}
	}
	// Merdivenin kuyruğu daima korunur (emniyet basamakları düşmez).
	if got := backfillLadderFor(8_000_000_000); got[len(got)-1] != 5*time.Minute {
		t.Error("alt emniyet basamağı düşmüş")
	}
}

// v0.10.110 — Operator-reported (test ortamı): bayat trace_summary_5m
// DROP'u 211 GB inner'a cascade edip code 359'la boot'u kilitledi.
// Her iki düşürme de hacim-guard'ı taşımak ZORUNDA.
func TestTraceDropStatementsCarryVolumeGuard(t *testing.T) {
	for _, f := range []string{"trace_backfill.go", "store.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, stmt := range []string{
			`DROP PARTITION '"+day+"'"`,
			`DROP TABLE IF EXISTS trace_summary_5m"+s.onCluster()+" SYNC"`,
		} {
			for i := 0; ; {
				j := strings.Index(src[i:], stmt)
				if j < 0 {
					break
				}
				i += j + len(stmt)
				// İfadenin devamında aynı satır zincirinde purgeGuard olmalı.
				tail := src[i:min(i+80, len(src))]
				if !strings.Contains(tail, "purgeGuard") {
					t.Errorf("%s: %q guard'sız — 359 sınıfı (50 GB drop sınırı)", f, stmt)
				}
			}
		}
	}
}

// v0.10.111 — 10.97 probe'u ham AggregateFunction kolonunu SELECT'liyordu;
// clickhouse-go o tipi çözemediğinden probe her kümede false kalıp
// "iframe kökü unknown" düzeltmesini ölü bırakıyordu (lokal pod logunda
// ölçüldü). Pin: probe metadata'dan (system.columns) sorar; hiçbir probe
// ham *_state kolonunu tele bindiremez.
func TestEntrySvcProbeChecksSchemaNotWire(t *testing.T) {
	b, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if strings.Contains(src, "SELECT entry_service_state FROM") {
		t.Error("probe ham state kolonunu SELECT'liyor — sürücü AggregateFunction çözemez, probe daima false")
	}
	if !strings.Contains(src, `table = 'trace_summary_5m' AND name = 'entry_service_state'`) {
		t.Error("entry_service_state varlık probe'u (system.columns) kayıp")
	}
	if m := regexp.MustCompile(`SELECT \w+_state FROM`).FindString(src); m != "" {
		t.Errorf("ham state kolonu tele biniyor: %q", m)
	}
}
