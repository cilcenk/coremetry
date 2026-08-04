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

// pos — ifade listesinde ilk eşleşmenin sırası (-1 yoksa).
func pos(stmts []string, needle string) int {
	for i, q := range stmts {
		if strings.Contains(q, needle) {
			return i
		}
	}
	return -1
}

func TestPromotedAttrDDL(t *testing.T) {
	a := promotedAttr{col: "attr_channel_code", keys: []string{"CHANNEL_CODE", "channel_code"}}
	broken := "attr_values[indexOf(attr_keys, 'CHANNEL_CODE')]"
	good := promotedAttrExpr(a.keys)

	t.Run("taze kurulum → ADD COLUMN sonra ADD INDEX", func(t *testing.T) {
		got := promotedAttrDDL(a, "", false, false)
		if pos(got, "DROP") != -1 {
			t.Fatalf("var olmayan kolon için DROP gönderilmemeli: %v", got)
		}
		if c, i := pos(got, "ADD COLUMN"), pos(got, "ADD INDEX"); c == -1 || i == -1 || c > i {
			t.Fatalf("önce ADD COLUMN sonra ADD INDEX gelmeli: %v", got)
		}
	})

	t.Run("her şey yerinde → HİÇBİR ŞEY", func(t *testing.T) {
		// En önemli vaka: burada boş dönmezse her boot kolonu düşürür
		// ve tıkalı DDL kuyruğunda bedava olmayan turlar harcanır.
		if got := promotedAttrDDL(a, good, true, true); len(got) != 0 {
			t.Fatalf("onarım gerekmiyorken DDL üretildi: %v", got)
		}
	})

	t.Run("kolon doğru ama indeks yok → yalnız ADD INDEX", func(t *testing.T) {
		got := promotedAttrDDL(a, good, true, false)
		if len(got) != 1 || !strings.Contains(got[0], "ADD INDEX") {
			t.Fatalf("yalnız ADD INDEX beklenir, alınan: %v", got)
		}
	})

	t.Run("bozuk + indeks var → DROP INDEX EN BAŞTA", func(t *testing.T) {
		got := promotedAttrDDL(a, broken, true, true)
		di, dc := pos(got, "DROP INDEX"), pos(got, "DROP COLUMN")
		if di == -1 || dc == -1 {
			t.Fatalf("hem DROP INDEX hem DROP COLUMN beklenir: %v", got)
		}
		// ÖLÇÜLDÜ (CH 24.8): indeksi duran kolonu düşürmek
		// "Code: 47 … Cannot apply mutation because it breaks skip
		// index" ile REDDEDİLİYOR. Sıra tersine dönerse onarım
		// sessizce başarısız olur ve kolon bozuk kalır.
		if di > dc {
			t.Fatalf("DROP INDEX, DROP COLUMN'dan ÖNCE gelmeli (CH kod 47): %v", got)
		}
		if ac, ai := pos(got, "ADD COLUMN"), pos(got, "ADD INDEX"); ac < dc || ai < ac {
			t.Fatalf("sıra DROP INDEX → DROP COLUMN → ADD COLUMN → ADD INDEX olmalı: %v", got)
		}
	})

	t.Run("bozuk + indeks yok → DROP INDEX gönderilmez", func(t *testing.T) {
		got := promotedAttrDDL(a, broken, true, false)
		if pos(got, "DROP INDEX") != -1 {
			t.Fatalf("olmayan indeks için DROP INDEX gönderilmemeli: %v", got)
		}
		if pos(got, "ADD INDEX") == -1 {
			t.Fatal("indeks yine de eklenmeli")
		}
	})

	t.Run("MODIFY hiçbir dalda kullanılmaz", func(t *testing.T) {
		// Ölçüldü: var olan kolonun ifadesini değiştirmek eski
		// part'ları ONARMIYOR — tarihsel veri boş kalır ve filtreyi
		// kolona yönlendirmek YANLIŞ sonuç verir.
		for _, c := range [][2]bool{{true, true}, {true, false}, {false, false}} {
			for _, q := range promotedAttrDDL(a, broken, c[0], c[1]) {
				if strings.Contains(q, "MODIFY COLUMN") {
					t.Fatalf("MODIFY eski part'ları onarmaz: %s", q)
				}
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
	stmts := promotedAttrDDL(a, "attr_values[indexOf(attr_keys, 'CHANNEL_CODE')]", true, true)

	// Boot anlık görüntüsü: kolon HÂLÂ var (DROP daha koşmadı).
	snapshot := map[string]bool{"spans.attr_channel_code": true}
	send, skipped := planAlterDDL(stmts, snapshot)

	if skipped != 1 {
		t.Fatalf("planlayıcı ADD COLUMN'ı elemeliydi (tuzağın kendisi), skipped=%d", skipped)
	}
	// Tuzağın tam şekli: DROP COLUMN hayatta kalıyor, onu geri getirecek
	// ADD COLUMN eleniyor → kolon düşer ve HİÇ GERİ GELMEZ.
	if pos(send, "DROP COLUMN") == -1 {
		t.Fatalf("DROP COLUMN elenmemeliydi (regex'e uymuyor), kalan: %v", send)
	}
	if pos(send, "ADD COLUMN") != -1 {
		t.Fatal("beklenmedik: ADD COLUMN hayatta kaldı — tuzak kapanmış olabilir, yorumları güncelle")
	}
}
