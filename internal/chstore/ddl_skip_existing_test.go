// v0.9.607 — var olan nesne için DDL göndermeme kararının testi.
//
// Testin İKİ yönü var ve ikincisi daha önemli: doğru şeyin elendiği
// KADAR, yanlış şeyin ELENMEDİĞİ de. Fazladan bir no-op zararsız;
// atlanmış bir ALTER, eksik kolonla çalışan bir kurulum demektir.
package chstore

import (
	"reflect"
	"testing"
)

func TestDDLCreatesObject(t *testing.T) {
	ok := map[string]string{
		"düz tablo":            "CREATE TABLE IF NOT EXISTS spans (a String)",
		"backtick":             "CREATE TABLE IF NOT EXISTS `spans` (a String)",
		"materialized view":    "CREATE MATERIALIZED VIEW IF NOT EXISTS trace_summary_5m ENGINE = X AS SELECT 1",
		"görünüm":              "CREATE VIEW IF NOT EXISTS v_x AS SELECT 1",
		"baştaki boşluk/satır": "\n\t\tCREATE TABLE IF NOT EXISTS ai_calls (a String)",
		"küçük harf":           "create table if not exists ai_feedback (a String)",
	}
	want := map[string]string{
		"düz tablo": "spans", "backtick": "spans",
		"materialized view": "trace_summary_5m", "görünüm": "v_x",
		"baştaki boşluk/satır": "ai_calls", "küçük harf": "ai_feedback",
	}
	for name, sql := range ok {
		t.Run(name, func(t *testing.T) {
			got, isCreate := ddlCreatesObject(sql)
			if !isCreate {
				t.Fatalf("CREATE tanınmadı: %q", sql)
			}
			if got != want[name] {
				t.Errorf("nesne adı %q, %q bekleniyordu", got, want[name])
			}
		})
	}
}

// TestDDLNotSkippable — elenmeye UYGUN OLMAYANLAR.
//
// Bu testin kırılması, yükseltme yolunun sessizce atlanması demektir:
// ALTER'lar kolon ekler, DROP'lar ölü nesneyi temizler, IF NOT
// EXISTS'siz CREATE var olan nesnede HATA verir (yani no-op değildir).
func TestDDLNotSkippable(t *testing.T) {
	for name, sql := range map[string]string{
		"ALTER ADD COLUMN":     "ALTER TABLE spans ADD COLUMN IF NOT EXISTS x String",
		"ALTER MODIFY COLUMN":  "ALTER TABLE spans MODIFY COLUMN attr_values Array(String)",
		"DROP TABLE":           "DROP TABLE IF EXISTS feedbacks",
		"DROP VIEW":            "DROP VIEW IF EXISTS operation_group_summary_5m",
		"IF NOT EXISTS'siz":    "CREATE TABLE spans (a String)",
		"IF NOT EXISTS'siz MV": "CREATE MATERIALIZED VIEW mv_x AS SELECT 1",
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := ddlCreatesObject(sql); ok {
				t.Errorf("elenebilir sayıldı: %q\n\n"+
					"Bu ifadenin var olan nesnede ETKİSİ VAR — atlanması "+
					"yükseltme yolunu sessizce bozar.", sql)
			}
		})
	}
}

func TestPlanDeclarativeDDL(t *testing.T) {
	stmts := []string{
		"CREATE TABLE IF NOT EXISTS spans (a String)",               // var → elenir
		"CREATE TABLE IF NOT EXISTS yeni_tablo (a String)",          // yok → gider
		"ALTER TABLE spans ADD COLUMN IF NOT EXISTS x String",       // ALTER → hep gider
		"CREATE MATERIALIZED VIEW IF NOT EXISTS mv_var AS SELECT 1", // var → elenir
		"DROP TABLE IF EXISTS feedbacks",                            // DROP → hep gider
	}
	existing := map[string]bool{"spans": true, "mv_var": true}

	send, skipped := planDeclarativeDDL(stmts, existing)
	if skipped != 2 {
		t.Errorf("elenen %d, 2 bekleniyordu", skipped)
	}
	want := []string{
		"CREATE TABLE IF NOT EXISTS yeni_tablo (a String)",
		"ALTER TABLE spans ADD COLUMN IF NOT EXISTS x String",
		"DROP TABLE IF EXISTS feedbacks",
	}
	if !reflect.DeepEqual(send, want) {
		t.Errorf("gönderilecekler yanlış:\n got: %v\nwant: %v", send, want)
	}
}

// TestFreshInstallSendsEverything — TAZE kurulumda davranış AYNI.
//
// Hiçbir nesne yoksa hiçbiri elenmemeli. Bu, değişikliğin ilk kurulumu
// bozmadığının garantisi.
func TestFreshInstallSendsEverything(t *testing.T) {
	stmts := []string{
		"CREATE TABLE IF NOT EXISTS spans (a String)",
		"CREATE MATERIALIZED VIEW IF NOT EXISTS mv AS SELECT 1",
	}
	send, skipped := planDeclarativeDDL(stmts, map[string]bool{})
	if skipped != 0 || len(send) != 2 {
		t.Errorf("taze kurulumda %d ifade elendi — hiçbiri elenmemeliydi", skipped)
	}
}

// TestProbeFailureSendsEverything — nesne listesi okunamazsa TÜM DDL
// gönderilir. Emin olamadığımızda göndermek doğru taraf: fazladan bir
// no-op zararsız, eksik bir tablo değil.
func TestProbeFailureSendsEverything(t *testing.T) {
	// existingObjects hata hâlinde boş küme döner; planlayıcı o
	// durumda hiçbir şey elememeli.
	send, skipped := planDeclarativeDDL(
		[]string{"CREATE TABLE IF NOT EXISTS spans (a String)"}, nil)
	if skipped != 0 || len(send) != 1 {
		t.Error("boş/nil mevcut-nesne kümesinde eleme yapıldı — probe hatasında " +
			"davranış bugünküyle aynı kalmalı")
	}
}

// ── v0.9.608: ADD COLUMN elemesi ─────────────────────────────────────

func TestDDLAddsColumn(t *testing.T) {
	tbl, col, ok := ddlAddsColumn("ALTER TABLE spans ADD COLUMN IF NOT EXISTS cluster String")
	if !ok || tbl != "spans" || col != "cluster" {
		t.Errorf("ayrıştırma yanlış: %q/%q ok=%v", tbl, col, ok)
	}
}

// TestAlterNotSkippable — ELENMEMESİ gerekenler.
//
// Bu testin kırılması sessiz ve kalıcı hasar demek: uygulanmamış bir
// tip/codec değişikliği hiçbir yerde hata vermez, sadece yanlış
// davranır.
func TestAlterNotSkippable(t *testing.T) {
	for name, sql := range map[string]string{
		"MODIFY COLUMN":     "ALTER TABLE spans MODIFY COLUMN attr_values Array(String) CODEC(ZSTD(3))",
		"MODIFY TTL":        "ALTER TABLE trace_snapshots MODIFY TTL toDate(created_at) + INTERVAL 7 DAY",
		"DELETE":            "ALTER TABLE system_settings DELETE WHERE key = 'sampling'",
		"ADD INDEX":         "ALTER TABLE spans ADD INDEX IF NOT EXISTS idx_x x TYPE set(0) GRANULARITY 4",
		"IF NOT EXISTS'siz": "ALTER TABLE spans ADD COLUMN cluster String",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, ok := ddlAddsColumn(sql); ok {
				t.Errorf("elenebilir sayıldı: %q\n\nBu ifadenin etkisi VAR — "+
					"atlanması sessizce uygulanmamış bir şema değişikliği bırakır.", sql)
			}
		})
	}
}

func TestPlanAlterDDL(t *testing.T) {
	stmts := []string{
		"ALTER TABLE spans ADD COLUMN IF NOT EXISTS cluster String",    // var → elenir
		"ALTER TABLE spans ADD COLUMN IF NOT EXISTS yeni_kolon String", // yok → gider
		"ALTER TABLE spans MODIFY COLUMN attr_values Array(String)",    // hep gider
	}
	send, skipped := planAlterDDL(stmts, map[string]bool{"spans.cluster": true})
	if skipped != 1 || len(send) != 2 {
		t.Errorf("elenen %d (1 bekleniyordu), gönderilen %d (2)", skipped, len(send))
	}
}

// TestAlterProbeFailureSendsEverything — kolon listesi okunamazsa TÜM
// ALTER gönderilir; davranış bugünküyle aynı kalır.
func TestAlterProbeFailureSendsEverything(t *testing.T) {
	send, skipped := planAlterDDL(
		[]string{"ALTER TABLE spans ADD COLUMN IF NOT EXISTS x String"}, nil)
	if skipped != 0 || len(send) != 1 {
		t.Error("boş kolon kümesinde eleme yapıldı — probe hatasında davranış değişmemeli")
	}
}
