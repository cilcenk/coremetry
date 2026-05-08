package chstore

import (
	"context"
	"time"
)

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
		WHERE p.trace_id IN (SELECT trace_id FROM child)
		  AND p.time >= ? AND p.time <= ?
		  AND p.service_name != ?
		GROUP BY caller_service, caller_host, caller_instance, client_address, user_agent
		ORDER BY calls DESC
		LIMIT ?
		SETTINGS max_execution_time = 30, join_use_nulls = 0`,
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
