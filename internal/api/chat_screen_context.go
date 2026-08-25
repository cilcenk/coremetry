package api

import (
	"fmt"
	"strings"
)

// chat_screen_context.go — serbest tool döngüsüne EKRAN BAĞLAMI
// (v0.10.32, Copilot denetiminin #1 sıradaki sınırı).
//
// ── KUSUR ───────────────────────────────────────────────────────────────
//
// Dört kademenin ilk üçü ekran bağlamını alıyordu (servis, operation,
// aralık, env, trace); serbest tool döngüsü HİÇBİRİNİ almıyordu:
//
//	loopPrompt := withAddressee(addressee, copilot.SystemPromptChat())
//
// `req.Context.*` guided/drawer/RAG'e ve link kurucusuna gidiyor, ama
// döngünün prompt'una asla. Üstelik prompt kendi varsayılanını dayatıyor:
//
//	"Zaman penceresi: … Operatör aksini söylemedikçe 1800 (30 dk) kullan."
//
// Sonuç: ekranda `checkout-service` açık ve 6 saatlik pencere seçiliyken
// sorulan "hata oranı ne" sorusu FİLO GENELİNE ve 30 DAKİKAYA gidiyor.
// Cevap makul görünüyor, kaynağı doğru, sayılar gerçek — yalnız SORULAN
// ŞEY DEĞİL. Ve cevapta bunu belirten hiçbir şey yok.
//
// ⚠ Bu, kademelerin en kötü yerinde: guided'ın ıskaladığı sorular buraya
// düşüyor, yani en ZOR ve en serbest sorularda model en az bağlama sahip.
//
// ── NEDEN SUNUCUDA KURULUYOR ────────────────────────────────────────────
//
// Modelden bağlamı "çıkarmasını" istemek (ör. konuşmadan servis adı
// tahmin etmesi) küçük modelde güvenilmez ve uydurma yüzeyi açar. Önsöz
// deterministik: ne verildiyse o yazılıyor, verilmeyen alan HİÇ
// yazılmıyor. Boş bir alanı "(bilinmiyor)" diye yazmak modele
// doldurulacak bir boşluk sunardı.
//
// ── NEDEN "AKSİNİ SÖYLEMEDİKÇE" ─────────────────────────────────────────
//
// Operatör tek servise bakarken pekâlâ "hangi servisler yavaş" diye
// filo sorusu sorabilir. Bağlam bir VARSAYILAN, kelepçe değil.

// ChatScreenContext — istekle gelen ekran bağlamı.
type ChatScreenContext struct {
	Service   string
	Operation string
	Env       string
	RangeS    int64
}

// Empty — ilan edilecek hiçbir şey yok mu.
func (c ChatScreenContext) Empty() bool {
	return strings.TrimSpace(c.Service) == "" &&
		strings.TrimSpace(c.Operation) == "" &&
		strings.TrimSpace(c.Env) == "" &&
		c.RangeS <= 0
}

// screenContextPreambleTR — döngü prompt'unun başına eklenen önsöz.
//
// Boş bağlamda BOŞ dize döner ve prompt'un kendi 1800 varsayılanı
// yürürlükte kalır — uydurma bir bağlam eklemek, hiç eklememekten kötü.
func screenContextPreambleTR(c ChatScreenContext) string {
	if c.Empty() {
		return ""
	}
	var b strings.Builder
	b.WriteString("EKRAN BAĞLAMI — operatör ŞU AN buna bakıyor:\n")
	if s := strings.TrimSpace(c.Service); s != "" {
		fmt.Fprintf(&b, "- servis: %s\n", s)
	}
	if o := strings.TrimSpace(c.Operation); o != "" {
		fmt.Fprintf(&b, "- operation: %s\n", o)
	}
	if e := strings.TrimSpace(c.Env); e != "" {
		fmt.Fprintf(&b, "- ortam: %s\n", e)
	}
	if c.RangeS > 0 {
		// ⚠ Bu satır prompt'taki "aksini söylemedikçe 1800 kullan"
		// kuralını EZMEK zorunda; ezmezse model iki çelişik talimat
		// alır ve küçük model çelişkide genelde İLK gördüğünü izler.
		fmt.Fprintf(&b, "- zaman aralığı: %s (range_s=%d) — tool çağrılarında "+
			"varsayılan 1800 YERİNE BUNU kullan\n", fmtRangeTR(c.RangeS), c.RangeS)
	}
	b.WriteString("Soru AKSİNİ SÖYLEMEDİKÇE tool argümanlarında bu değerleri kullan; " +
		"operatör açıkça daha geniş bir kapsam isterse (ör. \"tüm servisler\") onu izle.\n\n")
	return b.String()
}

// screenContextChipTR — operatöre GÖRÜNEN kısa özet.
//
// Bağlam sessizce uygulanırsa operatör cevabın neden o kapsamda
// olduğunu bilemez — v0.9.1259'da env için şeffaflık eklenmişti, aralık
// ve servis için eklenmemişti. Çip o boşluğu kapatıyor.
func screenContextChipTR(c ChatScreenContext) string {
	if c.Empty() {
		return ""
	}
	var parts []string
	if s := strings.TrimSpace(c.Service); s != "" {
		parts = append(parts, s)
	}
	if o := strings.TrimSpace(c.Operation); o != "" {
		parts = append(parts, o)
	}
	if e := strings.TrimSpace(c.Env); e != "" {
		parts = append(parts, "env="+e)
	}
	if c.RangeS > 0 {
		parts = append(parts, fmtRangeTR(c.RangeS))
	}
	return "ekran bağlamı: " + strings.Join(parts, " · ")
}

// fmtRangeTR — saniyeyi okunur pencereye çevirir.
func fmtRangeTR(s int64) string {
	switch {
	case s >= 86400 && s%86400 == 0:
		return fmt.Sprintf("son %d gün", s/86400)
	case s >= 3600 && s%3600 == 0:
		return fmt.Sprintf("son %d saat", s/3600)
	case s >= 60:
		return fmt.Sprintf("son %d dakika", s/60)
	default:
		return fmt.Sprintf("son %d saniye", s)
	}
}
