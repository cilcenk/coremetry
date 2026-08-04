package chstore

import (
	"strings"
	"testing"
)

// v0.9.621 — terfi etmiş attribute kolonları prod'da hiç çalışmadı:
// ifade 'CHANNEL_CODE' okuyordu, veri 'channel_code' yazıyordu, yani
// kolon v0.9.198'den beri boştu. Bu testler onarımın üç özelliğini
// çiviliyor: ifade iki yazımı da okur, onarım gerektiğinde DROP+ADD
// üretir, ve gerekmediğinde HİÇBİR ŞEY üretmez (yoksa her boot kolonu
// düşürür).

func TestPromotedAttrExprReadsBothSpellings(t *testing.T) {
	got := promotedAttrExpr([]string{"CHANNEL_CODE", "channel_code"})
	for _, want := range []string{"'CHANNEL_CODE'", "'channel_code'", "coalesce(", "nullIf("} {
		if !strings.Contains(got, want) {
			t.Fatalf("ifade %q içermeli, üretilen: %s", want, got)
		}
	}
	// coalesce SIRASI davranışı belirliyor: iki yazım birden varsa
	// BÜYÜK harfli kazanır. probePromotedAttrs o durumda küçük harfli
	// yazımı kaydetmez — yanlış sonuç yerine yavaş sonuç.
	if strings.Index(got, "'CHANNEL_CODE'") > strings.Index(got, "'channel_code'") {
		t.Fatalf("BÜYÜK harf yazım coalesce'ta önce gelmeli: %s", got)
	}
	// Sonda '' olmalı: aksi halde kolon Nullable semantiğine kayar.
	if !strings.HasSuffix(got, ", '')") {
		t.Fatalf("ifade boş-string fallback ile bitmeli: %s", got)
	}
}

func TestPromotedAttrNeedsRepair(t *testing.T) {
	keys := []string{"CHANNEL_CODE", "channel_code"}
	cases := []struct {
		name string
		have string
		want bool
	}{
		{
			// v0.9.198'den beri prod'da duran ifade — küçük harf YOK.
			name: "prod'daki bozuk ifade",
			have: "attr_values[indexOf(attr_keys, 'CHANNEL_CODE')]",
			want: true,
		},
		{
			name: "yalnız küçük harf de eksik sayılır",
			have: "attr_values[indexOf(attr_keys, 'channel_code')]",
			want: true,
		},
		{
			name: "onarılmış ifade",
			have: promotedAttrExpr(keys),
			want: false,
		},
		{
			// CH ifadeyi normalize ederek saklıyor; anahtar-geçiyor-mu
			// testi boşluk/parantez farklarına dayanıklı olmalı, yoksa
			// her boot gereksiz bir DROP+ADD koşar.
			name: "CH normalizasyonu onarımı TETİKLEMEZ",
			have: "coalesce(nullIf(attr_values[indexOf(attr_keys, 'CHANNEL_CODE')], ''), nullIf(attr_values[indexOf(attr_keys, 'channel_code')], ''), '')",
			want: false,
		},
	}
	for _, c := range cases {
		if got := promotedAttrNeedsRepair(c.have, keys); got != c.want {
			t.Errorf("%s: needsRepair=%v, beklenen %v (have=%q)", c.name, got, c.want, c.have)
		}
	}
}

func TestPromotedAttrDDL(t *testing.T) {
	a := promotedAttr{col: "attr_channel_code", keys: []string{"CHANNEL_CODE", "channel_code"}}

	t.Run("kolon yok → yalnız ADD", func(t *testing.T) {
		got := promotedAttrDDL(a, "", false)
		if len(got) != 1 || !strings.Contains(got[0], "ADD COLUMN IF NOT EXISTS") {
			t.Fatalf("tek bir ADD beklenir, alınan: %v", got)
		}
		if strings.Contains(got[0], "DROP") {
			t.Fatal("var olmayan kolon için DROP gönderilmemeli")
		}
	})

	t.Run("ifade doğru → HİÇBİR ŞEY", func(t *testing.T) {
		// En önemli vaka: burada boş dönmezse her boot kolonu düşürür
		// ve okuma tarafı sürekli dizi yoluna geri düşer.
		if got := promotedAttrDDL(a, promotedAttrExpr(a.keys), true); got != nil {
			t.Fatalf("onarım gerekmiyorken DDL üretildi: %v", got)
		}
	})

	t.Run("ifade bozuk → DROP sonra ADD", func(t *testing.T) {
		got := promotedAttrDDL(a, "attr_values[indexOf(attr_keys, 'CHANNEL_CODE')]", true)
		if len(got) != 2 {
			t.Fatalf("iki ifade beklenir, alınan: %v", got)
		}
		if !strings.Contains(got[0], "DROP COLUMN") {
			t.Fatalf("önce DROP gelmeli, alınan: %s", got[0])
		}
		if !strings.Contains(got[1], "ADD COLUMN") {
			t.Fatalf("sonra ADD gelmeli, alınan: %s", got[1])
		}
		// MODIFY ASLA kullanılmamalı: ölçüldü, var olan kolonun
		// ifadesini değiştirmek eski part'ları ONARMIYOR — tarihsel
		// veri boş kalır ve filtreyi kolona yönlendirmek YANLIŞ
		// sonuç verir.
		for _, q := range got {
			if strings.Contains(q, "MODIFY COLUMN") {
				t.Fatalf("MODIFY eski part'ları onarmaz, kullanılmamalı: %s", q)
			}
		}
	})
}

// Bu test bir HATA DEĞİL, bir TUZAĞI çiviliyor: onarım ifadeleri
// `alters` listesine konursa planAlterDDL (v0.9.608) DROP'u geçirir
// ama ADD'i ELER — çünkü boot'ta aldığı kolon anlık görüntüsü kolonu
// hâlâ "var" gösterir. Sonuç: kolon düşer ve hiç geri gelmez.
//
// repairPromotedAttrCols bu yüzden doğrudan s.execDDL kullanıyor.
// Biri bir gün "tutarlılık olsun" diye onları alters'a taşırsa bu test
// ne olacağını gösterir.
func TestPromotedAttrDDLMustBypassAlterPlanner(t *testing.T) {
	a := promotedAttr{col: "attr_channel_code", keys: []string{"CHANNEL_CODE", "channel_code"}}
	stmts := promotedAttrDDL(a, "attr_values[indexOf(attr_keys, 'CHANNEL_CODE')]", true)

	// Boot anlık görüntüsü: kolon HÂLÂ var (DROP daha koşmadı).
	snapshot := map[string]bool{"spans.attr_channel_code": true}
	send, skipped := planAlterDDL(stmts, snapshot)

	if skipped != 1 {
		t.Fatalf("planlayıcı ADD'i elemeliydi (tuzağın kendisi), skipped=%d", skipped)
	}
	if len(send) != 1 || !strings.Contains(send[0], "DROP COLUMN") {
		t.Fatalf("geriye yalnız DROP kalmalıydı, kalan: %v", send)
	}
	// Yani: bu ifadeler planlayıcıdan GEÇİRİLMEZ.
	for _, q := range send {
		if strings.Contains(q, "ADD COLUMN") {
			t.Fatal("beklenmedik: ADD hayatta kaldı — tuzak kapanmış olabilir, yorumları güncelle")
		}
	}
}
