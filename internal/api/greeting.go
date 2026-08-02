package api

import (
	"strings"
	"unicode"
)

// greeting.go — v0.9.528. CoSRE sohbetinin kullanıcıya adıyla hitap
// edebilmesi için ilk adı çıkarır.
//
// Neden SUNUCUDA: aynı değeri iki tüketici kullanacak — sohbetin
// karşılaması (frontend, /api/auth/me üzerinden) ve sistem prompt'u
// (backend). İki dilde iki ayrıştırıcı tutmak sessiz ayrışma demek.
//
// Girdi kaynağı `users.full_name`: LDAP login'de `displayName`'den
// yazılıyor (v0.8.266). Operatörün AD'sinde bu alan BİLEŞİK —
// "Ad Soyad (Bölüm) * ÜNVAN-Ekip" — ve ham basılırsa karşılama
// "Merhaba Fatih Yılmaz (Bilgi Teknolojileri) * UZMAN-APM" olurdu.

// firstNameFrom — hitap için kullanılacak ilk ad, yoksa "".
//
// Boş dönmek MEŞRU bir sonuç: local/OIDC hesapların full_name'i yok ve
// e-postadan güvenle ad türetilemeyen adresler var (admin@, fyilmaz@).
// Uydurulmuş bir ada göre "Merhaba Fyilmaz" demek, isimsiz karşılamadan
// daha kötü.
func firstNameFrom(fullName, email string) string {
	if n := firstNameFromDisplay(fullName); n != "" {
		return n
	}
	return firstNameFromEmail(email)
}

// firstNameFromDisplay — dizin displayName'inden ilk ad.
func firstNameFromDisplay(full string) string {
	s := strings.TrimSpace(full)
	if s == "" {
		return ""
	}
	// Bileşik kuyruğu at: "(Bölüm) * ÜNVAN-Ekip" ve benzerleri. Parantez
	// ya da yıldız gördüğümüz yerde ad kısmı bitmiştir.
	if i := strings.IndexAny(s, "(*"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	// "YILMAZ, Fatih" — dizinlerde yaygın soyad-önce şekli. Virgülden
	// SONRASI addır.
	if i := strings.Index(s, ","); i >= 0 {
		s = strings.TrimSpace(s[i+1:])
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return sanitizeName(fields[0])
}

// firstNameFromEmail — e-posta yerel kısmından ad; YALNIZ nokta varsa.
//
// "fatih.yilmaz@…" → Fatih. Noktasız adreslerde ("admin@", "fyilmaz@",
// "sre-team@") çıkacak şey ad değil kullanıcı kodudur; onunla hitap
// etmektense hiç hitap etmemek doğru.
func firstNameFromEmail(email string) string {
	local, _, ok := strings.Cut(strings.TrimSpace(email), "@")
	if !ok || !strings.Contains(local, ".") {
		return ""
	}
	head, _, _ := strings.Cut(local, ".")
	return sanitizeName(head)
}

// sanitizeName — aday token gerçekten bir ada benziyor mu, ve
// görüntülenebilir hâle getir.
func sanitizeName(tok string) string {
	tok = strings.Trim(tok, ".,;:-_'\"")
	if n := len([]rune(tok)); n < 2 || n > 20 {
		return ""
	}
	for _, r := range tok {
		if !unicode.IsLetter(r) && r != '\'' && r != '-' {
			return ""
		}
	}
	return titleCaseTR(tok)
}

// titleCaseTR — tek-biçim yazılmış adı görüntülenebilir hâle getirir.
//
// İki gerçek girdi var ve ikisi de düzeltme istiyor:
//
//	dizin  → "FATİH YILMAZ"  (BÜYÜK) — "Merhaba FATİH" bağırır
//	e-posta → "fatih.yilmaz"  (küçük) — "Merhaba fatih" özensiz
//
// Zaten karışık yazılmış bir ad ("Fatih", "McDonald") dizinin doğru
// biçimlendirdiği anlamına gelir; dokunulmaz.
//
// 'I' BELİRSİZ ve bu belirsizlik dizeden çözülemez:
//
//	"FATIH"  ASCII'ye çevrilmiş "FATİH"tir → I, i demek
//	"IŞIL"   Türkçe klavyeyle yazılmıştır  → I, ı demek
//
// Ayrım için tek sinyal metnin kendisi: dizede Türkçe'ye özgü harf
// (İıŞşĞğÜüÖöÇç) VARSA yazan Türkçe klavye kullanmıştır ve 'I' → 'ı';
// yoksa ASCII çevrimidir ve 'I' → 'i'. E-posta yerel kısımları Türkçe
// harf taşıyamaz, yani o yol her zaman ASCII kipinde çalışır.
//
// Go'nun kendi kuralları burada YETMEZ: unicode.ToLower('İ') birleşik
// noktalı "i̇" üretir, ToLower('I') noktalı 'i' verir — Türkçe kipte
// ikisi de yanlış harf ve ad ekranda yanlış yazılır.
func titleCaseTR(s string) string {
	rs := []rune(s)
	if len(rs) == 0 || isMixedCase(rs) {
		return s
	}
	tr := hasTurkishLetter(rs)
	out := make([]rune, 0, len(rs))
	out = append(out, upperTR(rs[0], tr))
	for _, r := range rs[1:] {
		out = append(out, lowerTR(r, tr))
	}
	return string(out)
}

// isMixedCase — hem büyük hem küçük harf içeriyor mu? İçeriyorsa
// kaynağın biçimlendirmesine güvenilir.
func isMixedCase(rs []rune) bool {
	var upper, lower bool
	for _, r := range rs {
		if !unicode.IsLetter(r) {
			continue
		}
		if unicode.IsUpper(r) {
			upper = true
		} else {
			lower = true
		}
	}
	return upper && lower
}

func hasTurkishLetter(rs []rune) bool {
	for _, r := range rs {
		switch r {
		case 'İ', 'ı', 'Ş', 'ş', 'Ğ', 'ğ', 'Ü', 'ü', 'Ö', 'ö', 'Ç', 'ç':
			return true
		}
	}
	return false
}

func lowerTR(r rune, turkish bool) rune {
	if turkish && r == 'I' {
		return 'ı'
	}
	if r == 'İ' {
		return 'i'
	}
	return unicode.ToLower(r)
}

func upperTR(r rune, turkish bool) rune {
	if turkish {
		switch r {
		case 'i':
			return 'İ'
		case 'ı':
			return 'I'
		}
	}
	return unicode.ToUpper(r)
}
