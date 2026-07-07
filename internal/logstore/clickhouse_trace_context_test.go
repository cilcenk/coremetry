package logstore

// v0.8.348 — pivot Phase 1c: the CH trace-context coverage queries must
// stay bounded per the house rule (time-bounded WHERE + LIMIT +
// max_execution_time) — logs is a billion-row/day table and this runs from
// an admin page. The SQL lives in consts precisely so this shape test pins
// the guards against drive-by edits.

import (
	"strings"
	"testing"
)

func TestCHTraceCoverageSQL_Bounded(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{"overall", chTraceCoverageSQL},
		{"per-service top-50", chTraceCoverageTopSQL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, must := range []string{"time >= ?", "time <= ?", "LIMIT", "max_execution_time"} {
				if !strings.Contains(tc.sql, must) {
					t.Fatalf("%s SQL must contain %q (bounded-query house rule):\n%s", tc.name, must, tc.sql)
				}
			}
			if !strings.Contains(tc.sql, "countIf(trace_id != '')") {
				t.Fatalf("%s SQL must count trace-context via countIf(trace_id != ''):\n%s", tc.name, tc.sql)
			}
		})
	}
}

func TestCHTraceCoverageTopSQL_GroupingCaps(t *testing.T) {
	for _, must := range []string{
		"GROUP BY service_name",
		"LIMIT 50",
		"max_rows_to_group_by",
		"group_by_overflow_mode",
	} {
		if !strings.Contains(chTraceCoverageTopSQL, must) {
			t.Fatalf("per-service SQL must contain %q (FieldStats overflow-guard parity):\n%s",
				must, chTraceCoverageTopSQL)
		}
	}
}
