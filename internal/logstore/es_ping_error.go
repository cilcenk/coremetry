// v0.9.630 — "ELASTICSEARCH UNREACHABLE AT BOOT" çoğu zaman YALAN'dı.
//
// Operatör-bildirimli: boot'ta bu satır çıkıyor, sonra ES kendiliğinden
// düzeliyor. Sebep ağ değil SIRALAMA:
//
//	main.go  buildLogStore(cfg, …)     ← YALNIZ env/YAML config
//	         → API key env'de YOK (Settings'te, system_settings'te)
//	         → ping auth=none ile gidiyor → ES 401 döndürüyor
//	         → "ELASTICSEARCH UNREACHABLE AT BOOT"
//	main.go  logsMgr.LoadPersisted(…)  ← API key ANCAK BURADA geliyor
//	         → ES gerçekten bağlanıyor
//
// Yani küme ayaktaydı, adresler doğruydu, kimlik bilgisi henüz
// yüklenmemişti. Mesaj operatörü ağa ve adreslere bakmaya yolluyordu —
// yanlış yere. Bu, kod bir şeyi bilmediği için değil, bildiğini
// SÖYLEMEDİĞİ için oluşan bir hata: NewES 401'i ayırt ediyor ama
// çağırana düz bir error olarak veriyor, o da hepsini "unreachable"
// diye raporluyor.
package logstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// HasPersistedESSettings — system_settings'te kimlik BİLGİSİ TAŞIYAN bir
// ES yapılandırması duruyor mu?
//
// Yalnız "blob var mı" yetmez: kaydedilmiş ama kimliksiz bir blob 401'i
// açıklamaz. Sorulan şey "birazdan yüklenecek olan şey bu 401'i
// çözecek mi".
//
// Hata hâlinde false: emin olamadığımızda YUMUŞATMA yapmıyoruz —
// operatörün gerçek bir yapılandırma hatasını "birazdan düzelir" diye
// okuması, gereksiz bir uyarı görmesinden kötü.
func HasPersistedESSettings(ctx context.Context, store ESSettingsStore) bool {
	if store == nil {
		return false
	}
	raw, err := store.GetLogstoreESSettingsRaw(ctx)
	if err != nil || len(raw) == 0 {
		return false
	}
	var cfg ESSettings
	if json.Unmarshal(raw, &cfg) != nil {
		return false
	}
	return cfg.APIKey != "" || cfg.Username != "" || cfg.Password != ""
}

// ESPingError — boot ping'inin NEDEN başarısız olduğunu taşıyan tipli
// hata. Çağıran (main.go) mesajı buna göre seçiyor.
//
// Status == 0 → hiç yanıt yok (dial/DNS/timeout) = GERÇEKTEN ulaşılamıyor.
// Status 401/403 → ulaşıldı, kimlik reddedildi.
// Diğer HTTP → ulaşıldı, başka bir yapılandırma sorunu.
type ESPingError struct {
	Status    int    // 0 = ağa hiç ulaşılamadı
	AuthMode  string // "api-key" | "basic" | "none"
	Addresses []string
	Body      string // ES'in yanıt gövdesi (varsa)
	Err       error  // taşınan ağ hatası (varsa)
}

func (e *ESPingError) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("ES ping (addresses=%v): %v", e.Addresses, e.Err)
	}
	return fmt.Sprintf("ES ping rejected (auth=%s addresses=%v): %s — "+
		"verify COREMETRY_ES_API_KEY (or username/password) + the addresses",
		e.AuthMode, e.Addresses, e.Body)
}

func (e *ESPingError) Unwrap() error { return e.Err }

// Unreachable — ağ katmanında hiç yanıt alınamadı.
func (e *ESPingError) Unreachable() bool { return e.Status == 0 }

// Unauthorized — küme CEVAP VERDİ ama kimliği reddetti.
func (e *ESPingError) Unauthorized() bool { return e.Status == 401 || e.Status == 403 }

// CredentialsAbsent — kimlik reddedildi VE hiç kimlik bilgisi
// yapılandırılmamıştı.
//
// Bu, boot sırasında BEKLENEN durum: api-key Settings'te (system_settings)
// duruyorsa env'de yoktur ve buildLogStore ondan önce koşar. Operatöre
// "yapılandırma hatası" diye bağırmak yanlış; doğru cümle "kaydedilmiş
// ayarlar birazdan yüklenecek".
func (e *ESPingError) CredentialsAbsent() bool {
	return e.Unauthorized() && e.AuthMode == "none"
}

// ESBootDiagnosis — main.go'nun boot'ta basacağı satır. SAF (tablo
// testli): sınıflandırma ağdan ve loglamadan ayrı.
//
// expectPersisted: kaydedilmiş (system_settings) bir ES yapılandırması
// yüklenmek üzere mi? Öyleyse kimliksiz bir 401 gürültü değil, sıradan
// bir boot adımı.
func ESBootDiagnosis(err error, expectPersisted bool) (headline, hint string) {
	var pe *ESPingError
	if !errors.As(err, &pe) {
		return "ELASTICSEARCH BAŞLATILAMADI",
			"ClickHouse logstore ile DEGRADED başlanıyor; 30sn'de bir yeniden denenecek"
	}
	switch {
	case pe.CredentialsAbsent() && expectPersisted:
		return "Elasticsearch kimlik bilgisi henüz yüklenmedi",
			"küme CEVAP VERDİ (401), env'de kimlik yok — kaydedilmiş ayarlar birazdan uygulanacak; o ana kadar ClickHouse logstore"
	case pe.Unauthorized():
		return "ELASTICSEARCH KİMLİK REDDETTİ",
			"küme ULAŞILABİLİR — sorun ağ değil kimlik: COREMETRY_ES_API_KEY (veya kullanıcı/parola) doğrulanmalı"
	case pe.Unreachable():
		return "ELASTICSEARCH ULAŞILAMIYOR",
			"ağ katmanında yanıt yok — adresler, DNS ve güvenlik duvarı kontrol edilmeli"
	default:
		return "ELASTICSEARCH YAPILANDIRMA HATASI",
			"küme ULAŞILABİLİR ama ping'i reddetti — yanıt gövdesi yukarıda"
	}
}
