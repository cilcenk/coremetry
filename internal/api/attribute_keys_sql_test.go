package api

import (
	"strings"
	"testing"
)

// v0.7.30 — Operator-reported: at billions of spans the Traces "Add column"
// picker showed "no more attribute keys to add" because getAttributeKeys
// arrayJoin'd attr_keys/res_keys across the WHOLE time window and blew past
// max_execution_time=30. The fix samples the inner scan. This test pins the
// scale-safety invariants so a future edit can't silently regress to an
// unbounded full-window scan (CLAUDE.md #11).
func TestAttributeKeysSQL(t *testing.T) {
	noFilter := attributeKeysSQL("", attrKeysSampleRows)
	for _, want := range []string{
		"LIMIT 200000",                          // inner sample bound
		"max_execution_time = 30",               // wall-clock cap
		"time >= now() - toIntervalSecond(?)",   // time-bounded WHERE on indexed col
		"arrayJoin(attr_keys)",
		"arrayJoin(res_keys)",
	} {
		if !strings.Contains(noFilter, want) {
			t.Errorf("attributeKeysSQL: expected SQL to contain %q\n--- SQL ---\n%s", want, noFilter)
		}
	}
	// BOTH union branches must sample — a regression that bounds only one branch
	// still lets the other full-scan.
	if n := strings.Count(noFilter, "LIMIT 200000"); n != 2 {
		t.Errorf("both union branches must sample: want 2 inner LIMITs, got %d", n)
	}

	// The filter fragment is AND-merged into both branches, with no malformed
	// double-AND.
	withFilter := attributeKeysSQL(" AND service_name = ?", attrKeysSampleRows)
	if n := strings.Count(withFilter, "AND service_name = ?"); n != 2 {
		t.Errorf("filter fragment must appear in both branches, got %d", n)
	}
	if strings.Contains(withFilter, "AND  AND") {
		t.Errorf("malformed double-AND in filtered SQL:\n%s", withFilter)
	}
}
