package otlp

import (
	"bytes"
	"compress/gzip"
	"net/http/httptest"
	"testing"

	tracecollpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// TestReadProtoGzip guards the v0.9.171 fix — operator-reported: a real
// otlphttp exporter (default compression: gzip) POSTing to /v1/* got HTTP 400
// because readProto fed the still-gzipped bytes straight to proto.Unmarshal.
// readProto MUST transparently decompress Content-Encoding: gzip; both gzipped
// and plain protobuf bodies must decode to the same message.
func TestReadProtoGzip(t *testing.T) {
	const want = "GET /checkout"
	srcMsg := &tracecollpb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{Name: want}},
			}},
		}},
	}
	raw, err := proto.Marshal(srcMsg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	firstSpanName := func(m *tracecollpb.ExportTraceServiceRequest) string {
		if len(m.ResourceSpans) == 0 ||
			len(m.ResourceSpans[0].ScopeSpans) == 0 ||
			len(m.ResourceSpans[0].ScopeSpans[0].Spans) == 0 {
			return ""
		}
		return m.ResourceSpans[0].ScopeSpans[0].Spans[0].Name
	}

	t.Run("gzip body decodes (the otlphttp default)", func(t *testing.T) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		if _, err := gz.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/v1/traces", &buf)
		req.Header.Set("Content-Type", "application/x-protobuf")
		req.Header.Set("Content-Encoding", "gzip")
		var got tracecollpb.ExportTraceServiceRequest
		if err := readProto(req, &got); err != nil {
			t.Fatalf("gzip readProto errored (the v0.9.171 bug): %v", err)
		}
		if firstSpanName(&got) != want {
			t.Fatalf("gzip decode: got %q want %q", firstSpanName(&got), want)
		}
	})

	t.Run("uncompressed body still decodes", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/traces", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/x-protobuf")
		var got tracecollpb.ExportTraceServiceRequest
		if err := readProto(req, &got); err != nil {
			t.Fatalf("plain readProto: %v", err)
		}
		if firstSpanName(&got) != want {
			t.Fatalf("plain decode: got %q want %q", firstSpanName(&got), want)
		}
	})
}
