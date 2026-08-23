// v0.9.1302 — DDL elemesi DİLİMDEN BAĞIMSIZ.
//
// v0.9.1301 `alters` diliminde duran beş CREATE TABLE'ı `tables`'a taşıdı:
// o dilim yalnız planAlterDDL'den geçtiği için CREATE eleyicisi onlara hiç
// bakmıyor, tablolar aylardır yerinde olsa bile her boot'ta gönderiliyordu.
// AYNI TURDA AYNA KUSUR ölçüldü — `tables` diliminde de yedi
// `ALTER TABLE … ADD COLUMN IF NOT EXISTS` duruyor ve simetrik olarak ALTER
// eleyicisi onlara bakmıyordu.
//
// Yedisini taşımak kusuru kapatır, SINIFI kapatmaz: dilim adı zorlanmayan
// bir sözleşme olarak kalır. v0.9.1302 dilimi yük taşıyan olmaktan çıkardı —
// planDDL her iki eleyiciyi her iki dilime uyguluyor.
//
// Bu dosya iki yönü de pinler (tables'ta duran ALTER elenir / alters'ta
// duran CREATE elenir) ve elemenin EN TEHLİKELİ yönünü: var OLMAYAN bir
// nesne/kolon asla elenmemeli — taze kurulum ve yarım yükseltme oradan
// bozulurdu.
package chstore

import (
	"os"
	"strings"
	"testing"
)

// ── Saf planlayıcı: iki yön ──────────────────────────────────────────

// TestPlanDDLSkipsAddColumnWherverItSits — `tables` diliminin ŞEKLİ:
// CREATE'lerin arasına serpiştirilmiş ADD COLUMN'lar. v0.9.1301 öncesi
// bu ALTER'lar hiç elenmiyordu.
func TestPlanDDLSkipsAddColumnWherverItSits(t *testing.T) {
	tablesShaped := []string{
		"CREATE TABLE IF NOT EXISTS users (id String) ENGINE = MergeTree ORDER BY id",
		"ALTER TABLE users ADD COLUMN IF NOT EXISTS last_login_at DateTime64(9) DEFAULT toDateTime64(0, 9)",
		"CREATE TABLE IF NOT EXISTS dashboards (id String) ENGINE = MergeTree ORDER BY id",
		"ALTER TABLE dashboards ADD COLUMN IF NOT EXISTS variables String DEFAULT '[]' CODEC(ZSTD(3))",
	}
	existing := map[string]bool{"users": true, "dashboards": true}
	cols := map[string]bool{"users.last_login_at": true, "dashboards.variables": true}

	send, skippedObjects, skippedColumns := planDDL(tablesShaped, existing, cols)
	if skippedObjects != 2 {
		t.Errorf("nesne elemesi %d, beklenen 2 — CREATE kolu bozuldu", skippedObjects)
	}
	if skippedColumns != 2 {
		t.Errorf("kolon elemesi %d, beklenen 2 — `tables` diliminde duran ADD COLUMN "+
			"hâlâ elenmiyor (v0.9.1302 ayna kusuru geri geldi)", skippedColumns)
	}
	if len(send) != 0 {
		t.Errorf("her şey mevcutken %d ifade gönderildi, hiçbiri gönderilmemeliydi: %q", len(send), send)
	}
}

// TestPlanDDLSkipsCreateWhereverItSits — simetrik yön: `alters` diliminin
// ŞEKLİ. v0.9.1301'in taşıdığı beş ifade gibi bir CREATE buraya yeniden
// düşerse artık elenir; düzeltme yerleşime bağımlı değil.
func TestPlanDDLSkipsCreateWhereverItSits(t *testing.T) {
	altersShaped := []string{
		"ALTER TABLE spans ADD COLUMN IF NOT EXISTS cluster LowCardinality(String) DEFAULT ''",
		"CREATE TABLE IF NOT EXISTS log_templates (id String) ENGINE = MergeTree ORDER BY id",
		"ALTER TABLE spans ADD COLUMN IF NOT EXISTS op_group String DEFAULT ''",
	}
	existing := map[string]bool{"log_templates": true}
	cols := map[string]bool{"spans.cluster": true, "spans.op_group": true}

	send, skippedObjects, skippedColumns := planDDL(altersShaped, existing, cols)
	if skippedObjects != 1 {
		t.Errorf("nesne elemesi %d, beklenen 1 — `alters` diliminde duran CREATE elenmiyor", skippedObjects)
	}
	if skippedColumns != 2 {
		t.Errorf("kolon elemesi %d, beklenen 2", skippedColumns)
	}
	if len(send) != 0 {
		t.Errorf("%d ifade gönderildi, hiçbiri gönderilmemeliydi: %q", len(send), send)
	}
}

// ── Taze kurulum güvenliği: elemenin en tehlikeli yönü ───────────────

// TestPlanDDLFreshInstallSendsEverything — hiçbir nesne/kolon yokken
// HİÇBİR ŞEY elenmez ve SIRA korunur. Taze kurulumda şemayı kuran şey bu
// dilimlerin kendisi; burada yanlış bir eleme boş bir veritabanı demek.
func TestPlanDDLFreshInstallSendsEverything(t *testing.T) {
	stmts := []string{
		"CREATE TABLE IF NOT EXISTS users (id String) ENGINE = MergeTree ORDER BY id",
		"ALTER TABLE users ADD COLUMN IF NOT EXISTS last_login_at DateTime64(9)",
		"CREATE MATERIALIZED VIEW IF NOT EXISTS service_summary_5m TO x AS SELECT 1",
		"ALTER TABLE spans ADD COLUMN IF NOT EXISTS op_group String DEFAULT ''",
	}
	for _, tc := range []struct {
		name           string
		existing, cols map[string]bool
	}{
		{"iki küme de boş", map[string]bool{}, map[string]bool{}},
		{"iki küme de nil (okuma hatası kolu)", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			send, so, sc := planDDL(stmts, tc.existing, tc.cols)
			if so != 0 || sc != 0 {
				t.Fatalf("taze kurulumda %d nesne + %d kolon elendi — hiçbiri elenmemeliydi", so, sc)
			}
			if len(send) != len(stmts) {
				t.Fatalf("%d/%d ifade gönderildi", len(send), len(stmts))
			}
			for i := range stmts {
				if send[i] != stmts[i] {
					t.Errorf("send[%d] sırası/metni bozuldu:\n got %q\nwant %q", i, send[i], stmts[i])
				}
			}
		})
	}
}

// TestPlanDDLSendsWhatIsMissing — YARIM yükseltme: tablo var, kardeş
// kolonlar var, ama BU kolon yok. Eleme yalnız "zaten var"ı atlamalı;
// eksik olanı atlarsa kolon kalıcı olarak hiç inmez (sessiz, ve sonraki
// boot'ta da aynı karar verilir).
func TestPlanDDLSendsWhatIsMissing(t *testing.T) {
	stmts := []string{
		"CREATE TABLE IF NOT EXISTS users (id String) ENGINE = MergeTree ORDER BY id",
		"ALTER TABLE users ADD COLUMN IF NOT EXISTS last_login_at DateTime64(9)",
		"ALTER TABLE users ADD COLUMN IF NOT EXISTS ldap_username String DEFAULT ''",
		"CREATE TABLE IF NOT EXISTS ai_feedback (id String) ENGINE = MergeTree ORDER BY id",
		"ALTER TABLE ai_feedback ADD COLUMN IF NOT EXISTS comment String DEFAULT ''",
	}
	// users var, ai_feedback YOK. users.last_login_at var, ldap_username YOK.
	existing := map[string]bool{"users": true}
	cols := map[string]bool{"users.last_login_at": true}

	send, so, sc := planDDL(stmts, existing, cols)
	if so != 1 || sc != 1 {
		t.Fatalf("%d nesne + %d kolon elendi; beklenen 1 + 1", so, sc)
	}
	want := []string{
		"ALTER TABLE users ADD COLUMN IF NOT EXISTS ldap_username String DEFAULT ''",
		"CREATE TABLE IF NOT EXISTS ai_feedback (id String) ENGINE = MergeTree ORDER BY id",
		"ALTER TABLE ai_feedback ADD COLUMN IF NOT EXISTS comment String DEFAULT ''",
	}
	if len(send) != len(want) {
		t.Fatalf("%d ifade gönderildi, beklenen %d: %q", len(send), len(want), send)
	}
	for i := range want {
		if send[i] != want[i] {
			t.Errorf("send[%d]:\n got %q\nwant %q", i, send[i], want[i])
		}
	}
}

// TestPlanDDLPassesUnrecognisedThrough — tanınmayan ifade AYNEN gider.
// MODIFY COLUMN / ALTER DELETE / DROP no-op OLDUKLARI KANITLANAMAZ
// (v0.9.608 gerekçesi) ve `IF NOT EXISTS`siz bir CREATE var olan nesnede
// HATA verir — yani hiçbiri elenmeye uygun değil.
func TestPlanDDLPassesUnrecognisedThrough(t *testing.T) {
	stmts := []string{
		"DROP TABLE IF EXISTS feedbacks",
		"ALTER TABLE users MODIFY COLUMN last_login_at DateTime64(9)",
		"ALTER TABLE system_settings DELETE WHERE key = 'sampling'",
		"ALTER TABLE spans DROP COLUMN IF EXISTS attr_http_method",
		"CREATE TABLE users (id String) ENGINE = MergeTree ORDER BY id",
		"ALTER TABLE spans ADD COLUMN attr_x String",
	}
	// Her şey "var" — buna rağmen hiçbiri elenmemeli.
	existing := map[string]bool{"users": true, "feedbacks": true, "spans": true, "system_settings": true}
	cols := map[string]bool{
		"users.last_login_at": true, "spans.attr_http_method": true, "spans.attr_x": true,
	}
	send, so, sc := planDDL(stmts, existing, cols)
	if so != 0 || sc != 0 {
		t.Fatalf("tanınmayan ifadelerden %d nesne + %d kolon elendi — eleme kapsamı genişledi", so, sc)
	}
	if len(send) != len(stmts) {
		t.Fatalf("%d/%d gönderildi", len(send), len(stmts))
	}
}

// ── Üretim bağlaması: gerçek `tables` dilimindeki yedi ALTER ─────────

// tablesSliceAddColumns — v0.9.1302'nin kapattığı ayna kusurun ÖLÇÜLEN
// listesi (store.go, `tables := []string{…}`).
var tablesSliceAddColumns = [][2]string{
	{"users", "last_login_at"},
	{"dashboards", "variables"},
	{"dashboards", "tags"},
	{"root_cause_hypotheses", "deep_evidence"},
	{"root_cause_hypotheses", "exemplar_trace_id"},
	{"system_settings", "updated_by"},
	{"ai_feedback", "comment"},
}

// TestTablesSliceAddColumnsAreSkippable — sabit bir fixture değil, GERÇEK
// `tables` dilimi üzerinden: bu yedi ifade artık planDDL'in eleyebildiği
// şekilde ve kolonlar mevcutken HİÇ gönderilmiyorlar.
//
// Dilim adına GÜVENMİYOR: kararı ifadenin kendisi veriyor (ddlAddsColumn).
// Yani biri bunları `alters`'a taşırsa test yine geçer — v0.9.1302'nin
// tezi tam olarak bu: yerleşim artık yük taşımıyor.
func TestTablesSliceAddColumnsAreSkippable(t *testing.T) {
	tables := migrateDDLSlice(t, "tables")

	// POZİTİF KONTROL: gezici gerçekten ifade görüyor mu?
	if len(tables) < 50 {
		t.Fatalf("`tables` yalnız %d eleman verdi — gezici ölü tarama yapıyor olabilir", len(tables))
	}

	var found []string
	cols := map[string]bool{}
	for _, want := range tablesSliceAddColumns {
		hit := ""
		for _, s := range tables {
			tbl, col, ok := ddlAddsColumn(s)
			if ok && tbl == want[0] && col == want[1] {
				hit = s
				break
			}
		}
		if hit == "" {
			t.Errorf("`ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s` `tables` diliminde "+
				"ddlAddsColumn tarafından TANINMADI — ya silindi ya da eleyicinin "+
				"göremeyeceği bir şekle girdi (o hâlde her boot'ta gönderilir)",
				want[0], want[1])
			continue
		}
		found = append(found, hit)
		cols[want[0]+"."+want[1]] = true
	}
	if len(found) != len(tablesSliceAddColumns) {
		t.FailNow()
	}

	// Kolonlar mevcutken: hepsi elenir.
	send, so, sc := planDDL(found, map[string]bool{}, cols)
	if sc != len(found) || so != 0 || len(send) != 0 {
		t.Errorf("kolonlar mevcutken %d kolon elendi (%d nesne), %d ifade gönderildi; "+
			"beklenen %d / 0 / 0", sc, so, len(send), len(found))
	}

	// Taze kurulum kolu: kolon yokken hepsi gönderilir.
	send, so, sc = planDDL(found, map[string]bool{}, map[string]bool{})
	if sc != 0 || so != 0 || len(send) != len(found) {
		t.Errorf("taze kurulumda %d kolon + %d nesne elendi, %d/%d gönderildi; "+
			"hiçbiri elenmemeliydi", sc, so, len(send), len(found))
	}
}

// TestBothSlicesRunThroughUnifiedPlanner — kompozisyonun BAĞLANDIĞINI
// kaynaktan doğrular. Saf planlayıcı testleri planDDL'in doğru olduğunu
// gösterir, migrate()'in onu ÇAĞIRDIĞINI göstermez: v0.9.1301 öncesi bug
// tam olarak "doğru eleyici, yanlış çağrı yeri"ydi.
func TestBothSlicesRunThroughUnifiedPlanner(t *testing.T) {
	b, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("store.go okunamadı: %v", err)
	}
	src := string(b)
	for _, want := range []string{
		"planDDL(tables, existing, s.existingColumns(ctx))",
		"planDDL(alters, s.existingObjects(ctx), s.existingColumns(ctx))",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("store.go içinde `%s` çağrısı yok — dilimlerden biri birleşik "+
				"eleme yolundan çıkarılmış olabilir (v0.9.1302)", want)
		}
	}
	// Eski tek-taraflı çağrılar migrate()'te KALMAMALI; kalırsa bir dilim
	// yine yalnız yarı elemeden geçiyor demektir.
	for _, gone := range []string{
		"planDeclarativeDDL(tables,",
		"planAlterDDL(alters,",
	} {
		if strings.Contains(src, gone) {
			t.Errorf("store.go hâlâ `%s` çağırıyor — o dilim eleyicilerden yalnız "+
				"birini görüyor (v0.9.1302 ayna kusuru)", gone)
		}
	}
}
