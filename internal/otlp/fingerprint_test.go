package otlp

// v0.8.328 — series fingerprint for the cross-signal pivot (metric ↔ trace).
// These tests PIN the canonical byte design of SeriesFingerprint:
//
//	metricName · 0x00 · "k=v"‹0x1F›"k=v"… (dp attrs sorted by key) · 0x00 ·
//	"service.instance.id=Y"‹0x1F›"service.name=X" (resource identity, sorted
//	by key, empty-valued pairs skipped)
//
// The fingerprint is persisted on metric_points.series_fingerprint AND on
// exemplars.series_fingerprint — any change to the byte layout orphans every
// stored exemplar, so a failing test here means "you are breaking the join
// key", not "update the expectation".

import (
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

func kvStr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func kvInt(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
}

func TestSeriesFingerprintDeterministic(t *testing.T) {
	attrs := []*commonpb.KeyValue{kvStr("method", "GET"), kvStr("route", "/api/x")}
	a := SeriesFingerprint("http.server.duration", attrs, "checkout", "pod-1")
	b := SeriesFingerprint("http.server.duration", attrs, "checkout", "pod-1")
	if a != b {
		t.Fatalf("same input hashed differently: %x vs %x", a, b)
	}
	if a == 0 {
		t.Fatalf("fingerprint must not be 0 (0 is the legacy-row sentinel on metric_points)")
	}
}

func TestSeriesFingerprintAttrOrderInvariance(t *testing.T) {
	// The SAME series arrives with attrs in different wire order (OTLP makes
	// no ordering promise) — identity must not change.
	a := SeriesFingerprint("m", []*commonpb.KeyValue{kvStr("a", "1"), kvStr("b", "2"), kvStr("c", "3")}, "svc", "inst")
	b := SeriesFingerprint("m", []*commonpb.KeyValue{kvStr("c", "3"), kvStr("a", "1"), kvStr("b", "2")}, "svc", "inst")
	if a != b {
		t.Fatalf("attr order changed the fingerprint: %x vs %x", a, b)
	}
}

// TestSeriesFingerprintSplitPointInjection pins the 0x00 / 0x1F separator
// design: values that CONTAIN the separators must not let two different
// attribute sets serialize to the same canonical bytes.
func TestSeriesFingerprintSplitPointInjection(t *testing.T) {
	cases := []struct {
		name   string
		aName  string
		aAttrs []*commonpb.KeyValue
		bName  string
		bAttrs []*commonpb.KeyValue
	}{
		{
			// One value smuggling the 0x1F pair separator vs a genuine
			// two-pair set at the same split point.
			name:   "pair separator in value",
			aName:  "m",
			aAttrs: []*commonpb.KeyValue{kvStr("a", "b\x1fc")},
			bName:  "m",
			bAttrs: []*commonpb.KeyValue{kvStr("a", "b"), kvStr("c", "")},
		},
		{
			// Metric name smuggling the 0x00 section separator + a fake pair
			// vs a genuine attr pair.
			name:   "section separator in metric name",
			aName:  "m\x00a=b",
			aAttrs: nil,
			bName:  "m",
			bAttrs: []*commonpb.KeyValue{kvStr("a", "b")},
		},
	}
	// KNOWN, ACCEPTED boundary of the pinned design: a KEY containing '='
	// ({"a=b":"c"} vs {"a":"b=c"} → both "a=b=c") is ambiguous. OTLP semconv
	// keys are dotted identifiers and never contain '=', and VALUES with '='
	// (the realistic case) are unambiguous because the split always happens
	// at the FIRST '=' of a pair. Not asserted — documented so a future
	// "harden the hash" change knows the trade-off was deliberate.
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := SeriesFingerprint(tc.aName, tc.aAttrs, "svc", "inst")
			b := SeriesFingerprint(tc.bName, tc.bAttrs, "svc", "inst")
			if a == b {
				t.Fatalf("split-point collision: %q/%v == %q/%v → %x", tc.aName, tc.aAttrs, tc.bName, tc.bAttrs, a)
			}
		})
	}
}

// Attr↔resource boundary: a datapoint attr literally named service.name must
// not collide with the resource-identity section carrying the same value.
func TestSeriesFingerprintAttrResourceBoundary(t *testing.T) {
	a := SeriesFingerprint("m", []*commonpb.KeyValue{kvStr("service.name", "x")}, "", "")
	b := SeriesFingerprint("m", nil, "x", "")
	if a == b {
		t.Fatalf("dp-attr service.name collided with resource identity: %x", a)
	}
}

func TestSeriesFingerprintResourceIdentityIsolation(t *testing.T) {
	attrs := []*commonpb.KeyValue{kvStr("le", "0.5")}
	base := SeriesFingerprint("m", attrs, "svc-a", "inst-1")
	if got := SeriesFingerprint("m", attrs, "svc-b", "inst-1"); got == base {
		t.Fatalf("different service.name produced the same fingerprint")
	}
	if got := SeriesFingerprint("m", attrs, "svc-a", "inst-2"); got == base {
		t.Fatalf("different service.instance.id produced the same fingerprint")
	}
	if got := SeriesFingerprint("m2", attrs, "svc-a", "inst-1"); got == base {
		t.Fatalf("different metric name produced the same fingerprint")
	}
}

func TestSeriesFingerprintEmptyInstanceID(t *testing.T) {
	attrs := []*commonpb.KeyValue{kvStr("k", "v")}
	// Empty instance id (the common non-k8s SDK case) is skipped from the
	// identity section — but must stay deterministic and distinct from a
	// populated one.
	a := SeriesFingerprint("m", attrs, "svc", "")
	b := SeriesFingerprint("m", attrs, "svc", "")
	if a != b {
		t.Fatalf("empty instance id not deterministic: %x vs %x", a, b)
	}
	if got := SeriesFingerprint("m", attrs, "svc", "pod-1"); got == a {
		t.Fatalf("empty vs set instance id collided")
	}
	// Both identity halves empty still hashes (degenerate but deterministic).
	c := SeriesFingerprint("m", attrs, "", "")
	if c == a {
		t.Fatalf("empty service name collided with populated one")
	}
}

// Attr values are stringified EXACTLY the way convert.go stores them into
// attr_values (anyValStr) — so int 5 and string "5" are the SAME series, by
// design: metric_points group-by reads compare the stringified arrays, and the
// fingerprint must agree with what the read path considers one series.
func TestSeriesFingerprintValueStringification(t *testing.T) {
	a := SeriesFingerprint("m", []*commonpb.KeyValue{kvInt("code", 5)}, "svc", "")
	b := SeriesFingerprint("m", []*commonpb.KeyValue{kvStr("code", "5")}, "svc", "")
	if a != b {
		t.Fatalf("int/string stringification drifted from anyValStr semantics: %x vs %x", a, b)
	}
}
