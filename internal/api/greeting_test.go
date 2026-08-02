package api

import (
	"strings"
	"testing"
)

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

// v0.9.528 Faz 2 — sohbet prompt'una eklenen "kiminle konuşuyorsun"
// ön-sözü. İki sözleşme pinleniyor:
//
//  1. Kaynak yoksa ön-söz BOŞ, dolayısıyla prompt bayt-bayt eskisi.
//     Boş bir başlık eklemek modele "bir kullanıcı var ama bilmiyorum"
//     der ve gereksiz temkin üretir.
//  2. Yetki uyarısı YALNIZ viewer'a çıkar. Bugün model rolü bilmiyor ve
//     viewer'a "bu ayarı değiştir" diyebiliyor — yapamayacağı bir
//     eylem. Yanlış tavsiye, yardımın olmamasından kötü.
func TestAddresseeLine(t *testing.T) {
	t.Run("kaynak yoksa boş", func(t *testing.T) {
		if got := addresseeLine("", ""); got != "" {
			t.Errorf("boş beklenirdi, got %q", got)
		}
		if got := addresseeLine("  ", "  "); got != "" {
			t.Errorf("yalnız boşluk da boş sayılmalı, got %q", got)
		}
	})

	t.Run("ad varsa hitap talimatı gelir", func(t *testing.T) {
		got := addresseeLine("Fatih", "admin")
		if !strings.Contains(got, "Fatih") {
			t.Errorf("ad geçmeli: %q", got)
		}
		if !strings.Contains(got, "adıyla hitap et") {
			t.Errorf("hitap talimatı olmalı: %q", got)
		}
		if !strings.Contains(got, "TEKRARLAMA") {
			t.Errorf("her cümlede tekrar etmeme uyarısı olmalı: %q", got)
		}
	})

	t.Run("viewer yetki uyarısı alır", func(t *testing.T) {
		got := addresseeLine("Fatih", "viewer")
		for _, want := range []string{"YALNIZ OKUMA", "ÖNERME", "kime iletmesi"} {
			if !strings.Contains(got, want) {
				t.Errorf("viewer uyarısında %q yok: %q", want, got)
			}
		}
	})

	t.Run("admin ve editor uyarı ALMAZ", func(t *testing.T) {
		// Kısıt cümlesini herkese eklemek prompt'u şişirir ve modeli
		// yetkisi olan kullanıcıya karşı da temkinli yapar.
		for _, role := range []string{"admin", "editor"} {
			got := addresseeLine("Fatih", role)
			if strings.Contains(got, "YALNIZ OKUMA") {
				t.Errorf("%s rolü yetki uyarısı almamalı: %q", role, got)
			}
			if !strings.Contains(got, role) {
				t.Errorf("%s rolü prompt'ta geçmeli: %q", role, got)
			}
		}
	})

	t.Run("rol büyük harfle gelse de tanınır", func(t *testing.T) {
		if !strings.Contains(addresseeLine("Fatih", "VIEWER"), "YALNIZ OKUMA") {
			t.Error("rol karşılaştırması büyük/küçük harfe duyarlı olmamalı")
		}
	})

	t.Run("ad yok rol var — uyarı yine çıkar", func(t *testing.T) {
		// Asıl koruyucu yetki uyarısı; adın çözülememesi onu
		// düşürmemeli.
		got := addresseeLine("", "viewer")
		if !strings.Contains(got, "YALNIZ OKUMA") {
			t.Errorf("ad olmasa da yetki uyarısı olmalı: %q", got)
		}
		if strings.Contains(got, "adıyla hitap et") {
			t.Errorf("ad yokken hitap talimatı olmamalı: %q", got)
		}
	})
}

// withAddressee ön-sözü BAŞA koyar ve boş ön-sözde metne DOKUNMAZ.
func TestWithAddressee(t *testing.T) {
	const prompt = "Sen Coremetry'nin asistanısın."
	if got := withAddressee("", prompt); got != prompt {
		t.Errorf("boş ön-sözde prompt değişmemeli, got %q", got)
	}
	got := withAddressee("KONUŞTUĞUN KİŞİ: Fatih\n", prompt)
	if !strings.HasPrefix(got, "KONUŞTUĞUN KİŞİ: Fatih") {
		t.Errorf("ön-söz BAŞTA olmalı: %q", got)
	}
	if !strings.HasSuffix(got, prompt) {
		t.Errorf("özgün prompt korunmalı: %q", got)
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

// v0.9.529 — ekrandan gelen aralık sabit basamaklara oturur.
//
// Neden: mutlak (custom from/to) aralık keyfi bir saniye sayısı üretir
// ve o değer guided prefetch'lerin sunucu cache anahtarlarına giriyor.
// Sınırsız kardinalite = her sorunun kendi cache satırı. Aynı sınıf
// v0.8.270'te ES tarafında bir kez yakalandı.
func TestSnapRangeS(t *testing.T) {
	for in, want := range map[int64]int64{
		0: 0, -5: 0, // bilgi yok

		// Tam basamaklar DEĞİŞMEZ — preset kullanan operatör (çoğunluk)
		// hiçbir kayma görmemeli.
		60: 60, 1800: 1800, 3600: 3600, 21600: 21600, 86400: 86400, 2592000: 2592000,

		// Aradaki değerler YUKARI oturur: pencere görüleni KAPSAMALI.
		1:      60,
		61:     300,
		1801:   3600,
		21917:  43200, // "6 saat 5 dakika 17 saniye" — gerçek custom aralık
		100000: 172800,

		// 30 günün üstü tavanlanır — sınırsız pencere CH'a gitmez.
		5000000: 2592000,
	} {
		if got := snapRangeS(in); got != want {
			t.Errorf("snapRangeS(%d) = %d, beklenen %d", in, got, want)
		}
	}
}

// Oturtma ASLA aşağı inmemeli: "6 saate bakıyorum ama cevap 3 saatlik"
// eksik rapordur ve biraz fazla okumadan kötüdür.
func TestSnapRangeSNeverShrinks(t *testing.T) {
	for _, v := range []int64{1, 59, 61, 301, 1799, 1801, 3599, 21917, 86401, 999999} {
		if got := snapRangeS(v); got < v {
			t.Errorf("snapRangeS(%d) = %d — pencere küçültülemez", v, got)
		}
	}
}
