package auth

import "strings"

// secret_strength.go — imzalama anahtarının SESSİZCE zayıf olması
// (v0.10.4).
//
// `NewService` yalnız BOŞ anahtarı denetliyordu: boşsa ephemeral bir
// anahtar üretip logluyordu. Dolu ama YER TUTUCU bir anahtar ise hiçbir
// iz bırakmadan geçiyordu.
//
// Prod'da tam bu oldu: imzalama anahtarı `CHANGE_ME_openssl_rand_hex_32`
// değerindeydi — kurulum sırasında "burayı değiştir" diye bırakılmış
// dizge. Sistem tamamen sağlıklı görünüyordu.
//
// Neden ciddi: sunucu token'ları saklamıyor, yalnız İMZAYI doğruluyor.
// Anahtarı bilen biri kendi token'ını yazar, içine istediği rolü koyar,
// kendi imzasını basar — parola yok, giriş yok, hesap yok. Ve
// /api/users uçları (createUser · setUserRole · resetUserPassword)
// admin rolüne açık olduğu için forge edilmiş bir token KALICI bir
// hesaba dönüşebiliyor; yani anahtar sonradan döndürülse bile iz kalır.
//
// TASARIM: tespit BOOT'U DURDURMUYOR. Reddetmek, operatör anahtarı
// döndürmeden bir rollout başlattığında prod'u düşürürdü — ve bu kod
// tam da rollout sırasında ilk kez koşacak. Onun yerine iki kanal:
// yüksek sesli boot logu VE /admin/stats'ta görünür bir şerit. Yalnız
// log yeterli değil; v0.9.1384'te "uyarı yanıt gövdesinde kalır, kimse
// bakmaz" diye reddetmeyi seçmiştim, burada reddedemediğim için uyarıyı
// operatörün BAKTIĞI yere koyuyorum.

// weakSecretMarkers — yer tutucu olduğunu ilan eden dizgeler.
//
// Küçük harfe indirilmiş anahtarda ALT DİZE olarak aranıyor: gerçek
// örnek `CHANGE_ME_openssl_rand_hex_32` idi, yani işaret başta ama
// kuyruk serbest. Tam eşleşme arasaydım o değeri kaçırırdım.
var weakSecretMarkers = []string{
	"change_me", "changeme", "change-me",
	"replace_me", "replaceme", "replace-me",
	"your_secret", "yoursecret",
	"placeholder", "example", "sample",
	"insecure", "notsecret", "dummy",
}

// minSecretLen — bu uzunluğun altı zayıf sayılıyor.
//
// 32 seçildi çünkü evdeki reçete `openssl rand -hex 32` ve o 64 karakter
// üretiyor; yani doğru kurulmuş bir anahtar bu eşiğin iki katı. Eşik
// tahmin edilebilirliği ölçmüyor (entropi hesabı sahte-kesinlik olurdu),
// yalnız "elle yazılmış kısa bir parola" sınıfını yakalıyor.
const minSecretLen = 32

// WeakSecretReason — anahtar zayıfsa SEBEBİNİ döndürür, değilse "".
//
// Dönüş bir SEBEP dizgesi, bool değil: çağıran onu operatöre gösteriyor
// ve "zayıf" demek tek başına ne yapılacağını söylemiyor.
//
// ⚠ ANAHTARIN KENDİSİ ASLA döndürülmüyor, loglanmıyor, API'ye
// yazılmıyor. Zayıf bir anahtarı teşhis etmek onu yaymak için gerekçe
// değil; sebep dizgesi anahtarsız tam olarak anlaşılıyor.
func WeakSecretReason(secret string) string {
	s := strings.ToLower(strings.TrimSpace(secret))
	if s == "" {
		// Boş zaten NewService'in kendi dalı (ephemeral üretiyor); burada
		// "zayıf" demek o dalı ikinci kez raporlamak olurdu.
		return ""
	}
	for _, m := range weakSecretMarkers {
		if strings.Contains(s, m) {
			return "yer tutucu değerde (kurulum sırasında değiştirilmemiş)"
		}
	}
	if len(s) < minSecretLen {
		return "çok kısa (< 32 karakter)"
	}
	return ""
}

// WeakSecretReason (metot) — servisin KENDİ anahtarı hakkındaki teşhis.
//
// Anahtar Service'in özel alanı ve öyle kalmalı; dışarıya çıkarmak yerine
// servis kendisi hakkında rapor veriyor. Boş-anahtar dalında NewService
// rastgele bir anahtar üretmiş olur, dolayısıyla burası doğru biçimde ""
// döner — o durumun kendi logu var, iki kez raporlanmaz.
func (s *Service) WeakSecretReason() string {
	if s == nil {
		return ""
	}
	return WeakSecretReason(string(s.secret))
}
