package otlp

// v0.10.373 — the VictoriaMetrics forward honours pipeline metric DROP
// rules. Known gap of v0.10.367 (INCIDENTS): the exclusion rules'
// "ingest'te düşür" checkbox reached ClickHouse through the pipeline
// bridge and never the raw forward.

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"testing"

	metricscollpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/pipeline"
)

func kv(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func numDP(route string, v float64) *metricspb.NumberDataPoint {
	return &metricspb.NumberDataPoint{TimeUnixNano: 1_700_000_000_000_000_000,
		Attributes: []*commonpb.KeyValue{kv("http.route", route)},
		Value:      &metricspb.NumberDataPoint_AsDouble{AsDouble: v}}
}

func histDP(route string) *metricspb.HistogramDataPoint {
	return &metricspb.HistogramDataPoint{TimeUnixNano: 1_700_000_000_000_000_000,
		Attributes: []*commonpb.KeyValue{kv("http.route", route)}, Count: 3}
}

// probeReq — one resource (service svc-a), two metrics: a sum with a
// probe + a business route, a histogram with only probe routes, and a
// second resource whose metric name the rule does not cover.
func probeReq() *metricscollpb.ExportMetricsServiceRequest {
	return &metricscollpb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{
			{
				Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kv("service.name", "svc-a")}},
				ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{
					{Name: "http.server.request.duration", Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{DataPoints: []*metricspb.NumberDataPoint{
						numDP("/BSAWEB/bsa/core/server/checkLiveness", 1), numDP("/BSAWEB/loan/assessment", 2),
					}}}},
					{Name: "http.server.request.duration", Data: &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{DataPoints: []*metricspb.HistogramDataPoint{
						histDP("/BSAWEB/bsa/core/server/checkReadiness"), histDP("/BSAWEB/bsa/core/server/checkStartup"),
					}}}},
				}}},
			},
			{
				Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kv("service.name", "svc-b")}},
				ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{
					{Name: "jvm.memory.used", Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{
						numDP("/BSAWEB/bsa/core/server/checkLiveness", 9),
					}}}},
				}}},
			},
		},
	}
}

func probeDrop(m *chstore.MetricPoint) bool {
	if m.Metric != "http.server.request.duration" {
		return false
	}
	for i, k := range m.AttrKeys {
		if k == "http.route" && len(m.AttrValues[i]) > 0 && (m.AttrValues[i] == "/BSAWEB/bsa/core/server/checkLiveness" ||
			m.AttrValues[i] == "/BSAWEB/bsa/core/server/checkReadiness" || m.AttrValues[i] == "/BSAWEB/bsa/core/server/checkStartup") {
			return true
		}
	}
	return false
}

func TestDropForwardPointsPrunesAcrossShapes(t *testing.T) {
	req := probeReq()
	var seen []string
	n := dropForwardPoints(req, func(m *chstore.MetricPoint) bool {
		seen = append(seen, m.ServiceName+"|"+m.Metric+"|"+m.Instrument)
		return probeDrop(m)
	})
	if n != 3 {
		t.Fatalf("dropped = %d, want 3 (1 sum probe + 2 histogram probes)", n)
	}
	if len(req.ResourceMetrics) != 2 {
		t.Fatalf("both resources keep a survivor: %d", len(req.ResourceMetrics))
	}
	ms := req.ResourceMetrics[0].ScopeMetrics[0].Metrics
	if len(ms) != 1 || ms[0].GetSum() == nil || len(ms[0].GetSum().DataPoints) != 1 {
		t.Fatalf("empty histogram metric must be pruned, sum keeps the business route: %+v", ms)
	}
	if got := ms[0].GetSum().DataPoints[0].Attributes[0].Value.GetStringValue(); got != "/BSAWEB/loan/assessment" {
		t.Fatalf("survivor = %s", got)
	}
	// The view carries what the ClickHouse converter carries.
	want := map[string]bool{"svc-a|http.server.request.duration|sum": true, "svc-a|http.server.request.duration|histogram": true, "svc-b|jvm.memory.used|gauge": true}
	for _, s := range seen {
		if !want[s] {
			t.Fatalf("unexpected view %q", s)
		}
	}
}

func TestDropForwardPointsAllDroppedEmptiesRequest(t *testing.T) {
	req := probeReq()
	n := dropForwardPoints(req, func(*chstore.MetricPoint) bool { return true })
	if n != 5 || len(req.ResourceMetrics) != 0 {
		t.Fatalf("dropped=%d resources=%d, want 5 / 0", n, len(req.ResourceMetrics))
	}
	if dropForwardPoints(probeReq(), nil) != 0 {
		t.Fatal("nil predicate drops nothing")
	}
}

func TestDropForwardPointsZeroDropsLeavesRequestEqual(t *testing.T) {
	req := probeReq()
	before := proto.Clone(req)
	if n := dropForwardPoints(req, func(*chstore.MetricPoint) bool { return false }); n != 0 {
		t.Fatalf("dropped = %d", n)
	}
	if !proto.Equal(before, req) {
		t.Fatal("no drop must leave the request byte-equal")
	}
}

// fakeRuleStore — LoadPersisted's seam, fed the exclusion bridge's rule.
type fakeRuleStore struct{ raw []byte }

func (f fakeRuleStore) GetPipelineRulesRaw(context.Context) ([]byte, error) { return f.raw, nil }
func (f fakeRuleStore) PutPipelineRulesRaw(context.Context, []byte) error   { return nil }

func probeEngine(t *testing.T) *pipeline.Engine {
	t.Helper()
	raw, err := json.Marshal([]pipeline.Rule{{
		ID: "metric-excl-test", Name: "metric-excl: * ~ probes", Kind: pipeline.KindDrop, Signal: pipeline.SignalMetrics, Enabled: true,
		When: pipeline.Condition{Key: chstore.MetricExclusionAttrKey, Op: pipeline.OpMatches, Value: "check(Liveness|Readiness|Startup)"},
		And:  []pipeline.Condition{{Key: "metric", Op: pipeline.OpEq, Value: "http.server.request.duration"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	e := pipeline.New()
	if err := e.LoadPersisted(context.Background(), fakeRuleStore{raw}); err != nil {
		t.Fatal(err)
	}
	return e
}

func gzipOf(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decodeForward(t *testing.T, b MetricBody) *metricscollpb.ExportMetricsServiceRequest {
	t.Helper()
	if b.Gzipped {
		t.Fatal("a filtered forward must be re-marshalled protobuf, not the raw gzip bytes")
	}
	var back metricscollpb.ExportMetricsServiceRequest
	if err := proto.Unmarshal(b.Body, &back); err != nil {
		t.Fatal(err)
	}
	return &back
}

func assertProbesGone(t *testing.T, req *metricscollpb.ExportMetricsServiceRequest) {
	t.Helper()
	pts := 0
	for _, rm := range req.ResourceMetrics {
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if s := m.GetSum(); s != nil {
					for _, dp := range s.DataPoints {
						pts++
						if r := dp.Attributes[0].Value.GetStringValue(); r != "/BSAWEB/loan/assessment" {
							t.Fatalf("probe route forwarded: %s", r)
						}
					}
				}
				if m.GetHistogram() != nil {
					t.Fatal("all-probe histogram must not be forwarded")
				}
				if g := m.GetGauge(); g != nil {
					pts += len(g.DataPoints) // jvm.memory.used — rule does not cover it
				}
			}
		}
	}
	if pts != 2 {
		t.Fatalf("forwarded points = %d, want 2 (business sum + uncovered gauge)", pts)
	}
}

func TestForwardHTTPGzipAppliesDropRules(t *testing.T) {
	ing, got := testIngester(t)
	ing.SetPipeline(probeEngine(t))
	pb, _ := proto.Marshal(probeReq())
	if w := post(t, ing, gzipOf(t, pb), "application/x-protobuf", "gzip"); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if len(*got) != 1 {
		t.Fatalf("forwards = %d", len(*got))
	}
	assertProbesGone(t, decodeForward(t, (*got)[0]))
	if ing.MetricForwardFiltered() != 3 {
		t.Fatalf("filtered counter = %d, want 3", ing.MetricForwardFiltered())
	}
}

func TestForwardGRPCAppliesDropRules(t *testing.T) {
	ing, got := testIngester(t)
	ing.SetPipeline(probeEngine(t))
	if _, err := (&metricsGRPC{ing: ing}).Export(context.Background(), probeReq()); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("forwards = %d", len(*got))
	}
	assertProbesGone(t, decodeForward(t, (*got)[0]))
}

func TestForwardAllDroppedSendsNothing(t *testing.T) {
	ing, got := testIngester(t)
	ing.SetPipeline(probeEngine(t))
	req := &metricscollpb.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{
			{Name: "http.server.request.duration", Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{DataPoints: []*metricspb.NumberDataPoint{
				numDP("/BSAWEB/bsa/core/server/checkStartup", 1),
			}}}},
		}}}}}}
	pb, _ := proto.Marshal(req)
	post(t, ing, pb, "application/x-protobuf", "")
	if len(*got) != 0 {
		t.Fatalf("an all-dropped export must not reach VictoriaMetrics: %d forwards", len(*got))
	}
}

func TestForwardWithoutDropRulesStaysRaw(t *testing.T) {
	ing, got := testIngester(t)
	ing.SetPipeline(pipeline.New()) // engine present, no rules
	pb, _ := proto.Marshal(probeReq())
	gz := gzipOf(t, pb)
	post(t, ing, gz, "application/x-protobuf", "gzip")
	if len(*got) != 1 || !(*got)[0].Gzipped || !bytes.Equal((*got)[0].Body, gz) {
		t.Fatal("no drop rule → raw gzip bytes byte-identical (v0.10.293 contract)")
	}
}
