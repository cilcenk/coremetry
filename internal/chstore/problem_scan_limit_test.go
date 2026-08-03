// v0.9.576 regresyon testi — öncelik daraltmasında tarama genişler.
//
// ProblemFilter.Priority SQL'de UYGULANMAZ (öncelik okuma anı hesabı,
// CH satırında yok). Daraltma Go'da, LIMIT'ten SONRA olur — yani sayfa
// boyutu kadar satır taranıp içinden P1'ler süzülürse, filoda yüzlerce
// P1 varken SIFIR sonuç dönebilir.
//
// make audit CHECK 8'in kovaladığı "LIMIT'ten sonra filtrele" sınıfı.
// Sayfa yolu bunu doğru yapıyordu; MCP list_problems aracı v0.9.554'te
// tuzağa düştü ve v0.9.576'da kural buraya taşındı — iki tüketici, tek
// kural (mcptools internal/api'yi import edemez).
package chstore

import "testing"

func TestProblemScanLimit(t *testing.T) {
	cases := []struct {
		name     string
		page     int
		narrowed bool
		want     int
	}{
		{"daraltma yok → sayfa kadar tara", 25, false, 25},
		{"daraltma var → 5× aç", 25, true, 125},
		{"büyük sayfa tavana çarpar", 500, true, ProblemScanCeiling},
		{"tavana tam oturan", 400, true, 2000},
		{"sıfır sayfa → varsayılan 100", 0, false, 100},
		{"sıfır sayfa + daraltma → 500", 0, true, 500},
		{"negatif sayfa → varsayılan", -5, false, 100},
	}
	for _, c := range cases {
		if got := ProblemScanLimit(c.page, c.narrowed); got != c.want {
			t.Errorf("%s: ProblemScanLimit(%d,%v) = %d, beklenen %d",
				c.name, c.page, c.narrowed, got, c.want)
		}
	}
}

// Tarama HER ZAMAN sayfa kadar ya da fazlası olmalı. Daha azı, daraltma
// öncesi aday setini sayfa boyutunun altına indirir ve tam da
// düzeltilen hatayı üretir.
func TestProblemScanLimitNeverBelowPage(t *testing.T) {
	for _, page := range []int{1, 25, 100, 200, 500, 5000} {
		for _, narrowed := range []bool{false, true} {
			got := ProblemScanLimit(page, narrowed)
			if got < page && got != ProblemScanCeiling {
				t.Errorf("ProblemScanLimit(%d,%v) = %d — sayfa boyutunun ALTINDA",
					page, narrowed, got)
			}
		}
	}
}
