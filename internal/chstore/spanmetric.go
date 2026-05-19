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
	// ── MV fast-path (v0.5.268) ───────────────────────────────────────────────
	// When the query maps onto service_summary_5m's columns
	// (group by service.name only, step ≥ 5min, no
	// attribute filters, agg in the MV's state set), route to
	// the MV. Same eligibility shape GetTraceAggregate uses
	// (line 1521 in repo.go) — sub-second on billion-row
	// installs where the raw GROUP BY would otherwise burn
	// 5-10s of CH time. Fall through on MV error so a
	// regression here doesn't blank the page.
	if rows, ok := s.tryServiceMVFastPath(ctx, f); ok {
		return rows, nil
	}

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
		// v0.5.259 — same sub-10s ramp as metricquery.go.
		span := f.To.Sub(f.From).Seconds()
		switch {
		case span <= 120:        step = 1   // ≤2m   → 1s
		case span <= 600:        step = 5   // ≤10m  → 5s
		case span <= 1800:       step = 10  // ≤30m  → 10s
		case span <= 3600:       step = 30  // ≤1h   → 30s
		case span <= 6*3600:     step = 60  // ≤6h   → 1m
		case span <= 24*3600:    step = 300 // ≤1d   → 5m
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

// tryServiceMVFastPath (v0.5.268) routes eligible
// QuerySpanMetric queries to service_summary_5m. Eligibility
// gate:
//
//   • step ≥ 300s (the MV's bucket granularity; we re-bucket
//     bigger windows via toStartOfInterval on time_bucket)
//   • GroupBy is empty OR exactly ["service.name"]
//   • Filters all key on service.name with op = (the MV only
//     has service_name as a dimension; any other predicate
//     would need raw spans)
//   • Aggregation is one the MV's states can serve:
//     count, rate, error_rate, errors, avg, p50, p95, p99
//
// Returns (series, true) on a successful MV read; (nil, false)
// when the query isn't eligible or the MV query errors so the
// caller falls through to the raw-spans path.
//
// Same numerical model the /api/services page already serves
// (quantilesMergeState / countMerge); the operator's quantile
// estimate is consistent across surfaces.
func (s *Store) tryServiceMVFastPath(ctx context.Context, f SpanMetricFilter) ([]SpanMetricSeries, bool) {
	// Auto-step preview — mirrors the switch below so the
	// eligibility check matches the bucket we'd actually run.
	step := f.StepSeconds
	if step <= 0 {
		span := f.To.Sub(f.From).Seconds()
		switch {
		case span <= 24*3600:
			// auto would pick something sub-5min; not eligible.
			return nil, false
		case span <= 7*24*3600:
			step = 1800
		default:
			step = 3600
		}
	}
	if step < 300 {
		return nil, false
	}

	// GroupBy gate.
	switch len(f.GroupBy) {
	case 0:
	case 1:
		if f.GroupBy[0] != "service.name" && f.GroupBy[0] != "service_name" {
			return nil, false
		}
	default:
		return nil, false
	}

	// Filter gate — only service.name = X allowed; everything
	// else needs raw spans.
	var serviceFilter string
	for _, fe := range f.Filters {
		if (fe.Key == "service.name" || fe.Key == "service_name") && fe.Op == "=" && len(fe.Values) == 1 {
			serviceFilter = fe.Values[0]
			continue
		}
		return nil, false
	}

	// Aggregation gate.
	field := f.Field
	if field == "" {
		field = "duration_ms"
	}
	if field != "duration_ms" {
		// MV only has duration; non-duration aggs can't use it.
		return nil, false
	}
	var aggExpr string
	switch f.Aggregation {
	case "", "count":
		aggExpr = "toNullable(toFloat64(countMerge(span_count_state)))"
	case "rate":
		aggExpr = fmt.Sprintf("toNullable(toFloat64(countMerge(span_count_state)) / %d.0 * 60.0)", step)
	case "error_rate":
		aggExpr = "toNullable(toFloat64(countMerge(error_count_state)) / nullIf(toFloat64(countMerge(span_count_state)), 0))"
	case "errors":
		aggExpr = "toNullable(toFloat64(countMerge(error_count_state)))"
	case "avg":
		aggExpr = "toNullable(toFloat64(sumMerge(duration_sum_state)) / nullIf(toFloat64(countMerge(span_count_state)), 0) / 1e6)"
	case "p50":
		aggExpr = "toNullable(toFloat64(arrayElement(quantilesMerge(0.5, 0.95, 0.99)(duration_q_state), 1) / 1e6))"
	case "p95":
		aggExpr = "toNullable(toFloat64(arrayElement(quantilesMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6))"
	case "p99":
		aggExpr = "toNullable(toFloat64(arrayElement(quantilesMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6))"
	default:
		return nil, false
	}

	// Build the query. We re-bucket the MV's 5min slots into
	// the operator's requested step via toStartOfInterval on
	// time_bucket. WHERE clause on time_bucket prunes
	// partitions efficiently.
	groupSelect := "[]::Array(String)"
	if len(f.GroupBy) == 1 {
		groupSelect = "[service_name]"
	}
	var whereClauses []string
	args := []any{f.From, f.To}
	whereClauses = append(whereClauses, "time_bucket >= ?", "time_bucket <= ?")
	if serviceFilter != "" {
		whereClauses = append(whereClauses, "service_name = ?")
		args = append(args, serviceFilter)
	}

	sql := fmt.Sprintf(`
		SELECT
		    toUnixTimestamp(toStartOfInterval(time_bucket, INTERVAL %d SECOND)) * 1000000000 AS bucket,
		    %s AS gk,
		    %s AS v
		FROM service_summary_5m
		WHERE %s
		GROUP BY bucket, gk
		ORDER BY gk, bucket
		LIMIT 50000
		SETTINGS max_execution_time = 30`,
		step, groupSelect, aggExpr, strings.Join(whereClauses, " AND "))

	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	seriesMap := make(map[string]*SpanMetricSeries)
	var order []string
	for rows.Next() {
		var bucket uint64
		var gk []string
		var val *float64
		if err := rows.Scan(&bucket, &gk, &val); err != nil {
			return nil, false
		}
		key := strings.Join(gk, "|")
		ser, ok := seriesMap[key]
		if !ok {
			ser = &SpanMetricSeries{GroupKey: gk}
			seriesMap[key] = ser
			order = append(order, key)
		}
		v := 0.0
		if val != nil {
			v = *val
		}
		ser.Points = append(ser.Points, SpanMetricPoint{Time: int64(bucket), Value: v})
	}
	if err := rows.Err(); err != nil {
		return nil, false
	}
	out := make([]SpanMetricSeries, 0, len(order))
	for _, k := range order {
		out = append(out, *seriesMap[k])
	}
	return out, true
}

// SpanMetricBatchFilter computes N aggregations over the same
// span selection in a single CH query. Drives the Service
// detail page's "rate + error_rate + p99" chart row (and the
// compare-period twin) — three independent QuerySpanMetric
// calls fanned out into one CH pass over the spans table.
// Cold-cache time drops from ~3 × singleN to ~1 × singleN.
//
// All aggregations share the SAME GroupBy + StepSeconds +
// filters; they only differ in (Name, Aggregation, Field).
// Name is the operator's label for the result key in the
// response map — callers pick something stable ("rate",
// "error_rate", "p99") so the frontend can address each
// series without inspecting types.
type SpanMetricBatchFilter struct {
	Filters     []FilterExpr
	GroupBy     []string
	From, To    time.Time
	StepSeconds int
	Aggs        []SpanMetricAggSpec
}

type SpanMetricAggSpec struct {
	Name        string // result key, e.g. "rate" / "error_rate" / "p99"
	Aggregation string // count | error_rate | rate | avg | sum | p50 | p95 | p99 | max | min
	Field       string // attribute / column when aggregation needs one (default duration_ms)
}

// QuerySpanMetricMulti runs every aggregation in `f.Aggs`
// against the same WHERE + GROUP BY in ONE round trip. Returns
// a map keyed by spec.Name → series list. Empty result map on
// success is allowed (no spans matched the filter); per-spec
// failures (e.g. unknown aggregation) fail the whole call.
func (s *Store) QuerySpanMetricMulti(ctx context.Context, f SpanMetricBatchFilter) (map[string][]SpanMetricSeries, error) {
	if len(f.Aggs) == 0 {
		return map[string][]SpanMetricSeries{}, nil
	}
	// ── Build WHERE ───────────────────────────────────────────────────────────
	var wc whereClause
	if !f.From.IsZero() {
		wc.add("time >= ?", f.From)
	}
	if !f.To.IsZero() {
		wc.add("time <= ?", f.To)
	}
	ApplyFilters(&wc, f.Filters)

	// ── Bucket size (same auto-pick as QuerySpanMetric) ───────────────────────
	step := f.StepSeconds
	if step <= 0 {
		span := f.To.Sub(f.From).Seconds()
		switch {
		case span <= 600:        step = 10
		case span <= 3600:       step = 30
		case span <= 6*3600:     step = 60
		case span <= 24*3600:    step = 300
		case span <= 7*24*3600:  step = 1800
		default:                 step = 3600
		}
	}

	// Build one SELECT expression per aggregation, aliased
	// with `v0` / `v1` / `v2`. Position-aliasing avoids a
	// name-collision when the operator picks names that
	// happen to match SQL keywords (`count`, `rate`).
	selectParts := []string{
		fmt.Sprintf(
			"toUnixTimestamp(toStartOfInterval(time, INTERVAL %d SECOND)) * 1000000000 AS bucket",
			step),
	}
	for i, a := range f.Aggs {
		field := a.Field
		if field == "" {
			field = "duration_ms"
		}
		expr, err := aggToSQL(a.Aggregation, fieldToSQL(field), step)
		if err != nil {
			return nil, fmt.Errorf("agg %q: %w", a.Name, err)
		}
		selectParts = append(selectParts, fmt.Sprintf("%s AS v%d", expr, i))
	}

	// GroupBy → single Array(String) tuple (same path as
	// QuerySpanMetric so the result-shape stays familiar).
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
	selectParts = append(selectParts, groupSelect+" AS gk")

	sql := fmt.Sprintf(`
		SELECT %s
		FROM spans
		%s
		GROUP BY bucket, gk
		ORDER BY gk, bucket
		LIMIT 50000`,
		strings.Join(selectParts, ", "),
		wc.sql())

	rows, err := s.conn.Query(ctx, sql, wc.args...)
	if err != nil {
		return nil, fmt.Errorf("query span metric multi: %w", err)
	}
	defer rows.Close()

	// Per-agg accumulator: agg_name → (key→series).
	type acc struct {
		seriesMap map[string]*SpanMetricSeries
		order     []string
	}
	accs := make([]acc, len(f.Aggs))
	for i := range accs {
		accs[i].seriesMap = map[string]*SpanMetricSeries{}
	}

	for rows.Next() {
		// Scan one row of: bucket, v0..vN, gk.
		dest := make([]any, 0, 2+len(f.Aggs))
		var bucket uint64
		dest = append(dest, &bucket)
		vals := make([]*float64, len(f.Aggs))
		for i := range vals {
			dest = append(dest, &vals[i])
		}
		var gk []string
		dest = append(dest, &gk)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		key := strings.Join(gk, "|")
		for i, v := range vals {
			ser, ok := accs[i].seriesMap[key]
			if !ok {
				ser = &SpanMetricSeries{GroupKey: append([]string{}, gk...)}
				accs[i].seriesMap[key] = ser
				accs[i].order = append(accs[i].order, key)
			}
			f := 0.0
			if v != nil {
				f = *v
			}
			ser.Points = append(ser.Points, SpanMetricPoint{Time: int64(bucket), Value: f})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make(map[string][]SpanMetricSeries, len(f.Aggs))
	for i, a := range f.Aggs {
		list := make([]SpanMetricSeries, 0, len(accs[i].order))
		for _, k := range accs[i].order {
			list = append(list, *accs[i].seriesMap[k])
		}
		out[a.Name] = list
	}
	return out, nil
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
