package api

import "testing"

// v0.9.528 — CoSRE karşılaması operatörün adını kullanıyor. Adın kaynağı
// LDAP `displayName` ve operatörün AD'sinde bu alan BİLEŞİK:
// "Ad Soyad (Bölüm) * ÜNVAN-Ekip". Ham basmak karşılamayı saçmalatırdı.
//
// Bu testin ikinci işi Türkçe büyük/küçük harf tuzağını pinlemek:
// Go'nun unicode.ToLower'ı 'İ' için birleşik-noktalı "i̇", 'I' için
// noktalı 'i' üretir. İkisi de yanlış harf, ve ekranda ADIN yanlış
// yazılması operatöre doğrudan görünür.

func TestFirstNameFromDisplay(t *testing.T) {
	cases := []struct{ name, in, want string }{
		// Operatörün AD şekli — asıl hedef.
		{"bileşik displayName", "Fatih Yılmaz (Bilgi Teknolojileri) * UZMAN-APM", "Fatih"},
		{"bileşik, tire ayraçlı", "Fatih Yılmaz (BT) - UZMAN-APM", "Fatih"},
		{"parantezsiz yıldızlı", "Fatih Yılmaz * UZMAN", "Fatih"},

		// Düz şekiller.
		{"sade ad soyad", "Fatih Yılmaz", "Fatih"},
		{"tek kelime", "Fatih", "Fatih"},
		{"baş/son boşluk", "  Fatih Yılmaz  ", "Fatih"},
		{"çoklu boşluk", "Fatih   Yılmaz", "Fatih"},

		// Soyad-önce dizin şekli.
		{"virgüllü", "YILMAZ, Fatih", "Fatih"},
		{"virgüllü + bileşik", "YILMAZ, Fatih (BT) * UZMAN", "Fatih"},

		// BÜYÜK HARF dizin kaydı — Türkçe küçültme.
		{"büyük harf İ", "FATİH YILMAZ", "Fatih"},
		{"büyük harf I", "IŞIL DEMİR", "Işıl"},
		{"büyük harf Ç/Ğ/Ü", "GÜLÇİN ÖZ", "Gülçin"},
		{"karışık yazım dokunulmaz", "Fatih Yılmaz", "Fatih"},

		// Ada benzemeyenler → boş. "Merhaba Sre-Team" demektense sessiz.
		{"boş", "", ""},
		{"yalnız boşluk", "   ", ""},
		{"tek harf", "F Yılmaz", ""},
		{"rakam içeren", "user123 X", ""},
		{"parantezle başlıyor", "(Bilgi Teknolojileri)", ""},
		{"çok uzun token", "Abcdefghijklmnopqrstuvwxyz Y", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := firstNameFromDisplay(c.in); got != c.want {
				t.Errorf("firstNameFromDisplay(%q) = %q, beklenen %q", c.in, got, c.want)
			}
		})
	}
}

// E-posta türetmesi YALNIZ noktalı yerel kısımda çalışır. Noktasız
// adreste çıkan şey ad değil kullanıcı kodudur ("Merhaba Fyilmaz"
// isimsiz karşılamadan kötüdür) — operatör kararı, 2026-08-02.
func TestFirstNameFromEmail(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"nokta ayraçlı", "fatih.yilmaz@banka.com.tr", "Fatih"},
		{"üç parçalı", "fatih.can.yilmaz@x.com", "Fatih"},
		{"büyük harfli", "FATIH.YILMAZ@x.com", "Fatih"},

		{"noktasız", "fyilmaz@x.com", ""},
		{"rol hesabı", "admin@x.com", ""},
		{"tireli takım", "sre-team@x.com", ""},
		{"@ yok", "fatih.yilmaz", ""},
		{"boş", "", ""},
		{"nokta ile başlıyor", ".yilmaz@x.com", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := firstNameFromEmail(c.in); got != c.want {
				t.Errorf("firstNameFromEmail(%q) = %q, beklenen %q", c.in, got, c.want)
			}
		})
	}
}

// Öncelik: dizin adı e-postayı EZER. Dizin gerçek adı taşır, e-posta
// bir tahmindir.
func TestFirstNameFromPrefersDirectory(t *testing.T) {
	got := firstNameFrom("Fatih Yılmaz (BT) * UZMAN", "baska.isim@x.com")
	if got != "Fatih" {
		t.Errorf("dizin adı öncelikli olmalı, got %q", got)
	}
	// Dizin adı ada benzemiyorsa e-postaya düş.
	if got := firstNameFrom("(BT)", "fatih.yilmaz@x.com"); got != "Fatih" {
		t.Errorf("dizin kullanılamazsa e-postaya düşmeli, got %q", got)
	}
	// İkisi de yoksa boş — çağıran isimsiz karşılamaya geçer.
	if got := firstNameFrom("", "admin@x.com"); got != "" {
		t.Errorf("kaynak yokken boş dönmeli, got %q", got)
	}
}

// Türkçe küçültme doğrudan: Go'nun unicode.ToLower'ı burada YANLIŞ
// sonuç verir, o yüzden ayrı pinleniyor.
func TestLowerTR(t *testing.T) {
	// Türkçe kip: 'I' dotsuz 'ı'dır.
	for in, want := range map[rune]rune{
		'I': 'ı', 'İ': 'i', 'A': 'a', 'Ç': 'ç', 'Ğ': 'ğ', 'Ö': 'ö', 'Ş': 'ş', 'Ü': 'ü',
	} {
		if got := lowerTR(in, true); got != want {
			t.Errorf("lowerTR(%q, tr) = %q, beklenen %q", in, got, want)
		}
	}
	// ASCII kip: 'I' noktalı 'i'dir (çevrilmiş "FATIH" → "Fatih").
	if got := lowerTR('I', false); got != 'i' {
		t.Errorf("lowerTR('I', ascii) = %q, beklenen 'i'", got)
	}
	// 'İ' her iki kipte de 'i' — birleşik-noktalı "i̇" ASLA çıkmamalı.
	for _, tr := range []bool{true, false} {
		if got := lowerTR('İ', tr); got != 'i' {
			t.Errorf("lowerTR('İ', %v) = %q, beklenen 'i'", tr, got)
		}
	}
}

// 'I' belirsizliği dizeden çözülür: Türkçe harf varsa Türkçe klavye
// kipi, yoksa ASCII çevrimi. Belirsizliğin kendisi burada belgeleniyor —
// aynı harf iki farklı doğru cevaba sahip.
func TestTitleCaseIAmbiguity(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"ASCII çevrimi: I = i", "FATIH", "Fatih"},
		{"Türkçe klavye: I = ı", "IŞIL", "Işıl"},
		{"Türkçe harf İ ile", "FATİH", "Fatih"},
		{"küçük ASCII büyütülür", "fatih", "Fatih"},
		{"küçük Türkçe: i = İ", "işıl", "İşıl"},
		{"karışık yazım korunur", "McDonald", "McDonald"},
		{"karışık yazım korunur 2", "Fatih", "Fatih"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := titleCaseTR(c.in); got != c.want {
				t.Errorf("titleCaseTR(%q) = %q, beklenen %q", c.in, got, c.want)
			}
		})
	}
}
