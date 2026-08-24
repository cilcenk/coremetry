package chstore

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// problem_projection_test.go — v0.9.1338.
//
// problemSelectExpr ile scanProblemRow POZİSYONEL olarak eşleşiyor: SQL
// hangi kolonu kaçıncı sırada seçtiyse, Scan o sıradaki işaretçiye yazar.
// v0.9.976 bu çifti tek kaynağa indirdi ama HİZAYI hiçbir şey ölçmüyordu —
// tek opsiyonel kolon (comparator) varken sıra hatası mümkün DEĞİLDİ.
// İkinci opsiyonel kolonla (kind) mümkün hâle geldi ve derleyici SUSAR:
// iki `String` kolonu takas etmek geçerli Go'dur, yalnız her problemin
// comparator'ı "db" ve türü "<" olur.
//
// Kapı: projeksiyonu ayrıştır, her kolonun ADINI karşılık gelen
// işaretçiye yaz, sonra alanların doğru ADI taşıdığını doğrula.

// namedColScanner — projeksiyonun kolon ADLARINI sırayla hedeflere yazan
// sahte satır. Gerçek bir CH sürücüsünün yaptığı şeyin iskeleti: n hedef,
// n değer, POZİSYONA göre.
type namedColScanner struct{ cols []string }

func (s namedColScanner) Scan(dst ...any) error {
	if len(dst) != len(s.cols) {
		return fmt.Errorf("projeksiyon %d kolon seçiyor ama Scan %d hedef "+
			"veriyor — INSERT/SELECT hizası bozuk", len(s.cols), len(dst))
	}
	for i, d := range dst {
		switch v := d.(type) {
		case *string:
			*v = s.cols[i]
		case *float64:
			*v = float64(i)
		case *int64:
			*v = int64(i)
		case **time.Time:
			*v = nil
		default:
			return fmt.Errorf("kolon %d (%s) için beklenmeyen hedef tipi %T",
				i, s.cols[i], d)
		}
	}
	return nil
}

// projectionColumns — `a, b, toX(c)` biçimindeki projeksiyonu kolon
// adlarına ayırır. Bu projeksiyonda iç içe virgül YOK (tek argümanlı
// fonksiyon çağrıları), o yüzden düz bölme yeterli — ve yeterli
// olmaktan çıkarsa bu testin kendisi kırılır, sessizce yanlış saymaz.
func projectionColumns(t *testing.T, expr string) []string {
	t.Helper()
	var out []string
	for _, part := range strings.Split(expr, ",") {
		p := strings.Join(strings.Fields(part), "")
		if p == "" {
			t.Fatalf("projeksiyonda boş kolon: %q", expr)
		}
		if strings.Count(p, "(") != strings.Count(p, ")") {
			t.Fatalf("kolon %q dengesiz parantez taşıyor — projeksiyon iç içe "+
				"virgül kazanmış olabilir, ayrıştırıcı artık geçerli değil", p)
		}
		out = append(out, p)
	}
	return out
}

// TestProblemProjectionAndScanAgreeOnOrder — HİZA KAPISI, dört kombinasyon.
//
// Her kolonun kendi adını taşıması, sıranın uçtan uca aynı olduğunun
// kanıtı. comparator ve kind'ı takas etmek bu testi ANINDA kırar.
func TestProblemProjectionAndScanAgreeOnOrder(t *testing.T) {
	for _, c := range allProblemColCombos() {
		t.Run(fmt.Sprintf("cmp=%v/kind=%v", c.Comparator, c.Kind), func(t *testing.T) {
			cols := projectionColumns(t, problemSelectExprFor(c))
			p, err := scanProblemRow(namedColScanner{cols: cols}, c)
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			// String alanlar KENDİ kolon adlarını taşımalı.
			for _, chk := range []struct{ field, want string }{
				{p.ID, "id"},
				{p.RuleID, "rule_id"},
				{p.RuleName, "rule_name"},
				{p.Severity, "severity"},
				{p.Service, "service"},
				{p.Metric, "metric"},
				{p.Status, "status"},
				{p.Description, "description"},
				{p.Assignee, "assignee"},
				{p.Pod, "pod"},
				{p.AISummary, "ai_summary"},
			} {
				if chk.field != chk.want {
					t.Errorf("%q alanına %q düştü — projeksiyon ile scan sırası "+
						"AYRIŞMIŞ", chk.want, chk.field)
				}
			}
			if c.Comparator && p.Comparator != "comparator" {
				t.Errorf("Comparator alanına %q düştü, \"comparator\" bekleniyordu", p.Comparator)
			}
			if !c.Comparator && p.Comparator != "" {
				t.Errorf("comparator seçilmediği hâlde alan dolu: %q", p.Comparator)
			}
			if c.Kind && p.Kind != "kind" {
				t.Errorf("Kind alanına %q düştü, \"kind\" bekleniyordu — "+
					"comparator ile kind takas edilmiş olabilir", p.Kind)
			}
		})
	}
}

// TestScanNormalisesKindWhenColumnAbsent — İKİ-BOOT SÖZLEŞMESİNİN kapısı.
//
// Kolonu EKLEYEN boot probe'u false okur (küme kipinde DDL ertelenir,
// v0.9.614), yani o boot boyunca projeksiyon kind'ı ATLAR. O gövdede
// Kind alanı boş kalırsa üst katmanlar üçüncü bir dal ("boş mu, service
// mi?") taşımak zorunda kalır — ve taşımayan her okuyucu db özneli
// satırı da servis özneli satırı da AYNI şekilde ele alamaz.
//
// Normalizasyon scanProblemRow'da, yani TEK yerde.
func TestScanNormalisesKindWhenColumnAbsent(t *testing.T) {
	c := problemCols{Comparator: true, Kind: false}
	cols := projectionColumns(t, problemSelectExprFor(c))
	p, err := scanProblemRow(namedColScanner{cols: cols}, c)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if p.Kind != ProblemKindService {
		t.Errorf("kind kolonu yokken Kind=%q; %q bekleniyordu — ertelenmiş DDL "+
			"boot'unda ürün bugünkü davranışın BİREBİR aynısını göstermeli",
			p.Kind, ProblemKindService)
	}
}

// TestScanRejectsAMismatchedProjection — NEGATİF KONTROL.
//
// Yukarıdaki kapının ısırabildiğini kanıtlar: hedef sayısı ile kolon
// sayısı ayrıştığında namedColScanner GERÇEKTEN hata veriyor mu? Bu
// olmadan "her kombinasyon geçti" cümlesi, sahte satırın sessizce her
// şeyi kabul ettiği bir dünyada da doğru olurdu.
func TestScanRejectsAMismatchedProjection(t *testing.T) {
	// kind'lı projeksiyon, kind'sız scan bayrağı → bir hedef eksik.
	cols := projectionColumns(t, problemSelectExprFor(problemCols{Kind: true}))
	if _, err := scanProblemRow(namedColScanner{cols: cols}, problemCols{Kind: false}); err == nil {
		t.Fatal("hiza bozukken scan hata VERMEDİ — hiza kapısı ölü, geçen " +
			"testler hiçbir şey kanıtlamıyor")
	}
}
