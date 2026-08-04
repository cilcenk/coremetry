package chstore

import (
	"strings"
	"testing"
)

// v0.9.624 — iş-boyutu kırılımı prod'da SESSİZCE BOŞ dönüyordu.
//
// businessDimKeys (anomaly/investigation.go:61) ve copilot kırılım
// listeleri (api/copilot_aianalyze.go:269, :375) "CHANNEL_CODE" sabitini
// taşıyor; prod verisi ise "channel_code" yazıyor. BusinessBreakdown'ın
// `WHERE <ifade> != ''` süzgeci hiçbir satır tutmuyor, yani operatörün
// v0.9.511/580'de istediği "hangi kanal patlıyor" kanıt bloğu ölüydü.
//
// Bu testler düzeltmenin üç özelliğini çiviliyor.

// Kolon KAYITLI DEĞİLKEN (probe doğrulayamadı ya da onarım henüz
// uygulanmadı) ifade HER İKİ yazımı da denemeli — aksi halde kırılım
// yine boş döner.
func TestBusinessDimTriesBothSpellingsWithoutColumn(t *testing.T) {
	if _, ok := promotedCols()["channel_code"]; ok {
		t.Fatal("ön koşul: bu testte harita boş olmalı")
	}
	expr, args := businessDimExpr("CHANNEL_CODE")

	if !strings.Contains(expr, "coalesce(") {
		t.Fatalf("her iki yazımı deneyen coalesce beklenir: %s", expr)
	}
	if n := strings.Count(expr, "indexOf(attr_keys, ?)"); n != 2 {
		t.Fatalf("iki yazım için iki arama beklenir, bulunan %d: %s", n, expr)
	}
	// Bind sırası SQL'deki placeholder sırasıyla birebir olmalı;
	// BusinessBreakdown bu args'ı iki kez splice ediyor.
	if len(args) != 2 || args[0] != "CHANNEL_CODE" || args[1] != "channel_code" {
		t.Fatalf("bind argümanları yazım sırasını izlemeli, alınan: %v", args)
	}
}

// Prod'un GERÇEK hâli: veri küçük harf, probe küçük harfli yazımı
// doğruladı. Kod içi BÜYÜK harfli sabit yine de kolona ulaşmalı.
func TestBusinessDimUppercaseConstantReachesLowercaseColumn(t *testing.T) {
	withPromoted(t, "channel_code", "attr_channel_code")

	expr, args := businessDimExpr("CHANNEL_CODE")
	if expr != "attr_channel_code" {
		t.Fatalf("BÜYÜK harfli sabit kayıtlı kolona ulaşmalıydı, alınan: %s", expr)
	}
	if len(args) != 0 {
		t.Fatalf("kolon yolunda bind olmamalı, alınan: %v", args)
	}
}

// Terfi listesinde OLMAYAN bir anahtar eski davranışta kalmalı: tek
// arama, tek bind, harf duyarlı. Yoksa rastgele bir attribute sessizce
// başka bir yazıma eşlenirdi.
func TestBusinessDimUnknownKeyUnchanged(t *testing.T) {
	expr, args := businessDimExpr("tenant_id")
	if expr != "attr_values[indexOf(attr_keys, ?)]" {
		t.Fatalf("bilinmeyen anahtar eski yolda kalmalı: %s", expr)
	}
	if len(args) != 1 || args[0] != "tenant_id" {
		t.Fatalf("anahtar aynen bind edilmeli, alınan: %v", args)
	}
}

// Kullanıcı FİLTRESİ harf duyarlı KALMALI. Ayrım bilinçli: OTel'de
// attribute anahtarları harf duyarlıdır ve operatörün yazdığı anahtarın
// sessizce başkasına eşlenmesi sürpriz olur. Kod içi sabit listeler bir
// KAVRAMI ifade eder, kullanıcı girdisi bir ANAHTARI.
func TestUserFilterStaysCaseSensitive(t *testing.T) {
	withPromoted(t, "channel_code", "attr_channel_code")

	sql, _, err := FilterExpr{Key: "CHANNEL_CODE", Op: "=", Values: []string{"030101"}}.SQL()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sql, "attr_channel_code") {
		t.Fatalf("kullanıcı filtresi yazımı harf duyarsız eşlememeli: %s", sql)
	}
}
