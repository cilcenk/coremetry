package anomaly

// external.go — dış (Influx) seri anomalisi (v0.10.228, audit
// docs/audit/influx-integration.md K6 + dilim D3).
//
// Kaynak: metric_points'teki `ext:<sorgu>` gauge serisi (service_name =
// kaynak adı, attr'lar = groupBy). Poller her kovayı Grafana'nın
// aggregateWindow toplamı olarak kova zamanına yazar (v0.10.224); burada
// dakikalık kovalar OKUNUR, karar mevcut evaluateAnomaly'ye (MAD, dwell,
// kritik z) verilir. Ayrı bir istatistik yolu YOK: aynı verdict, aynı
// hassasiyet blobu — yalnız sorgu başına eşik üst-yazımı (Thresholds).
//
// Yaşam döngüsü: evaluator.sweepStaleProblems updated_at'i 3×interval'den
// eski her açık problemi "source silent" diye kapatır (kind'a bakmaz). Bu
// tarayıcı YALNIZ başarılı bir poll'dan sonra koşar (influx.Worker hook)
// ve açık problemi her tikte dokunur (touch). Influx erişilemezken hiç
// koşmaz → süpürme dürüst gerekçeyle kapatır; sıfır-padli sahte
// "iyileşti" üretilmez.
//
// Pencere: kaynağın GÖZLENMİŞ ilk ve son kovası arası (en çok externalSlots
// dakika). Sabit 4 saatlik pencere genç bir kaynağı 200 sıfırla doldurur,
// medyan 0 olur ve ilk gerçek değer "anomali" diye açılırdı — geometriye
// değil gözlenmiş kanıta karar (v0.10.199 dersi). Aralık İÇİNDEKİ eksik
// dakikalar 0'dır: hata SAYISI serisinde eksik = sıfır hata (audit R3).

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/notify"
)

const (
	// ExternalMetricPrefix — poller'ın yazdığı metrik adı öneki (audit §7).
	ExternalMetricPrefix = "ext:"
	// externalSlots — okunan en uzun geçmiş: 4 saat × 1 dk.
	externalSlots = 240
	externalStep  = time.Minute
	// externalDefaultMinAbsDelta / MinMAD — sorgu eşiği boşsa (audit §3):
	// 5'lik mutlak fark altı gürültü, MAD tabanı 1 birim.
	externalDefaultMinAbsDelta = 5
	externalDefaultMinMAD      = 1
)

// ExternalTarget — bir kaynak+sorgu çifti; influx.QueryConfig'ten main.go
// çevirir (anomaly paketi influx'a bağlanmaz).
type ExternalTarget struct {
	SourceID   string
	SourceName string   // metric_points.service_name
	Query      string   // metrik = ExternalMetricPrefix + Query
	GroupBy    []string // metric attr adları (attrMap uygulanmış)
	Thresholds ExternalThresholds
	// OnEvidence (v0.10.229, D4) — kanıt toplama kancası: açılışta hemen,
	// sürerken externalEnrichEvery'de bir, tik başına ≤externalEnrichPerTick.
	// main.go'da influx.Enricher'a bağlanır; nil = kanıt yok (yalnız Problem).
	OnEvidence func(ctx context.Context, ev ExternalEvent)
}

// ExternalEvent — Problem + karar sayıları + pencere [startedAt−2m, now].
type ExternalEvent struct {
	Target  ExternalTarget
	Problem chstore.Problem
	Values  []string // grup değerleri, Target.GroupBy sırasıyla
	Current float64
	Median  float64
	MAD     float64
	Z       float64
	From    time.Time
	To      time.Time
}

const (
	externalEnrichEvery   = 5 * time.Minute
	externalEnrichPerTick = 20
	externalEnrichLead    = 2 * time.Minute
)

// ExternalThresholds — influx.Thresholds ile alan-alan aynı (dönüşüm
// main.go'da tip çevirisiyle). Sıfır = varsayılan.
type ExternalThresholds struct {
	CriticalZ   float64
	Dwell       int
	MinAbsDelta float64
	MinMAD      float64
}

// ExternalScanReport — bir Scan'in sayımı (log + test). Seasonal = mevsimsel
// baseline'la karar verilen seri sayısı (0 = kapı kapalı ya da geçmiş yok).
type ExternalScanReport struct {
	Series, Opened, Refreshed, Resolved, Touched, Skipped, Seasonal int
}

type externalStore interface {
	QueryMetric(ctx context.Context, f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error)
	OpenProblemsSnapshot(ctx context.Context) (*chstore.OpenProblems, error)
	UpsertProblem(ctx context.Context, p chstore.Problem) error
	AnomalySensitivity() chstore.AnomalySensitivityConfig
	// v0.10.231 (D6) — mevsimsel baseline: promosyon blobu (gün/örnek/yarıçap),
	// metric_points ufku (retention kapısı) ve aynı-dilim okuması.
	GetAnomalyPromotion(ctx context.Context) chstore.AnomalyPromotionConfig
	MetricsHorizonDays(ctx context.Context) int
	ExternalSeasonal(ctx context.Context, req chstore.ExternalSeasonalReq) (map[string][]float64, map[string]map[int64]struct{}, error)
}

// ExternalScanner — kaynak+sorgu başına seri tarayıcısı.
type ExternalScanner struct {
	store    externalStore
	notifier *notify.Notifier
	now      func() time.Time
	// lastEnriched — ruleID → son kanıt toplama; tek goroutine (worker tiki).
	lastEnriched map[string]time.Time
}

func NewExternalScanner(store externalStore, n *notify.Notifier) *ExternalScanner {
	return &ExternalScanner{store: store, notifier: n, now: time.Now, lastEnriched: map[string]time.Time{}}
}

// Scan — hedefin bütün serilerini okur, her biri için karar verir ve
// uygular. Hata yalnız okuma katmanından döner; tek serinin upsert hatası
// loglanır, diğer seriler devam eder.
func (s *ExternalScanner) Scan(ctx context.Context, t ExternalTarget) (ExternalScanReport, error) {
	var rep ExternalScanReport
	if t.SourceName == "" || t.Query == "" {
		return rep, fmt.Errorf("external target: source name and query required")
	}
	metric := ExternalMetricPrefix + t.Query
	now := s.now()
	series, err := s.store.QueryMetric(ctx, chstore.MetricQueryFilter{
		Name:        metric,
		Service:     t.SourceName,
		GroupBy:     t.GroupBy,
		Aggregation: "max", // aynı kovanın yinelenen yazımı (poll örtüşmesi) idempotent
		From:        now.Add(-externalSlots * externalStep),
		To:          now,
		StepSeconds: int(externalStep / time.Second),
	})
	if err != nil {
		return rep, err
	}
	start, end, ok := observedSpan(series)
	if !ok {
		return rep, nil
	}
	openSnap, err := s.store.OpenProblemsSnapshot(ctx)
	if err != nil {
		return rep, err
	}
	cfg := externalSensitivity(s.store.AnomalySensitivity(), metric, t.Thresholds)
	seasonal, seasonalMin := s.seasonalFor(ctx, t, metric, now)
	enriched := 0
	for _, sr := range series {
		rep.Series++
		buckets := padMinuteSlots(sr.Points, start, end)
		subject := ExternalSubject(t.SourceName, sr.GroupKey)
		ruleID := "anomaly:" + subject + ":" + metric
		open := openSnap.ByKey(ruleID, subject)
		hasOpen := open != nil && open.ID != ""
		// seasonalMin ≥ 1, ASLA 0: chooseBaseline `len(seasonal) >= min` ile
		// seçer; 0 geçilirse BOŞ mevsimsel dizi kazanır, medyan 0 olur ve her
		// değer anomali görünür (TestExternalScan_OpensProblemOnSpike'ın
		// Threshold iddiası). Mevsimsel yoksa/azsa ardışık 4 saat kazanır.
		season := seasonal[strings.Join(sr.GroupKey, chstore.ExternalSeasonalKeySep)]
		if len(season) >= seasonalMin {
			rep.Seasonal++
		}
		oc := evaluateAnomaly(metric, buckets, season, ones(len(buckets)), seasonalMin, hasOpen, cfg)
		live := s.apply(ctx, &rep, t, metric, subject, ruleID, sr.GroupKey, oc, open, hasOpen, now)
		if live != nil && t.OnEvidence != nil && enriched < externalEnrichPerTick && s.enrichDue(ruleID, live.StartedAt, now) {
			enriched++
			s.lastEnriched[ruleID] = now
			t.OnEvidence(ctx, ExternalEvent{
				Target: t, Problem: *live, Values: sr.GroupKey,
				Current: oc.Current, Median: oc.Median, MAD: oc.MAD, Z: oc.Z,
				From: time.Unix(0, live.StartedAt).UTC().Add(-externalEnrichLead), To: now,
			})
		}
	}
	return rep, nil
}

// seasonalFor (v0.10.231, D6) — hedefin bütün serileri için aynı-dilim
// geçmişi: gün-sınıfı (hafta içi / cumartesi / pazar) + dakika-of-day ±
// yarıçap, promosyon blobundaki gün/örnek/yarıçap ayarlarıyla
// (seasonalParams — servis dedektörüyle AYNI kural). Kapı: metric_points
// ufku (retention.metrics) gün-çeşitliliği eşiğinin (seasonalMinDays)
// altındaysa okuma HİÇ yapılmaz — ardışık 4 saat baseline kalır (audit
// R7: Faz 2 kapısı = prod ayarı). Ufuk gün sayısından kısaysa gün sayısı
// ufka iner: eldeki geçmişle mevsimsel, sıfır yerine. Okuma hatası
// fail-open (ardışık baseline), loglanır. Dönüş: anahtar → değerler,
// ve karar eşiği (minSamples ≥ 1).
func (s *ExternalScanner) seasonalFor(ctx context.Context, t ExternalTarget, metric string, now time.Time) (map[string][]float64, int) {
	days, minS, neighbor := seasonalParams(s.store.GetAnomalyPromotion(ctx))
	if minS < 1 {
		minS = 1
	}
	if horizon := s.store.MetricsHorizonDays(ctx); horizon > 0 && horizon < days {
		days = horizon
	}
	if !seasonalBaseline || days < seasonalMinDays {
		return nil, minS
	}
	at := now.UTC().Truncate(externalStep)
	radius := neighbor * bucketSeconds // ±(N × 5 dk): servis dedektörüyle aynı duvar-saati genişliği
	out, daysSeen, err := s.store.ExternalSeasonal(ctx, chstore.ExternalSeasonalReq{
		Metric: metric, Service: t.SourceName, GroupBy: t.GroupBy,
		Cutoff:    at.Add(-time.Duration(days) * 24 * time.Hour),
		Upper:     at.Add(-time.Duration(radius+int(externalStep/time.Second)) * time.Second),
		Class:     dayClass(at),
		TargetSod: at.Hour()*3600 + at.Minute()*60,
		RadiusSec: radius,
	})
	if err != nil {
		log.Printf("[anomaly/external] seasonal %s/%s: %v (ardışık baseline)", t.SourceName, t.Query, err)
		return nil, minS
	}
	pruneSeasonalByDayDiversity(out, daysSeen, seasonalMinDays)
	return out, minS
}

// enrichDue — açılışta hemen (kayıt yok), sonra externalEnrichEvery'de bir.
func (s *ExternalScanner) enrichDue(ruleID string, startedAt int64, now time.Time) bool {
	last, ok := s.lastEnriched[ruleID]
	if !ok {
		return true
	}
	return now.Sub(last) >= externalEnrichEvery
}

// apply — kararı uygular; dönüş = hâlâ AÇIK problem (kanıt kancası için),
// çözüldü/yok ise nil.
func (s *ExternalScanner) apply(ctx context.Context, rep *ExternalScanReport, t ExternalTarget,
	metric, subject, ruleID string, values []string, oc anomalyOutcome, open *chstore.Problem, hasOpen bool, now time.Time) *chstore.Problem {
	switch oc.Action {
	case "open":
		desc := externalDescription(metric, subject, t.GroupBy, values, oc)
		if hasOpen {
			open.Value = oc.Current
			open.Comparator = anomalyComparator(oc.Direction)
			open.Description = desc
			if err := s.store.UpsertProblem(ctx, *open); err != nil {
				log.Printf("[anomaly/external] refresh %s: %v", ruleID, err)
				return nil
			}
			rep.Refreshed++
			return open
		}
		p := chstore.Problem{
			ID:          newID(),
			RuleID:      ruleID,
			RuleName:    "Anomaly · " + displayMetric(metric),
			Severity:    oc.Severity,
			Service:     subject,
			Kind:        chstore.ProblemKindExternal,
			Metric:      metric,
			Value:       oc.Current,
			Threshold:   oc.Median,
			Comparator:  anomalyComparator(oc.Direction),
			Status:      "open",
			Description: desc,
			StartedAt:   now.UnixNano(),
		}
		if err := s.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[anomaly/external] open %s: %v", ruleID, err)
			return nil
		}
		rep.Opened++
		log.Printf("[anomaly/external] OPENED %s · %s = %.0f (med=%.1f mad=%.2f z=%.1f)",
			subject, metric, oc.Current, oc.Median, oc.MAD, oc.Z)
		if s.notifier != nil {
			go s.notifier.SendProblemAlert(context.Background(), p)
		}
		return &p
	case "resolve":
		if !hasOpen {
			return nil
		}
		chstore.MarkResolved(open, now.UnixNano())
		if err := s.store.UpsertProblem(ctx, *open); err != nil {
			log.Printf("[anomaly/external] resolve %s: %v", ruleID, err)
			return open
		}
		rep.Resolved++
		delete(s.lastEnriched, ruleID)
		log.Printf("[anomaly/external] RESOLVED %s · %s (recovered, z=%.1f)", subject, metric, oc.Z)
		return nil
	default: // none | skip
		if !hasOpen {
			rep.Skipped++
			return nil
		}
		// Touch: kaynak canlı, karar "sürüyor" — updated_at yenilenmezse
		// evaluator'ın bayat süpürmesi 3×interval sonra "source silent"
		// diye kapatır (feedback-slow-detectors-vs-problem-lifecycle).
		if err := s.store.UpsertProblem(ctx, *open); err != nil {
			log.Printf("[anomaly/external] touch %s: %v", ruleID, err)
			return open
		}
		rep.Touched++
		return open
	}
}

// ExternalSubject — Problem.Service: `ext:<kaynak>/<v1>/<v2>` (db: emsali).
// FE problemSubject.ts aynı biçimi çözer; kind alanı olmayan eski sekme
// önekten tanır.
func ExternalSubject(source string, values []string) string {
	if len(values) == 0 {
		return ExternalMetricPrefix + source
	}
	return ExternalMetricPrefix + source + "/" + strings.Join(values, "/")
}

// externalDescription — her Problem'le giden gerekçe (CLAUDE.md: reason
// string ships with every Problem): sayı + baseline + z + dwell + etiketler.
func externalDescription(metric, subject string, keys, values []string, oc anomalyOutcome) string {
	labels := make([]string, 0, len(values))
	for i, v := range values {
		if i < len(keys) && keys[i] != "" {
			labels = append(labels, keys[i]+"="+v)
		} else {
			labels = append(labels, v)
		}
	}
	desc := fmt.Sprintf("%s %s on %s — current %.0f vs baseline %.0f (%.1fσ, sustained %d buckets).",
		displayMetric(metric), oc.Direction, subject, oc.Current, oc.Median, oc.Z, oc.Dwell)
	if len(labels) > 0 {
		desc += " Labels: " + strings.Join(labels, ", ")
	}
	return desc
}

// externalSensitivity — küresel hassasiyet blobunun kopyası + bu metrik
// için sorgu eşikleri. Metrics haritası KOPYALANIR: atomik snapshot'ın
// haritasına yazmak paylaşılan durumu bozar (v0.10.156 dersi).
func externalSensitivity(base chstore.AnomalySensitivityConfig, metric string, th ExternalThresholds) chstore.AnomalySensitivityConfig {
	cfg := base
	cfg.Metrics = make(map[string]chstore.AnomalyMetricSensitivity, len(base.Metrics)+1)
	for k, v := range base.Metrics {
		cfg.Metrics[k] = v
	}
	ms := chstore.AnomalyMetricSensitivity{FloorPct: 0.10, MinAbsDelta: externalDefaultMinAbsDelta, MinMAD: externalDefaultMinMAD}
	if th.MinAbsDelta > 0 {
		ms.MinAbsDelta = th.MinAbsDelta
	}
	if th.MinMAD > 0 {
		ms.MinMAD = th.MinMAD
	}
	cfg.Metrics[metric] = ms
	if th.CriticalZ > 0 {
		cfg.CriticalZ = th.CriticalZ
	}
	if th.Dwell > 0 {
		cfg.DwellBuckets = th.Dwell
	}
	return cfg
}

// observedSpan — bütün serilerin gözlenmiş ilk/son kova dakikası; son
// externalSlots dakikaya kırpılır. Nokta yoksa ok=false.
func observedSpan(series []chstore.SpanMetricSeries) (start, end time.Time, ok bool) {
	var minNs, maxNs int64
	for _, sr := range series {
		for _, p := range sr.Points {
			if !ok || p.Time < minNs {
				minNs = p.Time
			}
			if !ok || p.Time > maxNs {
				maxNs = p.Time
			}
			ok = true
		}
	}
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	start = time.Unix(0, minNs).UTC().Truncate(externalStep)
	end = time.Unix(0, maxNs).UTC().Truncate(externalStep)
	if floor := end.Add(-(externalSlots - 1) * externalStep); start.Before(floor) {
		start = floor
	}
	return start, end, true
}

// padMinuteSlots — [start, end] dakikalarına yerleştirilmiş değerler;
// eksik dakika 0, aralık dışı nokta yok sayılır, aynı dakikaya iki nokta
// düşerse büyüğü kalır (Aggregation "max" ile tutarlı). SAF.
func padMinuteSlots(points []chstore.SpanMetricPoint, start, end time.Time) []float64 {
	if end.Before(start) {
		return nil
	}
	n := int(end.Sub(start)/externalStep) + 1
	out := make([]float64, n)
	startNs := start.UnixNano()
	step := int64(externalStep)
	for _, p := range points {
		if p.Time < startNs {
			continue
		}
		i := int((p.Time - startNs) / step)
		if i >= n {
			continue
		}
		if p.Value > out[i] {
			out[i] = p.Value
		}
	}
	return out
}

// ones — hacim serisi: dış seride "istek hızı" yok; kovaların hepsi canlı
// sayılır ki trimTrailingSilent baseline'ı kırpmasın ve hacim kapısı
// geçsin. Padlenmiş sıfırlar da GERÇEK gözlemdir (sıfır hata).
func ones(n int) []float64 {
	r := make([]float64, n)
	for i := range r {
		r[i] = 1
	}
	return r
}
