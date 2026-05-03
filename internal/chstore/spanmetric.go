package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SpanMetricFilter selects a slice of spans and turns them into a time-series
// metric (Tempo's span-metrics generator pattern). Optional groupBy keys
// produce one series per unique combination — Dynatrace-style MDA.
type SpanMetricFilter struct {
	Filters     []FilterExpr // span filter chips
	Aggregation string       // count | error_rate | rate | avg | sum | p50 | p95 | p99 | max | min
	Field       string       // attribute / column to aggregate (default: duration_ms)
	GroupBy     []string     // 0..N attribute names; same syntax as FilterExpr.Key
	From, To    time.Time
	StepSeconds int          // bucket size; if 0, auto-pick from time range
}

// SpanMetricSeries is one line on the chart — typically one per groupKey.
type SpanMetricSeries struct {
	GroupKey []string          `json:"groupKey"` // raw tuple, joined in UI
	Points   []SpanMetricPoint `json:"points"`
}

type SpanMetricPoint struct {
	Time  int64   `json:"time"`  // unix nanos (bucket start)
	Value float64 `json:"value"`
}

// QuerySpanMetric computes the requested aggregation over the matching spans,
// bucketed by step seconds, optionally split by 1+ group keys.
func (s *Store) QuerySpanMetric(ctx context.Context, f SpanMetricFilter) ([]SpanMetricSeries, error) {
	// ── Build WHERE ───────────────────────────────────────────────────────────
	var wc whereClause
	if !f.From.IsZero() {
		wc.add("time >= ?", f.From)
	}
	if !f.To.IsZero() {
		wc.add("time <= ?", f.To)
	}
	ApplyFilters(&wc, f.Filters)

	// ── Bucket size ───────────────────────────────────────────────────────────
	step := f.StepSeconds
	if step <= 0 {
		span := f.To.Sub(f.From).Seconds()
		switch {
		case span <= 600:        step = 10  // ≤10m → 10s
		case span <= 3600:       step = 30  // ≤1h  → 30s
		case span <= 6*3600:     step = 60  // ≤6h  → 1m
		case span <= 24*3600:    step = 300 // ≤1d  → 5m
		case span <= 7*24*3600:  step = 1800
		default:                 step = 3600
		}
	}

	// ── Aggregation expression ────────────────────────────────────────────────
	field := f.Field
	if field == "" {
		field = "duration_ms"
	}
	fieldExpr := fieldToSQL(field)
	aggExpr, err := aggToSQL(f.Aggregation, fieldExpr, step)
	if err != nil {
		return nil, err
	}

	// ── GroupBy expressions → single Array(String) tuple ──────────────────────
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
		// Group args go BEFORE the where-clause args because they appear
		// earlier in the SQL — fold them into the front of the arg list.
		wc.args = append(groupArgs, wc.args...)
	}

	// Note: toStartOfInterval returns DateTime (seconds precision), not
	// DateTime64 — multiply by 1e9 to get nanoseconds for the wire format.
	sql := fmt.Sprintf(`
		SELECT
		    toUnixTimestamp(toStartOfInterval(time, INTERVAL %d SECOND)) * 1000000000 AS bucket,
		    %s AS gk,
		    %s AS v
		FROM spans
		%s
		GROUP BY bucket, gk
		ORDER BY gk, bucket
		LIMIT 50000`, step, groupSelect, aggExpr, wc.sql())

	rows, err := s.conn.Query(ctx, sql, wc.args...)
	if err != nil {
		return nil, fmt.Errorf("query span metric: %w", err)
	}
	defer rows.Close()

	// Group adjacent rows into series (rows are sorted by gk then time)
	seriesMap := make(map[string]*SpanMetricSeries)
	var order []string
	for rows.Next() {
		// bucket comes back as UInt64 from `toUnixTimestamp() * 1e9`.
		var bucket uint64
		var gk []string
		var val *float64 // can be NULL when count = 0
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

// fieldToSQL maps a friendly field name to the underlying ClickHouse expression.
func fieldToSQL(field string) string {
	switch field {
	case "duration_ms", "duration":
		return "(duration / 1e6)"
	case "duration_s":
		return "(duration / 1e9)"
	case "1", "":
		return "1"
	default:
		// Treat as attribute lookup
		if col, ok := wellKnown[field]; ok {
			return "accurateCastOrNull(toString(" + col + "), 'Float64')"
		}
		// Fallback — span attr lookup
		return "accurateCastOrNull(attr_values[indexOf(attr_keys, '" + escapeStr(field) + "')], 'Float64')"
	}
}

// groupKeyExpr returns the SQL expression for one group key plus any extra
// query parameters it needs (for attribute lookups by name).
func groupKeyExpr(key string) (string, []any) {
	switch {
	case strings.HasPrefix(key, "resource."):
		return "toString(res_values[indexOf(res_keys, ?)])", []any{strings.TrimPrefix(key, "resource.")}
	case strings.HasPrefix(key, "span."):
		name := strings.TrimPrefix(key, "span.")
		if col, ok := wellKnown[name]; ok {
			return "toString(" + col + ")", nil
		}
		return "toString(attr_values[indexOf(attr_keys, ?)])", []any{name}
	default:
		if col, ok := wellKnown[key]; ok {
			return "toString(" + col + ")", nil
		}
		return "toString(attr_values[indexOf(attr_keys, ?)])", []any{key}
	}
}

// aggToSQL turns a friendly aggregation name into a ClickHouse expression.
// Whitelisted to avoid SQL injection via the URL parameter.
//
// Every result is wrapped in `toNullable(toFloat64(…))` so the scanner can use
// a single `*float64` for both nullable (quantile) and non-nullable (count)
// aggregations.
func aggToSQL(agg, field string, stepSec int) (string, error) {
	wrap := func(s string) string { return "toNullable(toFloat64(" + s + "))" }
	switch strings.ToLower(agg) {
	case "", "count":
		return wrap("count()"), nil
	case "rate":
		return wrap(fmt.Sprintf("count() / %d.0", stepSec)), nil
	case "error_rate":
		return wrap("100.0 * countIf(status_code = 'error') / count()"), nil
	case "errors":
		return wrap("countIf(status_code = 'error')"), nil
	case "sum":
		return wrap("sumOrNull(" + field + ")"), nil
	case "avg":
		return wrap("avgOrNull(" + field + ")"), nil
	case "min":
		return wrap("minOrNull(" + field + ")"), nil
	case "max":
		return wrap("maxOrNull(" + field + ")"), nil
	case "p50":
		return wrap("quantile(0.50)(" + field + ")"), nil
	case "p90":
		return wrap("quantile(0.90)(" + field + ")"), nil
	case "p95":
		return wrap("quantile(0.95)(" + field + ")"), nil
	case "p99":
		return wrap("quantile(0.99)(" + field + ")"), nil
	case "p999":
		return wrap("quantile(0.999)(" + field + ")"), nil
	}
	return "", fmt.Errorf("unknown aggregation %q", agg)
}

// escapeStr is a tiny helper for embedding a literal in raw SQL where we
// can't use a parameter (e.g. string concat in a SELECT expression).
// Limited to ASCII letters, digits, dot, underscore, dash — anything else
// is stripped to avoid quoting issues.
func escapeStr(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '-', r == ':':
			b.WriteRune(r)
		}
	}
	return b.String()
}
