package api

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/cilcenk/coremetry/internal/auth"
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

// addresseeLine — sohbet sistem prompt'unun başına eklenen "kiminle
// konuşuyorsun" bölümü (v0.9.528, Faz 2). İkisi de boşsa "" döner ve
// prompt bayt-bayt eskisi kalır.
//
// İki somut kazanç, ikisi de operatöre görünür:
//
//  1. Model konuşma ORTASINDA adıyla hitap edebilir. Karşılama (Faz 1)
//     deterministik ve tek satırlıktı; buradan sonrası modelin.
//  2. Model ROLÜ bilir. Bugün bilmiyor ve viewer'a "şu ayarı değiştir",
//     "bu kuralı sustur" diyebiliyor — kullanıcının yapamayacağı bir
//     eylem. Yanlış tavsiye, yardımın olmamasından kötüdür: operatör
//     denemek için zaman harcar ve asistana güveni azalır.
//
// Rol adları auth paketinin sabitleriyle aynı: admin | editor | viewer.
func addresseeLine(firstName, role string) string {
	name := strings.TrimSpace(firstName)
	role = strings.ToLower(strings.TrimSpace(role))
	if name == "" && role == "" {
		return ""
	}

	var b strings.Builder
	b.WriteString("KONUŞTUĞUN KİŞİ: ")
	if name != "" {
		b.WriteString(name)
	} else {
		b.WriteString("(adı bilinmiyor)")
	}
	if role != "" {
		b.WriteString(" · rol: ")
		b.WriteString(role)
	}
	b.WriteString("\n")

	if name != "" {
		b.WriteString("- Uygun düştüğünde adıyla hitap et; her cümlede TEKRARLAMA.\n")
	}
	// Yetki uyarısı YALNIZ viewer için. admin/editor'e kısıt cümlesi
	// eklemek prompt'u boşuna şişirir ve modeli gereksiz temkinli yapar.
	if role == "viewer" {
		b.WriteString("- Bu kullanıcının YALNIZ OKUMA yetkisi var: ayar değiştiremez, " +
			"kural/alarm oluşturamaz, problem susturamaz. Ona bu eylemleri ÖNERME; " +
			"bunun yerine ne bulduğunu söyle ve kime iletmesi gerektiğini belirt.\n")
	}
	return b.String()
}

// withAddressee — sistem prompt'una eklenecek ön-sözü metnin BAŞINA
// koyar. Ön-söz boşsa metin hiç değişmez.
func withAddressee(prefix, systemPrompt string) string {
	if prefix == "" {
		return systemPrompt
	}
	return prefix + "\n" + systemPrompt
}

// Hitap ön-sözü ctx ile taşınır (WithMeta / WithJSONMode deseninin
// aynısı): guided yolu, çekmece sohbeti ve serbest döngü aynı isteğin
// ctx'ini paylaşıyor, yani üç yol da tek yerden beslenir ve hiçbirinin
// imzası değişmez.
type addresseeKey struct{}

func ctxWithAddressee(ctx context.Context, prefix string) context.Context {
	if prefix == "" {
		return ctx
	}
	return context.WithValue(ctx, addresseeKey{}, prefix)
}

func addresseeFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(addresseeKey{}).(string)
	return v
}

// chatAddressee — oturumdaki kullanıcının hitap ön-sözünü üretir.
//
// Ad için kullanıcı satırı gerekir (full_name JWT'de YOK, yalnız
// users tablosunda). Okuma /api/auth/me'nin 30s cache'inden geçer:
// sohbet başına yeni bir FINAL satır okuması eklemek, bir prompt
// cümlesi için ödenecek yanlış bir bedel olurdu.
//
// Cache ıskalar ve satır okunamazsa ROL yine de claim'den gelir —
// yetki uyarısı adın varlığına bağlı değil, ve asıl koruyucu o.
func (s *Server) chatAddressee(ctx context.Context, c *auth.Claims) string {
	if c == nil {
		return ""
	}
	first := ""
	if u, ok := s.meUsers.get(c.UserID, time.Now()); ok && u != nil {
		first = firstNameFrom(u.FullName, u.Email)
	} else if u, err := s.store.GetUserByID(ctx, c.UserID); err == nil && u != nil {
		s.meUsers.put(c.UserID, u, time.Now())
		first = firstNameFrom(u.FullName, u.Email)
	} else {
		// Satır okunamadı: e-postadan türetmeyi dene, claim'de o var.
		first = firstNameFromEmail(c.Email)
	}
	return addresseeLine(first, c.Role)
}

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
