package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// MetricQueryFilter is a Grafana-style query against metric_points.
// Same shape as SpanMetricFilter but targets the metrics table.
type MetricQueryFilter struct {
	Name        string       // metric name (required)
	Service     string       // shortcut filter on service_name
	Filters     []FilterExpr // arbitrary attribute filters (resource.X / span.X also supported)
	GroupBy     []string     // 0..N attribute keys → multi-line series
	Aggregation string       // avg | sum | min | max | last | p50 | p95 | p99 (default: avg)
	From, To    time.Time
	StepSeconds int
	// MaxDataPoints (F1, v0.9.105) — panel pixel width ≈ target bucket count.
	// When > 0 and StepSeconds is auto (≤0), the step is pixel-adaptive
	// (rangeSec/maxDataPoints, snapped) instead of the fixed span ladder, so
	// wide windows expose the sub-bucket resolution OTLP metrics carry. 0 =
	// px unknown → fixed ladder. clampStepToExport still caps the LOWER bound.
	MaxDataPoints int
}

// buildMetricQuerySQL builds the metric_points query SQL + bound args for a
// MetricQueryFilter. Extracted as a pure function (v0.8.4) so the CH-bounds
// guards are unit-testable without a live ClickHouse — see metricquery_test.go.
//
// Scale-hardening (v0.8.4, scale-audit): the metric_points GROUP BY scan
// behind the Grafana-style MetricQueryEditor MUST satisfy the CLAUDE.md
// CH-bounds constraint — LIMIT (had it), a time-bounded WHERE that prunes
// partitions (was CONDITIONAL — absent when from/to were zero), and
// SETTINGS max_execution_time (was MISSING, unlike the QueryMetricHistogram
// twin). A degenerate call with no window scanned every partition unbounded.
// Now the window always defaults to the last 24h and max_execution_time caps
// wall-clock at 30s, mirroring metrichist.go.
func buildMetricQuerySQL(f MetricQueryFilter, now time.Time) (string, []any, error) {
	// Always time-bound: default the window so `time >= ?` / `time <= ?`
	// below can prune partitions even on a from/to-less call.
	if f.To.IsZero() {
		f.To = now
	}
	if f.From.IsZero() {
		f.From = f.To.Add(-24 * time.Hour)
	}

	var wc whereClause
	wc.add("metric = ?", f.Name)
	if f.Service != "" {
		wc.add("service_name = ?", f.Service)
	}
	wc.add("time >= ?", f.From)
	wc.add("time <= ?", f.To)
	// v0.8.381 — metric_points lacks most spans-typed columns; the
	// metric-aware variant reroutes those keys to array lookups
	// (deployment.environment picked in the Explore filter 500'd).
	ApplyMetricFilters(&wc, f.Filters)

	step := f.StepSeconds
	if step <= 0 {
		// Fallback for direct/test callers that didn't pre-resolve the step
		// (QueryMetric sets f.StepSeconds first). v0.9.105 (F1) — pixel-
		// adaptive when MaxDataPoints>0, else the fixed span ladder (which
		// still exposes sub-10s resolution for short windows, v0.5.259).
		step = metricAutoStepPx(f.From, f.To, f.MaxDataPoints)
	}

	aggExpr, err := metricAggToSQL(f.Aggregation)
	if err != nil {
		return "", nil, err
	}

	groupSelect := "[]::Array(String)"
	if len(f.GroupBy) > 0 {
		parts := make([]string, len(f.GroupBy))
		var groupArgs []any
		for i, k := range f.GroupBy {
			// metric_points path: op_group isn't a metric dimension, so pass
			// true to keep the expression byte-identical to pre-v0.8.187.
			expr, args := groupKeyExpr(k, true)
			parts[i] = expr
			groupArgs = append(groupArgs, args...)
		}
		groupSelect = "[" + strings.Join(parts, ", ") + "]"
		wc.args = append(groupArgs, wc.args...)
	}

	sql := fmt.Sprintf(`
		SELECT
		    toUnixTimestamp(toStartOfInterval(time, INTERVAL %d SECOND)) * 1000000000 AS bucket,
		    %s AS gk,
		    %s AS v
		FROM metric_points
		%s
		GROUP BY bucket, gk
		ORDER BY gk, bucket
		LIMIT 50000
		SETTINGS max_execution_time = 30`, step, groupSelect, aggExpr, wc.sql())
	return sql, wc.args, nil
}

// QueryMetric runs a multi-series time-bucketed query against metric_points.
// Returns the same SpanMetricSeries shape so the UI can reuse MultiLineChart.
func (s *Store) QueryMetric(ctx context.Context, f MetricQueryFilter) ([]SpanMetricSeries, error) {
	if f.Name == "" {
		return nil, fmt.Errorf("metric name required")
	}

	// v0.8.243 — min-step clamp: never bucket finer than the metric's
	// observed export cadence (Grafana's $__rate_interval equivalent —
	// see metric_export_interval.go). Normalize the window + auto-step
	// HERE with the same rules buildMetricQuerySQL applies internally,
	// so the clamp sees the effective step, not the raw 0=auto.
	now := time.Now()
	if f.To.IsZero() {
		f.To = now
	}
	if f.From.IsZero() {
		f.From = f.To.Add(-24 * time.Hour)
	}
	if f.StepSeconds <= 0 {
		// v0.9.105 (F1) — pixel-adaptif; MaxDataPoints=0 ise eski ladder.
		f.StepSeconds = metricAutoStepPx(f.From, f.To, f.MaxDataPoints)
	}
	if iv := s.metricExportInterval(ctx, f.Name, f.Service); iv > 0 {
		f.StepSeconds = clampStepToExport(f.StepSeconds, iv)
	}

	sql, args, err := buildMetricQuerySQL(f, now)
	if err != nil {
		return nil, err
	}

	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("metric query: %w", err)
	}
	defer rows.Close()

	seriesMap := make(map[string]*SpanMetricSeries)
	var order []string
	for rows.Next() {
		var bucket uint64
		var gk []string
		var val *float64
		if err := rows.Scan(&bucket, &gk, &val); err != nil {
			return nil, err
		}
		key := strings.Join(gk, "|")
		s, ok := seriesMap[key]
		if !ok {
			s = &SpanMetricSeries{GroupKey: gk}
			seriesMap[key] = s
			order = append(order, key)
		}
		v := 0.0
		if val != nil {
			v = *val
		}
		s.Points = append(s.Points, SpanMetricPoint{Time: int64(bucket), Value: v})
	}
	out := make([]SpanMetricSeries, 0, len(order))
	for _, k := range order {
		out = append(out, *seriesMap[k])
	}
	return out, rows.Err()
}

func metricAggToSQL(agg string) (string, error) {
	wrap := func(s string) string { return "toNullable(toFloat64(" + s + "))" }
	switch strings.ToLower(agg) {
	case "", "avg":
		return wrap("avgOrNull(value)"), nil
	case "sum":
		return wrap("sumOrNull(value)"), nil
	case "min":
		return wrap("minOrNull(value)"), nil
	case "max":
		return wrap("maxOrNull(value)"), nil
	case "last":
		return wrap("argMaxOrNull(value, time)"), nil
	case "p50":
		return wrap("quantile(0.50)(value)"), nil
	case "p95":
		return wrap("quantile(0.95)(value)"), nil
	case "p99":
		return wrap("quantile(0.99)(value)"), nil
	}
	return "", fmt.Errorf("unknown aggregation %q", agg)
}

// MetricLabelValues returns distinct values for a single attribute key
// observed in the given metric — fuels the value-suggestions in the UI.
func (s *Store) MetricLabelValues(ctx context.Context, metric, key string, since time.Duration) ([]string, error) {
	if metric == "" || key == "" {
		return nil, nil
	}
	expr, args := groupKeyExpr(key, true) // metric_points labels; op_group inert here
	cutoff := time.Now().Add(-since)
	queryArgs := append(args, metric, cutoff)
	rows, err := s.conn.Query(ctx,
		`SELECT DISTINCT `+expr+` AS v
		 FROM metric_points
		 WHERE metric = ? AND time >= ?
		 ORDER BY v
		 LIMIT 200`, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		if v != "" {
			out = append(out, v)
		}
	}
	return out, rows.Err()
}
