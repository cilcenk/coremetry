package otlp

// v0.8.345 — OTLP backpressure honesty (HA audit H5). Pins the receiver's
// overload contract on BOTH transports for all three signals:
//
//   - full accept        → success, NO PartialSuccess (happy path unchanged),
//   - partial accept     → success + PartialSuccess{rejected_*} (OTLP spec
//     §"Partial Success") — never an error, because an error makes the SDK
//     retry the WHOLE batch and the accepted items land a second time in the
//     non-deduplicating tables (double-counted rates in every MV),
//   - total reject       → gRPC ResourceExhausted + RetryInfo(2s) (OTLP spec
//     §"OTLP/gRPC Throttling") / HTTP 429 + Retry-After: 2 (§"OTLP/HTTP
//     Throttling") — duplicate-safe (nothing accepted) and back-off inducing,
//   - derived side-signals (exemplars, span links) — their buffer drops must
//     NOT reject the parent batch; they only tick the consumer drop counters.
//
// Consumers are constructed with tiny buffers and never Start()ed: New()
// creates the channel, Start() only spawns the drain loop, so Add() fills
// the buffer then fails deterministically — no goroutines, no timing.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	logscollpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricscollpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracecollpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/consumer"
)

// bpIngester builds an Ingester over real consumers with the given buffer
// capacities, deliberately NOT started (see file header). BufferSize 0 =
// unbuffered channel with no reader = every Add fails (total reject).
func bpIngester(spanBuf, logBuf, metricBuf int) *Ingester {
	return NewIngester(
		consumer.New[*chstore.Span]("spans", consumer.Options{BufferSize: spanBuf}, nil),
		consumer.New[*chstore.Log]("logs", consumer.Options{BufferSize: logBuf}, nil),
		consumer.New[*chstore.MetricPoint]("metrics", consumer.Options{BufferSize: metricBuf}, nil),
	)
}

func plainSpansRequest(n int) *tracecollpb.ExportTraceServiceRequest {
	spans := make([]*tracepb.Span, n)
	for i := range spans {
		spans[i] = &tracepb.Span{
			TraceId: testTraceID, SpanId: []byte{byte(i + 1), 2, 3, 4, 5, 6, 7, 8},
			Name:              "op",
			StartTimeUnixNano: spanStartNs, EndTimeUnixNano: spanStartNs + 1_000_000,
		}
	}
	return tracesRequest(spans...)
}

func logRecordsRequest(n int) *logscollpb.ExportLogsServiceRequest {
	recs := make([]*logspb.LogRecord, n)
	for i := range recs {
		recs[i] = &logspb.LogRecord{
			TimeUnixNano: dpTimeNs,
			Body:         &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "log line"}},
		}
	}
	return &logscollpb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource:  &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kvStr("service.name", "checkout")}},
			ScopeLogs: []*logspb.ScopeLogs{{LogRecords: recs}},
		}},
	}
}

// gaugePointsRequest yields exactly n metric points and zero exemplars.
func gaugePointsRequest(n int) *metricscollpb.ExportMetricsServiceRequest {
	dps := make([]*metricspb.NumberDataPoint, n)
	for i := range dps {
		dps[i] = &metricspb.NumberDataPoint{
			TimeUnixNano: dpTimeNs,
			Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: float64(i)},
		}
	}
	return metricsRequest(&metricspb.Metric{Name: "app.gauge",
		Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: dps}}})
}

// backpressureCase is the shared table shape: send `send` items into a
// signal buffer of `buf`; wantRejected is the exact PartialSuccess count
// (0 with wantThrottle=false = clean accept, empty response).
type backpressureCase struct {
	name         string
	buf, send    int
	wantRejected int64
	wantThrottle bool
}

var backpressureCases = []backpressureCase{
	{"full accept", 4, 2, 0, false},
	{"empty batch", 0, 0, 0, false}, // 0 items ≠ "all rejected" — must stay OK
	{"partial accept", 2, 5, 3, false},
	{"total reject", 0, 3, 0, true},
}

// assertGRPCThrottle checks the fully-rejected contract: ResourceExhausted
// carrying a RetryInfo detail with the 2s delay — without RetryInfo, spec-
// compliant OTLP clients treat RESOURCE_EXHAUSTED as NON-retryable.
func assertGRPCThrottle(t *testing.T, err error) {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.ResourceExhausted {
		t.Fatalf("total reject: want ResourceExhausted status, got %v", err)
	}
	for _, d := range st.Details() {
		if ri, ok := d.(*errdetails.RetryInfo); ok {
			if got := ri.RetryDelay.AsDuration(); got != throttleRetryDelay {
				t.Fatalf("RetryInfo delay = %s, want %s", got, throttleRetryDelay)
			}
			return
		}
	}
	t.Fatalf("ResourceExhausted status missing RetryInfo detail (details: %v)", st.Details())
}

// ── gRPC ──────────────────────────────────────────────────────────────────────

func TestGRPCTracesBackpressure(t *testing.T) {
	for _, tc := range backpressureCases {
		t.Run(tc.name, func(t *testing.T) {
			ing := bpIngester(tc.buf, 4, 4)
			resp, err := (&traceGRPC{ing: ing}).Export(context.Background(), plainSpansRequest(tc.send))
			if tc.wantThrottle {
				assertGRPCThrottle(t, err)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			ps := resp.GetPartialSuccess()
			if tc.wantRejected == 0 {
				if ps != nil {
					t.Fatalf("clean accept must not carry PartialSuccess, got %+v", ps)
				}
				return
			}
			if ps == nil {
				t.Fatalf("partial accept must carry PartialSuccess")
			}
			if ps.RejectedSpans != tc.wantRejected {
				t.Fatalf("RejectedSpans = %d, want %d", ps.RejectedSpans, tc.wantRejected)
			}
			if ps.ErrorMessage == "" {
				t.Fatalf("PartialSuccess.ErrorMessage must explain the rejection")
			}
		})
	}
}

func TestGRPCLogsBackpressure(t *testing.T) {
	for _, tc := range backpressureCases {
		t.Run(tc.name, func(t *testing.T) {
			ing := bpIngester(4, tc.buf, 4)
			resp, err := (&logsGRPC{ing: ing}).Export(context.Background(), logRecordsRequest(tc.send))
			if tc.wantThrottle {
				assertGRPCThrottle(t, err)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			ps := resp.GetPartialSuccess()
			if tc.wantRejected == 0 {
				if ps != nil {
					t.Fatalf("clean accept must not carry PartialSuccess, got %+v", ps)
				}
				return
			}
			if ps == nil {
				t.Fatalf("partial accept must carry PartialSuccess")
			}
			if ps.RejectedLogRecords != tc.wantRejected {
				t.Fatalf("RejectedLogRecords = %d, want %d", ps.RejectedLogRecords, tc.wantRejected)
			}
			if ps.ErrorMessage == "" {
				t.Fatalf("PartialSuccess.ErrorMessage must explain the rejection")
			}
		})
	}
}

func TestGRPCMetricsBackpressure(t *testing.T) {
	for _, tc := range backpressureCases {
		t.Run(tc.name, func(t *testing.T) {
			ing := bpIngester(4, 4, tc.buf)
			resp, err := (&metricsGRPC{ing: ing}).Export(context.Background(), gaugePointsRequest(tc.send))
			if tc.wantThrottle {
				assertGRPCThrottle(t, err)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			ps := resp.GetPartialSuccess()
			if tc.wantRejected == 0 {
				if ps != nil {
					t.Fatalf("clean accept must not carry PartialSuccess, got %+v", ps)
				}
				return
			}
			if ps == nil {
				t.Fatalf("partial accept must carry PartialSuccess")
			}
			if ps.RejectedDataPoints != tc.wantRejected {
				t.Fatalf("RejectedDataPoints = %d, want %d", ps.RejectedDataPoints, tc.wantRejected)
			}
			if ps.ErrorMessage == "" {
				t.Fatalf("PartialSuccess.ErrorMessage must explain the rejection")
			}
		})
	}
}

// Span links are a DERIVED side-signal: a full span_links buffer must not
// reject a batch whose SPANS were all accepted — the drop only ticks the
// link consumer's counter. (A rejection here would trigger a client retry
// that re-writes the accepted spans.)
func TestGRPCTraceLinkDropsDoNotRejectBatch(t *testing.T) {
	ing := bpIngester(8, 4, 4)
	ing.SetSpanLinks(consumer.New[*chstore.SpanLinkRow]("span_links", consumer.Options{BufferSize: 0}, nil))
	resp, err := (&traceGRPC{ing: ing}).Export(context.Background(), tracesRequest(linkedSpanFixture()))
	if err != nil {
		t.Fatalf("link drops must not fail the batch: %v", err)
	}
	if resp.GetPartialSuccess() != nil {
		t.Fatalf("link drops must not surface as PartialSuccess (spans were accepted), got %+v", resp.GetPartialSuccess())
	}
	// linkedSpanFixture: 2 links — 1 invalid (gate-dropped, not a buffer
	// drop) + 1 valid that hits the full consumer.
	if got := ing.SpanLinks.Dropped(); got != 1 {
		t.Fatalf("span_links consumer Dropped = %d, want 1", got)
	}
}

// Exemplars mirror the span-link decision: derived from ACCEPTED datapoints,
// so their buffer drops ride the exemplar consumer's counter only.
func TestGRPCMetricExemplarDropsDoNotRejectBatch(t *testing.T) {
	ing := bpIngester(4, 4, 8)
	ing.SetExemplars(consumer.New[*chstore.ExemplarRow]("exemplars", consumer.Options{BufferSize: 0}, nil))
	req := metricsRequest(&metricspb.Metric{Name: "app.requests",
		Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
			AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
			DataPoints:             []*metricspb.NumberDataPoint{numberDPFixture()},
		}}})
	resp, err := (&metricsGRPC{ing: ing}).Export(context.Background(), req)
	if err != nil {
		t.Fatalf("exemplar drops must not fail the batch: %v", err)
	}
	if resp.GetPartialSuccess() != nil {
		t.Fatalf("exemplar drops must not surface as PartialSuccess (points were accepted), got %+v", resp.GetPartialSuccess())
	}
	if got := ing.Exemplars.Dropped(); got != 1 {
		t.Fatalf("exemplars consumer Dropped = %d, want 1", got)
	}
}

// ── HTTP ──────────────────────────────────────────────────────────────────────

func postProto(t *testing.T, ing *Ingester, path string, req proto.Message) *httptest.ResponseRecorder {
	t.Helper()
	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/x-protobuf")
	w := httptest.NewRecorder()
	HTTPHandler(ing).ServeHTTP(w, r)
	return w
}

// assertHTTPThrottle checks the fully-rejected OTLP/HTTP contract: 429 +
// Retry-After (so the collector keeps + re-sends its copy later) + a
// google.rpc.Status body.
func assertHTTPThrottle(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("total reject: status = %d, want 429", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want \"2\"", got)
	}
	var st statuspb.Status
	if err := proto.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("429 body must be a google.rpc.Status: %v", err)
	}
	if st.Code != int32(codes.ResourceExhausted) {
		t.Fatalf("Status.Code = %d, want %d (RESOURCE_EXHAUSTED)", st.Code, codes.ResourceExhausted)
	}
}

func TestHTTPTracesBackpressure(t *testing.T) {
	for _, tc := range backpressureCases {
		t.Run(tc.name, func(t *testing.T) {
			ing := bpIngester(tc.buf, 4, 4)
			w := postProto(t, ing, "/v1/traces", plainSpansRequest(tc.send))
			if tc.wantThrottle {
				assertHTTPThrottle(t, w)
				return
			}
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			var resp tracecollpb.ExportTraceServiceResponse
			if err := proto.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			ps := resp.GetPartialSuccess()
			if tc.wantRejected == 0 {
				if ps != nil {
					t.Fatalf("clean accept must not carry PartialSuccess, got %+v", ps)
				}
				return
			}
			if ps == nil || ps.RejectedSpans != tc.wantRejected {
				t.Fatalf("PartialSuccess = %+v, want RejectedSpans %d", ps, tc.wantRejected)
			}
			if ps.ErrorMessage == "" {
				t.Fatalf("PartialSuccess.ErrorMessage must explain the rejection")
			}
		})
	}
}

func TestHTTPLogsBackpressure(t *testing.T) {
	for _, tc := range backpressureCases {
		t.Run(tc.name, func(t *testing.T) {
			ing := bpIngester(4, tc.buf, 4)
			w := postProto(t, ing, "/v1/logs", logRecordsRequest(tc.send))
			if tc.wantThrottle {
				assertHTTPThrottle(t, w)
				return
			}
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			var resp logscollpb.ExportLogsServiceResponse
			if err := proto.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			ps := resp.GetPartialSuccess()
			if tc.wantRejected == 0 {
				if ps != nil {
					t.Fatalf("clean accept must not carry PartialSuccess, got %+v", ps)
				}
				return
			}
			if ps == nil || ps.RejectedLogRecords != tc.wantRejected {
				t.Fatalf("PartialSuccess = %+v, want RejectedLogRecords %d", ps, tc.wantRejected)
			}
		})
	}
}

func TestHTTPMetricsBackpressure(t *testing.T) {
	for _, tc := range backpressureCases {
		t.Run(tc.name, func(t *testing.T) {
			ing := bpIngester(4, 4, tc.buf)
			w := postProto(t, ing, "/v1/metrics", gaugePointsRequest(tc.send))
			if tc.wantThrottle {
				assertHTTPThrottle(t, w)
				return
			}
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			var resp metricscollpb.ExportMetricsServiceResponse
			if err := proto.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			ps := resp.GetPartialSuccess()
			if tc.wantRejected == 0 {
				if ps != nil {
					t.Fatalf("clean accept must not carry PartialSuccess, got %+v", ps)
				}
				return
			}
			if ps == nil || ps.RejectedDataPoints != tc.wantRejected {
				t.Fatalf("PartialSuccess = %+v, want RejectedDataPoints %d", ps, tc.wantRejected)
			}
		})
	}
}

// JSON round trip — the response marshaling follows the Accept header (how
// writeProtoResp has always picked), so a JSON client sees partialSuccess /
// the throttle Status as JSON too.
func TestHTTPTracesBackpressureJSON(t *testing.T) {
	postJSON := func(t *testing.T, ing *Ingester, req proto.Message) *httptest.ResponseRecorder {
		t.Helper()
		body, err := protojson.Marshal(req)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		r := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json")
		w := httptest.NewRecorder()
		HTTPHandler(ing).ServeHTTP(w, r)
		return w
	}

	t.Run("partial accept", func(t *testing.T) {
		ing := bpIngester(2, 4, 4)
		w := postJSON(t, ing, plainSpansRequest(5))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", ct)
		}
		var resp tracecollpb.ExportTraceServiceResponse
		if err := protojson.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal JSON response: %v", err)
		}
		if ps := resp.GetPartialSuccess(); ps == nil || ps.RejectedSpans != 3 {
			t.Fatalf("PartialSuccess = %+v, want RejectedSpans 3", ps)
		}
	})

	t.Run("total reject", func(t *testing.T) {
		ing := bpIngester(0, 4, 4)
		w := postJSON(t, ing, plainSpansRequest(3))
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429", w.Code)
		}
		if got := w.Header().Get("Retry-After"); got != "2" {
			t.Fatalf("Retry-After = %q, want \"2\"", got)
		}
		var st statuspb.Status
		if err := protojson.Unmarshal(w.Body.Bytes(), &st); err != nil {
			t.Fatalf("429 JSON body must be a google.rpc.Status: %v", err)
		}
		if st.Code != int32(codes.ResourceExhausted) {
			t.Fatalf("Status.Code = %d, want %d (RESOURCE_EXHAUSTED)", st.Code, codes.ResourceExhausted)
		}
	})
}

// HTTP mirror of the derived-side-signal decision: a full span_links buffer
// on an otherwise-accepted batch stays a clean 200.
func TestHTTPTraceLinkDropsDoNotRejectBatch(t *testing.T) {
	ing := bpIngester(8, 4, 4)
	ing.SetSpanLinks(consumer.New[*chstore.SpanLinkRow]("span_links", consumer.Options{BufferSize: 0}, nil))
	w := postProto(t, ing, "/v1/traces", tracesRequest(linkedSpanFixture()))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (link drops must not throttle)", w.Code)
	}
	var resp tracecollpb.ExportTraceServiceResponse
	if err := proto.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.GetPartialSuccess() != nil {
		t.Fatalf("link drops must not surface as PartialSuccess, got %+v", resp.GetPartialSuccess())
	}
	if got := ing.SpanLinks.Dropped(); got != 1 {
		t.Fatalf("span_links consumer Dropped = %d, want 1", got)
	}
}
