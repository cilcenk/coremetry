package otlp

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	_ "google.golang.org/grpc/encoding/gzip" // register gzip compressor (clients commonly use it)
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	logscollpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricscollpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracecollpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

// StartGRPC starts the OTLP/gRPC server and blocks until it fails.
func StartGRPC(addr string, ing *Ingester) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
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
	return srv.Serve(lis)
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
	// addSpanLink. Enqueued BEFORE the span-drop error return so links from
	// the accepted spans of a partially-dropped batch still land.
	for _, l := range links {
		s.ing.addSpanLink(l)
	}
	if dropped > 0 {
		return nil, status.Errorf(codes.ResourceExhausted, "dropped %d spans: buffer full", dropped)
	}
	return &tracecollpb.ExportTraceServiceResponse{}, nil
}

// ── Logs service ───────────────────────────────────────────────────────────────

type logsGRPC struct {
	logscollpb.UnimplementedLogsServiceServer
	ing *Ingester
}

func (s *logsGRPC) Export(_ context.Context, req *logscollpb.ExportLogsServiceRequest) (*logscollpb.ExportLogsServiceResponse, error) {
	logs := ConvertLogs(req)
	for _, l := range logs {
		s.ing.addLog(l)
	}
	return &logscollpb.ExportLogsServiceResponse{}, nil
}

// ── Metrics service ────────────────────────────────────────────────────────────

type metricsGRPC struct {
	metricscollpb.UnimplementedMetricsServiceServer
	ing *Ingester
}

func (s *metricsGRPC) Export(_ context.Context, req *metricscollpb.ExportMetricsServiceRequest) (*metricscollpb.ExportMetricsServiceResponse, error) {
	pts, exs := ConvertMetrics(req)
	for _, p := range pts {
		s.ing.addMetric(p)
	}
	// v0.8.328 — OTLP exemplars; gate + counters shared with the HTTP path
	// via addExemplar.
	for _, ex := range exs {
		s.ing.addExemplar(ex)
	}
	return &metricscollpb.ExportMetricsServiceResponse{}, nil
}
