package otlp

import (
	"context"
	"fmt"
	"log"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	_ "google.golang.org/grpc/encoding/gzip" // register gzip compressor (clients commonly use it)
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
	spans := ConvertTraces(req)
	dropped := 0
	for _, sp := range spans {
		if !s.ing.Spans.Add(sp) {
			dropped++
		}
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
		s.ing.Logs.Add(l)
	}
	return &logscollpb.ExportLogsServiceResponse{}, nil
}

// ── Metrics service ────────────────────────────────────────────────────────────

type metricsGRPC struct {
	metricscollpb.UnimplementedMetricsServiceServer
	ing *Ingester
}

func (s *metricsGRPC) Export(_ context.Context, req *metricscollpb.ExportMetricsServiceRequest) (*metricscollpb.ExportMetricsServiceResponse, error) {
	pts := ConvertMetrics(req)
	for _, p := range pts {
		s.ing.Metrics.Add(p)
	}
	return &metricscollpb.ExportMetricsServiceResponse{}, nil
}
