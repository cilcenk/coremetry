package mcptools

// pivots_test.go — v0.8.333 (pivot Phase 4, MCP parity). First test file in
// internal/mcptools: table-driven coverage of the pivot tools' pure arg
// helpers (hex validation, window clamp), the handlers' validation paths
// (which return BEFORE any store access, so Deps{} suffices), and the
// get_logs_for_trace degraded semantics against a stub logstore.Store —
// a slow/unreachable log backend must yield {degraded:true} as a RESULT,
// never a tool error, so the copilot can reason about the condition.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// ─── pure helpers ──────────────────────────────────────────────

func TestIsHexLen(t *testing.T) {
	cases := []struct {
		name string
		s    string
		n    int
		want bool
	}{
		{"valid 32-hex trace id", "0123456789abcdef0123456789abcdef", 32, true},
		{"valid 16-hex span id", "0123456789abcdef", 16, true},
		{"too short", "abc", 32, false},
		{"too long", strings.Repeat("a", 33), 32, false},
		{"uppercase rejected at helper level", "0123456789ABCDEF0123456789ABCDEF", 32, false},
		{"non-hex char g", "g123456789abcdef0123456789abcdef", 32, false},
		{"hyphenated uuid form", "01234567-89ab-cdef-0123-456789abcdef", 32, false},
		{"empty", "", 32, false},
		{"all zeros still shape-valid", strings.Repeat("0", 32), 32, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isHexLen(tc.s, tc.n); got != tc.want {
				t.Fatalf("isHexLen(%q, %d) = %v, want %v", tc.s, tc.n, got, tc.want)
			}
		})
	}
}

func TestNormalizeTraceID(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"lowercase passes through", "0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef", false},
		{"uppercase lowered at the edge", "0123456789ABCDEF0123456789ABCDEF", "0123456789abcdef0123456789abcdef", false},
		{"surrounding whitespace trimmed", "  0123456789abcdef0123456789abcdef ", "0123456789abcdef0123456789abcdef", false},
		{"short id rejected", "abcdef", "", true},
		{"empty rejected", "", "", true},
		{"garbage rejected", "not-a-trace-id-not-a-trace-id-xx", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeTraceID(tc.in)
			if tc.wantErr != (err != nil) {
				t.Fatalf("normalizeTraceID(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("normalizeTraceID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestClampWindowS(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero means default", 0, 900},
		{"negative means default", -5, 900},
		{"below floor clamps to 60", 30, 60},
		{"floor exact", 60, 60},
		{"in-band passes through", 600, 600},
		{"default value in-band", 900, 900},
		{"ceiling exact", 3600, 3600},
		{"above ceiling clamps to 3600", 86400, 3600},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampWindowS(tc.in); got != tc.want {
				t.Fatalf("clampWindowS(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// ─── stub logstore.Store ───────────────────────────────────────

// stubLogStore implements logstore.Store. searchErr non-nil → Search fails
// with it; otherwise Search returns page and records the Filter it saw so
// tests can assert the tool's arg plumbing (ids lowered, limit clamped).
type stubLogStore struct {
	page      *logstore.Page
	searchErr error
	gotFilter logstore.Filter
}

func (s *stubLogStore) Search(_ context.Context, f logstore.Filter) (*logstore.Page, error) {
	s.gotFilter = f
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	return s.page, nil
}

func (s *stubLogStore) CountPatterns(context.Context, []logstore.PatternSpec, time.Time, time.Time, time.Time) ([]logstore.PatternStats, error) {
	return nil, nil
}
func (s *stubLogStore) Histogram(context.Context, logstore.Filter, int, string) ([]logstore.LogSeries, error) {
	return nil, nil
}
func (s *stubLogStore) EQLSearch(context.Context, logstore.EQLQuery) ([]logstore.EQLSequence, error) {
	return nil, nil
}
func (s *stubLogStore) Indices(context.Context) ([]logstore.IndexInfo, error) { return nil, nil }
func (s *stubLogStore) FieldValues(context.Context, string, string, int) ([]string, error) {
	return nil, nil
}
func (s *stubLogStore) FieldStats(context.Context, logstore.Filter, string, int) (*logstore.FieldStatsResult, error) {
	return nil, nil
}
func (s *stubLogStore) Backend() string            { return "stub" }
func (s *stubLogStore) Ping(context.Context) error { return nil }

// callTool finds a tool by name in ToolList(d) and invokes its handler.
func callTool(t *testing.T, d Deps, name, rawArgs string) (any, error) {
	t.Helper()
	for _, tool := range ToolList(d) {
		if tool.Name == name {
			return tool.Handler(context.Background(), json.RawMessage(rawArgs))
		}
	}
	t.Fatalf("tool %q not in ToolList", name)
	return nil, nil
}

const validTID = "0123456789abcdef0123456789abcdef"

// ─── handler arg validation (returns before any store access) ──

func TestPivotToolArgValidation(t *testing.T) {
	// A benign stub so get_logs_for_trace clears the nil-LogStore gate and
	// reaches its validation branches. Store stays nil: every case below
	// must error out BEFORE the tool touches chstore.
	d := Deps{LogStore: &stubLogStore{page: &logstore.Page{}}}
	cases := []struct {
		name    string
		tool    string
		args    string
		wantSub string // substring the error must contain
	}{
		{"logs: malformed json", "get_logs_for_trace", `{"trace_id":`, "decode args"},
		{"logs: missing trace_id", "get_logs_for_trace", `{}`, "trace_id must be 32 hex"},
		{"logs: short trace_id", "get_logs_for_trace", `{"trace_id":"abc"}`, "trace_id must be 32 hex"},
		{"logs: non-hex trace_id", "get_logs_for_trace", `{"trace_id":"zzzz56789abcdef0123456789abcdef0"}`, "trace_id must be 32 hex"},
		{"logs: bad span_id", "get_logs_for_trace", `{"trace_id":"` + validTID + `","span_id":"xyz"}`, "span_id must be 16 hex"},
		{"exemplars: missing metric", "get_exemplar_traces", `{"service":"checkout"}`, "metric is required"},
		{"exemplars: whitespace metric", "get_exemplar_traces", `{"metric":"  ","service":"checkout"}`, "metric is required"},
		{"exemplars: missing service", "get_exemplar_traces", `{"metric":"http.server.request.duration"}`, "service is required"},
		{"linked: missing trace_id", "get_linked_traces", `{}`, "trace_id must be 32 hex"},
		{"linked: uuid-shaped trace_id", "get_linked_traces", `{"trace_id":"01234567-89ab-cdef-0123-456789abcd"}`, "trace_id must be 32 hex"},
		{"winmetrics: missing service", "get_metrics_for_span", `{"at_unix_ns":1700000000000000000}`, "service is required"},
		{"winmetrics: missing at_unix_ns", "get_metrics_for_span", `{"service":"checkout"}`, "at_unix_ns is required"},
		{"winmetrics: negative at_unix_ns", "get_metrics_for_span", `{"service":"checkout","at_unix_ns":-1}`, "at_unix_ns is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := callTool(t, d, tc.tool, tc.args)
			if err == nil {
				t.Fatalf("%s(%s): want error containing %q, got nil", tc.tool, tc.args, tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("%s(%s): error %q does not contain %q", tc.tool, tc.args, err.Error(), tc.wantSub)
			}
		})
	}
}

func TestGetLogsForTraceNilLogStore(t *testing.T) {
	_, err := callTool(t, Deps{}, "get_logs_for_trace", `{"trace_id":"`+validTID+`"}`)
	if err == nil || !strings.Contains(err.Error(), "log backend not configured") {
		t.Fatalf("nil LogStore: want 'log backend not configured' error, got %v", err)
	}
}

// ─── degraded semantics (slow backend → result, not error) ─────

func TestGetLogsForTraceDegraded(t *testing.T) {
	// A deadline-exceeded from the backend classifies as ErrBackendSlow in
	// logstore.SearchWithTimeout; the tool must convert that into a
	// structured degraded RESULT — nil error — so the copilot reasons
	// "logs unavailable right now", not "the tool is broken".
	stub := &stubLogStore{searchErr: context.DeadlineExceeded}
	res, err := callTool(t, Deps{LogStore: stub}, "get_logs_for_trace",
		`{"trace_id":"`+validTID+`"}`)
	if err != nil {
		t.Fatalf("degraded path must not be a tool error, got %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("want map result, got %T", res)
	}
	if m["degraded"] != true {
		t.Fatalf("want degraded=true, got %v", m["degraded"])
	}
	reason, _ := m["reason"].(string)
	if !strings.Contains(reason, "log backend slow/unreachable") {
		t.Fatalf("want reason mentioning the slow backend, got %q", reason)
	}
	if m["count"] != 0 {
		t.Fatalf("degraded result must carry count=0, got %v", m["count"])
	}
}

func TestGetLogsForTraceGenuineErrorPassesThrough(t *testing.T) {
	// A non-slow backend failure (bad query, ES 400, …) is a bug to surface
	// — it must stay a tool error, NOT be masked as degraded.
	stub := &stubLogStore{searchErr: errQuery{}}
	_, err := callTool(t, Deps{LogStore: stub}, "get_logs_for_trace",
		`{"trace_id":"`+validTID+`"}`)
	if err == nil {
		t.Fatal("genuine query error must pass through as a tool error, got nil")
	}
	if !strings.Contains(err.Error(), "mapping mismatch") {
		t.Fatalf("want the original query error surfaced, got %v", err)
	}
}

// errQuery is a genuine (non-timeout, non-transport) backend failure.
type errQuery struct{}

func (errQuery) Error() string { return "bad field: mapping mismatch" }

// ─── happy-path plumbing (ids lowered, limit clamped, span narrowing) ─

func TestGetLogsForTracePlumbing(t *testing.T) {
	upperTID := strings.ToUpper(validTID)
	cases := []struct {
		name      string
		args      string
		wantTID   string
		wantSID   string
		wantLimit int
	}{
		{"default limit is 100", `{"trace_id":"` + validTID + `"}`, validTID, "", 100},
		{"limit clamps at 500", `{"trace_id":"` + validTID + `","limit":9999}`, validTID, "", 500},
		{"explicit in-band limit kept", `{"trace_id":"` + validTID + `","limit":25}`, validTID, "", 25},
		{"uppercase ids lowered", `{"trace_id":"` + upperTID + `","span_id":"0123456789ABCDEF"}`, validTID, "0123456789abcdef", 100},
		{"span_id narrows the filter", `{"trace_id":"` + validTID + `","span_id":"0123456789abcdef"}`, validTID, "0123456789abcdef", 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubLogStore{page: &logstore.Page{Total: 3, Logs: nil}}
			res, err := callTool(t, Deps{LogStore: stub}, "get_logs_for_trace", tc.args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			f := stub.gotFilter
			if f.TraceID != tc.wantTID {
				t.Fatalf("Filter.TraceID = %q, want %q", f.TraceID, tc.wantTID)
			}
			if f.SpanID != tc.wantSID {
				t.Fatalf("Filter.SpanID = %q, want %q", f.SpanID, tc.wantSID)
			}
			if f.Limit != tc.wantLimit {
				t.Fatalf("Filter.Limit = %d, want %d", f.Limit, tc.wantLimit)
			}
			m := res.(map[string]any)
			if m["degraded"] != false {
				t.Fatalf("healthy backend must report degraded=false, got %v", m["degraded"])
			}
			if m["total"] != 3 {
				t.Fatalf("want total=3 echoed from the page, got %v", m["total"])
			}
		})
	}
}
