package chstore

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// engine_scan_silence_test.go — v0.10.51. SESSİZ SIFIR.
//
// ── OLAY ────────────────────────────────────────────────────────────────
//
// v0.10.15 oran paydasını düzeltirken sorguya `observed_sec` kolonunu
// ekledi. CH'de `dateDiff` Int64 döner ve `greatest(Int64, UInt8)` de
// Int64'tür; Go tarafı onu `float64`'e tarıyordu, yani clickhouse-go her
// satırda tip hatası veriyordu.
//
// Hata görünmedi, çünkü tarama şöyle yazılmıştı:
//
//	if err := rows.Scan(&m, &v, &obs); err == nil { out[m] = v }
//
// Hata olunca satır SESSİZCE atlanıyor. Sonuç: `/api/databases/oracle`
// canlıda BÜTÜN oranları 0 döndürdü — cpuTimeSec, logicalReadsPerSec,
// executionsPerSec, hepsi — veri ClickHouse'da yerli yerinde dururken ve
// hiçbir log satırı çıkmadan.
//
// ── NEDEN BU KAPI ───────────────────────────────────────────────────────
//
// Kusurun iki yarısı vardı ve YALNIZ İKİNCİSİ genellenebilir:
//  1. tip uyuşmazlığı — SQL'de toFloat64() ile kapatıldı (tek yer),
//  2. HATANIN YUTULMASI — bunu kapatan tek şey disiplin.
//
// Sessiz sıfır, gürültülü hatadan pahalıdır: operatör "Oracle boşta" diye
// okur ve olmayan bir soruna bakmaz. Bu kapı ikinci yarıyı çiviliyor.
//
// ⚠ Tarama AST üzerinden. Bu gece beş kez bir gate, aradığı deseni KENDİ
// yorumunda bulup kendini ısırdı — yukarıdaki kod örneği de dahil olurdu.
// AST yorumları hiç görmez.
func TestRowScanErrorsAreNotSwallowed(t *testing.T) {
	engines := []string{"oracle.go", "postgres.go", "mysql.go", "redis.go"}
	fset := token.NewFileSet()
	for _, f := range engines {
		t.Run(f, func(t *testing.T) {
			file, err := parser.ParseFile(fset, f, nil, 0) // yorumlar ayrıştırılmıyor
			if err != nil {
				t.Fatalf("%s ayrıştırılamadı: %v", f, err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				ifst, ok := n.(*ast.IfStmt)
				if !ok || ifst.Init == nil {
					return true
				}
				// `if err := rows.Scan(...); err == nil { … }`
				assign, ok := ifst.Init.(*ast.AssignStmt)
				if !ok || len(assign.Rhs) != 1 {
					return true
				}
				call, ok := assign.Rhs[0].(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Scan" {
					return true
				}
				bin, ok := ifst.Cond.(*ast.BinaryExpr)
				if !ok || bin.Op != token.EQL {
					return true
				}
				if id, ok := bin.Y.(*ast.Ident); ok && id.Name == "nil" {
					t.Errorf("%s:%d — `Scan(...); err == nil` deseni: tarama hatası "+
						"satırı SESSİZCE düşürür ve çağıran onu SIFIR diye okur. "+
						"v0.10.15'te tam bu, bütün Oracle oranlarını 0 yaptı. "+
						"Hatayı DÖNDÜR.", f, fset.Position(ifst.Pos()).Line)
				}
				return true
			})
		})
	}
}

// TestObservedSecIsFloatInSQL — tip sözleşmesi KAYNAKTA sabit.
//
// Kolon tek yerde tanımlanıyor; tipi de orada sabitlenmeli. Her tüketicinin
// doğru Go tipini hatırlaması beklenirse biri unutur ve aynı sessiz sıfır
// geri gelir ([[feedback-gate-single-spelling]]).
func TestObservedSecIsFloatInSQL(t *testing.T) {
	if !strings.Contains(rateSelectSQL, "toFloat64(") {
		t.Error("observed_sec Float64'e zorlanmıyor — dateDiff Int64 döner ve " +
			"float64'e taranınca clickhouse-go tip hatası verir")
	}
	if !strings.Contains(rateSelectSQL, "AS observed_sec") {
		t.Error("observed_sec kolonu kaybolmuş — geri çarpım aralığı taşıyamaz")
	}
}
