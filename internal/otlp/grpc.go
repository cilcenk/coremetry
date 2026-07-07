package otlp

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	_ "google.golang.org/grpc/encoding/gzip" // register gzip compressor (clients commonly use it)
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	logscollpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricscollpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracecollpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

// ── Backpressure honesty (v0.8.345, HA audit H5) ──────────────────────────────
//
// The OTLP spec draws a hard line between the two overload outcomes, and
// conflating them is what this release fixes:
//
//   - PARTIAL acceptance MUST be a SUCCESS response carrying PartialSuccess
//     with the rejected_* count (OTLP spec §"Partial Success"). Returning a
//     gRPC error instead made SDKs/collectors retry the WHOLE batch — the
//     already-accepted items landed a SECOND time in the non-deduplicating
//     spans/logs/metric_points tables (double-counted rates in every MV)
//     plus retry load amplification exactly when the buffer was full.
//   - FULL rejection (zero items accepted → a whole-batch retry cannot
//     duplicate anything) is the only case that returns RESOURCE_EXHAUSTED,
//     and per OTLP spec §"OTLP/gRPC Throttling" it carries a
//     google.rpc.RetryInfo status detail — that detail is what tells a
//     well-behaved client the condition is retryable-with-backoff rather
//     than terminal.

// bufferFullMsg is the human-readable reason attached to every
// PartialSuccess / throttle response this receiver emits.
const bufferFullMsg = "ingest buffer full"

// throttleRetryDelay is the backoff a fully-rejected client is told to
// honor — RetryInfo.retry_delay on gRPC, Retry-After on HTTP. ~2s spans a
// consumer flush interval, so by the retry the flush stage has had a
// chance to drain buffer headroom.
const throttleRetryDelay = 2 * time.Second

// errBufferFull is the fully-rejected-batch gRPC error: ResourceExhausted
// + RetryInfo (see block comment above). WithDetails only fails on an
// invalid proto — fall back to the bare status rather than masking the
// throttle signal behind an internal error.
func errBufferFull(rejected int, what string) error {
	st := status.Newf(codes.ResourceExhausted, "%s: rejected all %d %s", bufferFullMsg, rejected, what)
	if withRetry, err := st.WithDetails(&errdetails.RetryInfo{
		RetryDelay: durationpb.New(throttleRetryDelay),
	}); err == nil {
		st = withRetry
	}
	return st.Err()
}

// GRPCHandle is the shutdown handle StartGRPC returns (v0.8.336, HA
// audit H1). Opaque on purpose: main owns WHEN to stop accepting, the
// otlp package owns HOW (graceful-with-bound), and main never imports
// google.golang.org/grpc.
type GRPCHandle struct {
	srv *grpc.Server
}

// Shutdown drains gracefully — GOAWAY to collectors, in-flight Exports
// finish — but never longer than `grace`: GracefulStop can hang on a
// stuck stream, and a shutdown that outlives terminationGracePeriod
// gets SIGKILLed mid-drain, losing more than the hard Stop would.
func (h *GRPCHandle) Shutdown(grace time.Duration) {
	if h == nil || h.srv == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		h.srv.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(grace):
		log.Printf("[grpc] graceful stop exceeded %s — forcing", grace)
		h.srv.Stop()
	}
}

// StartGRPC listens, registers the OTLP services and serves in a
// background goroutine, returning a handle main GracefulStops during
// shutdown (v0.8.336, HA audit H1 — the server used to outlive the
// consumers: post-SIGTERM Exports were ACKed into channels nobody
// drained, and the abrupt connection cut on process exit is the
// client-side trigger for the otelcol zero-addresses wedge).
func StartGRPC(addr string, ing *Ingester) (*GRPCHandle, error) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}

	srv := grpc.NewServer(
		grpc.MaxRecvMsgSize(32<<20),
		grpc.MaxSendMsgSize(32<<20),
		// v0.8.x — recycle long-lived OTLP/gRPC connections so a fleet of
		// collector exporters re-balances across api/ingest replicas instead of
		// pinning to one. A k8s ClusterIP Service load-balances per CONNECTION,
		// not per RPC, and OTLP/gRPC multiplexes everything over one long-lived
		// HTTP/2 connection — so without this a collector's stream sticks to a
		// single replica for its whole life (the load test saw one replica take
		// ~all spans while siblings idled). MaxConnectionAge sends a GOAWAY after
		// the age; the grace lets in-flight Exports drain; the collector's
		// exporter transparently reconnects (re-resolving the Service → possibly
		// a different replica). 2m keeps reconnect churn negligible on a single
		// pod while re-distributing within ~2m of a scale-out event.
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionAge:      2 * time.Minute,
			MaxConnectionAgeGrace: 15 * time.Second,
		}),
	)

	tracecollpb.RegisterTraceServiceServer(srv, &traceGRPC{ing: ing})
	logscollpb.RegisterLogsServiceServer(srv, &logsGRPC{ing: ing})
	metricscollpb.RegisterMetricsServiceServer(srv, &metricsGRPC{ing: ing})

	log.Printf("[grpc] listening on %s", addr)
	go func() {
		if err := srv.Serve(lis); err != nil {
			// GracefulStop/Stop return nil from Serve; anything else is a
			// real serve failure worth logging.
			log.Printf("[grpc] serve: %v", err)
		}
	}()
	return &GRPCHandle{srv: srv}, nil
}

// ── Trace service ──────────────────────────────────────────────────────────────

type traceGRPC struct {
	tracecollpb.UnimplementedTraceServiceServer
	ing *Ingester
}

func (s *traceGRPC) Export(_ context.Context, req *tracecollpb.ExportTraceServiceRequest) (*tracecollpb.ExportTraceServiceResponse, error) {
	spans, links := ConvertTraces(req)
	dropped := 0
	for _, sp := range spans {
		if !s.ing.addSpan(sp) {
			dropped++
		}
	}
	// v0.8.329 — span links; gate + counters shared with the HTTP path via
	// addSpanLink. Links are a DERIVED side-signal (v0.8.345): the spans
	// they were extracted from were already accepted above, so a link-buffer
	// drop must NOT reject the batch — that would trigger a client retry
	// that re-writes the accepted spans. Link drops ride the span_links
	// consumer's dropped counter (surfaced on /admin/stats) instead.
	for _, l := range links {
		s.ing.addSpanLink(l)
	}
	switch {
	case dropped == 0:
		return &tracecollpb.ExportTraceServiceResponse{}, nil
	case dropped == len(spans):
		// Nothing accepted → whole-batch retry is duplicate-safe; throttle.
		return nil, errBufferFull(dropped, "spans")
	default:
		// Partial acceptance → OK + PartialSuccess, never a gRPC error
		// (v0.8.345, HA audit H5 — see the backpressure block comment above).
		return &tracecollpb.ExportTraceServiceResponse{
			PartialSuccess: &tracecollpb.ExportTracePartialSuccess{
				RejectedSpans: int64(dropped),
				ErrorMessage:  bufferFullMsg,
			},
		}, nil
	}
}

// ── Logs service ───────────────────────────────────────────────────────────────

type logsGRPC struct {
	logscollpb.UnimplementedLogsServiceServer
	ing *Ingester
}

func (s *logsGRPC) Export(_ context.Context, req *logscollpb.ExportLogsServiceRequest) (*logscollpb.ExportLogsServiceResponse, error) {
	logs := ConvertLogs(req)
	dropped := 0
	for _, l := range logs {
		// v0.8.345 (HA audit H5) — the Add result used to be DISCARDED here:
		// a 100% buffer-full drop still returned OK, so the collector deleted
		// its copy and never slowed down (silent loss with no backpressure).
		if !s.ing.addLog(l) {
			dropped++
		}
	}
	switch {
	case dropped == 0:
		return &logscollpb.ExportLogsServiceResponse{}, nil
	case dropped == len(logs):
		return nil, errBufferFull(dropped, "log records")
	default:
		return &logscollpb.ExportLogsServiceResponse{
			PartialSuccess: &logscollpb.ExportLogsPartialSuccess{
				RejectedLogRecords: int64(dropped),
				ErrorMessage:       bufferFullMsg,
			},
		}, nil
	}
}

// ── Metrics service ────────────────────────────────────────────────────────────

type metricsGRPC struct {
	metricscollpb.UnimplementedMetricsServiceServer
	ing *Ingester
}

func (s *metricsGRPC) Export(_ context.Context, req *metricscollpb.ExportMetricsServiceRequest) (*metricscollpb.ExportMetricsServiceResponse, error) {
	pts, exs := ConvertMetrics(req)
	dropped := 0
	for _, p := range pts {
		// v0.8.345 (HA audit H5) — Add result was discarded; see logs Export.
		if !s.ing.addMetric(p) {
			dropped++
		}
	}
	// v0.8.328 — OTLP exemplars; gate + counters shared with the HTTP path
	// via addExemplar. Exemplars are a DERIVED side-signal (v0.8.345): the
	// datapoints they came from were already accepted, so an exemplar-buffer
	// drop must NOT reject the batch (a retry would re-write the accepted
	// points). Drops ride the exemplars consumer's dropped counter only.
	for _, ex := range exs {
		s.ing.addExemplar(ex)
	}
	switch {
	case dropped == 0:
		return &metricscollpb.ExportMetricsServiceResponse{}, nil
	case dropped == len(pts):
		return nil, errBufferFull(dropped, "data points")
	default:
		return &metricscollpb.ExportMetricsServiceResponse{
			PartialSuccess: &metricscollpb.ExportMetricsPartialSuccess{
				RejectedDataPoints: int64(dropped),
				ErrorMessage:       bufferFullMsg,
			},
		}, nil
	}
}
