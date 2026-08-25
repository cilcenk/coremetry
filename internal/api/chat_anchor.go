package api

import "time"

// chat_anchor.go — sohbetin zaman ÇIPASI (v0.10.33, Copilot denetiminin
// #5 sıradaki sınırı).
//
// ── KUSUR ───────────────────────────────────────────────────────────────
//
// Ekrandaki zaman aralığı sohbete yalnız SÜRE olarak taşınıyordu:
//
//	frontend: rangeS = round((to - from) / 1e9)     ← mutlak pencere çöküyor
//	sunucu:   to := time.Now(); from := to - rangeS ← her zaman ŞİMDİ
//
// Operatör dün gece 03:00-04:00 arasındaki bir olaya zoom yapıp "burada
// ne oldu" diye sorduğunda, sohbet aynı UZUNLUKTA ama BUGÜNKÜ pencereyi
// cevaplıyordu. Cevap makul görünüyor, sayılar gerçek, kaynak doğru —
// yalnız YANLIŞ ZAMAN DİLİMİNDEN. Operatör bunu fark edemez ve o veriyle
// karar verir.
//
// v0.10.32 bunun YARISINI çözdü: uzunluk artık modele doğru gidiyor.
// Kalan yarısı çıpa.
//
// ── NEDEN GÖRELİ ARALIK ÇIPALANMAMALI ───────────────────────────────────
//
// "Son 1 saat" seçiliyken çıpayı sohbetin açıldığı ana sabitlemek, uzun
// bir soruşturmada cevabı DONDURUR: operatör yirmi dakika sonra "şimdi
// nasıl" diye sorduğunda hâlâ yirmi dakika önceki pencereyi görür.
// Göreli aralık `now()`a çapalanmaya DEVAM ediyor; çıpa yalnız operatör
// MUTLAK bir pencere seçtiğinde (custom/zoom) taşınıyor.
//
// ── NEDEN İSTEMCİ DEĞERİ DOĞRULANIYOR ───────────────────────────────────
//
// Çıpa istemciden geliyor ve istemci saati kayabilir (ya da istek
// elle kurulabilir). Gelecekteki bir çıpa boş pencere üretir; çok eski
// bir çıpa saklama ufkunun dışını sorar ve yine boş döner. İkisi de
// "veri yok" gibi görünür, oysa sebep çıpadır. Doğrulama bu yüzden
// sessiz değil: geçersiz çıpa REDDEDİLİP şimdiye düşülüyor.

const (
	// chatAnchorMaxFutureSkew — istemci saatinin ileri kayma toleransı.
	// Bunun ötesi kabul edilmiyor: gelecekten veri yok.
	chatAnchorMaxFutureSkew = 5 * time.Minute
	// chatAnchorMaxAge — bu kadar eski bir çıpa reddediliyor. En uzun
	// saklama ufkundan (spans, ayarlanabilir) belirgin biçimde geniş;
	// amaç eski pencereyi yasaklamak değil, saçma değeri elemek.
	chatAnchorMaxAge = 400 * 24 * time.Hour
)

// chatAnchorTime — pencerenin BİTİŞ anı.
//
// toMs <= 0 → göreli aralık, şimdiye çapala.
// Geçersiz (gelecek / çok eski) → şimdiye çapala.
//
// İkinci dönüş, çıpanın GERÇEKTEN taşındığını söylüyor; çağıran bunu
// operatöre ilan ediyor, çünkü sessizce şimdiye düşmek kusurun aynısını
// bir kez daha üretirdi.
func chatAnchorTime(toMs int64, now time.Time) (time.Time, bool) {
	// ⚠ Bu muhafız ile aşağıdaki YAŞ kontrolü toMs=0 için BİRBİRİNİ
	// GÖLGELİYOR: UnixMilli(0) = 1970 ve yaş kontrolü onu zaten
	// reddediyor, o yüzden `<= 0`ı `< 0`a çevirmek davranışı DEĞİŞTİRMEZ
	// (mutasyon denetiminde ölçüldü). Isırmayan mutasyon ölü dal demek
	// değil: bu muhafız NİYETİ taşıyor — "göreli aralık" ile "saçma
	// değer" farklı şeyler ve ikisi ayrı okunabilmeli.
	if toMs <= 0 {
		return now, false
	}
	t := time.UnixMilli(toMs)
	if t.After(now.Add(chatAnchorMaxFutureSkew)) {
		return now, false
	}
	if t.Before(now.Add(-chatAnchorMaxAge)) {
		return now, false
	}
	return t, true
}
