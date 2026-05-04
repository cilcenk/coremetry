package chstore

import (
	"context"
	"fmt"
	"time"
)

// ServiceSummaryRow is one 5-minute bucket of pre-aggregated stats for a
// single service, sourced from the service_summary_5m materialized view.
// Use for time-bucketed reads that span hours/days — the MV merges
// AggregateFunction states cheaply at query time, no raw spans scan.
type ServiceSummaryRow struct {
	Service     string  `json:"service"`
	BucketStart int64   `json:"bucketStart"`  // unix ns
	SpanCount   uint64  `json:"spanCount"`
	ErrorCount  uint64  `json:"errorCount"`
	AvgMs       float64 `json:"avgMs"`
	P50Ms       float64 `json:"p50Ms"`
	P95Ms       float64 `json:"p95Ms"`
	P99Ms       float64 `json:"p99Ms"`
}

// GetServicesAgg returns one aggregate row per service for the requested
// window, reading entirely from service_summary_5m. Replaces the raw-spans
// scan in GetServices for any window where the MV has data — orders of
// magnitude faster at scale (sub-second across 10s of thousands of
// services / billions of source spans).
//
// `limit` caps the result to the top-N services by span count; pass 0 to
// disable. Apdex is computed from the new countIfState columns; if the
// MV pre-dates the schema upgrade those columns are NULL → apdex = 0.
//
// 30-second hard execution timeout via SETTINGS — this endpoint must
// never hang the UI thread, even when the MV itself has a backlog.
func (s *Store) GetServicesAgg(ctx context.Context, from, to time.Time, limit int) ([]ServiceSummary, error) {
	if from.IsZero() {
		from = time.Now().Add(-24 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	limitClause := ""
	if limit > 0 {
		limitClause = fmt.Sprintf(" LIMIT %d", limit)
	}
	const apdexT = 200.0
	rows, err := s.conn.Query(ctx, `
		SELECT service_name,
		       countMerge(span_count_state)                                            AS spans,
		       countIfMerge(error_count_state)                                         AS errs,
		       sumMerge(duration_sum_state) / nullIf(spans, 0) / 1e6                   AS avg_ms,
		       arrayElement(quantilesMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms,
		       (countIfMerge(apdex_satisfied_state) + countIfMerge(apdex_tolerating_state) / 2)
		         / nullIf(spans, 0)                                                     AS apdex
		FROM service_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?
		GROUP BY service_name
		ORDER BY spans DESC`+limitClause+`
		SETTINGS max_execution_time = 30`,
		from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServiceSummary{}
	for rows.Next() {
		var (
			sv     ServiceSummary
			avg    *float64
			p99    *float64
			apdex  *float64
		)
		if err := rows.Scan(&sv.Name, &sv.SpanCount, &sv.ErrorCount, &avg, &p99, &apdex); err != nil {
			return nil, err
		}
		if avg != nil {
			sv.AvgMs = *avg
		}
		if p99 != nil {
			sv.P99Ms = *p99
		}
		if apdex != nil {
			sv.Apdex = *apdex
		}
		if sv.SpanCount > 0 {
			sv.ErrorRate = float64(sv.ErrorCount) / float64(sv.SpanCount) * 100
		}
		sv.ApdexThresholdMs = apdexT
		out = append(out, sv)
	}
	return out, rows.Err()
}

// GetServiceSummary5m reads pre-aggregated 5-minute buckets from the MV.
// Suitable for "show last N hours per-service trend" without paying the
// cost of scanning raw span rows. Buckets that haven't materialised yet
// (under 5 minutes old) will be missing — callers should overlay raw
// spans for the most recent window if they need second-fresh numbers.
func (s *Store) GetServiceSummary5m(ctx context.Context, service string, from, to time.Time) ([]ServiceSummaryRow, error) {
	args := []any{from, to}
	svcFilter := ""
	if service != "" {
		svcFilter = " AND service_name = ?"
		args = append(args, service)
	}
	// time_bucket is plain DateTime (seconds precision — toStartOfInterval
	// strips sub-second precision regardless of input type), so explicitly
	// widen to DateTime64(9) before extracting nanoseconds. Otherwise CH
	// errors with "illegal type ... Expected: DateTime64, got: DateTime".
	rows, err := s.conn.Query(ctx, `
		SELECT
		  service_name,
		  toUnixTimestamp64Nano(toDateTime64(time_bucket, 9)) AS bucket_ns,
		  countMerge(span_count_state)                      AS spans,
		  countIfMerge(error_count_state)                   AS errors,
		  sumMerge(duration_sum_state) / nullIf(countMerge(span_count_state), 0) / 1e6 AS avg_ms,
		  arrayElement(quantilesMerge(0.5, 0.95, 0.99)(duration_q_state), 1) / 1e6 AS p50_ms,
		  arrayElement(quantilesMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95_ms,
		  arrayElement(quantilesMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms
		FROM service_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?`+svcFilter+`
		GROUP BY service_name, time_bucket
		ORDER BY service_name, time_bucket`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceSummaryRow
	for rows.Next() {
		var r ServiceSummaryRow
		if err := rows.Scan(&r.Service, &r.BucketStart, &r.SpanCount, &r.ErrorCount,
			&r.AvgMs, &r.P50Ms, &r.P95Ms, &r.P99Ms); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
