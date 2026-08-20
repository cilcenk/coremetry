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

	// ── v0.9.1188 — PATLAMA KAPILARI (operatör-bildirimli, DÖRDÜNCÜ kez) ──
	//
	// Yukarıdaki v0.9.775 notu "sabitin DEĞERİ hiç doğru cevap değildi,
	// sabitin KENDİSİ yanlıştı" diyor ve PENCERELERİ ayarlanabilir yaptı.
	// Ama patlamanın TANIMI (hız + hacim eşiği) kodda gömülü kaldı ve
	// dördüncü vaka tam oradan geldi:
	//
	//	2.374 olay / 13dk 09sn → 180,5/dk   (gömülü kapı: 200/dk)
	//
	// Kapıyı %10 farkla kaçırdı, patlama sayılmadı, sonra 8 saatlik yaş
	// diğer bütün kapıları kapattı ve satır "steady" gerekçesiyle P3'e
	// düştü. Aynı ders bir katman aşağıda tekrarlanmış: pencereleri
	// ayarlanabilir yapıp eşiği gömülü bırakmak, duvarı kaldırmadı —
	// yerini değiştirdi.
	//
	// BurstMinRate — patlama sayılmak için dakikadaki en az olay.
	// Varsayılan v0.9.1188'de 200 → 100 İNDİ (aşağıdaki gerekçe).
	BurstMinRate float64 `json:"burstMinRate"`
	// BurstMinTotal — hız ne olursa olsun gereken taban hacim. Hız tek
	// başına yeterli değil: 5 saniyede 20 olay da 240/dk eder ama patlama
	// değildir. Varsayılan 1000 (değişmedi).
	BurstMinTotal int `json:"burstMinTotal"`

	// P1MinOccurrences — patlama SAYILMAYAN ama hacimli bir grubun P1
	// olması için gereken toplam olay (v0.9.1189, operatör-bildirimli).
	//
	// Bu kapı zaten vardı ama BEŞ DAKİKALIK bir uçurumun ardındaydı:
	// "son 5 dakikada görülmüş VE ≥500". v0.9.699 tam olarak bu uçurumu
	// yanlış ilan etmişti ("şiddet bir OLGU, tazelik ONA ERİŞİM
	// aciliyeti") ama düzeltmeyi yalnız PATLAMA yolunda yaptı; hacim yolu
	// 5 dakikada kaldı. Sonuç, bildirilen vaka: 888 olaylık bir grup
	// (mobil giriş) 1sa12dk önce durduğu için P1 olamadı, P2'de kaldı —
	// oysa aynı grup 5 dakika önce durmuş olsa P1'di.
	//
	// Artık kapı P1FreshHours penceresini kullanıyor, yani hacim ve
	// tazelik AYNI merdivende. Varsayılan 500 (eski gömülü değer).
	P1MinOccurrences int `json:"p1MinOccurrences"`

	// ── v0.9.1194 — FIRTINA (operatör-bildirimli + spec onayı) ──
	//
	// 2026-08-20 ekranı: 25 saniyede DOKUZ farklı servis yeni exception
	// grubu açtı, her biri 1-7 olaylık — hiçbiri tek başına hiçbir eşiği
	// geçmiyor ve eşik indirmekle de geçmez, çünkü sistemde "aynı anda
	// birden çok servis patladı" diye bir kavram yoktu. Fırtına dedektörü
	// (internal/anomaly/exception_storm.go) bu iki vidayı okur.
	//
	// StormWindowMinutes — pencere: bu kadar dakika içinde first_seen'i
	// olan YENİ gruplar sayılır (yalnız yeni — spec onayı; kronik gürültü
	// fırtına kanıtı değildir). Varsayılan 10.
	StormWindowMinutes int `json:"stormWindowMinutes"`
	// StormMinServices — eşik: pencerede yeni grup açan FARKLI servis
	// sayısı bunu bulunca tek bir P1 fırtına problemi açılır. Varsayılan 5.
	StormMinServices int `json:"stormMinServices"`
}

// DefaultExceptionTriage — v0.9.775'in gemiye giren davranışı.
func DefaultExceptionTriage() ExceptionTriageConfig {
	return ExceptionTriageConfig{
		P1FreshHours:      4,
		P2SameDayHours:    24,
		StaleResolveHours: int(DefaultExceptionStaleHorizon / time.Hour),
		// v0.9.1188 — 200'den 100'e İNDİ ve bu bilinçli bir davranış
		// değişikliği. Eski 200, TEK bir vakadan türetilmişti (v0.9.627'de
		// ölçülen olay ~938/dk) ve o notun kendi cümlesi zaten sınırı
		// söylüyordu: "gürültülü ama sağlıklı bir servisin ürettiği
		// tekrarlayan uyarı tipik olarak bu mertebenin altında kalıyor."
		// 100/dk = saniyede 1,7 olay; on üç dakika sürdürülen bu hız
		// sağlıklı bir servisin gürültüsü değil, kırılmış bir bağımlılıktır.
		// Küçük grupları BurstMinTotal (1000) zaten eliyor, yani indirme
		// "her şey patlama olur" riskini taşımıyor.
		BurstMinRate:  100,
		BurstMinTotal: 1000,
		// v0.9.1194 — fırtına varsayılanları: 10 dk / 5 servis. Bildirilen
		// olay 25 saniyede 9 servisti; 5, tek bir paylaşılan bağımlılığın
		// tipik patlama genişliğinin altında, iki bağımsız kazanın üstünde.
		StormWindowMinutes: 10,
		StormMinServices:   5,
		// v0.9.1189 — eski gömülü değerin aynısı; değişen sabitin DEĞERİ
		// değil, arkasındaki PENCERE (5dk → P1FreshHours).
		P1MinOccurrences: 500,
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
	// v0.9.1188 — patlama kapıları. Sıfır/negatif varsayılana düşer;
	// 0 "kapıyı kaldır" ANLAMINA GELMEZ, çünkü 0/dk her grubu patlama
	// yapar ve basamağın üst ucunu anlamsızlaştırırdı (BigBreachRatio'nun
	// ≥1.1 kelepçesiyle aynı gerekçe).
	if c.BurstMinRate <= 0 {
		c.BurstMinRate = d.BurstMinRate
	}
	if c.BurstMinTotal <= 0 {
		c.BurstMinTotal = d.BurstMinTotal
	}
	if c.P1MinOccurrences <= 0 {
		c.P1MinOccurrences = d.P1MinOccurrences
	}
	if c.StormWindowMinutes <= 0 {
		c.StormWindowMinutes = d.StormWindowMinutes
	}
	if c.StormMinServices <= 0 {
		c.StormMinServices = d.StormMinServices
	}
	return c
}

// StormWindow — dakika → süre, tek kaynak (birim-karışması sınıfına karşı).
func (c ExceptionTriageConfig) StormWindow() time.Duration {
	return time.Duration(NormalizeExceptionTriage(c).StormWindowMinutes) * time.Minute
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
