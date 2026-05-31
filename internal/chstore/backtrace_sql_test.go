package chstore

import (
	"strings"
	"testing"
)

// v0.7.41 — Operator-reported: service Backtrace returned HTTP 500 "code 184:
// Aggregate function sum(errors) AS errors is found inside another aggregate
// function". Root cause: `sum(calls) AS calls` / `sum(errors) AS errors`
// aliased the totals to the SAME names as the calls/errors columns, so the
// error_rate / avg_ms expressions that re-`sum()` those columns resolved the
// identifier to the alias → sum(sum(...)). This pins that the shadow can't
// return (CLAUDE.md #11).
func TestServiceCallersAggSQLNoAliasShadow(t *testing.T) {
	sql := serviceCallersAggSQL(50)

	// The totals must NOT be aliased to a column name that is re-aggregated in
	// error_rate / avg_ms (calls, errors). That shadow is what caused error 184.
	for _, bad := range []string{"AS calls\n", "AS calls,", "AS errors\n", "AS errors,"} {
		if strings.Contains(sql, bad) {
			t.Errorf("query reintroduces the alias/column shadow %q (CH error 184)\n--- SQL ---\n%s", bad, sql)
		}
	}
	// The renamed totals + the compound expressions that depend on the raw
	// columns must be present, and the read stays bounded.
	for _, want := range []string{
		"AS c_calls", "AS c_errors",
		"sum(errors) * 100.0 / nullIf(sum(calls), 0)", // error_rate references columns
		"ORDER BY c_calls DESC",
		"LIMIT 50",
		"max_execution_time = 10",
		"FINAL",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("query missing %q\n--- SQL ---\n%s", want, sql)
		}
	}
}
