package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.8.352 (perf P2-A) — the alert evaluator's per-(rule, service) reads
// (~70k queries/hour measured on a 100-service install) collapsed into one
// GROUP BY service_name query per (metric, window). These tests pin:
//
//  1. the ROUTING — which backing store serves which metric (basic RED →
//     service_summary_5m; mq_* transport → spanmetrics_1m via its kind
//     dimension; http_/db_/rpc_ transport → one bounded raw-spans GROUP BY,
//     because their predicates need http_method / http_status / db_system /
//     rpc_system which are not spanmetrics_1m dimensions);
//  2. the SQL SHAPE — every batched read is GROUP BY service_name,
//     time-bounded on the aligned window arg, LIMIT'd, and wall-clock
//     capped, per the CLAUDE.md CH hard constraints;
//  3. the v0.8.315 aligned-window math, which moved here from
//     internal/evaluator so the batched and per-service paths share ONE
//     implementation.

func TestMeasureAllServicesPlanRouting(t *testing.T) {
	cases := []struct {
		metric   string
		source   string
		scan     measureAllScan
		contains []string
	}{
		// ── basic RED → service_summary_5m merge states ─────────────
		{"error_rate", "service_summary_5m", scanNullableFloat,
			[]string{"countMerge(error_count_state)", "nullIf(toFloat64(countMerge(span_count_state)),0) * 100"}},
		{"error_count", "service_summary_5m", scanCountScaled,
			[]string{"countMerge(error_count_state)"}},
		{"request_rate", "service_summary_5m", scanCountRate,
			[]string{"countMerge(span_count_state)"}},
		{"avg_ms", "service_summary_5m", scanNullableFloat,
			[]string{"sumMerge(duration_sum_state)"}},
		// Quantile index matches service_summary_5m's
		// quantilesTDigestState(0.5, 0.95, 0.99): 1=p50, 2=p95, 3=p99.
		{"p50_ms", "service_summary_5m", scanFloat,
			[]string{"quantilesTDigestMerge(0.5,0.95,0.99)(duration_q_state), 1)"}},
		{"p95_ms", "service_summary_5m", scanFloat,
			[]string{"quantilesTDigestMerge(0.5,0.95,0.99)(duration_q_state), 2)"}},
		{"p99_ms", "service_summary_5m", scanFloat,
			[]string{"quantilesTDigestMerge(0.5,0.95,0.99)(duration_q_state), 3)"}},

		// ── mq_* transport → spanmetrics_1m (kind IS a dimension) ───
		// Quantile index matches spanmetrics_1m's quantilesTDigestState
		// (0.5, 0.9, 0.95, 0.99) — NOT the summary MV's grid: 4=p99, 3=p95.
		{"mq_consume_p99_ms", "spanmetrics_1m", scanFloat,
			[]string{"kind='consumer'", "quantilesTDigestMerge(0.5,0.9,0.95,0.99)(duration_q_state), 4)"}},
		{"mq_consume_p95_ms", "spanmetrics_1m", scanFloat,
			[]string{"kind='consumer'", "quantilesTDigestMerge(0.5,0.9,0.95,0.99)(duration_q_state), 3)"}},
		{"mq_publish_p50_ms", "spanmetrics_1m", scanFloat,
			[]string{"kind='producer'", "quantilesTDigestMerge(0.5,0.9,0.95,0.99)(duration_q_state), 1)"}},
		{"mq_publish_error_rate", "spanmetrics_1m", scanNullableFloat,
			[]string{"kind='producer'", "countMerge(error_state)", "nullIf(toFloat64(countMerge(calls_state)),0) * 100"}},
		{"mq_consume_avg_ms", "spanmetrics_1m", scanNullableFloat,
			[]string{"kind='consumer'", "sumMerge(duration_sum_state)"}},
		{"mq_consume_count", "spanmetrics_1m", scanCountRaw,
			[]string{"kind='consumer'", "countMerge(calls_state)"}},

		// ── http_/db_/rpc_ transport → ONE bounded raw-spans GROUP BY ──
		// (predicates not expressible on spanmetrics_1m's dimensions).
		{"http_p99_ms", "spans", scanFloat,
			[]string{"quantile(0.99)(duration)", "kind='server' AND http_method != ''"}},
		{"http_5xx_rate", "spans", scanNullableFloat,
			[]string{"countIf(http_status >= 500)", "kind='server' AND http_method != ''"}},
		{"http_4xx_rate", "spans", scanNullableFloat,
			[]string{"countIf(http_status >= 400 AND http_status < 500)"}},
		{"db_p99_ms", "spans", scanFloat,
			[]string{"quantile(0.99)(duration)", "db_system != ''"}},
		{"db_avg_ms", "spans", scanFloat,
			[]string{"avg(duration)", "db_system != ''"}},
		{"db_count", "spans", scanCountRaw,
			[]string{"count()", "db_system != ''"}},
		{"rpc_error_rate", "spans", scanNullableFloat,
			[]string{"countIf(status_code='error')", "rpc_system != ''"}},
	}
	for _, c := range cases {
		t.Run(c.metric, func(t *testing.T) {
			plan, err := measureAllServicesPlan(c.metric)
			if err != nil {
				t.Fatalf("plan(%q): %v", c.metric, err)
			}
			if plan.source != c.source {
				t.Fatalf("plan(%q).source = %q, want %q", c.metric, plan.source, c.source)
			}
			if plan.scan != c.scan {
				t.Fatalf("plan(%q).scan = %d, want %d", c.metric, plan.scan, c.scan)
			}
			// Hard-constraint shape: batched, bounded, time-bounded on
			// the single aligned-window bind arg.
			timeBound := "time_bucket >= ?"
			if c.source == "spans" {
				timeBound = "time >= ?"
			}
			for _, want := range append([]string{
				"FROM " + c.source,
				"GROUP BY service_name",
				"LIMIT 100000",
				"max_execution_time = 10",
				timeBound,
			}, c.contains...) {
				if !strings.Contains(plan.sql, want) {
					t.Errorf("plan(%q) SQL missing %q\n--- SQL ---\n%s", c.metric, want, plan.sql)
				}
			}
			if strings.Count(plan.sql, "?") != 1 {
				t.Errorf("plan(%q) must bind exactly the aligned window start, got %d binds\n--- SQL ---\n%s",
					c.metric, strings.Count(plan.sql, "?"), plan.sql)
			}
			// The MV routes must never touch raw spans and vice versa.
			if c.source != "spans" && strings.Contains(plan.sql, "FROM spans") {
				t.Errorf("plan(%q) routed to %s but reads raw spans\n--- SQL ---\n%s", c.metric, c.source, plan.sql)
			}
		})
	}
}

func TestMeasureAllServicesPlanUnknownMetric(t *testing.T) {
	for _, m := range []string{"bogus_metric", "anomaly_ratio", "log_query", ""} {
		if _, err := measureAllServicesPlan(m); err == nil {
			t.Errorf("plan(%q) must fail, got nil error", m)
		}
	}
}

// TestSpanmetricsTransportWhere pins the transport→MV mapping decision:
// ONLY the mq_* families ride spanmetrics_1m. http_* needs http_method
// (+ http_status for 4xx/5xx), db_* needs db_system, rpc_* needs
// rpc_system — none are spanmetrics_1m dimensions, so mapping any of them
// to the MV would silently measure the wrong population.
func TestSpanmetricsTransportWhere(t *testing.T) {
	cases := []struct {
		metric string
		where  string
		ok     bool
	}{
		{"mq_publish_error_rate", "kind='producer'", true},
		{"mq_publish_p99_ms", "kind='producer'", true},
		{"mq_consume_p99_ms", "kind='consumer'", true},
		{"mq_consume_count", "kind='consumer'", true},
		{"http_p99_ms", "", false},
		{"http_5xx_rate", "", false},
		{"http_4xx_rate", "", false},
		{"db_p99_ms", "", false},
		{"db_error_rate", "", false},
		{"rpc_error_rate", "", false},
		{"error_rate", "", false},
		{"p99_ms", "", false},
	}
	for _, c := range cases {
		t.Run(c.metric, func(t *testing.T) {
			where, ok := spanmetricsTransportWhere(c.metric)
			if where != c.where || ok != c.ok {
				t.Fatalf("spanmetricsTransportWhere(%q) = (%q, %v), want (%q, %v)",
					c.metric, where, ok, c.where, c.ok)
			}
		})
	}
}

// TestTransportMappingSingleSource pins the TransportFilter/TransportOp
// tables through the v0.8.352 move from internal/evaluator (whose wrappers
// now delegate here) — a drift during the move would silently change which
// spans every transport alert measures.
func TestTransportMappingSingleSource(t *testing.T) {
	filters := []struct {
		metric, where, numerator string
		ok                       bool
	}{
		{"http_5xx_rate", "kind='server' AND http_method != ''", "http_status >= 500", true},
		{"http_4xx_rate", "kind='server' AND http_method != ''", "http_status >= 400 AND http_status < 500", true},
		{"http_p99_ms", "kind='server' AND http_method != ''", "status_code='error'", true},
		{"db_p99_ms", "db_system != ''", "status_code='error'", true},
		{"rpc_error_rate", "rpc_system != ''", "status_code='error'", true},
		{"mq_publish_error_rate", "kind='producer'", "status_code='error'", true},
		{"mq_consume_p99_ms", "kind='consumer'", "status_code='error'", true},
		{"error_rate", "", "", false},
		{"request_rate", "", "", false},
	}
	for _, c := range filters {
		where, numerator, ok := TransportFilter(c.metric)
		if where != c.where || numerator != c.numerator || ok != c.ok {
			t.Errorf("TransportFilter(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.metric, where, numerator, ok, c.where, c.numerator, c.ok)
		}
	}
	ops := map[string]string{
		"http_5xx_rate":         "error_rate",
		"http_p99_ms":           "p99_ms",
		"db_p95_ms":             "p95_ms",
		"db_p50_ms":             "p50_ms",
		"mq_consume_avg_ms":     "avg_ms",
		"mq_publish_count":      "count",
		"mq_publish_error_rate": "error_rate",
		"weird_metric":          "",
	}
	for metric, want := range ops {
		if got := TransportOp(metric); got != want {
			t.Errorf("TransportOp(%q) = %q, want %q", metric, got, want)
		}
	}
}

// TestAlignedWindowExports mirrors internal/evaluator/mv_window_test.go on
// the chstore exports: the v0.8.315 contract (align DOWN — over-cover,
// never under-cover; normalize counts back to the nominal window) must
// survive the v0.8.352 move unchanged. The evaluator's wrappers delegate
// here, so its own test file pins the same math transitively.
func TestAlignedWindowExports(t *testing.T) {
	at := func(h, m, s int) time.Time {
		return time.Date(2026, 7, 6, h, m, s, 0, time.UTC)
	}
	if got := MVWindowStart(at(10, 7, 0), 5*time.Minute); !got.Equal(at(10, 0, 0)) {
		t.Fatalf("MVWindowStart mid-bucket = %s, want 10:00", got)
	}
	if got := MVWindowStart(at(10, 9, 59), 5*time.Minute); !got.Equal(at(10, 0, 0)) {
		t.Fatalf("MVWindowStart worst drift = %s, want 10:00", got)
	}
	if got := MVWindowStart(at(10, 7, 0), 10*time.Minute); !got.Equal(at(9, 55, 0)) {
		t.Fatalf("MVWindowStart 10m = %s, want 09:55", got)
	}
	if got := MVCoveredSeconds(at(10, 7, 0), 5*time.Minute); got != 420 {
		t.Fatalf("MVCoveredSeconds = %v, want 420", got)
	}
	if got := ScaleToWindow(840, 300, 420); got != 600 {
		t.Fatalf("ScaleToWindow(840,300,420) = %v, want 600", got)
	}
	if got := ScaleToWindow(840, 300, 0); got != 840 {
		t.Fatalf("ScaleToWindow zero-covered must fall back to raw, got %v", got)
	}
	// Boundary: the batched reads refuse sub-5m windows (the evaluator
	// keeps its per-service raw path there).
	if UseSummaryMV(4*time.Minute + 59*time.Second) {
		t.Fatal("UseSummaryMV(4m59s) must be false")
	}
	if !UseSummaryMV(5 * time.Minute) {
		t.Fatal("UseSummaryMV(5m) must be true")
	}
	if UseSummaryMV(0) {
		t.Fatal("UseSummaryMV(0) must be false")
	}
}
