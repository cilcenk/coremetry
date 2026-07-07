package chstore

// Batched alert-evaluator reads (v0.8.352, perf Phase 2 fix P2-A).
//
// MEASURED PROBLEM (live query_log, 1h, 100-service install): the evaluator
// issued ~70k queries/hour because measure()/measureCount() ran once per
// (rule, service) per tick — ~32k countMerge(span_count_state) singles for
// the MinSamples gate, ~23k raw quantile(0.99)(duration) scans for the
// kind-scoped http_/db_/mq_ metrics, ~15k per-service merge reads, with max
// spikes to 8.5s. This file collapses each (metric, window) to ONE
// GROUP BY service_name query per tick; the evaluator prefetches the maps
// once and evaluates every rule×service from memory.
//
// The v0.8.315 aligned-window math (mvWindowStart / mvCoveredSeconds /
// scaleToWindow, formerly private to internal/evaluator) moved here so the
// batched and the surviving per-service paths share ONE implementation —
// the evaluator keeps thin delegating wrappers for its sub-5m raw path.
//
// Which backing store serves which metric (see measureAllServicesPlan):
//   - error_rate / error_count / request_rate / avg_ms / p50/p95/p99_ms
//       → service_summary_5m merge states (v0.6.12 routing, batched).
//   - mq_publish_* / mq_consume_* transport metrics
//       → spanmetrics_1m: its `kind` dimension expresses the exact
//         transportFilter predicate (kind='producer' / 'consumer') and
//         error_state ≡ the mq numerator (status_code='error'). This kills
//         the raw quantile class for MQ rules.
//   - http_* / db_* / rpc_* transport metrics
//       → raw spans, but ONE GROUP BY service_name per (metric, window)
//         per tick instead of one scan per service. Their transportFilter
//         predicates need http_method / http_status / db_system /
//         rpc_system, none of which are spanmetrics_1m dimensions, so the
//         MV cannot express them. Still ~100× fewer queries than before,
//         and each read keeps LIMIT + max_execution_time + a time-bounded
//         WHERE per the CH hard constraints.

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// summaryMVBucket is service_summary_5m's bucket width — the grid every
// evaluator window ≥ 5m aligns to (v0.8.315).
const summaryMVBucket = 5 * time.Minute

// UseSummaryMV decides whether an evaluator window can ride the 5-minute
// MVs instead of scanning raw spans (v0.6.12). Sub-5min windows fall back
// to per-service raw spans because the MV's granularity can't reconstruct
// them faithfully. Moved from internal/evaluator in v0.8.352 so the batched
// reads and the evaluator share one boundary.
func UseSummaryMV(window time.Duration) bool {
	return window >= summaryMVBucket
}

// MVWindowStart aligns the window cutoff DOWN to the 5m MV bucket grid
// (v0.8.315). The MV filter runs on time_bucket — the bucket START — so an
// unaligned `now-window` cutoff excluded the bucket containing it and a
// "5-minute" rule read as little as ~1 minute of data (the still-filling
// bucket only). Down-alignment over-covers by <1 bucket instead;
// count/rate metrics normalize back via ScaleToWindow/MVCoveredSeconds.
// time.Truncate aligns on the UTC epoch — the same grid as ClickHouse's
// toStartOfInterval on the UTC-typed column.
func MVWindowStart(now time.Time, window time.Duration) time.Time {
	return now.Add(-window).Truncate(summaryMVBucket)
}

// MVCoveredSeconds is the real span the aligned MV read covers — always
// ≥ the nominal window (window + up-to-299s drift).
func MVCoveredSeconds(now time.Time, window time.Duration) float64 {
	return now.Sub(MVWindowStart(now, window)).Seconds()
}

// ScaleToWindow normalizes an absolute count observed over `coveredSec`
// seconds to the nominal window, so thresholds keep their configured
// meaning ("50 errors in 5 min" stays a 5-minute quantity even though the
// aligned read spans up to 5m+299s).
func ScaleToWindow(n, windowSec, coveredSec float64) float64 {
	if coveredSec <= 0 {
		return n
	}
	return n * windowSec / coveredSec
}

// TransportFilter returns, for a transport-scoped alert metric
// (http_* / db_* / rpc_* / mq_publish_* / mq_consume_*):
//   - where:     denominator population predicate (WHERE narrows the span
//     set we're measuring against)
//   - numerator: numerator predicate for *_rate metrics (counts the "bad"
//     rows within the population). Unused for latency/count metrics.
//
// All fragments are literal SQL — no user input — so they're safe to
// concatenate. Moved from internal/evaluator in v0.8.352; the evaluator's
// sub-5m per-service path delegates here so the mapping stays single-sourced.
func TransportFilter(metric string) (where, numerator string, ok bool) {
	switch {
	case strings.HasPrefix(metric, "http_5xx_"):
		return "kind='server' AND http_method != ''",
			"http_status >= 500", true
	case strings.HasPrefix(metric, "http_4xx_"):
		return "kind='server' AND http_method != ''",
			"http_status >= 400 AND http_status < 500", true
	case strings.HasPrefix(metric, "http_"):
		return "kind='server' AND http_method != ''",
			"status_code='error'", true
	case strings.HasPrefix(metric, "db_"):
		return "db_system != ''",
			"status_code='error'", true
	case strings.HasPrefix(metric, "rpc_"):
		return "rpc_system != ''",
			"status_code='error'", true
	case strings.HasPrefix(metric, "mq_publish_"):
		return "kind='producer'",
			"status_code='error'", true
	case strings.HasPrefix(metric, "mq_consume_"):
		return "kind='consumer'",
			"status_code='error'", true
	}
	return "", "", false
}

// TransportOp pulls the aggregate suffix off a transport metric:
//
//	http_5xx_rate          → error_rate (5xx-narrowed by TransportFilter)
//	http_p99_ms            → p99_ms
//	db_error_rate          → error_rate
//	mq_publish_error_rate  → error_rate
func TransportOp(metric string) string {
	switch {
	case strings.HasSuffix(metric, "_rate"):
		return "error_rate"
	case strings.HasSuffix(metric, "_p99_ms"):
		return "p99_ms"
	case strings.HasSuffix(metric, "_p95_ms"):
		return "p95_ms"
	case strings.HasSuffix(metric, "_p50_ms"):
		return "p50_ms"
	case strings.HasSuffix(metric, "_avg_ms"):
		return "avg_ms"
	case strings.HasSuffix(metric, "_count"):
		return "count"
	}
	return ""
}

// spanmetricsTransportWhere maps a transport metric to a WHERE fragment
// expressible on spanmetrics_1m's dimensions (service_name, name, kind,
// status_code, http_route). Only the mq_* families qualify: their
// TransportFilter denominator is pure `kind`, and their *_rate numerator
// (status_code='error') is exactly what error_state pre-aggregates.
// http_* needs http_method (+ http_status for 4xx/5xx), db_* needs
// db_system, rpc_* needs rpc_system — none are MV dimensions, so those
// families fall back to the batched raw-spans plan.
func spanmetricsTransportWhere(metric string) (where string, ok bool) {
	switch {
	case strings.HasPrefix(metric, "mq_publish_"):
		return "kind='producer'", true
	case strings.HasPrefix(metric, "mq_consume_"):
		return "kind='consumer'", true
	}
	return "", false
}

// measureAllScan tells MeasureAllServices how to read + post-process each
// grouped row. The variants preserve the exact per-service measure()
// semantics (v0.8.315 math applied to the SAME metrics as before, and NOT
// to the ones it never applied to):
type measureAllScan int

const (
	// scanNullableFloat — Nullable(Float64) expr (ratio / MV avg). NULL
	// rows are skipped, matching the per-service NULL-scan error → skip.
	// (Unreachable for grouped rows — a group only exists with count>0 —
	// but kept faithful.)
	scanNullableFloat measureAllScan = iota
	// scanFloat — plain Float64 (quantiles, raw avg).
	scanFloat
	// scanCountScaled — UInt64 normalized to the nominal window via
	// ScaleToWindow (error_count parity, v0.8.315).
	scanCountScaled
	// scanCountRate — UInt64 ÷ MVCoveredSeconds (request_rate parity,
	// v0.8.315: rate over the REAL covered span, not the nominal window).
	scanCountRate
	// scanCountRaw — UInt64 as-is. Transport `count` op parity: the
	// per-service measure() never scaled transport counts, so the batched
	// read doesn't either (do NOT "fix" this here — thresholds are tuned
	// against the existing behavior).
	scanCountRaw
)

// measureAllPlan is the resolved read for one metric: which table, the
// exact SQL (one bind arg: the aligned window start), and how to scan.
type measureAllPlan struct {
	source string // "service_summary_5m" | "spanmetrics_1m" | "spans"
	sql    string
	scan   measureAllScan
}

// evalReadTail bounds every batched read: the GROUP BY cardinality is the
// service catalogue (thousands by design — LowCardinality), the LIMIT is a
// pure safety valve, and max_execution_time caps a CH lock fight at the
// same 10s the per-service reads used.
const evalReadTail = `
 GROUP BY service_name
 LIMIT 100000
 SETTINGS max_execution_time = 10`

// measureAllServicesPlan routes an alert metric to its batched query.
// Pure — the routing + SQL shape are table-tested without a CH connection.
// smSource is the FROM source for spanmetrics_1m reads (Store passes
// spanmetricsSourceFor: bare name single-node, cluster() on a
// chstore-owned cluster — v0.8.358, the per-shard undercount class).
func measureAllServicesPlan(metric, smSource string) (measureAllPlan, error) {
	// Basic RED metrics — service_summary_5m merge states, the batched
	// twins of the per-service v0.6.12 MV queries.
	switch metric {
	case "error_rate":
		return measureAllPlan{source: "service_summary_5m", scan: scanNullableFloat, sql: `
			SELECT service_name,
			       toFloat64(countMerge(error_count_state)) /
			       nullIf(toFloat64(countMerge(span_count_state)),0) * 100
			FROM service_summary_5m
			WHERE time_bucket >= ?` + evalReadTail}, nil
	case "error_count":
		return measureAllPlan{source: "service_summary_5m", scan: scanCountScaled, sql: `
			SELECT service_name, countMerge(error_count_state)
			FROM service_summary_5m
			WHERE time_bucket >= ?` + evalReadTail}, nil
	case "request_rate":
		return measureAllPlan{source: "service_summary_5m", scan: scanCountRate, sql: `
			SELECT service_name, countMerge(span_count_state)
			FROM service_summary_5m
			WHERE time_bucket >= ?` + evalReadTail}, nil
	case "avg_ms":
		return measureAllPlan{source: "service_summary_5m", scan: scanNullableFloat, sql: `
			SELECT service_name,
			       toFloat64(sumMerge(duration_sum_state)) /
			       nullIf(toFloat64(countMerge(span_count_state)),0) / 1e6
			FROM service_summary_5m
			WHERE time_bucket >= ?` + evalReadTail}, nil
	case "p50_ms", "p95_ms", "p99_ms":
		// Indices match the MV's quantilesTDigestState(0.5, 0.95, 0.99)
		// ordering: 1=p50, 2=p95, 3=p99.
		idx := map[string]int{"p50_ms": 1, "p95_ms": 2, "p99_ms": 3}[metric]
		return measureAllPlan{source: "service_summary_5m", scan: scanFloat, sql: fmt.Sprintf(`
			SELECT service_name,
			       arrayElement(quantilesTDigestMerge(0.5,0.95,0.99)(duration_q_state), %d) / 1e6
			FROM service_summary_5m
			WHERE time_bucket >= ?`, idx) + evalReadTail}, nil
	}

	// Transport-scoped metrics.
	where, numerator, ok := TransportFilter(metric)
	if !ok {
		return measureAllPlan{}, fmt.Errorf("unknown metric %q", metric)
	}
	op := TransportOp(metric)

	// mq_* families ride spanmetrics_1m (kind is an MV dimension; the mq
	// *_rate numerator is exactly error_state's predicate — guarded below
	// so a future numerator change forces the raw fallback instead of
	// silently misreading the MV).
	if mvWhere, mvOK := spanmetricsTransportWhere(metric); mvOK &&
		(op != "error_rate" || numerator == "status_code='error'") {
		mvHead := `
			FROM ` + smSource + `
			WHERE time_bucket >= ? AND ` + mvWhere
		switch op {
		case "error_rate":
			return measureAllPlan{source: "spanmetrics_1m", scan: scanNullableFloat, sql: `
			SELECT service_name,
			       toFloat64(countMerge(error_state)) /
			       nullIf(toFloat64(countMerge(calls_state)),0) * 100` + mvHead + evalReadTail}, nil
		case "p50_ms", "p95_ms", "p99_ms":
			// spanmetrics_1m stores quantilesTDigestState(0.5, 0.9, 0.95,
			// 0.99) — note the extra 0.9: 1=p50, 2=p90, 3=p95, 4=p99.
			idx := map[string]int{"p50_ms": 1, "p95_ms": 3, "p99_ms": 4}[op]
			return measureAllPlan{source: "spanmetrics_1m", scan: scanFloat, sql: fmt.Sprintf(`
			SELECT service_name,
			       arrayElement(quantilesTDigestMerge(0.5,0.9,0.95,0.99)(duration_q_state), %d) / 1e6`, idx) + mvHead + evalReadTail}, nil
		case "avg_ms":
			return measureAllPlan{source: "spanmetrics_1m", scan: scanNullableFloat, sql: `
			SELECT service_name,
			       toFloat64(sumMerge(duration_sum_state)) /
			       nullIf(toFloat64(countMerge(calls_state)),0) / 1e6` + mvHead + evalReadTail}, nil
		case "count":
			return measureAllPlan{source: "spanmetrics_1m", scan: scanCountRaw, sql: `
			SELECT service_name, countMerge(calls_state)` + mvHead + evalReadTail}, nil
		}
	}

	// Raw-spans fallback: the predicate isn't expressible on any MV's
	// dimensions, but ONE bounded GROUP BY per (metric, window) per tick
	// replaces the former per-service scans. The cutoff bind is the SAME
	// 5m-aligned start the per-service transport path used post-v0.8.315
	// (measure() aligned it before the transport branch), so the covered
	// population is identical. The tail is spelled out inline (not via
	// evalReadTail) so every FROM spans literal carries its LIMIT +
	// max_execution_time in the same string (audit CHECK 6).
	rawTail := `
			FROM spans
			WHERE time >= ? AND ` + where + `
			GROUP BY service_name
			LIMIT 100000
			SETTINGS max_execution_time = 10`
	switch op {
	case "error_rate":
		return measureAllPlan{source: "spans", scan: scanNullableFloat, sql: `
			SELECT service_name,
			       countIf(` + numerator + `) * 100.0 / nullIf(count(),0)` + rawTail}, nil
	case "p50_ms", "p95_ms", "p99_ms":
		q := op[1 : len(op)-3] // "50" / "95" / "99"
		return measureAllPlan{source: "spans", scan: scanFloat, sql: `
			SELECT service_name, quantile(0.` + q + `)(duration) / 1e6` + rawTail}, nil
	case "avg_ms":
		return measureAllPlan{source: "spans", scan: scanFloat, sql: `
			SELECT service_name, avg(duration) / 1e6` + rawTail}, nil
	case "count":
		return measureAllPlan{source: "spans", scan: scanCountRaw, sql: `
			SELECT service_name, count()` + rawTail}, nil
	}
	return measureAllPlan{}, fmt.Errorf("unknown transport op %q in %q", op, metric)
}

// MeasureAllServices measures one alert metric for EVERY service in one
// query — the batched twin of the evaluator's per-service measure()
// (v0.8.352, perf P2-A). Requires a window the 5m MV grid serves
// (UseSummaryMV true); the evaluator keeps its per-service raw path for
// rarer sub-5m custom windows.
//
// Absent map key = the service had no rows in the window (zero traffic).
// The evaluator's absentMeasure translates that to the exact value/skip
// the per-service query used to produce for an empty result.
func (s *Store) MeasureAllServices(ctx context.Context, metric string, window time.Duration, now time.Time) (map[string]float64, error) {
	if !UseSummaryMV(window) {
		return nil, fmt.Errorf("MeasureAllServices: window %s below the %s MV grid — use the per-service raw path", window, summaryMVBucket)
	}
	plan, err := measureAllServicesPlan(metric, s.spanmetricsSourceFor("spanmetrics_1m"))
	if err != nil {
		return nil, err
	}
	start := MVWindowStart(now, window)
	covered := MVCoveredSeconds(now, window)

	rows, err := s.conn.Query(ctx, plan.sql, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]float64)
	for rows.Next() {
		var svc string
		switch plan.scan {
		case scanNullableFloat:
			var v *float64
			if err := rows.Scan(&svc, &v); err != nil {
				return nil, err
			}
			if v != nil {
				out[svc] = *v
			}
		case scanFloat:
			var v float64
			if err := rows.Scan(&svc, &v); err != nil {
				return nil, err
			}
			out[svc] = v
		case scanCountScaled:
			var n uint64
			if err := rows.Scan(&svc, &n); err != nil {
				return nil, err
			}
			out[svc] = ScaleToWindow(float64(n), window.Seconds(), covered)
		case scanCountRate:
			var n uint64
			if err := rows.Scan(&svc, &n); err != nil {
				return nil, err
			}
			out[svc] = float64(n) / covered
		case scanCountRaw:
			var n uint64
			if err := rows.Scan(&svc, &n); err != nil {
				return nil, err
			}
			out[svc] = float64(n)
		}
	}
	return out, rows.Err()
}

// MeasureCountAllServices returns the span count per service over the
// window in ONE query — the batched twin of the evaluator's per-service
// measureCount() that feeds the MinSamples gate (v0.8.352, perf P2-A: this
// single class was ~32k queries/hour). Counts are normalized to the
// nominal window exactly like the per-service read (v0.8.315). Absent key
// = zero spans, which is what countMerge over no buckets returned.
func (s *Store) MeasureCountAllServices(ctx context.Context, window time.Duration, now time.Time) (map[string]uint64, error) {
	if !UseSummaryMV(window) {
		return nil, fmt.Errorf("MeasureCountAllServices: window %s below the %s MV grid — use the per-service raw path", window, summaryMVBucket)
	}
	start := MVWindowStart(now, window)
	covered := MVCoveredSeconds(now, window)

	rows, err := s.conn.Query(ctx, `
			SELECT service_name, countMerge(span_count_state)
			FROM service_summary_5m
			WHERE time_bucket >= ?`+evalReadTail, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]uint64)
	for rows.Next() {
		var svc string
		var n uint64
		if err := rows.Scan(&svc, &n); err != nil {
			return nil, err
		}
		out[svc] = uint64(ScaleToWindow(float64(n), window.Seconds(), covered))
	}
	return out, rows.Err()
}
