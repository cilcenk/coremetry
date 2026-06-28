package chstore

import (
	"context"
	"fmt"
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
func (s *Store) GetSlowQueriesGlobal(
	ctx context.Context, from, to time.Time, dbSystem string, limit int,
) ([]SlowQueryRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	const placeholder = "__P__"
	var wc whereClause
	wc.add("time >= ?", from)
	wc.add("time <= ?", to)
	wc.add("db_statement != ''")
	if dbSystem != "" {
		wc.add("db_system = ?", dbSystem)
	}
	sql := `
		SELECT
			service_name,
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
		FROM spans ` + wc.sql() + `
		GROUP BY service_name, norm_stmt
		ORDER BY (cnt * avg_ms) DESC
		LIMIT ?
		SETTINGS max_execution_time = 30,
		         ` + s.shardSkipSetting()
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
