package otlp

import (
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"

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

// ExemplarsIngested / ExemplarsDroppedNoTrace are the two v0.8.328 exemplar
// ingest totals surfaced on /admin/stats (SystemStats.Exemplars), following
// the pipeline-counter accessor pattern above.
func (ing *Ingester) ExemplarsIngested() uint64       { return ing.exemplarsIngested.Load() }
func (ing *Ingester) ExemplarsDroppedNoTrace() uint64 { return ing.exemplarsDroppedNoTrace.Load() }

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
	ing.exemplarsIngested.Add(1)
	if ing.Exemplars == nil {
		return true // pod without the exemplar consumer wired — count only
	}
	return ing.Exemplars.Add(ex)
}

// HTTPHandler returns an http.Handler that accepts OTLP/HTTP (protobuf + JSON).
func HTTPHandler(ing *Ingester) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/traces", ing.handleTraces)
	mux.HandleFunc("POST /v1/logs", ing.handleLogs)
	mux.HandleFunc("POST /v1/metrics", ing.handleMetrics)
	return mux
}

func (ing *Ingester) handleTraces(w http.ResponseWriter, r *http.Request) {
	var req tracecollpb.ExportTraceServiceRequest
	if err := readProto(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	spans := ConvertTraces(&req)
	dropped := 0
	for _, sp := range spans {
		if !ing.addSpan(sp) {
			dropped++
		}
	}
	if dropped > 0 {
		log.Printf("[otlp/http] traces: dropped %d spans (buffer full)", dropped)
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
	for _, l := range logs {
		ing.addLog(l)
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
	for _, p := range pts {
		ing.addMetric(p)
	}
	// v0.8.328 — OTLP exemplars ride the same request; the gate + counters
	// live in addExemplar so gRPC shares them.
	for _, ex := range exs {
		ing.addExemplar(ex)
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
