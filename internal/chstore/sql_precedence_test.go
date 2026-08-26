package chstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sql_precedence_test.go — v0.10.57. AND/OR ÖNCELİĞİ.
//
// ── OLAY ────────────────────────────────────────────────────────────────
//
// queryPgDatabases'in CTE'si şöyleydi:
//
//	WHERE time >= ? AND time <= ?
//	  AND startsWith(metric, 'postgresql.')
//	  AND has(attr_keys, 'database') OR has(attr_keys, 'postgresql.database.name')
//
// SQL'de AND, OR'dan SIKI bağlar. Yani zincir "…AND has(database)" ile
// KAPANIYOR ve "OR has(postgresql.database.name)" AYRI bir dal oluyor —
// zaman sınırı da metrik süzgeci de TAŞIMAYAN bir dal.
//
// Canlı ClickHouse'da ölçüldü (aynı şekil, var olan veriyle):
//
//	parantezli   →     7.109 satır, pencere 10 DAKİKA
//	parantezsiz  → 5.193.027 satır, pencere 10 GÜN   (731×)
//
// İki kat zarar: (a) CLAUDE.md'nin "her metric_points sorgusu zaman
// sınırlı" sert kısıtı çiğneniyor ve milyar-nokta ölçeğinde sorgu
// taramayı patlatıyor; (b) observedSpanSQL paydayı TÜM SAKLAMA aralığı
// üzerinden hesaplıyor, yani commits_ps sessizce yanlış çıkıyor.
//
// ⚠ Göze çarpmıyor çünkü sorgu HATA VERMİYOR ve sonuç MAKUL görünüyor.
//
// ── NEDEN GENEL KAPI ────────────────────────────────────────────────────
//
// Tek satırı pinlemek yeterli değil: bu bir YAZIM tuzağı ve bir sonraki
// sorguda aynen tekrarlanır. Kapı, aynı DÜZEYDE AND ile OR karışan her
// SQL satırını arıyor.
//
// Yöntem: satırdan DENGELİ parantez gruplarını sök; kalanda hem "AND "
// hem " OR " varsa karışım aynı düzeydedir. `AND (a OR b)` bu sökümden
// sonra "AND" olarak kalır ve yanlış bayraklanmaz.

// stripBalancedParens — içteki dengeli parantez gruplarını siler.
func stripBalancedParens(s string) string {
	for {
		out, depth, changed := make([]rune, 0, len(s)), 0, false
		for _, r := range s {
			switch r {
			case '(':
				depth++
				changed = true
			case ')':
				if depth > 0 {
					depth--
					continue
				}
				out = append(out, r)
			default:
				if depth == 0 {
					out = append(out, r)
				}
			}
		}
		if !changed {
			return string(out)
		}
		next := string(out)
		if next == s {
			return next
		}
		s = next
	}
}

func TestNoUnparenthesizedAndOrMix(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("dosyalar okunamadı: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", f, err)
		}
		for i, line := range strings.Split(stripGoCommentsCH(string(b)), "\n") {
			l := stripBalancedParens(line)
			if !strings.Contains(l, "AND ") || !strings.Contains(l, " OR ") {
				continue
			}
			t.Errorf("%s:%d — AYNI DÜZEYDE AND ve OR, parantezsiz:\n    %s\n"+
				"SQL'de AND, OR'dan SIKI bağlar: zincir AND'de kapanır ve OR "+
				"dalı zaman/metrik süzgeçlerini TAŞIMAZ. Ölçülen bedel: 731× "+
				"satır ve 10 dakika yerine 10 GÜN pencere (v0.10.57). "+
				"OR'u parantez içine al.", f, i+1, strings.TrimSpace(line))
		}
	}
}
