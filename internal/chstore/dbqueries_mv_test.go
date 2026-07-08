package chstore

// v0.8.375 — Stage-2 slice D1: /slow-queries catalog moves to the
// db_statement_summary_5m MV. These pin the two pure pieces of the
// dispatch: the MV SQL builder (alias-shadow + bounds discipline) and the
// routing condition (probe-driven MV/raw split).

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestSlowQueriesGlobalMVSQL — the MV twin of
// TestSlowQueriesGlobalSQLNoAliasShadow (v0.8.362): with the optional
// `db_system = ?` filter present, a SELECT alias named db_system would
// shadow the MV's real dimension column and ClickHouse rejects the query
// with code 184 ("Aggregate function any(db_system) is found in WHERE").
// The MV path must not reintroduce the exact bug the raw path already
// fixed.
func TestSlowQueriesGlobalMVSQL(t *testing.T) {
	var wc whereClause
	wc.add("time_bucket >= ?", nil)
	wc.add("time_bucket <= ?", nil)
	wc.add("db_system = ?", "postgresql")
	sql := slowQueriesGlobalMVSQL(wc.sql())

	if regexp.MustCompile(`(?i)\bAS\s+db_system\b`).MatchString(sql) {
		t.Fatalf("SELECT aliases the filtered db_system column (CH code 184 class)\n--- SQL ---\n%s", sql)
	}

	for _, want := range []string{
		// Reads the MV, not raw spans (the MV-bypass invariant).
		"FROM db_statement_summary_5m",
		// Bounds discipline: time-bounded WHERE + LIMIT + execution cap.
		"time_bucket >= ?", "time_bucket <= ?", "LIMIT ?", "max_execution_time = 30",
		// The optional system filter itself.
		"db_system = ?",
		// Correct finalisers for the MV's aggregate states — countMerge on
		// a countIf state (or vice versa) reads the wrong aggregate
		// silently, the GetDBTrends lesson.
		"countMerge(span_count_state)",
		"countIfMerge(error_count_state)",
		"sumMerge(duration_sum_state)",
		"quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state)",
		"maxMerge(duration_max_state)",
		"anyMerge(sample_stmt_state)",
		// Identity + grouping.
		"stmt_hash",
		"GROUP BY service_name, stmt_hash",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("MV SQL missing %q\n--- SQL ---\n%s", want, sql)
		}
	}

	// The raw-spans table must not appear — no mid-query path mixing.
	if regexp.MustCompile(`(?i)\bFROM\s+spans\b`).MatchString(sql) {
		t.Fatalf("MV SQL reads raw spans\n--- SQL ---\n%s", sql)
	}
}

// TestSlowQueriesUseMV pins the dispatcher condition: probe-driven column
// presence AND the 5-minute MV grain boundary (chstore.UseSummaryMV — the
// v0.6.12 evaluator rule). Sub-5m windows and column-less installs
// (external Distributed cluster, cluster_name unset) stay on the raw path.
func TestSlowQueriesUseMV(t *testing.T) {
	base := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		hasCol bool
		window time.Duration
		want   bool
	}{
		{"column absent, wide window", false, 24 * time.Hour, false},
		{"column absent, 5m window", false, 5 * time.Minute, false},
		{"column present, exactly 5m", true, 5 * time.Minute, true},
		{"column present, 1h", true, time.Hour, true},
		{"column present, 24h", true, 24 * time.Hour, true},
		{"column present, sub-grain 4m59s", true, 5*time.Minute - time.Second, false},
		{"column present, 1m", true, time.Minute, false},
		{"column present, zero window", true, 0, false},
		{"column present, inverted window", true, -time.Hour, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := slowQueriesUseMV(tc.hasCol, base.Add(-tc.window), base); got != tc.want {
				t.Fatalf("slowQueriesUseMV(hasCol=%v, window=%s) = %v, want %v",
					tc.hasCol, tc.window, got, tc.want)
			}
		})
	}
}
