package chstore

import (
	"regexp"
	"strings"
	"testing"
)

// v0.8.362 — operator-reported: picking a database type on the
// /slow-queries page returned "failed to load slow queries". Root
// cause: the SELECT aliased `any(db_system) AS db_system`, shadowing
// the raw column; with the optional `db_system = ?` WHERE filter
// present, ClickHouse resolved the WHERE identifier to the alias and
// rejected the query (code 184, "Aggregate function any(db_system)
// is found in WHERE"). Without the filter nothing referenced the
// name, which is why the unfiltered page worked and the bug survived
// until an operator used the type picker.
func TestSlowQueriesGlobalSQLNoAliasShadow(t *testing.T) {
	var wc whereClause
	wc.add("time >= ?", nil)
	wc.add("time <= ?", nil)
	wc.add("db_statement != ''")
	wc.add("db_system = ?", "postgresql")
	sql := slowQueriesGlobalSQL(wc.sql(), "")

	// No SELECT alias may shadow a column the WHERE filters on —
	// `AS db_system` anywhere in the text is the regression.
	if regexp.MustCompile(`(?i)\bAS\s+db_system\b`).MatchString(sql) {
		t.Fatalf("SELECT aliases the filtered db_system column (CH code 184 class)\n--- SQL ---\n%s", sql)
	}

	// The filter itself and the bounds survive.
	for _, want := range []string{"db_system = ?", "time >= ?", "LIMIT ?", "max_execution_time = 30"} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL missing %q\n--- SQL ---\n%s", want, sql)
		}
	}
}
