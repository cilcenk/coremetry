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

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/consumer"
	"github.com/cilcenk/coremetry/internal/pipeline"
	"github.com/cilcenk/coremetry/internal/sampling"
)

// Ingester wires the three OTLP consumers together. When a Sampler
// is attached (non-nil), each incoming span is routed through it
// before reaching the consumer; sampled-out spans are dropped on
// the floor and counted in spansSampledOut for /api/health.
type Ingester struct {
	Spans   *consumer.Consumer[*chstore.Span]
	Logs    *consumer.Consumer[*chstore.Log]
	Metrics *consumer.Consumer[*chstore.MetricPoint]

	sampler         *sampling.Sampler
	spansSampledOut atomic.Uint64

	// pipeline (v0.5.263) — ingest-time drop / enrich rules,
	// evaluated BEFORE the sampler so dropped spans never touch
	// the consumer's batch buffer or the tail sampler's
	// bookkeeping. nil = no rules (the engine itself is a
	// no-op-on-empty so passing it through is fine too).
	pipeline       *pipeline.Engine
	spansDropped   atomic.Uint64
}

// SetPipeline wires the ingest-time policy engine. Always called
// from main(); nil keeps the old behaviour (every span flows
// through unchanged).
func (ing *Ingester) SetPipeline(p *pipeline.Engine) { ing.pipeline = p }

// SpansDroppedByPipeline is a monotonic counter of spans dropped
// by a pipeline rule since boot. Surfaced on /api/health
// alongside sampler-dropped + buffer-dropped counters so the
// operator can see the policy engine's effect at a glance.
func (ing *Ingester) SpansDroppedByPipeline() uint64 { return ing.spansDropped.Load() }

func NewIngester(
	spans *consumer.Consumer[*chstore.Span],
	logs *consumer.Consumer[*chstore.Log],
	metrics *consumer.Consumer[*chstore.MetricPoint],
) *Ingester {
	return &Ingester{Spans: spans, Logs: logs, Metrics: metrics}
}

// SetSampler swaps in a sampling decision engine. Pass nil to
// disable sampling (the default — keep every span). Safe to call
// at any time; the next span uses the new sampler.
func (ing *Ingester) SetSampler(s *sampling.Sampler) { ing.sampler = s }

// SpansSampledOut is a monotonic counter of spans dropped by the
// sampler since process start. Surfaced on /api/health alongside
// the existing spans_dropped counter so an operator can see
// sampling effectiveness without reading server logs.
func (ing *Ingester) SpansSampledOut() uint64 { return ing.spansSampledOut.Load() }

// addSpan applies sampling (if configured) and forwards to the
// span consumer. Returns false if the consumer's buffer was full
// and the span had to be dropped — same return semantics as the
// raw Spans.Add it replaces.
//
// Routing:
//   - If a tail sampler is enabled, the span is buffered there
//     and the tail's sweeper later flushes kept spans through
//     the consumer. addSpan returns true immediately — we treat
//     "buffered for later decision" as a successful accept.
//   - Otherwise the head sampler decides; sampled-out spans are
//     counted and dropped on the floor.
func (ing *Ingester) addSpan(sp *chstore.Span) bool {
	// Pipeline (v0.5.263) — drop runs BEFORE sampling so the
	// dropped span never enters the tail buffer or burns the
	// sampler's per-trace decision slot.
	if ing.pipeline != nil && !ing.pipeline.AcceptSpan(sp) {
		ing.spansDropped.Add(1)
		return true // operator policy = accept-but-discard
	}
	if ing.sampler != nil {
		if t := ing.sampler.Tail(); t != nil && t.Enabled() {
			t.Add(sp)
			return true
		}
		if !ing.sampler.Decide(sp) {
			ing.spansSampledOut.Add(1)
			return true
		}
	}
	return ing.Spans.Add(sp)
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
		ing.Logs.Add(l)
	}
	writeProtoResp(w, r, &logscollpb.ExportLogsServiceResponse{})
}

func (ing *Ingester) handleMetrics(w http.ResponseWriter, r *http.Request) {
	var req metricscollpb.ExportMetricsServiceRequest
	if err := readProto(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pts := ConvertMetrics(&req)
	for _, p := range pts {
		ing.Metrics.Add(p)
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
