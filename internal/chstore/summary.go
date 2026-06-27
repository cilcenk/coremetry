package chstore

import (
	"context"
	"fmt"
	"strings"
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

// ListServiceNames is the lookup behind UI service-name pickers (traces,
// logs, services filter, alerts, SLOs, exceptions, ...).
//
// Reads DISTINCT service_name from the 5-minute MV. The MV stores one
// row per (service, 5min bucket) so DISTINCT is essentially "what
// services have we seen in the last 90 days" (= MV TTL) — exactly the
// set the pickers care about, and the read is cheap because the MV's
// ORDER BY (service_name, time_bucket) makes the distinct streamable.
//
// `pattern` accepts simple Lucene-style wildcards:
//   - bare text  → case-insensitive substring (LIKE '%text%')
//   - "*"        → multi-char wildcard
//   - "?"        → single-char wildcard
// SQL LIKE special chars in user input ('%', '_') are escaped first so
// they're matched literally rather than acting as inadvertent wildcards.
// ListOperationNames — operations-picker counterpart to
// ListServiceNames (v0.5.180). Reads operation_summary_5m so
// the GROUP BY is cheap even at billions of spans / tens of
// thousands of operations per service. Service filter is
// optional but recommended at scale — a global op listing on
// an install with 10k services × 100 ops/service is
// approaching the limits of "useful in a dropdown".
//
// Wildcard semantics match ListServiceNames: `*` and `?` map
// to CH `%` / `_`; bare strings are wrapped in `%…%` for
// substring match. Returns (names, total, err) so the UI can
// surface "showing 200 of 12,345 — refine" hints.
func (s *Store) ListOperationNames(ctx context.Context, service, pattern string, limit, offset int) ([]string, int, error) {
	if limit <= 0 {
		limit = 200
	}
	var wc whereClause
	if service != "" {
		wc.add("service_name = ?", service)
	}
	if pattern != "" {
		like := strings.NewReplacer(`*`, `%`, `?`, `_`).Replace(pattern)
		if !strings.ContainsAny(pattern, "*?") {
			like = "%" + like + "%"
		}
		wc.add("name ILIKE ?", like)
	}

	var total uint64
	if err := s.conn.QueryRow(ctx,
		"SELECT count(DISTINCT name) FROM operation_summary_5m "+wc.sql()+
			" SETTINGS max_execution_time = 30",
		wc.args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args := append(append([]any{}, wc.args...), limit, offset)
	rows, err := s.conn.Query(ctx,
		"SELECT DISTINCT name FROM operation_summary_5m "+wc.sql()+
			" ORDER BY name LIMIT ? OFFSET ?"+
			" SETTINGS max_execution_time = 30",
		args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, 0, err
		}
		out = append(out, n)
	}
	return out, int(total), rows.Err()
}

func (s *Store) ListServiceNames(ctx context.Context, pattern string, limit, offset int) ([]string, int, error) {
	if limit <= 0 {
		limit = 200
	}
	args := []any{}
	where := ""
	if pattern != "" {
		// Translate user pattern → ClickHouse ILIKE. Service names in
		// the wild are typically [a-zA-Z0-9._-]+ so the SQL wildcard
		// chars (%, _) almost never appear literally; we accept the
		// edge case rather than escape (CH doesn't support ESCAPE
		// on ILIKE anyway).
		like := strings.NewReplacer(`*`, `%`, `?`, `_`).Replace(pattern)
		// If the user didn't include any wildcards, default to a
		// substring match — that's what they expect when typing into a
		// picker, not an exact equality.
		if !strings.ContainsAny(pattern, "*?") {
			like = "%" + like + "%"
		}
		where = " WHERE service_name ILIKE ?"
		args = append(args, like)
	}

	var total uint64
	if err := s.conn.QueryRow(ctx,
		"SELECT count(DISTINCT service_name) FROM service_summary_5m"+where+
			" SETTINGS max_execution_time = 30",
		args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, limit, offset)
	rows, err := s.conn.Query(ctx,
		"SELECT DISTINCT service_name FROM service_summary_5m"+where+
			" ORDER BY service_name LIMIT ? OFFSET ?"+
			" SETTINGS max_execution_time = 30",
		args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, 0, err
		}
		out = append(out, n)
	}
	return out, int(total), rows.Err()
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
	return s.GetServicesAggFilteredIn(ctx, from, to, "", nil, "", "", limit, 0)
}

// GetServicesAggFiltered — preserves the prior surface (no
// service-name allowlist). New callers should use
// GetServicesAggFilteredIn directly.
func (s *Store) GetServicesAggFiltered(ctx context.Context, from, to time.Time, nameMatch, sort, dir string, limit, offset int) ([]ServiceSummary, error) {
	return s.GetServicesAggFilteredIn(ctx, from, to, nameMatch, nil, sort, dir, limit, offset)
}

// servicesAggSortExpr — alias for servicesSortExpr but using
// the column names produced by the MV-aggregation SELECT
// (`spans` / `errs` instead of `span_count` / `error_count`).
// Same whitelist; never interpolate the raw key.
func servicesAggSortExpr(sort, dir string) string {
	col := "spans"
	switch sort {
	case "name":
		col = "service_name"
	case "spans", "span_count":
		col = "spans"
	case "errorCount", "errors", "error_count":
		col = "errs"
	case "errorRate", "error_rate":
		col = "(errs / nullIf(spans, 0))"
	case "avg", "avg_ms":
		col = "avg_ms"
	case "p99", "p99_ms":
		col = "p99_ms"
	case "apdex":
		col = "apdex"
	}
	d := "DESC"
	if dir == "asc" || dir == "ASC" {
		d = "ASC"
	}
	return col + " " + d + " NULLS LAST"
}

// GetServicesAggFiltered narrows the row set by a substring match on
// service_name *before* the GROUP BY — used by the Services page
// dropdown so a service that's outside the limited top-N still
// surfaces when the user types its name. `nameMatch` empty disables
// the filter.
// GetServicesAggFilteredIn — same as GetServicesAggFiltered
// plus a service-name allowlist (the API uses this to
// pre-narrow the universe by ownerTeam / sreTeam without
// joining at query time). nil / empty = no constraint.
// CountServicesAgg returns the number of DISTINCT services matching the same
// MV-path filters as GetServicesAggFilteredIn — the denominator the Services
// page needs for First/Last paging. uniqExact over service_summary_5m is cheap
// (no per-service aggregation). Kept OPT-IN at the handler (?withTotal=1) so the
// default hot path stays count-free per the /api/services p99<50ms budget
// (v0.7.44).
func (s *Store) CountServicesAgg(ctx context.Context, from, to time.Time, nameMatch string, serviceIn []string) (int, error) {
	if from.IsZero() {
		from = time.Now().Add(-24 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	nameClause := ""
	args := []any{from, to}
	if nameMatch != "" {
		nameClause = " AND positionCaseInsensitive(service_name, ?) > 0"
		args = append(args, nameMatch)
	}
	if len(serviceIn) > 0 {
		holders := make([]string, len(serviceIn))
		for i, n := range serviceIn {
			holders[i] = "?"
			args = append(args, n)
		}
		nameClause += " AND service_name IN (" + strings.Join(holders, ",") + ")"
	}
	var n uint64
	err := s.conn.QueryRow(ctx, `
		SELECT toUInt64(uniqExact(service_name))
		FROM service_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?`+nameClause+`
		SETTINGS max_execution_time = 30`,
		args...).Scan(&n)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// mvQuantileMemSettings bounds the memory + time of summary-MV reads that
// merge the duration_q_state reservoir quantile state.
//
// v0.8.191 — operator-reported PRODUCTION (external Distributed cluster):
// merging full 8192-sample reservoir quantilesState (measured ~64 KiB/row)
// over a wide window blew CH's per-query memory limit (3.73 GiB → code 241,
// "while reading column duration_q_state") AND the execution timeout
// (code 159). Big services errored out, so /services and the /service
// operations table rendered only the tiny self-obs service. Two memory-only
// levers, neither of which can worsen behaviour (single-node ignores the
// distributed one):
//   - max_threads = 2 caps the parallel per-granule read buffers (each
//     granule of 64 KiB states is large; 8 concurrent = ~8x the peak).
//   - distributed_aggregation_memory_efficient streams the cross-shard
//     merge instead of pulling every shard's partial states onto the
//     initiator (the real sink on the 4-shard cluster).
// max_execution_time is raised to 30 on the ops reads (was 10) so the
// less-parallel scan still finishes.
//
// PROPER fix (queued): migrate duration_q_state to quantilesTDigestState —
// measured 4.3 KiB/row, ~15x smaller and parallel-safe. Reservoir
// quantilesState at billion-span scale is exactly CLAUDE.md's documented
// anti-pattern ("quantile() past ~1M rows → quantileTDigest").
const mvQuantileMemSettings = `max_threads = 2, distributed_aggregation_memory_efficient = 1`

func (s *Store) GetServicesAggFilteredIn(ctx context.Context, from, to time.Time, nameMatch string, serviceIn []string, sort, dir string, limit, offset int) ([]ServiceSummary, error) {
	if from.IsZero() {
		from = time.Now().Add(-24 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	limitClause := ""
	if limit > 0 {
		limitClause = fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	}
	const apdexT = 200.0
	nameClause := ""
	args := []any{from, to}
	if nameMatch != "" {
		// Case-insensitive substring match — matches what the
		// service-names autocomplete does.
		nameClause = " AND positionCaseInsensitive(service_name, ?) > 0"
		args = append(args, nameMatch)
	}
	if len(serviceIn) > 0 {
		// IN-list against the allowlist. Splat each value as
		// its own placeholder so the driver binds them one
		// per `?` (the IN clause itself takes a parenthesised
		// list of literals).
		holders := make([]string, len(serviceIn))
		for i, n := range serviceIn {
			holders[i] = "?"
			args = append(args, n)
		}
		nameClause += " AND service_name IN (" + strings.Join(holders, ",") + ")"
	}
	rows, err := s.conn.Query(ctx, `
		SELECT service_name,
		       countMerge(span_count_state)                                            AS spans,
		       countIfMerge(error_count_state)                                         AS errs,
		       sumMerge(duration_sum_state) / nullIf(spans, 0) / 1e6                   AS avg_ms,
		       arrayElement(quantilesMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms,
		       (countIfMerge(apdex_satisfied_state) + countIfMerge(apdex_tolerating_state) / 2)
		         / nullIf(spans, 0)                                                     AS apdex
		FROM service_summary_5m
		WHERE time_bucket >= ? AND time_bucket <= ?`+nameClause+`
		GROUP BY service_name
		ORDER BY `+servicesAggSortExpr(sort, dir)+limitClause+`
		SETTINGS max_execution_time = 30, `+mvQuantileMemSettings,
		args...)
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
		// v0.5.301 — same NaN-from-quantilesMerge guard as the
		// operations path; encoding/json rejects NaN+Inf so a
		// rogue float silently 500s the /services response.
		sv.AvgMs = safeF(avg)
		sv.P99Ms = safeF(p99)
		sv.Apdex = safeF(apdex)
		if sv.SpanCount > 0 {
			sv.ErrorRate = float64(sv.ErrorCount) / float64(sv.SpanCount) * 100
		}
		sv.ApdexThresholdMs = apdexT
		out = append(out, sv)
	}
	return out, rows.Err()
}

// GetServiceSummary5mFor reads MV buckets for a set of named services.
// Same shape as GetServiceSummary5m but accepts a list — used by the
// sparklines endpoint to scope the result to the visible top-N rows on
// the services page (otherwise the response is one array per service
// across all of them, which is multi-MB at high cardinality).
//
// Empty list returns ALL services (so an internal caller that genuinely
// wants the full set still has a path).
func (s *Store) GetServiceSummary5mFor(ctx context.Context, services []string, from, to time.Time) ([]ServiceSummaryRow, error) {
	args := []any{from, to}
	svcFilter := ""
	if len(services) > 0 {
		// Use the IN(...) tuple form. clickhouse-go takes a slice and
		// binds it as an array; keeps the SQL parameterised (no
		// hand-quoted values).
		svcFilter = " AND service_name IN ?"
		args = append(args, services)
	}
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
		ORDER BY service_name, time_bucket
		SETTINGS max_execution_time = 30`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServiceSummaryRow{}
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
