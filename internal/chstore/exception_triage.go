package chstore

import (
	"context"
	"encoding/json"
	"time"
)

// ExceptionTriageConfig — exception önceliği (P1/P2/P3) basamağının
// pencereleri, artık operatörün elinde.
//
// v0.9.775 — operatör-bildirimli, BU SINIFTA ÜÇÜNCÜ kez (v0.9.627,
// v0.9.699, 2026-08-08 ekranı). Her seferinde aynı düzeltmeyi yaptım:
// sabiti bir çentik ötelemek. Her seferinde operatör bir çentik ötede
// aynı duvara çarptı. Sabitin DEĞERİ hiç doğru cevap değildi — sabitin
// KENDİSİ yanlıştı, çünkü "ne kadar taze hâlâ acildir" sorusunun cevabı
// filoya, nöbet devrine ve operatörün ekrana ne sıklıkla baktığına
// bağlı. Bunu kodda tahmin etmek yerine burada ayarlanabilir yapıyoruz.
//
// Zero-value config = varsayılanlar (Normalize aşağıda), yani ayar
// sayfasına hiç uğramamış bir kurulum v0.9.775 davranışını alır.
type ExceptionTriageConfig struct {
	// P1FreshHours — bir PATLAMANIN P1 kalmayı sürdürdüğü tazelik
	// penceresi (son görülme ≤ bu kadar saat). Aynı pencere,
	// patlama olmayan ama hacimli (≥100) grupların P2 kapısı için de
	// kullanılır — v0.9.699'un dersi: iki kapı ayrı sabitlere
	// bağlıysa biri kapanırken satır P2'yi ATLAYIP P3'e düşüyor.
	//
	// Varsayılan 4: problem tarafındaki "critical open ≥4h → P1"
	// felsefesiyle simetrik (CLAUDE.md triyaj kuralı) ve gece
	// nöbetinde 22:37'de biten bir patlamayı 00:22'de hâlâ P1
	// gösterecek kadar geniş.
	P1FreshHours int `json:"p1FreshHours"`
	// P2SameDayHours — patlamanın P2 ("bugün") kaldığı pencere.
	// Sonrası P3 ("sırası gelince"). Varsayılan 24.
	//
	// P1FreshHours'tan küçük olamaz; olursa basamak ters dönerdi
	// (taze bir patlama P1'i geçip doğrudan P3'e düşerdi). API
	// sınırında reddedilir, okuma yolunda kelepçelenir.
	P2SameDayHours int `json:"p2SameDayHours"`
	// StaleResolveHours — AutoResolveStaleExceptionGroups'un vidası:
	// hiç yeni olay görmeyen açık/ack'li bir grup bu kadar saat sonra
	// kendiliğinden resolved'a geçer. Varsayılan 24
	// (DefaultExceptionStaleHorizon).
	//
	// Triyaj penceresiyle aynı blob'da çünkü operatör ikisini aynı
	// cümleyle düşünüyor: "bir exception ne kadar süre benim
	// gündemimde kalsın?"
	StaleResolveHours int `json:"staleResolveHours"`
}

// DefaultExceptionTriage — v0.9.775'in gemiye giren davranışı.
func DefaultExceptionTriage() ExceptionTriageConfig {
	return ExceptionTriageConfig{
		P1FreshHours:      4,
		P2SameDayHours:    24,
		StaleResolveHours: int(DefaultExceptionStaleHorizon / time.Hour),
	}
}

const exceptionTriageKey = "exception_triage"

// GetExceptionTriage returns the persisted config, or the defaults when
// nothing is saved. Soft-fails to defaults on CH error — aynı duruş
// GetProblemEscalation'da: geçici bir CH tökezlemesi triyaj basamağını
// sessizce değiştiremez.
func (s *Store) GetExceptionTriage(ctx context.Context) ExceptionTriageConfig {
	raw, err := s.GetSetting(ctx, exceptionTriageKey)
	if err != nil || len(raw) == 0 {
		return DefaultExceptionTriage()
	}
	var c ExceptionTriageConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return DefaultExceptionTriage()
	}
	return NormalizeExceptionTriage(c)
}

// NormalizeExceptionTriage patches absent / nonsensical values back into
// a usable ladder. Saf + tablo-testli; API doğrulaması ve okuma yolu
// AYNI kuralları kullanır ki elle düzenlenmiş bir settings satırı da
// güvenli bir şekle düşsün.
//
// `int` "yok" ile "açıkça 0"ı ayırt edemez ve 0 burada "hiçbir patlama
// asla P1 olmasın" demek olurdu — güvenli tarafa düşmenin TERSİ. Bu
// yüzden 0 ve negatif varsayılana döner.
func NormalizeExceptionTriage(c ExceptionTriageConfig) ExceptionTriageConfig {
	d := DefaultExceptionTriage()
	if c.P1FreshHours <= 0 {
		c.P1FreshHours = d.P1FreshHours
	}
	if c.P2SameDayHours <= 0 {
		c.P2SameDayHours = d.P2SameDayHours
	}
	if c.StaleResolveHours <= 0 {
		c.StaleResolveHours = d.StaleResolveHours
	}
	// Basamak ters dönemez: P2 penceresi P1'den dar olursa taze bir
	// patlama P1'i geçip doğrudan P3'e düşerdi — v0.9.699'un tam
	// olarak düzelttiği kusur.
	if c.P2SameDayHours < c.P1FreshHours {
		c.P2SameDayHours = c.P1FreshHours
	}
	return c
}

// P1Window / P2Window / StaleHorizon — saatleri süreye çeviren tek
// kaynak. Çağıranlar `time.Duration(c.X) * time.Hour` yazmasın diye
// var: birim karışması bu depoda tekrarlayan bir hata sınıfı
// (retention_test.go dersi).
func (c ExceptionTriageConfig) P1Window() time.Duration {
	return time.Duration(NormalizeExceptionTriage(c).P1FreshHours) * time.Hour
}

func (c ExceptionTriageConfig) P2Window() time.Duration {
	return time.Duration(NormalizeExceptionTriage(c).P2SameDayHours) * time.Hour
}

func (c ExceptionTriageConfig) StaleHorizon() time.Duration {
	return time.Duration(NormalizeExceptionTriage(c).StaleResolveHours) * time.Hour
}

// SaveExceptionTriage persists the config under system_settings — new
// schema yok, her operatör ayarıyla aynı anahtar/değer tablosu.
func (s *Store) SaveExceptionTriage(ctx context.Context, c ExceptionTriageConfig) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.PutSetting(ctx, exceptionTriageKey, raw)
}
