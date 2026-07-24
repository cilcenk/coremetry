// Watcher raw-search Faz-2 tests (v0.9.x): payload access for
// agg-path conditions + sample-fetch guards for the notification
// examples. Contracts:
//
//   - watcherPayloadJSON assembles the ctx.payload-shaped subset the
//     evaluator walks — {"hits":{"total":{"value":N}}} plus the
//     response's aggregations VERBATIM (json.RawMessage passthrough,
//     no float64 round-trip — big literals stay byte-exact).
//   - injectRawSampleGuards carries the SAME cost discipline as
//     injectRawSearchGuards (shared window core) but shaped for a
//     tiny sample fetch: size clamped to ≤5, _source restricted to
//     the configured fields, track_total_hits off, newest-first sort
//     injected when the body has none.
//   - watcherSampleLine flattens one hit's _source into a single-line
//     "<ts> [<service>] <message>" summary ≤200 chars.
package logstore

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWatcherPayloadJSON(t *testing.T) {
	t.Run("total only", func(t *testing.T) {
		got := watcherPayloadJSON(42, nil)
		var m map[string]any
		if err := json.Unmarshal(got, &m); err != nil {
			t.Fatalf("payload not valid JSON: %v\n%s", err, got)
		}
		hits, _ := m["hits"].(map[string]any)
		total, _ := hits["total"].(map[string]any)
		if total["value"] != float64(42) {
			t.Fatalf("hits.total.value = %v, want 42", total["value"])
		}
		if _, ok := m["aggregations"]; ok {
			t.Fatalf("no aggregations in the response must mean no aggregations key: %s", got)
		}
	})
	t.Run("aggregations pass through verbatim", func(t *testing.T) {
		aggs := json.RawMessage(`{"err_count":{"value":9007199254740993}}`)
		got := watcherPayloadJSON(7, aggs)
		if !strings.Contains(string(got), "9007199254740993") {
			t.Fatalf("aggregation literal corrupted (float64 round-trip?): %s", got)
		}
		var m map[string]any
		if err := json.Unmarshal(got, &m); err != nil {
			t.Fatalf("payload not valid JSON: %v\n%s", err, got)
		}
		if _, ok := m["aggregations"].(map[string]any); !ok {
			t.Fatalf("aggregations missing: %s", got)
		}
	})
}

func TestInjectRawSampleGuards(t *testing.T) {
	src := []string{"@timestamp", "message", "service.name"}
	tests := []struct {
		name  string
		body  string
		n     int
		check func(t *testing.T, m map[string]any, raw []byte)
	}{
		{
			name: "sample shape: size n, restricted _source, no total count",
			body: `{"query": {"query_string": {"query": "level:ERROR"}}}`,
			n:    3,
			check: func(t *testing.T, m map[string]any, raw []byte) {
				if m["size"] != float64(3) {
					t.Fatalf("size = %v, want 3", m["size"])
				}
				if m["track_total_hits"] != false {
					t.Fatalf("track_total_hits = %v, want false (samples don't need the count)", m["track_total_hits"])
				}
				srcList, _ := m["_source"].([]any)
				if len(srcList) != 3 {
					t.Fatalf("_source = %v, want the 3 configured fields", m["_source"])
				}
				if m["timeout"] != "10s" {
					t.Fatalf("timeout = %v, want 10s default", m["timeout"])
				}
			},
		},
		{
			name: "n clamped to 5 and floored at 1",
			body: `{"query": {"match_all": {}}}`,
			n:    50,
			check: func(t *testing.T, m map[string]any, raw []byte) {
				if m["size"] != float64(5) {
					t.Fatalf("size = %v, want clamp to 5", m["size"])
				}
			},
		},
		{
			name: "zero n floors to 1",
			body: `{"query": {"match_all": {}}}`,
			n:    0,
			check: func(t *testing.T, m map[string]any, raw []byte) {
				if m["size"] != float64(1) {
					t.Fatalf("size = %v, want floor 1", m["size"])
				}
			},
		},
		{
			name: "operator size overridden, operator _source overridden",
			body: `{"size": 500, "_source": true, "query": {"match_all": {}}}`,
			n:    3,
			check: func(t *testing.T, m map[string]any, raw []byte) {
				if m["size"] != float64(3) {
					t.Fatalf("size = %v, want 3 (operator value overridden)", m["size"])
				}
				if _, isList := m["_source"].([]any); !isList {
					t.Fatalf("_source = %v, want restricted field list", m["_source"])
				}
			},
		},
		{
			name: "newest-first sort injected when absent",
			body: `{"query": {"match_all": {}}}`,
			n:    3,
			check: func(t *testing.T, m map[string]any, raw []byte) {
				s := string(raw)
				if !strings.Contains(s, `"desc"`) || !strings.Contains(s, `"unmapped_type"`) {
					t.Fatalf("no newest-first sort injected: %s", s)
				}
			},
		},
		{
			name: "operator sort preserved",
			body: `{"sort": [{"severity": "asc"}], "query": {"match_all": {}}}`,
			n:    3,
			check: func(t *testing.T, m map[string]any, raw []byte) {
				if strings.Contains(string(raw), `"desc"`) {
					t.Fatalf("operator sort must be kept: %s", raw)
				}
			},
		},
		{
			name: "range-less query gains the 24h window (same core as RawSearch)",
			body: `{"query": {"query_string": {"query": "level:ERROR"}}}`,
			n:    3,
			check: func(t *testing.T, m map[string]any, raw []byte) {
				s := string(raw)
				if !strings.Contains(s, `"now-24h"`) {
					t.Fatalf("no 24h fallback window: %s", s)
				}
				if !strings.Contains(s, "query_string") {
					t.Fatalf("original query lost: %s", s)
				}
			},
		},
		{
			name: "existing range respected (no double window)",
			body: `{"query": {"bool": {"filter": [{"range": {"@timestamp": {"gte": "now-10m"}}}]}}}`,
			n:    3,
			check: func(t *testing.T, m map[string]any, raw []byte) {
				if strings.Contains(string(raw), "now-24h") {
					t.Fatalf("24h window injected despite existing range: %s", raw)
				}
			},
		},
		{
			name: "big integer literals preserved",
			body: `{"query": {"term": {"transaction.id": 9007199254740993}}}`,
			n:    3,
			check: func(t *testing.T, m map[string]any, raw []byte) {
				if !strings.Contains(string(raw), "9007199254740993") {
					t.Fatalf("integer literal corrupted: %s", raw)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := injectRawSampleGuards([]byte(tt.body), "@timestamp", "10s", tt.n, src)
			if err != nil {
				t.Fatalf("injectRawSampleGuards: %v", err)
			}
			tt.check(t, decodeBody(t, out), out)
		})
	}
	if _, err := injectRawSampleGuards([]byte(`{"query": `), "@timestamp", "10s", 3, src); err == nil {
		t.Fatal("want error on malformed body")
	}
}

func TestWatcherSampleLine(t *testing.T) {
	dec := func(t *testing.T, s string) map[string]any {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatalf("fixture: %v", err)
		}
		return m
	}
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "nested service path",
			src:  `{"@timestamp": "2026-07-24T08:00:01Z", "message": "boom", "service": {"name": "checkout"}}`,
			want: "2026-07-24T08:00:01Z [checkout] boom",
		},
		{
			name: "flat dotted key wins",
			src:  `{"@timestamp": "2026-07-24T08:00:01Z", "message": "boom", "service.name": "checkout"}`,
			want: "2026-07-24T08:00:01Z [checkout] boom",
		},
		{
			name: "missing service omitted",
			src:  `{"@timestamp": "2026-07-24T08:00:01Z", "message": "boom"}`,
			want: "2026-07-24T08:00:01Z boom",
		},
		{
			name: "missing timestamp omitted",
			src:  `{"message": "boom", "service": {"name": "checkout"}}`,
			want: "[checkout] boom",
		},
		{
			name: "newlines and runs of whitespace collapse to single spaces",
			src:  `{"message": "line one\n\tline two   spaced"}`,
			want: "line one line two spaced",
		},
		{
			name: "numeric timestamp stringified",
			src:  `{"@timestamp": 1753344000000, "message": "boom"}`,
			want: "1753344000000 boom",
		},
		{
			name: "empty source",
			src:  `{}`,
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := watcherSampleLine(dec(t, tt.src), "@timestamp", "service.name", "message")
			if got != tt.want {
				t.Fatalf("watcherSampleLine = %q, want %q", got, tt.want)
			}
		})
	}
	t.Run("truncated to 200 chars with ellipsis", func(t *testing.T) {
		long := strings.Repeat("x", 500)
		got := watcherSampleLine(dec(t, `{"message": "`+long+`"}`), "@timestamp", "service.name", "message")
		if len([]rune(got)) != 200 || !strings.HasSuffix(got, "…") {
			t.Fatalf("len = %d (suffix %q), want exactly 200 runes ending in …", len([]rune(got)), got[len(got)-3:])
		}
	})
}
