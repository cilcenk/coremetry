package chstore

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// spanMetricTopN is the server-side ceiling on the number of series
// QuerySpanMetric returns on a high-cardinality groupBy. It is set to the
// frontend's TOP_N_MAX (the operator can pick at most this many "top" series
// in PanelStack) so the trimmed set is a SUPERSET of anything the UI will
// display — the frontend ranks by the SAME area metric (sum of abs(value))
// and slices to ≤ this cap, so displayed lines are byte-identical while the
// wire payload drops from thousands of series to at most this many.
const spanMetricTopN = 50

// seriesArea is the ranking weight used by both the server trim and the
// frontend cap: the sum of the absolute point values of a series. Bigger
// area = more visually significant line.
func seriesArea(s SpanMetricSeries) float64 {
	var a float64
	for _, p := range s.Points {
		a += math.Abs(p.Value)
	}
	return a
}

// trimTopNByArea returns the input untouched when it already fits within n,
// otherwise ranks by area (sum of abs(point value)) descending and keeps the
// top n. The full series count BEFORE trimming is returned as `total` so the
// caller can surface an accurate "+N more" to the operator. A stable sort
// keeps the original (gk-then-time) ordering among equal-area series so the
// result is deterministic across calls.
func trimTopNByArea(series []SpanMetricSeries, n int) (kept []SpanMetricSeries, total int) {
	total = len(series)
	if total <= n {
		return series, total
	}
	ranked := make([]SpanMetricSeries, len(series))
	copy(ranked, series)
	sort.SliceStable(ranked, func(i, j int) bool {
		return seriesArea(ranked[i]) > seriesArea(ranked[j])
	})
	return ranked[:n], total
}

// SpanMetricFilter selects a slice of spans and turns them into a time-series
// metric (Tempo's span-metrics generator pattern). Optional groupBy keys
// produce one series per unique combination — Dynatrace-style MDA.
type SpanMetricFilter struct {
	Filters     []FilterExpr // span filter chips
	// FilterRoot is the optional grouped AND/OR builder (v0.8.x gap-2,
	// extended into Explore). When non-nil, QuerySpanMetric routes the
	// predicate through ApplyFilterGroup INSTEAD of ApplyFilters(f.Filters),
	// so the operator can express `(http.status >= 500 OR db.system = oracle)
	// AND env = prod` in an Explore panel. Mirrors how repo.go's TraceFilter
	// gained FilterRoot. A flat-AND FilterRoot is byte-identical to the legacy
	// Filters path (ApplyFilterGroup delegates flat-AND to ApplyFilters); an
	// OR / nested group disqualifies the MV fast-paths (it can't ride the
	// service_summary_5m / operation_summary_5m rollups, same cost class as a
	// free-text Search), so it falls to the bounded raw-spans GROUP BY.
	FilterRoot  *FilterGroup
	Aggregation string       // count | error_rate | rate | avg | sum | p50 | p95 | p99 | max | min
	Field       string       // attribute / column to aggregate (default: duration_ms)
	GroupBy     []string     // 0..N attribute names; same syntax as FilterExpr.Key
	From, To    time.Time
	StepSeconds int          // bucket size; if 0, auto-pick from time range
	// v0.6.32 — free-text search predicate. Same shape as
	// GetTraces' search HAVING (positionCaseInsensitive across
	// name / http_route / http_method+route concat / attr
	// values). Operator-reported: /traces span-volume histogram
	// counted 929 spans for a service while the trace list with
	// `search=SELECT * FROM FND_USER` showed only 3 traces — the
	// histogram wasn't honouring the search filter. Pushing it
	// down at the WHERE level makes the histogram's total
	// agree with the spans the search actually selects.
	Search string
}

// effectiveFastPathFilters returns the flat []FilterExpr the MV fast-path
// gates (tryServiceMVFastPath / tryOperationMVFastPath) should inspect. The
// fast-paths only ever accept `service.name = X` predicates, and they're only
// reached when the root is nil or flat-AND (see QuerySpanMetric's gate), so:
//   - FilterRoot == nil           → the legacy f.Filters slice
//   - FilterRoot is flat-AND       → FilterRoot.Filters (the AND-joined leaves,
//     which is exactly what ApplyFilters would emit), so a flat-AND group with
//     a `service.name = X` leaf stays MV-eligible — byte-identical to passing
//     that leaf via f.Filters.
// A non-flat root never reaches here (the gate disables the fast-paths first),
// but for safety we return f.Filters unchanged in that case.
func (f SpanMetricFilter) effectiveFastPathFilters() []FilterExpr {
	if f.FilterRoot != nil && f.FilterRoot.isFlatAnd() {
		return f.FilterRoot.Filters
	}
	return f.Filters
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

// QuerySpanMetricTopN runs QuerySpanMetric and, on a high-cardinality groupBy,
// trims the result to the spanMetricTopN biggest-by-area series — the exact set
// the frontend would render anyway (PanelStack ranks by the same area metric and
// caps at TOP_N_MAX). `total` is the series count BEFORE trimming so the UI's
// "+N more" stays accurate even though the wire payload is bounded.
//
// Only the primary /api/spans/metric handler uses this. The resolver, DQL, RED
// and batch paths keep calling QuerySpanMetric directly (they either already
// bound cardinality or need every series), so their behaviour is unchanged.
func (s *Store) QuerySpanMetricTopN(ctx context.Context, f SpanMetricFilter) (series []SpanMetricSeries, total int, err error) {
	all, err := s.QuerySpanMetric(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	kept, total := trimTopNByArea(all, spanMetricTopN)
	return kept, total, nil
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
	// v0.6.32 — search predicate bypasses the MV fast-paths.
	// service_summary_5m / operation_summary_5m don't store
	// attr_values or http_route, so a search clause can't be
	// honoured against them. Same gate shape GetTraces uses
	// (repo.go line ~1177).
	// v0.8.x gap-2 (Explore) — an OR / nested FilterRoot ALSO bypasses the
	// fast-paths: the MV rollups can't represent boolean OR structure (same
	// cost class as Search). FilterRoot.IsFlatAnd() is true for nil and for a
	// pure AND group, so the legacy + flat-AND paths still ride the MV.
	if f.Search == "" && f.FilterRoot.IsFlatAnd() {
		if rows, ok := s.tryServiceMVFastPath(ctx, f); ok {
			return rows, nil
		}
		if rows, ok := s.tryOperationMVFastPath(ctx, f); ok {
			return rows, nil
		}
	}

	// ── Build WHERE ───────────────────────────────────────────────────────────
	var wc whereClause
	if !f.From.IsZero() {
		wc.add("time >= ?", f.From)
	}
	if !f.To.IsZero() {
		wc.add("time <= ?", f.To)
	}
	// v0.8.x gap-2 (Explore) — grouped AND/OR builder supersedes the flat
	// Filters when present. A flat-AND FilterRoot routes straight through
	// ApplyFilters inside ApplyFilterGroup, so the legacy path stays
	// byte-identical; an OR / nested group emits a single parenthesised
	// conjunct. Same shape repo.go's buildGetTracesWhere uses.
	if f.FilterRoot != nil {
		ApplyFilterGroup(&wc, *f.FilterRoot)
	} else {
		ApplyFilters(&wc, f.Filters)
	}
	// v0.6.32 — free-text search at WHERE level, applied per-span (not
	// per-trace) so the histogram total matches the search-narrowed set the
	// traces table shows. v0.8.x — shares searchPredicate with GetTraces:
	// ALL-tokens match over the combined haystack, so both surfaces narrow
	// identically.
	if pred, pargs := searchPredicate(f.Search); pred != "" {
		wc.add(pred, pargs...)
	}

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
			expr, args := groupKeyExpr(k, s.hasOpGroupCol)
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
		LIMIT 50000
		SETTINGS max_execution_time = 30`, step, groupSelect, aggExpr, wc.sql())

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
	// else needs raw spans. effectiveFastPathFilters resolves a flat-AND
	// FilterRoot's leaves to the same slice ApplyFilters would emit, so a
	// grouped flat-AND `service.name = X` still rides the MV (v0.8.x gap-2).
	var serviceFilter string
	for _, fe := range f.effectiveFastPathFilters() {
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
	case "per_min":
		// Per-minute throughput (Uptrace perMin). count over the step / step
		// seconds × 60. v0.8.x forced add alongside the legacy `rate`.
		aggExpr = fmt.Sprintf("toNullable(toFloat64(countMerge(span_count_state)) / %d.0 * 60.0)", step)
	case "apdex":
		// Apdex score from the MV's satisfied/tolerating states (Uptrace
		// apdex()). T is fixed at MV build time (apdex_satisfied = dur ≤ T,
		// tolerating = T < dur ≤ 4T). v0.8.x.
		aggExpr = "toNullable((toFloat64(countMerge(apdex_satisfied_state)) + toFloat64(countMerge(apdex_tolerating_state)) / 2) / nullIf(toFloat64(countMerge(span_count_state)), 0))"
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

// tryOperationMVFastPath (v0.5.269) routes eligible
// QuerySpanMetric queries to operation_summary_5m — the same
// pattern as tryServiceMVFastPath but with operation as the
// second dimension. Powers "RED by operation" style queries
// (DQL: `spans | summarize p99(duration_ms) by service.name,
// name, bin(time, 5m)`) without ever touching raw spans.
//
// Eligibility:
//   • step ≥ 300s
//   • GroupBy contains "name" (operation), optionally
//     "service.name". The two-key set ["service.name","name"]
//     splits per (service, operation); ["name"] alone is
//     valid when a service filter pins the scope.
//   • Filters are all service.name = X with op =.
//   • Agg in the MV's state set (same as the service fast-path).
//
// When GroupBy is just ["name"] without a service filter we
// reject — the MV would return cross-service operation rows
// which probably isn't what the operator meant.
func (s *Store) tryOperationMVFastPath(ctx context.Context, f SpanMetricFilter) ([]SpanMetricSeries, bool) {
	step := f.StepSeconds
	if step <= 0 {
		span := f.To.Sub(f.From).Seconds()
		switch {
		case span <= 24*3600:
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

	// GroupBy gate: must include "name"; service.name is
	// optional. Normalise so the SQL emit is deterministic.
	hasName := false
	hasService := false
	for _, k := range f.GroupBy {
		switch k {
		case "name", "operation":
			hasName = true
		case "service.name", "service_name":
			hasService = true
		default:
			return nil, false
		}
	}
	if !hasName {
		return nil, false
	}

	// Filter gate. effectiveFastPathFilters resolves a flat-AND FilterRoot's
	// leaves to the legacy slice so a grouped `service.name = X` stays
	// MV-eligible (v0.8.x gap-2).
	var serviceFilter string
	for _, fe := range f.effectiveFastPathFilters() {
		if (fe.Key == "service.name" || fe.Key == "service_name") && fe.Op == "=" && len(fe.Values) == 1 {
			serviceFilter = fe.Values[0]
			continue
		}
		return nil, false
	}

	// If groupBy is just ["name"] WITHOUT a service filter,
	// the MV scan would mix operations from every service —
	// probably not what the operator wanted. Refuse.
	if !hasService && serviceFilter == "" {
		return nil, false
	}

	field := f.Field
	if field == "" {
		field = "duration_ms"
	}
	if field != "duration_ms" {
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
	case "per_min":
		// Per-minute throughput (Uptrace perMin). count over the step / step
		// seconds × 60. v0.8.x forced add alongside the legacy `rate`.
		aggExpr = fmt.Sprintf("toNullable(toFloat64(countMerge(span_count_state)) / %d.0 * 60.0)", step)
	case "apdex":
		// Apdex score from the MV's satisfied/tolerating states (Uptrace
		// apdex()). T is fixed at MV build time (apdex_satisfied = dur ≤ T,
		// tolerating = T < dur ≤ 4T). v0.8.x.
		aggExpr = "toNullable((toFloat64(countMerge(apdex_satisfied_state)) + toFloat64(countMerge(apdex_tolerating_state)) / 2) / nullIf(toFloat64(countMerge(span_count_state)), 0))"
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

	// Build groupSelect — order matches f.GroupBy exactly so
	// the wire-format keys line up with what the operator
	// asked for (service.name first if both, else just name).
	var groupSelect string
	switch {
	case hasService && hasName:
		// Match the operator's f.GroupBy order so the chip
		// tuple "service / operation" reads naturally.
		if len(f.GroupBy) >= 2 && (f.GroupBy[0] == "service.name" || f.GroupBy[0] == "service_name") {
			groupSelect = "[service_name, name]"
		} else {
			groupSelect = "[name, service_name]"
		}
	case hasName && !hasService:
		groupSelect = "[name]"
	default:
		// Service-only group should have been caught by the
		// service fast-path. Defensive — refuse.
		return nil, false
	}

	whereClauses := []string{"time_bucket >= ?", "time_bucket <= ?"}
	args := []any{f.From, f.To}
	if serviceFilter != "" {
		whereClauses = append(whereClauses, "service_name = ?")
		args = append(args, serviceFilter)
	}

	sql := fmt.Sprintf(`
		SELECT
		    toUnixTimestamp(toStartOfInterval(time_bucket, INTERVAL %d SECOND)) * 1000000000 AS bucket,
		    %s AS gk,
		    %s AS v
		FROM operation_summary_5m
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

// tryOperationMVFastPathMulti (v0.5.273) is the batched peer
// of tryOperationMVFastPath — same eligibility, but selects N
// aggregation columns in one operation_summary_5m scan + emits
// one result map keyed by the operator-given spec names.
//
// Powers ServiceCharts on /service?name=X (the "rate +
// error_rate + p99 by operation" triple) at month-scale where
// the raw spans path would otherwise hit the 30s execution
// ceiling — operator-reported regression that surfaced after
// v0.5.268/269 only covered the single-version.
func (s *Store) tryOperationMVFastPathMulti(ctx context.Context, f SpanMetricBatchFilter) (map[string][]SpanMetricSeries, bool) {
	step := f.StepSeconds
	if step <= 0 {
		span := f.To.Sub(f.From).Seconds()
		switch {
		case span <= 24*3600:
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

	// GroupBy gate: must include "name"; service.name optional.
	hasName, hasService := false, false
	for _, k := range f.GroupBy {
		switch k {
		case "name", "operation":
			hasName = true
		case "service.name", "service_name":
			hasService = true
		default:
			return nil, false
		}
	}
	if !hasName {
		return nil, false
	}

	// Filter gate: only service.name = X.
	var serviceFilter string
	for _, fe := range f.Filters {
		if (fe.Key == "service.name" || fe.Key == "service_name") && fe.Op == "=" && len(fe.Values) == 1 {
			serviceFilter = fe.Values[0]
			continue
		}
		return nil, false
	}
	if !hasService && serviceFilter == "" {
		return nil, false
	}

	// Every spec must be MV-supported. Field must be duration_ms
	// (the only column the MV pre-aggregates). Build aggExpr
	// per spec; the SQL emits them as v0/v1/v2 aliases so the
	// scan position-aliasing matches the agg order.
	aggExprs := make([]string, 0, len(f.Aggs))
	for _, a := range f.Aggs {
		field := a.Field
		if field == "" {
			field = "duration_ms"
		}
		if field != "duration_ms" {
			return nil, false
		}
		var expr string
		switch a.Aggregation {
		case "", "count":
			expr = "toNullable(toFloat64(countMerge(span_count_state)))"
		case "rate":
			expr = fmt.Sprintf("toNullable(toFloat64(countMerge(span_count_state)) / %d.0 * 60.0)", step)
		case "error_rate":
			expr = "toNullable(toFloat64(countMerge(error_count_state)) / nullIf(toFloat64(countMerge(span_count_state)), 0))"
		case "errors":
			expr = "toNullable(toFloat64(countMerge(error_count_state)))"
		case "per_min":
			expr = fmt.Sprintf("toNullable(toFloat64(countMerge(span_count_state)) / %d.0 * 60.0)", step)
		case "apdex":
			expr = "toNullable((toFloat64(countMerge(apdex_satisfied_state)) + toFloat64(countMerge(apdex_tolerating_state)) / 2) / nullIf(toFloat64(countMerge(span_count_state)), 0))"
		case "avg":
			expr = "toNullable(toFloat64(sumMerge(duration_sum_state)) / nullIf(toFloat64(countMerge(span_count_state)), 0) / 1e6)"
		case "p50":
			expr = "toNullable(toFloat64(arrayElement(quantilesMerge(0.5, 0.95, 0.99)(duration_q_state), 1) / 1e6))"
		case "p95":
			expr = "toNullable(toFloat64(arrayElement(quantilesMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6))"
		case "p99":
			expr = "toNullable(toFloat64(arrayElement(quantilesMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6))"
		default:
			return nil, false
		}
		aggExprs = append(aggExprs, expr)
	}

	// Build groupSelect — matches operator's f.GroupBy order.
	var groupSelect string
	switch {
	case hasService && hasName:
		if len(f.GroupBy) >= 2 && (f.GroupBy[0] == "service.name" || f.GroupBy[0] == "service_name") {
			groupSelect = "[service_name, name]"
		} else {
			groupSelect = "[name, service_name]"
		}
	case hasName:
		groupSelect = "[name]"
	default:
		return nil, false
	}

	selectParts := []string{
		fmt.Sprintf("toUnixTimestamp(toStartOfInterval(time_bucket, INTERVAL %d SECOND)) * 1000000000 AS bucket", step),
		groupSelect + " AS gk",
	}
	for i, e := range aggExprs {
		selectParts = append(selectParts, fmt.Sprintf("%s AS v%d", e, i))
	}

	whereClauses := []string{"time_bucket >= ?", "time_bucket <= ?"}
	args := []any{f.From, f.To}
	if serviceFilter != "" {
		whereClauses = append(whereClauses, "service_name = ?")
		args = append(args, serviceFilter)
	}

	sql := fmt.Sprintf(`
		SELECT %s
		FROM operation_summary_5m
		WHERE %s
		GROUP BY bucket, gk
		ORDER BY gk, bucket
		LIMIT 50000
		SETTINGS max_execution_time = 30`,
		strings.Join(selectParts, ",\n        "),
		strings.Join(whereClauses, " AND "))

	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	// Per-agg seriesMap, one per spec.
	type seriesAcc struct {
		byKey map[string]*SpanMetricSeries
		order []string
	}
	accs := make([]seriesAcc, len(f.Aggs))
	for i := range accs {
		accs[i].byKey = map[string]*SpanMetricSeries{}
	}

	// Scan into a dynamic-width row: (bucket, gk, v0, v1, ...).
	scanArgs := make([]any, 2+len(f.Aggs))
	for rows.Next() {
		var bucket uint64
		var gk []string
		vals := make([]*float64, len(f.Aggs))
		scanArgs[0] = &bucket
		scanArgs[1] = &gk
		for i := range vals {
			scanArgs[2+i] = &vals[i]
		}
		if err := rows.Scan(scanArgs...); err != nil {
			return nil, false
		}
		key := strings.Join(gk, "|")
		for i := range f.Aggs {
			ser, ok := accs[i].byKey[key]
			if !ok {
				ser = &SpanMetricSeries{GroupKey: gk}
				accs[i].byKey[key] = ser
				accs[i].order = append(accs[i].order, key)
			}
			v := 0.0
			if vals[i] != nil {
				v = *vals[i]
			}
			ser.Points = append(ser.Points, SpanMetricPoint{Time: int64(bucket), Value: v})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false
	}

	out := make(map[string][]SpanMetricSeries, len(f.Aggs))
	for i, a := range f.Aggs {
		series := make([]SpanMetricSeries, 0, len(accs[i].order))
		for _, k := range accs[i].order {
			series = append(series, *accs[i].byKey[k])
		}
		out[a.Name] = series
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
	// ── MV fast-path (v0.5.273) ───────────────────────────────────────────────
	// ServiceCharts on /service?name=X fires this batch every
	// time the operator changes range. At month-scale the raw-
	// spans GROUP BY otherwise burns 5-30s of CH time per call.
	// The single-agg paths got MV-routing in v0.5.268/269; this
	// is the missing peer for the batched ("rate + error_rate
	// + p99 in one CH pass") variant.
	if out, ok := s.tryOperationMVFastPathMulti(ctx, f); ok {
		return out, nil
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
			expr, args := groupKeyExpr(k, s.hasOpGroupCol)
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
		LIMIT 50000
		SETTINGS max_execution_time = 30`,
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
// query parameters it needs (for attribute lookups by name). hasOpGroup is the
// Store's probed op_group presence; when false a groupBy=op_group soft-degrades
// to the raw operation name instead of emitting `toString(op_group)`, which
// would hard-error code 16 against raw spans on an external Distributed cluster
// where op_group never reached spans_local (v0.8.187).
func groupKeyExpr(key string, hasOpGroup bool) (string, []any) {
	switch {
	case strings.HasPrefix(key, "resource."):
		return "toString(res_values[indexOf(res_keys, ?)])", []any{strings.TrimPrefix(key, "resource.")}
	// op_group — the normalized operation-shape column (group_id rel B).
	// Lets Explore's groupBy=op_group fold the high-cardinality raw-name
	// tail (GET /orders/8421, …) into shape rows (GET /orders/:id) on the
	// spans/metric group-by path. Resolved to the dedicated LowCardinality
	// column, not the attr-array fallback. NOTE: the existing `operation`
	// alias is intentionally left mapping to the raw `name` column
	// (wellKnown["operation"]="name") — re-pointing it to op_group would
	// silently change every existing groupBy=operation query + the
	// MetricLabelValues value-suggester, which is a behaviour change, not a
	// trivial alias. The explicit `op_group` key is the normalized handle.
	case key == "op_group":
		if !hasOpGroup {
			// op_group never reached spans_local (external Distributed,
			// cluster_name unset). Soft-degrade to the raw operation name so
			// the Explore panel returns rows instead of code 16, mirroring
			// GetOperationSummary's normalized fallback (v0.8.187).
			return "toString(name)", nil
		}
		return "toString(op_group)", nil
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
	case "per_min":
		return wrap(fmt.Sprintf("count() / %d.0 * 60.0", stepSec)), nil
	case "error_rate":
		return wrap("100.0 * countIf(status_code = 'error') / count()"), nil
	case "errors":
		return wrap("countIf(status_code = 'error')"), nil
	case "apdex":
		// Raw-spans Apdex, thresholds matched to the MV (T=200ms, 4T=800ms;
		// see store.go apdexT). field = (duration / 1e6) ms via fieldToSQL.
		return wrap(fmt.Sprintf("(countIf(%[1]s <= 200.0) + countIf(%[1]s > 200.0 AND %[1]s <= 800.0) / 2.0) / count()", field)), nil
	case "sum":
		return wrap("sumOrNull(" + field + ")"), nil
	case "avg":
		return wrap("avgOrNull(" + field + ")"), nil
	case "min":
		return wrap("minOrNull(" + field + ")"), nil
	case "max":
		return wrap("maxOrNull(" + field + ")"), nil
	// Percentiles use quantileTDigest, not exact quantile(): the raw-spans
	// span-metric path runs over up to LIMIT 50000 buckets' worth of rows
	// and at billion-row scale exact quantile() holds every value in memory
	// (the CLAUDE.md anti-pattern). TDigest is ≤2% error at a fraction of the
	// RAM and matches the approximate quantilesMerge the MV fast-paths already
	// serve, so the operator's p99 stays consistent across surfaces.
	// Accuracy tradeoff is intentional (speed/memory > exactness on a chart).
	case "p50":
		return wrap("quantileTDigest(0.50)(" + field + ")"), nil
	case "p90":
		return wrap("quantileTDigest(0.90)(" + field + ")"), nil
	case "p95":
		return wrap("quantileTDigest(0.95)(" + field + ")"), nil
	case "p99":
		return wrap("quantileTDigest(0.99)(" + field + ")"), nil
	case "p999":
		return wrap("quantileTDigest(0.999)(" + field + ")"), nil
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
