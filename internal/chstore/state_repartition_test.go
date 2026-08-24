// v0.9.1335 — Kural P1: problems + anomaly_events'te DEĞİŞKEN partition
// kolonu.
//
// Orijinal semptom (denetim §7.2 A1): her iki tablo da
// `PARTITION BY toDate(started_at)` + `ORDER BY id` taşıyordu. started_at
// ORDER BY'da değil ve yeniden yazımda değişebiliyor → aynı id ikinci bir
// gün-partition'ına düşüyor, ReplacingMergeTree'nin arka plan
// birleştirmesi partition sınırını aşamadığı için kopya ölümsüz kalıyor.
// Doğruluğu ayakta tutan tek şey `SELECT … FINAL`'in sorgu anında
// partition'lar arası birleştirmesi — yani bir SUNUCU AYARI.
//
// Bu dosya ÜÇ kapı kuruyor:
//
//  1. İki DDL'in artık PARTITION BY taşımadığını + dedup anahtarının
//     DEĞİŞMEDİĞİNİ pinler (dar regresyon). Genelleştirilmiş P1 taraması
//     ve sicil muhafızları partition_dedup_test.go'da.
//  2. Boot teşhisinin (statePartitionDriftMsg) saf davranışı.
//  3. DAVRANIŞIN KENDİSİ — canlı CH üzerinde, `do_not_merge_across_
//     partitions_select_final=1` AÇIKKEN. Varsayılan ayarla koşan bir
//     test hiçbir şey kanıtlamaz: kusuru gizleyen şey tam olarak o
//     varsayılan. Eski şekil NEGATİF KONTROL olarak yanında koşar —
//     onsuz yeşil sonuç "ayar hiçbir şey yapmıyor" ile ayırt edilemez.
package chstore

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"
)

// ── 1. DDL şekli ────────────────────────────────────────────────────────

func TestRepartitionedStateTablesHaveNoPartitionBy(t *testing.T) {
	tables := migrateDDLSlice(t, "tables")

	// POZİTİF KONTROL — liste boşsa aşağıdaki döngü hiç koşmaz ve test
	// "hiç ihlal yok" diye yeşil yanardı.
	if len(repartitionedStateTables) != 2 {
		t.Fatalf("repartitionedStateTables %d eleman — beklenen 2 (anomaly_events, problems)",
			len(repartitionedStateTables))
	}

	for _, name := range repartitionedStateTables {
		ddl := tableDDLByName(tables, name)
		if ddl == "" {
			t.Errorf("%s CREATE ifadesi `tables` diliminde bulunamadı — "+
				"tablo silindiyse repartitionedStateTables da güncellenmeli", name)
			continue
		}
		loc := replacingEngineRe.FindStringIndex(ddl)
		if loc == nil {
			t.Errorf("%s: ReplacingMergeTree motoru bulunamadı — tablo sınıf "+
				"değiştirdiyse bu testin gerekçesi yeniden düşünülmeli", name)
			continue
		}
		tail := ddl[loc[0]:]

		if m := partitionByRe.FindStringSubmatch(tail); m != nil {
			t.Errorf("%s yine PARTITION BY taşıyor (%q). started_at ORDER BY'da "+
				"DEĞİL ve yeniden yazımda değişebiliyor → aynı id'nin eski satırı "+
				"başka bir partition'da ÖLÜMSÜZ kopya olur (Kural P1, v0.9.1335).",
				name, strings.TrimSpace(m[1]))
		}

		// Dedup anahtarı DEĞİŞMEMELİ. started_at'i ORDER BY'a eklemek de
		// "düzeltme" gibi görünür ve genelleştirilmiş P1 taramasını
		// SUSTURUR — ama her started_at yeniden yazımını YENİ SATIR
		// yapar, yani bug'ı büyüterek gizler. O yüzden burada tam eşitlik.
		om := orderByRe.FindStringSubmatch(tail)
		if om == nil {
			t.Errorf("%s: ORDER BY ayrıştırılamadı", name)
			continue
		}
		if got := strings.Join(strings.Fields(om[1]), " "); got != "id" {
			t.Errorf("%s: ORDER BY %q, beklenen \"id\" — dedup anahtarı "+
				"MÜNHASIRAN id olmalı (started_at eklemek her yeniden yazımı "+
				"yeni satır yapar)", name, got)
		}
	}

	// anomaly_events'in 30 günlük TTL'i PARTITION BY düştüğü için artık
	// SATIR düzeyinde uygulanıyor ve tabloyu sınırlayan TEK şey o
	// (root_cause_hypotheses emsali). Kaybolursa tablo sınırsız büyür.
	ae := tableDDLByName(tables, "anomaly_events")
	if !strings.Contains(strings.ToUpper(ae), "TTL TODATE(STARTED_AT) + INTERVAL 30 DAY") {
		t.Error("anomaly_events'in 30 günlük TTL'i kayboldu — PARTITION BY " +
			"düştüğüne göre tabloyu sınırlayan TEK şey bu")
	}
	// problems'in TTL'i YOK ve bu değişmedi: partition düşmesi zaten
	// hiçbir şeyi temizlemiyordu (EnforceRetention bu tabloyu yönetmiyor).
	// Sessizce bir TTL eklenmesi operatör verisini silerdi.
	pr := tableDDLByName(tables, "problems")
	if strings.Contains(strings.ToUpper(pr), "TTL ") {
		t.Error("problems'e TTL eklenmiş — bu tablo operatör durumu taşıyor " +
			"(ack, assignee, incident bağları) ve tarihte hiç TTL'i olmadı; " +
			"eklemek sessiz veri silme olurdu")
	}
}

// ── 2. Boot teşhisi ─────────────────────────────────────────────────────

func TestStatePartitionDriftMsg(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stale   map[string]string
		wantSub []string
		wantNil bool
	}{
		{
			name:    "göç yapılmış — hiç satır yok",
			stale:   map[string]string{},
			wantNil: true,
		},
		{
			name:    "nil harita",
			stale:   nil,
			wantNil: true,
		},
		{
			name:    "tek tablo",
			stale:   map[string]string{"problems": "toDate(started_at)"},
			wantSub: []string{"problems (PARTITION BY toDate(started_at))", "0010_state_repartition.sql"},
		},
	} {
		got := statePartitionDriftMsg(tc.stale)
		if tc.wantNil {
			if got != "" {
				t.Errorf("%s: %q döndü, boş beklenirdi — 'her şey yolunda' "+
					"satırı log gürültüsüdür", tc.name, got)
			}
			continue
		}
		if got == "" {
			t.Errorf("%s: boş döndü, mesaj beklenirdi", tc.name)
			continue
		}
		for _, sub := range tc.wantSub {
			if !strings.Contains(got, sub) {
				t.Errorf("%s: mesajda %q yok:\n%s", tc.name, sub, got)
			}
		}
	}
}

// TestStatePartitionDriftMsgIsDeterministic — sıralama muhafızı.
//
// Ayrı bir test ve TEKRARLI: Go'nun harita gezinme sırası rastgele, yani
// `sort.Strings` silinse TEK bir koşuda %50 ihtimalle yine doğru sıra
// çıkar ve mutasyon ISIRMAZ. 32 tur, kaçırma ihtimalini 2^-32'ye indirir.
// (Bu, "kapı ısırmıyorsa mutasyon ölü değildir, kapı zayıftır" dersinin
// doğrudan uygulaması.)
func TestStatePartitionDriftMsgIsDeterministic(t *testing.T) {
	stale := map[string]string{
		"problems":       "toDate(started_at)",
		"anomaly_events": "toDate(started_at)",
	}
	const want = "anomaly_events (PARTITION BY toDate(started_at)), " +
		"problems (PARTITION BY toDate(started_at))"
	for i := 0; i < 32; i++ {
		got := statePartitionDriftMsg(stale)
		if !strings.Contains(got, want) {
			t.Fatalf("tur %d: mesaj sıralı değil — %q bekleniyordu:\n%s", i, want, got)
		}
	}
}

// TestMigrateCallsStatePartitionDriftWarning — TEŞHİSİN BAĞLI OLDUĞU kapı.
//
// statePartitionDriftMsg'in saf testleri fonksiyonun DOĞRU olduğunu
// kanıtlar, ÇAĞRILDIĞINI değil. Çağrı satırı silinirse eski şemada kalmış
// bir kurulumun bunu öğrenebileceği başka hiçbir kanal yok — göç
// operatörde olduğu için uyarı, "bu kurulum hâlâ bozuk" diyen TEK ses.
//
// AST ile (grep ile değil): yorum satırına alınmış bir çağrı grep'i
// tatmin ederdi. SINIRI açık: AST çağrının VAR olduğunu görür, ölü bir
// dalın içinde olmadığını değil.
func TestMigrateCallsStatePartitionDriftWarning(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "store.go", nil, 0)
	if err != nil {
		t.Fatalf("store.go ayrıştırılamadı: %v", err)
	}
	var inMigrate, called bool
	ast.Inspect(f, func(n ast.Node) bool {
		if fn, ok := n.(*ast.FuncDecl); ok {
			inMigrate = fn.Name.Name == "migrate"
			return true
		}
		if !inMigrate {
			return true
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok &&
			sel.Sel.Name == "warnStatePartitionDrift" {
			called = true
		}
		return true
	})
	if !called {
		t.Error("migrate() artık warnStatePartitionDrift çağırmıyor — eski " +
			"şemada kalmış bir kurulum bunu ÖĞRENEMEZ (göç operatörde, " +
			"v0.9.1335)")
	}
}

// ── 3. DAVRANIŞ — canlı CH, ayar AÇIK ───────────────────────────────────

// liveWriteStore — YAZAN canlı test için AYRI kapı.
//
// state_unify_live_test.go'nun `COREMETRY_LIVE_CH` kapısı SALT OKUNUR bir
// sözleşme taşıyor ve operatör onu prod'a doğrultabiliyor. Bu test iki
// scratch tablo KURAR ve DÜŞÜRÜR, yani aynı kapıyı paylaşamaz: ikinci bir
// env değişkeni, "-run Live" yazan birinin prod'a DDL göndermesini
// imkânsız kılar.
//
//	COREMETRY_LIVE_CH=localhost:9100 COREMETRY_LIVE_CH_WRITE=1 \
//	COREMETRY_LIVE_DB=coremetry go test ./internal/chstore/ -run LiveP1 -v
func liveWriteStore(t *testing.T) *Store {
	t.Helper()
	if os.Getenv("COREMETRY_LIVE_CH_WRITE") == "" {
		t.Skip("COREMETRY_LIVE_CH_WRITE ayarlı değil — scratch tablo kuran " +
			"canlı test atlanıyor")
	}
	s, _ := liveStore(t)
	return s
}

// TestLiveP1CrossPartitionDedup — Kural P1'in DAVRANIŞ kanıtı.
//
// İki scratch tablo, tek fark PARTITION BY. Her ikisine de AYNI id iki
// kez yazılır: farklı started_at (farklı gün) + artan version. Doğru
// cevap her iki tabloda da TEK satır ve TAZE değer.
//
//	varsayılan ayar        → ikisi de 1 satır  (kusur GİZLİ)
//	do_not_merge_…=1       → ESKİ 2 satır, YENİ 1 satır  (kusur GÖRÜNÜR)
//
// Eski şekil NEGATİF KONTROLDÜR: o kol 2 döndürmezse ayar ısırmıyor
// demektir ve yeni koldaki "1" hiçbir şey kanıtlamaz — test o durumda
// KENDİNİ geçersiz ilan eder.
func TestLiveP1CrossPartitionDedup(t *testing.T) {
	s := liveWriteStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const (
		oldTbl = "p1_probe_old_v1335"
		newTbl = "p1_probe_new_v1335"
	)
	ddl := func(name, partition string) string {
		return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id String, started_at DateTime64(9), payload String,
			version UInt64
		) ENGINE = ReplacingMergeTree(version) %s ORDER BY id`, name, partition)
	}
	drop := func(name string) {
		if err := s.conn.Exec(context.Background(),
			"DROP TABLE IF EXISTS "+name+" SYNC"); err != nil {
			t.Logf("scratch tablo %s düşürülemedi: %v", name, err)
		}
	}
	drop(oldTbl)
	drop(newTbl)
	defer drop(oldTbl)
	defer drop(newTbl)

	if err := s.conn.Exec(ctx, ddl(oldTbl, "PARTITION BY toDate(started_at)")); err != nil {
		t.Fatalf("eski şekilli scratch tablo kurulamadı: %v", err)
	}
	if err := s.conn.Exec(ctx, ddl(newTbl, "")); err != nil {
		t.Fatalf("yeni şekilli scratch tablo kurulamadı: %v", err)
	}

	// Aynı id, İKİ farklı gün, artan version. "stale" önce yazılır.
	day1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 1, 9, 12, 0, 0, 0, time.UTC)
	for _, tbl := range []string{oldTbl, newTbl} {
		b, err := s.conn.PrepareBatch(ctx,
			"INSERT INTO "+tbl+" (id, started_at, payload, version)")
		if err != nil {
			t.Fatalf("%s batch: %v", tbl, err)
		}
		if err := b.Append("anchor", day1, "STALE", uint64(1)); err != nil {
			t.Fatalf("%s append stale: %v", tbl, err)
		}
		if err := b.Append("anchor", day2, "FRESH", uint64(2)); err != nil {
			t.Fatalf("%s append fresh: %v", tbl, err)
		}
		if err := b.Send(); err != nil {
			t.Fatalf("%s send: %v", tbl, err)
		}
		// OPTIMIZE FINAL: arka plan birleştirmesinin şansını VER. Eski
		// şekilde yine de birleşmeyecek — iddianın tamamı bu.
		if err := s.conn.Exec(ctx, "OPTIMIZE TABLE "+tbl+" FINAL"); err != nil {
			t.Fatalf("%s optimize: %v", tbl, err)
		}
	}

	read := func(tbl string, noMerge bool) (int, string) {
		t.Helper()
		q := "SELECT count(), max(payload) FROM " + tbl + " FINAL WHERE id = 'anchor'"
		if noMerge {
			q += " SETTINGS do_not_merge_across_partitions_select_final = 1"
		}
		var n uint64
		var payload string
		if err := s.conn.QueryRow(ctx, q).Scan(&n, &payload); err != nil {
			t.Fatalf("%s okuma (noMerge=%v): %v", tbl, noMerge, err)
		}
		return int(n), payload
	}

	// Varsayılan ayar — kusur GİZLİ, iki kol da doğru görünür.
	if n, _ := read(oldTbl, false); n != 1 {
		t.Errorf("eski şekil varsayılan FINAL ile %d satır döndürdü, 1 beklenirdi "+
			"— bu kolun doğru görünmesi kusuru gizleyen şeyin ta kendisi", n)
	}

	// NEGATİF KONTROL: ayar açıkken eski şekil BÖLÜNMELİ. Bölünmezse
	// ayar ısırmıyordur ve aşağıdaki assert hiçbir şey kanıtlamaz.
	nOld, _ := read(oldTbl, true)
	if nOld < 2 {
		t.Fatalf("NEGATİF KONTROL DÜŞTÜ: eski (PARTITION BY'lı) şekil "+
			"do_not_merge_across_partitions_select_final=1 ile %d satır "+
			"döndürdü, ≥2 beklenirdi. Ayar bu CH sürümünde ısırmıyor — "+
			"testin YENİ koldaki sonucu geçersizdir, düzeltme kanıtlanmadı", nOld)
	}

	// ASIL İDDİA: yeni şekil aynı ayar altında TEK satır ve TAZE değer.
	nNew, payload := read(newTbl, true)
	if nNew != 1 {
		t.Errorf("yeni (partition'sız) şekil %d satır döndürdü, 1 beklenirdi — "+
			"partition yoksa partition-arası kopya İMKÂNSIZ olmalıydı", nNew)
	}
	if payload != "FRESH" {
		t.Errorf("yeni şekil %q döndürdü, \"FRESH\" beklenirdi — bayat satır "+
			"kazanıyor", payload)
	}
	t.Logf("eski şekil (ayar açık): %d satır · yeni şekil: %d satır (%s)",
		nOld, nNew, payload)
}
