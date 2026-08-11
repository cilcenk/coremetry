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
//
// v0.9.969 (UX denetimi Ö15) — the query grew a second time-predicate shape
// (absolute from/to alongside the now()-anchored duration), so every
// invariant below is now asserted for BOTH shapes. That is the point of the
// table: a scale guard that only holds on the path someone remembered to
// re-check is not a guard.
func TestAttributeKeysSQL(t *testing.T) {
	cases := []struct {
		name     string
		absolute bool
		wantWhen string
		// How many positional binds the time predicate contributes PER union
		// branch. The handler builds its arg slice from this shape; if the SQL
		// and the handler ever disagree the second branch reads the wrong
		// window and the answer is wrong but never errors.
		timeBinds int
	}{
		{name: "relative", absolute: false, wantWhen: "time >= now() - toIntervalSecond(?)", timeBinds: 1},
		{name: "absolute", absolute: true, wantWhen: "time >= ? AND time <= ?", timeBinds: 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			noFilter := attributeKeysSQL("", attrKeysSampleRows, tc.absolute)
			for _, want := range []string{
				"LIMIT 200000",            // inner sample bound
				"max_execution_time = 25", // wall-clock cap
				tc.wantWhen,               // time-bounded WHERE on the indexed col
				"arrayJoin(attr_keys)",
				"arrayJoin(res_keys)",
			} {
				if !strings.Contains(noFilter, want) {
					t.Errorf("attributeKeysSQL: expected SQL to contain %q\n--- SQL ---\n%s", want, noFilter)
				}
			}
			// BOTH union branches must sample — a regression that bounds only one
			// branch still lets the other full-scan.
			if n := strings.Count(noFilter, "LIMIT 200000"); n != 2 {
				t.Errorf("both union branches must sample: want 2 inner LIMITs, got %d", n)
			}
			// Both branches must carry the SAME time predicate. A mixed query
			// would scan two different windows and union the results — a wrong
			// answer with no error to notice.
			if n := strings.Count(noFilter, tc.wantWhen); n != 2 {
				t.Errorf("both branches need %q: got %d", tc.wantWhen, n)
			}
			// Bind-count contract with getAttributeKeys' arg slice.
			// +1 for the trailing LIMIT ?.
			wantBinds := tc.timeBinds*2 + 1
			if n := strings.Count(noFilter, "?"); n != wantBinds {
				t.Errorf("unfiltered SQL: want %d binds (2×%d time + LIMIT), got %d\n--- SQL ---\n%s",
					wantBinds, tc.timeBinds, n, noFilter)
			}

			// The filter fragment is AND-merged into both branches, with no
			// malformed double-AND.
			withFilter := attributeKeysSQL(" AND service_name = ?", attrKeysSampleRows, tc.absolute)
			if n := strings.Count(withFilter, "AND service_name = ?"); n != 2 {
				t.Errorf("filter fragment must appear in both branches, got %d", n)
			}
			if strings.Contains(withFilter, "AND  AND") {
				t.Errorf("malformed double-AND in filtered SQL:\n%s", withFilter)
			}
			// With a one-bind filter the layout is (time…, filter) per branch.
			if n := strings.Count(withFilter, "?"); n != wantBinds+2 {
				t.Errorf("filtered SQL: want %d binds, got %d", wantBinds+2, n)
			}
		})
	}

	// The two shapes must not be confusable: an absolute request that
	// accidentally rendered the relative predicate would scan "the last N
	// seconds" with from/to bound positionally into it.
	abs := attributeKeysSQL("", attrKeysSampleRows, true)
	if strings.Contains(abs, "now()") {
		t.Errorf("absolute SQL must not anchor on now():\n%s", abs)
	}
}
