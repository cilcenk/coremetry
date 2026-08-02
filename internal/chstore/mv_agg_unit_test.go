// v0.9.565 regresyon testi — MV yolu ile ham yolun BİRİM SÖZLEŞMESİ.
//
// Bug: aynı panel, MV hızlı yolunun devreye girip girmemesine göre
// farklı BİRİMDE değer döndürüyordu.
//
//	rate:       MV dakika başına  / ham saniye başına  → 60× sapma
//	error_rate: MV 0-1 oranı      / ham yüzde          → 100× sapma
//
// Daha kötüsü sapma ARALIĞA BAĞLIYDI: MV kapısı ≤24 saatlik aralıkları
// reddediyor, yani operatör aralığı 24 saatin üstüne çekince aynı karo
// 60× sıçrıyor, "%2.5 hata" birden "%0.025" oluyordu. Hiçbir hata
// görünmeden — sayı makul kaldığı için fark edilmesi de zordu.
//
// Kök sebep: MV switch'i ÜÇ yerde birebir kopyalanmıştı ve kopyalar ham
// yoldan sessizce ayrıştı. v0.9.565 üçünü mvAggExpr'e indirdi; bu test
// iki yolun BİRLİKTE evrilmesini sabitliyor.
package chstore

import (
	"strings"
	"testing"
)

const testStep = 60

// unitClass — bir SQL ifadesinin birim sınıfını yapısal olarak okur.
// İfadeler çalıştırılamaz (CH gerekir), ama birim taşıyan çarpanlar
// metinde görünür ve sözleşme tam olarak onlarda duruyor.
func hasPerMinuteFactor(expr string) bool { return strings.Contains(expr, "* 60.0") }
func hasPercentFactor(expr string) bool   { return strings.Contains(expr, "100.0 *") }

func TestMVAndRawAggUnitsAgree(t *testing.T) {
	cases := []struct {
		agg     string
		perMin  bool // birim dakika başına mı
		percent bool // birim yüzde mi
		why     string
	}{
		{
			agg: "rate", perMin: false, percent: false,
			why: "rate SANİYE başına — ham yol count()/step diyor; MV'nin " +
				"*60 çarpanı 60× sapma üretiyordu",
		},
		{
			agg: "per_min", perMin: true, percent: false,
			why: "per_min DAKİKA başına — burada *60 DOĞRU, iki yolda da olmalı",
		},
		{
			agg: "error_rate", perMin: false, percent: true,
			why: "error_rate YÜZDE — ham yol 100.0*… diyor; MV'nin çıplak " +
				"oranı 100× sapma üretiyordu",
		},
		{agg: "count", perMin: false, percent: false, why: "ham sayım"},
		{agg: "errors", perMin: false, percent: false, why: "ham sayım"},
	}

	for _, c := range cases {
		t.Run(c.agg, func(t *testing.T) {
			mv, ok := mvAggExpr(c.agg, testStep)
			if !ok {
				t.Fatalf("mvAggExpr(%q) desteklenmiyor", c.agg)
			}
			raw, err := aggToSQL(c.agg, "duration_ms", testStep)
			if err != nil {
				t.Fatalf("aggToSQL(%q): %v", c.agg, err)
			}

			if hasPerMinuteFactor(mv) != c.perMin {
				t.Errorf("MV dakika-çarpanı=%v, beklenen %v — %s",
					hasPerMinuteFactor(mv), c.perMin, c.why)
			}
			if hasPerMinuteFactor(raw) != c.perMin {
				t.Errorf("ham dakika-çarpanı=%v, beklenen %v — %s",
					hasPerMinuteFactor(raw), c.perMin, c.why)
			}
			if hasPercentFactor(mv) != c.percent {
				t.Errorf("MV yüzde-çarpanı=%v, beklenen %v — %s",
					hasPercentFactor(mv), c.percent, c.why)
			}
			if hasPercentFactor(raw) != c.percent {
				t.Errorf("ham yüzde-çarpanı=%v, beklenen %v — %s",
					hasPercentFactor(raw), c.percent, c.why)
			}

			// ASIL İDDİA: iki yol AYNI birim sınıfında olmalı.
			if hasPerMinuteFactor(mv) != hasPerMinuteFactor(raw) ||
				hasPercentFactor(mv) != hasPercentFactor(raw) {
				t.Errorf("MV ile ham yol BİRİM olarak ayrışıyor.\n  MV : %s\n  ham: %s\n"+
					"Aynı panel, aralık 24 saati geçtiğinde sessizce farklı bir "+
					"sayı gösterir.", mv, raw)
			}
		})
	}
}

// Desteklenmeyen agg MV'den karşılanamaz demeli — sessizce boş ifade
// döndürmemeli (çağıran ham yola düşecek).
func TestMVAggExprRejectsUnknown(t *testing.T) {
	if _, ok := mvAggExpr("band", testStep); ok {
		t.Error("band MV'den karşılanıyor görünüyor — o yalnız metric " +
			"resolver yolunda geçerli")
	}
	if _, ok := mvAggExpr("uydurma", testStep); ok {
		t.Error("bilinmeyen agg kabul edildi")
	}
}
