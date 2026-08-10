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
	// Behavior — DAVRANIŞ MOTORU AŞAMA 1 (v0.9.935). Aynı blob'un
	// altında, çünkü operatör buraya tek bir soruyla geliyor: "ne zaman
	// olay sayılsın?". Üstteki alanlar ANİ sapmayı (5-dk pencere, 24s
	// geçmiş) ayarlıyor; bu bölüm KALICI davranış değişimini (haftanın
	// saati baseline'ı, 28 gün) ayarlıyor.
	//
	// AYRI VİDA GEREKTİ çünkü iki soru farklı: "şu an sıçradı mı" ile
	// "bu servis artık başka türlü davranıyor mu" aynı eşikle
	// cevaplanmaz. Bir rejim kayması 1.5× ile gerçektir ve 6σ'ya hiç
	// ulaşmayabilir; bir sıçrama 6σ ile gerçektir ve 30 dakika sürmez.
	Behavior AnomalyBehaviorConfig `json:"behavior"`
	// AttachToIncident — dedektörün açtığı metrik problemi otomatik
	// olarak aktif incident'a bağlansın mı? (v0.9.827)
	//
	// NEDEN GEREKTİ: dedektör hattının HİÇBİR kapısı yoktu. Settings'teki
	// "Promote strong anomalies" vidaları (15× / 1800s / 1000) BAŞKA bir
	// hattı yönetiyor — evaluator.promoteStrongAnomalies, recorder'ın
	// AnomalyEvent'lerinden (log/trace desen anomalileri) besleniyor.
	// Metrik dedektörü kendi problemini KOŞULSUZ açıp koşulsuz incident'a
	// bağlıyordu; o kutucuk KAPALIYKEN bile. Operatör "anomali terfisini
	// kapattım" sanıp incident akışının devam ettiğini görüyordu.
	//
	// *bool ÇÜNKÜ VARSAYILAN TRUE: düz bool'da JSON'ın sıfır değeri false
	// ve "alan yok" ile "operatör kapattı" ayırt edilemezdi — bu sürümden
	// ESKİ her settings satırı sessizce davranış değiştirirdi. nil =
	// "yazılmamış" = bugünkü davranış (bağla).
	AttachToIncident *bool `json:"attachToIncident,omitempty"`
}

// AttachesToIncident — nil-güvenli okuma. Yazılmamış = BAĞLA (bugünkü
// davranış); bir ayarın yokluğu asla davranış değiştirmemeli.
func (c AnomalySensitivityConfig) AttachesToIncident() bool {
	return c.AttachToIncident == nil || *c.AttachToIncident
}

// AnomalyBehaviorConfig — davranış motoru AŞAMA 1'in vidaları
// (v0.9.935). Motorun kendisi internal/anomaly/behavior.go'da.
//
// NE YAPAR: her servisin RED metriklerini (hata oranı, P99, istek hızı)
// HAFTANIN SAATİ kovasına göre (168 kova, UTC) son 28 günün MV
// bucket'larından öğrenilen medyan+MAD baseline'ına karşı puanlar. İki
// sinyal üretir:
//
//	mevsimsel sapma : kendi kovasının baseline'ından robust-z ile ayrılma
//	                  (DwellSeasonal ardışık dilim boyunca, aynı yönde)
//	rejim kayması   : baseline'a karşı KALICI oransal kayma
//	                  (DwellRegime dilim; deploy ilişkisi varsa iliştirilir)
//
// İkisi de anomaly_events'e kind="behavior_change" olarak düşer — yeni
// tablo, yeni lane, yeni bildirim hunisi YOK. Mevcut terfi kapısı
// (anomaly_promotion) hangisinin Problem olacağına karar verir.
//
// NEDEN VİDA: eşikler filoya bağlı. 1.5× kalıcı kayma bir ödeme
// servisinde olaydır, bir batch işçisinde günlük rutindir.
type AnomalyBehaviorConfig struct {
	// Enabled — motor koşsun mu? *bool ÇÜNKÜ VARSAYILAN TRUE: düz
	// bool'da JSON'ın sıfır değeri false olurdu ve bu sürümden ESKİ
	// her settings satırı motoru sessizce kapalı gösterirdi
	// (AttachToIncident ile aynı gerekçe). nil = "yazılmamış" = açık.
	Enabled *bool `json:"enabled,omitempty"`
	// SeasonalZ — mevsimsel sapmanın açılma eşiği (robust z, σ).
	// Ani-sapma dedektörünün CriticalZ'sinden AYRI ve daha düşük
	// olması normaldir: burada baseline haftanın aynı saatinden
	// geliyor, yani gürültünün mevsimsel bileşeni zaten çıkarılmış.
	SeasonalZ float64 `json:"seasonalZ"`
	// RegimeRatio — rejim kaymasının açılma oranı (× baseline
	// medyanı). Düşüş tarafında karşılığı 1/RegimeRatio'dur.
	RegimeRatio float64 `json:"regimeRatio"`
	// DwellSeasonal / DwellRegime — kaç ardışık 5-dk dilimin AYNI
	// yönde ateşlemesi gerektiği. Geçici sıçramayı kalıcı kaymadan
	// ayıran tek şey bu: rejim kayması varsayılan 6 dilim = 30 dakika.
	DwellSeasonal int `json:"dwellSeasonal"`
	DwellRegime   int `json:"dwellRegime"`
	// MaxCandidatesPerTick — fırtına koruması. Filo geneli bir olayda
	// (ortak bağımlılık çöktü) her servis birden aday üretir; tavan,
	// EN GÜÇLÜ N tanesini geçirir. Kesilenler kaybolmaz, bir sonraki
	// tikte hâlâ ateşliyorlarsa yine sıraya girerler.
	MaxCandidatesPerTick int `json:"maxCandidatesPerTick"`
	// MinSamplesPerBucket — bir kovanın baseline sayılması için gereken
	// asgari 5-dk örneği (v0.9.957). v0.9.935'te bu kodda sabit 12'ydi;
	// vidalaştırıldı, DEĞERİ DEĞİŞMEDİ.
	//
	// Neden vida: "yeterli geçmiş" filoya bağlı. Sürekli trafik alan bir
	// API'de saat başına 12 bucket'ın hepsi dolar; günde birkaç kez
	// koşan bir batch işçisinde 3 tanesi dolar ve o servis 28 gün
	// beklese de bu eşiği geçemez.
	MinSamplesPerBucket int `json:"minSamplesPerBucket"`
	// MinBucketRepeats — bir kovanın baseline sayılması için gereken
	// asgari FARKLI GÜN sayısı, yani "bu haftanın-saatini kaç kez
	// gördüm" (v0.9.957).
	//
	// ── Neden AYRI bir kapı, MinSamplesPerBucket yetmiyor ────────────
	// ÖLÇÜLDÜ (lokal, 2026-08-11): service_summary_5m 9 gün taşıyordu
	// ve her servisin hedef kovasında TAM 24 örnek vardı — yani 12'lik
	// örnek kapısı rahat geçiliyordu. Ama o 24 örnek YALNIZ İKİ GÜNDEN
	// geliyordu (aynı saatin iki haftalık tekrarı). Mevsimsel z'nin
	// ölçtüğü şey haftadan haftaya YAYILIM; iki gözlemden yayılım
	// kestirilemez. Sonuç: MAD dejenere, z patladı, tek tikte 178 aday
	// (ezici çoğunluğu "istek hızı mevsimsel sapma ↓", hepsi aynı
	// kovada). Örnek SAYISI kapısı bunu göremez çünkü sayı yeterliydi;
	// eksik olan ÇEŞİTLİLİKTİ.
	//
	// Bu v0.8.250'nin sınıfı (örnek kıtlığı → baseline'a güvenilemez)
	// ve oradaki karar burada da geçerli: SESSİZ KALMAK yalan
	// söylemekten iyidir. 3 = "yayılımdan söz edebilmek için en az üç
	// gözlem". 28 günlük pencerede 4 tekrar var, yani vida dolu bir
	// kurulumu susturmuyor; henüz 3 haftası dolmamış bir kurulumu
	// (ya da lokal 9 günü) susturuyor — ki orada söyleyecek dürüst bir
	// şey zaten yok.
	MinBucketRepeats int `json:"minBucketRepeats"`
}

// IsEnabled — nil-güvenli okuma. Yazılmamış = AÇIK.
func (b AnomalyBehaviorConfig) IsEnabled() bool {
	return b.Enabled == nil || *b.Enabled
}

// Davranış motoru kelepçeleri. Üst sınırlar "vidayı sonuna kadar
// çevirdim, motor sustu / motor her şeyi açtı" hallerini engelliyor.
const (
	behaviorMinSeasonalZ   = 1.0
	behaviorMaxSeasonalZ   = 50.0
	behaviorMinRegimeRatio = 1.05 // 1.0 = "her kıpırtı rejim kaymasıdır"
	behaviorMaxRegimeRatio = 100.0
	behaviorMinDwell       = 1
	behaviorMaxDwell       = 24 // 2 saat; ötesi "hiç açma" demek
	behaviorMinCandidates  = 1
	behaviorMaxCandidates  = 5000
	behaviorMinSamplesLo   = 1
	behaviorMinSamplesHi   = 576 // 28 gün × 4 tekrar × 12 bucket'ın üstü: ötesi "hiç açma"
	behaviorMinRepeatsLo   = 1
	// behaviorMinRepeatsHi — pencerede FİİLEN mümkün olan tekrar sayısı
	// (28 gün / 7). Üstüne çıkmak motoru KALICI olarak susturur ve bunu
	// hiçbir ekran açıklayamazdı; kelepçe o sessiz hâli imkânsız kılıyor.
	behaviorMinRepeatsHi = behaviorBaselineWeeks
	// behaviorBaselineWeeks — internal/anomaly.behaviorBaselineDays / 7.
	// BİLİNÇLİ TEKRAR: chstore, anomaly'yi import EDEMEZ (ters yön —
	// anomaly zaten chstore'u import ediyor), AnomalySensitivityMetrics
	// ile aynı durum. Pencere değişirse burası da değişmeli.
	behaviorBaselineWeeks = 4
)

// DefaultAnomalyBehavior — spec'te onaylanan varsayılanlar
// (2026-08-10). Ayrı fonksiyon çünkü Normalize hem eksik alanı hem de
// aralık dışı alanı BURADAN dolduruyor.
func DefaultAnomalyBehavior() AnomalyBehaviorConfig {
	return AnomalyBehaviorConfig{
		Enabled:              boolPtr(true),
		SeasonalZ:            4.0,
		RegimeRatio:          1.5,
		DwellSeasonal:        3, // 15 dk
		DwellRegime:          6, // 30 dk
		MaxCandidatesPerTick: 50,
		// v0.9.957 — 12: v0.9.935'in koddaki sabitinin AYNISI (davranış
		// değişmiyor, yalnız vidalaşıyor). 3: ölçülmüş kapı; gerekçe
		// MinBucketRepeats alanının yorumunda.
		MinSamplesPerBucket: 12,
		MinBucketRepeats:    3,
	}
}

// NormalizeAnomalyBehavior — her alanı kelepçeler; aralık dışı ya da
// sayı-olmayan değer VARSAYILANA döner (sıfırlanmaz).
//
// Ani-sapma alanlarından FARKLI duruş: orada 0 meşrudur ("vidayı
// kapat"), burada değildir. seasonalZ=0 ya da dwell=0 "her bucket
// aday" demek olurdu, yani motoru kapatmak değil AÇIK SONUNA KADAR
// açmak. Motoru kapatmanın yolu Enabled=false.
func NormalizeAnomalyBehavior(b AnomalyBehaviorConfig) AnomalyBehaviorConfig {
	d := DefaultAnomalyBehavior()
	out := AnomalyBehaviorConfig{
		// Normalize SOMUTLAŞTIRIR: nil gelen bayrak açık haliyle
		// yazılır, böylece kaydedilen blob ne olduğunu AÇIKÇA söyler.
		Enabled:              boolPtr(b.IsEnabled()),
		SeasonalZ:            clampRangeF(b.SeasonalZ, behaviorMinSeasonalZ, behaviorMaxSeasonalZ, d.SeasonalZ),
		RegimeRatio:          clampRangeF(b.RegimeRatio, behaviorMinRegimeRatio, behaviorMaxRegimeRatio, d.RegimeRatio),
		DwellSeasonal:        clampRangeI(b.DwellSeasonal, behaviorMinDwell, behaviorMaxDwell, d.DwellSeasonal),
		DwellRegime:          clampRangeI(b.DwellRegime, behaviorMinDwell, behaviorMaxDwell, d.DwellRegime),
		MaxCandidatesPerTick: clampRangeI(b.MaxCandidatesPerTick, behaviorMinCandidates, behaviorMaxCandidates, d.MaxCandidatesPerTick),
		MinSamplesPerBucket:  clampRangeI(b.MinSamplesPerBucket, behaviorMinSamplesLo, behaviorMinSamplesHi, d.MinSamplesPerBucket),
		MinBucketRepeats:     clampRangeI(b.MinBucketRepeats, behaviorMinRepeatsLo, behaviorMinRepeatsHi, d.MinBucketRepeats),
	}
	return out
}

// clampRangeF / clampRangeI — aralık dışı (ya da NaN/Inf) → varsayılan.
func clampRangeF(v, min, max, def float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < min || v > max {
		return def
	}
	return v
}

func clampRangeI(v, min, max, def int) int {
	if v < min || v > max {
		return def
	}
	return v
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
		// v0.9.935 — davranış motoru AŞAMA 1; varsayılan AÇIK, ama
		// adaylar mevcut terfi kapısından geçtiği için kendiliğinden
		// Problem/bildirim üretmez: /anomalies akışına düşerler.
		Behavior: DefaultAnomalyBehavior(),
		// v0.9.827 — AÇIK = bugünkü davranış. Bu sürüm bir GÖRÜNÜRLÜK
		// sürümü: var olan davranışı kapatılabilir yapıyor, kendiliğinden
		// değiştirmiyor.
		AttachToIncident: boolPtr(true),
	}
}

func boolPtr(b bool) *bool { return &b }

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
		// Normalize SOMUTLAŞTIRIR: nil gelen bayrak açık haliyle yazılır.
		// Böylece kaydedilen blob her zaman ne olduğunu AÇIKÇA söyler ve
		// bir sonraki okuyucu (ya da elle bakan operatör) varsayılanı
		// tahmin etmek zorunda kalmaz.
		AttachToIncident: boolPtr(c.AttachesToIncident()),
		// v0.9.935 — davranış bölümü kendi kelepçesinden geçer. Eksik
		// bölüm (bu sürümden ESKİ her settings satırı) varsayılanını
		// alır: sıfır-değerli bir AnomalyBehaviorConfig aralık dışıdır,
		// dolayısıyla Normalize onu varsayılanlara doldurur.
		Behavior: NormalizeAnomalyBehavior(c.Behavior),
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
