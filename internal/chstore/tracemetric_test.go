package chstore

import (
	"strings"
	"testing"
)

// tracemetric_test.go — guards the v0.8.53 (doorway D4) tracemetrics planner.
// Pure formula + dim-mapping logic, table-tested so every agg and the
// three-dimension mapping stay correct without a live ClickHouse. The
// two-level SQL itself was cross-validated against ground truth on the live
// cluster (resolver trace count == independent grouped count).

func TestTraceDimColumn(t *testing.T) {
	ok := map[string]string{
		"service.name": "root_service", "service_name": "root_service",
		"http.route": "entry_route", "http_route": "entry_route", "entry_route": "entry_route",
		"name": "root_op", "operation": "root_op", "root_name": "root_op",
	}
	for key, want := range ok {
		got, isOK := traceDimColumn(key)
		if !isOK || got != want {
			t.Errorf("traceDimColumn(%q) = (%q,%v), want (%q,true)", key, got, isOK, want)
		}
	}
	// tracemetrics only carries three dims; everything else is unsupported.
	for _, key := range []string{"kind", "status", "db.system", "host.name", "anything"} {
		if _, isOK := traceDimColumn(key); isOK {
			t.Errorf("traceDimColumn(%q) should be unsupported (ok=false)", key)
		}
	}
}

func TestTraceMetricAgg(t *testing.T) {
	const step = 60
	cases := []struct {
		agg      string
		contains string
	}{
		{"count", "count()"},
		{"", "count()"},
		{"rate", "count() / 60.0"},
		{"errors", "countIf(err_spans > 0)"},
		{"error_rate", "100.0 * countIf(err_spans > 0) / nullIf(count(), 0)"},
		{"sum", "sum(dur_ns) / 1e6"},
		{"avg", "avg(dur_ns) / 1e6"},
		{"p50", "quantileTDigest(0.50)(dur_ns) / 1e6"},
		{"p90", "quantileTDigest(0.90)(dur_ns) / 1e6"},
		{"p95", "quantileTDigest(0.95)(dur_ns) / 1e6"},
		{"p99", "quantileTDigest(0.99)(dur_ns) / 1e6"},
	}
	for _, c := range cases {
		got, err := traceMetricAgg(c.agg, step)
		if err != nil {
			t.Errorf("traceMetricAgg(%q) unexpected error: %v", c.agg, err)
			continue
		}
		if !strings.Contains(got, c.contains) {
			t.Errorf("traceMetricAgg(%q) = %q, want to contain %q", c.agg, got, c.contains)
		}
		if !strings.HasPrefix(got, "toNullable(toFloat64(") {
			t.Errorf("traceMetricAgg(%q) = %q, missing toNullable(toFloat64 wrap", c.agg, got)
		}
	}
	if _, err := traceMetricAgg("bogus", step); err == nil {
		t.Error("traceMetricAgg(bogus) expected error, got nil")
	}
}

// Trace exemplars are plain argMax over the per-trace subquery (the trace_id IS
// the exemplar), not state merges — so the finalizers differ from spanmetrics.
func TestTraceMetricExemplarCols(t *testing.T) {
	got := traceMetricExemplarCols()
	if !strings.Contains(got, "argMax(trace_id, dur_ns) AS slow_trace") {
		t.Errorf("slow exemplar must be argMax(trace_id, dur_ns); got %q", got)
	}
	if !strings.Contains(got, "argMaxIf(trace_id, dur_ns, err_spans > 0) AS error_trace") {
		t.Errorf("error exemplar must be argMaxIf(trace_id, dur_ns, err_spans > 0); got %q", got)
	}
}
