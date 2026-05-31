package chstore

import (
	"context"
	"time"
)

// DBTopSQL is one row of the engine-authoritative "heaviest
// statement" view for Postgres / MySQL — the receiver-side
// parity with Oracle's V$SQL TopSQL list (OracleSQL). The field
// shape deliberately mirrors OracleSQL so the frontend renders
// all three engines through the same TopSQLTable component:
//
//	SQL          — the statement text (normalised by the engine —
//	               pg_stat_statements collapses literals to $N,
//	               performance_schema's DIGEST_TEXT collapses to ?).
//	ElapsedSec   — total accumulated exec time over the window, in
//	               seconds (receiver values are normalised to s).
//	Executions   — call count over the window.
//	AvgElapsedMs — ElapsedSec*1000 / Executions, server-computed so
//	               the SRE reads "constant-but-cheap vs rare-but-heavy"
//	               at a glance without re-deriving the ratio.
//
// Unlike the span-derived "Top statements" list (which only sees
// what the application actually traced), this is what the DB
// itself measured across ALL clients — the same complementary
// relationship Oracle's TopSQL has to our db_statement top list.
type DBTopSQL = OracleSQL

// GetPostgresTopSQL returns the heaviest statements for one
// Postgres instance as measured by the database itself, sourced
// from pg_stat_statements-shaped metric_points the OpenTelemetry
// postgresql receiver (or a sqlquery receiver scraping
// pg_stat_statements) publishes.
//
// IMPORTANT — empty is the expected default. The stock
// postgresql receiver does NOT emit pg_stat_statements metrics;
// the operator must enable the pg_stat_statements extension +
// the receiver's statement scrape (or run a sqlquery receiver).
// When no such points exist in the window we return an empty
// slice and the panel renders its EMPTY state — exactly the
// path the bundled demo takes (demo only emits Oracle TopSQL).
//
// Metric-name + attribute coverage is intentionally broad
// because the receiver naming for statement-level stats is not
// yet stabilised across versions / community variants. We match
// every plausible shape and let the engine-authoritative ones
// that exist win; absent ones contribute nothing.
func (s *Store) GetPostgresTopSQL(
	ctx context.Context, instance string, from, to time.Time,
) ([]DBTopSQL, error) {
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	withInstance := instance != "" && instance != "unknown"

	// The statement text rides as one of several attribute keys
	// depending on the receiver: `query` / `statement` /
	// `db.statement` (sqlquery receiver mapping pg_stat_statements
	// `query`). The total exec-time metric is the gauge/counter we
	// rank on; the call count rides as a sibling attribute OR a
	// sibling metric — we read the attribute form here (single
	// round-trip) and fall back to elapsed-only ranking when calls
	// aren't present.
	//
	// argMax(value, time) gives the freshest per-statement reading
	// (pg_stat_statements totals are cumulative since stats reset;
	// the latest snapshot in the window is the read). Bounded:
	// LIMIT + max_execution_time + time WHERE on the indexed `time`
	// column. Read source is metric_points (NOT raw spans).
	q := `
		SELECT
		    coalesce(
		        nullIf(attr_values[indexOf(attr_keys, 'query')], ''),
		        nullIf(attr_values[indexOf(attr_keys, 'statement')], ''),
		        nullIf(attr_values[indexOf(attr_keys, 'db.statement')], ''),
		        nullIf(attr_values[indexOf(attr_keys, 'queryid')], ''),
		        ''
		    ) AS stmt,
		    argMax(value, time) AS total_ms,
		    argMax(
		        toUInt64OrZero(coalesce(
		            nullIf(attr_values[indexOf(attr_keys, 'calls')], ''),
		            nullIf(attr_values[indexOf(attr_keys, 'executions')], ''),
		            '0'
		        )),
		        time
		    ) AS calls
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND metric IN (
		      'postgresql.statements.total_exec_time',
		      'postgresql.statement.total_time',
		      'postgresql.query.total_exec_time',
		      'postgresql.pg_stat_statements.total_time',
		      'postgresql_stat_statements_total_time',
		      'pg_stat_statements.total_exec_time',
		      'db.postgresql.query.total_time'
		  )
		  AND (has(attr_keys, 'query') OR has(attr_keys, 'statement')
		       OR has(attr_keys, 'db.statement') OR has(attr_keys, 'queryid'))
		  ` + pgInstanceClause(withInstance) + `
		GROUP BY stmt
		HAVING stmt != ''
		ORDER BY total_ms DESC
		LIMIT 10
		SETTINGS max_execution_time = 8`
	args := []any{from, to}
	if withInstance {
		args = append(args, instance, instance)
	}
	return scanTopSQL(ctx, s, q, args)
}

// GetMySQLTopSQL returns the heaviest statements for one MySQL
// instance as measured by the database itself, sourced from
// performance_schema (events_statements_summary_by_digest)-shaped
// metric_points the OpenTelemetry mysql receiver (or a sqlquery
// receiver scraping performance_schema) publishes.
//
// Same empty-is-expected contract as Postgres: the stock mysql
// receiver does NOT emit per-digest statement metrics unless the
// operator enables performance_schema statement instrumentation
// and the corresponding scrape. Absent → empty slice → panel
// EMPTY state. The bundled demo emits only Oracle, so MySQL shows
// empty there — expected.
//
// performance_schema reports SUM_TIMER_WAIT in picoseconds; the
// receiver is expected to normalise to milliseconds before
// export (the mysql receiver normalises its other timer metrics
// the same way). We treat the value as milliseconds total and
// convert to seconds for the panel — matching the Postgres path
// so a single frontend renderer covers both.
func (s *Store) GetMySQLTopSQL(
	ctx context.Context, instance string, from, to time.Time,
) ([]DBTopSQL, error) {
	if from.IsZero() {
		from = time.Now().Add(-1 * time.Hour)
	}
	if to.IsZero() {
		to = time.Now()
	}
	withInstance := instance != "" && instance != "unknown"

	// Statement text rides as `digest_text` (performance_schema's
	// normalised form), `statement`, `query`, or `db.statement`.
	// Call count rides as `count_star` / `calls` / `executions`.
	q := `
		SELECT
		    coalesce(
		        nullIf(attr_values[indexOf(attr_keys, 'digest_text')], ''),
		        nullIf(attr_values[indexOf(attr_keys, 'statement')], ''),
		        nullIf(attr_values[indexOf(attr_keys, 'query')], ''),
		        nullIf(attr_values[indexOf(attr_keys, 'db.statement')], ''),
		        nullIf(attr_values[indexOf(attr_keys, 'digest')], ''),
		        ''
		    ) AS stmt,
		    argMax(value, time) AS total_ms,
		    argMax(
		        toUInt64OrZero(coalesce(
		            nullIf(attr_values[indexOf(attr_keys, 'count_star')], ''),
		            nullIf(attr_values[indexOf(attr_keys, 'calls')], ''),
		            nullIf(attr_values[indexOf(attr_keys, 'executions')], ''),
		            '0'
		        )),
		        time
		    ) AS calls
		FROM metric_points
		WHERE time >= ? AND time <= ?
		  AND metric IN (
		      'mysql.statement_event.wait.time',
		      'mysql.statement.total_time',
		      'mysql.statements.total_latency',
		      'mysql.performance_schema.statement.total_time',
		      'mysql_statements_total_latency',
		      'mysql.query.total_time',
		      'db.mysql.statement.sum_timer_wait'
		  )
		  AND (has(attr_keys, 'digest_text') OR has(attr_keys, 'statement')
		       OR has(attr_keys, 'query') OR has(attr_keys, 'db.statement')
		       OR has(attr_keys, 'digest'))
		  ` + mysqlInstanceClause(withInstance) + `
		GROUP BY stmt
		HAVING stmt != ''
		ORDER BY total_ms DESC
		LIMIT 10
		SETTINGS max_execution_time = 8`
	args := []any{from, to}
	if withInstance {
		args = append(args, instance, instance)
	}
	return scanTopSQL(ctx, s, q, args)
}

// scanTopSQL is the shared (stmt, total_ms, calls) → []DBTopSQL
// reader for the Postgres + MySQL Top-SQL queries. The ranking
// metric is exec time in MILLISECONDS (both pg_stat_statements
// and performance_schema report time, and the receivers are
// expected to normalise to ms); we convert to ElapsedSec for the
// shared OracleSQL-shaped struct and compute AvgElapsedMs from
// the call count. Rows with no call count keep AvgElapsedMs at 0
// (undefined avg) rather than dividing by zero.
func scanTopSQL(ctx context.Context, s *Store, q string, args []any) ([]DBTopSQL, error) {
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DBTopSQL{}
	for rows.Next() {
		var stmt string
		var totalMs float64
		var calls uint64
		if err := rows.Scan(&stmt, &totalMs, &calls); err != nil {
			continue
		}
		if totalMs < 0 {
			totalMs = 0 // counter reset → suppress
		}
		elapsedSec := totalMs / 1000
		avgMs := 0.0
		if calls > 0 {
			avgMs = totalMs / float64(calls)
		}
		out = append(out, DBTopSQL{
			SQL:          stmt,
			ElapsedSec:   elapsedSec,
			Executions:   calls,
			AvgElapsedMs: avgMs,
		})
	}
	return out, nil
}
