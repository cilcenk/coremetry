package chstore

import (
	"context"
	"encoding/json"
	"math"
)

// AnomalySensitivityConfig — anomali DEDEKTÖRÜNÜN eşikleri
// (system_settings anahtarı "anomaly_sensitivity", v0.9.826).
//
// NEDEN VİDA: bu eşikler bugüne kadar altı kez kodda değişti (v0.9.48
// düz-MAD tabanı, v0.9.180 mutlak taban, v0.9.193 criticalZ 5→6 ve
// P1-only kapısı, v0.8.220 dwell…) ve her seferinde operatör bir çentik
// ötede aynı duvara çarptı. "Ne kadar sapma OLAY sayılır" filoya, servis
// hacmine ve gecikme profiline bağlı — kodda tahmin edilecek bir sayı
// değil. anomaly_tracked (v0.9.800) hangi metriğin ÖLÇÜLECEĞİNİ verdi;
// bu blob ölçülenin ne zaman OLAY sayılacağını veriyor.
//
// KABLO: anomaly_tracked emsalinin aynısı — system_settings blob'u +
// boot hidrasyonu + 30 sn yenileme + admin PUT + audit; derlenmiş set
// Store'da atomic pointer'da yayınlanır. Dedektör her tarama tikinde
// oradan okur, yani ayar tarayıcıya bir CH okuması EKLEMEDEN canlı kalır.
type AnomalySensitivityConfig struct {
	// Metrics — metrik başına eşikler. Kanonik olmayan anahtarlar okuma
	// yolunda DÜŞÜRÜLÜR: elle düzenlenmiş bir settings satırı dedektöre
	// politikası olmayan bir metrik adı sokamaz.
	Metrics map[string]AnomalyMetricSensitivity `json:"metrics"`
	// DwellBuckets — açılmak için ÜST ÜSTE ateşlemesi gereken 5-dk
	// bucket sayısı (anti-flap). Global: yön/birim taşımıyor.
	DwellBuckets int `json:"dwellBuckets"`
	// CriticalZ — bu |z|'nin üstü critical sayılır. v0.9.193'ten beri
	// dedektör YALNIZ critical verdict'te Problem açtığı için bu, fiilen
	// "açılma eşiği"dir; operatörün en çok çevireceği vida budur.
	CriticalZ float64 `json:"criticalZ"`
}

// AnomalyMetricSensitivity — tek bir metriğin eşikleri. Beşi de
// BİRLİKTE "bu sapma bir olay mı" sorusunu cevaplıyor ama farklı
// boşlukları kapatıyorlar; biri diğerinin yerine geçmez.
type AnomalyMetricSensitivity struct {
	// FloorPct — göreli değişim tabanı: |current-median|/|median|.
	// İstatistiksel olarak anlamlı ama KÜÇÜK hareketleri eler.
	FloorPct float64 `json:"floorPct"`
	// AbsFloor — mutlak DEĞER tabanı: current bunun altındaysa yükseliş
	// yönlü anomali açılmaz. "Sıfıra yakın tabandan minik sıçrama"
	// boşluğunu kapatır (error_rate=1 → milyonlarca istekte 1-2-3 hata
	// problem olmasın). Düşüşleri etkilemez.
	AbsFloor float64 `json:"absFloor"`
	// MinAbsDelta — mutlak FARK tabanı: |current-median| bunun
	// altındaysa açılmaz. FloorPct'in kör noktası: küçük bir medyanda
	// %10 göreli değişim mutlak olarak hiçbir şey ifade etmeyebilir
	// (1.9ms → 2.1ms yüzde olarak %10'dur ama kimse uyanmaz).
	MinAbsDelta float64 `json:"minAbsDelta"`
	// MinMAD — MAD'in ALT SINIRI, MAX olarak uygulanır:
	// mad = max(mad, minMAD). Sıkı (küçük MAD'li) bir baseline'da z
	// patlar; bu taban sigmanın anlamlı bir alt sınırı olmasını sağlar.
	//
	// flatMADFloor'un (v0.9.48) DÜZELTİLMİŞ hâli: o yalnız mad≈0 iken
	// devreye giriyordu, yani "tarihi hiç kıpırdamamış" seriyi koruyup
	// "neredeyse hiç kıpırdamamış" seriyi korumasız bırakıyordu — ve
	// gerçek false-pozitifler tam o ikinci kümede.
	MinMAD float64 `json:"minMAD"`
	// MinBaselineRate — hacim kapısı: son bucket'ın istek hızı (istek/sn)
	// bunun altındaysa anomali AÇILMAZ. Düşük hacimli bir serviste
	// yüzdeler ve kuyruk gecikmeleri gürültüdür — 20 isteğin 1'i hata
	// %5'tir ama olay değildir. Yalnız AÇILMAYA uygulanır; çözülme
	// etkilenmez (susan servisin problemi kapanmaya devam etmeli).
	MinBaselineRate float64 `json:"minBaselineRate"`
}

// AnomalySensitivityMetrics — kanonik metrik listesi VE gösterim sırası.
// AnomalyTrackedMetrics ile AYNI küme olmak zorunda (aynı bilinçli
// tekrar: chstore, anomaly'yi import EDEMEZ — ters yön).
var AnomalySensitivityMetrics = AnomalyTrackedMetrics

// Kelepçeler — elle düzenlenmiş bir settings satırı ya da hatalı bir PUT
// dedektörü işlevsiz bırakamasın. Üst sınırlar "bu vidayı sonuna kadar
// çevirdim, motor sustu" halini engelliyor; alt sınırlar negatif/anlamsız
// değerleri.
const (
	anomalyMinCriticalZ    = 1.0
	anomalyMaxCriticalZ    = 50.0
	anomalyMinDwellBuckets = 1
	anomalyMaxDwellBuckets = 24 // 2 saat; ötesi "hiç açma" demek
)

// DefaultAnomalySensitivity — v0.9.826'nın gemiye giren davranışı:
// BUGÜNKÜ davranış, ARTI iki cerrahi düzeltme (p99_ms).
//
// p99_ms MinMAD=1.0 ve AbsFloor=10 operatörün bildirdiği vakadan
// geliyor: medyan 1.90ms olan bir op 9.69ms'e çıkınca (mad 0.657)
// z=8.0σ üretiyor ve critical anomali açıyordu. 8 milisaniyelik bir
// sıçrama hiçbir kullanıcının fark etmediği bir şey; sorun sapmanın
// istatistiksel büyüklüğü değil, BİRİMİNİN önemsizliğiydi.
//   - MinMAD 1.0ms: sıkı baseline'da sigmayı gerçekçi bir alt sınıra
//     çeker; aynı vakada z 8.0 → 5.25, yani criticalZ 6.0 kapısının
//     altında kalır.
//   - AbsFloor 10ms: 10ms'nin altındaki bir p99 yükseliş yönünde hiç
//     olay sayılmaz — göreli oran ne olursa olsun.
//
// Diğer metrikler AYNEN bugünkü sabitler; yeni vidalar (MinAbsDelta,
// MinBaselineRate) 0 = kapalı, yani davranış değişmiyor.
func DefaultAnomalySensitivity() AnomalySensitivityConfig {
	return AnomalySensitivityConfig{
		Metrics: map[string]AnomalyMetricSensitivity{
			// v0.9.180: <%1 = birkaç-hata gürültüsü, açma.
			"error_rate": {FloorPct: 0.10, AbsFloor: 1.0},
			// v0.9.826: iki cerrahi düzeltme — gerekçe yukarıda.
			"p99_ms": {FloorPct: 0.10, AbsFloor: 10.0, MinMAD: 1.0},
			// Hacim sıçraması VE düşüşü; mutlak taban yok (düşüş yönü
			// zaten AbsFloor'dan etkilenmiyor, sıçrama için de anlamlı
			// bir evrensel taban yok).
			"request_rate": {FloorPct: 0.15},
		},
		DwellBuckets: 3,   // v0.8.220 — 3 × 5dk = 15 dk sürekli ateşleme
		CriticalZ:    6.0, // v0.9.193 — operatör: yalnız P1-sınıfı gelsin
	}
}

// NormalizeAnomalySensitivity kanonik anahtarları tamamlar, bilinmeyenleri
// düşürür ve her alanı kelepçeler.
//
// NEGATİF → VARSAYILAN (sıfırlamak DEĞİL): negatif bir taban anlamsız ve
// "operatör bu alanı boş bıraktı" ile "operatör bilerek 0 yazdı"yı
// ayırmak gerekiyor. 0 MEŞRU bir değer (vidayı kapat) ve öyle korunur;
// yalnız negatifler varsayılana döner.
func NormalizeAnomalySensitivity(c AnomalySensitivityConfig) AnomalySensitivityConfig {
	d := DefaultAnomalySensitivity()
	out := AnomalySensitivityConfig{
		Metrics:      make(map[string]AnomalyMetricSensitivity, len(AnomalySensitivityMetrics)),
		DwellBuckets: c.DwellBuckets,
		CriticalZ:    c.CriticalZ,
	}
	for _, m := range AnomalySensitivityMetrics {
		def := d.Metrics[m]
		got, ok := c.Metrics[m]
		if !ok {
			// Eksik anahtar varsayılanını alır: ileride dördüncü bir
			// metrik eklendiğinde eski satırlar onu varsayılanıyla
			// devralır.
			out.Metrics[m] = def
			continue
		}
		out.Metrics[m] = AnomalyMetricSensitivity{
			FloorPct:        clampFloor(got.FloorPct, def.FloorPct),
			AbsFloor:        clampFloor(got.AbsFloor, def.AbsFloor),
			MinAbsDelta:     clampFloor(got.MinAbsDelta, def.MinAbsDelta),
			MinMAD:          clampFloor(got.MinMAD, def.MinMAD),
			MinBaselineRate: clampFloor(got.MinBaselineRate, def.MinBaselineRate),
		}
	}
	if out.DwellBuckets < anomalyMinDwellBuckets || out.DwellBuckets > anomalyMaxDwellBuckets {
		out.DwellBuckets = d.DwellBuckets
	}
	if out.CriticalZ < anomalyMinCriticalZ || out.CriticalZ > anomalyMaxCriticalZ {
		out.CriticalZ = d.CriticalZ
	}
	return out
}

// clampFloor — negatif ya da sayı-olmayan (NaN/Inf, elle düzenlenmiş
// JSON'dan gelebilir) bir taban varsayılana döner. Sıfır MEŞRU: vidayı
// kapatmanın yolu.
func clampFloor(v, def float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return def
	}
	return v
}

// For — bir metriğin eşikleri; bilinmeyen metrik varsayılan politikayı
// alır (dedektörün policyFor'u ile aynı duruş: eksik ayar SUSTURMAZ).
func (c AnomalySensitivityConfig) For(metric string) AnomalyMetricSensitivity {
	if s, ok := c.Metrics[metric]; ok {
		return s
	}
	return AnomalyMetricSensitivity{FloorPct: 0.10}
}

const anomalySensitivityKey = "anomaly_sensitivity"

// GetAnomalySensitivity — KAYITLI blob (ya da varsayılanlar). CH
// hatasında varsayılana yumuşak düşer: geçici bir tökezleme dedektörün
// eşiklerini sessizce değiştiremez (GetAnomalyTracked ile aynı duruş).
func (s *Store) GetAnomalySensitivity(ctx context.Context) AnomalySensitivityConfig {
	raw, err := s.GetSetting(ctx, anomalySensitivityKey)
	if err != nil || len(raw) == 0 {
		return DefaultAnomalySensitivity()
	}
	var c AnomalySensitivityConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return DefaultAnomalySensitivity()
	}
	return NormalizeAnomalySensitivity(c)
}

// SaveAnomalySensitivity — system_settings'e yazar (yeni şema yok,
// invariant #6).
func (s *Store) SaveAnomalySensitivity(ctx context.Context, c AnomalySensitivityConfig) error {
	raw, err := json.Marshal(NormalizeAnomalySensitivity(c))
	if err != nil {
		return err
	}
	return s.PutSetting(ctx, anomalySensitivityKey, raw)
}

// SetAnomalySensitivity yayınlar (atomic) — anomaly_tracked kablosunun
// aynısı.
func (s *Store) SetAnomalySensitivity(c AnomalySensitivityConfig) {
	n := NormalizeAnomalySensitivity(c)
	s.anomalySensitivity.Store(&n)
}

// AnomalySensitivity — yayınlanmış ayar. Hiç hidrate edilmemişse
// (Store{} kuran testler, hidrasyondan önceki ilk tik) varsayılanlar.
func (s *Store) AnomalySensitivity() AnomalySensitivityConfig {
	if p := s.anomalySensitivity.Load(); p != nil {
		return *p
	}
	return DefaultAnomalySensitivity()
}

// LoadAnomalySensitivity = oku + yayınla. Boot hidrasyonunun tek gövdesi;
// hem API sunucusu hem de dedektör (kendi Start'ında, sunucudan önce
// koşabildiği için) bunu çağırır.
func (s *Store) LoadAnomalySensitivity(ctx context.Context) AnomalySensitivityConfig {
	c := s.GetAnomalySensitivity(ctx)
	s.SetAnomalySensitivity(c)
	return c
}
