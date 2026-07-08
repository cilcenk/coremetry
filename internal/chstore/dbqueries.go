package chstore

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DBQueryStat is one row in the database query analyzer — a
// single normalized statement aggregated across every span
// that issued it for the given service in the time window.
//
// Normalisation collapses literal-only differences ("WHERE
// id = 1" vs "WHERE id = 2") so a single hot query surfaces
// as one row rather than thousands of near-duplicates. The
// sample statement keeps a real example so the operator can
// see what literals were involved without losing the
// aggregation benefit.
type DBQueryStat struct {
	// Normalised statement — literals replaced with "?". Used
	// as the GROUP BY key in CH and as the row label in the UI.
	Statement string `json:"statement"`
	// One real (non-normalised) example of the query so the
	// operator sees actual values, not just placeholders.
	SampleStatement string `json:"sampleStatement"`
	DBSystem        string `json:"dbSystem"`
	// Span counts + latency stats for the bucket.
	Count      int     `json:"count"`
	AvgMs      float64 `json:"avgMs"`
	P95Ms      float64 `json:"p95Ms"`
	P99Ms      float64 `json:"p99Ms"`
	MaxMs      float64 `json:"maxMs"`
	ErrorCount int     `json:"errorCount"`
	// TotalMs = count × avgMs — the aggregate wall-clock cost
	// of this query class in the window. Sorting by total ms
	// surfaces the queries actually worth optimising (a 50ms
	// query running 10k times is a bigger problem than a 500ms
	// one running once, but the second one beats it on max).
	TotalMs float64 `json:"totalMs"`
}

// SlowQueryRow extends DBQueryStat with the originating service
// so the global slow-query catalog (v0.5.165) can show which
// service is responsible. The same query text issued from two
// different services is intentionally kept as two rows — same
// SQL, different teams to ping.
type SlowQueryRow struct {
	DBQueryStat
	Service string `json:"service"`
	// StmtHash — persistent statement identity (v0.8.375, Stage-2 D1):
	// spans.db_stmt_hash / chstore.DBStmtHash as a DECIMAL STRING, because
	// a uint64 in JSON silently loses precision past 2^53 in JS and D2
	// keys the statement detail view on this value (same reason pivot
	// fingerprints ride URLs as decimal strings). Additive: MV-path rows
	// carry the stored column, raw-path rows compute it Go-side from the
	// sample (hash-consistent by the dbstmt.go parity contract); empty
	// only on pre-D1 cached responses.
	StmtHash string `json:"stmtHash,omitempty"`
}

// GetSlowQueriesGlobal — the cross-service slow-query catalog.
// Same normalisation rules as GetTopDBQueries but no service
// filter; grouped by (service, norm_stmt) so the operator sees
// "this query class is hot on payment-api AND on billing-api"
// as separate rows. Ordered by total wall-clock time so the
// top of the list is what's actually worth optimising.
//
// Optional dbSystem filter (e.g. "postgresql") narrows the
// view when the operator already knows which engine they're
// after. Cost is bounded by `db_statement != ''` filter — at
// billion-span scale this still has to scan the partition
// pruning helps for the time window, and CH's index on
// service_name doesn't help here since we don't filter on it.
// 30s execution-time guard keeps the worst case bounded.
// slowQueriesGlobalSQL builds the global slow-queries scan. Pure —
// pinned by TestSlowQueriesGlobalSQLNoAliasShadow (v0.8.362,
// operator-reported: picking a database type on /slow-queries
// 500'd). The sample-system aggregate MUST NOT be aliased back to
// the bare `db_system` column name: with the db_system WHERE
// filter present, ClickHouse resolves the WHERE identifier to the
// SELECT alias and rejects the query with code 184 "Aggregate
// function any(db_system) is found in WHERE".
func slowQueriesGlobalSQL(where, shardSetting string) string {
	return `
		SELECT
			service_name,
			replaceRegexpAll(
				replaceRegexpAll(db_statement, '''[^'']*''', '__P__'),
				'\\b[0-9]+(\\.[0-9]+){0,1}\\b', '__P__'
			)                                          AS norm_stmt,
			any(db_statement)                          AS sample_stmt,
			any(db_system)                             AS db_sys,
			count()                                    AS cnt,
			avg(duration / 1e6)                        AS avg_ms,
			quantile(0.95)(duration / 1e6)             AS p95_ms,
			quantile(0.99)(duration / 1e6)             AS p99_ms,
			max(duration / 1e6)                        AS max_ms,
			countIf(status_code = 'error')             AS err_cnt
		FROM spans ` + where + `
		GROUP BY service_name, norm_stmt
		ORDER BY (cnt * avg_ms) DESC
		LIMIT ?
		SETTINGS max_execution_time = 30,
		         ` + shardSetting
}

// slowQueriesUseMV — v0.8.375 (Stage-2 D1) dispatcher condition, pure so
// the routing is table-tested. The MV path needs (a) the db_stmt_hash
// column + its MV to exist (hasCol — the boot probe, which also gated the
// MV's creation) and (b) an MV-eligible window: the 5-minute bucket grain
// can't resolve narrower windows (the UseSummaryMV boundary the evaluator
// established in v0.6.12). Same dispatcher shape as endpoints.go
// GetEndpoints (v0.8.356): one condition, two whole paths, no mid-query
// mixing.
func slowQueriesUseMV(hasCol bool, from, to time.Time) bool {
	return hasCol && UseSummaryMV(to.Sub(from))
}

// slowQueriesGlobalMVSQL builds the MV-backed catalog read over
// db_statement_summary_5m (v0.8.375, Stage-2 D1). Pure — pinned by
// TestSlowQueriesGlobalMVSQL. Grouping is (service, stmt_hash): the MV's
// finer db_system/db_name dims fold together exactly like the raw path's
// (service, norm_stmt) grouping folds across systems. Same alias rule as
// slowQueriesGlobalSQL: the sample-system aggregate is `db_sys`, NEVER
// `AS db_system` — with the optional db_system WHERE filter present,
// ClickHouse would resolve the WHERE identifier to the SELECT alias and
// reject the query with code 184 (the v0.8.362 incident).
func slowQueriesGlobalMVSQL(where string) string {
	return `
		SELECT
			service_name,
			stmt_hash,
			anyMerge(sample_stmt_state)                 AS sample_stmt,
			any(db_system)                              AS db_sys,
			countMerge(span_count_state)                AS cnt,
			countIfMerge(error_count_state)             AS err_cnt,
			sumMerge(duration_sum_state) / 1e6          AS total_ms,
			arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95_ms,
			arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms,
			maxMerge(duration_max_state) / 1e6          AS max_ms
		FROM db_statement_summary_5m ` + where + `
		GROUP BY service_name, stmt_hash
		ORDER BY total_ms DESC
		LIMIT ?
		SETTINGS max_execution_time = 30`
}

func (s *Store) GetSlowQueriesGlobal(
	ctx context.Context, from, to time.Time, dbSystem string, limit int,
) ([]SlowQueryRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	// v0.8.375 — MV-first dispatch (Stage-2 D1). The raw scan below
	// regex-normalizes + GROUP BYs every db-span in the window per page
	// load; the MV read merges pre-aggregated 5-minute states instead.
	// The raw path SURVIVES as the fallback for sub-5m windows and for
	// installs where the db_stmt_hash column couldn't land (external
	// Distributed cluster with cluster_name unset — probe-driven).
	if slowQueriesUseMV(s.hasDBStmtHashCol, from, to) {
		return s.getSlowQueriesGlobalMV(ctx, from, to, dbSystem, limit)
	}
	const placeholder = "__P__"
	var wc whereClause
	wc.add("time >= ?", from)
	wc.add("time <= ?", to)
	wc.add("db_statement != ''")
	if dbSystem != "" {
		wc.add("db_system = ?", dbSystem)
	}
	sql := slowQueriesGlobalSQL(wc.sql(), s.shardSkipSetting())
	args := append(wc.args, limit)
	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query slow queries: %w", err)
	}
	defer rows.Close()
	out := []SlowQueryRow{}
	for rows.Next() {
		var r SlowQueryRow
		var cnt, errCnt uint64
		if err := rows.Scan(&r.Service, &r.Statement, &r.SampleStatement,
			&r.DBSystem, &cnt, &r.AvgMs, &r.P95Ms, &r.P99Ms, &r.MaxMs, &errCnt); err != nil {
			return nil, err
		}
		r.Count = int(cnt)
		r.ErrorCount = int(errCnt)
		r.TotalMs = float64(cnt) * r.AvgMs
		r.Statement = strings.ReplaceAll(r.Statement, placeholder, "?")
		// v0.8.375 — persistent identity on the raw path too: computed
		// Go-side from the sample. Hash-consistent with the MV path's
		// stored stmt_hash by the dbstmt.go parity contract, so a D2
		// deep-link keyed off a raw-window row resolves the same class.
		r.StmtHash = strconv.FormatUint(DBStmtHash(r.SampleStatement), 10)
		out = append(out, r)
	}
	return out, rows.Err()
}

// getSlowQueriesGlobalMV is the db_statement_summary_5m read behind
// GetSlowQueriesGlobal (v0.8.375, Stage-2 D1). Response shape is
// byte-compatible with the raw path: Statement is the '?'-normalized
// display form (re-derived from the bucket sample via
// NormalizeDBStatement — any sample of a hash class normalizes to the
// class's canonical form by construction), TotalMs comes straight from
// the duration sum (≡ cnt×avg), and MaxMs from the dedicated max state.
// safeF on every merged float: TDigest/aggregate merges can yield NaN on
// edge-case states and encoding/json rejects NaN (the v0.5.301 500-class).
func (s *Store) getSlowQueriesGlobalMV(
	ctx context.Context, from, to time.Time, dbSystem string, limit int,
) ([]SlowQueryRow, error) {
	// Snap the window start DOWN to the MV's 5-minute grid so a rolling
	// window covers whole buckets (the GetDBTrends trick — an unaligned
	// cutoff would half-clip the first bucket).
	bucketStart := from.Truncate(5 * time.Minute)
	var wc whereClause
	wc.add("time_bucket >= ?", bucketStart)
	wc.add("time_bucket <= ?", to)
	if dbSystem != "" {
		wc.add("db_system = ?", dbSystem)
	}
	sql := slowQueriesGlobalMVSQL(wc.sql())
	args := append(wc.args, limit)
	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query slow queries (mv): %w", err)
	}
	defer rows.Close()
	out := []SlowQueryRow{}
	for rows.Next() {
		var r SlowQueryRow
		var stmtHash, cnt, errCnt uint64
		var totalMs, p95, p99, maxMs float64
		if err := rows.Scan(&r.Service, &stmtHash, &r.SampleStatement, &r.DBSystem,
			&cnt, &errCnt, &totalMs, &p95, &p99, &maxMs); err != nil {
			return nil, err
		}
		r.Count = int(cnt)
		r.ErrorCount = int(errCnt)
		r.TotalMs = safeF(&totalMs)
		if cnt > 0 {
			r.AvgMs = r.TotalMs / float64(cnt)
		}
		r.P95Ms = safeF(&p95)
		r.P99Ms = safeF(&p99)
		r.MaxMs = safeF(&maxMs)
		r.Statement = NormalizeDBStatement(r.SampleStatement)
		r.StmtHash = strconv.FormatUint(stmtHash, 10)
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetTopDBQueries returns the top-N normalized DB statements
// for the given service in the time window, ordered by total
// wall-clock time spent in them (count × avgMs).
//
// Performance posture: the query reads only spans where
// db_statement != '' (a small slice of total span volume),
// applies regex normalisation in CH (no Go-side post-pass),
// groups in-store, and the result is bounded by `limit`. At
// billion-span scale it lands in <2s with the (service_name,
// time) primary key handling the partition pruning.
//
// The two replaceRegexpAll passes:
//
//   1. Replace single-quoted string literals with "?". A
//      bracketed character class with negation handles
//      embedded apostrophes badly, but the simple form covers
//      the vast majority of ORM-emitted SQL — and pathological
//      cases just produce an extra normalisation cluster
//      rather than an incorrect result.
//   2. Replace integer / decimal numeric literals with "?".
//      Boundary anchors (\\b) prevent munging column names
//      that happen to end in digits ("col1" stays intact).
//
// IN-list collapse and parameter-binding placeholders ($1 / ?N)
// are left as-is — they're not literals, they're already
// normalised forms.
func (s *Store) GetTopDBQueries(
	ctx context.Context, service string, from, to time.Time, limit int,
) ([]DBQueryStat, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	// IMPORTANT: clickhouse-go counts every '?' in the SQL,
	// including ones inside string literals and regex patterns,
	// as a positional parameter. The normalisation regex would
	// naively be `\\b[0-9]+(\\.[0-9]+)?\\b` and the replacement
	// strings would naively be '?', but those literal '?'s blow
	// up the placeholder count. We work around it by:
	//   • Using `{0,1}` instead of `?` for the decimal quantifier
	//     in the regex pattern.
	//   • Using a sentinel `__P__` as the replacement, then
	//     swapping it for `?` Go-side after scan so the
	//     displayed query reads naturally.
	const placeholder = "__P__"
	sql := `
		SELECT
			replaceRegexpAll(
				replaceRegexpAll(db_statement, '''[^'']*''', '__P__'),
				'\\b[0-9]+(\\.[0-9]+){0,1}\\b', '__P__'
			)                                          AS norm_stmt,
			any(db_statement)                          AS sample_stmt,
			any(db_system)                             AS db_system,
			count()                                    AS cnt,
			avg(duration / 1e6)                        AS avg_ms,
			quantile(0.95)(duration / 1e6)             AS p95_ms,
			quantile(0.99)(duration / 1e6)             AS p99_ms,
			max(duration / 1e6)                        AS max_ms,
			countIf(status_code = 'error')             AS err_cnt
		FROM spans
		WHERE service_name = ?
		  AND time >= ? AND time <= ?
		  AND db_statement != ''
		GROUP BY norm_stmt
		ORDER BY (cnt * avg_ms) DESC
		LIMIT ?
		SETTINGS max_execution_time = 30,
		         ` + s.shardSkipSetting()
	rows, err := s.conn.Query(ctx, sql, service, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("query db queries: %w", err)
	}
	defer rows.Close()

	out := []DBQueryStat{}
	for rows.Next() {
		var r DBQueryStat
		var cnt uint64
		var errCnt uint64
		if err := rows.Scan(&r.Statement, &r.SampleStatement, &r.DBSystem,
			&cnt, &r.AvgMs, &r.P95Ms, &r.P99Ms, &r.MaxMs, &errCnt); err != nil {
			return nil, err
		}
		r.Count = int(cnt)
		r.ErrorCount = int(errCnt)
		r.TotalMs = float64(cnt) * r.AvgMs
		// Swap the sentinel back to "?" so the displayed
		// statement matches the canonical normalised form an
		// operator expects (`SELECT * WHERE id = ?`). The
		// sample statement is a real span, so it never carried
		// the sentinel.
		r.Statement = strings.ReplaceAll(r.Statement, placeholder, "?")
		out = append(out, r)
	}
	return out, rows.Err()
}
