package api

import "time"

// chat_deadline.go — sohbet alışverişinin UÇTAN UCA tavanı
// (v0.10.24, Copilot denetimi bulgusu).
//
// ── KUSUR ───────────────────────────────────────────────────────────────
//
// Hiçbir katmanda uçtan uca sınır YOKTU:
//
//	handler        → yalnız tool BAŞINA 20s (copilot_chat.go)
//	http.Server    → api.go:1390 `&http.Server{Addr, Handler}` — tavan yok
//	istemci        → api.ts ham fetch, `request()`in 60s tavanını atlıyor
//	sağlayıcı      → çağrı BAŞINA 180s (operatör 10-600s ayarlayabiliyor)
//
// `http.Server`da tavan olmaması aslında DOĞRU ve bilinçli: sunucu geneli
// bir `WriteTimeout` her SSE akışını keserdi. Eksik olan, sohbet yoluna
// özgü bir deadline.
//
// ── EN KÖTÜ HÂL: İLK HESABIM FAZLA KÖTÜMSERDİ ───────────────────────────
//
// "5 tur × 180s = 15 dk" diye başladım; yanlıştı. Döngü bir sağlayıcı
// hatasında `break` ediyor (copilot_chat.go), yani dolu bir timeout turu
// döngüyü SÜRDÜRMÜYOR, bitiriyor.
//
// Gerçekten sınırsız olan eksen başka ve daha sinsi: **tur başına tool
// çağrısı sayısı tavansız**. Her tool 20s taşıyor ama kaç tane
// çağrılacağının sınırı yok, yani tek bir tur N × 20s sürebilir.
//
// Tek bir uçtan uca deadline ikisini birden kapatıyor: tool bağlamı
// (copilot_chat.go'daki `context.WithTimeout(ctx, 20s)`) bu ctx'ten
// TÜREDİĞİ için deadline dolduğunda uçan tool çağrıları da iptal olur.
//
// ── DEĞER NEDEN TÜRETİLİYOR, SABİT DEĞİL ────────────────────────────────
//
// Sabit bir sayı (ör. 300s) operatör çağrı-başı tavanı 600s'ye çektiğinde
// TEK bir meşru çağrıdan kısa kalırdı — yani düzeltme, çalışan bir
// kurulumu bozardı. Tavan bu yüzden yapılandırılmış değere bağlı.
//
// ⚠ Bu bir ARKA DURAK, UX hedefi değil. Operatörün 9 dakika spinner'a
// bakması zaten kötü; onun asıl çaresi serbest döngünün akıtılması
// (ayrı kuyruk maddesi) ve Durdur düğmesi (v0.10.23). Buradaki iş,
// hiçbir şey olmadığında bağlantının sonsuza kadar asılı kalmaması.

const (
	// chatDeadlineFactor — çağrı-başı tavanın kaç katı.
	//
	// 3: bir alışveriş birden çok tur koşabiliyor (chatMaxToolRounds = 5)
	// ve hepsinin dolu tavana yaklaşması, model YAVAŞ ama ÇALIŞIYOR
	// demek. 2 seçseydim gerçekten ilerleyen bir soruşturmayı keserdim.
	chatDeadlineFactor = 3
	// Alt sınır: operatör tavanı 10s'ye indirse bile 30s'lik bir uçtan
	// uca pencere anlamsız olur; alışveriş daha başlamadan ölür.
	chatDeadlineMin = 180 * time.Second
	// Üst sınır: 600s × 3 = 30 dk, ki bu "sonsuz"dan pratik olarak
	// ayırt edilemez. 15 dk hâlâ uzun ama sonlu.
	chatDeadlineMax = 900 * time.Second
)

// chatExchangeTimeout — bir sohbet alışverişinin uçtan uca tavanı.
//
// Saf: değer bir POLİTİKA ve politikanın testi, çalışan bir LLM'e
// bağlı olmamalı.
func chatExchangeTimeout(perCall time.Duration) time.Duration {
	if perCall <= 0 {
		// Yapılandırma okunamadı; ölçülmemiş bir çarpan üretmektense
		// alt sınırı kullan.
		return chatDeadlineMin
	}
	d := perCall * chatDeadlineFactor
	if d < chatDeadlineMin {
		return chatDeadlineMin
	}
	if d > chatDeadlineMax {
		return chatDeadlineMax
	}
	return d
}

// chatDeadlineMessageTR — tavan dolduğunda operatöre ne denecek.
//
// Ham `context deadline exceeded` metni burada YETMEZ: operatör bunu
// "model zaman aşımına uğradı" diye okur ve modeli suçlar, oysa olan şey
// ALIŞVERİŞİN tavana dayanmasıdır — farklı bir eylem gerektiriyor
// (soruyu daralt, tek bir servise sor).
func chatDeadlineMessageTR(d time.Duration) string {
	return "Bu alışveriş " + fmtChatDeadlineTR(d) + " tavanına dayandı ve durduruldu. " +
		"Soru muhtemelen çok geniş: tek bir servise ya da daha dar bir zaman aralığına sorun."
}

func fmtChatDeadlineTR(d time.Duration) string {
	if m := int(d / time.Minute); m >= 1 {
		return itoaChat(m) + " dakika"
	}
	return itoaChat(int(d/time.Second)) + " saniye"
}

func itoaChat(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
