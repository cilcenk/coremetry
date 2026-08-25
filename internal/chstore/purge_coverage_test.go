package chstore

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// purge_coverage_test.go — v0.10.17, F0.4.
//
// ── NEDEN BU KAPI VAR ───────────────────────────────────────────────────
//
// F0.4 tek bir eksik tablo (`db_statement_summary_5m`) olarak bildirildi.
// Sayınca BEŞ çıktı. Bu, tek tek düzeltmenin yanlış çözüm olduğunun
// kanıtı: liste elle tutuluyor, yeni tablo eklemek listeyi güncellemeyi
// HATIRLAMAYI gerektiriyor, ve unutmanın hiçbir belirtisi yok —
// `go build` sessiz, testler yeşil, purge "başarılı" diyor. Yalnız
// yarısını siliyor.
//
// Kapı şunu zorluyor: chstore'un YARATTIĞI her tablo, iki listeden
// birinde adı geçmek zorunda. Üçüncü bir seçenek (hiçbirinde olmamak)
// artık derleme değil, test hatası.
//
// ── NEDEN KAYNAK TARAMASI ───────────────────────────────────────────────
//
// Canlı CH'ye bağlanmak burada işe yaramazdı: kapının yakalaması gereken
// an, tablonun KODA eklendiği an — henüz hiçbir yerde yaratılmamışken.

// purgeCoverageExempt — kasıtlı olarak İKİ listede de olmayanlar.
// Her satır gerekçesini taşır; gerekçesiz muafiyet, kapının kendisini
// delmenin kolay yolu olurdu.
var purgeCoverageExempt = map[string]string{
	// NOT: `spans_local` burada YOK ve olmamalı. Dağıtık iç tablolar adı
	// çalışma zamanında (`name + "_local"`) kuruluyor, hiçbir kaynakta
	// düz metin CREATE'i geçmiyor — dolayısıyla taramaya hiç girmiyor.
	// Muafiyet olarak yazmıştım; canlılık testi ölü satır diye yakaladı.
	//
	// Kendi başına veri tutmayan TO-form MV nesnesi; hedef tablosu
	// (span_links_reverse) allowlist'te.
	"span_links_reverse_mv": "TO-form MV nesnesi, veri hedefte",
	// v0.10.17 — AÇIK KARAR OPERATÖRDE. Bildirim teslim kaydı: ne
	// telemetri (yeniden doğmaz) ne operatör içeriği (kimse yazmadı).
	// audit_log emsaliyle korunmaya, monitor_results emsaliyle
	// silinmeye yakın. Karar verilene dek DAVRANIŞ DEĞİŞMİYOR:
	// purge etmiyor. Muafiyet, kararın verilmediğini görünür tutuyor.
	"notification_log": "operatör kararı bekliyor — telemetri mi teslim kanıtı mı",
}

var reCreate = regexp.MustCompile(
	"CREATE\\s+(?:MATERIALIZED\\s+VIEW|TABLE)\\s+IF\\s+NOT\\s+EXISTS\\s+`?([a-z][a-z0-9_]{3,})`?")

// createdTables — chstore'un test-dışı kaynaklarında yaratılan tablolar.
func createdTables(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	seen := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		// ⚠ YORUMLARI ÖNCE SÖK. İlk yazımda sökmemiştim ve kapı iki
		// Türkçe yorum cümlesini ("... mevcut", "... rebuilds") tablo
		// adı sandı. Bu, tekrar eden bir sınıf: kapı kendi
		// dokümantasyonunu ısırıyor (v0.9.1375, v0.9.1382).
		for _, m := range reCreate.FindAllStringSubmatch(stripGoCommentsCH(string(b)), -1) {
			seen[m[1]] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestEveryCreatedTableIsClassified — asıl kapı.
func TestEveryCreatedTableIsClassified(t *testing.T) {
	inPurge := map[string]bool{}
	for _, n := range telemetryPurgeTables {
		inPurge[n] = true
	}
	inPreserve := map[string]bool{}
	for _, n := range configPreserveTables {
		inPreserve[n] = true
	}

	created := createdTables(t)
	if len(created) < 40 {
		// Tarama bozulduysa kapı sessizce yeşile döner — bu, korumayı
		// kaybetmenin en olası yolu.
		t.Fatalf("yalnız %d tablo bulundu; regex ya da glob bozulmuş olmalı", len(created))
	}

	var unclassified []string
	for _, name := range created {
		if inPurge[name] || inPreserve[name] {
			continue
		}
		if _, ok := purgeCoverageExempt[name]; ok {
			continue
		}
		unclassified = append(unclassified, name)
	}
	if len(unclassified) > 0 {
		t.Errorf("şu tablolar HİÇBİR listede değil — purge onları sessizce atlar:\n  %s\n\n"+
			"Telemetriden türüyor ve yeniden doğuyorsa telemetryPurgeTables'a; operatör\n"+
			"içeriği taşıyorsa configPreserveTables'a ekle. Kararsızsan purgeCoverageExempt'e\n"+
			"GEREKÇEYLE yaz — ama gerekçesi 'emin değilim' ise cevap preserve'dür: silmenin\n"+
			"geri dönüşü yok, saklamanınki bir sonraki sürüm.",
			strings.Join(unclassified, "\n  "))
	}
}

// TestNoTableIsInBothLists — çelişki kontrolü. Bir tablo hem silinip hem
// korunamaz; ikisinde birden olması, hangisinin kazandığını koda bakmadan
// bilinemez hale getirir.
func TestNoTableIsInBothLists(t *testing.T) {
	inPurge := map[string]bool{}
	for _, n := range telemetryPurgeTables {
		inPurge[n] = true
	}
	for _, n := range configPreserveTables {
		if inPurge[n] {
			t.Errorf("%q hem purge hem preserve listesinde", n)
		}
	}
}

// TestExemptionsAreJustifiedAndReal — muafiyet listesi çürümesin.
//
// Boş gerekçe, muafiyeti sessiz bir kaçış kapısına çevirir. Artık
// yaratılmayan bir tablonun muafiyeti ise ölü satırdır ve bir sonraki
// okuyucuyu yanıltır.
func TestExemptionsAreJustifiedAndReal(t *testing.T) {
	created := map[string]bool{}
	for _, n := range createdTables(t) {
		created[n] = true
	}
	for name, why := range purgeCoverageExempt {
		if len(strings.TrimSpace(why)) < 15 {
			t.Errorf("%q muafiyeti gerekçesiz (%q)", name, why)
		}
		if !created[name] {
			t.Errorf("%q artık yaratılmıyor — ölü muafiyet, silinmeli", name)
		}
	}
}

// TestTheFiveFamiliesStayClassified — F0.4'ün kendisi.
//
// Kapı genel; bu test bildirilen beş aileyi ADIYLA çiviliyor ki bir
// gelecek yeniden düzenleme onları muafiyete kaydırıp kapıyı teknik
// olarak yeşil bırakmasın.
func TestTheFiveFamiliesStayClassified(t *testing.T) {
	inPurge := map[string]bool{}
	for _, n := range telemetryPurgeTables {
		inPurge[n] = true
	}
	for _, n := range []string{
		"db_statement_summary_5m", "metric_catalog", "service_seen",
		"service_version_5m", "rca_verdicts",
	} {
		if !inPurge[n] {
			t.Errorf("%q purge allowlist'inden düşmüş — F0.4 regresyonu", n)
		}
	}
	// Ve karşı taraf: operatörün yüklediği dokümanlar ASLA silinmemeli.
	for _, n := range telemetryPurgeTables {
		if n == "rag_chunks" {
			t.Error("rag_chunks purge listesine girmiş — operatörün yüklediği dokümanlar geri gelmez")
		}
	}
}
