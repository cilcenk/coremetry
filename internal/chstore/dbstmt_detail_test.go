package chstore

// v0.8.378 — Stage-2 slice D2: statement-detail readers over
// db_statement_summary_5m + the true-exemplar raw read. Table-driven
// SQL-shape pins, same discipline as dbqueries_mv_test.go (v0.8.375):
// MV-first, bounds on every query, no db_system/db_name alias shadow
// (the CH code-184 class, v0.8.362), correct state finalisers.

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// reFromSpans matches a raw-spans FROM — used both ways below: the MV
// readers must NOT contain it, the exemplar read MUST.
var reFromSpans = regexp.MustCompile(`(?i)\bFROM\s+spans\b`)

// TestDBStmtDetailMVSQLShapes pins the three MV builders with BOTH
// optional filters present (db_system + db_name — the alias-shadow
// trigger condition) plus the always-on identity/window predicate.
func TestDBStmtDetailMVSQLShapes(t *testing.T) {
	q := DBStmtDetailQuery{
		Hash:     0xDEADBEEF,
		DBSystem: "postgresql",
		DBName:   "orders",
		From:     time.Date(2026, 7, 8, 11, 0, 0, 0, time.UTC),
		To:       time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC),
	}
	wcAll := dbStmtDetailWhere(q)
	where := wcAll.sql()

	common := []string{
		// MV-first invariant + identity + bounds discipline.
		"FROM db_statement_summary_5m",
		"stmt_hash = ?",
		"time_bucket >= ?", "time_bucket <= ?",
		"db_system = ?", "db_name = ?",
		"max_execution_time = 10",
		// Correct finalisers for the MV's aggregate states — a countMerge
		// on a countIf state (or vice versa) reads the wrong aggregate
		// silently (the GetDBTrends lesson).
		"countMerge(span_count_state)",
		"countIfMerge(error_count_state)",
		"sumMerge(duration_sum_state)",
		"quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state)",
	}
	cases := []struct {
		name  string
		sql   string
		extra []string
	}{
		{"summary", dbStmtSummarySQL(where), []string{
			"anyMerge(sample_stmt_state)",
			"maxMerge(duration_max_state)",
			"LIMIT 1",
		}},
		{"trend", dbStmtTrendSQL(where), []string{
			"intDiv(toUnixTimestamp(time_bucket) - ?, ?)",
			"GROUP BY b", "ORDER BY b", "LIMIT 400",
		}},
		{"callers", dbStmtCallersSQL(where), []string{
			"GROUP BY service_name",
			"ORDER BY total_ms DESC",
			"LIMIT ?",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, want := range append(append([]string{}, common...), tc.extra...) {
				if !strings.Contains(tc.sql, want) {
					t.Errorf("%s SQL missing %q\n--- SQL ---\n%s", tc.name, want, tc.sql)
				}
			}
			// Alias-shadow guard: with the db_system/db_name WHERE filters
			// present, a SELECT alias named after either column makes CH
			// resolve the WHERE identifier to the alias → code 184.
			if regexp.MustCompile(`(?i)\bAS\s+db_system\b`).MatchString(tc.sql) {
				t.Errorf("%s SQL aliases the filtered db_system column (code-184 class)\n--- SQL ---\n%s", tc.name, tc.sql)
			}
			if regexp.MustCompile(`(?i)\bAS\s+db_name\b`).MatchString(tc.sql) {
				t.Errorf("%s SQL aliases the filtered db_name column (code-184 class)\n--- SQL ---\n%s", tc.name, tc.sql)
			}
			// No mid-query path mixing: aggregate readers never touch raw spans.
			if reFromSpans.MatchString(tc.sql) {
				t.Errorf("%s SQL reads raw spans (MV-bypass violation)\n--- SQL ---\n%s", tc.name, tc.sql)
			}
		})
	}
}

// TestDBStmtDetailWhereOptionalFilters pins that the optional dims stay
// OUT of the predicate when unset — a stray empty-string equality would
// silently match nothing (db_system is never ” in the MV: its source
// filter is db_stmt_hash != 0 ⊂ db_system != ”).
func TestDBStmtDetailWhereOptionalFilters(t *testing.T) {
	q := DBStmtDetailQuery{
		Hash: 42,
		From: time.Date(2026, 7, 8, 11, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC),
	}
	wc := dbStmtDetailWhere(q)
	sql := wc.sql()
	for _, notWant := range []string{"db_system", "db_name"} {
		if strings.Contains(sql, notWant) {
			t.Errorf("WHERE contains %q with the filter unset\n--- SQL ---\n%s", notWant, sql)
		}
	}
	if len(wc.args) != 3 { // hash + from + to
		t.Errorf("want 3 args (hash, from, to), got %d: %v", len(wc.args), wc.args)
	}
	// Window snap: the MV read must align to the 5-minute grid so a
	// rolling window covers whole buckets (the GetDBTrends trick).
	from2 := q
	from2.From = time.Date(2026, 7, 8, 11, 3, 27, 0, time.UTC)
	wc2 := dbStmtDetailWhere(from2)
	if got := wc2.args[1].(time.Time); !got.Equal(time.Date(2026, 7, 8, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("window start not snapped to 5m grid: %v", got)
	}
}

// TestDBStmtExemplarSQL pins the true-exemplar raw read (v0.8.378):
// bounded spans point-read keyed on the stored db_stmt_hash column —
// PK-friendly time bound, slowest-first LIMIT 1, 10s cap — and the
// error-variant predicate.
func TestDBStmtExemplarSQL(t *testing.T) {
	q := DBStmtDetailQuery{
		Hash:     7,
		DBSystem: "mysql",
		From:     time.Date(2026, 7, 8, 11, 0, 0, 0, time.UTC),
		To:       time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC),
	}
	for _, tc := range []struct {
		name      string
		errorOnly bool
	}{
		{"slowest", false},
		{"error", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wc := dbStmtExemplarWhere(q, tc.errorOnly)
			sql := dbStmtExemplarSQL(wc.sql(), "optimize_skip_unused_shards = 0")
			if !reFromSpans.MatchString(sql) {
				t.Fatalf("exemplar must read raw spans\n--- SQL ---\n%s", sql)
			}
			for _, want := range []string{
				"time >= ?", "time <= ?",
				"db_stmt_hash = ?",
				"db_system = ?",
				"ORDER BY duration DESC",
				"LIMIT 1",
				"max_execution_time = 10",
			} {
				if !strings.Contains(sql, want) {
					t.Errorf("exemplar SQL missing %q\n--- SQL ---\n%s", want, sql)
				}
			}
			hasErrPred := strings.Contains(sql, "status_code = 'error'")
			if hasErrPred != tc.errorOnly {
				t.Errorf("error predicate presence = %v, want %v\n--- SQL ---\n%s",
					hasErrPred, tc.errorOnly, sql)
			}
		})
	}
}

// TestDBStmtTrendBucketSec pins the trend coarsening: native 5m grain up
// to 24h, then the smallest 5m multiple keeping ≤ dbStmtTrendMaxPoints
// buckets. Degenerate windows fall back to the grain instead of a
// zero/negative divisor.
func TestDBStmtTrendBucketSec(t *testing.T) {
	base := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		window time.Duration
		want   int64
	}{
		{"zero window", 0, 300},
		{"inverted window", -time.Hour, 300},
		{"5m", 5 * time.Minute, 300},
		{"1h", time.Hour, 300},
		{"24h — exactly 288 native buckets", 24 * time.Hour, 300},
		{"25h — first coarsening step", 25 * time.Hour, 600},
		{"7d", 7 * 24 * time.Hour, 2100},
		{"30d", 30 * 24 * time.Hour, 9000},
		{"90d — MV TTL horizon", 90 * 24 * time.Hour, 27000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dbStmtTrendBucketSec(base.Add(-tc.window), base)
			if got != tc.want {
				t.Fatalf("dbStmtTrendBucketSec(%s) = %d, want %d", tc.window, got, tc.want)
			}
			if got%300 != 0 {
				t.Fatalf("bucket width %d not a 5m-grain multiple", got)
			}
			if w := int64(tc.window / time.Second); w > 0 && (w+got-1)/got > dbStmtTrendMaxPoints {
				t.Fatalf("window %s at width %d yields %d buckets (> %d)",
					tc.window, got, (w+got-1)/got, dbStmtTrendMaxPoints)
			}
		})
	}
}
