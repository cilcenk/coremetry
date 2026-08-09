package chstore

import (
	"context"
	"encoding/json"
	"sync/atomic"
)

// ProblemPriorityConfig — alert-problem öncelik merdiveninin (P1/P2/P3)
// iki gömülü sabiti, artık operatörün elinde.
//
// v0.9.838 — operatör-bildirimli: "hâlâ çok fazla alert rule'dan P1
// geliyor". Prod'da 29 critical'in 22'si P1'di; örnek bir error_rate
// problemi 3.93/1.31 = 3.0× oranla eşiği aşıyor ve 2× kapısından
// otomatik P1 çıkıyordu.
//
// Kök neden, exception tarafındaki v0.9.775 vakasının aynısı: değer
// yanlış değil, DEĞERİN KODDA GÖMÜLÜ OLMASI yanlış. "Eşiğin kaç katı
// ihlal artık gece kaldırır" sorusunun cevabı filoya, kural setine ve
// nöbet devrine bağlı. HİÇBİR ayar blobu bu hatta dokunmuyordu:
// exception_triage yalnız exception gruplarına, anomaly_* yalnız
// anomali dedektörlerine iniyor.
//
// Bu sürüm DAVRANIŞ DEĞİŞTİRMİYOR — varsayılanlar bugünkü sabitlerin
// birebir aynısı (2.0× ve 4 saat). Yalnız vidayı takıyor; katı sıkmak
// operatörün kararı.
type ProblemPriorityConfig struct {
	// BigBreachRatio — "büyük ihlal" kapısı: değer eşiğin bu kadar katına
	// çıktığında (">" kuralları) ya da eşiğin bu kadar katı altına
	// düştüğünde ("<" kuralları, oran ters çevrilir) ihlal büyük sayılır.
	// critical + büyük ihlal = P1; warning + büyük ihlal = P2.
	//
	// Varsayılan 2.0. Kelepçe ≥1.1: 1.0 "eşiği aşan HER ŞEY büyük ihlal"
	// demek olurdu ve merdivenin üst basamağını anlamsızlaştırırdı.
	BigBreachRatio float64 `json:"bigBreachRatio"`
	// StaleCriticalHours — bir critical problem bu kadar saattir AÇIKSA
	// (çözülmemiş) tek başına P1'e terfi eder. Varsayılan 4.
	//
	// 0 = bu terfi TAMAMEN KAPALI. Operatörün P1 selini kesmek için
	// isteyebileceği ilk vida bu olabilir: uzun süre açık kalan bir
	// critical "hâlâ ilgilenilmedi" demek, "şu an daha acil" demek değil.
	// Negatif değer kelepçeyle 0'a düşer.
	StaleCriticalHours float64 `json:"staleCriticalHours"`
}

// DefaultProblemPriority — v0.9.838 ÖNCESİNİN gömülü sabitleri, birebir.
// Bu değerler değişirse davranış değişir; sürüm notunda söylenmeli.
func DefaultProblemPriority() ProblemPriorityConfig {
	return ProblemPriorityConfig{
		BigBreachRatio:     2.0,
		StaleCriticalHours: 4,
	}
}

// MinBigBreachRatio — 1.0 ve altı, "eşiği aşan her şey büyük ihlal"
// anlamına gelirdi (oran zaten ≥1 olduğunda ihlal var). Kapının bir
// ANLAMI kalması için alt sınır.
const MinBigBreachRatio = 1.1

const problemPriorityKey = "problem_priority"

// NormalizeProblemPriority saçma / eksik değerleri kullanılabilir bir
// merdivene çeker. Saf + tablo-testli; API doğrulaması ve okuma yolu
// AYNI kuralları kullanır ki elle düzenlenmiş bir system_settings satırı
// da güvenli bir şekle düşsün.
//
// TAMAMEN SIFIR struct = "hiç doldurulmamış" → varsayılanlar. Bu dal
// bilinçli: StaleCriticalHours'ta 0 ANLAMLI bir değer ("terfi kapalı"),
// yani alan-alan "0 ise varsayılan" diyemiyoruz. Zero-value'yu bütün
// olarak yakalamak, `ProblemPriorityConfig{}` yazan bir çağıranın
// sessizce bayat-critical terfisini kapatmasını engelliyor — ve
// BigBreachRatio 0'da `ratio >= 0` HER problemi büyük ihlal yapardı.
func NormalizeProblemPriority(c ProblemPriorityConfig) ProblemPriorityConfig {
	d := DefaultProblemPriority()
	if c == (ProblemPriorityConfig{}) {
		return d
	}
	if c.BigBreachRatio <= 0 {
		c.BigBreachRatio = d.BigBreachRatio
	}
	if c.BigBreachRatio < MinBigBreachRatio {
		c.BigBreachRatio = MinBigBreachRatio
	}
	if c.StaleCriticalHours < 0 {
		c.StaleCriticalHours = 0
	}
	return c
}

// problemPriorityCfg — süreç-genelinde tek kopya, atomik.
//
// EnrichProblemsWithPriority satır BAŞINA computePriority çağırıyor ve
// bildirim hattından da (notify/alert_title.go) geçiyor; oradan bir CH
// okuması yapmak ayarı hot-path'e sokardı. Blob boot'ta hidrate edilir
// (api.LoadProblemPriority), PUT'ta güncellenir ve çok-pod kurulumlarda
// 30 sn'lik yenileme döngüsüyle yakınsar — exception_triage kablosunun
// aynısı, yalnız global chstore'da çünkü EnrichProblemsWithPriority
// paket-düzeyi ve ctx almıyor.
var problemPriorityCfg atomic.Pointer[ProblemPriorityConfig]

// CurrentProblemPriority — hiç hidrate edilmemişse varsayılanlar. Test
// ikilisi hiçbir zaman Store'a bağlanmaz, yani saf öncelik testleri bu
// daldan geçer ve v0.9.838 öncesi davranışı görür.
func CurrentProblemPriority() ProblemPriorityConfig {
	if c := problemPriorityCfg.Load(); c != nil {
		return *c
	}
	return DefaultProblemPriority()
}

// SetProblemPriority — normalize EDİLMİŞ hâlini yayınlar, böylece
// okuyucular hiçbir zaman anlamsız bir kapı görmez.
func SetProblemPriority(c ProblemPriorityConfig) {
	n := NormalizeProblemPriority(c)
	problemPriorityCfg.Store(&n)
}

// GetProblemPriority returns the persisted config, or the defaults when
// nothing is saved. Soft-fails to defaults on CH error — GetExceptionTriage
// ile aynı duruş: geçici bir CH tökezlemesi öncelik merdivenini sessizce
// değiştiremez.
//
// Unmarshal ÖNCEDEN DOLDURULMUŞ bir struct'a yapılıyor: JSON'da olmayan
// bir alan varsayılanında kalır. Yoksa `{"bigBreachRatio":3}` gibi elle
// yazılmış bir blob, staleCriticalHours'ı sessizce 0'a (terfi kapalı)
// çekerdi — operatörün yazmadığı bir karar.
func (s *Store) GetProblemPriority(ctx context.Context) ProblemPriorityConfig {
	raw, err := s.GetSetting(ctx, problemPriorityKey)
	if err != nil || len(raw) == 0 {
		return DefaultProblemPriority()
	}
	c := DefaultProblemPriority()
	if err := json.Unmarshal(raw, &c); err != nil {
		return DefaultProblemPriority()
	}
	return NormalizeProblemPriority(c)
}

// SaveProblemPriority persists the config under system_settings — yeni
// şema yok, her operatör ayarıyla aynı anahtar/değer tablosu.
func (s *Store) SaveProblemPriority(ctx context.Context, c ProblemPriorityConfig) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.PutSetting(ctx, problemPriorityKey, raw)
}
