package influx

// poller.go — Influx → metric_points poll işçisi (v0.10.223, Influx D2;
// audit §7 veri modeli + §5 lider kancası + K1/K3).
//
// Akış (yalnız worker lideri; kapı çağıranda — main.go entity-syncer
// şablonu): kaynak aralığı dolunca SORGU 1 → Record'lar → SAF
// BuildMetricsRequest → OTLP Gauge isteği → otlp.ConvertMetrics →
// sink.InsertMetrics. Dönüştürücüden geçmek bilinçli (audit §7,
// /otlp-converter): attribute yönlendirme + series_fingerprint TEK
// kaynaktan; CH'deki her satır OTLP kapısından geçmiş olur, metric_catalog
// MV kaynağı kendiliğinden kaydeder (K1 bedava kazanımlar).
//
// Semantik (K3, v0.10.224 ile GRAFANA HİZASI): operatörün gerçek sorgusu
// `aggregateWindow(every: 1m, fn: sum)` — kayıt `_time` = kova BİTİŞİ
// taşır ve nokta zamanı ODUR; Coremetry'deki dakika Grafana'daki dakikayla
// aynı sayıyı gösterir. Tamamlanmamış kova (`_time` > now) atlanır; aynı
// kova (kaynak, sorgu) başına watermark ile bir kez yazılır (30 s poll ×
// 2 dk range aynı kovayı 2-4 kez getirir). `_time` yoksa (spec'in düz
// `sum()` sorgusu) değer poll anına yazılır — eski gauge davranışı.
// Boş sonuç → satır YOK (sıfır pad D3 dedektöründe, audit R3). Düşen
// satırlar (kötü değer / eksik tag / tavan) sayılır, sessiz düşüş yok
// (otlp-converter §3 sınıf B/C).

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	metricscollpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/otlp"
	"github.com/cilcenk/coremetry/internal/selfobs"
)

const (
	// MaxRowsPerQuery — bir poll'un üretebileceği seri tavanı (grup
	// kardinalitesi kapısı, audit §7). Aşan satırlar düşer ve sayılır.
	MaxRowsPerQuery = 5000
	// MetricPrefix — dış seri metrik adı öneki (`ext:<sorgu adı>`).
	MetricPrefix = "ext:"
	// tickGranularity — Run döngüsü; kaynak aralığı (≥10 s) bunun katı
	// olmak zorunda değil, nextDue geçmişse poll.
	tickGranularity = 5 * time.Second
)

// MetricSink — chstore.Store'un bu işçi için gereken tek metodu.
type MetricSink interface {
	InsertMetrics(ctx context.Context, pts []*chstore.MetricPoint) error
}

// DropStats — BuildMetricsRequest'in düşürdüğü satırlar (sınıf B/C: say).
type DropStats struct {
	BadValue   int // _value yok ya da sayı değil
	MissingTag int // groupBy tag'i yok ya da boş
	OverCap    int // MaxRowsPerQuery aşımı
}

func (d DropStats) Total() int { return d.BadValue + d.MissingTag + d.OverCap }

// SkipStats — SplitBuckets'ın atladıkları (say, sessizce düşürme).
type SkipStats struct {
	Old     int // ≤ watermark: zaten yazılmış kova
	Partial int // _time > now: tamamlanmamış kova
}

// recordTime — `_time` varsa ve çözülüyorsa (RFC3339/RFC3339Nano) kova
// bitişi; yoksa sıfır zaman (çağıran poll anına düşer).
func recordTime(r Record) time.Time {
	raw := r.Values["_time"]
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t
	}
	return time.Time{}
}

// SplitBuckets — SAF: kayıtları watermark'a göre ayıklar. `_time` taşıyan
// kayıtlardan yalnız (watermark, now] aralığındakiler kalır; `_time`'sız
// kayıtlar (düz sum()) watermark'a bakılmaksızın geçer. Yeni watermark =
// kalan kayıtların en büyük `_time`'ı (yoksa eski değer).
func SplitBuckets(recs []Record, watermark, now time.Time) (kept []Record, newWatermark time.Time, st SkipStats) {
	newWatermark = watermark
	kept = make([]Record, 0, len(recs))
	for _, r := range recs {
		t := recordTime(r)
		if t.IsZero() {
			kept = append(kept, r)
			continue
		}
		if t.After(now) {
			st.Partial++
			continue
		}
		if !t.After(watermark) {
			st.Old++
			continue
		}
		kept = append(kept, r)
		if t.After(newWatermark) {
			newWatermark = t
		}
	}
	return kept, newWatermark, st
}

func strKV(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

// BuildMetricsRequest — SAF: Influx kayıtları → OTLP Gauge isteği.
// Resource: service.name=<kaynak adı> (metric_points.service_name, ORDER
// BY öneki), coremetry.source=influx, influx.source.id. Data point attrs:
// groupBy tag'leri, attrMap ile adlandırılmış, groupBy SIRASIYLA
// (fingerprint kararlılığı). Zaman: kaydın `_time`'ı (aggregateWindow kova
// bitişi, Grafana hizası) — yoksa now (poll anı).
func BuildMetricsRequest(src SourceConfig, qc QueryConfig, recs []Record, now time.Time) (*metricscollpb.ExportMetricsServiceRequest, DropStats) {
	var drops DropStats
	pollTs := uint64(now.UnixNano())
	points := make([]*metricspb.NumberDataPoint, 0, len(recs))
	for _, r := range recs {
		if len(points) >= MaxRowsPerQuery {
			drops.OverCap++
			continue
		}
		v, err := strconv.ParseFloat(r.Values["_value"], 64)
		if err != nil {
			drops.BadValue++
			continue
		}
		ts := pollTs
		if bt := recordTime(r); !bt.IsZero() {
			ts = uint64(bt.UnixNano())
		}
		attrs := make([]*commonpb.KeyValue, 0, len(qc.GroupBy))
		ok := true
		for _, tag := range qc.GroupBy {
			val := r.Values[tag]
			if val == "" {
				ok = false
				break
			}
			attrs = append(attrs, strKV(qc.metricAttrKey(tag), val))
		}
		if !ok {
			drops.MissingTag++
			continue
		}
		points = append(points, &metricspb.NumberDataPoint{
			Attributes:   attrs,
			TimeUnixNano: ts,
			Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: v},
		})
	}
	req := &metricscollpb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				strKV("service.name", src.Name),
				strKV("coremetry.source", "influx"),
				strKV("influx.source.id", src.ID),
				strKV("influx.org", src.Org),
			}},
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Scope: &commonpb.InstrumentationScope{Name: "coremetry/influx"},
				Metrics: []*metricspb.Metric{{
					Name:        MetricPrefix + qc.Name,
					Description: fmt.Sprintf("InfluxDB kaynağı %s — poll sorgusu %s (gauge; zaman = kova bitişi, yoksa poll anı)", src.Name, qc.Name),
					Data:        &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: points}},
				}},
			}},
		}},
	}
	return req, drops
}

// SourceStatus — işçinin kaynak başına bellek-içi durumu (bu pod'da).
type SourceStatus struct {
	SourceID   string `json:"sourceId"`
	Name       string `json:"name"`
	LastPollAt int64  `json:"lastPollAt,omitempty"` // unix ms
	NextDueAt  int64  `json:"nextDueAt,omitempty"`  // unix ms
	LastRows   int    `json:"lastRows"`
	LastPoints int    `json:"lastPoints"`
	LastDrops  int    `json:"lastDrops"`
	// v0.10.224 — watermark ayıklaması: zaten yazılmış / tamamlanmamış kova.
	LastSkippedOld     int    `json:"lastSkippedOld"`
	LastSkippedPartial int    `json:"lastSkippedPartial"`
	LastError          string `json:"lastError,omitempty"`
}

// Worker — leader-gated poll döngüsü.
type Worker struct {
	svc  *Service
	sink MetricSink

	// enjekte edilebilir (testler): saat + kaynak istemcisi.
	now         func() time.Time
	queryAPIFor func(SourceConfig) (QueryAPI, error)

	// hook (v0.10.228, D3) — başarılı bir poll'dan sonra sorgu başına çağrı;
	// dış anomali tarayıcısı buradan koşar. Poll hata verdiyse ÇAĞRILMAZ:
	// Influx erişilemezken CH'deki seriyi okumak sıfır-padli "iyileşti"
	// uydururdu; sessiz kaynağı evaluator'ın bayat süpürmesi kapatır.
	hook func(ctx context.Context, src SourceConfig, qc QueryConfig)

	mu      sync.Mutex
	nextDue map[string]time.Time
	status  map[string]SourceStatus
	// watermark — (kaynak id + "/" + sorgu adı) → en son yazılan kova bitişi.
	watermark map[string]time.Time

	mPolls, mPoints, mDropped, mErrors metric.Int64Counter
}

// NewWorker — sink tipik olarak *chstore.Store.
// SetHook — poll-sonrası çağrı (bkz. Worker.hook). Start'tan ÖNCE kurulur.
func (w *Worker) SetHook(h func(ctx context.Context, src SourceConfig, qc QueryConfig)) {
	w.hook = h
}

func NewWorker(svc *Service, sink MetricSink) *Worker {
	w := &Worker{
		svc: svc, sink: sink,
		now:         time.Now,
		queryAPIFor: svc.QueryAPIFor,
		nextDue:     map[string]time.Time{},
		status:      map[string]SourceStatus{},
		watermark:   map[string]time.Time{},
	}
	m := selfobs.Meter()
	var err error
	if w.mPolls, err = m.Int64Counter("influx_polls_total", metric.WithDescription("Influx poll sorguları")); err != nil {
		log.Printf("[influx] metric influx_polls_total: %v", err)
	}
	if w.mPoints, err = m.Int64Counter("influx_points_total", metric.WithDescription("metric_points'e yazılan Influx noktaları")); err != nil {
		log.Printf("[influx] metric influx_points_total: %v", err)
	}
	if w.mDropped, err = m.Int64Counter("influx_rows_dropped_total", metric.WithDescription("Düşen Influx satırları (kötü değer / eksik tag / tavan)")); err != nil {
		log.Printf("[influx] metric influx_rows_dropped_total: %v", err)
	}
	if w.mErrors, err = m.Int64Counter("influx_errors_total", metric.WithDescription("Influx poll/yazım hataları")); err != nil {
		log.Printf("[influx] metric influx_errors_total: %v", err)
	}
	return w
}

// Run — tickGranularity'de döner; her tik lider kapısından geçer. Lider
// olmayan pod hiçbir şey yapmaz (durum haritası boş kalır).
func (w *Worker) Run(ctx context.Context, isLeader func() bool) {
	t := time.NewTicker(tickGranularity)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if isLeader == nil || isLeader() {
				w.Tick(ctx)
			}
		}
	}
}

// Tick — aralığı dolmuş etkin kaynakları poll'lar. Kaynaklar birbirinden
// yalıtık: birinin hatası diğerini durdurmaz, LastError'a yazılır.
func (w *Worker) Tick(ctx context.Context) {
	cfg := w.svc.CurrentSettings()
	now := w.now()
	for _, src := range cfg.Sources {
		if !src.Enabled || src.ID == "" {
			continue
		}
		w.mu.Lock()
		due, seen := w.nextDue[src.ID]
		w.mu.Unlock()
		if seen && now.Before(due) {
			continue
		}
		interval := time.Duration(src.IntervalSec) * time.Second
		if interval < MinIntervalSec*time.Second {
			interval = DefaultIntervalSec * time.Second
		}
		st := w.pollSource(ctx, src, now)
		st.NextDueAt = now.Add(interval).UnixMilli()
		if st.LastError == "" && w.hook != nil {
			for _, qc := range src.Queries {
				w.hook(ctx, src, qc)
			}
		}
		w.mu.Lock()
		w.nextDue[src.ID] = now.Add(interval)
		w.status[src.ID] = st
		w.mu.Unlock()
	}
	// Silinen/kapatılan kaynakların durumu haritadan düşsün.
	live := map[string]bool{}
	for _, src := range cfg.Sources {
		if src.Enabled {
			live[src.ID] = true
		}
	}
	w.mu.Lock()
	for id := range w.status {
		if !live[id] {
			delete(w.status, id)
			delete(w.nextDue, id)
		}
	}
	w.mu.Unlock()
}

func (w *Worker) pollSource(ctx context.Context, src SourceConfig, now time.Time) SourceStatus {
	st := SourceStatus{SourceID: src.ID, Name: src.Name, LastPollAt: now.UnixMilli()}
	attrs := metric.WithAttributes(attribute.String("source", selfobs.SafeAttr(src.Name)))
	q, err := w.queryAPIFor(src)
	if err != nil {
		st.LastError = err.Error()
		w.count(w.mErrors, ctx, 1, attrs)
		log.Printf("[influx] %s: istemci: %v", src.Name, err)
		return st
	}
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for _, qc := range src.Queries {
		w.count(w.mPolls, ctx, 1, attrs)
		recs, qerr := q.Query(pctx, qc.Flux)
		if qerr != nil {
			st.LastError = fmt.Sprintf("%s: %v", qc.Name, qerr)
			w.count(w.mErrors, ctx, 1, attrs)
			log.Printf("[influx] %s/%s: sorgu: %v", src.Name, qc.Name, qerr)
			continue
		}
		st.LastRows += len(recs)
		if len(recs) == 0 {
			continue // boş küme: satır yok, sıfır yazılmaz (D3 pad'ler)
		}
		wmKey := src.ID + "/" + qc.Name
		w.mu.Lock()
		wm := w.watermark[wmKey]
		w.mu.Unlock()
		kept, newWM, skips := SplitBuckets(recs, wm, now)
		st.LastSkippedOld += skips.Old
		st.LastSkippedPartial += skips.Partial
		if len(kept) == 0 {
			continue // hepsi ya yazılmış ya kısmi kova
		}
		req, drops := BuildMetricsRequest(src, qc, kept, now)
		if n := drops.Total(); n > 0 {
			st.LastDrops += n
			w.count(w.mDropped, ctx, int64(n), attrs)
			log.Printf("[influx] %s/%s: %d satır düştü (kötü değer %d, eksik tag %d, tavan %d)",
				src.Name, qc.Name, n, drops.BadValue, drops.MissingTag, drops.OverCap)
		}
		pts, _ := otlp.ConvertMetrics(req)
		if len(pts) == 0 {
			continue
		}
		if werr := w.sink.InsertMetrics(pctx, pts); werr != nil {
			st.LastError = fmt.Sprintf("%s: yazım: %v", qc.Name, werr)
			w.count(w.mErrors, ctx, 1, attrs)
			log.Printf("[influx] %s/%s: metric_points yazım: %v", src.Name, qc.Name, werr)
			continue
		}
		st.LastPoints += len(pts)
		w.count(w.mPoints, ctx, int64(len(pts)), attrs)
		// Watermark yalnız YAZIM başarılıysa ilerler; yazım düşerse kova bir
		// sonraki poll'da yeniden denenir (kayıp değil, gecikme).
		w.mu.Lock()
		if newWM.After(w.watermark[wmKey]) {
			w.watermark[wmKey] = newWM
		}
		w.mu.Unlock()
	}
	return st
}

func (w *Worker) count(c metric.Int64Counter, ctx context.Context, n int64, opts ...metric.AddOption) {
	if c != nil {
		c.Add(ctx, n, opts...)
	}
}

// Status — kaynak başına son durum, ada göre sıralı (bu pod'daki işçi).
func (w *Worker) Status() []SourceStatus {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]SourceStatus, 0, len(w.status))
	for _, s := range w.status {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
