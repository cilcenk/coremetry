package logstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// CHStore adapts the existing ClickHouse logs table to the LogStore
// interface. Pure delegation — chstore.GetLogs already takes a similar
// filter shape.
type CHStore struct {
	store *chstore.Store
}

func NewCH(store *chstore.Store) *CHStore { return &CHStore{store: store} }

func (s *CHStore) Backend() string { return "clickhouse" }

// Ping delegates to the wrapped chstore — the same CH connection the
// rest of the app uses, so no separate liveness contract.
func (s *CHStore) Ping(ctx context.Context) error { return s.store.Ping(ctx) }

func (s *CHStore) Search(ctx context.Context, f Filter) (*Page, error) {
	rows, total, next, err := s.store.GetLogs(ctx, chstore.LogFilter{
		Service:     f.Service,
		Search:      f.Search,
		From:        f.From,
		To:          f.To,
		SeverityMin: f.SeverityMin,
		TraceID:     f.TraceID,
		SpanID:      f.SpanID,
		Limit:       f.Limit,
		Offset:      f.Offset,
		Cursor:      f.Cursor, // v0.7.22 — opaque CH keyset token round-trip
		Ascending:   f.Ascending, // v0.7.83 — oldest-first for Context "after"
		SinceNs:     f.SinceNs,   // v0.8.x — forward-tail (live-tail SSE)
	})
	if err != nil {
		return nil, err
	}
	out := make([]*LogRecord, 0, len(rows))
	for _, l := range rows {
		out = append(out, &LogRecord{
			ID:                 int64(l.ID),
			Timestamp:          l.Timestamp,
			Severity:           l.SeverityNumber,
			SeverityText:       l.SeverityText,
			Body:               l.Body,
			ServiceName:        l.ServiceName,
			TraceID:            l.TraceID,
			SpanID:             l.SpanID,
			Attributes:         l.Attributes,
			ResourceAttributes: l.ResourceAttributes,
		})
	}
	return &Page{Total: int(total), Logs: out, NextCursor: next}, nil
}

// Histogram buckets log volume server-side via the same logs
// table. Whitelisted groupBy options ("service", "severity",
// or "" for total) map to indexed LowCardinality columns so the
// query stays partition-pruned + index-friendly even at billion
// log/day. Unknown groupBy collapses to a single _total series
// rather than failing — operator notices empty break-down and
// can pick a different field.
func (s *CHStore) Histogram(ctx context.Context, f Filter, bucketSec int, groupBy string) ([]LogSeries, error) {
	if bucketSec <= 0 {
		bucketSec = 30
	}
	groupExpr := "'_total'"
	switch groupBy {
	case "service":
		groupExpr = "service_name"
	case "severity":
		groupExpr = "if(severity_text != '', severity_text, toString(severity_num))"
	}

	from, to := f.From, f.To
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	args := []any{from, to}
	wc := "time >= ? AND time <= ?"
	if f.Cluster != "" {
		// v0.5.471 — coalesce the three resource-attribute paths
		// the chstore.spans table also uses (clusterDeriveExpr).
		// logs reuses the same key conventions emitted by OTel
		// SDKs at init time.
		wc += ` AND coalesce(
			nullIf(resource_attributes['k8s.cluster.name'], ''),
			nullIf(resource_attributes['openshift.cluster.name'], ''),
			nullIf(resource_attributes['cluster'], ''),
			''
		) = ?`
		args = append(args, f.Cluster)
	}
	if f.Service != "" {
		wc += " AND service_name = ?"
		args = append(args, f.Service)
	}
	if f.Search != "" {
		// multiSearchAnyCaseInsensitive uses the tokenbf_v1 index
		// on body via the per-token bloom filter, so granules
		// that don't contain the search substring are pruned
		// before the row-level match runs. positionCaseInsensitive
		// (the obvious choice) cannot use the index — at
		// billion-log/day scale that's a full scan.
		wc += " AND multiSearchAnyCaseInsensitive(body, [?])"
		args = append(args, f.Search)
	}
	if f.SeverityMin > 0 {
		wc += " AND severity_num >= ?"
		args = append(args, f.SeverityMin)
	}
	if f.TraceID != "" {
		wc += " AND trace_id = ?"
		args = append(args, f.TraceID)
	}
	// v0.5.271 — multi-trace filter for the DQL cross-signal
	// join. AND-merge with the single-trace TraceID; mostly
	// they're mutually exclusive in practice (UI uses one,
	// join executor uses the other).
	if len(f.TraceIDs) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(f.TraceIDs)), ",")
		wc += " AND trace_id IN (" + placeholders + ")"
		for _, id := range f.TraceIDs {
			args = append(args, id)
		}
	}

	// Top-20 groups by total count (mirrors the ES path's
	// terms.size:20 cap). Without this a high-cardinality group
	// like service_name with 10k+ services would return 10k ×
	// N_buckets rows; the chart can't render that anyway.
	sql := fmt.Sprintf(`
		WITH top_groups AS (
		  SELECT %s AS g, count() AS c
		  FROM logs WHERE %s
		  GROUP BY g
		  ORDER BY c DESC
		  LIMIT 20
		)
		SELECT %s AS g,
		       toStartOfInterval(time, INTERVAL %d SECOND) AS bucket,
		       count() AS c
		FROM logs
		WHERE %s AND (%s) GLOBAL IN (SELECT g FROM top_groups)
		GROUP BY g, bucket
		ORDER BY g, bucket
		SETTINGS max_execution_time = 30,
		         distributed_product_mode = 'global'`,
		groupExpr, wc,
		groupExpr, bucketSec, wc, groupExpr)
	// The IN-subquery references the same args twice (top_groups
	// CTE + outer SELECT), so we duplicate the binding list.
	args = append([]any{}, append(args, args...)...)

	rows, err := s.store.Conn().Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byName := map[string]*LogSeries{}
	order := []string{}
	for rows.Next() {
		var g string
		var t time.Time
		var c uint64
		if err := rows.Scan(&g, &t, &c); err != nil {
			return nil, err
		}
		s, ok := byName[g]
		if !ok {
			s = &LogSeries{Name: g}
			byName[g] = s
			order = append(order, g)
		}
		s.Points = append(s.Points, LogPoint{T: t.UnixNano(), V: int64(c)})
	}
	out := make([]LogSeries, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out, rows.Err()
}

// CountPatterns matches each detector pattern against the raw
// logs table. CH path runs sequentially because each query is
// already cheap behind the tokenbf_v1 skip index — granules
// without any of the tokens get pruned before the per-row regex
// eval. ES batches via _msearch (see elasticsearch.go) because
// its per-pattern cost dominates the round-trip cost there.
func (s *CHStore) CountPatterns(
	ctx context.Context,
	pats []PatternSpec,
	curStart, baseStart, now time.Time,
) ([]PatternStats, error) {
	out := make([]PatternStats, len(pats))
	for i, pat := range pats {
		stats, err := s.countOnePattern(ctx, pat, curStart, baseStart, now)
		if err != nil {
			return out, err
		}
		out[i] = stats
	}
	return out, nil
}

func (s *CHStore) countOnePattern(
	ctx context.Context,
	pat PatternSpec,
	curStart, baseStart, now time.Time,
) (PatternStats, error) {
	var out PatternStats
	tokensSQL := chBuildTokenLiteral(pat.Tokens)
	where := "time >= ? AND time < ? AND match(body, ?)"
	if tokensSQL != "" {
		where = "time >= ? AND time < ? AND multiSearchAnyCaseInsensitive(body, " + tokensSQL + ") AND match(body, ?)"
	}
	sql := `
		SELECT
		  countIf(time >= ?)                                      AS cur,
		  countIf(time <  ?)                                      AS base,
		  anyHeavyIf(service_name, time >= ?)                     AS svc,
		  anyIf(body, time >= ?)                                  AS sample,
		  toUnixTimestamp64Nano(maxIf(time, time >= ?))           AS last_ns
		FROM logs
		WHERE ` + where
	err := s.store.Conn().QueryRow(ctx, sql,
		curStart, curStart, curStart, curStart, curStart,
		baseStart, now, pat.Regex,
	).Scan(&out.Cur, &out.Base, &out.Service, &out.Sample, &out.LastSeenNs)
	if err != nil {
		return PatternStats{}, err
	}
	// v0.5.287 — per-service breakdown for the current window
	// (top 5). Only fires when cur > 0 so the no-match common
	// case stays one query. The WHERE clause + tokenbf prefilter
	// matches the aggregate above, so the same granules are
	// pruned + the LowCardinality service_name GROUP BY is
	// in-memory after the existing scan.
	if out.Cur > 0 {
		topSQL := `
			SELECT service_name, count() AS cnt
			FROM logs
			WHERE time >= ? AND time < ? AND ` +
			func() string {
				if tokensSQL != "" {
					return "multiSearchAnyCaseInsensitive(body, " + tokensSQL + ") AND match(body, ?)"
				}
				return "match(body, ?)"
			}() + `
			GROUP BY service_name
			ORDER BY cnt DESC
			LIMIT 5
			SETTINGS max_execution_time = 5`
		topRows, terr := s.store.Conn().Query(ctx, topSQL, curStart, now, pat.Regex)
		if terr == nil {
			defer topRows.Close()
			for topRows.Next() {
				var svc string
				var cnt uint64
				if err := topRows.Scan(&svc, &cnt); err != nil {
					break
				}
				out.TopServices = append(out.TopServices, PatternServiceHit{
					Service: svc, Count: cnt,
				})
			}
		}
	}
	return out, nil
}

// chBuildTokenLiteral renders []string as a CH array literal
// `['t1', 't2', …]` for inlining. Tokens are detector-supplied
// (no untrusted input) so we just escape embedded single quotes.
func chBuildTokenLiteral(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	parts := make([]string, len(tokens))
	for i, t := range tokens {
		parts[i] = "'" + strings.ReplaceAll(t, "'", "\\'") + "'"
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// EQLSearch — CH stub (v0.5.468). EQL is ES-native; CH has no
// equivalent sequence-matching aggregate. Returning a typed
// error lets the handler surface "not supported on this
// backend" cleanly and lets the frontend hide the EQL panel.
func (s *CHStore) EQLSearch(ctx context.Context, q EQLQuery) ([]EQLSequence, error) {
	_ = ctx
	_ = q
	return nil, fmt.Errorf("EQL is Elasticsearch-only (ClickHouse backend has no equivalent)")
}

// Indices — CH stub (v0.5.466). Single physical table on the
// CH backend; per-shard / part-level surface is on the existing
// /admin/clickhouse page. Returning nil makes /admin/elastic
// render its "logs backend isn't Elasticsearch" empty state
// without touching CH-specific plumbing.
func (s *CHStore) Indices(ctx context.Context) ([]IndexInfo, error) {
	_ = ctx
	return nil, nil
}

// FieldValues — CH stub (v0.5.464). The KQL search box's
// field-aware autocomplete is Kibana-flavoured and primarily
// useful on ES installs; CH operators tend to filter via the
// explicit FilterBuilder UI instead. Returning empty makes the
// autocomplete simply not surface on CH backends, which
// degrades gracefully without spurious "no matches" rows.
// A CH-native implementation (SELECT DISTINCT field WHERE
// field LIKE prefix% LIMIT) can land as a follow-up if
// operators report wanting it.
func (s *CHStore) FieldValues(ctx context.Context, field, prefix string, limit int) ([]string, error) {
	_ = ctx
	_ = field
	_ = prefix
	_ = limit
	return nil, nil
}

// FieldStats — top-N values of one field in the filtered window
// (fields-panel accordion, v0.8.255). Well-known ids resolve to
// their indexed columns; anything else is looked up in the
// attributes map first, then resource_attributes. Grouping is
// capped (max_rows_to_group_by + 'any' overflow) so a pathological
// high-cardinality field (trace_id…) degrades to approximate
// counts instead of blowing memory at billion-row scale.
func (s *CHStore) FieldStats(ctx context.Context, f Filter, field string, limit int) (*FieldStatsResult, error) {
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	valueExpr := ""
	switch field {
	case "service", "service.name", "service_name":
		valueExpr = "service_name"
	case "severity", "level", "log.level", "severity_text":
		valueExpr = "if(severity_text != '', severity_text, toString(severity_num))"
	default:
		valueExpr = "coalesce(nullIf(attributes[?], ''), nullIf(resource_attributes[?], ''), '')"
	}

	from, to := f.From, f.To
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	args := []any{}
	if strings.Contains(valueExpr, "?") {
		args = append(args, field, field)
	}
	args = append(args, from, to)
	wc := "time >= ? AND time <= ?"
	if f.Cluster != "" {
		wc += ` AND coalesce(
			nullIf(resource_attributes['k8s.cluster.name'], ''),
			nullIf(resource_attributes['openshift.cluster.name'], ''),
			nullIf(resource_attributes['cluster'], ''),
			''
		) = ?`
		args = append(args, f.Cluster)
	}
	if f.Service != "" {
		wc += " AND service_name = ?"
		args = append(args, f.Service)
	}
	if f.Search != "" {
		wc += " AND multiSearchAnyCaseInsensitive(body, [?])"
		args = append(args, f.Search)
	}
	if f.SeverityMin > 0 {
		wc += " AND severity_num >= ?"
		args = append(args, f.SeverityMin)
	}
	if f.TraceID != "" {
		wc += " AND trace_id = ?"
		args = append(args, f.TraceID)
	}
	args = append(args, limit)

	// Window function over the grouped subquery: one scan yields both
	// the top-N rows and the all-values total for the % denominator.
	sql := fmt.Sprintf(`
		SELECT v, c, tot FROM (
		  SELECT v, c, sum(c) OVER () AS tot FROM (
		    SELECT %s AS v, count() AS c
		    FROM logs
		    WHERE %s
		    GROUP BY v
		    HAVING v != ''
		  )
		)
		ORDER BY c DESC
		LIMIT ?
		SETTINGS max_execution_time = 15,
		         max_rows_to_group_by = 100000,
		         group_by_overflow_mode = 'any'`, valueExpr, wc)

	rows, err := s.store.Conn().Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &FieldStatsResult{Field: field, Values: []FieldValueCount{}}
	for rows.Next() {
		var v string
		var c, tot uint64
		if err := rows.Scan(&v, &c, &tot); err != nil {
			return nil, err
		}
		out.Total = int64(tot)
		out.Values = append(out.Values, FieldValueCount{Value: v, Count: int64(c)})
	}
	return out, rows.Err()
}

// ─── Trace-context self-discovery (v0.8.348, pivot Phase 1c) ────────────────
//
// CH sibling of the ES field_caps + coverage probe so the /admin surface
// works on BOTH backends. The mapping half is trivial here — `trace_id` is a
// fixed String column on the Coremetry-created logs table, exact-match
// lookups always work — so the report is really about coverage: what share
// of the last-24h logs actually carry a trace id. Both queries are bounded
// per the house rule (time-bounded WHERE + LIMIT + max_execution_time);
// per-service grouping caps mirror FieldStats' overflow guards. Consts so
// clickhouse_trace_context_test.go pins the bounded shape.

const chTraceCoverageSQL = `
	SELECT count() AS total, countIf(trace_id != '') AS with_trace
	FROM logs
	WHERE time >= ? AND time <= ?
	LIMIT 1
	SETTINGS max_execution_time = 10`

const chTraceCoverageTopSQL = `
	SELECT service_name, count() AS total, countIf(trace_id != '') AS with_trace
	FROM logs
	WHERE time >= ? AND time <= ?
	GROUP BY service_name
	ORDER BY total DESC
	LIMIT 50
	SETTINGS max_execution_time = 10,
	         max_rows_to_group_by = 100000,
	         group_by_overflow_mode = 'any'`

// TraceContextDiagnostics implements TraceContextDiagnoser. Same error
// posture as the ES side: backend failures come back as a typed report
// (available:false + reason for the overall count; verdict kept + Reason
// set when only the per-service breakdown failed) — never a raw 5xx.
func (s *CHStore) TraceContextDiagnostics(ctx context.Context) (*TraceContextReport, error) {
	to := time.Now()
	from := to.Add(-24 * time.Hour)
	rep := &TraceContextReport{
		Available:      true,
		EffectiveField: "trace_id",
		EffectiveType:  "String", // fixed schema column — exact match always works
		PivotReady:     true,
		WindowHours:    24,
		Fields: []TraceContextField{{
			Name: "trace_id", Types: []string{"String"},
			Searchable: true, Aggregatable: true, Configured: true,
		}},
		Services: []TraceContextServiceCoverage{},
	}
	var total, withTrace uint64
	if err := s.store.Conn().QueryRow(ctx, chTraceCoverageSQL, from, to).Scan(&total, &withTrace); err != nil {
		return &TraceContextReport{
			Available: false, Reason: "coverage query failed: " + err.Error(),
			EffectiveType: "absent",
			Fields:        []TraceContextField{},
			Services:      []TraceContextServiceCoverage{},
			WindowHours:   24,
		}, nil
	}
	rep.Total, rep.WithTrace = int64(total), int64(withTrace)

	rows, err := s.store.Conn().Query(ctx, chTraceCoverageTopSQL, from, to)
	if err != nil {
		rep.Reason = "per-service coverage failed: " + err.Error()
		return rep, nil
	}
	defer rows.Close()
	for rows.Next() {
		var svc string
		var t, w uint64
		if err := rows.Scan(&svc, &t, &w); err != nil {
			rep.Reason = "per-service coverage failed: " + err.Error()
			return rep, nil
		}
		rep.Services = append(rep.Services, TraceContextServiceCoverage{
			Service: svc, Total: int64(t), WithTrace: int64(w),
		})
	}
	if err := rows.Err(); err != nil {
		rep.Reason = "per-service coverage failed: " + err.Error()
	}
	return rep, nil
}

