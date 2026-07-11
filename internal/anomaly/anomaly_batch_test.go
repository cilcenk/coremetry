package anomaly

import (
	"strings"
	"testing"
)

// v0.8.507 — the anomaly scan's per-(service,metric) MV reads were an N+1
// fan-out: for services × trackedMetrics(3) → checkOne → one
// `WHERE service_name = ?` fetchBuckets + one seasonal read APIECE. Live
// query_log measured ~6365 fetchBuckets + 6366 seasonal queries/hour, each
// 46-65K read_rows (granule/part rounding re-read ~the whole window), ~708M
// rows/hr re-scanning the same data. scan() now issues ONE
// `GROUP BY service_name, t` batch per metric (×2 = 6 queries/tick) and
// distributes the per-service series to checkOne — the same pattern the
// evaluator adopted in v0.8.352. These tests pin the batch SQL shape (no
// service filter, GROUP BY service_name, bounds, MV, correct per-metric
// aggregate) and the pure distribution/skip contract, so a silent revert to
// the per-service fan-out fails here.

// TestBuildAllBucketsQueryShape asserts the batched consecutive query keeps
// every scale guardrail AND drops the per-service filter: it reads the MV (not
// raw spans), groups by service_name, is time-bounded top AND bottom (the
// v0.8.316 complete-buckets-only upper bound), carries a per-service + overall
// LIMIT and a max_execution_time, and binds exactly the two window edges.
func TestBuildAllBucketsQueryShape(t *testing.T) {
	sql := buildAllBucketsQuery("countMerge(span_count_state) / 300.0")

	mustContain := map[string]string{
		"MV read (not raw spans)": "service_summary_5m",
		"batch grouping":          "GROUP BY service_name, t",
		"lower time bound":        "time_bucket >= ?",
		"complete-buckets upper":  "time_bucket < ?",
		"per-service row cap":     "LIMIT 1000 BY service_name",
		"overall row cap":         "LIMIT 20000000",
		"execution-time bound":    "max_execution_time = 30",
		"deterministic order":     "ORDER BY service_name, t",
	}
	for label, sub := range mustContain {
		if !strings.Contains(sql, sub) {
			t.Errorf("batch buckets SQL missing %s: %q not found in\n%s", label, sub, sql)
		}
	}
	// MV-bypass invariant: must never scan raw spans.
	if strings.Contains(sql, "FROM spans") {
		t.Errorf("batch buckets SQL must read the MV, not raw spans:\n%s", sql)
	}
	// The per-service filter is GONE — one pass covers all services.
	if strings.Contains(sql, "service_name = ?") {
		t.Errorf("batched buckets SQL must NOT filter by a single service:\n%s", sql)
	}
	// Exactly two bind placeholders (cutoff, lastCompleteBucketStart).
	if n := strings.Count(sql, "?"); n != 2 {
		t.Errorf("batch buckets SQL must have 2 bind placeholders, got %d", n)
	}
}

// TestBatchQueriesUseCorrectMetricExpr pins that BOTH batch queries carry the
// SAME aggregate expression the per-service reads used, per metric — the change
// is purely "drop the service filter, group by service_name", never a change to
// how a metric is derived from the MV states. metricValueExpr is the single
// source: countMerge for the rate/error states, quantilesTDigestMerge (NOT the
// reservoir quantilesState) for p99.
func TestBatchQueriesUseCorrectMetricExpr(t *testing.T) {
	cases := []struct {
		metric string
		expr   string // the exact aggregate the batch SQL must embed
	}{
		{"error_rate", "countMerge(error_count_state) / nullIf(countMerge(span_count_state), 0) * 100"},
		{"request_rate", "countMerge(span_count_state) / 300.0"},
		{"p99_ms", "quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state)[3] / 1e6"},
	}
	for _, c := range cases {
		t.Run(c.metric, func(t *testing.T) {
			vexpr, err := metricValueExpr(c.metric)
			if err != nil {
				t.Fatalf("metricValueExpr(%q) error: %v", c.metric, err)
			}
			if vexpr != c.expr {
				t.Fatalf("metricValueExpr(%q) = %q, want %q", c.metric, vexpr, c.expr)
			}
			if b := buildAllBucketsQuery(vexpr); !strings.Contains(b, c.expr) {
				t.Errorf("buildAllBucketsQuery(%s) missing aggregate %q:\n%s", c.metric, c.expr, b)
			}
			if s := buildAllSeasonalQuery(vexpr); !strings.Contains(s, c.expr) {
				t.Errorf("buildAllSeasonalQuery(%s) missing aggregate %q:\n%s", c.metric, c.expr, s)
			}
		})
	}
}

// TestEnoughHistory pins the skip contract that stands in for the old
// per-service "fetch returned too few rows → return" guard: a service the batch
// returned no (or fewer than minSamples+dwellBuckets) rows for is skipped, so a
// service absent from the batch map behaves exactly as before.
func TestEnoughHistory(t *testing.T) {
	need := minSamples + dwellBuckets
	cases := []struct {
		n    int
		want bool
	}{
		{0, false},        // service absent from the batch → nil series
		{need - 1, false}, // one short of a full baseline + dwell window
		{need, true},      // exactly enough
		{need + 100, true},
	}
	for _, c := range cases {
		if got := enoughHistory(c.n); got != c.want {
			t.Errorf("enoughHistory(%d) = %v, want %v (need=%d)", c.n, got, c.want, need)
		}
	}
}

// TestAccumulateSeriesAndSeriesFor pins the batch distribution: scanned rows
// (arriving ordered by service_name, t) group into per-service slices in
// arrival order, and a service the batch never returned yields nil via
// seriesFor — which enoughHistory then skips. This is the "empty service /
// missing bucket behaves as before" contract, without a live ClickHouse.
func TestAccumulateSeriesAndSeriesFor(t *testing.T) {
	byService := make(map[string][]float64)
	// Two services interleaved-then-grouped exactly as `ORDER BY service_name,
	// t` delivers them: all of svc-a's buckets, then all of svc-b's.
	rows := []struct {
		svc string
		v   float64
	}{
		{"svc-a", 1}, {"svc-a", 2}, {"svc-a", 3},
		{"svc-b", 10}, {"svc-b", 11},
	}
	for _, r := range rows {
		accumulateSeries(byService, r.svc, r.v)
	}

	// svc-a: three buckets, preserved in arrival (ascending-time) order.
	if got := seriesFor(byService, "svc-a"); len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Errorf("seriesFor(svc-a) = %v, want [1 2 3] in order", got)
	}
	// svc-b: two buckets.
	if got := seriesFor(byService, "svc-b"); len(got) != 2 || got[1] != 11 {
		t.Errorf("seriesFor(svc-b) = %v, want [10 11]", got)
	}
	// A service the batch never returned → nil, which enoughHistory skips.
	if got := seriesFor(byService, "svc-missing"); got != nil {
		t.Errorf("seriesFor(missing service) = %v, want nil", got)
	}
	if enoughHistory(len(seriesFor(byService, "svc-missing"))) {
		t.Error("a missing service (nil series) must be skipped by enoughHistory")
	}
	// A nil map (the metric's whole batch read errored this tick) → nil series
	// for every service → skipped, matching the old per-service fetch-error path.
	if got := seriesFor(nil, "svc-a"); got != nil {
		t.Errorf("seriesFor(nil map) = %v, want nil", got)
	}
}
