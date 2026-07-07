package logstore

// v0.8.348 — pivot Phase 1c trace-context self-discovery. Pins:
//   - the candidate fan-out (configured field first, traceTermsAny shapes,
//     deduped) that the field_caps probe queries,
//   - field_caps parsing against keyword / text / mixed / absent fixtures,
//   - the VERDICT rule (resolveEffectiveTraceField): configured-first,
//     else first present shape; pivotReady ⇔ keyword-mapped. A `text`
//     mapping silently kills the pivot's term clauses — the verdict is the
//     runtime detector the audit (§3 ⚠) called for,
//   - the coverage body's cost guards (size:0, bounded range, capped terms,
//     track_total_hits off, ES soft timeout) and its response parse.
// All pure — no live ES (house style, elasticsearch_fieldstats_test.go).

import (
	"reflect"
	"testing"
	"time"
)

func TestTraceFieldCandidates(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		want       []string
	}{
		{"custom field leads", "labels.trace",
			[]string{"labels.trace", "trace.id", "trace_id", "traceId", "TraceId"}},
		{"configured equals a default — deduped", "trace.id",
			[]string{"trace.id", "trace_id", "traceId", "TraceId"}},
		{"empty configured — defaults only", "",
			[]string{"trace.id", "trace_id", "traceId", "TraceId"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := traceFieldCandidates(tc.configured); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("traceFieldCandidates(%q) = %v, want %v", tc.configured, got, tc.want)
			}
		})
	}
}

func TestParseFieldCaps(t *testing.T) {
	raw := []byte(`{
		"indices": ["app-2026.07.06", "app-2026.07.07"],
		"fields": {
			"trace.id": {
				"keyword": {"type": "keyword", "metadata_field": false, "searchable": true, "aggregatable": true}
			},
			"trace_id": {
				"text":    {"type": "text",    "metadata_field": false, "searchable": true, "aggregatable": false},
				"keyword": {"type": "keyword", "metadata_field": false, "searchable": true, "aggregatable": true}
			},
			"service.name": {
				"keyword": {"type": "keyword", "metadata_field": false, "searchable": true, "aggregatable": true}
			}
		}
	}`)
	caps, err := parseFieldCaps(raw)
	if err != nil {
		t.Fatalf("parseFieldCaps: %v", err)
	}
	if got := caps["trace.id"]; !reflect.DeepEqual(got.Types, []string{"keyword"}) || !got.Searchable || !got.Aggregatable {
		t.Fatalf("trace.id cap = %+v, want keyword searchable aggregatable", got)
	}
	// Mixed mapping across dailies: types sorted, capabilities OR-ed.
	if got := caps["trace_id"]; !reflect.DeepEqual(got.Types, []string{"keyword", "text"}) || !got.Aggregatable {
		t.Fatalf("trace_id cap = %+v, want sorted [keyword text] aggregatable", got)
	}
	// Absent field = no entry (IncludeUnmapped off), zero value on lookup.
	if got, ok := caps["traceId"]; ok || len(got.Types) != 0 {
		t.Fatalf("absent field must have no entry, got %+v", got)
	}
}

func TestParseFieldCaps_Garbage(t *testing.T) {
	if _, err := parseFieldCaps([]byte(`not json`)); err == nil {
		t.Fatal("garbage body must error, not return an empty (\"all absent\") result")
	}
}

// The verdict table. fields are in candidate order — configured first —
// exactly as traceContextFields emits them.
func TestResolveEffectiveTraceField(t *testing.T) {
	f := func(name string, configured bool, types ...string) TraceContextField {
		if types == nil {
			types = []string{}
		}
		return TraceContextField{Name: name, Types: types, Configured: configured}
	}
	cases := []struct {
		name      string
		fields    []TraceContextField
		wantName  string
		wantType  string
		wantReady bool
	}{
		{"configured keyword → ready",
			[]TraceContextField{f("trace.id", true, "keyword"), f("trace_id", false)},
			"trace.id", "keyword", true},
		{"configured text → pivot inoperable",
			[]TraceContextField{f("trace.id", true, "text"), f("trace_id", false, "keyword")},
			"trace.id", "text", false},
		{"configured absent → first present shape wins",
			[]TraceContextField{f("labels.trace", true), f("trace.id", false), f("trace_id", false, "keyword")},
			"trace_id", "keyword", true},
		{"first present shape is text → not ready",
			[]TraceContextField{f("labels.trace", true), f("trace.id", false, "text")},
			"trace.id", "text", false},
		{"mixed keyword+text counts as keyword",
			[]TraceContextField{f("trace.id", true, "keyword", "text")},
			"trace.id", "keyword", true},
		{"all absent → configured named, absent, not ready",
			[]TraceContextField{f("labels.trace", true), f("trace.id", false), f("trace_id", false)},
			"labels.trace", "absent", false},
		{"empty input", nil, "", "absent", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, typ, ready := resolveEffectiveTraceField(tc.fields)
			if name != tc.wantName || typ != tc.wantType || ready != tc.wantReady {
				t.Fatalf("resolveEffectiveTraceField = (%q, %q, %v), want (%q, %q, %v)",
					name, typ, ready, tc.wantName, tc.wantType, tc.wantReady)
			}
		})
	}
}

func TestPickServiceAggField(t *testing.T) {
	caps := map[string]traceFieldCap{
		"svc.keyword.only":         {Types: []string{"text"}, Searchable: true},
		"svc.keyword.only.keyword": {Types: []string{"keyword"}, Aggregatable: true},
		"svc.pure":                 {Types: []string{"keyword"}, Aggregatable: true},
	}
	cases := []struct{ in, want string }{
		{"svc.pure", "svc.pure"},                        // bare aggregatable wins
		{"svc.keyword.only", "svc.keyword.only.keyword"}, // text field → .keyword subfield
		{"svc.unmapped", "svc.unmapped"},                // absent → harmless bare fallback
		{"already.keyword", "already.keyword"},          // never double-append
	}
	for _, tc := range cases {
		if got := pickServiceAggField(tc.in, caps); got != tc.want {
			t.Fatalf("pickServiceAggField(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The coverage body must carry EVERY cost guard — this is a size:0
// aggregation an admin page triggers against a 10B-docs/day cluster.
func TestBuildTraceCoverageBody_CostGuards(t *testing.T) {
	from := time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	body := buildTraceCoverageBody("@timestamp", "trace.id", "service.name", from, to, "10s")

	if body["size"] != 0 {
		t.Fatalf("size must be 0 (agg-only), got %v", body["size"])
	}
	if body["track_total_hits"] != false {
		t.Fatal("track_total_hits must be off — counts come from exact agg doc_counts")
	}
	if body["timeout"] != "10s" {
		t.Fatalf("ES soft timeout must ride the body, got %v", body["timeout"])
	}
	rng := body["query"].(map[string]any)["range"].(map[string]any)["@timestamp"].(map[string]any)
	if rng["gte"] != "2026-07-06T10:00:00Z" || rng["lte"] != "2026-07-07T10:00:00Z" {
		t.Fatalf("window must be absolute + bounded on the configured ts field, got %v", rng)
	}
	aggs := body["aggs"].(map[string]any)
	terms := aggs["services"].(map[string]any)["terms"].(map[string]any)
	if terms["size"] != traceCoverageTopServices || terms["field"] != "service.name" {
		t.Fatalf("services terms agg must be capped at %d on the picked field, got %v",
			traceCoverageTopServices, terms)
	}
	exists := aggs["with_trace"].(map[string]any)["filter"].(map[string]any)["exists"].(map[string]any)
	if exists["field"] != "trace.id" {
		t.Fatalf("with_trace exists must target the effective field, got %v", exists)
	}
	// Per-service share needs the same exists sub-filter.
	sub := aggs["services"].(map[string]any)["aggs"].(map[string]any)["with_trace"].(map[string]any)
	if sub["filter"].(map[string]any)["exists"].(map[string]any)["field"] != "trace.id" {
		t.Fatal("services sub-agg must carry the same exists filter")
	}
	// Exact total via a match_all filter agg (not hits.total).
	if _, ok := aggs["total"].(map[string]any)["filter"].(map[string]any)["match_all"]; !ok {
		t.Fatal("total must be a match_all filter agg — exact doc_count with track_total_hits off")
	}
}

func TestParseTraceCoverageResponse(t *testing.T) {
	raw := []byte(`{
		"took": 42, "timed_out": false,
		"aggregations": {
			"total":      {"doc_count": 1000000},
			"with_trace": {"doc_count": 750000},
			"services": {
				"doc_count_error_upper_bound": 0,
				"sum_other_doc_count": 100,
				"buckets": [
					{"key": "checkout", "doc_count": 600000, "with_trace": {"doc_count": 590000}},
					{"key": "identity", "doc_count": 400000, "with_trace": {"doc_count": 160000}}
				]
			}
		}
	}`)
	total, withTrace, services, err := parseTraceCoverageResponse(raw)
	if err != nil {
		t.Fatalf("parseTraceCoverageResponse: %v", err)
	}
	if total != 1000000 || withTrace != 750000 {
		t.Fatalf("overall = (%d, %d), want (1000000, 750000)", total, withTrace)
	}
	want := []TraceContextServiceCoverage{
		{Service: "checkout", Total: 600000, WithTrace: 590000},
		{Service: "identity", Total: 400000, WithTrace: 160000},
	}
	if !reflect.DeepEqual(services, want) {
		t.Fatalf("services = %+v, want %+v", services, want)
	}
}

func TestParseTraceCoverageResponse_Garbage(t *testing.T) {
	if _, _, _, err := parseTraceCoverageResponse([]byte(`{`)); err == nil {
		t.Fatal("garbage body must error, not report 0% coverage")
	}
}
