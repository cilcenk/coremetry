// Package anomaly runs a Watchdog/Lookout-style baseline check on a few
// key signals (error_rate, p99 latency, request_rate). For each (service,
// metric) it builds a 24h baseline of 5-minute buckets, then compares the
// most-recent bucket against that distribution. Significant deviations
// (|z-score| > openZ) are surfaced as Problems with rule_id="anomaly:*",
// auto-resolved when the value returns inside resolveZ.
//
// This is intentionally simple — no seasonality, no trend removal. It
// catches sudden spikes well; slow drifts are better handled by SLO burn.
package anomaly

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/notify"
)

const lockKey = "coremetry:lock:anomaly"

// Tunables — exposed as constants so it's obvious what to fiddle with.
const (
	bucketSeconds = 300 // 5-minute buckets
	historyHours  = 24  // window used to learn the baseline
	// v0.8.220 — operator-reported too many anomalies + transient spikes that
	// don't clear. Davis-style asymmetry: HARD to open (3.5σ AND 3 sustained
	// 5-min buckets = 15 min, so an instant blip never opens), FAST to resolve
	// (the most-recent bucket back inside the band clears it — see the detector;
	// a single-bucket dip can't re-open thanks to the 3-bucket dwell, so it
	// doesn't flap).
	openZ    = 3.5 // |z| above this opens an anomaly
	resolveZ = 1.5 // and below this clears it
	// v0.9.193 — operatör (prod): P2/P3 anomali gürültüsü çok; yalnız
	// P1-sınıfı gelsin + o da sertleşsin. criticalZ 5→6 ve decideAnomaly
	// artık YALNIZ critical verdict'te açıyor (warning-grade anomali hiç
	// Problem olmaz). request_rate DÜŞÜŞÜ istisna: trafik kaybı z'den
	// bağımsız critical'dır ve openZ'de açılmaya devam eder.
	// v0.9.826 — criticalZ ve dwellBuckets ARTIK BURADA DEĞİL.
	//
	// İkisi de operatör ayarı oldu (system_settings "anomaly_sensitivity");
	// varsayılanları chstore.DefaultAnomalySensitivity()'de. Sabitleri
	// burada bırakmak en tehlikelisi olurdu: derleyici uyarmaz, kod
	// okunur görünür, ama biri onları referans aldığında ayarı SESSİZCE
	// atlamış olur — bu depoda tekrarlayan hata sınıfı. Yokluk, doğru
	// yere bakmaya zorluyor.
	minSamples   = 12     // need at least this many baseline buckets
	madScale     = 0.6745 // scales MAD to a normal-dist stdev (modified z-score)
	magnitudeEps = 1e-9   // denominator guard for the relative-change floor

	seasonalBaseline = true // baseline from same-time-of-day history, not a flat 24h window
	// v0.8.250 — operator-reported diurnal false positives on off-peak/night
	// slots (a bank: some ops finish fast by day but slow + thin out at night).
	// Root cause was SAMPLE SCARCITY: the old baseline matched the EXACT 5-min
	// slot on the SAME weekday/weekend class over 7 days, so a Saturday night
	// slot had only ~2 candidate samples — below seasonalMinSamples — and fell
	// back to the flat 24h window, which conflates day peak with night trough
	// and fires. Three widenings feed the SAME slot more samples:
	//   • seasonalDays 7→14 (twice the history: 2 Saturdays instead of 1)
	//   • ±seasonalNeighborBuckets same-class neighbour slots join the baseline
	//     (a ±15-min window ⇒ 7 candidate buckets/day instead of 1)
	//   • the weekend class splits into saturday / sunday (a bank runs a
	//     different profile Sat vs Sun; cmt ≠ paz) — dayClass() below.
	seasonalDays            = 14 // days of same-slot history for the seasonal baseline
	seasonalMinSamples      = 4  // min same-slot samples before the seasonal baseline is trusted
	seasonalNeighborBuckets = 3  // ± same-class neighbour buckets (±15 min) folded into the baseline
)

// The tracked-metric SET is intentionally small (cardinality stays
// bounded: services × tracked checks per tick) and, since v0.9.800,
// OPERATOR-DRIVEN — system_settings key "anomaly_tracked", published on
// the Store (chstore.AnomalyTrackedConfig). Varsayılan: error_rate +
// p99_ms açık, request_rate KAPALI (operatör 2026-08-09: request_rate
// anomalileri false-pozitif; hacim bu filoda kampanya/batch/nöbet
// devriyle normalde de sıçrıyor).
//
// Kapalı bir metrik HİÇ ölçülmez: ne toplu MV okuması açılır ne de
// checkOne çalışır — tarama maliyeti de metrik başına 2 sorgu düşer.
// Kanonik liste ve varsayılanlar chstore.AnomalyTrackedMetrics /
// DefaultAnomalyTracked'de; buradaki metricPolicies + metricValueExpr
// ile aynı küme olmak zorunda.
//
// trackedMetricsNow — bu tikte ölçülecek metrikler. Atomic load (CH
// okuması değil): blob boot'ta hidrate edilir, admin PUT'unda takas
// edilir, çok-pod kurulumlarda 30 sn'de yakınsar — internal/api/
// anomaly_tracked.go. Bilinmeyen bir metrik adı buraya SIZAMAZ:
// chstore tarafı kanonik olmayan anahtarları düşürür, burada da
// metricValueExpr'i olmayan her ad ayıklanır (elle düzenlenmiş bir
// settings satırı dedektöre var olmayan bir MV ifadesi sorduramaz).
func (d *Detector) trackedMetricsNow() []string {
	enabled := d.store.AnomalyTracked().Enabled()
	out := make([]string, 0, len(enabled))
	for _, m := range enabled {
		if _, err := metricValueExpr(m); err != nil {
			log.Printf("[anomaly] bilinmeyen izlenen metrik %q — atlanıyor", m)
			continue
		}
		out = append(out, m)
	}
	return out
}

// metricPolicy makes detection metric-aware: which DIRECTION of deviation
// matters, and the relative-change floor that filters statistically-
// significant-but-tiny moves. A symmetric |z| would open a "critical"
// anomaly on a 3σ p99 DROP (good news), and one absolute magnitude floor
// can't fit error_rate(%), p99(ms) and rps at once.
type metricPolicy struct {
	direction string  // "up" | "down" | "both" — which side opens an anomaly
	floorPct  float64 // relative-change floor: |current-median|/max(|median|,eps)
	// absFloor — mutlak-değer tabanı (v0.9.180). Yükseliş yönlü bir anomali,
	// current metrik bu değerin ALTINDAysa AÇILMAZ — göreli floor'un
	// yakalayamadığı "0 tabandan minik sıçrama" boşluğunu kapatır. error_rate=1
	// (%): yüksek hacimde birkaç mutlak hata (rate ~%0), küçük-MAD baseline'da
	// yüksek z üretse bile Problem'e dönmez (operatör: "milyonlarca istekte
	// 1-2-3 hata problem olmasın"). 0 = mutlak taban yok. Düşüşleri etkilemez.
	absFloor float64
	// minAbsDelta — mutlak FARK tabanı (v0.9.826). |current-median| bunun
	// altındaysa açılmaz. floorPct'in kör noktası: küçük bir medyanda %10
	// göreli değişim mutlak olarak hiçbir şey ifade etmeyebilir.
	minAbsDelta float64
	// minMAD — MAD'in alt sınırı, MAX olarak uygulanır (v0.9.826).
	// checkOne'da kullanılır (z hesabından ÖNCE), decideAnomaly'de değil.
	minMAD float64
	// minBaselineRate — hacim kapısı (istek/sn). checkOne'da uygulanır;
	// yalnız AÇILMAYA, çözülmeye değil.
	minBaselineRate float64
}

// metricDirections — metriğin HANGİ yönünün olay sayıldığı. Bu, ayara
// bağlanmadı ve bu bilinçli: yön bir eşik değil, metriğin ANLAMI.
// "p99 düşüşü de anomali olsun" diyen bir vida, iyi haberi sayfaya
// çevirirdi (v0.9.180'in kapattığı sınıf). Vidalar NE KADAR sorusunu
// ayarlar, NE sorusunu değil.
var metricDirections = map[string]string{
	"error_rate":   "up",   // rising errors only
	"p99_ms":       "up",   // only rising latency matters
	"request_rate": "both", // drop AND spike both matter
}

// directionFor — metriğin yönü; bilinmeyen metrik simetrik davranır.
func directionFor(metric string) string {
	if d, ok := metricDirections[metric]; ok {
		return d
	}
	return "both"
}

// policyFor — bir metriğin ÇÖZÜLMÜŞ politikası: sabit yön + operatörün
// ayarladığı eşikler (v0.9.826, system_settings "anomaly_sensitivity").
//
// Eşikler artık kodda sabit DEĞİL çünkü altı kez kodda değiştiler ve her
// seferinde operatör bir çentik ötede aynı duvara çarptı — "ne kadar
// sapma olay sayılır" filoya bağlı bir sayı. Gerekçenin tamamı
// chstore/anomaly_sensitivity.go'da.
func policyFor(metric string, cfg chstore.AnomalySensitivityConfig) metricPolicy {
	s := cfg.For(metric)
	return metricPolicy{
		direction:       directionFor(metric),
		floorPct:        s.FloorPct,
		absFloor:        s.AbsFloor,
		minAbsDelta:     s.MinAbsDelta,
		minMAD:          s.MinMAD,
		minBaselineRate: s.MinBaselineRate,
	}
}

// flatMADFloor — düz (MAD≈0) baseline'da modified z-score'u tanımlı
// kılan metrik-farkındalı taban (v0.9.48). Birimler farklı olduğundan
// tek mutlak taban üçüne birden uymaz:
//
//	error_rate  : 0.5 yüzde puanı — %0 tabanlı serviste sürekli %3
//	  hata ~4σ (warning), %30 hata ~40σ (critical); %1'lik kıpırtı
//	  ~1.3σ ile sessiz.
//	p99_ms      : max(1ms, medyanın %5'i) — sabit-2ms cache'li op
//	  10ms'e çıkınca ~5σ; 500ms'lik sabit servis 600ms'de sessiz.
//	request_rate: max(0.1 rps, medyanın %5'i) — sıfır-trafik servis
//	  aniden trafik alınca açılır (direction both).
func flatMADFloor(metric string, median float64) float64 {
	switch metric {
	case "error_rate":
		return 0.5
	case "p99_ms":
		return math.Max(1, 0.05*math.Abs(median))
	case "request_rate":
		return math.Max(0.1, 0.05*math.Abs(median))
	default:
		return math.Max(1e-3, 0.05*math.Abs(median))
	}
}

// effectiveMAD — z hesabında KULLANILACAK MAD. İki taban üst üste:
//
//  1. flatMADFloor (v0.9.48) — YALNIZ mad≈0 iken. Tarihi hiç kıpırdamamış
//     bir seride z tanımsızdı ve dedektör o servisi HİÇ değerlendirmiyordu;
//     en temiz servisler en görünmez olanlardı.
//  2. minMAD (v0.9.826) — HER ZAMAN, MAX olarak. 1'in kör noktası:
//     "neredeyse hiç kıpırdamamış" seri korumasız kalıyordu ve gerçek
//     false-pozitifler tam o kümede.
//
// OPERATÖRÜN VAKASI: medyan 1.90ms, mad 0.657, current 9.69ms.
//
//	minMAD kapalı → z = 0.6745 × 7.79 / 0.657 = 8.0σ → critical → AÇILIR
//	minMAD 1.0ms  → z = 0.6745 × 7.79 / 1.0   = 5.25σ → criticalZ 6.0
//	                kapısının altında → AÇILMAZ
//
// 8 ms'lik bir sıçrama hiçbir kullanıcının fark etmediği bir şey; sorun
// sapmanın istatistiksel büyüklüğü değil, BİRİMİNİN önemsizliğiydi.
//
// Saf — checkOne'dan ayrıldı ki bu zincir canlı CH olmadan tablo-testli
// olsun (fırtına vakası pinlenmiş: sensitivity_test.go).
func effectiveMAD(metric string, median, mad, minMAD float64) float64 {
	if mad < 1e-9 {
		mad = flatMADFloor(metric, median)
	}
	if minMAD > 0 {
		mad = math.Max(mad, minMAD)
	}
	return mad
}

// anomalyComparator — anomali YÖNÜNÜ öncelik hesabının anladığı ihlal
// comparator'ına çevirir (v0.9.978). SAF + tablo testli.
//
// Kaynak alan anomalyDecision.direction ve yalnız iki değer taşıyor:
// "dropped" (z < 0) ve "spiked". Uydurma yok — decideAnomaly'nin
// döndürdüğü dizgiler.
//
//	dropped → "<"  değer düştükçe kötüleşiyor; chstore.computePriority
//	               oranı ters çevirir (0.001× baseline = 1000× sapma)
//	spiked  → ">"  değer yükseldikçe kötüleşiyor; oran zaten >1, çevirme
//	               YOK. Boş bırakmak da aynı hesabı verirdi ama satır o
//	               zaman "yön bilinmiyor" derdi; burada BİLİNİYOR ve
//	               kaydı tutmak /problems'ta da doğru cümleyi kurdurur.
//
// totalLoss kolu (Value==0 && Threshold>0 → P1) bundan BAĞIMSIZ: trafiği
// tamamen kesilen servis comparator ne olursa olsun P1 kalır (v0.9.825).
func anomalyComparator(direction string) string {
	if direction == "dropped" {
		return "<"
	}
	return ">"
}

// anomalyDecision is the pure open/severity/direction verdict for one sample.
type anomalyDecision struct {
	open      bool
	severity  string // "warning" | "critical" (meaningful when open)
	direction string // "spiked" | "dropped"
}

// decideAnomaly applies the metric's directional gate + relative-change floor
// + direction-aware severity to a single (z, current, median) sample. Pure +
// store-free so the policy is unit-testable without a Detector. A 3σ p99 DROP
// returns open=false ("up" only); a request_rate DROP escalates to critical
// (traffic loss is worse than a spike).
//
// v0.9.826 — politika ve criticalZ ARTIK PARAMETRE. Eskiden ikisi de
// paket sabitlerinden okunuyordu; operatörün vidası bu fonksiyona
// ulaşamadan kalırdı. Saf kalmaya devam ediyor: girdi genişledi, bağımlılık
// eklenmedi.
func decideAnomaly(metric string, z, current, median float64, pol metricPolicy, criticalZ float64) anomalyDecision {
	dirOpen := false
	switch pol.direction {
	case "up":
		dirOpen = z >= openZ
	case "down":
		dirOpen = z <= -openZ
	default: // "both"
		dirOpen = math.Abs(z) >= openZ
	}
	absDelta := math.Abs(current - median)
	relChange := absDelta / math.Max(math.Abs(median), magnitudeEps)
	if !dirOpen || relChange < pol.floorPct {
		return anomalyDecision{}
	}
	// Mutlak FARK tabanı (v0.9.826): göreli floor'un kör noktası. Küçük bir
	// medyanda %10 göreli değişim mutlak olarak hiçbir şey ifade etmeyebilir
	// (1.9ms → 2.1ms yüzde olarak eşiği geçer ama kimse uyanmaz). Yöne
	// bakmadan uygulanır: küçüklük, sapmanın hangi tarafa olduğundan bağımsız.
	if pol.minAbsDelta > 0 && absDelta < pol.minAbsDelta {
		return anomalyDecision{}
	}
	// Mutlak-değer tabanı (v0.9.180): yükseliş yönlü bir anomali current bu
	// tabanın ALTINDAysa açılmaz — yüksek hacimde birkaç hata (error_rate ~%0)
	// küçük-MAD baseline'da yüksek z üretse bile Problem yaratmaz. Yalnız SPIKE
	// (z>0) tarafına uygulanır; düşüşler (request_rate drop) etkilenmez.
	if z > 0 && pol.absFloor > 0 && current < pol.absFloor {
		return anomalyDecision{}
	}
	dropped := z < 0
	dir := "spiked"
	if dropped {
		dir = "dropped"
	}
	severity := "warning"
	if math.Abs(z) >= criticalZ {
		severity = "critical"
	}
	if metric == "request_rate" && dropped {
		severity = "critical" // traffic loss is more serious than a spike
	}
	// v0.9.193 — P1-only gate (operatör: prod'da P2/P3 anomali gürültüsü).
	// warning-grade verdict Problem açmaz; dwell sayacına da girmez —
	// yalnız critical-sınıfı sapmalar (≥criticalZ, ya da trafik kaybı)
	// anomali üretir. resolve bandı değişmedi.
	if severity != "critical" {
		return anomalyDecision{}
	}
	return anomalyDecision{open: true, severity: severity, direction: dir}
}

// resolvedFor reports whether the metric has returned inside the resolve band
// for its policy direction (the directional counterpart of |z| <= resolveZ).
//
// YÖNE bağlı, EŞİKLERE değil — bu yüzden ayardan etkilenmiyor ve
// etkilenmemeli: hassasiyet vidaları AÇILMAYI ayarlar. Çözülmeyi de
// kısmak, operatörün "daha az gürültü" isteğini "problemler ekranda
// takılı kalsın"a çevirirdi.
func resolvedFor(metric string, z float64) bool {
	switch directionFor(metric) {
	case "up":
		return z <= resolveZ
	case "down":
		return z >= -resolveZ
	default:
		return math.Abs(z) <= resolveZ
	}
}

// evalWindow scores every bucket in the dwell window against the baseline's
// median/MAD. It reports allOpen (every bucket fires AND in the SAME
// direction — so a flapping spike→drop doesn't open), allResolved (every
// bucket back inside the resolve band), and cur (the most-recent bucket's
// verdict, which drives the reported severity/direction). Pure + store-free
// so the dwell/M-of-N policy is unit-testable, and stateless so a leader
// handoff loses no in-memory streak counter.
func evalWindow(metric string, median, mad float64, window []float64, pol metricPolicy, criticalZ float64) (allOpen, allResolved bool, cur anomalyDecision) {
	if len(window) == 0 {
		return false, false, anomalyDecision{}
	}
	allOpen, allResolved = true, true
	dir := ""
	for i, v := range window {
		zv := madScale * (v - median) / mad
		dv := decideAnomaly(metric, zv, v, median, pol, criticalZ)
		cur = dv
		if i == 0 {
			dir = dv.direction
		}
		if !dv.open || dv.direction != dir {
			allOpen = false
		}
		if !resolvedFor(metric, zv) {
			allResolved = false
		}
	}
	return allOpen, allResolved, cur
}

// anomalyAction is the pure open/resolve/none decision for one (service, metric)
// check: open/refresh when ALL dwell buckets fire (allOpen), else RESOLVE an
// already-open problem the moment the MOST-RECENT bucket is back inside the band
// (resolvedFor(metric, latestZ)). Extracted from checkOne so the v0.8.220
// asymmetry (hard open / fast resolve) is unit-tested — a silent revert to the
// old all-dwell-buckets `allResolved` resolve condition changes this function
// and fails TestAnomalyAction. Returns "open" | "resolve" | "none".
func anomalyAction(hasOpen, allOpen bool, metric string, latestZ float64) string {
	switch {
	case allOpen:
		return "open"
	case hasOpen && resolvedFor(metric, latestZ):
		return "resolve"
	default:
		return "none"
	}
}

type Detector struct {
	store    *chstore.Store
	interval time.Duration
	lock     cache.Lock
	leader   *cache.LeaderHolder // v0.5.429 — long-lived leader designation
	notifier *notify.Notifier
	// lastTracked — en son loglanan izlenen-metrik seti (v0.9.800).
	// Set DEĞİŞTİĞİNDE bir satır logluyoruz, her tikte değil: operatör
	// vidayı çevirdiğinde etkiyi logta görsün, ama 2 dakikada bir aynı
	// satır tekrarlanmasın. YALNIZ scan()'den, yani tek goroutine'den
	// (leader tik döngüsü) yazılır.
	lastTracked string
	// lastSensitivity — en son loglanan hassasiyet özeti (v0.9.826).
	// lastTracked ile aynı sözleşme: DEĞİŞİNCE bir satır, her tikte
	// değil. Operatör vidayı çevirdiğinde etkinin gerçekten canlıya
	// indiğini logtan doğrulayabilmeli — çok-pod kurulumlarda "PUT hangi
	// pod'a düştü, dedektör gördü mü" sorusunun tek cevabı bu satır.
	lastSensitivity string
}

// sensitivityLogLine — hassasiyet ayarının tek satırlık, KARARLI özeti.
// Kararlı olmak zorunda: map üzerinde gezmek sıra garantisi vermez ve
// aynı ayar her tikte farklı görünüp log seli üretirdi.
func sensitivityLogLine(c chstore.AnomalySensitivityConfig) string {
	var b strings.Builder
	fmt.Fprintf(&b, "dwell=%d criticalZ=%.1f incident=%v", c.DwellBuckets, c.CriticalZ, c.AttachesToIncident())
	for _, m := range chstore.AnomalySensitivityMetrics {
		s := c.For(m)
		fmt.Fprintf(&b, " | %s floorPct=%.2f absFloor=%.2f minAbsDelta=%.2f minMAD=%.2f minRate=%.2f",
			m, s.FloorPct, s.AbsFloor, s.MinAbsDelta, s.MinMAD, s.MinBaselineRate)
	}
	// v0.9.936 — davranış motoru AYNI satırda. Ayrı bir log satırı
	// ikinci bir "değişti mi" takibi demek olurdu; operatörün sorusu
	// tek: "vidalarım canlıya indi mi?".
	b.WriteString(behaviorLogLine(c.Behavior))
	return b.String()
}

// New takes a cache.Lock so multiple replicas don't all open the same
// anomaly, and a notifier so PROBLEM OPENED transitions email/slack out.
func New(store *chstore.Store, interval time.Duration, lock cache.Lock, notifier *notify.Notifier) *Detector {
	if interval == 0 {
		interval = 2 * time.Minute
	}
	return &Detector{
		store: store, interval: interval,
		lock:     lock,
		leader:   cache.NewLeaderHolder(lock, lockKey, cache.LeaderTTL(interval)),
		notifier: notifier,
	}
}

func (d *Detector) Start(ctx context.Context) {
	d.leader.Start(ctx)
	// v0.9.800 — izlenen metrik setini boot'ta bir kez CH'den hidrate
	// et. API sunucusu da aynı blob'u hidrate edip 30 sn'de bir
	// yeniliyor, ama dedektör main.go'da ondan ÖNCE başlıyor ve ilk
	// taramayı hemen aşağıda yapıyor: bu satır olmasa operatörün
	// kaydettiği set ilk tikte görülmezdi.
	d.store.LoadAnomalyTracked(ctx)
	// v0.9.826 — hassasiyet eşikleri de boot'ta hidrate edilir, AYNI
	// gerekçeyle: dedektör main.go'da API sunucusundan ÖNCE başlıyor ve
	// ilk taramayı hemen aşağıda yapıyor. Bu satır olmasa operatörün
	// kaydettiği eşikler ilk tikte görülmez, yani vidalanmış bir kurulum
	// her restart'ta bir tur varsayılanlarla tarardı.
	d.store.LoadAnomalySensitivity(ctx)
	t := time.NewTicker(d.interval)
	defer t.Stop()
	d.runIfLeader(ctx) // run once immediately
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.runIfLeader(ctx)
		}
	}
}

func (d *Detector) runIfLeader(ctx context.Context) {
	if !d.leader.IsLeader() {
		return
	}
	d.scan(ctx)
}

func (d *Detector) scan(ctx context.Context) {
	// v0.8.506: yalnız isim listesi gerekiyor — MV'den (bkz.
	// ListActiveServiceNames); ham spans 24h taraması kalktı.
	services, err := d.store.ListActiveServiceNames(ctx, 24*time.Hour)
	if err != nil {
		log.Printf("[anomaly] list services: %v", err)
		return
	}
	// Read the seasonal-baseline knobs fresh each sweep (same
	// LoadPersisted-per-tick pattern the evaluator uses for the
	// promotion config) so an operator tune takes effect on the next
	// scan without a redeploy. They ride the existing anomaly_promotion
	// blob — one anomaly settings surface, not two. v0.8.250.
	days, minSamples, neighbor := seasonalParams(d.store.GetAnomalyPromotion(ctx))

	// v0.9.800 — izlenen metrik seti (operatör ayarı). Kapalı bir metrik
	// bu listeden hiç çıkmaz, dolayısıyla ne toplu okuması açılır ne de
	// checkOne'ı koşar.
	tracked := d.trackedMetricsNow()
	if joined := strings.Join(tracked, ","); joined != d.lastTracked {
		log.Printf("[anomaly] izlenen metrikler: %s", joined)
		d.lastTracked = joined
	}

	// v0.9.826 — hassasiyet eşikleri (operatör ayarı). Atomic load, CH
	// okuması DEĞİL: blob boot'ta hidrate edilir, admin PUT'unda takas
	// edilir, çok-pod kurulumlarda 30 sn'de yakınsar. Tik BAŞINA BİR KEZ
	// okunuyor ve tik boyunca sabit kalıyor — checkOne içinde okusaydık
	// aynı taramanın ilk ve son servisi farklı eşiklerle değerlendirilebilir,
	// yani tarama kendi içinde tutarsız olurdu (aynı gerekçe `now` için de
	// geçerli, v0.8.507).
	sens := d.store.AnomalySensitivity()
	if line := sensitivityLogLine(sens); line != d.lastSensitivity {
		log.Printf("[anomaly] hassasiyet: %s", line)
		d.lastSensitivity = line
	}

	// v0.8.507 — batch the per-(service,metric) MV reads into ONE
	// GROUP BY service_name pass PER metric (×2: consecutive + seasonal),
	// replacing the old services × tracked × 2 per-service queries.
	// At prod scale that loop was ~1400 svc × 3 metrics × 2 reads ≈ 8400
	// queries / 2-min tick, each re-reading ~the whole window's granules
	// (query_log: 46-65K read_rows apiece, ~708M rows/hr re-scanning the
	// same window). The batched form reads those rows ONCE — 2 queries per
	// TRACKED metric / tick — then distributes the per-service series to
	// checkOne. Same pattern the evaluator adopted in v0.8.352. One `now`
	// for the whole tick keeps the window (and the seasonal slot)
	// consistent across every service, instead of the old per-checkOne
	// time.Now() drift.
	now := time.Now()
	fetchSeasonal := func(m string) (map[string][]float64, error) {
		return d.fetchAllSeasonal(ctx, m, now, days, neighbor)
	}
	if !seasonalBaseline {
		fetchSeasonal = nil
	}
	bucketsByMetric, seasonalByMetric, ratesByMetric := batchSeries(tracked,
		func(m string) (map[string][]float64, map[string][]float64, error) {
			return d.fetchAllBuckets(ctx, m, now)
		}, fetchSeasonal)

	// v0.9.691 (perf taraması #2) — AÇIK PROBLEMLER TEK SORGUDA.
	//
	// checkOne her (servis, metrik) çifti için `FindOpenProblem` çağırıyordu:
	// `problems FINAL` üzerinde NOKTA SORGUSU, ve `problems` sıralama
	// anahtarı `id` olduğu için rule_id/service ile budama YOK — her çağrı
	// tam tarama.
	//
	// ÖLÇÜLDÜ (chc-0 query_log, 6 saat): 47.442 çağrı · 42 GiB · SELECT
	// baytının %4.5. A/B: 297 nokta sorgusu 1.725 ms, tek snapshot 5.2 ms
	// → 331×. `satır/çağrı` her iki tarafta 3.775 (EXPLAIN: Parts 9/9,
	// Granules 9/9 — budama sıfır, teyitli).
	//
	// Toplu okuma ZATEN YAZILMIŞ (OpenProblemsSnapshot, dört çağıranı var);
	// bu dedektör bağlanmamıştı.
	//
	// HATA DAVRANIŞI KORUNUYOR: eski kod `open, _ :=` ile hatayı yutup
	// "açık problem yok" sayıyordu; ByKey nil-alıcı güvenli, yani snapshot
	// hatasında aynı yola düşülüyor.
	snap, err := d.store.OpenProblemsSnapshot(ctx)
	if err != nil {
		log.Printf("[anomaly] open-problems snapshot: %v — bu tik açık problem YOK sayılıyor", err)
	}

	for _, svc := range services {
		for _, m := range tracked {
			buckets := seriesFor(bucketsByMetric[m], svc)
			seasonal := seriesFor(seasonalByMetric[m], svc)
			rates := seriesFor(ratesByMetric[m], svc)
			d.checkOne(ctx, svc, m, buckets, seasonal, rates, minSamples, snap, sens)
		}
		// v0.9.1051 (Faz 0.3) — servis-sustu kontrolü, servis başına BİR
		// kez. Hacim serisi metrikten bağımsız (istek sayısı); ilk izlenen
		// metriğin toplu okumasından gelir, ek sorgu yok. tracked boşsa
		// (operatör her şeyi kapattıysa) bu kontrol de kapalı — bilinçli:
		// izleme tamamen kapatılmışken arka kapıdan problem açmayız.
		if len(tracked) > 0 {
			if rates := seriesFor(ratesByMetric[tracked[0]], svc); len(rates) > 0 {
				d.checkSilence(ctx, svc, rates, snap, sens)
			}
		}
	}

	// v0.9.936 — DAVRANIŞ MOTORU, aynı tikin sonunda.
	//
	// SONDA olması bilinçli: ani-sapma dedektörü operatörün bugün
	// güvendiği hat ve onun gecikmesi/başarısı bu motorun ek iki
	// sorgusuna bağlanmamalı. scanBehavior kendi içinde soft-fail —
	// panik/hata buraya sızmaz, tarama tamamlanmış sayılır.
	//
	// AYNI `now`: kova hesabı ve pencere sınırı yukarıdaki okumalarla
	// hizalı kalsın (v0.8.507 tik-tutarlılığı).
	d.scanBehavior(ctx, now, sens)
}

// batchSeries — bir tikin TOPLU OKUMALARINI toplar: izlenen her metrik
// için bir consecutive, bir de (seasonal açıksa) same-slot okuması.
// Okuyucular parametre olarak geliyor ki "kapalı bir metrik için hiç
// sorgu açılmadığı" testlenebilsin (v0.9.800 — casus fonksiyonlarla
// pinlenmiş); scan() gerçek fetchAll* metotlarını veriyor.
//
// Hata davranışı v0.8.507'den aynen korunuyor:
//   - consecutive okuma hatası → metrik bu tikte TAMAMEN atlanır (her
//     servisin serisi aşağıda yok → checkOne enoughHistory'de eler);
//   - seasonal okuma hatası → best-effort, metrik seasonal haritasında
//     yok kalır → chooseBaseline consecutive pencereye düşer.
//
// fetchSeasonal nil ise seasonal baseline kapalı demektir.
func batchSeries(
	tracked []string,
	fetchBuckets func(metric string) (values, rates map[string][]float64, err error),
	fetchSeasonal func(metric string) (map[string][]float64, error),
) (buckets, seasonal, rates map[string]map[string][]float64) {
	buckets = make(map[string]map[string][]float64, len(tracked))
	seasonal = make(map[string]map[string][]float64, len(tracked))
	rates = make(map[string]map[string][]float64, len(tracked))
	for _, m := range tracked {
		all, rt, err := fetchBuckets(m)
		if err != nil {
			log.Printf("[anomaly] batch buckets %s: %v", m, err)
			continue
		}
		buckets[m] = all
		// v0.9.826 — hacim serisi AYNI okumadan geliyor (ek sorgu yok),
		// dolayısıyla ayrı bir hata dalı da yok.
		rates[m] = rt
		if fetchSeasonal == nil {
			continue
		}
		s, err := fetchSeasonal(m)
		if err != nil {
			log.Printf("[anomaly] batch seasonal %s: %v", m, err)
			continue
		}
		seasonal[m] = s
	}
	return buckets, seasonal, rates
}

// checkOne runs the anomaly verdict for one (service, metric) from PRE-BATCHED
// series (v0.8.507): buckets = the consecutive 5-min series, seasonal = the
// same-slot history — both handed in by scan()'s per-metric batch reads rather
// than fetched here per service. A nil/short `buckets` (service absent from the
// batch, or the metric's batch read errored this tick) is skipped by the
// enoughHistory guard — identical to the old per-service fetch returning empty.
func (d *Detector) checkOne(ctx context.Context, service, metric string, buckets, seasonal, rates []float64, seasonalMinSamples int, openSnap *chstore.OpenProblems, cfg chstore.AnomalySensitivityConfig) {
	// v0.9.1068 (F1.6-R1) — KARAR fazı saf evaluateAnomaly'de
	// (verdict.go); bu gövde yalnız YAN ETKİLERİ uygular. Davranış
	// birebir — karar mantığı taşınırken satır satır korundu.
	ruleID := "anomaly:" + service + ":" + metric
	// v0.9.691 — tik başına TEK snapshot'tan arama (bkz. scan()).
	open := openSnap.ByKey(ruleID, service)
	hasOpen := open != nil && open.ID != ""

	oc := evaluateAnomaly(metric, buckets, seasonal, rates, seasonalMinSamples, hasOpen, cfg)
	if oc.Action == "skip" || oc.Action == "none" {
		return
	}
	current, median, mad, z, dwell := oc.Current, oc.Median, oc.MAD, oc.Z, oc.Dwell
	action := oc.Action
	if action == "open" {
		severity := oc.Severity
		desc := fmt.Sprintf("%s %s on %s — current %.2f%s vs baseline %.2f%s (%.1fσ, sustained %d buckets).",
			displayMetric(metric), oc.Direction, service, current, unitOf(metric), median, unitOf(metric), z, dwell)
		if hasOpen {
			open.Value = current
			// v0.9.978 — yön her tazelemede YENİDEN yazılır. evalWindow
			// pencerenin TAMAMI aynı yönde olduğunda açık tutuyor, yani
			// yön gerçekten değişebilir (uzun süre açık kalan bir problem
			// sıçramadan çöküşe geçebilir). Eski satırların yönü de bu
			// dalda dolduğu için düzeltme geçmişe de iniyor.
			open.Comparator = anomalyComparator(oc.Direction)
			open.Description = desc
			if err := d.store.UpsertProblem(ctx, *open); err != nil {
				log.Printf("[anomaly] refresh %s: %v", ruleID, err)
			}
			return
		}
		p := chstore.Problem{
			ID:          newID(),
			RuleID:      ruleID,
			RuleName:    fmt.Sprintf("Anomaly · %s", displayMetric(metric)),
			Severity:    severity,
			Service:     service,
			Metric:      metric,
			Value:       current,
			Threshold:   median,
			// v0.9.978 (operatör kararı) — anomali satırında Threshold bir
			// İHLAL EŞİĞİ değil, BASELINE MEDYANI. Yani value/threshold
			// "baseline'ın kaç katı" demek ve DÜŞÜŞ yönlü bir olayda doğal
			// olarak 1'in altında kalır (trafik baseline'ın binde birine
			// inince oran 0.001). Yönü satıra yazmak bu aileyi öncelik
			// hesabında doğru tarafa koyuyor: '<' ile oran ters çevrilir,
			// trafik çöküşü P1 kalır (v0.9.976 ters-çevirmeyi comparator'a
			// bağladığında bu aile yanlışlıkla P2'ye düşmüştü).
			Comparator:  anomalyComparator(oc.Direction),
			Status:      "open",
			Description: desc,
			StartedAt:   time.Now().UnixNano(),
		}
		if err := d.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[anomaly] open %s: %v", ruleID, err)
			return
		}
		log.Printf("[anomaly] OPENED %s · %s = %.2f%s (med=%.2f mad=%.2f z=%.1f)",
			service, metric, current, unitOf(metric), median, mad, z)
		// Auto-attach to the active incident for this service+severity
		// (same convention as evaluator-opened problems).
		//
		// v0.9.827 — ARTIK KAPATILABİLİR. Bu çağrı bugüne kadar KOŞULSUZDU
		// ve Settings'teki hiçbir vida ona ulaşmıyordu: "Promote strong
		// anomalies" kutucuğu BAŞKA bir hattı yönetiyor (evaluator.
		// promoteStrongAnomalies ← recorder'ın log/trace AnomalyEvent'leri).
		// Operatör o kutucuğu kapatıp metrik dedektörünün incident açmaya
		// devam ettiğini görüyordu — ayar sayfası yalan söylüyordu.
		//
		// KAPALIYKEN PROBLEM YİNE AÇILIR ve bildirim YİNE gider; yalnız
		// incident açılmaz/bağlanmaz. Ayrım bilinçli: bu vida "bana haber
		// verme" değil, "bunu OLAY YÖNETİMİNE sokma" demek.
		if cfg.AttachesToIncident() {
			if _, err := d.store.AttachProblemToIncident(ctx, p); err != nil {
				log.Printf("[anomaly] incident attach: %v", err)
			}
		}
		if d.notifier != nil {
			go d.notifier.SendProblemAlert(context.Background(), p)
		}
	} else if action == "resolve" {
		// v0.8.220 — FAST resolve (anomalyAction): the most-recent bucket is back
		// inside the band, so clear a recovered problem immediately (incl. the
		// "transient spike that opened then never resolved" report) instead of
		// waiting for ALL dwell buckets to align — which left problems stuck open
		// on gradual recovery / silent sources. The 3-bucket open dwell still
		// prevents re-open flapping.
		// v0.9.977 — Value (anomali anındaki değer) KORUNUR; toparlanmış
		// değeri yazmak "hiç sapmamış" gibi bir satır bırakıyordu.
		//
		// v0.9.1051 (Faz 0.3) — GEREKÇE DÜRÜSTLÜĞÜ: susan serviste son
		// bucket padlenmiş SIFIRDIR (padTrailingSilence) ve z bandın içine
		// "veri olmadığı için" düşer — bu toparlanma değil, sinyal kaybı.
		// Problem yine kapanır (v0.9.449 donmuş-kuyruk kararı: hacim
		// kapısı çözülmeye uygulanmaz), ama "recovered" DEĞİL "source
		// silent" gerekçesiyle; asıl kayıp aynı tikte checkSilence'ın
		// açtığı critical service_silent problemi olarak görünür kalır.
		latestHasData := oc.LatestHasData
		reason := "recovered"
		if !latestHasData {
			reason = "source silent"
			open.Description = strings.TrimRight(open.Description, " ") +
				" Resolved on signal loss, not recovery — the service stopped emitting spans."
		}
		chstore.MarkResolved(open, time.Now().UnixNano())
		if err := d.store.UpsertProblem(ctx, *open); err != nil {
			log.Printf("[anomaly] resolve %s: %v", ruleID, err)
			return
		}
		log.Printf("[anomaly] RESOLVED %s · %s (%s, z=%.1f)", service, metric, reason, z)
	}
}

// ─── service_silent dedektörü (v0.9.1051, Faz 0.3) ─────────────────────
//
// "Servis tamamen sustu" en pahalı arıza sınıfıydı ve varsayılan
// kurulumda TAMAMEN KÖRDÜ: request_rate izlemesi varsayılan kapalı,
// gömülü kuralların hiçbiri rate değil; üstelik susan servisin açık
// p99/error anomalisi resolve bandına "veri yokluğundan" girip
// kapanıyordu — servis ölünce ekran temizleniyordu.
//
// Kapı bilinçli MUHAFAZAKÂR: baseline'da İSTİKRARLI trafik şartı
// (aktif-kova payı ≥ %90) batch/cron servislerini dışarıda tutar —
// onların baseline'ı zaten sıfırlarla dolu, 15 dakikalık sessizlik
// olağan. comparator '<' + value 0 sayesinde P1 "tamamen kayıp" kapısı
// (problem_priority) doğal tetiklenir.

const (
	// silentTrailingBuckets — kaç ardışık SIFIR kovası "sustu" sayılır
	// (3 × 5dk = 15dk). Tek kova scrape/ingest gecikmesi olabilir.
	silentTrailingBuckets = 3
	// silentMinActiveShare — baseline kovalarının en az bu payı trafik
	// taşımalı ki "istikrarlı akış kesildi" iddiası dürüst olsun.
	silentMinActiveShare = 0.9
)

// trimTrailingSilent — baseline diliminin KUYRUĞUNDAKİ veri-yok
// (rate==0) koşusunu kırpar (v0.9.1052, Q3 — padTrailingSilence
// sıfırları baseline'a taşmasın). İçerideki sıfırlara DOKUNMAZ:
// padTrailingSilence yalnız kuyruğu doldurur, içerideki kovalar gerçek
// veridir. values ile rates aynı seriden geldiği için hizalıdır; yine
// de savunmacı min-uzunlukla kesilir.
func trimTrailingSilent(values, rates []float64) []float64 {
	n := len(values)
	if len(rates) < n {
		n = len(rates)
	}
	for n > 0 && rates[n-1] == 0 {
		n--
	}
	return values[:n]
}

// silenceVerdict — saf, tablo-testli. rates = 5dk hacim serisi
// (padTrailingSilence'tan geçmiş, yani eksik kuyruk kovaları sıfır).
// silent=true: son `trailing` kova tamamen sıfır VE baseline istikrarlı
// trafik taşıyor. baselineRate = baseline medyanı (problem satırının
// Threshold'u).
func silenceVerdict(rates []float64, trailing int, minActiveShare float64) (silent bool, baselineRate float64) {
	if trailing <= 0 || len(rates) < trailing+minSamples {
		return false, 0
	}
	split := len(rates) - trailing
	for _, v := range rates[split:] {
		if v > 0 {
			return false, 0
		}
	}
	base := rates[:split]
	active := 0
	for _, v := range base {
		if v > 0 {
			active++
		}
	}
	if float64(active) < minActiveShare*float64(len(base)) {
		return false, 0
	}
	med, _ := medianMAD(base)
	if med <= 0 {
		return false, 0
	}
	return true, med
}

// checkSilence — scan()'in servis başına BİR kez çağırdığı taraf-etkili
// yarı: silenceVerdict'e göre critical service_silent problemi açar/
// tazeler, trafik dönünce hızlı-çözer. Ek CH okuması SIFIR — rates
// serisi zaten tikin toplu okumasından geliyor.
func (d *Detector) checkSilence(ctx context.Context, service string, rates []float64, openSnap *chstore.OpenProblems, cfg chstore.AnomalySensitivityConfig) {
	ruleID := "anomaly:" + service + ":service_silent"
	open := openSnap.ByKey(ruleID, service)
	hasOpen := open != nil && open.ID != ""

	silent, baseRate := silenceVerdict(rates, silentTrailingBuckets, silentMinActiveShare)
	if !silent {
		if hasOpen && len(rates) > 0 && rates[len(rates)-1] > 0 {
			chstore.MarkResolved(open, time.Now().UnixNano())
			if err := d.store.UpsertProblem(ctx, *open); err != nil {
				log.Printf("[anomaly] resolve %s: %v", ruleID, err)
				return
			}
			log.Printf("[anomaly] RESOLVED %s · service_silent (traffic returned)", service)
		}
		return
	}

	desc := fmt.Sprintf("Service went SILENT — no spans for ≥%dm; baseline ~%.2f req/s. Total signal loss (crash, network partition or collector wedge), not a metric deviation.",
		silentTrailingBuckets*5, baseRate)
	if hasOpen {
		open.Description = desc
		if err := d.store.UpsertProblem(ctx, *open); err != nil {
			log.Printf("[anomaly] refresh %s: %v", ruleID, err)
		}
		return
	}
	p := chstore.Problem{
		ID:       newID(),
		RuleID:   ruleID,
		RuleName: "Anomaly · Service silent",
		Severity: "critical",
		Service:  service,
		Metric:   "request_rate",
		Value:    0,
		// Threshold = baseline medyanı; '<' comparator'la value/threshold
		// oranı ters çevrilir → "tamamen kayıp" P1 kapısı doğal tetiklenir
		// (v0.9.978 anomali-satırı sözleşmesinin aynısı).
		Threshold:   baseRate,
		Comparator:  "<",
		Status:      "open",
		Description: desc,
		StartedAt:   time.Now().UnixNano(),
	}
	if err := d.store.UpsertProblem(ctx, p); err != nil {
		log.Printf("[anomaly] open %s: %v", ruleID, err)
		return
	}
	log.Printf("[anomaly] OPENED %s · service_silent (baseline %.2f req/s, %dm sessiz)",
		service, baseRate, silentTrailingBuckets*5)
	if cfg.AttachesToIncident() {
		if _, err := d.store.AttachProblemToIncident(ctx, p); err != nil {
			log.Printf("[anomaly] incident attach: %v", err)
		}
	}
	if d.notifier != nil {
		go d.notifier.SendProblemAlert(context.Background(), p)
	}
}

// metricValueExpr returns the service_summary_5m SELECT expression that
// derives one tracked metric from the MV's aggregate states. Shared by the
// consecutive (fetchBuckets) and seasonal (fetchSeasonalBaseline) reads so
// both baselines are computed identically.
func metricValueExpr(metric string) (string, error) {
	switch metric {
	case "error_rate":
		return "countMerge(error_count_state) / nullIf(countMerge(span_count_state), 0) * 100", nil
	case "request_rate":
		return "countMerge(span_count_state) / 300.0", nil
	case "p99_ms":
		return "quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state)[3] / 1e6", nil
	}
	return "", fmt.Errorf("unknown metric %q", metric)
}

// enoughHistory reports whether a fetched bucket series has enough samples to
// run the dwell-windowed check: minSamples baseline buckets PLUS a full dwell
// window. A service the batch query returned no (or too few) rows for is
// skipped here — the same guard the old per-service fetchBuckets + len check
// applied. Pure so the "empty/sparse service is skipped" contract is testable
// without a live ClickHouse. v0.8.507.
// v0.9.826 — dwell operatör ayarı olduğu için parametre; sabitken bir
// operatör dwell'i 12'ye çıkardığında pencere seriden UZUN olabilir ve
// buckets[split:] negatif indeksle panikleyebilirdi.
func enoughHistory(n, dwell int) bool { return n >= minSamples+dwell }

// hasEnoughVolume — son bucket'ın istek hızı hacim kapısını geçiyor mu?
// (v0.9.826)
//
// AÇIK GEÇER iki halde: kapı kapalıysa (min<=0) ve hacim SERİSİ YOKSA.
// İkincisi önemli — hacim okuması bir gün başarısız olursa ya da yeni bir
// çağıran seriyi geçirmeyi unutursa, dedektörün SESSİZCE kapanması
// (hiçbir anomali açmaması) mümkün olmamalı. Bu depoda sessiz kapanma
// tekrarlayan bir hata sınıfı; kapı ölçemediğinde ölçmeden geçirir.
func hasEnoughVolume(rates []float64, min float64) bool {
	if min <= 0 || len(rates) == 0 {
		return true
	}
	return rates[len(rates)-1] >= min
}

// seriesFor returns the batched series for a service, or nil when the metric's
// batch query returned no rows for it (new/sparse service, OR the batch read
// errored this tick and the whole metric map is absent). A nil series makes
// checkOne's enoughHistory guard skip the service — identical to the old
// per-service fetch returning an empty result set. Pure. v0.8.507.
func seriesFor(byService map[string][]float64, service string) []float64 {
	return byService[service]
}

// accumulateSeries folds one scanned (service, value) row into the per-service
// series map, preserving arrival order. The batch queries ORDER BY
// service_name, t so each service's slice comes out ascending in time — the
// same order the old per-service `ORDER BY t` produced, which the dwell window
// (buckets[len-dwellBuckets:]) and the current sample (buckets[len-1]) depend
// on. Pure so the batch distribution is unit-tested without a live CH. v0.8.507.
func accumulateSeries(byService map[string][]float64, service string, v float64) {
	byService[service] = append(byService[service], v)
}

// padTrailingSilence (v0.9.449, hacim denetimi L3 "donmuş kuyruk") —
// susan servisin serisini pencere sonuna kadar SIFIRLA doldurur.
// GROUP BY yalnız veri OLAN bucket'ları döndürür; seri sıkışınca son
// dolu bucket "şimdi" muamelesi görüyordu: saatler önceki spike güncel
// sanılıp anomali last_seen'i 24 saate dek taze tutuyor, (terfi
// zinciriyle) problemi canlı tutuyordu. Kuyruk sıfırları gerçek
// sessizliğin dürüst temsilidir: current=0 → z sönümlenir → event
// 10dk'da clear → v0.9.444 resolve pass'i problemi kapatır.
//
// YALNIZ kuyruk doldurulur — iç boşluklara dokunulmaz (baseline
// öğreniminin mevcut davranışını değiştirmemek için asgari cerrahi).
// Saf — tablo-testli.
func padTrailingSilence(series []float64, lastBucketUnix int64, upperUnix int64) []float64 {
	if len(series) == 0 || lastBucketUnix <= 0 {
		return series
	}
	const step = int64(300) // 5-dk MV grid'i
	// Var olması gereken bucket'lar: last+step, last+2·step, … < upper
	// (upper = ilk TAMAMLANMAMIŞ bucket'ın başı, exclusive).
	for t := lastBucketUnix + step; t < upperUnix; t += step {
		series = append(series, 0)
	}
	return series
}

// buildAllBucketsQuery is the batched twin of the old per-service fetchBuckets
// read: ONE `GROUP BY service_name, t` pass over service_summary_5m for a
// metric, instead of one `WHERE service_name = ?` query PER service. The metric
// SELECT expression (metricValueExpr) is byte-identical to the per-service read
// so every baseline value is computed the same way. Extracted pure so the SQL
// SHAPE — no service filter, GROUP BY service_name, time-bounded WHERE (for
// partition pruning + the v0.8.316 complete-buckets-only upper bound), a
// per-service + overall LIMIT safety cap, max_execution_time, MV (never raw
// spans) — is table-tested without a CH connection. Two `?` binds, in order:
// cutoff (historyHours ago), lastCompleteBucketStart(now).
//
// v0.5.296 — the reads that this replaces already moved OFF raw spans onto
// service_summary_5m (scale-audit critical). v0.8.507 collapses the remaining
// per-service N+1 fan-out into one pass: prod was ~1400 svc × 3 metrics × 2
// reads ≈ 8400 queries / 2-min tick, each re-scanning ~the whole window's
// granules; the batch reads those rows ONCE.
// v0.9.826 — HACİM AYNI SORGUDA. Kapı için gereken istek hızı, ayrı bir
// okuma AÇMADAN geliyor: countMerge(span_count_state) zaten bu MV'nin
// aynı granüllerinde ve error_rate/request_rate ifadeleri onu ZATEN
// merge ediyor. İkinci bir sorgu, tik başına metrik başına bir tam
// pencere taraması daha demek olurdu — v0.8.507'nin toparladığı maliyeti
// geri açardık. Ek kolon, taranan granülü değiştirmez.
func buildAllBucketsQuery(vexpr string) string {
	// v0.8.316 — complete buckets only (time_bucket < lastCompleteBucketStart):
	// the still-filling bucket made request_rate (÷ fixed 300s) read
	// ~elapsed/300 of the true rate, so a live spike looked baseline one minute
	// into each bucket and the fast-resolve closed the open anomaly mid-incident.
	//
	// `rate` bölmesi request_rate ifadesiyle BİREBİR aynı (÷300.0), yani
	// hacim kapısının birimi operatörün /metrics'te gördüğü istek/sn ile
	// aynı şey — ayar sayfasında "istek/sn" yazarken bu garanti gerekli.
	return fmt.Sprintf(`
		SELECT service_name, toUnixTimestamp(time_bucket) AS t, %s AS v,
		       countMerge(span_count_state) / 300.0 AS rate
		FROM service_summary_5m
		WHERE time_bucket >= ? AND time_bucket < ?
		GROUP BY service_name, t
		ORDER BY service_name, t
		LIMIT 1000 BY service_name
		LIMIT 20000000
		SETTINGS max_execution_time = 25`, vexpr)
}

// fetchAllBuckets runs buildAllBucketsQuery once for a metric and returns the
// per-service 5-minute series (ascending in time), keyed by service_name. A
// service absent from the map had no complete buckets in the window; checkOne's
// enoughHistory guard skips it. `now` is fixed by the caller for the whole tick
// so every service shares one window. v0.8.507.
func (d *Detector) fetchAllBuckets(ctx context.Context, metric string, now time.Time) (map[string][]float64, map[string][]float64, error) {
	vexpr, err := metricValueExpr(metric)
	if err != nil {
		return nil, nil, err
	}
	cutoff := now.Add(-time.Duration(historyHours) * time.Hour)
	upper := lastCompleteBucketStart(now)
	rows, err := d.store.TelemetryReadConn().Query(ctx, buildAllBucketsQuery(vexpr), cutoff, upper)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := make(map[string][]float64)
	rates := make(map[string][]float64)
	lastT := make(map[string]int64)
	for rows.Next() {
		var svc string
		var t uint32
		var v, rate float64
		if err := rows.Scan(&svc, &t, &v, &rate); err != nil {
			return nil, nil, err
		}
		accumulateSeries(out, svc, v)
		accumulateSeries(rates, svc, rate)
		lastT[svc] = int64(t) // ORDER BY service, t — son görülen kazanır
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	// v0.9.449 — donmuş kuyruk: susan servis pencere sonuna dek sıfırla
	// doldurulur ki son dolu bucket "şimdi" sanılmasın (padTrailingSilence).
	// Hacim serisi de AYNI dolguyu alır: iki seri hizada kalmalı, yoksa
	// "son bucket" ikisinde farklı zamanı gösterir. Sıfır hacim zaten
	// sessizliğin dürüst temsili ve kapı çözülmeye uygulanmıyor.
	for svc, series := range out {
		out[svc] = padTrailingSilence(series, lastT[svc], upper.Unix())
		rates[svc] = padTrailingSilence(rates[svc], lastT[svc], upper.Unix())
	}
	return out, rates, nil
}

// lastCompleteBucketStart is the exclusive upper bound for MV series reads
// (v0.8.316): the current bucket's START on the UTC 5-minute grid — the
// same grid toStartOfInterval uses — so `time_bucket < bound` keeps only
// buckets whose full 300s have elapsed. Pure + table-tested
// (complete_bucket_test.go).
func lastCompleteBucketStart(now time.Time) time.Time {
	return now.Truncate(5 * time.Minute)
}

// dayClass buckets a timestamp into the day-of-week traffic profile the
// seasonal baseline matches on: "saturday" / "sunday" / "weekday". Split into
// THREE classes (was a weekday/weekend binary) because a bank runs a distinct
// profile on Saturday vs Sunday — cmt ≠ paz — so blending them poisoned the
// baseline with the wrong day's shape. Pure so it's table-testable across all
// seven weekdays. Mirrored in SQL by the multiIf on toDayOfWeek below.
func dayClass(t time.Time) string {
	switch t.Weekday() {
	case time.Saturday:
		return "saturday"
	case time.Sunday:
		return "sunday"
	default:
		return "weekday"
	}
}

// slotSecondsOfDay returns t's seconds-since-midnight aligned DOWN to the
// 5-min bucket grid (0 … 86340). This is the CENTRE of the neighbour window
// the seasonal query matches; the MV's toHour*3600+toMinute*60 is the same
// grid, so the circular distance below is a whole number of buckets.
func slotSecondsOfDay(t time.Time) int {
	return t.Hour()*3600 + (t.Minute()/5)*5*60
}

// seasonalParams resolves the operator-tunable seasonal knobs off the shared
// anomaly_promotion blob, clamping each to a sane range and falling back to
// the compile-time default when a field is zero/absent or out of bounds. The
// clamp keeps the CH read bounded regardless of a hand-crafted API PUT (days
// caps the cutoff lookback; neighbourBuckets caps the ±window). Pure so the
// default/clamp table is unit-tested. v0.8.250.
func seasonalParams(cfg chstore.AnomalyPromotionConfig) (days, minSamples, neighborBuckets int) {
	days = cfg.SeasonalDays
	if days < 1 || days > 90 {
		days = seasonalDays
	}
	minSamples = cfg.SeasonalMinSamples
	if minSamples < 1 || minSamples > 500 {
		minSamples = seasonalMinSamples
	}
	neighborBuckets = cfg.SeasonalNeighborBuckets
	if neighborBuckets < 1 || neighborBuckets > 24 {
		neighborBuckets = seasonalNeighborBuckets
	}
	return days, minSamples, neighborBuckets
}

// buildAllSeasonalQuery is the batched twin of the old per-service seasonal
// read: ONE `GROUP BY service_name, t` pass matching the same time-of-day slot
// (± neighbour buckets, circular midnight-wrap) and day class across ALL
// services, instead of one `WHERE service_name = ?` query per service. The
// slot / class / radius binds are sweep constants — identical for every service
// in a tick — so the ONLY shape change from the per-service query is dropping
// the service filter and grouping by service_name (v0.8.507). Extracted pure so
// the SQL SHAPE is unit-tested — the circular midnight-wrap distance, the LIMIT
// + max_execution_time bounds, the time-bounded WHERE, the three-way day class,
// GROUP BY service_name, and that it reads the MV (never raw spans). The five
// `?` placeholders bind, in order: cutoff, dayClass, targetSecondsOfDay (twice),
// radius.
func buildAllSeasonalQuery(vexpr string) string {
	// sodExpr — the bucket's seconds-of-day on the same 5-min grid as targetSod
	// (buckets are 5-min aligned, so toSecond is 0; included for correctness).
	// v0.8.323 — pinned to UTC: the Go side derives slot/class from at.UTC(),
	// so the SQL must resolve hour/weekday on the SAME clock no matter what
	// the CH server's default timezone is. A TZ delta (app Europe/Istanbul,
	// CH UTC) silently matched the wrong time-of-day slot — day-peak history
	// against a night "now" — reintroducing the diurnal false positives this
	// seasonal feature exists to kill.
	const sodExpr = "(toHour(time_bucket, 'UTC') * 3600 + toMinute(time_bucket, 'UTC') * 60)"
	// classExpr — three-way bank day class. toDayOfWeek mode 0: 1=Mon … 6=Sat, 7=Sun.
	const classExpr = "multiIf(toDayOfWeek(time_bucket, 0, 'UTC') = 6, 'saturday', toDayOfWeek(time_bucket, 0, 'UTC') = 7, 'sunday', 'weekday')"

	// least(|sod-target|, 86400-|sod-target|) is the circular (midnight-wrap)
	// distance in seconds; <= radius keeps the ±neighborBuckets slots of the
	// matching day class. time_bucket >= cutoff prunes daily partitions first.
	//
	// v0.9.1052 (Faz 0.4, Q1) — ÜST SINIR eklendi (`time_bucket < ?`).
	// Üstsüz hâlde BUGÜNÜN slot penceresi de eşleşiyordu: yani şu an
	// yargılanan (muhtemelen anomalili) dwell kovaları ve TAMAMLANMAMIŞ
	// cari kova KENDİ baseline'larına giriyordu — kendini normalleştirme
	// (davranış motoru bunu recentCutoff ile baştan engelliyor;
	// v0.8.316'nın ardışık okuma için düzelttiği tamamlanmamış-kova
	// hatasının mevsimsel ikizi). Üst sınır çağıranda at−(radius+bucket):
	// bugünün penceresi tamamen dışarıda, dünün aynı slotu içeride.
	return fmt.Sprintf(`
		SELECT service_name, toUnixTimestamp(time_bucket) AS t, %[1]s AS v
		FROM service_summary_5m
		WHERE time_bucket >= ?
		  AND time_bucket < ?
		  AND %[3]s = ?
		  AND least(abs(%[2]s - ?), 86400 - abs(%[2]s - ?)) <= ?
		GROUP BY service_name, t
		ORDER BY service_name, t
		LIMIT 700 BY service_name
		LIMIT 14000000
		SETTINGS max_execution_time = 25`, vexpr, sodExpr, classExpr)
}

// fetchAllSeasonal runs buildAllSeasonalQuery once for a metric and returns the
// per-service seasonal samples (the same time-of-day slot as `at` PLUS its
// ±neighborBuckets neighbours, across the last `days` days, matched on `at`'s
// dayClass), keyed by service_name. Widening the slot into a neighbour window +
// splitting saturday/sunday + 14 days of history is what feeds the baseline
// enough samples on thin off-peak/night slots so it clears seasonalMinSamples
// instead of falling back to the flat 24h window (the diurnal-false-positive
// root cause — v0.8.250). The neighbour match is a CIRCULAR seconds-of-day
// distance so the window wraps correctly across midnight. Returns fewer (or no)
// samples for new/sparse services; chooseBaseline falls back to the consecutive
// window. `at` is fixed by the caller for the whole tick so the slot is
// consistent across every service. v0.8.507.
func (d *Detector) fetchAllSeasonal(ctx context.Context, metric string, at time.Time, days, neighborBuckets int) (map[string][]float64, error) {
	vexpr, err := metricValueExpr(metric)
	if err != nil {
		return nil, err
	}
	// v0.8.323 — slot + day class derive from UTC so they match the SQL's
	// UTC-pinned toHour/toDayOfWeek (see buildAllSeasonalQuery). With no DST
	// in TR, a constant offset keeps slot-matching consistent either way —
	// but only when BOTH sides share one clock.
	at = at.UTC()
	cutoff := at.Add(-time.Duration(days) * 24 * time.Hour)
	targetSod := slotSecondsOfDay(at)         // 5-min-aligned centre of the window
	radius := neighborBuckets * bucketSeconds // ±window half-width in seconds
	class := dayClass(at)
	// v0.9.1052 (Q1) — üst sınır: bugünün yargılanan penceresi (slot
	// ±radius) ve tamamlanmamış cari kova baseline'a giremez; dünün aynı
	// slotu (24s−radius uzakta) güvenle içeride kalır.
	upper := at.Add(-time.Duration(radius+bucketSeconds) * time.Second)

	rows, err := d.store.TelemetryReadConn().Query(ctx, buildAllSeasonalQuery(vexpr), cutoff, upper, class, targetSod, targetSod, radius)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]float64)
	// v0.9.1052 (Q2) — gün-çeşitliliği. seasonalMinSamples=4 +
	// neighbor=3 ile 7 aday kova TEK GÜNDE var: iki günlük yeni bir
	// servis "mevsimsel" baseline'ı tek günün gürültüsünden kurup MAD'i
	// dejenere ediyordu — davranış motorunun MinBucketRepeats ile
	// ÖLÇEREK kapattığı sınıfın (v0.9.957, tek tikte 178 aday) ikizi.
	// Yetersiz çeşitlilikte servis mevsimselden DÜŞER → chooseBaseline
	// ardışık pencereye iner.
	daysSeen := make(map[string]map[int64]struct{})
	for rows.Next() {
		var svc string
		var t uint32
		var v float64
		if err := rows.Scan(&svc, &t, &v); err != nil {
			return nil, err
		}
		accumulateSeries(out, svc, v)
		day := int64(t) / 86400
		if daysSeen[svc] == nil {
			daysSeen[svc] = map[int64]struct{}{}
		}
		daysSeen[svc][day] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	pruneSeasonalByDayDiversity(out, daysSeen, seasonalMinDays)
	return out, nil
}

// seasonalMinDays — mevsimsel baseline'ın en az kaç FARKLI günden örnek
// taşıması gerekir (v0.9.1052, Q2). Davranış motorunun yeterlilik
// kuralıyla aynı sayı (≥3 farklı gün): tek/iki günün örnekleri "mevsim"
// değil, o günlerin gürültüsüdür.
const seasonalMinDays = 3

// pruneSeasonalByDayDiversity — saf, tablo-testli: çeşitliliği yetersiz
// servislerin mevsimsel serisini haritadan düşürür (ardışık pencereye
// düşüş chooseBaseline'da kendiliğinden olur).
func pruneSeasonalByDayDiversity(out map[string][]float64, daysSeen map[string]map[int64]struct{}, minDays int) {
	for svc := range out {
		if len(daysSeen[svc]) < minDays {
			delete(out, svc)
		}
	}
}

// chooseBaseline prefers the seasonal same-slot samples when seasonal mode is
// on AND there are at least minSamples of them; otherwise it falls back to the
// 24h consecutive baseline (new / sparse service, or seasonal disabled).
func chooseBaseline(seasonal, consecutive []float64, minSamples int) []float64 {
	if seasonalBaseline && len(seasonal) >= minSamples {
		return seasonal
	}
	return consecutive
}

// medianMAD returns the median and the Median Absolute Deviation
// (median of |x_i - median|) of xs — the outlier-robust analogue of
// mean+stdev that the modified z-score in checkOne uses. MAD=0 when the
// sample is empty or (near-)constant, mirroring meanStdev's stdev=0 case.
func medianMAD(xs []float64) (median, mad float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	median = medianOf(xs)
	dev := make([]float64, len(xs))
	for i, v := range xs {
		dev[i] = math.Abs(v - median)
	}
	return median, medianOf(dev)
}

// medianOf returns the median of xs without mutating the caller's slice.
func medianOf(xs []float64) float64 {
	n := len(xs)
	if n == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// meanStdev — population standard deviation. Stdev=0 when n<2.
// Retained for callers other than checkOne (which now uses medianMAD).
func meanStdev(xs []float64) (mean, stdev float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	for _, v := range xs {
		mean += v
	}
	mean /= float64(len(xs))
	if len(xs) < 2 {
		return mean, 0
	}
	var ss float64
	for _, v := range xs {
		d := v - mean
		ss += d * d
	}
	return mean, math.Sqrt(ss / float64(len(xs)))
}

func displayMetric(m string) string {
	switch m {
	case "p99_ms":
		return "P99 latency"
	case "error_rate":
		return "Error rate"
	case "request_rate":
		return "Request rate"
	}
	return m
}
func unitOf(m string) string {
	if strings.HasSuffix(m, "_ms") {
		return "ms"
	}
	if m == "error_rate" {
		return "%"
	}
	if m == "request_rate" {
		return "/s"
	}
	return ""
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
