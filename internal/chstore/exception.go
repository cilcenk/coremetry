package chstore

import (
	"context"
	"time"
)

type ExceptionFilter struct {
	Service  string
	GroupBy  string // "type" | "type-service" | "full"  (default: "type-service")
	From, To time.Time
	Limit    int
}

// GetExceptions returns OTel `exception` events grouped by (type, message,
// service) with totals and a sample trace/span pointer for drill-down.
//
// We dig the events JSON column with JSON_VALUE — slower than dedicated
// columns, but the volume of error spans is small relative to the total.
func (s *Store) GetExceptions(ctx context.Context, f ExceptionFilter) ([]ExceptionRow, error) {
	var wc whereClause
	if !f.From.IsZero() {
		wc.add("time >= ?", f.From)
	}
	if !f.To.IsZero() {
		wc.add("time <= ?", f.To)
	}
	if f.Service != "" {
		wc.add("service_name = ?", f.Service)
	}
	wc.add("events LIKE '%\"exception\"%'")
	if f.Limit == 0 {
		f.Limit = 100
	}

	// Choose grouping. anyIf makes ungrouped fields show *some* value.
	var groupCols, selectMsg, selectSvc string
	switch f.GroupBy {
	case "type":
		groupCols = "ex_type"
		selectMsg = "any(ex_msg)         AS ex_msg"
		selectSvc = "any(service_name)   AS svc"
	case "full":
		groupCols = "ex_type, ex_msg, service_name"
		selectMsg = "ex_msg"
		selectSvc = "service_name        AS svc"
	default: // "type-service"
		groupCols = "ex_type, service_name"
		selectMsg = "any(ex_msg)         AS ex_msg"
		selectSvc = "service_name        AS svc"
	}

	// Pull exception fields directly from the events JSON.
	rows, err := s.conn.Query(ctx, `
		WITH src AS (
		  SELECT
		    coalesce(JSON_VALUE(events, '$[0].attributes."exception.type"'),    '<unknown>') AS ex_type,
		    coalesce(JSON_VALUE(events, '$[0].attributes."exception.message"'), '')          AS ex_msg,
		    service_name, time, trace_id, span_id
		  FROM spans `+wc.sql()+`
		)
		SELECT ex_type, `+selectMsg+`, `+selectSvc+`,
		       count() AS cnt,
		       toUnixTimestamp64Nano(max(time)) AS last_seen,
		       argMax(trace_id, time) AS sample_trace,
		       argMax(span_id,  time) AS sample_span
		FROM src
		GROUP BY `+groupCols+`
		ORDER BY cnt DESC
		LIMIT ?`, append(wc.args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExceptionRow
	for rows.Next() {
		var r ExceptionRow
		if err := rows.Scan(&r.Type, &r.Message, &r.Service, &r.Count,
			&r.LastSeen, &r.SampleTraceID, &r.SampleSpanID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
