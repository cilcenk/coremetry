// injectRawSearchGuards tests — watcher import Faz-1 (v0.9.x). The
// operator's watch body passes through verbatim EXCEPT the cost
// guards: size:0 forced (only the count matters), per-body soft
// timeout, capped-but-sufficient track_total_hits (threshold*2 from
// the caller so a gte compare above 10k is counted correctly — the
// 10k-saturation bug class), and a 24h range fallback when the body
// carries no time filter (the EQL mandatory-window discipline).
package logstore

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeBody(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("guarded body is not valid JSON: %v\n%s", err, b)
	}
	return m
}

func TestInjectRawSearchGuards(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		cap      int
		check    func(t *testing.T, m map[string]any, raw []byte)
	}{
		{
			name: "guards injected into a bare query",
			body: `{"query": {"query_string": {"query": "level:ERROR"}}}`,
			cap:  200,
			check: func(t *testing.T, m map[string]any, raw []byte) {
				if m["size"] != float64(0) {
					t.Fatalf("size = %v, want 0", m["size"])
				}
				if m["timeout"] != "10s" {
					t.Fatalf("timeout = %v, want 10s default", m["timeout"])
				}
				if m["track_total_hits"] != float64(200) {
					t.Fatalf("track_total_hits = %v, want 200", m["track_total_hits"])
				}
			},
		},
		{
			name: "size always forced to zero",
			body: `{"size": 500, "query": {"match_all": {}}}`,
			cap:  100,
			check: func(t *testing.T, m map[string]any, raw []byte) {
				if m["size"] != float64(0) {
					t.Fatalf("size = %v, want 0 (operator value overridden)", m["size"])
				}
			},
		},
		{
			name: "existing timeout preserved",
			body: `{"timeout": "3s", "query": {"match_all": {}}}`,
			cap:  100,
			check: func(t *testing.T, m map[string]any, raw []byte) {
				if m["timeout"] != "3s" {
					t.Fatalf("timeout = %v, want operator's 3s kept", m["timeout"])
				}
			},
		},
		{
			name: "track_total_hits takes MAX of body and cap",
			body: `{"track_total_hits": 50000, "query": {"match_all": {}}}`,
			cap:  200,
			check: func(t *testing.T, m map[string]any, raw []byte) {
				if m["track_total_hits"] != float64(50000) {
					t.Fatalf("track_total_hits = %v, want 50000 (body larger)", m["track_total_hits"])
				}
			},
		},
		{
			name: "track_total_hits smaller than cap is raised",
			body: `{"track_total_hits": 10, "query": {"match_all": {}}}`,
			cap:  200,
			check: func(t *testing.T, m map[string]any, raw []byte) {
				if m["track_total_hits"] != float64(200) {
					t.Fatalf("track_total_hits = %v, want raised to 200", m["track_total_hits"])
				}
			},
		},
		{
			name: "track_total_hits true kept (exact count)",
			body: `{"track_total_hits": true, "query": {"match_all": {}}}`,
			cap:  200,
			check: func(t *testing.T, m map[string]any, raw []byte) {
				if m["track_total_hits"] != true {
					t.Fatalf("track_total_hits = %v, want true kept", m["track_total_hits"])
				}
			},
		},
		{
			name: "track_total_hits false replaced by cap",
			body: `{"track_total_hits": false, "query": {"match_all": {}}}`,
			cap:  200,
			check: func(t *testing.T, m map[string]any, raw []byte) {
				if m["track_total_hits"] != float64(200) {
					t.Fatalf("track_total_hits = %v, want cap (false would hide the count)", m["track_total_hits"])
				}
			},
		},
		{
			name: "cap floor of 10",
			body: `{"query": {"match_all": {}}}`,
			cap:  0,
			check: func(t *testing.T, m map[string]any, raw []byte) {
				if m["track_total_hits"] != float64(10) {
					t.Fatalf("track_total_hits = %v, want 10 floor", m["track_total_hits"])
				}
			},
		},
		{
			name: "range-less query gains a 24h fallback window",
			body: `{"query": {"query_string": {"query": "level:ERROR"}}}`,
			cap:  100,
			check: func(t *testing.T, m map[string]any, raw []byte) {
				s := string(raw)
				if !strings.Contains(s, `"now-24h"`) || !strings.Contains(s, `"@timestamp"`) {
					t.Fatalf("no 24h range fallback injected: %s", s)
				}
				// The original clause must survive inside the wrapper.
				if !strings.Contains(s, "query_string") {
					t.Fatalf("original query lost: %s", s)
				}
			},
		},
		{
			name: "existing range filter is respected (no double window)",
			body: `{"query": {"bool": {"filter": [{"range": {"@timestamp": {"gte": "now-5m"}}}]}}}`,
			cap:  100,
			check: func(t *testing.T, m map[string]any, raw []byte) {
				if strings.Contains(string(raw), "now-24h") {
					t.Fatalf("24h fallback injected despite existing range: %s", raw)
				}
			},
		},
		{
			name: "empty body becomes guarded match-all count",
			body: ``,
			cap:  100,
			check: func(t *testing.T, m map[string]any, raw []byte) {
				if m["size"] != float64(0) {
					t.Fatalf("size = %v, want 0", m["size"])
				}
				if !strings.Contains(string(raw), "now-24h") {
					t.Fatalf("empty body must still get the 24h window: %s", raw)
				}
			},
		},
		{
			name: "query-less body with aggs gets window as bare query",
			body: `{"aggs": {"x": {"terms": {"field": "service"}}}}`,
			cap:  100,
			check: func(t *testing.T, m map[string]any, raw []byte) {
				s := string(raw)
				if !strings.Contains(s, "now-24h") {
					t.Fatalf("no fallback window: %s", s)
				}
				if !strings.Contains(s, "aggs") {
					t.Fatalf("aggs stripped: %s", s)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _, err := injectRawSearchGuards([]byte(tt.body), "@timestamp", "10s", tt.cap)
			if err != nil {
				t.Fatalf("injectRawSearchGuards: %v", err)
			}
			tt.check(t, decodeBody(t, out), out)
		})
	}
}

func TestInjectRawSearchGuardsBadBody(t *testing.T) {
	if _, _, err := injectRawSearchGuards([]byte(`{"query": `), "@timestamp", "10s", 100); err == nil {
		t.Fatal("want error on malformed body")
	}
	if _, _, err := injectRawSearchGuards([]byte(`[1,2]`), "@timestamp", "10s", 100); err == nil {
		t.Fatal("want error on non-object body")
	}
}

// Review F4 (v0.9.x) regression: map[string]any + float64 silently
// corrupted int64-scale literals on re-marshal (9007199254740993 →
// …992; 19-digit epoch-nanos bounds shifted by ~256ns) — breaking the
// file's own VERBATIM contract with zero indication. UseNumber keeps
// every numeric literal byte-exact.
func TestInjectRawSearchGuardsPreservesBigIntegers(t *testing.T) {
	body := `{"query": {"bool": {"must": [
		{"term": {"transaction.id": 9007199254740993}},
		{"range": {"ts_nanos": {"gte": 1753000000123456789}}}
	]}}}`
	out, _, err := injectRawSearchGuards([]byte(body), "@timestamp", "10s", 100)
	if err != nil {
		t.Fatalf("injectRawSearchGuards: %v", err)
	}
	for _, lit := range []string{"9007199254740993", "1753000000123456789"} {
		if !strings.Contains(string(out), lit) {
			t.Fatalf("integer literal %s corrupted in guarded body: %s", lit, out)
		}
	}
}

// Review F11 (v0.9.x) regression: a negative cap is the exact-count
// sentinel — track_total_hits must become `true` (a numeric cap near
// the ES int limit would land below the threshold and kill the
// compare), including over an operator-set numeric or false.
func TestInjectRawSearchGuardsExactCountSentinel(t *testing.T) {
	for _, body := range []string{
		`{"query": {"match_all": {}}}`,
		`{"track_total_hits": 10000, "query": {"match_all": {}}}`,
		`{"track_total_hits": false, "query": {"match_all": {}}}`,
	} {
		out, _, err := injectRawSearchGuards([]byte(body), "@timestamp", "10s", -1)
		if err != nil {
			t.Fatalf("injectRawSearchGuards: %v", err)
		}
		if m := decodeBody(t, out); m["track_total_hits"] != true {
			t.Fatalf("track_total_hits = %v, want true (exact-count sentinel) for body %s", m["track_total_hits"], body)
		}
	}
}

// Review F3 (v0.9.x) regression: a range under must_not EXCLUDES a
// window (all remaining retention stays in scope) and a should range
// is optional/scoring — neither bounds the search, so the 24h
// fallback must still be injected. Before the fix these bodies
// shipped as genuinely unbounded whole-retention counts.
func TestInjectRawSearchGuardsWindowsNonPositiveRanges(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"range only under must_not", `{"query": {"bool": {"must": [{"term": {"level": "ERROR"}}], "must_not": [{"range": {"@timestamp": {"gte": "now-10m"}}}]}}}`},
		{"range only under should beside a must", `{"query": {"bool": {"must": [{"term": {"level": "ERROR"}}], "should": [{"range": {"@timestamp": {"gte": "now-1h"}}}]}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, injected, err := injectRawSearchGuards([]byte(tt.body), "@timestamp", "10s", 100)
			if err != nil {
				t.Fatalf("injectRawSearchGuards: %v", err)
			}
			if !injected || !strings.Contains(string(out), "now-24h") {
				t.Fatalf("24h fallback missing (injected=%v): %s", injected, out)
			}
		})
	}
	// Control: a positive-context range still suppresses injection.
	out, injected, err := injectRawSearchGuards(
		[]byte(`{"query": {"bool": {"filter": [{"range": {"@timestamp": {"gte": "now-5m"}}}]}}}`),
		"@timestamp", "10s", 100)
	if err != nil {
		t.Fatalf("injectRawSearchGuards: %v", err)
	}
	if injected || strings.Contains(string(out), "now-24h") {
		t.Fatalf("positive-context range must suppress the fallback: %s", out)
	}
}

// prodShapeBody is the search body of the anonymized real-prod watch
// fixture (internal/watcher prodShapeWatch): range under bool.filter —
// a POSITIVE context — next to a nested bool.should. The watch's own
// 10m window must be respected (no 24h injection).
const prodShapeBody = `{"query":{"bool":{"must":[],"filter":[{"range":{"@timestamp":{"gte":"now-10m"}}},{"bool":{"should":[{"match_phrase":{"message":"EventAlreadyExistError"}}],"minimum_should_match":1}},{"match_phrase":{"kubernetes.container_name":"digital-core-prod"}}]}}}`

func TestInjectRawSearchGuardsProdShapeBody(t *testing.T) {
	out, injected, err := injectRawSearchGuards([]byte(prodShapeBody), "@timestamp", "10s", 10)
	if err != nil {
		t.Fatalf("injectRawSearchGuards: %v", err)
	}
	if injected || strings.Contains(string(out), "now-24h") {
		t.Fatalf("prod-shape body carries its own filter range — no 24h injection expected: %s", out)
	}
	if !strings.Contains(string(out), "EventAlreadyExistError") || !strings.Contains(string(out), "now-10m") {
		t.Fatalf("operator query mutated: %s", out)
	}
}

func TestHasRangeClause(t *testing.T) {
	tests := []struct {
		name string
		q    string
		want bool
	}{
		{"top-level range", `{"range": {"@timestamp": {"gte": "now-5m"}}}`, true},
		{"nested in bool filter", `{"bool": {"filter": [{"range": {"ts": {"gte": 1}}}]}}`, true},
		{"nested in must array", `{"bool": {"must": [{"term": {"a": "b"}}, {"range": {"t": {"lte": 2}}}]}}`, true},
		{"no range", `{"bool": {"must": [{"query_string": {"query": "x"}}]}}`, false},
		{"field literally named range is still a range key", `{"term": {"level": "ERROR"}}`, false},
		// Review F3 (v0.9.x): non-positive contexts never count.
		{"range under must_not does not bound", `{"bool": {"must_not": [{"range": {"@timestamp": {"gte": "now-10m"}}}]}}`, false},
		{"range under should does not bound", `{"bool": {"must": [{"term": {"a": "b"}}], "should": [{"range": {"t": {"gte": 1}}}]}}`, false},
		{"sole should range still does not bound (flat rule)", `{"bool": {"should": [{"range": {"t": {"gte": 1}}}]}}`, false},
		{"positive range beside a must_not range counts", `{"bool": {"filter": [{"range": {"ts": {"gte": 1}}}], "must_not": [{"range": {"x": {"lt": 5}}}]}}`, true},
		{"range inside nested positive bool chain", `{"bool": {"must": [{"bool": {"filter": [{"range": {"ts": {"gte": 1}}}]}}]}}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v any
			if err := json.Unmarshal([]byte(tt.q), &v); err != nil {
				t.Fatalf("fixture: %v", err)
			}
			if got := hasRangeClause(v); got != tt.want {
				t.Fatalf("hasRangeClause = %v, want %v", got, tt.want)
			}
		})
	}
}
