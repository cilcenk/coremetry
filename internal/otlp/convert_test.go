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
	tracecollpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

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

// ── Span links (v0.8.329, cross-signal pivot Phase 1b) ───────────────────────
//
// Before v0.8.329 convertSpan silently DROPPED sp.Links (pivot-audit §2).
// These fixtures pin:
//   - link rows carry the OWNING span's identity (trace/span id, start time,
//     service) — the forward pivot key of the span_links table,
//   - an all-zero linked trace id collapses to "" in conversion (parentID,
//     the same nil-vs-zero-bytes SDK disagreement spans handle) so the
//     Ingester's invalid gate sees both encodings identically,
//   - the Ingester gate: invalid rows are dropped+counted, valid rows count
//     as ingested; a nil span-link consumer (api-only pods) never panics.

var (
	linkedTraceID = []byte{0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28, 0x29, 0x2a, 0x2b, 0x2c, 0x2d, 0x2e, 0x2f, 0x30}
	linkedSpanID  = []byte{0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38}
)

const (
	linkedTraceHex = "2122232425262728292a2b2c2d2e2f30"
	linkedSpanHex  = "3132333435363738"
	spanStartNs    = uint64(1_700_000_200_000_000_000)
)

// linkedSpanFixture is one span with 2 links: one valid (with attributes) and
// one whose linked trace id is all-zero bytes (the OTel "no trace" encoding
// some SDKs emit) — the invalid one the ingest gate must drop.
func linkedSpanFixture() *tracepb.Span {
	return &tracepb.Span{
		TraceId:           testTraceID,
		SpanId:            testSpanID,
		Name:              "checkout.process",
		StartTimeUnixNano: spanStartNs,
		EndTimeUnixNano:   spanStartNs + 1_000_000,
		Links: []*tracepb.Span_Link{
			{
				TraceId:    linkedTraceID,
				SpanId:     linkedSpanID,
				Attributes: []*commonpb.KeyValue{kvStr("link.kind", "follows_from")},
			},
			{
				TraceId: make([]byte, 16), // all-zero = no trace context
				SpanId:  make([]byte, 8),
			},
		},
	}
}

func tracesRequest(spans ...*tracepb.Span) *tracecollpb.ExportTraceServiceRequest {
	return &tracecollpb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{kvStr("service.name", "checkout")},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}},
		}},
	}
}

func TestConvertTracesSpanLinks(t *testing.T) {
	spans, links := ConvertTraces(tracesRequest(linkedSpanFixture()))
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(spans))
	}
	// Conversion is pure: BOTH links come out (the all-zero one with its
	// linked ids collapsed to "") — the drop happens at the Ingester gate
	// where the counters live, so HTTP + gRPC share one policy.
	if len(links) != 2 {
		t.Fatalf("want 2 link rows, got %d", len(links))
	}

	// Owning-span identity — the forward pivot key. Every link row must carry
	// the trace/span id of the span that DECLARED the link, its start time,
	// and its resource's service.
	for i, ln := range links {
		if ln.TraceID != testTraceHex || ln.SpanID != testSpanHex {
			t.Errorf("link %d: owning ids = %q/%q, want %q/%q", i, ln.TraceID, ln.SpanID, testTraceHex, testSpanHex)
		}
		if got := ln.Time.UnixNano(); got != int64(spanStartNs) {
			t.Errorf("link %d: time = %d, want owning span start %d", i, got, spanStartNs)
		}
		if ln.ServiceName != "checkout" {
			t.Errorf("link %d: service = %q, want checkout", i, ln.ServiceName)
		}
	}

	// Link 1: valid linked ids + attributes preserved as arrays.
	if links[0].LinkedTraceID != linkedTraceHex || links[0].LinkedSpanID != linkedSpanHex {
		t.Errorf("linked ids = %q/%q, want %q/%q", links[0].LinkedTraceID, links[0].LinkedSpanID, linkedTraceHex, linkedSpanHex)
	}
	if len(links[0].AttrKeys) != 1 || links[0].AttrKeys[0] != "link.kind" ||
		len(links[0].AttrVals) != 1 || links[0].AttrVals[0] != "follows_from" {
		t.Errorf("link attrs = %v/%v, want [link.kind]/[follows_from]", links[0].AttrKeys, links[0].AttrVals)
	}

	// Link 2: all-zero linked ids collapse to "" (parentID semantics).
	if links[1].LinkedTraceID != "" || links[1].LinkedSpanID != "" {
		t.Errorf("all-zero linked ids must convert to empty strings, got %q/%q", links[1].LinkedTraceID, links[1].LinkedSpanID)
	}
}

func TestConvertTracesNoLinks(t *testing.T) {
	sp := linkedSpanFixture()
	sp.Links = nil
	spans, links := ConvertTraces(tracesRequest(sp))
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(spans))
	}
	if len(links) != 0 {
		t.Fatalf("want 0 link rows for a link-less span, got %d", len(links))
	}
}

// The Ingester's span-link gate: an empty linked trace id (was all-zero or
// nil on the wire) is a link pointing NOWHERE — dropped + counted as invalid,
// accept-but-discard like the exemplar/pipeline gates. Valid links count as
// ingested; a nil consumer (api-only pods) must never panic.
func TestIngesterSpanLinkGateDropsInvalid(t *testing.T) {
	ing := &Ingester{}
	if ok := ing.addSpanLink(&chstore.SpanLinkRow{TraceID: testTraceHex, SpanID: testSpanHex, LinkedTraceID: ""}); !ok {
		t.Fatalf("invalid-link drop must be accept-but-discard (true), got false")
	}
	if got := ing.SpanLinksDroppedInvalid(); got != 1 {
		t.Fatalf("droppedInvalid = %d, want 1", got)
	}
	if got := ing.SpanLinksIngested(); got != 0 {
		t.Fatalf("ingested = %d, want 0", got)
	}
	if ok := ing.addSpanLink(&chstore.SpanLinkRow{TraceID: testTraceHex, SpanID: testSpanHex, LinkedTraceID: linkedTraceHex}); !ok {
		t.Fatalf("valid link with nil consumer must be accepted (no-op), got false")
	}
	if got := ing.SpanLinksIngested(); got != 1 {
		t.Fatalf("ingested = %d, want 1", got)
	}
	if got := ing.SpanLinksDroppedInvalid(); got != 1 {
		t.Fatalf("droppedInvalid moved to %d, want 1", got)
	}
}

// v0.8.379 — operator-reported: the test environments emit the CURRENT
// semconv key deployment.environment.name (≥1.27 renamed it), while
// ingest read only the legacy deployment.environment — deploy_env
// stayed empty and every env facet/filter/groupBy built on the typed
// column showed nothing. Multi-name fallback chain per the v0.5.471
// cluster precedent; new key wins when both are present.
func TestConvertTracesDeployEnvSemconvFallback(t *testing.T) {
	mk := func(attrs ...*commonpb.KeyValue) *tracecollpb.ExportTraceServiceRequest {
		return &tracecollpb.ExportTraceServiceRequest{
			ResourceSpans: []*tracepb.ResourceSpans{{
				Resource: &resourcepb.Resource{Attributes: append([]*commonpb.KeyValue{
					kvStr("service.name", "mobile-bff"),
				}, attrs...)},
				ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
					TraceId: make([]byte, 16), SpanId: make([]byte, 8), Name: "op",
				}}}},
			}},
		}
	}
	cases := []struct {
		name string
		req  *tracecollpb.ExportTraceServiceRequest
		want string
	}{
		{"new key only", mk(kvStr("deployment.environment.name", "uat")), "uat"},
		{"legacy key only", mk(kvStr("deployment.environment", "prep")), "prep"},
		{"both present — new wins", mk(
			kvStr("deployment.environment.name", "int"),
			kvStr("deployment.environment", "legacy")), "int"},
		{"neither", mk(), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spans, _ := ConvertTraces(c.req)
			if len(spans) != 1 {
				t.Fatalf("want 1 span, got %d", len(spans))
			}
			if spans[0].DeployEnv != c.want {
				t.Fatalf("DeployEnv = %q, want %q", spans[0].DeployEnv, c.want)
			}
		})
	}
}
