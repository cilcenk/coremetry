package otlp

// v0.8.328 — OTLP metric exemplar extraction (cross-signal pivot Phase 1a).
// Before this release convertMetric silently DROPPED dp.Exemplars for every
// datapoint type (pivot-audit §2) — these fixtures are the first coverage in
// internal/otlp and pin:
//   - extraction from all 4 exemplar-bearing datapoint types
//     (Sum / Gauge / Histogram / ExponentialHistogram; Summary has no
//     exemplars field in the OTLP proto — correctly out of scope),
//   - the CONSISTENCY invariant: one payload → the metric row's
//     series_fingerprint == its exemplar rows' fingerprint (the join key),
//   - the Ingester's require-trace-context gate + atomic counters.

import (
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricscollpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	"github.com/cilcenk/coremetry/internal/chstore"
)

var (
	testTraceID = []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}
	testSpanID  = []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00, 0x11}
)

const (
	testTraceHex = "0102030405060708090a0b0c0d0e0f10"
	testSpanHex  = "aabbccddeeff0011"
	dpTimeNs     = uint64(1_700_000_100_000_000_000)
	exTimeNs     = uint64(1_700_000_099_500_000_000)
)

func exemplarFixture() *metricspb.Exemplar {
	return &metricspb.Exemplar{
		FilteredAttributes: []*commonpb.KeyValue{kvStr("pod", "p-1")},
		TimeUnixNano:       exTimeNs,
		Value:              &metricspb.Exemplar_AsDouble{AsDouble: 42.5},
		TraceId:            testTraceID,
		SpanId:             testSpanID,
	}
}

func numberDPFixture() *metricspb.NumberDataPoint {
	return &metricspb.NumberDataPoint{
		Attributes:   []*commonpb.KeyValue{kvStr("route", "/api/x")},
		TimeUnixNano: dpTimeNs,
		Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: 7},
		Exemplars:    []*metricspb.Exemplar{exemplarFixture()},
	}
}

// metricsRequest wraps the given metrics in one resource
// (service.name=checkout, service.instance.id=pod-1).
func metricsRequest(ms ...*metricspb.Metric) *metricscollpb.ExportMetricsServiceRequest {
	return &metricscollpb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{
					kvStr("service.name", "checkout"),
					kvStr("service.instance.id", "pod-1"),
				},
			},
			ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: ms}},
		}},
	}
}

func allTypesRequest() *metricscollpb.ExportMetricsServiceRequest {
	sum := &metricspb.Metric{Name: "app.requests", Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
		AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
		DataPoints:             []*metricspb.NumberDataPoint{numberDPFixture()},
	}}}
	gauge := &metricspb.Metric{Name: "app.inflight", Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
		DataPoints: []*metricspb.NumberDataPoint{numberDPFixture()},
	}}}
	sumV := 12.0
	hist := &metricspb.Metric{Name: "app.latency", Data: &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{
		AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
		DataPoints: []*metricspb.HistogramDataPoint{{
			Attributes:     []*commonpb.KeyValue{kvStr("route", "/api/x")},
			TimeUnixNano:   dpTimeNs,
			Count:          3,
			Sum:            &sumV,
			ExplicitBounds: []float64{0.1, 1},
			BucketCounts:   []uint64{1, 1, 1},
			Exemplars:      []*metricspb.Exemplar{exemplarFixture()},
		}},
	}}}
	expSumV := 9.0
	expHist := &metricspb.Metric{Name: "app.latency.exp", Data: &metricspb.Metric_ExponentialHistogram{ExponentialHistogram: &metricspb.ExponentialHistogram{
		DataPoints: []*metricspb.ExponentialHistogramDataPoint{{
			Attributes:   []*commonpb.KeyValue{kvStr("route", "/api/x")},
			TimeUnixNano: dpTimeNs,
			Count:        3,
			Sum:          &expSumV,
			Exemplars:    []*metricspb.Exemplar{exemplarFixture()},
		}},
	}}}
	return metricsRequest(sum, gauge, hist, expHist)
}

func TestConvertMetricsExemplarsAllDatapointTypes(t *testing.T) {
	pts, exs := ConvertMetrics(allTypesRequest())
	if len(pts) != 4 {
		t.Fatalf("want 4 metric points, got %d", len(pts))
	}
	if len(exs) != 4 {
		t.Fatalf("want 4 exemplar rows (sum/gauge/hist/exp_hist), got %d", len(exs))
	}
	byMetric := map[string]*chstore.ExemplarRow{}
	for _, ex := range exs {
		byMetric[ex.Metric] = ex
	}
	for _, m := range []string{"app.requests", "app.inflight", "app.latency", "app.latency.exp"} {
		ex, ok := byMetric[m]
		if !ok {
			t.Fatalf("no exemplar extracted for %s", m)
		}
		if ex.Service != "checkout" {
			t.Errorf("%s: service = %q, want checkout", m, ex.Service)
		}
		if ex.TraceID != testTraceHex || ex.SpanID != testSpanHex {
			t.Errorf("%s: ids = %q/%q, want %q/%q", m, ex.TraceID, ex.SpanID, testTraceHex, testSpanHex)
		}
		if ex.Value != 42.5 {
			t.Errorf("%s: value = %v, want 42.5", m, ex.Value)
		}
		if got := ex.Time.UnixNano(); got != int64(exTimeNs) {
			t.Errorf("%s: time = %d, want %d", m, got, exTimeNs)
		}
		if ex.FilteredAttrs["pod"] != "p-1" {
			t.Errorf("%s: filtered attrs = %v, want pod=p-1", m, ex.FilteredAttrs)
		}
		if ex.Fingerprint == 0 {
			t.Errorf("%s: fingerprint is 0", m)
		}
	}
}

// The join-key invariant: the exemplar row must carry EXACTLY the fingerprint
// stored on its datapoint's metric_points row, or the metric→trace pivot
// finds nothing.
func TestConvertMetricsFingerprintConsistency(t *testing.T) {
	pts, exs := ConvertMetrics(allTypesRequest())
	fpByMetric := map[string]uint64{}
	for _, p := range pts {
		if p.SeriesFingerprint == 0 {
			t.Errorf("%s: metric row fingerprint is 0", p.Metric)
		}
		fpByMetric[p.Metric] = p.SeriesFingerprint
	}
	for _, ex := range exs {
		if want := fpByMetric[ex.Metric]; ex.Fingerprint != want {
			t.Errorf("%s: exemplar fp %x != metric row fp %x", ex.Metric, ex.Fingerprint, want)
		}
	}
	// And the fingerprint matches an independent SeriesFingerprint call over
	// the same inputs (metric name + dp attrs + resource identity).
	want := SeriesFingerprint("app.requests",
		[]*commonpb.KeyValue{kvStr("route", "/api/x")}, "checkout", "pod-1")
	if got := fpByMetric["app.requests"]; got != want {
		t.Errorf("convertMetric fp %x != SeriesFingerprint %x", got, want)
	}
}

func TestConvertMetricsSummaryHasNoExemplars(t *testing.T) {
	summary := &metricspb.Metric{Name: "app.summary", Data: &metricspb.Metric_Summary{Summary: &metricspb.Summary{
		DataPoints: []*metricspb.SummaryDataPoint{{
			TimeUnixNano: dpTimeNs, Count: 2, Sum: 10,
		}},
	}}}
	pts, exs := ConvertMetrics(metricsRequest(summary))
	if len(pts) != 1 {
		t.Fatalf("want 1 summary point, got %d", len(pts))
	}
	if len(exs) != 0 {
		t.Fatalf("summary datapoints carry no exemplars in the OTLP proto; got %d rows", len(exs))
	}
	if pts[0].SeriesFingerprint == 0 {
		t.Errorf("summary point still gets a series fingerprint (identity is type-independent)")
	}
}

// ConvertMetrics itself keeps trace-less exemplars — the require-trace-context
// gate (and its counters) lives on the Ingester so HTTP and gRPC share it.
func TestConvertMetricsKeepsTracelessExemplars(t *testing.T) {
	dp := numberDPFixture()
	dp.Exemplars[0].TraceId = nil
	dp.Exemplars[0].SpanId = nil
	m := &metricspb.Metric{Name: "m", Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{dp}}}}
	_, exs := ConvertMetrics(metricsRequest(m))
	if len(exs) != 1 {
		t.Fatalf("want the traceless exemplar extracted, got %d rows", len(exs))
	}
	if exs[0].TraceID != "" || exs[0].SpanID != "" {
		t.Fatalf("nil ids must convert to empty strings, got %q/%q", exs[0].TraceID, exs[0].SpanID)
	}
}

// OTel "no trace" is nil bytes OR all-zero bytes (same disagreement between
// SDKs that parentID handles for spans) — both must gate identically.
func TestConvertMetricsAllZeroTraceIDIsEmpty(t *testing.T) {
	dp := numberDPFixture()
	dp.Exemplars[0].TraceId = make([]byte, 16)
	dp.Exemplars[0].SpanId = make([]byte, 8)
	m := &metricspb.Metric{Name: "m", Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{dp}}}}
	_, exs := ConvertMetrics(metricsRequest(m))
	if len(exs) != 1 {
		t.Fatalf("want 1 exemplar row, got %d", len(exs))
	}
	if exs[0].TraceID != "" || exs[0].SpanID != "" {
		t.Fatalf("all-zero ids must convert to empty strings, got %q/%q", exs[0].TraceID, exs[0].SpanID)
	}
}

func TestConvertMetricsExemplarIntValue(t *testing.T) {
	dp := numberDPFixture()
	dp.Exemplars[0].Value = &metricspb.Exemplar_AsInt{AsInt: 9}
	m := &metricspb.Metric{Name: "m", Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{DataPoints: []*metricspb.NumberDataPoint{dp}}}}
	_, exs := ConvertMetrics(metricsRequest(m))
	if len(exs) != 1 || exs[0].Value != 9 {
		t.Fatalf("int exemplar value not converted: %+v", exs)
	}
}

// A zero exemplar timestamp anchors to the datapoint's timestamp — a 1970 row
// would sit outside every query window and be unreachable forever.
func TestConvertMetricsExemplarZeroTimeFallsBackToDatapoint(t *testing.T) {
	dp := numberDPFixture()
	dp.Exemplars[0].TimeUnixNano = 0
	m := &metricspb.Metric{Name: "m", Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{dp}}}}
	_, exs := ConvertMetrics(metricsRequest(m))
	if len(exs) != 1 {
		t.Fatalf("want 1 exemplar row, got %d", len(exs))
	}
	if got := exs[0].Time.UnixNano(); got != int64(dpTimeNs) {
		t.Fatalf("zero exemplar ts must fall back to dp ts %d, got %d", dpTimeNs, got)
	}
}

// ── Ingester gate ─────────────────────────────────────────────────────────────

func exemplarRow(traceID string) *chstore.ExemplarRow {
	return &chstore.ExemplarRow{Fingerprint: 1, Metric: "m", Service: "svc",
		Time: time.Unix(0, int64(dpTimeNs)).UTC(), Value: 1, TraceID: traceID}
}

// Default policy (require_trace_context=true — the config zero value the
// Ingester must ALSO default to): traceless exemplars are accept-but-discard
// with the drop counter bumped; traced ones count as ingested. A nil
// exemplar consumer (api-only pods) must never panic.
func TestIngesterExemplarGateDefaultRequiresTrace(t *testing.T) {
	ing := &Ingester{}
	if ok := ing.addExemplar(exemplarRow("")); !ok {
		t.Fatalf("gate drop must be accept-but-discard (true), got false")
	}
	if got := ing.ExemplarsDroppedNoTrace(); got != 1 {
		t.Fatalf("droppedNoTrace = %d, want 1", got)
	}
	if got := ing.ExemplarsIngested(); got != 0 {
		t.Fatalf("ingested = %d, want 0", got)
	}
	if ok := ing.addExemplar(exemplarRow(testTraceHex)); !ok {
		t.Fatalf("traced exemplar with nil consumer must be accepted (no-op), got false")
	}
	if got := ing.ExemplarsIngested(); got != 1 {
		t.Fatalf("ingested = %d, want 1", got)
	}
	if got := ing.ExemplarsDroppedNoTrace(); got != 1 {
		t.Fatalf("droppedNoTrace moved to %d, want 1", got)
	}
}

func TestIngesterExemplarGateDisabledKeepsTraceless(t *testing.T) {
	ing := &Ingester{}
	ing.SetExemplarPolicy(false) // operator: exemplars.require_trace_context=false
	if ok := ing.addExemplar(exemplarRow("")); !ok {
		t.Fatalf("policy-off traceless exemplar must be accepted, got false")
	}
	if got := ing.ExemplarsIngested(); got != 1 {
		t.Fatalf("ingested = %d, want 1", got)
	}
	if got := ing.ExemplarsDroppedNoTrace(); got != 0 {
		t.Fatalf("droppedNoTrace = %d, want 0", got)
	}
}
