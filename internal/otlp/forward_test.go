package otlp

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	metricscollpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/consumer"
)

// forward_test.go — v0.10.293 (audit §7.5 madde 2): ileti HAM gövdedir,
// CH modelinden geri üretilmez — HTTP protobuf yolunda BAYT özdeşliği;
// gzip yolunda gzip'li baytların özdeşliği; JSON yolunda protobuf'a
// çeviri; kapalıyken kopya yok; kuyruk doluyken düşer ama OTLP cevabı 200.

func testIngester(t *testing.T) (*Ingester, *[]MetricBody) {
	t.Helper()
	noop := func(context.Context, []*chstore.MetricPoint) error { return nil }
	m := consumer.New[*chstore.MetricPoint]("m", consumer.Options{BufferSize: 1000, BatchSize: 100}, noop)
	ing := NewIngester(nil, nil, m)
	var got []MetricBody
	ing.SetMetricForwardFunc(func(b MetricBody) bool { got = append(got, b); return true })
	ing.SetForwardEnabled(func() bool { return true })
	return ing, &got
}

func sampleReq() *metricscollpb.ExportMetricsServiceRequest {
	return &metricscollpb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Metrics: []*metricspb.Metric{{
					Name: "http_requests_total",
					Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{{
						TimeUnixNano: 1_700_000_000_000_000_000, Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: 42},
					}}}},
				}},
			}},
		}},
	}
}

func post(t *testing.T, ing *Ingester, body []byte, ctype, cenc string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/metrics", bytes.NewReader(body))
	r.Header.Set("Content-Type", ctype)
	if cenc != "" {
		r.Header.Set("Content-Encoding", cenc)
	}
	w := httptest.NewRecorder()
	ing.handleMetrics(w, r)
	return w
}

func TestForwardHTTPProtobufByteIdentical(t *testing.T) {
	ing, got := testIngester(t)
	pb, _ := proto.Marshal(sampleReq())
	if w := post(t, ing, pb, "application/x-protobuf", ""); w.Code != 200 {
		t.Fatalf("http %d", w.Code)
	}
	if len(*got) != 1 || !bytes.Equal((*got)[0].Body, pb) || (*got)[0].Gzipped {
		t.Fatalf("ileti ham protobuf'la bayt bayt aynı olmalı: n=%d", len(*got))
	}
	if ing.MetricForwardEnqueued() != 1 {
		t.Error("enqueued sayacı")
	}
}

func TestForwardHTTPGzipPassesCompressedBytes(t *testing.T) {
	ing, got := testIngester(t)
	pb, _ := proto.Marshal(sampleReq())
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(pb)
	zw.Close()
	if w := post(t, ing, buf.Bytes(), "application/x-protobuf", "gzip"); w.Code != 200 {
		t.Fatalf("http %d", w.Code)
	}
	if len(*got) != 1 || !(*got)[0].Gzipped || !bytes.Equal((*got)[0].Body, buf.Bytes()) {
		t.Fatal("gzip'li gövde AÇILMADAN, olduğu gibi iletilmeli")
	}
}

func TestForwardHTTPJSONReencodesToProtobuf(t *testing.T) {
	ing, got := testIngester(t)
	js, _ := protojson.Marshal(sampleReq())
	if w := post(t, ing, js, "application/json", ""); w.Code != 200 {
		t.Fatalf("http %d", w.Code)
	}
	if len(*got) != 1 || (*got)[0].Gzipped {
		t.Fatal("JSON gövde bir ileti üretmeli")
	}
	var back metricscollpb.ExportMetricsServiceRequest
	if err := proto.Unmarshal((*got)[0].Body, &back); err != nil {
		t.Fatalf("ileti protobuf değil: %v", err)
	}
	if !proto.Equal(&back, sampleReq()) {
		t.Error("JSON → protobuf çevirisi anlamı korumadı")
	}
}

func TestForwardDisabledCopiesNothing(t *testing.T) {
	ing, got := testIngester(t)
	ing.SetForwardEnabled(func() bool { return false })
	pb, _ := proto.Marshal(sampleReq())
	if w := post(t, ing, pb, "application/x-protobuf", ""); w.Code != 200 {
		t.Fatalf("http %d", w.Code)
	}
	if len(*got) != 0 || ing.MetricForwardEnqueued() != 0 {
		t.Error("yazım kapalıyken kopya alınmamalı")
	}
	// Bağlanmamış pod (SetMetricForward hiç çağrılmadı): nil-safe.
	bare := NewIngester(nil, nil, consumer.New[*chstore.MetricPoint]("m", consumer.Options{BufferSize: 10}, func(context.Context, []*chstore.MetricPoint) error { return nil }))
	if w := post(t, bare, pb, "application/x-protobuf", ""); w.Code != 200 {
		t.Fatalf("bağlanmamış ingester %d", w.Code)
	}
}

func TestForwardQueueFullDropsButAccepts(t *testing.T) {
	ing, _ := testIngester(t)
	ing.SetMetricForwardFunc(func(MetricBody) bool { return false })
	pb, _ := proto.Marshal(sampleReq())
	if w := post(t, ing, pb, "application/x-protobuf", ""); w.Code != 200 {
		t.Fatalf("ileti kuyruğu dolu OTLP cevabını etkilememeli: %d", w.Code)
	}
	if ing.MetricForwardDropped() != 1 {
		t.Errorf("dropped = %d", ing.MetricForwardDropped())
	}
}

func TestForwardGRPCReencodes(t *testing.T) {
	ing, got := testIngester(t)
	srv := &metricsGRPC{ing: ing}
	if _, err := srv.Export(context.Background(), sampleReq()); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatal("gRPC yolu ileti üretmeli")
	}
	var back metricscollpb.ExportMetricsServiceRequest
	if err := proto.Unmarshal((*got)[0].Body, &back); err != nil || !proto.Equal(&back, sampleReq()) {
		t.Error("gRPC ileti anlamı korumadı")
	}
}
