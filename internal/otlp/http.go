package otlp

import (
	"io"
	"log"
	"net/http"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	logscollpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	metricscollpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	tracecollpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"

	"github.com/cenk/qmetry/internal/chstore"
	"github.com/cenk/qmetry/internal/consumer"
)

// Ingester wires the three OTLP consumers together.
type Ingester struct {
	Spans   *consumer.Consumer[*chstore.Span]
	Logs    *consumer.Consumer[*chstore.Log]
	Metrics *consumer.Consumer[*chstore.MetricPoint]
}

func NewIngester(
	spans *consumer.Consumer[*chstore.Span],
	logs *consumer.Consumer[*chstore.Log],
	metrics *consumer.Consumer[*chstore.MetricPoint],
) *Ingester {
	return &Ingester{Spans: spans, Logs: logs, Metrics: metrics}
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
		if !ing.Spans.Add(sp) {
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
