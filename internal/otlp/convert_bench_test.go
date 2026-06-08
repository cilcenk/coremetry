package otlp

// Phase-2 #3 (perf+accuracy mission) — convertSpan ran json.Marshal(convertEvents(...))
// unconditionally for EVERY span, even the empty-events majority, producing "[]"
// at the cost of a slice alloc + a reflect-based marshal on the ingest hot path.
// These benchmarks pin the before→after of skipping that work when a span has no
// events (the common case at billions of spans/day).

import (
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func benchSpan(withEvents bool) *tracepb.Span {
	sp := &tracepb.Span{
		TraceId:           make([]byte, 16),
		SpanId:            make([]byte, 8),
		Name:              "GET /api/accounts",
		Kind:              tracepb.Span_SPAN_KIND_SERVER,
		StartTimeUnixNano: 1_700_000_000_000_000_000,
		EndTimeUnixNano:   1_700_000_000_050_000_000,
		Attributes: []*commonpb.KeyValue{
			{Key: "http.method", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "GET"}}},
			{Key: "http.route", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "/api/accounts"}}},
			{Key: "http.status_code", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 200}}},
		},
	}
	if withEvents {
		sp.Events = []*tracepb.Span_Event{
			{Name: "exception", TimeUnixNano: 1_700_000_000_020_000_000, Attributes: []*commonpb.KeyValue{
				{Key: "exception.type", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "TimeoutError"}}},
			}},
		}
	}
	return sp
}

// The dominant case: a span with NO events. This is where the unconditional
// marshal was pure waste.
func BenchmarkConvertSpanNoEvents(b *testing.B) {
	sp := benchSpan(false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = convertSpan(sp, "account-service", "host-1", "prod", "scope", nil, nil)
	}
}

// The rarer case: a span that actually carries an event — must still marshal.
func BenchmarkConvertSpanWithEvents(b *testing.B) {
	sp := benchSpan(true)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = convertSpan(sp, "account-service", "host-1", "prod", "scope", nil, nil)
	}
}
