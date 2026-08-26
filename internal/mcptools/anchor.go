package mcptools

import (
	"context"
	"time"
)

// anchor.go — ARAÇ PENCERESİNİN SONU (v0.10.50).
//
// ── DÜZELTİLEN KUSUR ────────────────────────────────────────────────────
//
// v0.10.33 operatörün geçmişe zoom'unu (mutlak pencere) sohbete taşımıştı
// — ama YALNIZ guided kademesinde gerçekten uyguluyordu. Serbest tool
// döngüsünde çıpa iki yerde İLAN EDİLİYOR:
//
//	• operatöre: "pencere: 2026-08-25 04:00 UTC'de bitiyor" çipi
//	• modele:    "⚠ pencere GEÇMİŞE sabitlenmiş … Cevabı o ana göre yaz"
//
// ...ama araçların HİÇBİRİ onu görmüyordu: `rangeWindow` koşulsuz
// `to = time.Now()` kuruyor ve hiçbir tool mutlak from/to argümanı almıyor
// (ev kuralı: küçük modele epoch nanosaniye hesaplatma). Sonuç: model
// BUGÜNÜN sayısını okuyup, önsöze uyarak DÜNÜN penceresi diye yazıyordu.
//
// ⚠ Bu, düzeltmenin kusuru KÖTÜLEŞTİRDİĞİ bir durumdu. v0.10.32'de cevap
// sessizce yanlış pencereden geliyordu; v0.10.33'ten sonra hem çip hem
// cevap metni operatöre YANLIŞ pencereyi TEYİT ediyordu. Etiketli yanlış,
// etiketsiz yanlıştan daha tehlikeli — çünkü sorgulanmıyor.
//
// ── NEDEN CONTEXT, NEDEN TOOL ARGÜMANI DEĞİL ────────────────────────────
//
// Çıpayı bir tool argümanı yapmak, modelden epoch nanosaniye üretmesini
// istemek demekti; deponun ev kuralı bunu açıkça yasaklıyor (pivots.go
// başlığı) ve [[project-copilot-runtime]] küçük modelde aritmetik
// yaptırmamayı doktrin olarak koyuyor. Çıpa MODELİN kararı değil,
// OPERATÖRÜN ekran durumu — o yüzden modelin göremediği bir kanaldan,
// context'ten geçiyor.
//
// ── NEDEN İMZA DEĞİŞTİ ──────────────────────────────────────────────────
//
// `rangeWindow`ın yanına ctx alan İKİNCİ bir fonksiyon koymak daha az
// dokunuş olurdu ama 24 çağrı yerinden birinin eskisinde kalması SESSİZ
// bir kusur üretirdi ve hiçbir şey söylemezdi — bu deponun tekrar eden
// sınıfı ([[feedback-gate-single-spelling]]). İmzayı değiştirmek kapıyı
// DERLEYİCİYE yaptırıyor: unutulan çağrı yeri derlenmiyor.

type anchorKey struct{}

// WithAnchor — bu context'te koşan araçların pencere SONUNU sabitler.
//
// Sıfır zaman "çıpa yok" demek ve context'e hiç yazılmaz: göreli bir
// pencereyi mutlakmış gibi sabitlemek, düzeltmenin kendi üreteceği yeni
// bir yanlış olurdu (v0.10.33'ün ikinci yarısındaki aynı ayrım).
func WithAnchor(ctx context.Context, to time.Time) context.Context {
	if to.IsZero() {
		return ctx
	}
	return context.WithValue(ctx, anchorKey{}, to)
}

// anchorOf — context'teki çıpa; yoksa sıfır zaman.
func anchorOf(ctx context.Context) time.Time {
	if ctx == nil {
		return time.Time{}
	}
	if t, ok := ctx.Value(anchorKey{}).(time.Time); ok {
		return t
	}
	return time.Time{}
}

// nowOrAnchor — "şimdi", ama çıpa varsa ÇIPA.
//
// `rangeWindow` tek geçit değil: birkaç handler kendi penceresini elle
// kuruyor (heap penceresi, deploy geri-bakışı, exception örneklem kesimi).
// Onlar `rangeWindow`dan geçmediği için imza değişikliği yakalamadı —
// AST kapısı yakaladı (anchor_test.go).
//
// Bu yardımcı o yolların çıpayı ONURLANDIRMASININ tek yolu. `time.Now()`
// yerine bunu çağır; kapı zaten ham `time.Now()`u reddediyor.
func nowOrAnchor(ctx context.Context) time.Time {
	if t := anchorOf(ctx); !t.IsZero() {
		return t
	}
	return time.Now()
}
