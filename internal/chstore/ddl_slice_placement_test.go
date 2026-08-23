// v0.9.1301 — `alters` dilimi CREATE ifadesi TAŞIMAZ.
//
// Bulgu (CH şema taraması): migrate() DDL'i üç dilime ayırıyor —
// `tables`, `alters`, `mvs` — ve her dilim KENDİ eleyicisinden geçiyor:
//
//	tables → planDeclarativeDDL  (ddlObjectRe: CREATE … IF NOT EXISTS)
//	alters → planAlterDDL        (addColumnRe: ALTER … ADD COLUMN INE)
//
// Beş CREATE TABLE (log_templates, events, runbooks, runbook_executions,
// notification_log) yanlış dilimdeydi: `alters`'ta durdukları için
// planAlterDDL'in regex'ine uymuyorlardı ve HİÇ elenmiyorlardı. Tablo
// aylardır yerinde olsa bile her boot'ta ClickHouse'a gönderiliyorlardı;
// tıkalı bir dağıtık DDL kuyruğunda bu 5 × ddlTaskTimeoutSeconds ≈ 100 sn,
// sonucu tanım gereği no-op olan beş kuyruk turu (v0.9.607/608/614'ün
// kapatmaya çalıştığı maliyet sınıfının aynısı).
//
// Bu test o yerleşimi PİNLER. Kaynak metni değil AST'yi geziyor: yorum
// satırlarındaki "CREATE TABLE" geçişleri ve çok satırlı ham dizeler naif
// bir regex taramasını yanıltır (yorum-soyucu tuzağı), go/parser ise
// ifadeyi ifadeden ayırt eder.
package chstore

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// migrateDDLSlice — store.go'daki `<name> := []string{…}` atamasının
// ELEMANLARINI, her elemanın BAŞINDAKİ dize sabitiyle döner.
//
// Eleman şekilleri: ham dize sabiti, `"…" + expr` (clusterDeriveExpr),
// `fmt.Sprintf("…", …)`. Üçünde de karar veren şey ifadenin ilk
// sabitidir — DDL fiili orada.
func migrateDDLSlice(t *testing.T, name string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "store.go", nil, 0)
	if err != nil {
		t.Fatalf("store.go ayrıştırılamadı: %v", err)
	}
	var out []string
	var found bool
	ast.Inspect(f, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		id, ok := as.Lhs[0].(*ast.Ident)
		if !ok || id.Name != name {
			return true
		}
		cl, ok := as.Rhs[0].(*ast.CompositeLit)
		if !ok {
			return true
		}
		found = true
		for _, el := range cl.Elts {
			out = append(out, leadingStringLit(el))
		}
		return false
	})
	if !found {
		t.Fatalf("store.go içinde `%s := []string{…}` ataması bulunamadı — "+
			"dilim yeniden adlandırıldıysa bu test onunla birlikte güncellenmeli", name)
	}
	return out
}

// leadingStringLit — ifadenin en soldaki dize sabiti. Bulunamazsa "".
func leadingStringLit(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return ""
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return ""
		}
		return s
	case *ast.BinaryExpr:
		return leadingStringLit(v.X)
	case *ast.CallExpr:
		if len(v.Args) == 0 {
			return ""
		}
		return leadingStringLit(v.Args[0])
	case *ast.ParenExpr:
		return leadingStringLit(v.X)
	}
	return ""
}

// ddlCreateStmtRe — ifadenin BAŞINDA duran CREATE. Yorumları görmez
// (AST'den geliyoruz), gövde içindeki "CREATE" kelimelerini de görmez
// (^ ile çapalı).
var ddlCreateStmtRe = regexp.MustCompile(
	`(?is)^\s*CREATE\s+(?:MATERIALIZED\s+VIEW|VIEW|TABLE)\b`)

func TestAltersSliceHasNoCreateStatements(t *testing.T) {
	alters := migrateDDLSlice(t, "alters")

	// POZİTİF KONTROL: gezici gerçekten ifade görüyor mu? Bu assert
	// olmadan "hiç CREATE yok" sonucu, dilimi hiç bulamamış ölü bir
	// taramada da yeşil yanardı.
	if len(alters) < 50 {
		t.Fatalf("`alters` yalnız %d eleman verdi — gezici ifadeleri "+
			"göremiyor olabilir (ölü tarama)", len(alters))
	}
	nAlter := 0
	for _, s := range alters {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(s)), "ALTER ") {
			nAlter++
		}
	}
	if nAlter == 0 {
		t.Fatal("`alters` diliminde tek bir ALTER bile görünmüyor — " +
			"gezici dize sabitlerini çıkaramıyor")
	}

	for i, s := range alters {
		if ddlCreateStmtRe.MatchString(s) {
			t.Errorf("alters[%d] bir CREATE ifadesi:\n\t%.90s\n"+
				"planAlterDDL YALNIZ `ALTER … ADD COLUMN IF NOT EXISTS` eler; "+
				"buradaki bir CREATE her boot'ta gönderilir (tıkalı dağıtık DDL "+
				"kuyruğunda ~%d sn/ifade). Yeri `tables` dilimi — orada "+
				"planDeclarativeDDL nesne varsa hiç göndermez.", i, s, ddlTaskTimeoutSeconds)
		}
	}
}

// TestMovedCreateTablesLiveInTablesSlice — v0.9.1301'de taşınan beş
// tablonun VARIŞ tarafını pinler. Yalnız "alters'ta yok"u ölçmek yetmez:
// beş ifade tamamen silinseydi o assert de yeşil yanardı.
func TestMovedCreateTablesLiveInTablesSlice(t *testing.T) {
	tables := migrateDDLSlice(t, "tables")

	want := []string{
		"log_templates",
		"events",
		"runbooks",
		"runbook_executions",
		"notification_log",
	}
	for _, name := range want {
		re := regexp.MustCompile(`(?is)^\s*CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+` +
			regexp.QuoteMeta(name) + `\b`)
		hit := false
		for _, s := range tables {
			if re.MatchString(s) {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("`%s` CREATE TABLE'ı `tables` diliminde yok — v0.9.1301 "+
				"taşıması geri alınmış ya da tablo silinmiş olabilir", name)
		}
	}
}

// TestMovedCreatesAreSkippableByTheDeclarativePlanner — taşımanın ASIL
// kazancı: bu beş ifade artık planDeclarativeDDL'in eleyebildiği şekilde.
// Bunu ifadenin KENDİSİ üzerinden kanıtlıyoruz (ddlCreatesObject), dilim
// adına güvenerek değil.
func TestMovedCreatesAreSkippableByTheDeclarativePlanner(t *testing.T) {
	tables := migrateDDLSlice(t, "tables")
	existing := map[string]bool{}
	for _, name := range []string{
		"log_templates", "events", "runbooks",
		"runbook_executions", "notification_log",
	} {
		existing[name] = true
	}

	var moved []string
	for _, s := range tables {
		if obj, ok := ddlCreatesObject(s); ok && existing[obj] {
			moved = append(moved, s)
		}
	}
	if len(moved) != len(existing) {
		t.Fatalf("planDeclarativeDDL %d/%d taşınan CREATE'i tanıdı — "+
			"tanınmayan bir ifade her boot'ta gönderilmeye devam eder",
			len(moved), len(existing))
	}

	send, skipped := planDeclarativeDDL(moved, existing)
	if skipped != len(moved) || len(send) != 0 {
		t.Errorf("nesneler mevcutken %d elendi / %d gönderildi; hepsi elenmeliydi",
			skipped, len(send))
	}

	// Taze kurulum kolu: hiçbir nesne yokken hepsi gönderilir.
	send, skipped = planDeclarativeDDL(moved, map[string]bool{})
	if skipped != 0 || len(send) != len(moved) {
		t.Errorf("taze kurulumda %d elendi / %d gönderildi; hiçbiri elenmemeliydi",
			skipped, len(send))
	}
}
