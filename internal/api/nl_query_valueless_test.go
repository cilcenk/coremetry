// v0.9.600 — değer almayan operatörler sessizce düşüyordu.
//
// Şema ve prompt EXISTS / NOT EXISTS'i modele SUNUYOR, model doğru
// ayrıştırıyor, sonra handler'ın koşulsuz `len(v) == 0` kapısı cevabı
// çöpe atıyordu. Yığındaki üç yer bu iki operatörün değersiz olduğunu
// biliyordu (chstore/filterexpr.go SQL'i Values'a bakmadan kuruyor,
// FilterBuilder.tsx NEEDS_VALUE:false diyor, dsl.go Values'sız
// FilterExpr üretiyor) — yalnız NL yolu bilmiyordu.
//
// Belirti yanıltıcıydı: tek filtre EXISTS ise UI "Model produced no
// filters — try rephrasing." diyordu, oysa model DOĞRU ayrıştırmıştı.
package api

import (
	"reflect"
	"testing"
)

func TestValuelessOpsSurviveCleanup(t *testing.T) {
	for _, op := range []string{"EXISTS", "NOT EXISTS"} {
		if opNeedsValue(op) {
			t.Errorf("%s değer istiyor sayılıyor — chstore/filterexpr.go SQL'i "+
				"Values'a HİÇ bakmadan kuruyor, FilterBuilder.tsx da "+
				"NEEDS_VALUE:false diyor", op)
		}
	}
	for _, op := range []string{"=", "!=", "LIKE", "NOT LIKE", "IN", "NOT IN", ">", ">=", "<", "<="} {
		if !opNeedsValue(op) {
			t.Errorf("%s değersiz sayılıyor — değersiz geçerse anlamsız bir "+
				"filtre sorguya girer", op)
		}
	}
}

// TestValuelessOpNormalisesValue — aynı cümle HER ZAMAN aynı sonucu
// vermeli.
//
// Şema `v`yi required kılıyor, yani model bir şey üretmek ZORUNDA:
// kimi zaman [], kimi zaman [""]. Normalize etmezsek aynı soru iki
// farklı URL state'i üretir ve davranış modelin o günkü keyfine kalır.
func TestValuelessOpNormalisesValue(t *testing.T) {
	cases := map[string][]string{
		"boş dizi":    {},
		"nil":         nil,
		"boş string":  {""},
		"dolgu değer": {"true"},
		"birden çok":  {"a", "b"},
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			got := cleanNLFilters([]nlToQueryFilter{{K: "exception.type", Op: "EXISTS", V: v}})
			if len(got) != 1 {
				t.Fatalf("filtre düştü (%d) — EXISTS değer istemez", len(got))
			}
			if got[0].V != nil {
				t.Errorf("v normalize edilmedi: %v — aynı cümle iki farklı URL "+
					"state'i üretir ve davranış modelin keyfine kalır", got[0].V)
			}
		})
	}
}

// TestValueRequiringOpStillDropped — kapı ZAYIFLAMAMALI.
func TestValueRequiringOpStillDropped(t *testing.T) {
	got := cleanNLFilters([]nlToQueryFilter{
		{K: "service.name", Op: "=", V: nil},
		{K: "service.name", Op: "IN", V: []string{}},
		{K: "http.status", Op: ">", V: []string{"500"}},
	})
	if len(got) != 1 || got[0].Op != ">" {
		t.Errorf("değersiz kalan '=' / 'IN' geçti: %+v — anlamsız filtre "+
			"sorguya girer", got)
	}
}

// TestMixedRequestKeepsBothFilters — en sinsi vaka.
//
// "auth-service'te exception olanlar": service filtresi uygulanıp
// EXISTS sessizce düşüyordu. Operatör DAHA GENİŞ bir kümeye bakarken
// filtrenin uygulandığını sanıyor ve explain metni uygulanmayan
// filtreyi anlatmaya devam ediyordu — yanlış cevap değil, YANLIŞ
// GÜVEN.
func TestMixedRequestKeepsBothFilters(t *testing.T) {
	got := cleanNLFilters([]nlToQueryFilter{
		{K: "service.name", Op: "=", V: []string{"auth-service"}},
		{K: "exception.type", Op: "EXISTS", V: []string{}},
	})
	if len(got) != 2 {
		t.Fatalf("karışık istekte %d filtre kaldı, 2 bekleniyordu: %+v\n\n"+
			"Bu vakada operatör filtrenin uygulandığını SANIYOR — daha geniş "+
			"bir sonuç kümesine bakarken explain metni uygulanmayan filtreyi "+
			"anlatmaya devam ediyor.", len(got), got)
	}
	ops := []string{got[0].Op, got[1].Op}
	if !reflect.DeepEqual(ops, []string{"=", "EXISTS"}) {
		t.Errorf("sıra/operatörler bozuk: %v", ops)
	}
}
