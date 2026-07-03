package chstore

import (
	"strings"
	"testing"
)

// v0.8.234 — operator-reported: on an external Distributed test env in
// degraded ALLOW_UNSET_CLUSTER mode the summary MVs are empty by design,
// and ListServiceNames/ListOperationNames read ONLY the MV — so the
// /traces pickers listed nothing while raw spans held fresh data. The
// fix adds a bounded raw-spans fallback; this test pins the CH
// hard-constraint bounds (time-bounded WHERE on the indexed prefix,
// LIMIT, max_execution_time) onto EVERY branch of the generated SQL so
// a future edit can't silently ship an unbounded spans scan.
func TestRawPickerSQLBounds(t *testing.T) {
	cases := []struct {
		name       string
		col, extra string
	}{
		{"services no filter", "service_name", ""},
		{"services with pattern", "service_name", " AND service_name ILIKE ?"},
		{"ops no filter", "name", ""},
		{"ops service-scoped", "name", " AND service_name = ?"},
		{"ops service+pattern", "name", " AND service_name = ? AND name ILIKE ?"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			countQ, pageQ := rawPickerSQL(c.col, c.extra)
			for _, q := range []string{countQ, pageQ} {
				if !strings.Contains(q, "WHERE time >= ?") {
					t.Errorf("missing indexed time bound: %s", q)
				}
				if !strings.Contains(q, "max_execution_time") {
					t.Errorf("missing max_execution_time: %s", q)
				}
			}
			if !strings.Contains(pageQ, "LIMIT ? OFFSET ?") {
				t.Errorf("page query missing LIMIT/OFFSET: %s", pageQ)
			}
			if !strings.Contains(pageQ, "GROUP BY "+c.col) {
				t.Errorf("page query must GROUP BY the picked column: %s", pageQ)
			}
			// The count query must stay approximate (uniq, not
			// count(DISTINCT)) — exact would be a second full pass.
			if !strings.Contains(countQ, "uniq("+c.col+")") {
				t.Errorf("count query must use approximate uniq(): %s", countQ)
			}
			if c.extra != "" && !strings.Contains(pageQ, c.extra) {
				t.Errorf("extra where dropped: %s", pageQ)
			}
		})
	}
}
