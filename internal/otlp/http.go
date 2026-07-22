package otlp

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	logscollpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricscollpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracecollpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"

	"github.com/cilcenk/coremetry/internal/acache"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/consumer"
	"github.com/cilcenk/coremetry/internal/pipeline"
)

// Ingester wires the three OTLP consumers together. Every received span
// is stored — in-binary head/tail sampling was removed (v0.8.73); sampling,
// when wanted, happens at the collector (the sample-to-Coremetry /
// 100%-to-Tempo split). An optional pipeline drop/enrich engine still runs
// first.
type Ingester struct {
	Spans   *consumer.Consumer[*chstore.Span]
	Logs    *consumer.Consumer[*chstore.Log]
	Metrics *consumer.Consumer[*chstore.MetricPoint]
	// Exemplars (v0.8.328) — OTLP metric exemplars extracted alongside the
	// metric points (cross-signal pivot). Its flusher batches into
	// chstore.InsertExemplars with the same consumer machinery / flush
	// cadence as Metrics. nil-safe: addExemplar no-ops the enqueue (counters
	// still tick) on pods where main didn't wire it.
	Exemplars *consumer.Consumer[*chstore.ExemplarRow]
	// SpanLinks (v0.8.329) — OTel span links extracted alongside the span
	// rows (cross-signal pivot Phase 1b). Its flusher batches into
	// chstore.InsertSpanLinks with the same consumer machinery / flush
	// cadence as Spans. nil-safe: addSpanLink no-ops the enqueue (counters
	// still tick) on pods where main didn't wire it.
	SpanLinks *consumer.Consumer[*chstore.SpanLinkRow]

	// pipeline (v0.5.263) — ingest-time drop / enrich rules. nil = no rules
	// (the engine is a no-op-on-empty, so passing it through is fine too).
	// v0.8.282 — extended to logs + metrics; one counter per signal.
	pipeline       *pipeline.Engine
	spansDropped   atomic.Uint64
	logsDropped    atomic.Uint64
	metricsDropped atomic.Uint64

	// exemplar policy + counters (v0.8.328). exemplarsNoTraceOK is the
	// INVERTED config exemplars.require_trace_context so the zero value
	// (false) matches the config default (require=true) — an Ingester built
	// without SetExemplarPolicy still gates correctly. Counters follow the
	// pipeline-drop atomics above: droppedNoTrace is an INTENTIONAL policy
	// drop (a trace-less exemplar can't be clicked through), not data loss.
	exemplarsNoTraceOK      bool
	exemplarsIngested       atomic.Uint64
	exemplarsDroppedNoTrace atomic.Uint64
	// v0.8.433 (exemplar audit Faz C) — optional per-series×minute ingest
	// cap. nil = unlimited (the default posture; exemplar volume is
	// producer-bounded). droppedCapped is an INTENTIONAL policy drop,
	// like droppedNoTrace — never in the loss alarm.
	exemplarLimiter        *exemplarRateLimiter
	exemplarsDroppedCapped atomic.Uint64

	// span-link counters (v0.8.329). No policy knob: a link whose linked
	// trace id is empty/all-zero is MALFORMED per the OTel spec (trace_id is
	// required on a link), not an operator choice — always dropped + counted
	// as invalid. Same atomics + accessor shape as the exemplar counters.
	spanLinksIngested       atomic.Uint64
	spanLinksDroppedInvalid atomic.Uint64

	// autocomplete (v0.8.80) — Redis-backed picker-facet cache. nil-safe:
	// ObserveSpan short-circuits on a nil/disabled store, so pods without
	// Redis pay nothing.
	autocomplete *acache.Store
}

// SetAutocomplete wires the Redis autocomplete cache so every accepted span
// also populates the service/operation/attribute picker facets. Called from
// main(); nil keeps the old behaviour (no autocomplete cache).
func (ing *Ingester) SetAutocomplete(a *acache.Store) { ing.autocomplete = a }

// SetPipeline wires the ingest-time policy engine. Always called
// from main(); nil keeps the old behaviour (every span flows
// through unchanged).
func (ing *Ingester) SetPipeline(p *pipeline.Engine) { ing.pipeline = p }

// SpansDroppedByPipeline is a monotonic counter of spans dropped
// by a pipeline rule since boot. Surfaced on /api/health
// alongside sampler-dropped + buffer-dropped counters so the
// operator can see the policy engine's effect at a glance.
func (ing *Ingester) SpansDroppedByPipeline() uint64 { return ing.spansDropped.Load() }

// LogsDroppedByPipeline / MetricsDroppedByPipeline mirror the span
// accessor (v0.8.282). Surfaced on /admin/stats so the operator sees
// how many logs / metric points a pipeline rule discarded before the
// consumer buffer. Distinct from the queue-full / write-failed loss
// counters — pipeline drops are INTENTIONAL, not data loss.
func (ing *Ingester) LogsDroppedByPipeline() uint64    { return ing.logsDropped.Load() }
func (ing *Ingester) MetricsDroppedByPipeline() uint64 { return ing.metricsDropped.Load() }

// SetExemplars wires the exemplar consumer (v0.8.328). Called from main();
// nil keeps addExemplar counting but not enqueueing (api-only pods).
func (ing *Ingester) SetExemplars(c *consumer.Consumer[*chstore.ExemplarRow]) { ing.Exemplars = c }

// SetExemplarPolicy applies config exemplars.require_trace_context (default
// true). require=false stores trace-less exemplars too — useful when the
// operator wants the value/attr context even without a click-through target.
func (ing *Ingester) SetExemplarPolicy(requireTraceContext bool) {
	ing.exemplarsNoTraceOK = !requireTraceContext
}

// SetExemplarCap arms the per-series×minute ingest cap (v0.8.433,
// exemplar audit Faz C). n <= 0 keeps the unlimited default. Called at
// boot before traffic like SetExemplarPolicy — not safe to flip live.
func (ing *Ingester) SetExemplarCap(n int) {
	if n > 0 {
		ing.exemplarLimiter = newExemplarRateLimiter(n)
	} else {
		ing.exemplarLimiter = nil
	}
}

// ExemplarsIngested / ExemplarsDroppedNoTrace are the two v0.8.328 exemplar
// ingest totals surfaced on /admin/stats (SystemStats.Exemplars), following
// the pipeline-counter accessor pattern above.
func (ing *Ingester) ExemplarsIngested() uint64       { return ing.exemplarsIngested.Load() }
func (ing *Ingester) ExemplarsDroppedNoTrace() uint64 { return ing.exemplarsDroppedNoTrace.Load() }
func (ing *Ingester) ExemplarsDroppedCapped() uint64  { return ing.exemplarsDroppedCapped.Load() }

// SetSpanLinks wires the span-link consumer (v0.8.329). Called from main();
// nil keeps addSpanLink counting but not enqueueing (api-only pods).
func (ing *Ingester) SetSpanLinks(c *consumer.Consumer[*chstore.SpanLinkRow]) { ing.SpanLinks = c }

// SpanLinksIngested / SpanLinksDroppedInvalid are the two v0.8.329 span-link
// ingest totals surfaced on /admin/stats (SystemStats.SpanLinks), following
// the exemplar-counter accessor pattern above.
func (ing *Ingester) SpanLinksIngested() uint64       { return ing.spanLinksIngested.Load() }
func (ing *Ingester) SpanLinksDroppedInvalid() uint64 { return ing.spanLinksDroppedInvalid.Load() }

func NewIngester(
	spans *consumer.Consumer[*chstore.Span],
	logs *consumer.Consumer[*chstore.Log],
	metrics *consumer.Consumer[*chstore.MetricPoint],
) *Ingester {
	return &Ingester{Spans: spans, Logs: logs, Metrics: metrics}
}

// addSpan runs the optional ingest-time pipeline (drop/enrich) then forwards
// to the span consumer. Returns false only if the consumer's buffer was full
// and the span had to be dropped — same return semantics as the raw
// Spans.Add it replaces.
func (ing *Ingester) addSpan(sp *chstore.Span) bool {
	// Pipeline (v0.5.263) — operator drop rules. accept-but-discard so the
	// caller's accept accounting is unaffected.
	if ing.pipeline != nil && !ing.pipeline.AcceptSpan(sp) {
		ing.spansDropped.Add(1)
		return true
	}
	// Autocomplete cache (v0.8.80) — fire-and-forget, non-blocking (folds the
	// span into an in-memory delta map; a nil/disabled receiver returns at
	// once). Runs after the pipeline drop so discarded spans never pollute the
	// picker facets.
	ing.autocomplete.ObserveSpan(sp)
	return ing.Spans.Add(sp)
}

// addLog runs the optional ingest-time pipeline (drop/enrich/sample) then
// forwards to the log consumer (v0.8.282). Mirrors addSpan: an accept-but-
// discard on a pipeline drop keeps the caller's accept accounting unaffected.
// Returns false only when the consumer buffer was full.
func (ing *Ingester) addLog(l *chstore.Log) bool {
	if ing.pipeline != nil && !ing.pipeline.AcceptLog(l) {
		ing.logsDropped.Add(1)
		return true
	}
	return ing.Logs.Add(l)
}

// addMetric runs the optional ingest-time pipeline (drop/enrich/sample) then
// forwards to the metric consumer (v0.8.282). Same accept-but-discard
// semantics as addSpan / addLog.
func (ing *Ingester) addMetric(m *chstore.MetricPoint) bool {
	if ing.pipeline != nil && !ing.pipeline.AcceptMetric(m) {
		ing.metricsDropped.Add(1)
		return true
	}
	return ing.Metrics.Add(m)
}

// addExemplar applies the require-trace-context gate (v0.8.328) then forwards
// to the exemplar consumer. Accept-but-discard on a policy drop, like the
// pipeline drops — the caller's accounting is unaffected. Returns false only
// when the consumer buffer was full.
func (ing *Ingester) addExemplar(ex *chstore.ExemplarRow) bool {
	if ex.TraceID == "" && !ing.exemplarsNoTraceOK {
		ing.exemplarsDroppedNoTrace.Add(1)
		return true
	}
	// Per-series×minute cap (v0.8.433, Faz C) — intentional drop, same
	// accept-but-discard semantics as the trace gate above.
	if ing.exemplarLimiter != nil && !ing.exemplarLimiter.allow(ex.Fingerprint, time.Now()) {
		ing.exemplarsDroppedCapped.Add(1)
		return true
	}
	ing.exemplarsIngested.Add(1)
	if ing.Exemplars == nil {
		return true // pod without the exemplar consumer wired — count only
	}
	return ing.Exemplars.Add(ex)
}

// addSpanLink applies the invalid-link gate (v0.8.329) then forwards to the
// span-link consumer. A link whose linked trace id collapsed to "" (nil or
// all-zero on the wire) points NOWHERE — it can't be traversed in either
// pivot direction, so storing it would only bloat the PK. Accept-but-discard
// like the exemplar/pipeline gates; returns false only when the consumer
// buffer was full.
func (ing *Ingester) addSpanLink(l *chstore.SpanLinkRow) bool {
	if l.LinkedTraceID == "" {
		ing.spanLinksDroppedInvalid.Add(1)
		return true
	}
	ing.spanLinksIngested.Add(1)
	if ing.SpanLinks == nil {
		return true // pod without the span-link consumer wired — count only
	}
	return ing.SpanLinks.Add(l)
}

// HTTPHandler returns an http.Handler that accepts OTLP/HTTP (protobuf + JSON).
func HTTPHandler(ing *Ingester) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/traces", ing.handleTraces)
	mux.HandleFunc("POST /v1/logs", ing.handleLogs)
	mux.HandleFunc("POST /v1/metrics", ing.handleMetrics)
	return mux
}

// HTTPHandle wraps the dedicated OTLP/HTTP server so main can drain it on
// shutdown, symmetric with GRPCHandle (v0.9.168).
type HTTPHandle struct{ srv *http.Server }

// Shutdown drains the dedicated OTLP/HTTP listener, bounded by `grace` — same
// contract as GRPCHandle.Shutdown so main's ordered teardown treats both
// symmetrically. Nil-safe (nil on non-ingest roles / when disabled).
func (h *HTTPHandle) Shutdown(grace time.Duration) {
	if h == nil || h.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if err := h.srv.Shutdown(ctx); err != nil {
		log.Printf("[otlp-http] graceful stop exceeded %s — forcing: %v", grace, err)
		_ = h.srv.Close()
	}
}

// StartHTTP starts a DEDICATED OTLP/HTTP listener on addr serving ONLY the OTLP
// ingest routes (POST /v1/{traces,logs,metrics}) — no Web UI, no REST, no auth
// middleware — so a cross-cluster collector pushes straight to the standard
// OTel port (:4318) without reaching the login-gated UI that shares :8088.
// Serves plain HTTP; TLS is terminated at the edge (OpenShift Route edge
// termination / Ingress / LB), never in-binary. Binds synchronously so a port
// clash fails boot loud (an ingest pod with a dead OTLP listener is not a
// degraded mode), then serves in a goroutine and returns a handle main drains
// on SIGTERM — symmetric with StartGRPC (v0.9.168).
func StartHTTP(addr string, ing *Ingester) (*HTTPHandle, error) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	srv := &http.Server{
		Handler: HTTPHandler(ing),
		// Bound every phase of a request: the auth-free, edge-reachable OTLP
		// port must not be wedgeable by slow-body (slowloris) or idle
		// keep-alive exhaustion. The gRPC sibling gets this via MaxConnectionAge;
		// the HTTP one needs explicit timeouts (v0.9.168 review). ReadTimeout
		// caps the WHOLE request (headers + body), so a dribbled body can't pin
		// a goroutine+FD indefinitely; IdleTimeout reaps idle keep-alives. Body
		// size is already bounded at 32 MiB per request by readProto's
		// io.LimitReader (same cap as the gRPC MaxRecvMsgSize), so a single
		// request's memory is bounded; these timeouts bound its TIME + lifetime.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	log.Printf("[otlp-http] listening on %s", addr)
	go func() {
		if serr := srv.Serve(lis); serr != nil && serr != http.ErrServerClosed {
			log.Printf("[otlp-http] serve: %v", serr)
		}
	}()
	return &HTTPHandle{srv: srv}, nil
}

func (ing *Ingester) handleTraces(w http.ResponseWriter, r *http.Request) {
	var req tracecollpb.ExportTraceServiceRequest
	if err := readProto(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	spans, links := ConvertTraces(&req)
	dropped := 0
	for _, sp := range spans {
		if !ing.addSpan(sp) {
			dropped++
		}
	}
	// v0.8.329 — span links ride the same request; the invalid gate +
	// counters live in addSpanLink so gRPC shares them. Derived side-signal
	// (v0.8.345): link-buffer drops never reject the batch — see the
	// gRPC trace Export comment; drops ride the consumer's counter only.
	for _, l := range links {
		ing.addSpanLink(l)
	}
	if dropped > 0 {
		log.Printf("[otlp/http] traces: dropped %d/%d spans (buffer full)", dropped, len(spans))
		if dropped == len(spans) {
			writeThrottled(w, r, "all spans rejected: "+bufferFullMsg)
			return
		}
		// Partial acceptance → 200 + PartialSuccess (v0.8.345, HA audit H5;
		// OTLP spec §"Partial Success"). The old empty 200 made the
		// collector delete its copy of the dropped spans — silent loss.
		writeProtoResp(w, r, &tracecollpb.ExportTraceServiceResponse{
			PartialSuccess: &tracecollpb.ExportTracePartialSuccess{
				RejectedSpans: int64(dropped),
				ErrorMessage:  bufferFullMsg,
			},
		})
		return
	}
	writeProtoResp(w, r, &tracecollpb.ExportTraceServiceResponse{})
}

func (ing *Ingester) handleLogs(w http.ResponseWriter, r *http.Request) {
	var req logscollpb.ExportLogsServiceRequest
	if err := readProto(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logs := ConvertLogs(&req)
	dropped := 0
	for _, l := range logs {
		// v0.8.345 (HA audit H5) — Add result was discarded: a 100% drop
		// still answered an empty 200, so the collector deleted its copy
		// and never slowed down. See the gRPC logs Export.
		if !ing.addLog(l) {
			dropped++
		}
	}
	if dropped > 0 {
		log.Printf("[otlp/http] logs: dropped %d/%d records (buffer full)", dropped, len(logs))
		if dropped == len(logs) {
			writeThrottled(w, r, "all log records rejected: "+bufferFullMsg)
			return
		}
		writeProtoResp(w, r, &logscollpb.ExportLogsServiceResponse{
			PartialSuccess: &logscollpb.ExportLogsPartialSuccess{
				RejectedLogRecords: int64(dropped),
				ErrorMessage:       bufferFullMsg,
			},
		})
		return
	}
	writeProtoResp(w, r, &logscollpb.ExportLogsServiceResponse{})
}

func (ing *Ingester) handleMetrics(w http.ResponseWriter, r *http.Request) {
	var req metricscollpb.ExportMetricsServiceRequest
	if err := readProto(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pts, exs := ConvertMetrics(&req)
	dropped := 0
	for _, p := range pts {
		// v0.8.345 (HA audit H5) — Add result was discarded; see handleLogs.
		if !ing.addMetric(p) {
			dropped++
		}
	}
	// v0.8.328 — OTLP exemplars ride the same request; the gate + counters
	// live in addExemplar so gRPC shares them. Derived side-signal
	// (v0.8.345): exemplar-buffer drops never reject the batch — see the
	// gRPC metrics Export comment; drops ride the consumer's counter only.
	for _, ex := range exs {
		ing.addExemplar(ex)
	}
	if dropped > 0 {
		log.Printf("[otlp/http] metrics: dropped %d/%d points (buffer full)", dropped, len(pts))
		if dropped == len(pts) {
			writeThrottled(w, r, "all data points rejected: "+bufferFullMsg)
			return
		}
		writeProtoResp(w, r, &metricscollpb.ExportMetricsServiceResponse{
			PartialSuccess: &metricscollpb.ExportMetricsPartialSuccess{
				RejectedDataPoints: int64(dropped),
				ErrorMessage:       bufferFullMsg,
			},
		})
		return
	}
	writeProtoResp(w, r, &metricscollpb.ExportMetricsServiceResponse{})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func readProto(r *http.Request, msg proto.Message) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		return err
	}
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		return protojson.Unmarshal(body, msg)
	}
	return proto.Unmarshal(body, msg)
}

func writeProtoResp(w http.ResponseWriter, r *http.Request, msg proto.Message) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		b, _ := protojson.Marshal(msg)
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	} else {
		b, _ := proto.Marshal(msg)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Write(b)
	}
}

// writeThrottled answers a FULLY-rejected batch (v0.8.345, HA audit H5)
// per OTLP spec §"OTLP/HTTP Throttling": 429 Too Many Requests with a
// Retry-After header so the collector's retry queue KEEPS its copy and
// backs off — the old empty 200 made it delete the data it was still
// holding, i.e. indefinite silent loss with zero backpressure. Zero
// items were accepted, so the whole-batch retry the 429 triggers cannot
// duplicate anything. Body is a google.rpc.Status (OTLP spec
// §"Failures": error responses carry a Status message), marshaled with
// the same JSON/protobuf pick as writeProtoResp.
func writeThrottled(w http.ResponseWriter, r *http.Request, msg string) {
	w.Header().Set("Retry-After", strconv.Itoa(int(throttleRetryDelay/time.Second)))
	st := &statuspb.Status{Code: int32(codes.ResourceExhausted), Message: msg}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		b, _ := protojson.Marshal(st)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write(b)
		return
	}
	b, _ := proto.Marshal(st)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusTooManyRequests)
	w.Write(b)
}
