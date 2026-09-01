package influx

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// poller_test.go — v0.10.223 (Influx D2, audit §7 + §10 D2).
//
// Sözleşme:
//   • BuildMetricsRequest — SAF: Influx kayıtları → OTLP Gauge isteği.
//     Kaynak → resource {service.name=<kaynak adı>, coremetry.source=influx,
//     influx.source.id}; sorgu → metrik `ext:<ad>`; groupBy tag'leri →
//     data point attribute'ları (attrMap ile ADLANDIRILIR, sırası groupBy
//     sırası — fingerprint kararlılığı); zaman = POLL ANI (K3: `sum()`
//     _time'ı kaybeder). groupBy tag'i eksik/boş → satır DÜŞER + sayılır;
//     `_value` sayı değilse DÜŞER + sayılır; tavan aşımı DÜŞER + sayılır
//     (otlp-converter §3: sessiz düşüş yok).
//   • Worker.Tick — lider kapısı çağıranda; kaynak başına aralık (nextDue),
//     ilk tik hemen; sorgu → istek → otlp.ConvertMetrics → sink. Sorgu
//     hatası kaynağın LastError'ına yazılır, diğer kaynaklar etkilenmez.

func tfailSource() SourceConfig {
	s := validSource()
	s.ID = "i-aaaaaaaa"
	s.IntervalSec = 30
	return s
}

func recs(rows ...map[string]string) []Record {
	out := make([]Record, 0, len(rows))
	for _, r := range rows {
		out = append(out, Record{Values: r})
	}
	return out
}

func TestBuildMetricsRequest_Shape(t *testing.T) {
	src := tfailSource()
	qc := src.Queries[0]
	now := time.Date(2026, 9, 1, 10, 0, 30, 0, time.UTC)
	req, drops := BuildMetricsRequest(src, qc, recs(
		map[string]string{"_value": "42", "OPERATIONCODE": "OP1", "ERRORCODE": "E1", "_time": "2026-09-01T10:00:00Z"},
		map[string]string{"_value": "7", "OPERATIONCODE": "OP1", "ERRORCODE": "E2"},
	), now)
	if drops.Total() != 0 {
		t.Fatalf("no drops expected: %+v", drops)
	}
	if len(req.ResourceMetrics) != 1 {
		t.Fatalf("one resource, got %d", len(req.ResourceMetrics))
	}
	rm := req.ResourceMetrics[0]
	res := map[string]string{}
	for _, kv := range rm.Resource.Attributes {
		res[kv.Key] = kv.Value.GetStringValue()
	}
	if res["service.name"] != "ggfail" || res["coremetry.source"] != "influx" || res["influx.source.id"] != "i-aaaaaaaa" {
		t.Fatalf("resource attrs: %+v", res)
	}
	if len(rm.ScopeMetrics) != 1 || rm.ScopeMetrics[0].Scope == nil || rm.ScopeMetrics[0].Scope.Name != "coremetry/influx" {
		t.Fatalf("scope: %+v", rm.ScopeMetrics)
	}
	ms := rm.ScopeMetrics[0].Metrics
	if len(ms) != 1 || ms[0].Name != "ext:tfail_adet" {
		t.Fatalf("metric: %+v", ms)
	}
	g := ms[0].GetGauge()
	if g == nil || len(g.DataPoints) != 2 {
		t.Fatalf("gauge with 2 points expected: %+v", ms[0])
	}
	dp := g.DataPoints[0]
	if dp.GetAsDouble() != 42 || dp.TimeUnixNano != uint64(now.UnixNano()) {
		t.Fatalf("dp0 value/time: %+v", dp)
	}
	// attrMap ile ADLANDIRILMIŞ, groupBy SIRASIYLA.
	if len(dp.Attributes) != 2 || dp.Attributes[0].Key != "operation" || dp.Attributes[0].Value.GetStringValue() != "OP1" ||
		dp.Attributes[1].Key != "error.code" || dp.Attributes[1].Value.GetStringValue() != "E1" {
		t.Fatalf("dp attrs: %+v", dp.Attributes)
	}
}

func TestBuildMetricsRequest_Drops(t *testing.T) {
	src := tfailSource()
	qc := src.Queries[0]
	// attrMap'te olmayan groupBy tag'i ham adıyla gider.
	qc.GroupBy = []string{"OPERATIONCODE", "ERRORCODE", "KANALKOD"}
	now := time.Now()
	req, drops := BuildMetricsRequest(src, qc, recs(
		map[string]string{"_value": "1", "OPERATIONCODE": "OP1", "ERRORCODE": "E1", "KANALKOD": "MOBILE"},
		map[string]string{"_value": "x", "OPERATIONCODE": "OP1", "ERRORCODE": "E1", "KANALKOD": "MOBILE"}, // bad value
		map[string]string{"_value": "2", "OPERATIONCODE": "OP1", "ERRORCODE": "", "KANALKOD": "MOBILE"},   // empty tag
		map[string]string{"_value": "3", "OPERATIONCODE": "OP1", "KANALKOD": "MOBILE"},                    // missing tag
		map[string]string{"OPERATIONCODE": "OP1", "ERRORCODE": "E1", "KANALKOD": "MOBILE"},                // no _value
	), now)
	g := req.ResourceMetrics[0].ScopeMetrics[0].Metrics[0].GetGauge()
	if len(g.DataPoints) != 1 {
		t.Fatalf("only the clean row survives; got %d", len(g.DataPoints))
	}
	if g.DataPoints[0].Attributes[2].Key != "KANALKOD" {
		t.Fatalf("unmapped tag keeps its raw name: %+v", g.DataPoints[0].Attributes)
	}
	if drops.BadValue != 2 || drops.MissingTag != 2 || drops.OverCap != 0 {
		t.Fatalf("drop accounting: %+v", drops)
	}
}

func TestBuildMetricsRequest_Cap(t *testing.T) {
	src := tfailSource()
	qc := src.Queries[0]
	rows := make([]Record, 0, MaxRowsPerQuery+5)
	for i := 0; i < MaxRowsPerQuery+5; i++ {
		rows = append(rows, Record{Values: map[string]string{"_value": "1", "OPERATIONCODE": "OP", "ERRORCODE": "E"}})
	}
	req, drops := BuildMetricsRequest(src, qc, rows, time.Now())
	g := req.ResourceMetrics[0].ScopeMetrics[0].Metrics[0].GetGauge()
	if len(g.DataPoints) != MaxRowsPerQuery || drops.OverCap != 5 {
		t.Fatalf("cap: %d points, drops %+v", len(g.DataPoints), drops)
	}
}

// ── Worker ────────────────────────────────────────────────────────────────

type fakeQueryAPI struct {
	recs map[string][]Record // flux → rows
	err  error
	mu   sync.Mutex
	n    int
}

func (f *fakeQueryAPI) Query(_ context.Context, flux string) ([]Record, error) {
	f.mu.Lock()
	f.n++
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	for k, v := range f.recs {
		if strings.Contains(flux, k) {
			return v, nil
		}
	}
	return nil, nil
}

type fakeSink struct {
	mu  sync.Mutex
	pts []*chstore.MetricPoint
	err error
}

func (f *fakeSink) InsertMetrics(_ context.Context, pts []*chstore.MetricPoint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.pts = append(f.pts, pts...)
	return nil
}

func TestWorkerTick_PollsDueSourcesAndInserts(t *testing.T) {
	svc := New()
	src := tfailSource()
	svc.Configure(Settings{Sources: []SourceConfig{src}})
	q := &fakeQueryAPI{recs: map[string][]Record{"GGFailTraceBckt": recs(
		map[string]string{"_value": "42", "OPERATIONCODE": "OP1", "ERRORCODE": "E1"},
		map[string]string{"_value": "7", "OPERATIONCODE": "OP2", "ERRORCODE": "E1"},
	)}}
	sink := &fakeSink{}
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	w := NewWorker(svc, sink)
	w.now = func() time.Time { return now }
	w.queryAPIFor = func(SourceConfig) (QueryAPI, error) { return q, nil }

	w.Tick(context.Background())
	if q.n != 1 {
		t.Fatalf("first tick polls once, got %d", q.n)
	}
	if len(sink.pts) != 2 {
		t.Fatalf("2 metric points inserted, got %d", len(sink.pts))
	}
	p := sink.pts[0]
	if p.Metric != "ext:tfail_adet" || p.ServiceName != "ggfail" || p.Instrument != "gauge" || p.Value != 42 {
		t.Fatalf("point through otlp.ConvertMetrics: %+v", p)
	}
	if p.SeriesFingerprint == 0 {
		t.Fatalf("fingerprint must be set by the converter (exemplar pivot, D4)")
	}
	if !p.Time.Equal(now) {
		t.Fatalf("time = poll moment; got %v", p.Time)
	}
	// Aralık dolmadan ikinci tik: sorgu YOK.
	now = now.Add(10 * time.Second)
	w.Tick(context.Background())
	if q.n != 1 {
		t.Fatalf("not due yet → no poll, got %d", q.n)
	}
	now = now.Add(25 * time.Second)
	w.Tick(context.Background())
	if q.n != 2 {
		t.Fatalf("due after interval → second poll, got %d", q.n)
	}
	st := w.Status()
	if len(st) != 1 || st[0].SourceID != "i-aaaaaaaa" || st[0].LastPoints != 2 || st[0].LastError != "" {
		t.Fatalf("status: %+v", st)
	}
}

func TestWorkerTick_ErrorsAreIsolatedAndReported(t *testing.T) {
	svc := New()
	a := tfailSource()
	b := tfailSource()
	b.ID, b.Name = "i-bbbbbbbb", "other"
	svc.Configure(Settings{Sources: []SourceConfig{a, b}})
	sink := &fakeSink{}
	w := NewWorker(svc, sink)
	w.queryAPIFor = func(s SourceConfig) (QueryAPI, error) {
		if s.Name == "other" {
			return nil, errors.New("tokenRef env:NOPE: ortam değişkeni boş")
		}
		return &fakeQueryAPI{recs: map[string][]Record{"GGFailTraceBckt": recs(
			map[string]string{"_value": "1", "OPERATIONCODE": "OP", "ERRORCODE": "E"},
		)}}, nil
	}
	w.Tick(context.Background())
	if len(sink.pts) != 1 {
		t.Fatalf("healthy source still inserts: %d", len(sink.pts))
	}
	st := w.Status()
	var okSeen, errSeen bool
	for _, s := range st {
		if s.SourceID == "i-aaaaaaaa" && s.LastError == "" && s.LastPoints == 1 {
			okSeen = true
		}
		if s.SourceID == "i-bbbbbbbb" && strings.Contains(s.LastError, "NOPE") {
			errSeen = true
		}
	}
	if !okSeen || !errSeen {
		t.Fatalf("status must isolate per source: %+v", st)
	}

	// Kapalı kaynak hiç poll'lanmaz.
	c := tfailSource()
	c.ID, c.Name, c.Enabled = "i-cccccccc", "off", false
	svc.Configure(Settings{Sources: []SourceConfig{c}})
	w2 := NewWorker(svc, sink)
	polled := false
	w2.queryAPIFor = func(SourceConfig) (QueryAPI, error) { polled = true; return nil, nil }
	w2.Tick(context.Background())
	if polled {
		t.Fatalf("disabled source must not be polled")
	}
}

func TestWorkerTick_EmptyResultInsertsNothing(t *testing.T) {
	// Grubu olmayan sum() boş döner — poller SIFIR yazmaz (pad dedektörde, D3).
	svc := New()
	svc.Configure(Settings{Sources: []SourceConfig{tfailSource()}})
	sink := &fakeSink{}
	w := NewWorker(svc, sink)
	w.queryAPIFor = func(SourceConfig) (QueryAPI, error) { return &fakeQueryAPI{}, nil }
	w.Tick(context.Background())
	if len(sink.pts) != 0 {
		t.Fatalf("empty result → no rows, got %d", len(sink.pts))
	}
	if st := w.Status(); len(st) != 1 || st[0].LastError != "" || st[0].LastRows != 0 {
		t.Fatalf("empty is not an error: %+v", st)
	}
}
