// Davranış motoru — AŞAMA 1 (v0.9.935). Mevsimsel baseline + rejim
// kayması. Bu dosya SAF ÇEKİRDEK: kova hesabı, SQL şekli, sapma/kayma
// kararı ve fırtına tavanı. Canlı CH'ye bağlanan tarama tikinin kendisi
// ayrı sürümde (behavior_scan.go).
//
// ─── Neden var ───────────────────────────────────────────────────────
// Mevcut metrik dedektörü (anomaly.go) bir soruyu iyi cevaplıyor: "şu
// an sıçradı mı?". Baseline'ı son 24 saatin ardışık bucket'ları (ya da
// aynı-slot mevsimseli) ve dwell'i 15 dakika. Cevaplayamadığı soru:
// "bu servis ARTIK BAŞKA TÜRLÜ mü davranıyor?" — deploy sonrası P99'un
// 120ms'ten 190ms'e oturması 24 saat içinde yeni normal olur ve ani-
// sapma dedektörü onu ertesi gün göremez. Dynatrace/Datadog'un
// "behavioural baseline" katmanı tam olarak bu boşluğu kapatır.
//
// ─── Kova seçimi: 168 (haftanın saati), 48 DEĞİL ─────────────────────
// Brief iki seçenek bırakmıştı. 168 seçildi çünkü ÖRNEK SAYISI YETİYOR:
// 28 gün = her haftanın-saati kovasının 4 tekrarı × saat başına 12 adet
// 5-dk MV bucket'ı = kova başına 48 örnek. 48 örnek medyan+MAD için
// rahat; v0.8.250'nin (örnek kıtlığı → mevsimsel baseline'a
// güvenilemiyor → düz pencereye düşüş → gün-gece karışması) tekrarına
// düşmüyoruz. 48 kovalı basitleştirme (saat × haftaiçi/sonu) yalnız
// örnek kıtlığında gerekliydi; burada yok, ve 168 haftaiçi günleri
// arasındaki gerçek farkı (pazartesi sabahı ≠ çarşamba sabahı) koruyor.
//
// KOVA UTC'DE. v0.8.323'ün dersi: Go tarafı slot'u yerel saatten,
// SQL tarafı CH'nin varsayılan saat diliminden türetirse iki taraf
// SESSİZCE farklı zamanı eşleştirir ve mevsimsel baseline gündüz
// geçmişini gece "şimdi"siyle kıyaslar. İki taraf da 'UTC' pinli.
// DST de bu yüzden dert değil: UTC'de yaz saati sıçraması yok.
package anomaly

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// behaviorKind — anomaly_events'teki kind. YENİ TABLO YOK: adaylar
// mevcut olay tablosuna düşer, mevcut terfi kapısından (evaluator
// promoteStrongAnomalies) geçer, mevcut bildirim hunisini kullanır.
const behaviorKind = "behavior_change"

// behaviorMetrics — motorun ölçtüğü RED metrikleri. anomaly_tracked'den
// (ani-sapma dedektörünün metrik seti) BİLİNÇLİ OLARAK BAĞIMSIZ.
//
// Gerekçe: request_rate ani-sapma tarafında operatör kararıyla KAPALI
// (v0.9.800 — hacim sıçraması bu filoda kampanya/batch/nöbet devriyle
// normalde de oluyor). Ama "bu servisin hacmi kalıcı olarak yarıya
// düştü" bambaşka bir ifadedir ve tam olarak bu motorun cevaplaması
// gereken soru. İki set ayrı olduğu için motoru kapatmanın yolu da
// ayrı bir vida (Behavior.Enabled), başka bir bölümün yan etkisi değil.
var behaviorMetrics = []string{"error_rate", "p99_ms", "request_rate"}

const (
	// behaviorBaselineDays — kaç günlük geçmişten öğrenilir. 28 = her
	// haftanın-saati kovasının 4 tekrarı. service_summary_5m'in TTL'i
	// 90 gün olduğu için pencere her zaman kapsanır.
	behaviorBaselineDays = 28
	// behaviorRecentHours — "şimdi" penceresi: son bu kadar saatin
	// tamamlanmış bucket'ları. En uzun dwell (24) × 5 dk = 2 saat, artı
	// pay. Aynı sorguda okunur, ayrı sorgu AÇILMAZ.
	behaviorRecentHours = 3
	// behaviorNeighborHours — baseline'a katılan komşu haftanın-saati
	// kovaları (±). 1 olması ŞART, tercih değil: dwell penceresi saat
	// sınırını aşabilir (10:05'teki 6 dilim 09:35'e uzanır) ve her
	// dilim KENDİ kovasının baseline'ına karşı puanlanır.
	behaviorNeighborHours = 1
	// behaviorMinBaselineSamples — bir kovanın baseline sayılması için
	// gereken asgari örnek. 48'in dörtte biri. ALTINDA SİNYAL YOK —
	// yeni/seyrek servis sessiz kalır, uydurma baseline üretilmez.
	behaviorMinBaselineSamples = 12
	// behaviorDeployLookback — adayın başlangıcından ne kadar geriye
	// bakıp deploy arıyoruz. 30 dk: bir rollout'un metriğe yansıması
	// bu pencereye sığar; daha genişi tesadüfi eşleşme üretir.
	behaviorDeployLookback = 30 * time.Minute
	// hoursPerWeek — kova uzayı.
	hoursPerWeek = 168
)

// hourOfWeek — t'nin haftanın-saati kovası, 0..167, UTC, PAZARTESİ=0.
// SQL'deki karşılığı: (toDayOfWeek(tb,0,'UTC') - 1) * 24 + toHour(tb,'UTC')
// — toDayOfWeek mode 0: 1=Pzt … 7=Paz, yani -1 ile 0-tabanlı oluyor.
// Saf, tablo-testli: iki tarafın AYNI kovayı vermesi bu motorun
// doğruluğunun tamamı.
func hourOfWeek(t time.Time) int {
	u := t.UTC()
	// Go'da Sunday=0 … Saturday=6; SQL mode 0'a hizalamak için pazarı
	// haftanın SONUNA taşıyoruz (Pzt=0 … Paz=6).
	dow := (int(u.Weekday()) + 6) % 7
	return dow*24 + u.Hour()
}

// hourOfWeekDistance — iki kova arasındaki DAİRESEL uzaklık (0..84).
// Dairesel olmak zorunda: pazar 23:00 ile pazartesi 00:00 komşudur,
// 167 saat uzak değil. SQL'deki least(abs(d), 168-abs(d)) ifadesinin
// Go ikizi.
func hourOfWeekDistance(a, b int) int {
	d := a - b
	if d < 0 {
		d = -d
	}
	if r := hoursPerWeek - d; r < d {
		return r
	}
	return d
}

// behaviorRow — MV'den okunan tek bucket. Metrik türetimi Go tarafında
// (behaviorMetricValue) çünkü üç metrik AYNI granülden çıkıyor: SQL'de
// üç ayrı ifade döndürmek yerine ham sayaçları taşımak sorguyu tek
// tutuyor ve error_rate'in payda-sıfır davranışını test edilebilir
// kılıyor.
type behaviorRow struct {
	Unix  int64  // bucket başlangıcı (unix sn)
	HOW   int    // haftanın saati, 0..167
	Spans uint64 // bucket'taki span sayısı
	Errs  uint64 // hatalı span sayısı
	P99Ms float64
}

// behaviorMetricValue — bir bucket'ın metrik değeri. metricValueExpr
// (ani-sapma dedektörünün SQL ifadeleri) ile BİREBİR aynı tanımlar
// olmak zorunda, yoksa iki motor aynı seriye farklı sayılar der ve
// operatör /anomalies'te birbiriyle çelişen iki satır görür.
//
//	error_rate   : errs / spans * 100   (spans=0 → 0, SQL'deki nullIf'in karşılığı)
//	request_rate : spans / 300          (5-dk bucket'ı saniyeye böl)
//	p99_ms       : quantilesTDigestMerge(...)[3] / 1e6
func behaviorMetricValue(metric string, r behaviorRow) float64 {
	switch metric {
	case "error_rate":
		if r.Spans == 0 {
			return 0
		}
		return float64(r.Errs) / float64(r.Spans) * 100
	case "request_rate":
		return float64(r.Spans) / 300.0
	case "p99_ms":
		return r.P99Ms
	}
	return 0
}

// buildBehaviorBucketsQuery — motorun TEK toplu sorgusu.
//
// ŞEKLİ NEDEN BÖYLE (SQL-pin testli — behavior_test.go):
//
//   - service_summary_5m, ham spans DEĞİL (MV-first invariant #3). 28
//     günlük bir filo taramasını ham spans üzerinde yapmak milyarlarca
//     satır demek; MV'de aynı pencere ~yüz binlerce satır.
//   - Servis filtresi YOK, GROUP BY service_name VAR: tüm servisler tek
//     geçişte. v0.8.507'nin dersi — servis başına sorgu, prod'da 1400
//     servis × tik demektir.
//   - WHERE üç parça: zaman sınırı (günlük partition budaması), kova
//     eşleşmesi (dairesel, ±behaviorNeighborHours) ve "son pencere"
//     dalı. Kova dalı 28 günün 1/56'sını, son-pencere dalı son 3 saati
//     bırakır; ikisi ARASINDA örtüşme yok (son pencere en çok 3 saat,
//     aynı kova bir hafta önce tekrarlanıyor).
//   - Üst sınır lastCompleteBucketStart: v0.8.316'nın dersi — dolmakta
//     olan bucket request_rate'i (÷ sabit 300) geçmiş/300 kadar okur ve
//     canlı bir kayma her tikin ilk dakikasında baseline gibi görünür.
//   - LIMIT + max_execution_time: hard constraint. Servis başına tavan
//     ISIRAMAZ ve bu kanıtlanabilir — pencere yapısı gereği servis
//     başına en çok (3 kova × 12 bucket × 4 tekrar) + (3 saat × 12) =
//     180 satır üretebilir; tavan 600 ile 3× pay var. Tavanın ısırması
//     ORDER BY t nedeniyle en YENİ bucket'ları düşürürdü, yani sessizce
//     "şimdi"yi kaybederdik — bu yüzden pay bilinçli geniş.
//
// Beş `?` sırayla: cutoff (28g), upper (son tam bucket), hedef kova,
// hedef kova (dairesel ifadede iki kez), komşu yarıçapı, son-pencere
// başlangıcı.
func buildBehaviorBucketsQuery() string {
	// howExpr — bucket'ın haftanın-saati kovası. hourOfWeek()'in SQL
	// ikizi; ikisi de UTC pinli (v0.8.323).
	const howExpr = "((toDayOfWeek(time_bucket, 0, 'UTC') - 1) * 24 + toHour(time_bucket, 'UTC'))"
	return fmt.Sprintf(`
		SELECT service_name,
		       toUnixTimestamp(time_bucket) AS t,
		       -- toInt32: UInt8 aritmetiği CH'de sürüme göre farklı tipe
		       -- terfi eder; sürücü tarafında Scan hedefi SABİT olmalı.
		       toInt32(%[1]s) AS how,
		       countMerge(span_count_state) AS spans,
		       countMerge(error_count_state) AS errs,
		       quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state)[3] / 1e6 AS p99_ms
		FROM service_summary_5m
		WHERE time_bucket >= ? AND time_bucket < ?
		  AND (least(abs(%[1]s - ?), %[2]d - abs(%[1]s - ?)) <= ?
		       OR time_bucket >= ?)
		GROUP BY service_name, t, how
		ORDER BY service_name, t
		LIMIT 600 BY service_name
		LIMIT 4000000
		SETTINGS max_execution_time = 25`, howExpr, hoursPerWeek)
}

// splitBehaviorSeries — bir servisin satırlarını baseline (kova →
// örnekler) ve "şimdi" penceresine ayırır.
//
// KESİM NOKTASI recentCutoff: ondan ESKİ satırlar baseline, YENİ olanlar
// pencere. Böylece bugünün kendi sapması kendi baseline'ını KİRLETMEZ —
// aksi halde 6 saattir süren bir kayma, 4 tekrarın birine karışıp
// medyanı kendine doğru çeker ve motor kendi bulduğu şeyi normalleştirir.
//
// Saf; satırların zamana göre ARTAN geldiği varsayılır (SQL ORDER BY
// service_name, t).
func splitBehaviorSeries(rows []behaviorRow, metric string, recentCutoffUnix int64) (baseline map[int][]float64, recent []behaviorRow) {
	baseline = make(map[int][]float64)
	for _, r := range rows {
		if r.Unix >= recentCutoffUnix {
			recent = append(recent, r)
			continue
		}
		baseline[r.HOW] = append(baseline[r.HOW], behaviorMetricValue(metric, r))
	}
	return baseline, recent
}

// behaviorCandidate — motorun ürettiği tek aday. anomaly_events satırına
// dönüşmeden ÖNCEKİ saf hâli; testler bu yapıyı doğruluyor.
type behaviorCandidate struct {
	Service string
	Metric  string
	// Signal — "regime" | "seasonal". İkisi AYNI fingerprint'e yazılır
	// (servis+metrik), yani bir kayma önce mevsimsel olarak görünüp
	// sonra rejime "terfi ettiğinde" YENİ satır açılmaz, aynı satır
	// güncellenir. Bu bilinçli: operatörün gördüğü şey tek bir olay.
	Signal    string
	Direction string  // "up" | "down"
	Ratio     float64 // current / baseline medyanı (düşüşte < 1)
	Z         float64 // son dilimin robust z'si (işaretli)
	Baseline  float64 // kova medyanı
	Current   float64 // son tamamlanmış dilimin değeri
	HOW       int     // son dilimin haftanın-saati kovası
	Dwell     int     // kaç dilim boyunca ateşledi
	OnsetUnix int64   // sürekli pencerenin İLK dilimi — "ne zaman değişti"
	Spans     uint64  // penceredeki toplam span (terfi kapısının hacim tabanı)
	// Score — fırtına tavanının sıralama ölçütü. |z| değil |z| ve oranın
	// birlikte: rejim kaymaları düşük z'de gerçek olabilir.
	Score float64
	// Deploy — adayın başlangıcından geriye behaviorDeployLookback
	// içinde görülen deploy. nil = korelasyon yok (deploy'suz kalıcı
	// kaymalar da yakalanır — brief).
	Deploy *chstore.RecentDeployEntry
}

// behaviorConfigView — evalBehavior'ın ihtiyaç duyduğu eşikler. Ayrı
// tip DEĞİL, chstore.AnomalyBehaviorConfig'in kendisi kullanılıyor;
// bu takma ad yalnız okunabilirlik için.
type behaviorConfigView = chstore.AnomalyBehaviorConfig

// evalBehavior — bir (servis, metrik) için SAF karar.
//
// SIRA ÖNEMLİ: önce rejim, sonra mevsimsel. Kalıcı bir kayma İKİSİNİ
// birden tetikler (6 dilim ⊃ 3 dilim) ve ikisini de yazmak aynı olayı
// iki satır olarak göstermek olurdu — bu depoda "anomali fırtınası"nın
// tam olarak nasıl oluştuğu. Rejim daha SPESİFİK ifadedir (yalnız
// kalıcı olan), o yüzden kazanır.
//
// pol — operatörün ANI-SAPMA hassasiyet vidaları (aynı metrik için).
// BİLİNÇLİ PAYLAŞIM: "operasyonel olarak anlamsız" tanımı iki motorda
// farklı olamaz. 1.90ms medyanlı bir op'un 2.9ms'e oturması %52 kalıcı
// kaymadır ve hiç kimseyi ilgilendirmez — v0.9.826'nın kapattığı sınıfı
// bu motorda yeniden açmamak için floorPct/absFloor/minAbsDelta/minMAD
// aynen uygulanır. Motorun KENDİ vidaları (seasonalZ, regimeRatio,
// dwell) "ne kadar DEĞİŞİM" sorusunu, paylaşılanlar "değişim anlamlı
// mı" sorusunu cevaplıyor.
func evalBehavior(
	service, metric string,
	baseline map[int][]float64,
	recent []behaviorRow,
	pol metricPolicy,
	cfg behaviorConfigView,
) (behaviorCandidate, bool) {
	if len(recent) == 0 {
		return behaviorCandidate{}, false
	}
	// Hacim kapısı — son dilimin istek hızı. Ani-sapma dedektörüyle aynı
	// gerekçe: 20 isteğin 1'i hata %5'tir ama olay değildir. AÇILMAYA
	// uygulanır; bu motorda "çözülme" yok (olaylar last_seen tazeliğiyle
	// kendiliğinden temizlenir), o yüzden asimetri sorusu doğmuyor.
	last := recent[len(recent)-1]
	if !hasEnoughVolume([]float64{behaviorMetricValue("request_rate", last)}, pol.minBaselineRate) {
		return behaviorCandidate{}, false
	}
	// Rejim önce (daha spesifik), sonra mevsimsel.
	if c, ok := evalBehaviorWindow(service, metric, baseline, recent, pol, cfg, "regime", cfg.DwellRegime); ok {
		return c, true
	}
	return evalBehaviorWindow(service, metric, baseline, recent, pol, cfg, "seasonal", cfg.DwellSeasonal)
}

// evalBehaviorWindow — tek bir sinyalin dwell penceresini puanlar.
//
// HER DİLİM KENDİ KOVASINA KARŞI puanlanır. Pencere saat sınırını
// aşabildiği için (10:05'teki 6 dilim 09:35'te başlar) tek bir kova
// baseline'ı kullanmak, sabah rampasının ortasında sistematik yanlılık
// üretirdi: 09:xx dilimlerini 10:00 baseline'ına vurmak.
//
// AÇILMA KOŞULU — hepsi birden:
//  1. pencerenin her dilimi AYNI yönde ateşliyor (geçici sıçrama reddi),
//  2. her dilimin kovası behaviorMinBaselineSamples örnek görmüş
//     (baseline'sız servis SESSİZ — uydurma yok),
//  3. SON dilim operatörün anlamlılık tabanlarını geçiyor.
func evalBehaviorWindow(
	service, metric string,
	baseline map[int][]float64,
	recent []behaviorRow,
	pol metricPolicy,
	cfg behaviorConfigView,
	signal string,
	dwell int,
) (behaviorCandidate, bool) {
	if dwell <= 0 || len(recent) < dwell {
		return behaviorCandidate{}, false
	}
	window := recent[len(recent)-dwell:]

	dir := ""
	var lastZ, lastRatio, lastMedian, lastValue float64
	var spans uint64
	for i, r := range window {
		samples := baseline[r.HOW]
		if len(samples) < behaviorMinBaselineSamples {
			// DÜRÜSTLÜK: baseline yoksa sinyal de yok. Yeni bir servis
			// ilk 4 haftasında sessiz kalır; "veri yok" bir anomali
			// değildir.
			return behaviorCandidate{}, false
		}
		med, rawMAD := medianMAD(samples)
		mad := effectiveMAD(metric, med, rawMAD, pol.minMAD)
		v := behaviorMetricValue(metric, r)
		z := madScale * (v - med) / mad
		ratio := v / math.Max(math.Abs(med), magnitudeEps)

		d := behaviorFires(signal, z, ratio, cfg)
		if d == "" {
			return behaviorCandidate{}, false
		}
		if i == 0 {
			dir = d
		} else if d != dir {
			// Yön değiştirdi: bu kalıcı bir kayma değil, salınım.
			return behaviorCandidate{}, false
		}
		spans += r.Spans
		lastZ, lastRatio, lastMedian, lastValue = z, ratio, med, v
	}

	// Operatörün anlamlılık tabanları — son dilim üzerinden.
	// v0.9.826'nın vidaları; gerekçe evalBehavior yorumunda.
	absDelta := math.Abs(lastValue - lastMedian)
	if absDelta/math.Max(math.Abs(lastMedian), magnitudeEps) < pol.floorPct {
		return behaviorCandidate{}, false
	}
	if pol.minAbsDelta > 0 && absDelta < pol.minAbsDelta {
		return behaviorCandidate{}, false
	}
	if dir == "up" && pol.absFloor > 0 && lastValue < pol.absFloor {
		return behaviorCandidate{}, false
	}

	return behaviorCandidate{
		Service: service, Metric: metric,
		Signal: signal, Direction: dir,
		Ratio: lastRatio, Z: lastZ,
		Baseline: lastMedian, Current: lastValue,
		HOW:       window[len(window)-1].HOW,
		Dwell:     dwell,
		OnsetUnix: window[0].Unix,
		Spans:     spans,
		Score:     behaviorScore(lastZ, lastRatio),
	}, true
}

// behaviorFires — tek dilimin verdicti: "up" | "down" | "" (ateşlemedi).
// Saf ve minik olması bilinçli: iki sinyalin eşik SEMANTİĞİ burada tek
// yerde duruyor, dwell döngüsünde dağılmıyor.
//
//	seasonal : |z| >= seasonalZ
//	regime   : ratio >= regimeRatio  ya da  ratio <= 1/regimeRatio
func behaviorFires(signal string, z, ratio float64, cfg behaviorConfigView) string {
	switch signal {
	case "regime":
		if cfg.RegimeRatio <= 1 {
			return "" // kelepçe zaten engelliyor; savunma amaçlı
		}
		if ratio >= cfg.RegimeRatio {
			return "up"
		}
		if ratio <= 1/cfg.RegimeRatio {
			return "down"
		}
	case "seasonal":
		if z >= cfg.SeasonalZ {
			return "up"
		}
		if z <= -cfg.SeasonalZ {
			return "down"
		}
	}
	return ""
}

// behaviorScore — fırtına tavanının sıralama ölçütü.
//
// |z| TEK BAŞINA YETMEZ: sıkı bir baseline'da %10'luk bir kıpırtı 20σ
// üretebilirken, gevşek bir baseline'da 3× kalıcı kayma 4σ kalır.
// İkincisi operatör için daha önemli.
//
// İKİ TASARIM KARARI:
//
//	z DOYAR (behaviorScoreZCap). ~10σ'nın ötesinde z artık bilgi
//	taşımıyor — "kesinlikle gerçek" ile "kesinlikle gerçek" arasında
//	operatör açısından fark yok. Doymasaydı sıkı baseline'lı servisler
//	tavanı tek başına doldururdu ve bu tam olarak YANLIŞ küme: MAD'i
//	en küçük olan servis, tarihte en az kıpırdamış olandır.
//
//	BÜYÜKLÜK log(oran) ile ölçülür — 2× ile 0.5× aynı büyüklüktedir
//	(simetri) ve büyük oranlarda doğal olarak yavaşlar.
func behaviorScore(z, ratio float64) float64 {
	r := math.Abs(math.Log(math.Max(ratio, magnitudeEps)))
	return math.Min(math.Abs(z), behaviorScoreZCap) + behaviorScoreRatioWeight*r
}

const (
	// behaviorScoreZCap — z'nin sıralamaya katkısının tavanı (σ).
	behaviorScoreZCap = 10.0
	// behaviorScoreRatioWeight — log(oran)'ın ağırlığı. 20 ile 2×'lik
	// bir kayma (ln2≈0.69 → 13.9) doymuş bir z'nin (10) üstüne çıkar:
	// "ne kadar DEĞİŞTİ" sorusu "ne kadar EMİNİM" sorusunu yener.
	behaviorScoreRatioWeight = 20.0
)

// capBehaviorCandidates — fırtına koruması. EN GÜÇLÜ max tanesini
// bırakır; eşitlikte (servis, metrik) ile deterministik sıralar, yoksa
// aynı tik iki podda farklı kesim yapar ve "hangi anomali gitti"
// sorusunun cevabı yok olur.
//
// Kesilenler KAYBOLMAZ: bir sonraki tikte hâlâ ateşliyorlarsa yine
// sıraya girerler. Motor durum tutmadığı için (state tablosu YOK) bu
// otomatik.
func capBehaviorCandidates(cands []behaviorCandidate, max int) []behaviorCandidate {
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Score != cands[j].Score {
			return cands[i].Score > cands[j].Score
		}
		if cands[i].Service != cands[j].Service {
			return cands[i].Service < cands[j].Service
		}
		return cands[i].Metric < cands[j].Metric
	})
	if max > 0 && len(cands) > max {
		return cands[:max]
	}
	return cands
}

// pickBehaviorDeploy — adayın başlangıcından geriye behaviorDeployLookback
// içindeki EN YAKIN deploy. Yoksa nil.
//
// KORELASYON, NEDENSELLİK DEĞİL ve metin de öyle diyor: aday "deploy
// yüzünden" demiyor, "şu deploy'dan N dakika sonra başladı" diyor.
// Onset'ten SONRAKİ deploy'lar elenir — sonradan gelen bir rollout
// önceki kaymayı açıklayamaz.
func pickBehaviorDeploy(deploys []chstore.RecentDeployEntry, onsetUnix int64) *chstore.RecentDeployEntry {
	onsetNs := onsetUnix * int64(time.Second)
	lowNs := onsetNs - int64(behaviorDeployLookback)
	var best *chstore.RecentDeployEntry
	for i := range deploys {
		d := deploys[i]
		if d.FirstSeenNs > onsetNs || d.FirstSeenNs < lowNs {
			continue
		}
		if best == nil || d.FirstSeenNs > best.FirstSeenNs {
			best = &deploys[i]
		}
	}
	return best
}

// behaviorDetails — anomaly_events.sample'a yazılan makine-okunur
// ayrıntı. AYRI KOLON/TABLO YOK (invariant #5 ile aynı duruş): sample
// zaten "tespit anında yakalanan kanıt" alanı ve log/trace kindlerinde
// de öyle kullanılıyor. Alanlar brief'in istediği küme: metrik, yön,
// oran, kova, deploy ilişkisi.
type behaviorDetails struct {
	Metric     string  `json:"metric"`
	Signal     string  `json:"signal"`
	Direction  string  `json:"direction"`
	Ratio      float64 `json:"ratio"`
	Z          float64 `json:"z"`
	Baseline   float64 `json:"baseline"`
	Current    float64 `json:"current"`
	Unit       string  `json:"unit"`
	HourOfWeek int     `json:"hourOfWeek"`
	Dwell      int     `json:"dwell"`
	OnsetNs    int64   `json:"onsetNs"`
	// Deploy — korelasyon varsa. omitempty: alan YOKSA "deploy ilişkisi
	// aranmadı" ile "arandı, bulunamadı" ayırt edilemezdi; motor her
	// adayda arıyor, yani yokluk = bulunamadı.
	Deploy *behaviorDeployRef `json:"deploy,omitempty"`
}

type behaviorDeployRef struct {
	Version    string `json:"version"`
	AgeSeconds int64  `json:"ageSeconds"` // deploy ile onset arası
}

// encodeBehaviorDetails — sample alanına yazılacak tek satırlık JSON.
// Saf + testli: /anomalies çekmecesi bunu ayrıştırıp insan cümlesine
// çeviriyor, yani şeklin sessizce değişmesi UI'yı ham JSON göstermeye
// düşürür.
func encodeBehaviorDetails(c behaviorCandidate) string {
	d := behaviorDetails{
		Metric: c.Metric, Signal: c.Signal, Direction: c.Direction,
		Ratio: round2(c.Ratio), Z: round2(c.Z),
		Baseline: round2(c.Baseline), Current: round2(c.Current),
		Unit:       unitOf(c.Metric),
		HourOfWeek: c.HOW, Dwell: c.Dwell,
		OnsetNs: c.OnsetUnix * int64(time.Second),
	}
	if c.Deploy != nil {
		d.Deploy = &behaviorDeployRef{
			Version:    c.Deploy.Version,
			AgeSeconds: (c.OnsetUnix*int64(time.Second) - c.Deploy.FirstSeenNs) / int64(time.Second),
		}
	}
	b, err := json.Marshal(d)
	if err != nil {
		return ""
	}
	return string(b)
}

// round2 — JSON'da 17 haneli float gürültüsü olmasın. Sunum kararı,
// hesap kararı değil: eşik karşılaştırmaları ham değerlerle yapılıyor.
func round2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*100) / 100
}

// behaviorPattern — anomaly_events.pattern: KARARLI kimlik + insan
// etiketi bir arada. Fingerprint bu metinden DEĞİL ham metrik adından
// hesaplanır (behaviorEventID), yani etiket ileride değişse bile
// mevcut satırlar bölünmez — v0.6.27'nin log_template_new'de kurduğu
// düzenin aynısı (fingerprint TemplateID'den, pattern render'dan).
func behaviorPattern(metric string) string {
	return displayMetric(metric) + " · davranış değişimi"
}

// behaviorEventID — (servis, metrik) başına TEK satır. Sinyal
// fingerprint'e GİRMEZ: mevsimsel olarak başlayıp rejime dönüşen bir
// kayma aynı olaydır ve iki satır olarak görünmemeli.
func behaviorEventID(service, metric string) string {
	return chstore.FingerprintAnomaly(behaviorKind, metric, service)
}
