package chstore

import (
	"context"
	"fmt"
	"strings"
)

// tracemetric.go — "Every metric is a doorway" Phase D, increment 4 (v0.8.53):
// the tracemetrics branch of ResolveMetricQuery. Where spanmetrics answers
// "per span-operation RED", tracemetrics answers "per ENTRY-ENDPOINT,
// trace-level RED" — trace rate, % of traces with any error span, and
// end-to-end trace-duration percentiles, grouped by root service / entry
// http.route / root operation.
//
// It reads trace_summary_5m (the per-trace rollup, D3-extended with
// entry_route_state) in TWO levels:
//
//   inner: GROUP BY trace_id → finalize each trace's states into ONE row:
//          root_service, root_op, entry_route, start_time, full duration
//          (trace_end - trace_start), and error-span count.
//   outer: GROUP BY (step bucket of start_time, group keys) → aggregate those
//          per-trace values into the requested metric.
//
// The duration is computed on the FINALIZED per-trace states (max end − min
// start across the trace's buckets), so a trace that spans multiple 5-min
// buckets still yields its true wall-clock duration — you can't get that from
// a single-level GROUP BY over spans.

// traceDimColumn maps a descriptor key to the per-trace subquery's derived
// column. tracemetrics carries exactly three dimensions; anything else can't
// be answered from trace_summary_5m.
func traceDimColumn(key string) (col string, ok bool) {
	switch key {
	case "service.name", "service_name":
		return "root_service", true
	case "http.route", "http_route", "entry_route":
		return "entry_route", true
	case "name", "operation", "root_name":
		return "root_op", true
	default:
		return "", false
	}
}

// traceMetricAgg is the agg→SQL projection over the per-trace subquery values
// (plain values, not states — the inner GROUP BY already finalized them). dur
// is nanoseconds → /1e6 for ms; a trace is "errored" if any of its spans
// errored (err_spans > 0). rate is traces-per-second; error_rate a percentage.
func traceMetricAgg(agg string, stepSec int) (string, error) {
	wrap := func(s string) string { return "toNullable(toFloat64(" + s + "))" }
	switch strings.ToLower(agg) {
	case "", "count":
		return wrap("count()"), nil
	case "rate":
		return wrap(fmt.Sprintf("count() / %d.0", stepSec)), nil
	case "errors":
		return wrap("countIf(err_spans > 0)"), nil
	case "error_rate":
		return wrap("100.0 * countIf(err_spans > 0) / nullIf(count(), 0)"), nil
	case "sum":
		return wrap("sum(dur_ns) / 1e6"), nil
	case "avg":
		return wrap("avg(dur_ns) / 1e6"), nil
	case "p50":
		return wrap("quantileTDigest(0.50)(dur_ns) / 1e6"), nil
	case "p90":
		return wrap("quantileTDigest(0.90)(dur_ns) / 1e6"), nil
	case "p95":
		return wrap("quantileTDigest(0.95)(dur_ns) / 1e6"), nil
	case "p99":
		return wrap("quantileTDigest(0.99)(dur_ns) / 1e6"), nil
	}
	return "", fmt.Errorf("unknown aggregation %q", agg)
}

// traceMetricExemplarCols — the slowest / a representative errored trace per
// outer bucket. Plain argMax over the subquery's trace rows (no state merge):
// the trace_id IS the natural exemplar here, so no exemplar state was needed
// on trace_summary_5m.
func traceMetricExemplarCols() string {
	return ",\n\t\t    argMax(trace_id, dur_ns) AS slow_trace," +
		"\n\t\t    argMaxIf(trace_id, dur_ns, err_spans > 0) AS error_trace"
}

// resolveTraceMetric serves source="tracemetrics" descriptors.
func (s *Store) resolveTraceMetric(ctx context.Context, q MetricResolveQuery, step int) (MetricResolveResult, error) {
	aggExpr, err := traceMetricAgg(q.Agg, step)
	if err != nil {
		return MetricResolveResult{}, err
	}

	// GroupBy → derived columns, in the operator's order.
	groupSelect := "[]::Array(String)"
	if len(q.GroupBy) > 0 {
		cols := make([]string, len(q.GroupBy))
		for i, k := range q.GroupBy {
			col, ok := traceDimColumn(k)
			if !ok {
				return MetricResolveResult{}, fmt.Errorf("tracemetrics: unsupported groupBy dimension %q", k)
			}
			cols[i] = col
		}
		groupSelect = "[" + strings.Join(cols, ", ") + "]"
	}

	// Filters apply in the OUTER query — they key on argMaxIfMerge-derived
	// columns the inner subquery produces.
	outerConds := []string{}
	var args []any
	args = append(args, q.From, q.To) // inner subquery's time bounds come first in SQL text
	for k, v := range q.Filters {
		col, ok := traceDimColumn(k)
		if !ok {
			return MetricResolveResult{}, fmt.Errorf("tracemetrics: unsupported filter dimension %q", k)
		}
		outerConds = append(outerConds, col+" = ?")
		args = append(args, v)
	}
	outerWhere := ""
	if len(outerConds) > 0 {
		outerWhere = "WHERE " + strings.Join(outerConds, " AND ")
	}

	exemplarCols := ""
	if q.IncludeExemplars {
		exemplarCols = traceMetricExemplarCols()
	}

	// Inner: one finalized row per trace. dur_ns = max end − min start (both
	// nanoseconds); trace_end_state is already Int64 nanos, trace_start_state
	// is DateTime64(9) → toUnixTimestamp64Nano.
	inner := `
		SELECT
		    trace_id,
		    argMaxIfMerge(root_service_state) AS root_service,
		    argMaxIfMerge(root_name_state)    AS root_op,
		    argMaxIfMerge(entry_route_state)  AS entry_route,
		    minMerge(trace_start_state)       AS start_time,
		    (maxMerge(trace_end_state) - toUnixTimestamp64Nano(minMerge(trace_start_state))) AS dur_ns,
		    countIfMerge(error_count_state)   AS err_spans
		FROM trace_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?
		GROUP BY trace_id`

	sql := fmt.Sprintf(`
		SELECT
		    toUnixTimestamp(toStartOfInterval(start_time, INTERVAL %d SECOND)) * 1000000000 AS bucket,
		    %s AS gk,
		    %s AS v%s
		FROM (%s)
		%s
		GROUP BY bucket, gk
		ORDER BY gk, bucket
		LIMIT 50000
		SETTINGS max_execution_time = 30`,
		step, groupSelect, aggExpr, exemplarCols, inner, outerWhere)

	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return MetricResolveResult{}, fmt.Errorf("resolve tracemetric: %w", err)
	}
	defer rows.Close()

	seriesMap := make(map[string]*SpanMetricSeries)
	var order []string
	var exemplars []MetricExemplar
	for rows.Next() {
		var bucket uint64
		var gk []string
		var val *float64
		var slowTrace, errTrace string
		if q.IncludeExemplars {
			if err := rows.Scan(&bucket, &gk, &val, &slowTrace, &errTrace); err != nil {
				return MetricResolveResult{}, err
			}
		} else {
			if err := rows.Scan(&bucket, &gk, &val); err != nil {
				return MetricResolveResult{}, err
			}
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
		if q.IncludeExemplars && (slowTrace != "" || errTrace != "") {
			exemplars = append(exemplars, MetricExemplar{
				Time: int64(bucket), GroupKey: gk,
				SlowTraceID: slowTrace, ErrorTraceID: errTrace,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return MetricResolveResult{}, err
	}
	out := make([]SpanMetricSeries, 0, len(order))
	for _, k := range order {
		out = append(out, *seriesMap[k])
	}
	return MetricResolveResult{Series: out, Tier: "trace_summary_5m", StepSeconds: step, Exemplars: exemplars}, nil
}
