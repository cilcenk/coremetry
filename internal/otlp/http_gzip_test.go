package otlp

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"net/http/httptest"
	"testing"

	"github.com/klauspost/compress/zstd"
	tracecollpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// TestReadProtoCompression guards OTLP/HTTP compression coverage (v0.9.171
// gzip, v0.9.172 zstd + zlib/deflate). Operator-reported: a real otlphttp
// exporter (default compression: gzip; also zstd) POSTing to /v1/* got HTTP 400
// because readProto fed the still-compressed bytes straight to proto.Unmarshal.
// readProto MUST transparently decompress every standard Content-Encoding;
// an unknown/undecodable encoding is rejected (→ 400), not mis-decoded.
func TestReadProtoCompression(t *testing.T) {
	const want = "GET /checkout"
	raw, err := proto.Marshal(&tracecollpb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{Name: want}},
			}},
		}},
	})
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
	gzipEnc := func(b []byte) []byte {
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		_, _ = w.Write(b)
		_ = w.Close()
		return buf.Bytes()
	}
	zlibEnc := func(b []byte) []byte {
		var buf bytes.Buffer
		w := zlib.NewWriter(&buf)
		_, _ = w.Write(b)
		_ = w.Close()
		return buf.Bytes()
	}
	zstdEnc := func(b []byte) []byte {
		var buf bytes.Buffer
		w, _ := zstd.NewWriter(&buf)
		_, _ = w.Write(b)
		_ = w.Close()
		return buf.Bytes()
	}

	ok := []struct {
		name, enc string
		body      []byte
	}{
		{"plain", "", raw},
		{"gzip (otlphttp default)", "gzip", gzipEnc(raw)},
		{"zstd", "zstd", zstdEnc(raw)},
		{"deflate/zlib", "deflate", zlibEnc(raw)},
		{"identity", "identity", raw},
	}
	for _, c := range ok {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/traces", bytes.NewReader(c.body))
			req.Header.Set("Content-Type", "application/x-protobuf")
			if c.enc != "" {
				req.Header.Set("Content-Encoding", c.enc)
			}
			var got tracecollpb.ExportTraceServiceRequest
			if err := readProto(req, &got); err != nil {
				t.Fatalf("Content-Encoding %q: readProto errored: %v", c.enc, err)
			}
			if firstSpanName(&got) != want {
				t.Fatalf("Content-Encoding %q: got %q want %q", c.enc, firstSpanName(&got), want)
			}
		})
	}

	// A gzip header over a non-gzip body → decode error (the correct 400 path).
	t.Run("bad gzip -> error", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/traces", bytes.NewReader([]byte("not-gzip")))
		req.Header.Set("Content-Type", "application/x-protobuf")
		req.Header.Set("Content-Encoding", "gzip")
		var got tracecollpb.ExportTraceServiceRequest
		if err := readProto(req, &got); err == nil {
			t.Fatal("bad gzip should error (→ 400), got nil")
		}
	})

	// An unknown encoding is rejected, never silently mis-decoded.
	t.Run("unknown encoding -> error", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/v1/traces", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/x-protobuf")
		req.Header.Set("Content-Encoding", "brotli")
		var got tracecollpb.ExportTraceServiceRequest
		if err := readProto(req, &got); err == nil {
			t.Fatal("unknown Content-Encoding should error (→ 400), got nil")
		}
	})
}
