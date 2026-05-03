package chstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ── Batch inserts ─────────────────────────────────────────────────────────────

func (s *Store) InsertSpans(ctx context.Context, spans []*Span) error {
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO spans")
	if err != nil {
		return fmt.Errorf("prepare spans: %w", err)
	}
	for _, sp := range spans {
		if err := batch.Append(
			sp.TraceID, sp.SpanID, sp.ParentID, sp.Name, sp.Kind,
			sp.ServiceName, sp.HostName, sp.DeployEnv, sp.StatusCode, sp.StatusMsg,
			sp.Time, sp.Duration,
			sp.DBSystem, sp.DBStatement, sp.HTTPMethod, sp.HTTPRoute, sp.HTTPStatus,
			sp.RPCSystem, sp.RPCMethod, sp.PeerService, sp.MsgSystem,
			sp.AttrKeys, sp.AttrValues, sp.ResKeys, sp.ResValues,
			sp.Events, sp.ScopeName,
		); err != nil {
			return fmt.Errorf("append span: %w", err)
		}
	}
	return batch.Send()
}

func (s *Store) InsertLogs(ctx context.Context, logs []*Log) error {
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO logs")
	if err != nil {
		return fmt.Errorf("prepare logs: %w", err)
	}
	for _, l := range logs {
		if err := batch.Append(
			l.TraceID, l.SpanID, l.Time, l.SeverityNum, l.SeverityText,
			l.Body, l.ServiceName, l.HostName,
			l.AttrKeys, l.AttrValues, l.ResKeys, l.ResValues, l.ScopeName,
		); err != nil {
			return fmt.Errorf("append log: %w", err)
		}
	}
	return batch.Send()
}

func (s *Store) InsertMetrics(ctx context.Context, pts []*MetricPoint) error {
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO metric_points")
	if err != nil {
		return fmt.Errorf("prepare metrics: %w", err)
	}
	for _, p := range pts {
		if err := batch.Append(
			p.Metric, p.Instrument, p.Description, p.Unit,
			p.ServiceName, p.HostName, p.Time, p.StartTime,
			p.Value, p.Count, p.SumValue, p.MinValue, p.MaxValue,
			p.AttrKeys, p.AttrValues, p.ResKeys, p.ResValues,
		); err != nil {
			return fmt.Errorf("append metric: %w", err)
		}
	}
	return batch.Send()
}

// ── Service queries ───────────────────────────────────────────────────────────

// GetServices returns aggregate stats per service for the requested window.
// Pass `since` for a relative window (now-since … now), or non-zero `from`/`to`
// for an absolute window (overrides since).
func (s *Store) GetServices(ctx context.Context, since time.Duration, from, to time.Time) ([]ServiceSummary, error) {
	var wc whereClause
	if !from.IsZero() {
		wc.add("time >= ?", from)
		if !to.IsZero() {
			wc.add("time <= ?", to)
		}
	} else {
		wc.add("time >= ?", time.Now().Add(-since))
	}
	// Apdex threshold (T) — 200 ms is a common default. Frustrated boundary
	// is 4T. Computed per-service in the same pass to avoid an extra query.
	const apdexT = 200.0
	rows, err := s.conn.Query(ctx, `
		SELECT service_name,
		       count()                                  AS span_count,
		       countIf(status_code = 'error')           AS error_count,
		       avg(duration) / 1e6                      AS avg_ms,
		       quantile(0.99)(duration) / 1e6           AS p99_ms,
		       (countIf(duration <= ?* 1e6) + countIf(duration > ? * 1e6 AND duration <= ? * 1e6) / 2)
		         / nullIf(count(), 0)                   AS apdex
		FROM spans `+wc.sql()+`
		GROUP BY service_name
		ORDER BY span_count DESC`,
		append([]any{apdexT, apdexT, apdexT * 4}, wc.args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceSummary
	for rows.Next() {
		var sv ServiceSummary
		var apdex *float64
		if err := rows.Scan(&sv.Name, &sv.SpanCount, &sv.ErrorCount, &sv.AvgMs, &sv.P99Ms, &apdex); err != nil {
			return nil, err
		}
		if sv.SpanCount > 0 {
			sv.ErrorRate = float64(sv.ErrorCount) / float64(sv.SpanCount) * 100
		}
		if apdex != nil {
			sv.Apdex = *apdex
		}
		sv.ApdexThresholdMs = apdexT
		out = append(out, sv)
	}
	return out, rows.Err()
}

// GetOperations returns the distinct span names ("operations") seen in the
// given window, optionally filtered by service. Ordered by call count desc,
// so the most common operations appear first in the autocomplete list.
func (s *Store) GetOperations(ctx context.Context, service string, since time.Duration, from, to time.Time) ([]string, error) {
	var wc whereClause
	if !from.IsZero() {
		wc.add("time >= ?", from)
		if !to.IsZero() {
			wc.add("time <= ?", to)
		}
	} else {
		wc.add("time >= ?", time.Now().Add(-since))
	}
	if service != "" {
		wc.add("service_name = ?", service)
	}
	rows, err := s.conn.Query(ctx,
		`SELECT name, count() AS c
		 FROM spans `+wc.sql()+`
		 GROUP BY name
		 ORDER BY c DESC
		 LIMIT 500`, wc.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		var c uint64
		if err := rows.Scan(&name, &c); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// GetServiceGraph returns the directed call graph between services.
// If `service` is non-empty, only edges where it appears as source OR target
// are returned (i.e. the neighborhood of that service).
func (s *Store) GetServiceGraph(ctx context.Context, service string, since time.Duration, from, to time.Time) ([]ServiceEdge, error) {
	var wc whereClause
	if !from.IsZero() {
		wc.add("time >= ?", from)
		if !to.IsZero() {
			wc.add("time <= ?", to)
		}
	} else {
		wc.add("time >= ?", time.Now().Add(-since))
	}
	wc.add("peer_service != ''")
	wc.add("kind IN ('client', 'producer')")
	if service != "" {
		wc.add("(service_name = ? OR peer_service = ?)", service, service)
	}
	rows, err := s.conn.Query(ctx, `
		SELECT service_name                              AS source,
		       peer_service                             AS target,
		       count()                                  AS calls,
		       countIf(status_code = 'error') / count() * 100 AS error_rate,
		       avg(duration) / 1e6                      AS avg_ms
		FROM spans `+wc.sql()+`
		GROUP BY source, target
		ORDER BY calls DESC`, wc.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceEdge
	for rows.Next() {
		var e ServiceEdge
		if err := rows.Scan(&e.Source, &e.Target, &e.CallCount, &e.ErrorRate, &e.AvgMs); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ── Trace queries ─────────────────────────────────────────────────────────────

type TraceFilter struct {
	Service  string
	Search   string
	TraceID  string // exact match or prefix (16+ hex chars)
	From, To time.Time
	HasError bool
	MinMs    float64
	MaxMs    float64
	AttrKey  string
	AttrVal  string
	Filters  []FilterExpr // advanced filter chips (AND-joined)
	Sort     string       // "time" | "duration"
	Order    string       // "asc" | "desc"
	Limit    int
	Offset   int
}

func (s *Store) GetTraces(ctx context.Context, f TraceFilter) ([]TraceRow, uint64, error) {
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
	if f.Search != "" {
		wc.add("name LIKE ?", "%"+f.Search+"%")
	}
	if f.TraceID != "" {
		// Exact match for full 32-char trace ID, prefix match for shorter.
		// Bloom filter index on trace_id makes this efficient.
		if len(f.TraceID) == 32 {
			wc.add("trace_id = ?", f.TraceID)
		} else {
			wc.add("startsWith(trace_id, ?)", f.TraceID)
		}
	}
	if f.HasError {
		wc.add("status_code = 'error'")
	}
	if f.MinMs > 0 {
		wc.add("duration >= ?", int64(f.MinMs*1e6))
	}
	if f.MaxMs > 0 {
		wc.add("duration <= ?", int64(f.MaxMs*1e6))
	}
	ApplyFilters(&wc, f.Filters)
	if f.Limit == 0 {
		f.Limit = 50
	}

	// total count
	countSQL := "SELECT count(DISTINCT trace_id) FROM spans " + wc.sql()
	var total uint64
	if err := s.conn.QueryRow(ctx, countSQL, wc.args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// SAFE: sort key is whitelisted, never user-supplied SQL.
	sortMap := map[string]string{
		"time":      "trace_start",
		"duration":  "dur_ms",
		"spans":     "span_count",
		"service":   "root_svc",
		"operation": "root_name",
		"status":    "has_error",
	}
	sortCol, ok := sortMap[f.Sort]
	if !ok {
		sortCol = "trace_start"
	}
	order := "DESC"
	if f.Order == "asc" {
		order = "ASC"
	}

	// Note: use if() not ternary ? : — ClickHouse treats ? as a param placeholder
	querySQL := `
		SELECT trace_id,
		       anyIf(name, parent_id = '')             AS root_name,
		       anyIf(service_name, parent_id = '')     AS root_svc,
		       min(time)                               AS trace_start,
		       (max(toUnixTimestamp64Nano(time) + duration) -
		        toUnixTimestamp64Nano(min(time))) / 1e6 AS dur_ms,
		       count()                                 AS span_count,
		       max(if(status_code = 'error', 1, 0))    AS has_error
		FROM spans ` + wc.sql() + `
		GROUP BY trace_id
		ORDER BY ` + sortCol + ` ` + order + `
		LIMIT ? OFFSET ?`

	args := append(wc.args, f.Limit, f.Offset)
	rows, err := s.conn.Query(ctx, querySQL, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []TraceRow
	for rows.Next() {
		var t TraceRow
		var hasErr uint8
		var ts time.Time
		if err := rows.Scan(&t.TraceID, &t.RootName, &t.ServiceName, &ts, &t.DurationMs, &t.SpanCount, &hasErr); err != nil {
			return nil, 0, err
		}
		t.StartTime = ts.UnixNano()
		t.HasError = hasErr == 1
		out = append(out, t)
	}
	return out, total, rows.Err()
}

// GetTraceAggregate buckets traces by an attribute (operation/service) and
// returns RED-style stats per bucket. Each bucket = traces with the same
// root operation (or service). Filters mirror GetTraces, but sorting/limit
// applies to bucket aggregates, not individual traces.
type AggregateFilter struct {
	GroupBy  string // "operation" | "service"
	Service  string
	Search   string
	From, To time.Time
	HasError bool
	MinMs    float64
	MaxMs    float64
	Filters  []FilterExpr
	Sort     string // "count"|"errorRate"|"avg"|"p99"|"max"|"name"
	Order    string // "asc"|"desc"
	Limit    int
}

func (s *Store) GetTraceAggregate(ctx context.Context, f AggregateFilter) ([]AggregateRow, error) {
	// Per-trace stats first (subquery), then group across traces.
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
	if f.Search != "" {
		wc.add("name LIKE ?", "%"+f.Search+"%")
	}
	if f.HasError {
		wc.add("status_code = 'error'")
	}
	ApplyFilters(&wc, f.Filters)
	if f.Limit == 0 {
		f.Limit = 100
	}

	// Pick the grouping expression. Both forms use anyIf to grab the root
	// span's attribute (parent_id = '').
	var groupExpr, extraExpr string
	switch f.GroupBy {
	case "service":
		groupExpr = "anyIf(service_name, parent_id = '')"
		extraExpr = "''"
	default: // "operation"
		groupExpr = "anyIf(name, parent_id = '')"
		extraExpr = "anyIf(service_name, parent_id = '')"
	}

	// Whitelist sort key.
	sortMap := map[string]string{
		"count":     "trace_count",
		"errorRate": "error_rate",
		"avg":       "avg_ms",
		"p50":       "p50_ms",
		"p95":       "p95_ms",
		"p99":       "p99_ms",
		"max":       "max_ms",
		"name":      "group_key",
	}
	sortCol, ok := sortMap[f.Sort]
	if !ok {
		sortCol = "trace_count"
	}
	order := "DESC"
	if f.Order == "asc" {
		order = "ASC"
	}

	// 1) Inner: per-trace summary
	// 2) Outer: aggregate across traces per group bucket
	sql := `
		SELECT group_key, group_extra,
		       count()                                   AS trace_count,
		       countIf(has_error = 1)                    AS error_count,
		       countIf(has_error = 1) / count() * 100    AS error_rate,
		       avg(dur_ms)                                AS avg_ms,
		       quantile(0.50)(dur_ms)                     AS p50_ms,
		       quantile(0.95)(dur_ms)                     AS p95_ms,
		       quantile(0.99)(dur_ms)                     AS p99_ms,
		       max(dur_ms)                                AS max_ms,
		       toUnixTimestamp64Nano(max(trace_start))    AS last_seen_ns
		FROM (
		    SELECT trace_id,
		           ` + groupExpr + ` AS group_key,
		           ` + extraExpr + ` AS group_extra,
		           min(time) AS trace_start,
		           (max(toUnixTimestamp64Nano(time) + duration) -
		            toUnixTimestamp64Nano(min(time))) / 1e6 AS dur_ms,
		           max(if(status_code = 'error', 1, 0)) AS has_error
		    FROM spans ` + wc.sql() + `
		    GROUP BY trace_id
		    HAVING group_key != ''
		)`

	args := wc.args
	postFilter := ""
	if f.MinMs > 0 {
		postFilter += " AND avg_ms >= ?"
		args = append(args, f.MinMs)
	}
	if f.MaxMs > 0 {
		postFilter += " AND avg_ms <= ?"
		args = append(args, f.MaxMs)
	}

	sql += `
		GROUP BY group_key, group_extra`
	if postFilter != "" {
		sql += `
		HAVING 1=1` + postFilter
	}
	sql += `
		ORDER BY ` + sortCol + ` ` + order + `
		LIMIT ?`
	args = append(args, f.Limit)

	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AggregateRow
	for rows.Next() {
		var a AggregateRow
		if err := rows.Scan(
			&a.GroupKey, &a.GroupExtra, &a.TraceCount, &a.ErrorCount, &a.ErrorRate,
			&a.AvgMs, &a.P50Ms, &a.P95Ms, &a.P99Ms, &a.MaxMs, &a.LastSeen,
		); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetTrace(ctx context.Context, traceID string) ([]SpanRow, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT trace_id, span_id, parent_id, name, kind, service_name, host_name,
		       time, duration, status_code, status_msg,
		       attr_keys, attr_values, res_keys, res_values,
		       events, scope_name,
		       db_system, db_statement, http_method, http_route, http_status, peer_service
		FROM spans
		WHERE trace_id = ?
		ORDER BY time ASC`, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SpanRow
	for rows.Next() {
		var sp SpanRow
		var t time.Time
		var dur int64
		var attrK, attrV, resK, resV []string
		var eventsJSON string
		if err := rows.Scan(
			&sp.TraceID, &sp.SpanID, &sp.ParentSpanID, &sp.Name, &sp.Kind, &sp.ServiceName, &sp.HostName,
			&t, &dur, &sp.StatusCode, &sp.StatusMessage,
			&attrK, &attrV, &resK, &resV,
			&eventsJSON, &sp.ScopeName,
			&sp.DBSystem, &sp.DBStatement, &sp.HTTPMethod, &sp.HTTPRoute, &sp.HTTPStatus, &sp.PeerService,
		); err != nil {
			return nil, err
		}
		sp.StartTime = t.UnixNano()
		sp.EndTime = t.UnixNano() + dur
		sp.DurationMs = float64(dur) / 1e6
		sp.Attributes = arraysToMap(attrK, attrV)
		sp.ResourceAttributes = arraysToMap(resK, resV)
		json.Unmarshal([]byte(eventsJSON), &sp.Events)
		out = append(out, sp)
	}
	return out, rows.Err()
}

// ── Log queries ───────────────────────────────────────────────────────────────

type LogFilter struct {
	Service     string
	Search      string
	From, To    time.Time
	SeverityMin uint8
	TraceID     string
	SpanID      string // optional: only logs attached to this span
	Limit       int
	Offset      int
}

func (s *Store) GetLogs(ctx context.Context, f LogFilter) ([]LogRow, uint64, error) {
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
	if f.Search != "" {
		wc.add("body LIKE ?", "%"+f.Search+"%")
	}
	if f.SeverityMin > 0 {
		wc.add("severity_num >= ?", f.SeverityMin)
	}
	if f.TraceID != "" {
		wc.add("trace_id = ?", f.TraceID)
	}
	if f.SpanID != "" {
		wc.add("span_id = ?", f.SpanID)
	}
	if f.Limit == 0 {
		f.Limit = 100
	}

	var total uint64
	if err := s.conn.QueryRow(ctx, "SELECT count() FROM logs "+wc.sql(), wc.args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args := append(wc.args, f.Limit, f.Offset)
	rows, err := s.conn.Query(ctx, `
		SELECT rowNumberInAllBlocks() AS id,
		       time, severity_num, severity_text, body,
		       service_name, trace_id, span_id,
		       attr_keys, attr_values, res_keys, res_values
		FROM logs `+wc.sql()+`
		ORDER BY time DESC
		LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []LogRow
	for rows.Next() {
		var lr LogRow
		var t time.Time
		var attrK, attrV, resK, resV []string
		if err := rows.Scan(
			&lr.ID, &t, &lr.SeverityNumber, &lr.SeverityText, &lr.Body,
			&lr.ServiceName, &lr.TraceID, &lr.SpanID,
			&attrK, &attrV, &resK, &resV,
		); err != nil {
			return nil, 0, err
		}
		lr.Timestamp = t.UnixNano()
		lr.Attributes = arraysToMap(attrK, attrV)
		lr.ResourceAttributes = arraysToMap(resK, resV)
		out = append(out, lr)
	}
	return out, total, rows.Err()
}

// ── Metric queries ────────────────────────────────────────────────────────────

func (s *Store) GetMetricNames(ctx context.Context, service string) ([]MetricInfo, error) {
	var wc whereClause
	if service != "" {
		wc.add("service_name = ?", service)
	}
	rows, err := s.conn.Query(ctx,
		`SELECT DISTINCT metric, any(description), any(unit), any(instrument)
		 FROM metric_points `+wc.sql()+
			` GROUP BY metric ORDER BY metric`, wc.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MetricInfo
	for rows.Next() {
		var mi MetricInfo
		rows.Scan(&mi.Name, &mi.Description, &mi.Unit, &mi.Type)
		out = append(out, mi)
	}
	return out, rows.Err()
}

func (s *Store) GetMetricPoints(ctx context.Context, metric, service string, from, to time.Time, limit int) ([]MetricPointRow, error) {
	var wc whereClause
	wc.add("metric = ?", metric)
	if service != "" {
		wc.add("service_name = ?", service)
	}
	if !from.IsZero() {
		wc.add("time >= ?", from)
	}
	if !to.IsZero() {
		wc.add("time <= ?", to)
	}
	if limit == 0 {
		limit = 500
	}
	rows, err := s.conn.Query(ctx,
		`SELECT time, value, count, sum_value,
		        arrayStringConcat(arrayMap((k, v) -> concat(k, '=', v), attr_keys, attr_values), ',')
		 FROM metric_points `+wc.sql()+
			` ORDER BY time ASC LIMIT ?`,
		append(wc.args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MetricPointRow
	for rows.Next() {
		var p MetricPointRow
		var t time.Time
		rows.Scan(&t, &p.Value, &p.Count, &p.Sum, &p.Attrs)
		p.Time = t.UnixNano()
		out = append(out, p)
	}
	return out, rows.Err()
}

// ── WHERE clause builder ──────────────────────────────────────────────────────

type whereClause struct {
	conds []string
	args  []interface{}
}

func (w *whereClause) add(cond string, args ...interface{}) {
	w.conds = append(w.conds, cond)
	w.args = append(w.args, args...)
}

func (w *whereClause) sql() string {
	if len(w.conds) == 0 {
		return ""
	}
	return "WHERE " + strings.Join(w.conds, " AND ")
}
