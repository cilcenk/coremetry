package chstore

import (
	"context"
	"fmt"
	"time"
)

// WriteServiceCallersBucket batch-aggregates the per-(receiver,
// caller pod / client / UA) rollup for one 5-min slice. Called
// by the topology aggregator alongside WriteTopologyBucket so
// the schedule + settle-delay semantics stay aligned.
//
// Same shape as the prior raw-spans ServiceCallers query, just
// run ONCE per bucket instead of on every operator request.
// All the dimensional cardinality concerns we already accepted
// for the raw path (client_address can be high-card) carry
// forward into the MV unchanged — the TTL keeps storage
// bounded.
func (s *Store) WriteServiceCallersBucket(ctx context.Context, bucketStart time.Time) error {
	end := bucketStart.Add(5 * time.Minute)
	return s.conn.Exec(ctx, `
		INSERT INTO service_callers_5m
			(time_bucket, service, caller_service, caller_host,
			 caller_instance, client_address, user_agent,
			 calls, errors, sum_duration_ns,
			 p50_ms, p95_ms, p99_ms, last_seen_ns, version)
		SELECT
			toDateTime(?, 'UTC')                                        AS time_bucket,
			c.service_name                                              AS service,
			p.service_name                                              AS caller_service,
			p.host_name                                                 AS caller_host,
			p.caller_instance                                           AS caller_instance,
			c.attr_values[indexOf(c.attr_keys, 'client.address')]       AS client_address,
			c.attr_values[indexOf(c.attr_keys, 'user_agent.original')]  AS user_agent,
			toUInt64(count())                                           AS calls,
			toUInt64(countIf(c.status_code = 'error'))                  AS errors,
			toUInt64(sum(c.duration))                                   AS sum_duration_ns,
			toFloat64(quantileExact(0.50)(c.duration)) / 1e6            AS p50_ms,
			toFloat64(quantileExact(0.95)(c.duration)) / 1e6            AS p95_ms,
			toFloat64(quantileExact(0.99)(c.duration)) / 1e6            AS p99_ms,
			toUInt64(toUnixTimestamp64Nano(max(c.time)))                AS last_seen_ns,
			toUInt64(?)                                                 AS version
		FROM spans AS c
		GLOBAL INNER JOIN (
			-- v0.5.438 — pre-extract caller_instance inside the
			-- subquery so the res_keys / res_values arrays don't
			-- ride the GLOBAL broadcast. Pre-fix the right side
			-- carried two Array(String) columns per row across N
			-- shards, blowing the FillingRightJoinSide hash build
			-- past max_memory_usage (operator-reported: 7.48 GiB
			-- query on 7.45 GiB limit, code 241). Projecting
			-- caller_instance to a single string column here drops
			-- the right-side row width by an order of magnitude.
			SELECT trace_id, span_id, service_name, host_name,
			       ifNull(res_values[indexOf(res_keys, 'service.instance.id')], '') AS caller_instance
			FROM spans
			WHERE time >= toDateTime(?, 'UTC') AND time < toDateTime(?, 'UTC')
			  AND span_id != ''
		) AS p
			ON p.trace_id = c.trace_id AND p.span_id = c.parent_id
		WHERE c.time >= toDateTime(?, 'UTC') AND c.time < toDateTime(?, 'UTC')
		  AND c.parent_id != '' AND c.parent_id != '0000000000000000'
		  AND p.service_name != c.service_name
		GROUP BY service, caller_service, caller_host,
		         caller_instance, client_address, user_agent
		SETTINGS max_execution_time = 180,
		         join_algorithm = 'grace_hash',
		         max_bytes_in_join = 4000000000,
		         max_memory_usage = 8000000000,
		         distributed_product_mode = 'global'`,
		bucketStart.Unix(),
		uint64(time.Now().UnixNano()),
		bucketStart.Unix(), end.Unix(),
		bucketStart.Unix(), end.Unix(),
	)
}

// serviceCallersAggSQL builds the inbound-callers rollup query for the service
// backtrace.
//
// v0.7.41 — Operator-reported: Backtrace returned HTTP 500 "code 184: Aggregate
// function sum(errors) AS errors is found inside another aggregate function".
// calls/errors are plain UInt64, but aliasing `sum(calls) AS calls` /
// `sum(errors) AS errors` SHADOWED those columns — so the later error_rate /
// avg_ms expressions re-summed the ALIASES (sum(sum(...))), tripping CH's
// nested-aggregate guard. Fix: alias the totals c_calls / c_errors so the
// compound expressions still reference the raw columns. Scan stays positional,
// so the alias rename is invisible to the Go side.
func serviceCallersAggSQL(limit int) string {
	return fmt.Sprintf(`
		SELECT caller_service,
		       caller_host,
		       caller_instance,
		       client_address,
		       user_agent,
		       sum(calls)                                              AS c_calls,
		       sum(errors)                                             AS c_errors,
		       sum(errors) * 100.0 / nullIf(sum(calls), 0)             AS error_rate,
		       sum(sum_duration_ns) / nullIf(sum(calls), 0) / 1e6      AS avg_ms,
		       max(p50_ms)                                             AS p50_ms,
		       max(p95_ms)                                             AS p95_ms,
		       max(p99_ms)                                             AS p99_ms,
		       toInt64(max(last_seen_ns))                              AS last_seen_ns
		FROM service_callers_5m FINAL
		WHERE service = ?
		  AND time_bucket >= ?
		  AND time_bucket <= ?
		GROUP BY caller_service, caller_host, caller_instance,
		         client_address, user_agent
		ORDER BY c_calls DESC
		LIMIT %d
		SETTINGS max_execution_time = 10`, limit)
}

// ReadServiceCallersAgg surfaces the same CallerRow shape from
// the MV. Bucket-aligns the lower bound (the standard *_5m
// idiom) and reads with FINAL so ReplacingMergeTree version
// dedup is honoured. Sub-second even at billion-span scale —
// the prior raw-spans self-join could exceed 30s.
func (s *Store) ReadServiceCallersAgg(
	ctx context.Context, service string, from, to time.Time, limit int,
) ([]CallerRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	bucketStart := from.Truncate(5 * time.Minute)
	rows, err := s.conn.Query(ctx, serviceCallersAggSQL(limit), service, bucketStart, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CallerRow{}
	for rows.Next() {
		var r CallerRow
		var errRate, avgMs, p50, p95, p99 *float64
		if err := rows.Scan(
			&r.CallerService, &r.CallerHost, &r.CallerInstance,
			&r.ClientAddress, &r.UserAgent,
			&r.Calls, &r.Errors, &errRate, &avgMs,
			&p50, &p95, &p99,
			&r.LastSeenNs,
		); err != nil {
			return nil, err
		}
		r.ErrorRate = safeF(errRate)
		r.AvgMs = safeF(avgMs)
		r.P50Ms = safeF(p50)
		r.P95Ms = safeF(p95)
		r.P99Ms = safeF(p99)
		out = append(out, r)
	}
	return out, rows.Err()
}

// CallerRow is one row of the inbound-callers backtrace for a
// service: a unique combination of (caller service × caller pod /
// instance × client address × user agent) that has invoked the
// inspected service over the requested window, plus RED stats.
//
// Caller service / host / instance come from the PARENT span's
// resource attributes via a self-join on (trace_id, parent_id);
// client.address and user_agent.original are read directly off
// the receiving (server) span.
type CallerRow struct {
	CallerService  string  `json:"callerService"`
	CallerHost     string  `json:"callerHost"`
	CallerInstance string  `json:"callerInstance"`
	ClientAddress  string  `json:"clientAddress"`
	UserAgent      string  `json:"userAgent"`
	Calls          uint64  `json:"calls"`
	Errors         uint64  `json:"errors"`
	ErrorRate      float64 `json:"errorRate"`
	AvgMs          float64 `json:"avgMs"`
	P50Ms          float64 `json:"p50Ms"`
	P95Ms          float64 `json:"p95Ms"`
	P99Ms          float64 `json:"p99Ms"`
	LastSeenNs     int64   `json:"lastSeenNs"`
}

// ServiceCallers returns the inbound-callers backtrace for `service`
// over [from,to]. Each row identifies a distinct caller pod /
// instance + client IP combination so the operator can answer "who
// is hammering me right now". Self-join on (trace_id, parent_id) so
// caller pod / host comes from the calling span's resource attrs;
// client.address + user_agent.original come from the receiving
// (server) span itself, so even traces missing the parent edge
// still surface the IP-level identity.
//
// Performance posture:
//   - The receiving-side filter (service_name = ?) limits the LEFT
//     side aggressively — typically a small fraction of the spans.
//   - The right side is constrained by trace_id IN (LEFT.trace_id)
//     so ClickHouse can use the trace_id skip index instead of a
//     full window scan.
//   - GROUP BY columns are all low-cardinality lookups extracted
//     once in the LEFT subquery; aggregation happens on a few
//     thousand rows in the typical case.
func (s *Store) ServiceCallers(
	ctx context.Context, service string, from, to time.Time, limit int,
) ([]CallerRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.conn.Query(ctx, `
		WITH child AS (
		  SELECT trace_id, parent_id, time, duration, status_code,
		         attr_values[indexOf(attr_keys, 'client.address')]      AS client_addr,
		         attr_values[indexOf(attr_keys, 'user_agent.original')] AS user_agent
		  FROM spans
		  WHERE service_name = ?
		    AND time >= ? AND time <= ?
		    AND parent_id != '' AND parent_id != '0000000000000000'
		)
		SELECT
		  p.service_name AS caller_service,
		  p.host_name    AS caller_host,
		  ifNull(p.res_values[indexOf(p.res_keys, 'service.instance.id')], '') AS caller_instance,
		  c.client_addr  AS client_address,
		  c.user_agent   AS user_agent,
		  count()                                          AS calls,
		  countIf(c.status_code = 'error')                 AS errors,
		  countIf(c.status_code = 'error') / count() * 100 AS error_rate,
		  avg(c.duration) / 1e6                            AS avg_ms,
		  quantile(0.50)(c.duration) / 1e6                 AS p50_ms,
		  quantile(0.95)(c.duration) / 1e6                 AS p95_ms,
		  quantile(0.99)(c.duration) / 1e6                 AS p99_ms,
		  toUnixTimestamp64Nano(max(c.time))               AS last_seen_ns
		FROM child c
		INNER JOIN spans p ON p.trace_id = c.trace_id AND p.span_id = c.parent_id
		WHERE p.trace_id GLOBAL IN (SELECT trace_id FROM child)
		  AND p.time >= ? AND p.time <= ?
		  AND p.service_name != ?
		GROUP BY caller_service, caller_host, caller_instance, client_address, user_agent
		ORDER BY calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 30,
		         join_use_nulls = 0,
		         `+s.shardSkipSetting()+`,
		         distributed_product_mode = 'global'`,
		service, from, to,
		from, to, service,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []CallerRow{}
	for rows.Next() {
		var r CallerRow
		if err := rows.Scan(
			&r.CallerService, &r.CallerHost, &r.CallerInstance,
			&r.ClientAddress, &r.UserAgent,
			&r.Calls, &r.Errors, &r.ErrorRate,
			&r.AvgMs, &r.P50Ms, &r.P95Ms, &r.P99Ms,
			&r.LastSeenNs,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
