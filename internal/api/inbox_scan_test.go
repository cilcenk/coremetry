package api

import "testing"

// inbox_scan_test.go — v0.9.318. Pins the per-source candidate ceiling.
//
// The bug: every narrowing filter on /api/inbox (service, q, env, ownerTeam,
// sreTeam) runs on the MERGED list, but each source was fetched with a
// hardcoded LIMIT 200. So the narrow answered over a slice that had already
// been truncated by the source's own ordering. With 900 open exception
// groups, searching "OOMKill" could only ever match within the first 200 —
// the operator saw an empty table and read it as an empty queue.
//
// Same shape as the drawer (v0.9.306), pivots (v0.9.307) and entry points
// (v0.9.313): a filter present in the caller, absent in the callee.
func TestInboxSourceLimit(t *testing.T) {
	cases := []struct {
		name     string
		limit    int
		narrowed bool
		want     int
	}{
		// Unnarrowed: the old 200 stays the floor, so the common poll costs
		// exactly what it did before this change.
		{"default page, no filter", 200, false, 200},
		{"small page, no filter", 50, false, 200},

		// …but a page bigger than the floor must be satisfiable from ONE
		// source. The frontend asks for 300; at 200/source a source holding
		// 300 genuinely open rows could not fill the page it was asked for.
		{"page above the floor lifts the scan", 300, false, 300},
		{"max page", 500, false, 500},

		// Narrowed: scan the candidate set. The narrow can only REMOVE rows,
		// so an honest answer needs the candidates, not a page of them.
		{"search widens the scan", 200, true, inboxNarrowScan},
		{"service filter widens the scan", 50, true, inboxNarrowScan},
		{"max page still narrowed", 500, true, inboxNarrowScan},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := inboxSourceLimit(tc.limit, tc.narrowed); got != tc.want {
				t.Errorf("inboxSourceLimit(%d, %v) = %d, want %d",
					tc.limit, tc.narrowed, got, tc.want)
			}
		})
	}
}

// A narrowed scan must never be SMALLER than an unnarrowed one at the same
// page size: narrowing is the case that needs more candidates, not fewer.
// Pinned as a property because the two branches are easy to invert by hand.
func TestInboxSourceLimitNarrowNeverShrinks(t *testing.T) {
	for _, limit := range []int{1, 50, 200, 300, 500} {
		wide := inboxSourceLimit(limit, true)
		narrow := inboxSourceLimit(limit, false)
		if wide < narrow {
			t.Errorf("limit=%d: narrowed scan %d < unnarrowed %d", limit, wide, narrow)
		}
		if wide < limit || narrow < limit {
			t.Errorf("limit=%d: scan (%d/%d) cannot fill the requested page",
				limit, narrow, wide)
		}
	}
}
