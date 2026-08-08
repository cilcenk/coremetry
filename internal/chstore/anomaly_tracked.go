package chstore

import (
	"context"
	"encoding/json"
)

// AnomalyTrackedConfig — anomali DEDEKTÖRÜNÜN ölçtüğü metrik seti
// (system_settings anahtarı "anomaly_tracked").
//
// v0.9.800, operatör kararı (2026-08-09): request_rate anomalileri
// false-pozitif üretiyor. Trafik hacmi bu filoda kampanya / batch /
// nöbet devri gibi tamamen normal nedenlerle sıçrıyor ve düşüyor;
// "istek hızı beklenenden farklı" cümlesi operatör için bir OLAY
// değil, error_rate ve p99_ms ise gerçek olay sinyalleri.
//
// Metriği koddan SÖKMEK yerine vida: politika filoya bağlı, kodda
// tahmin edilecek bir şey değil — anomaly_promotion (v0.5.70) ve
// exception_triage (v0.9.775) ile aynı gerekçe. Geri dönüş tek tık.
//
// Yan kazanç: kapalı bir metrik için tarayıcı HİÇ sorgu açmaz —
// tik başına 2 toplu MV okuması (consecutive + seasonal) düşer.
//
// Şekil bilinçli olarak düz bir map: blob'un kendisi
// {"error_rate":true,"p99_ms":true,"request_rate":false}. Kanonik
// olmayan anahtarlar okuma yolunda DÜŞÜRÜLÜR (FillAnomalyTracked) —
// elle düzenlenmiş bir settings satırı dedektöre politikası/SQL
// ifadesi olmayan bir metrik adı sokamaz.
type AnomalyTrackedConfig map[string]bool

// AnomalyTrackedMetrics — kanonik metrik listesi VE gösterim/tarama
// sırası. internal/anomaly'deki metricPolicies + metricValueExpr
// kümesinin aynısı; chstore anomaly'yi import EDEMEZ (ters yön), o
// yüzden liste burada tekrarlanıyor — Seasonal* alanlarındaki
// (v0.8.250) aynı bilinçli tekrar.
var AnomalyTrackedMetrics = []string{"error_rate", "p99_ms", "request_rate"}

// DefaultAnomalyTracked — v0.9.800'ün gemiye giren davranışı:
// error_rate + p99_ms açık, request_rate KAPALI. Ayar sayfasına hiç
// uğramamış bir kurulum bunu alır (davranış değişikliği: v0.9.799'a
// kadar üçü de ölçülüyordu).
func DefaultAnomalyTracked() AnomalyTrackedConfig {
	return AnomalyTrackedConfig{
		"error_rate":   true,
		"p99_ms":       true,
		"request_rate": false,
	}
}

// FillAnomalyTracked kanonik anahtarları tamamlar ve bilinmeyenleri
// düşürür — KELEPÇESİZ hâli. Eksik anahtar varsayılanını alır: ileride
// dördüncü bir metrik eklendiğinde eski satırlar onu varsayılanıyla
// devralır (bool "yok" ile "açıkça false"ı ayırt edemez; burada "yok"
// = "bu sürümden eski satır" demek).
//
// API sınırı bunu kullanır ("hepsi kapalı" isteğini GÖRMESİ gerekir);
// okuma yolu NormalizeAnomalyTracked'i kullanır (aşağıda, kelepçeli).
func FillAnomalyTracked(c AnomalyTrackedConfig) AnomalyTrackedConfig {
	d := DefaultAnomalyTracked()
	out := make(AnomalyTrackedConfig, len(AnomalyTrackedMetrics))
	for _, m := range AnomalyTrackedMetrics {
		v, ok := c[m]
		if !ok {
			v = d[m]
		}
		out[m] = v
	}
	return out
}

// NormalizeAnomalyTracked = FillAnomalyTracked + "hepsi kapalı"
// kelepçesi. Tümü kapalı bir set, anomali motorunu tümden kapatmanın
// YOLU DEĞİL (o Background ayarıdır); elle düzenlenmiş böyle bir satır
// dedektörü sessizce sıfır metrikle çalıştırırdı — sessiz kapanma bu
// depoda tekrarlayan hata sınıfı. Okuma yolunda varsayılana döner, API
// sınırında 400 ile reddedilir.
func NormalizeAnomalyTracked(c AnomalyTrackedConfig) AnomalyTrackedConfig {
	f := FillAnomalyTracked(c)
	if !f.Any() {
		return DefaultAnomalyTracked()
	}
	return f
}

// Any — en az bir kanonik metrik açık mı?
func (c AnomalyTrackedConfig) Any() bool {
	for _, m := range AnomalyTrackedMetrics {
		if c[m] {
			return true
		}
	}
	return false
}

// Enabled — açık metrikler, kanonik sırada. Tarayıcının tik başına
// dolaştığı liste budur; kapalı metrik buradan hiç çıkmadığı için
// onun için ne MV okuması açılır ne de checkOne çalışır.
func (c AnomalyTrackedConfig) Enabled() []string {
	n := NormalizeAnomalyTracked(c)
	out := make([]string, 0, len(AnomalyTrackedMetrics))
	for _, m := range AnomalyTrackedMetrics {
		if n[m] {
			out = append(out, m)
		}
	}
	return out
}

const anomalyTrackedKey = "anomaly_tracked"

// GetAnomalyTracked — KAYITLI blob (ya da varsayılanlar). CH hatasında
// varsayılana yumuşak düşer: geçici bir tökezleme izlenen metrik
// setini sessizce değiştiremez (GetAnomalyPromotion ile aynı duruş).
func (s *Store) GetAnomalyTracked(ctx context.Context) AnomalyTrackedConfig {
	raw, err := s.GetSetting(ctx, anomalyTrackedKey)
	if err != nil || len(raw) == 0 {
		return DefaultAnomalyTracked()
	}
	var c AnomalyTrackedConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return DefaultAnomalyTracked()
	}
	return NormalizeAnomalyTracked(c)
}

// SaveAnomalyTracked — system_settings'e yazar (yeni şema yok,
// invariant #6).
func (s *Store) SaveAnomalyTracked(ctx context.Context, c AnomalyTrackedConfig) error {
	raw, err := json.Marshal(NormalizeAnomalyTracked(c))
	if err != nil {
		return err
	}
	return s.PutSetting(ctx, anomalyTrackedKey, raw)
}

// SetAnomalyTracked yayınlar (atomic). metric_exclusions (v0.9.797)
// kablosunun aynısı: blob boot'ta hidrate edilir, admin PUT'unda
// anında takas edilir, çok-pod kurulumlarda 30 sn'lik yenilemeyle
// yakınsar. Tarayıcı her tikte buradan okur — CH okuması değil,
// atomic load.
func (s *Store) SetAnomalyTracked(c AnomalyTrackedConfig) {
	n := NormalizeAnomalyTracked(c)
	s.anomalyTracked.Store(&n)
}

// AnomalyTracked — yayınlanmış set. Hiç hidrate edilmemişse (Store{}
// kuran testler, hidrasyondan önceki ilk tik) varsayılanlar.
func (s *Store) AnomalyTracked() AnomalyTrackedConfig {
	if p := s.anomalyTracked.Load(); p != nil {
		return *p
	}
	return DefaultAnomalyTracked()
}

// LoadAnomalyTracked = oku + yayınla. Boot hidrasyonunun tek gövdesi;
// hem API sunucusu hem de dedektör (kendi Start'ında, sunucudan önce
// koşabildiği için) bunu çağırır.
func (s *Store) LoadAnomalyTracked(ctx context.Context) AnomalyTrackedConfig {
	c := s.GetAnomalyTracked(ctx)
	s.SetAnomalyTracked(c)
	return c
}
