package chstore

// DB statement DETAIL reads — v0.8.378, Stage-2 slice D2
// (docs/pages-enhancement-audit.md §2 Faz D). The /slow-queries catalog row
// drills into ONE statement class, keyed by the persistent identity D1
// introduced (spans.db_stmt_hash / db_statement_summary_5m.stmt_hash —
// dbstmt.go parity contract). Four readers, all statement-scoped:
//
//   • GetDBStmtSummary — window totals for the drawer header strip
//     (calls / errors / avg / p95 / p99 / max) + the bucket sample the
//     display form is re-derived from. Also the compare=prior source:
//     the API layer runs it twice (shifted window) and fills the Prior*
//     fields — the Endpoints v0.5.404 / M1 v0.8.364 merge pattern.
//   • GetDBStmtTrend — 5m-grain latency/error TREND series (coarsened
//     to ≤ ~288 points on wide windows so the payload stays bounded).
//   • GetDBStmtCallers — per-service breakdown; service_name is a real
//     MV dimension, so "who issues this query" is one GROUP BY away.
//   • DBStmtExemplars — the TRUE exemplar pivot the audit wanted:
//     spans.db_stmt_hash = ? over a time-bounded raw read resolves the
//     slowest (+ worst-error) trace_id for the exact statement class —
//     replacing the lossy `db.statement LIKE prefix%` deep-link.
//
// MV-first: the three aggregate readers touch ONLY db_statement_summary_5m
// (bare name — the MV is a highVolumeTables member, so cluster mode reads
// its Distributed wrapper). Raw spans appear ONLY in the exemplar lookup,
// which returns two ids, not an aggregate. Same alias rule as
// slowQueriesGlobalMVSQL throughout: with the optional db_system/db_name
// WHERE filters present, a SELECT alias named after either column would
// make ClickHouse resolve the WHERE identifier to the alias and reject the
// query with code 184 (the v0.8.362 incident class) — pinned by
// dbstmt_detail_test.go.

import (
	"context"
	"log"
	"time"
)

// DBStmtDetailQuery bundles the statement-detail inputs. Hash is the
// REQUIRED identity (already parsed from the API's decimal string —
// 0 is the "no statement" sentinel and never reaches here); DBSystem /
// DBName optionally narrow a hash class that shows up under more than
// one engine/database (the MV keeps them as dimensions; the catalog
// folds across them).
type DBStmtDetailQuery struct {
	Hash     uint64
	DBSystem string
	DBName   string
	From, To time.Time
}

// dbStmtDetailWhere builds the shared MV predicate. The window start
// snaps DOWN to the MV's 5-minute grid (the getSlowQueriesGlobalMV /
// GetDBTrends trick) so a rolling window covers whole buckets instead of
// half-clipping the first one.
func dbStmtDetailWhere(q DBStmtDetailQuery) whereClause {
	var wc whereClause
	wc.add("stmt_hash = ?", q.Hash)
	wc.add("time_bucket >= ?", q.From.Truncate(5*time.Minute))
	wc.add("time_bucket <= ?", q.To)
	if q.DBSystem != "" {
		wc.add("db_system = ?", q.DBSystem)
	}
	if q.DBName != "" {
		wc.add("db_name = ?", q.DBName)
	}
	return wc
}

// DBStmtSummary is the drawer-header rollup for one statement class.
// Prior* fields are additive — filled by the API layer's second
// (shifted-window) read when ?compare=prior, zero otherwise (the
// EndpointRow Prior* convention: prior-absent renders as "NEW").
type DBStmtSummary struct {
	// One real bucket sample — the API re-derives the normalized display
	// form via NormalizeDBStatement (hash-consistent by construction).
	SampleStatement string  `json:"sampleStatement"`
	DBSystem        string  `json:"dbSystem"`
	DBName          string  `json:"dbName"`
	Calls           uint64  `json:"calls"`
	Errors          uint64  `json:"errors"`
	TotalMs         float64 `json:"totalMs"`
	AvgMs           float64 `json:"avgMs"`
	P95Ms           float64 `json:"p95Ms"`
	P99Ms           float64 `json:"p99Ms"`
	MaxMs           float64 `json:"maxMs"`
	PriorCalls      uint64  `json:"priorCalls,omitempty"`
	PriorErrors     uint64  `json:"priorErrors,omitempty"`
	PriorAvgMs      float64 `json:"priorAvgMs,omitempty"`
	PriorP95Ms      float64 `json:"priorP95Ms,omitempty"`
}

// dbStmtSummarySQL — pure builder, pinned by dbstmt_detail_test.go.
// db_sys / db_nm aliases, NEVER `AS db_system` / `AS db_name` (code-184
// alias-shadow class, v0.8.362).
func dbStmtSummarySQL(where string) string {
	return `
		SELECT
			anyMerge(sample_stmt_state)                 AS sample_stmt,
			any(db_system)                              AS db_sys,
			any(db_name)                                AS db_nm,
			countMerge(span_count_state)                AS cnt,
			countIfMerge(error_count_state)             AS err_cnt,
			sumMerge(duration_sum_state) / 1e6          AS total_ms,
			arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95_ms,
			arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms,
			maxMerge(duration_max_state) / 1e6          AS max_ms
		FROM db_statement_summary_5m ` + where + `
		LIMIT 1
		SETTINGS max_execution_time = 10`
}

// GetDBStmtSummary returns the statement's window rollup, or (nil, nil)
// when the class has no rows in the window (a GROUP-BY-less aggregate
// always yields one row — cnt==0 IS the "not found" signal). safeF on
// every merged float: TDigest merges over edge-case states can yield NaN
// and encoding/json rejects NaN (the v0.5.301 500-class).
func (s *Store) GetDBStmtSummary(ctx context.Context, q DBStmtDetailQuery) (*DBStmtSummary, error) {
	wc := dbStmtDetailWhere(q)
	row := s.conn.QueryRow(ctx, dbStmtSummarySQL(wc.sql()), wc.args...)
	var out DBStmtSummary
	var totalMs, p95, p99, maxMs float64
	if err := row.Scan(&out.SampleStatement, &out.DBSystem, &out.DBName,
		&out.Calls, &out.Errors, &totalMs, &p95, &p99, &maxMs); err != nil {
		return nil, err
	}
	if out.Calls == 0 {
		return nil, nil
	}
	out.TotalMs = safeF(&totalMs)
	out.AvgMs = out.TotalMs / float64(out.Calls)
	out.P95Ms = safeF(&p95)
	out.P99Ms = safeF(&p99)
	out.MaxMs = safeF(&maxMs)
	return &out, nil
}

// DBStmtTrendPoint is one trend bucket: bucket-start ns + RED-shaped
// values for the statement class.
type DBStmtTrendPoint struct {
	TsNs   int64   `json:"tsNs"`
	Calls  uint64  `json:"calls"`
	Errors uint64  `json:"errors"`
	AvgMs  float64 `json:"avgMs"`
	P95Ms  float64 `json:"p95Ms"`
}

// dbStmtTrendMaxPoints bounds the trend payload — ~288 points covers a
// 24h window at the MV's native 5m grain; wider windows coarsen instead
// of shipping tens of thousands of buckets (90d at 5m = 25 920 rows).
const dbStmtTrendMaxPoints = 288

// dbStmtTrendBucketSec picks the trend bucket width: the MV's native
// 300s up to 24h, then the smallest 5m multiple keeping the series at
// ≤ dbStmtTrendMaxPoints. Pure — table-pinned (v0.8.378).
func dbStmtTrendBucketSec(from, to time.Time) int64 {
	windowSec := to.Unix() - from.Unix()
	if windowSec <= 0 {
		return 300
	}
	b := (windowSec + dbStmtTrendMaxPoints - 1) / dbStmtTrendMaxPoints
	b = ((b + 299) / 300) * 300 // round UP to the 5m MV grain
	if b < 300 {
		b = 300
	}
	return b
}

// dbStmtTrendSQL — pure builder. Buckets via intDiv over the MV's
// time_bucket (the GetEndpointsMV sparkline shape); the two intDiv args
// (bucket start unix, bucket width) bind BEFORE the WHERE args. LIMIT
// carries headroom over dbStmtTrendMaxPoints for rounding at the edges.
func dbStmtTrendSQL(where string) string {
	return `
		SELECT
			intDiv(toUnixTimestamp(time_bucket) - ?, ?)     AS b,
			countMerge(span_count_state)                    AS cnt,
			countIfMerge(error_count_state)                 AS err_cnt,
			sumMerge(duration_sum_state) / 1e6              AS total_ms,
			arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95_ms
		FROM db_statement_summary_5m ` + where + `
		GROUP BY b
		ORDER BY b
		LIMIT 400
		SETTINGS max_execution_time = 10`
}

// GetDBStmtTrend returns the statement's trend series — one MV scan,
// GROUP BY coarsened bucket. Buckets with no data are absent (sparse);
// the returned bucketSec lets the frontend densify against the window
// exactly (gaps render as zeros) without re-deriving the coarsening.
func (s *Store) GetDBStmtTrend(ctx context.Context, q DBStmtDetailQuery) (points []DBStmtTrendPoint, bucketSec int64, err error) {
	bucketStart := q.From.Truncate(5 * time.Minute)
	bucketSec = dbStmtTrendBucketSec(q.From, q.To)
	wc := dbStmtDetailWhere(q)
	args := append([]any{bucketStart.Unix(), bucketSec}, wc.args...)
	rows, err := s.conn.Query(ctx, dbStmtTrendSQL(wc.sql()), args...)
	if err != nil {
		return nil, bucketSec, err
	}
	defer rows.Close()
	out := []DBStmtTrendPoint{}
	for rows.Next() {
		var b int64
		var p DBStmtTrendPoint
		var totalMs, p95 float64
		if err := rows.Scan(&b, &p.Calls, &p.Errors, &totalMs, &p95); err != nil {
			return nil, bucketSec, err
		}
		p.TsNs = (bucketStart.Unix() + b*bucketSec) * int64(time.Second)
		if p.Calls > 0 {
			p.AvgMs = safeF(&totalMs) / float64(p.Calls)
		}
		p.P95Ms = safeF(&p95)
		out = append(out, p)
	}
	return out, bucketSec, rows.Err()
}

// DBStmtCaller is one service's slice of the statement class. Prior*
// additive, same contract as DBStmtSummary.
type DBStmtCaller struct {
	Service     string  `json:"service"`
	Calls       uint64  `json:"calls"`
	Errors      uint64  `json:"errors"`
	AvgMs       float64 `json:"avgMs"`
	P95Ms       float64 `json:"p95Ms"`
	TotalMs     float64 `json:"totalMs"`
	PriorCalls  uint64  `json:"priorCalls,omitempty"`
	PriorErrors uint64  `json:"priorErrors,omitempty"`
	PriorAvgMs  float64 `json:"priorAvgMs,omitempty"`
	PriorP95Ms  float64 `json:"priorP95Ms,omitempty"`
}

// dbStmtCallersSQL — pure builder. Ordered by total wall-clock time
// (the catalog's own "worth optimising" metric) so the top caller is
// the service actually burning DB time, not just the chattiest.
func dbStmtCallersSQL(where string) string {
	return `
		SELECT
			service_name,
			countMerge(span_count_state)                AS cnt,
			countIfMerge(error_count_state)             AS err_cnt,
			sumMerge(duration_sum_state) / 1e6          AS total_ms,
			arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95_ms
		FROM db_statement_summary_5m ` + where + `
		GROUP BY service_name
		ORDER BY total_ms DESC
		LIMIT ?
		SETTINGS max_execution_time = 10`
}

// GetDBStmtCallers returns the per-service breakdown of the statement
// class, top `limit` by total time.
func (s *Store) GetDBStmtCallers(ctx context.Context, q DBStmtDetailQuery, limit int) ([]DBStmtCaller, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	wc := dbStmtDetailWhere(q)
	args := append(append([]any{}, wc.args...), limit)
	rows, err := s.conn.Query(ctx, dbStmtCallersSQL(wc.sql()), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DBStmtCaller{}
	for rows.Next() {
		var c DBStmtCaller
		var totalMs, p95 float64
		if err := rows.Scan(&c.Service, &c.Calls, &c.Errors, &totalMs, &p95); err != nil {
			return nil, err
		}
		c.TotalMs = safeF(&totalMs)
		if c.Calls > 0 {
			c.AvgMs = c.TotalMs / float64(c.Calls)
		}
		c.P95Ms = safeF(&p95)
		out = append(out, c)
	}
	return out, rows.Err()
}

// dbStmtExemplarWhere builds the raw-spans exemplar predicate: the
// (…, time) PK time bound leads, then the stored-column identity match.
// db_name is deliberately NOT filtered here — on spans it lives in the
// attr arrays (the MV derives it at insert), and an attr lookup per row
// would defeat the bounded-scan posture; hash + optional db_system is
// already exact for the class.
func dbStmtExemplarWhere(q DBStmtDetailQuery, errorOnly bool) whereClause {
	var wc whereClause
	wc.add("time >= ?", q.From)
	wc.add("time <= ?", q.To)
	wc.add("db_stmt_hash = ?", q.Hash)
	if q.DBSystem != "" {
		wc.add("db_system = ?", q.DBSystem)
	}
	if errorOnly {
		wc.add("status_code = 'error'")
	}
	return wc
}

// dbStmtExemplarSQL — pure builder for the true-exemplar raw read:
// slowest matching span's trace_id (ORDER BY duration DESC LIMIT 1),
// time-bounded, 10s capped.
func dbStmtExemplarSQL(where, shardSetting string) string {
	return `
		SELECT trace_id
		FROM spans ` + where + `
		ORDER BY duration DESC
		LIMIT 1
		SETTINGS max_execution_time = 10,
		         ` + shardSetting
}

// DBStmtExemplars resolves the statement class's slow + error exemplar
// trace_ids — the TRUE pivot D2 replaces the lossy `db.statement LIKE
// prefix%` deep-link with (v0.8.378). Two bounded raw-spans point reads
// keyed on the stored db_stmt_hash column; empty ids mean "no exemplar"
// (no traffic / all-healthy window) and surface as a soft-missing
// section, never an error. Guarded on the D1 boot probe: installs where
// the column couldn't land (external Distributed, cluster_name unset)
// skip the raw read entirely.
func (s *Store) DBStmtExemplars(ctx context.Context, q DBStmtDetailQuery) (slowTraceID, errorTraceID string, err error) {
	if !s.hasDBStmtHashCol {
		return "", "", nil
	}
	for _, target := range []struct {
		errorOnly bool
		dst       *string
	}{
		{false, &slowTraceID},
		{true, &errorTraceID},
	} {
		if ctx.Err() != nil {
			return slowTraceID, errorTraceID, ctx.Err()
		}
		wc := dbStmtExemplarWhere(q, target.errorOnly)
		row := s.conn.QueryRow(ctx, dbStmtExemplarSQL(wc.sql(), s.shardSkipSetting()), wc.args...)
		// v0.8.564 — no-rows is the legitimate "no exemplar in window"
		// and stays silent; every OTHER scan error used to be swallowed
		// by a bare `_ =`, which at prod scale meant a 10s
		// max_execution_time timeout's ONLY symptom was a drawer with no
		// exemplar link. Still soft (the exemplar is decoration, the
		// drawer's stats must ship), but the failure is now visible.
		if err := row.Scan(target.dst); err != nil && !isNoRows(err) {
			log.Printf("[chstore] dbstmt exemplar read (errorOnly=%v) failed — drawer renders without the link: %v", target.errorOnly, err)
		}
	}
	return slowTraceID, errorTraceID, nil
}
