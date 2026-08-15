// v0.9.1036 — failure-rate (%) SLO eşiği: latency SLO'sunun eksik ikizi.
//
// Bugüne dek bir servisin hata-oranı grafiğinde yatay bir eşik çizgisi
// görebilmenin TEK yolu o servis için elle bir *availability* SLO'su
// açmaktı (ServiceCharts: sliType=="availability" → hata bütçesi %'si).
// Latency tarafında da durum aynı (sliType=="latency" → thresholdMs),
// ama latency'de operatörün kafasında zaten bir sayı var; hata oranında
// "kaçtan sonra kötü" sorusunun filo-geneli bir cevabı var ve o cevap
// hiçbir yerde YAZILI DEĞİLDİ.
//
// Bu blob o cevabı yazıya döküyor: filo varsayılanı (%1) + servis başına
// override. PARALEL BİR ŞEMA DEĞİL — /api/slos tablosu olduğu gibi
// duruyor ve öncelik sırasında ÜSTTE: bir servis için gerçek bir
// availability SLO'su varsa çizgi ondan gelir, bu blob hiç konuşmaz
// (bkz. frontend/src/lib/failureSlo.ts, tek çözümleme noktası).
//
// Neden system_settings: CLAUDE.md invariant 6 — operatör ayarı = anahtar
// başına JSON blob, yüzey başına yeni şema YOK. Şablon problem_priority.
package chstore

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
)

// FailureSLOConfig — hata-oranı eşiğinin iki basamağı.
type FailureSLOConfig struct {
	// DefaultPct — filo geneli varsayılan, YÜZDE (1 = %1). Override'ı
	// olmayan her servis bunu görür.
	//
	// 0 GEÇERLİ ve anlamlıdır: "varsayılan çizgi çizme". Negatif değil.
	DefaultPct float64 `json:"defaultPct"`
	// Overrides — servis adı → yüzde. Varsayılanı ezen tek şey (ve onu
	// da gerçek bir availability SLO'su ezer).
	Overrides map[string]float64 `json:"overrides,omitempty"`
}

// DefaultFailureSLO — %1. Uydurulmuş bir sayı değil: Coremetry'nin kendi
// problem eşiği ailesinde de hata oranı bu mertebede konuşuluyor ve
// çizgi bir ALARM değil bir OKUMA YARDIMI — grafikte "buranın üstü kötü"
// diyen bir referans. Operatör sıkmak/gevşetmek isterse vida burada.
func DefaultFailureSLO() FailureSLOConfig {
	return FailureSLOConfig{DefaultPct: 1}
}

// MaxFailureSLOPct — yüzde tanım gereği 100'ü aşamaz.
const MaxFailureSLOPct = 100

// MaxFailureSLOOverrides — override haritasının üst sınırı.
//
// Blob HER servis sayfasında tarayıcıya iniyor (tek fetch, uzun
// staleTime). 1000s-servis kısıtında sınırsız bir harita sessizce
// yüzlerce KB'lık bir ayar cevabına dönüşebilir. 500 girdi ≈ birkaç on
// KB: elle politika yazan bir operatör için fazlasıyla geniş, kaza ile
// yazılmış bir döngü için dar.
const MaxFailureSLOOverrides = 500

const failureSLOKey = "failure_slo"

// NormalizeFailureSLO saçma/eksik değerleri kullanılabilir bir şekle
// çeker. Saf + tablo-testli; API doğrulaması ve OKUMA yolu AYNI kuralları
// kullanır ki elle düzenlenmiş bir system_settings satırı da güvenli bir
// şekle düşsün.
//
// TAMAMEN SIFIR struct = "hiç doldurulmamış" → varsayılanlar. Bu dal
// bilinçli, problem_priority'nin birebir dersi: DefaultPct'te 0 ANLAMLI
// bir değer ("çizgi yok"), yani alan-alan "0 ise varsayılan" diyemiyoruz.
// Bütün olarak yakalamak, `FailureSLOConfig{}` yazan bir çağıranın
// sessizce filo-geneli çizgiyi kapatmasını engelliyor.
func NormalizeFailureSLO(c FailureSLOConfig) FailureSLOConfig {
	if c.DefaultPct == 0 && len(c.Overrides) == 0 {
		return DefaultFailureSLO()
	}
	c.DefaultPct = clampFailurePct(c.DefaultPct)
	if len(c.Overrides) == 0 {
		c.Overrides = nil
		return c
	}
	out := make(map[string]float64, len(c.Overrides))
	for svc, pct := range c.Overrides {
		svc = strings.TrimSpace(svc)
		if svc == "" {
			// Adsız override hiçbir servise denk gelmez; sessizce
			// taşımak "kaydettim ama çalışmıyor" demektir.
			continue
		}
		out[svc] = clampFailurePct(pct)
	}
	if len(out) == 0 {
		out = nil
	}
	c.Overrides = out
	return c
}

// clampFailurePct — [0, 100]. Negatif bir yüzde çizgiyi ekseninin
// altına atardı; 100'ün üstü de görünmez olurdu.
func clampFailurePct(p float64) float64 {
	if p < 0 {
		return 0
	}
	if p > MaxFailureSLOPct {
		return MaxFailureSLOPct
	}
	return p
}

// FailureSLOOverrideServices — override'lı servis adları, SIRALI.
// Audit satırı ve log için: harita iterasyonu Go'da rastgeledir ve
// rastgele sıralı bir audit detayı iki özdeş kaydı farklı gösterir.
func FailureSLOOverrideServices(c FailureSLOConfig) []string {
	if len(c.Overrides) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.Overrides))
	for svc := range c.Overrides {
		out = append(out, svc)
	}
	sort.Strings(out)
	return out
}

// GetFailureSLO returns the persisted config, or the defaults when
// nothing is saved. Soft-fails to defaults on CH error — GetProblemPriority
// ile aynı duruş: geçici bir CH tökezlemesi bir grafik çizgisini sessizce
// yok edemez.
//
// Unmarshal ÖNCEDEN DOLDURULMUŞ bir struct'a yapılıyor: JSON'da olmayan
// bir alan varsayılanında kalır.
func (s *Store) GetFailureSLO(ctx context.Context) FailureSLOConfig {
	raw, err := s.GetSetting(ctx, failureSLOKey)
	if err != nil || len(raw) == 0 {
		return DefaultFailureSLO()
	}
	c := DefaultFailureSLO()
	if err := json.Unmarshal(raw, &c); err != nil {
		return DefaultFailureSLO()
	}
	return NormalizeFailureSLO(c)
}

// SaveFailureSLO persists the config under system_settings — yeni şema
// yok, her operatör ayarıyla aynı anahtar/değer tablosu.
func (s *Store) SaveFailureSLO(ctx context.Context, c FailureSLOConfig) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.PutSetting(ctx, failureSLOKey, raw)
}
