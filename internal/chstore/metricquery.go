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
}

// QueryMetric runs a multi-series time-bucketed query against metric_points.
// Returns the same SpanMetricSeries shape so the UI can reuse MultiLineChart.
func (s *Store) QueryMetric(ctx context.Context, f MetricQueryFilter) ([]SpanMetricSeries, error) {
	if f.Name == "" {
		return nil, fmt.Errorf("metric name required")
	}

	var wc whereClause
	wc.add("metric = ?", f.Name)
	if f.Service != "" {
		wc.add("service_name = ?", f.Service)
	}
	if !f.From.IsZero() {
		wc.add("time >= ?", f.From)
	}
	if !f.To.IsZero() {
		wc.add("time <= ?", f.To)
	}
	ApplyFilters(&wc, f.Filters)

	step := f.StepSeconds
	if step <= 0 {
		// v0.5.259 — sub-10s auto-steps for short windows. ≤2min
		// now picks 1s (120 buckets), ≤10min picks 5s (120
		// buckets) — exposing the point-precision OTel metrics
		// actually carry instead of folding them into 10s
		// smears. Existing wider-window rungs unchanged so
		// 24h/7d/30d performance budgets stay intact.
		span := f.To.Sub(f.From).Seconds()
		switch {
		case span <= 120:        step = 1   // ≤2m   → 1s  (120 buckets)
		case span <= 600:        step = 5   // ≤10m  → 5s  (120 buckets)
		case span <= 1800:       step = 10  // ≤30m  → 10s (180 buckets)
		case span <= 3600:       step = 30  // ≤1h   → 30s
		case span <= 6*3600:     step = 60  // ≤6h   → 1m
		case span <= 24*3600:    step = 300 // ≤1d   → 5m
		case span <= 7*24*3600:  step = 1800
		default:                 step = 3600
		}
	}

	aggExpr, err := metricAggToSQL(f.Aggregation)
	if err != nil {
		return nil, err
	}

	groupSelect := "[]::Array(String)"
	if len(f.GroupBy) > 0 {
		parts := make([]string, len(f.GroupBy))
		var groupArgs []any
		for i, k := range f.GroupBy {
			expr, args := groupKeyExpr(k)
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
		LIMIT 50000`, step, groupSelect, aggExpr, wc.sql())

	rows, err := s.conn.Query(ctx, sql, wc.args...)
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
	expr, args := groupKeyExpr(key)
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
