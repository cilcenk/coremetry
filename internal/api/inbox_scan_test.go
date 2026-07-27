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

// ── v0.9.319 — server-side sort ──────────────────────────────────────────
//
// The page sorted the RETURNED rows client-side. Since the server ranks by
// priority and caps, "Last seen ascending" meant "the oldest of the
// priority-ranked top 300" — not "the oldest in the queue". Every column but
// priority answered a different question than its header claimed.

func TestNormalizeInboxSort(t *testing.T) {
	cases := []struct{ inID, inDir, wantID, wantDir string }{
		{"", "", "priority", "desc"},
		{"lastSeen", "asc", "lastSeen", "asc"},
		{"service", "desc", "service", "desc"},
		// A stale link from an older build must open a usable queue, not a
		// 400 — unknown falls back rather than erroring.
		{"occurrences", "asc", "priority", "asc"},
		{"'; DROP", "asc", "priority", "asc"},
		// Anything that isn't exactly "asc" is descending.
		{"lastSeen", "sideways", "lastSeen", "desc"},
	}
	for _, tc := range cases {
		gotID, gotDir := normalizeInboxSort(tc.inID, tc.inDir)
		if gotID != tc.wantID || gotDir != tc.wantDir {
			t.Errorf("normalizeInboxSort(%q,%q) = (%q,%q), want (%q,%q)",
				tc.inID, tc.inDir, gotID, gotDir, tc.wantID, tc.wantDir)
		}
	}
}

func inboxFixture() []InboxItem {
	return []InboxItem{
		{ID: "a", Priority: "P3", Service: "checkout", Title: "zeta", LastSeen: 300, Source: "Anomaly"},
		{ID: "b", Priority: "P1", Service: "Payments", Title: "alpha", LastSeen: 100, Source: "Exception"},
		{ID: "c", Priority: "P2", Service: "checkout", Title: "beta", LastSeen: 200, Source: "Alert rule"},
		{ID: "d", Priority: "P1", Service: "billing", Title: "gamma", LastSeen: 400, Source: "Exception"},
	}
}

func inboxIDs(items []InboxItem) string {
	out := ""
	for _, it := range items {
		out += it.ID
	}
	return out
}

func TestSortInboxItems(t *testing.T) {
	cases := []struct {
		name, id, dir, want string
	}{
		// The historical rank, unchanged: P1 first, newest within a priority.
		// An operator who never touches a header must see the old page.
		{"default", "priority", "desc", "dbca"},
		{"priority asc", "priority", "asc", "acdb"},
		{"lastSeen asc", "lastSeen", "asc", "bcad"},
		{"lastSeen desc", "lastSeen", "desc", "dacb"},
		// Case-insensitive: "Payments" must not sort before "billing" just
		// because of a capital P.
		{"service asc", "service", "asc", "dca", // billing, checkout×2, Payments
			},
		{"detail asc", "detail", "asc", "bcda"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := inboxFixture()
			sortInboxItems(items, tc.id, tc.dir)
			got := inboxIDs(items)
			if tc.name == "service asc" {
				// billing(d) < checkout(c,a) < Payments(b) — the point is that
				// Payments lands LAST despite the capital.
				if got != "dcab" {
					t.Errorf("service asc = %q, want %q", got, "dcab")
				}
				return
			}
			if got != tc.want {
				t.Errorf("sort(%s,%s) = %q, want %q", tc.id, tc.dir, got, tc.want)
			}
		})
	}
}

// The triage tiebreak must NEVER flip with the header. Inside one service, a
// P1 stays above a P3 in both directions — otherwise sorting by service
// descending buries the urgent row at the bottom of its own group.
func TestSortInboxItemsTiebreakStaysTriageOrder(t *testing.T) {
	for _, dir := range []string{"asc", "desc"} {
		items := []InboxItem{
			{ID: "low", Priority: "P3", Service: "checkout", LastSeen: 900},
			{ID: "high", Priority: "P1", Service: "checkout", LastSeen: 100},
		}
		sortInboxItems(items, "service", dir)
		if items[0].ID != "high" {
			t.Errorf("dir=%s: tiebreak inverted — got %s first, want the P1", dir, items[0].ID)
		}
	}
}

// ── v0.9.320 — occurrence floor ──────────────────────────────────────────
//
// The floor shipped on /problems in v0.9.315 but not on the Inbox, which is
// the surface meant to REPLACE it. Operator: "1 defa Java timeout aldığı için
// problems'ta exception gözüküyor" — the merged queue had the same one-offs.

func TestNormalizeInboxMinOcc(t *testing.T) {
	cases := []struct {
		raw  string
		want uint64
	}{
		{"", inboxDefaultMinOcc},   // absent → the floor
		{"0", 0},                   // explicit "show all" is honoured
		{"10", 10},                 // the strip's second rung
		{"  7 ", 7},                // whitespace from a pasted URL
		{"-3", inboxDefaultMinOcc}, // nonsense → default, never an error
		{"abc", inboxDefaultMinOcc},
	}
	for _, tc := range cases {
		if got := normalizeInboxMinOcc(tc.raw); got != tc.want {
			t.Errorf("normalizeInboxMinOcc(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

func TestApplyInboxMinOcc(t *testing.T) {
	items := []InboxItem{
		{ID: "exc-1", Kind: "exception", Exception: &InboxExceptionRef{Occurrences: 1}},
		{ID: "exc-9", Kind: "exception", Exception: &InboxExceptionRef{Occurrences: 9}},
		{ID: "exc-5", Kind: "exception", Exception: &InboxExceptionRef{Occurrences: 5}},
		// An alert-rule Problem that fired ONCE is still a firing problem.
		// Neither problems nor anomalies carry an occurrence count, and
		// dropping a P1 because it fired once is the opposite of triage.
		{ID: "prob", Kind: "problem", Priority: "P1"},
		{ID: "anom", Kind: "anomaly"},
		// Defensive: an exception row with no ref must not be dropped by a
		// nil dereference guard that silently means "below the floor".
		{ID: "exc-nil", Kind: "exception"},
	}
	kept, hidden := applyInboxMinOcc(append([]InboxItem(nil), items...), 5)
	if hidden != 1 {
		t.Errorf("hidden = %d, want 1 (only exc-1 is below 5)", hidden)
	}
	got := inboxIDs(kept)
	if got != "exc-9exc-5probanomexc-nil" {
		t.Errorf("kept = %q, want the 1-occurrence exception dropped and nothing else", got)
	}

	// Floor 0 = show all: no filtering, no hidden count, same slice.
	all, none := applyInboxMinOcc(append([]InboxItem(nil), items...), 0)
	if none != 0 || len(all) != len(items) {
		t.Errorf("minOcc=0 filtered: kept %d/%d, hidden %d", len(all), len(items), none)
	}
}
