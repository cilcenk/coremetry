package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

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

// ── v0.9.321 — incidents as the fourth source ────────────────────────────

func TestInboxKeepsIncident(t *testing.T) {
	cases := []struct {
		incident, inbox string
		want            bool
	}{
		{"open", "open", true},
		{"acknowledged", "open", true}, // still open, just picked up
		{"resolved", "open", false},
		{"resolved", "all", true},
		// Defensive: a row written before the status field existed must never
		// be silently hidden.
		{"", "open", true},
	}
	for _, tc := range cases {
		if got := inboxKeepsIncident(tc.incident, tc.inbox); got != tc.want {
			t.Errorf("inboxKeepsIncident(%q,%q) = %v, want %v",
				tc.incident, tc.inbox, got, tc.want)
		}
	}
}

func TestIncidentToInbox(t *testing.T) {
	cases := []struct {
		name, severity, status, wantPrio string
	}{
		{"critical unacked", "critical", "open", "P1"},
		{"warning unacked", "warning", "open", "P2"},
		{"info unacked", "info", "open", "P3"},
		// Acknowledged = somebody is on it. Weaker claim on attention than an
		// untouched one, but still open — one rung down, not out of the queue.
		{"critical acked", "critical", "acknowledged", "P2"},
		{"warning acked", "warning", "acknowledged", "P3"},
		{"info acked", "info", "acknowledged", "P3"},
		// An unknown severity must not vanish or become urgent.
		{"unknown severity", "", "open", "P3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			it := incidentToInbox(chstore.Incident{
				ID: "inc-1", Title: "checkout down", Severity: tc.severity,
				Status: tc.status, Service: "checkout", StartedAt: 100, UpdatedAt: 900,
			})
			if it.Priority != tc.wantPrio {
				t.Errorf("priority = %s, want %s (reason %q)", it.Priority, tc.wantPrio, it.PriorityReason)
			}
			if it.PriorityReason == "" {
				t.Error("every row ships a reason — see CLAUDE.md triage")
			}
			if it.Kind != "incident" || it.ID != "incident:inc-1" {
				t.Errorf("kind/id = %s/%s", it.Kind, it.ID)
			}
			if it.Incident == nil || it.Incident.ID != "inc-1" {
				t.Error("drill-down ref missing — the row would open nothing")
			}
			if it.LastSeen != 900 {
				t.Errorf("lastSeen = %d, want the updated_at (900)", it.LastSeen)
			}
		})
	}

	// A never-updated incident must not sort as epoch-old: lastSeen falls
	// back to when it started.
	it := incidentToInbox(chstore.Incident{ID: "i", StartedAt: 500, UpdatedAt: 0})
	if it.LastSeen != 500 {
		t.Errorf("lastSeen fallback = %d, want startedAt 500", it.LastSeen)
	}
}

// v0.9.321 — the invalidation prefix must NOT carry the response-shape
// version. v0.9.319 bumped the key to :v3 while every mutation site still
// dropped "inbox:v2", so acknowledging a problem left it in the queue for a
// full TTL and nothing failed loudly. The prefix must match both the list key
// and the count key.
func TestInboxListCachePrefixMatchesKeys(t *testing.T) {
	listKey := inboxListKey("open", "", "", "", "", "", 200, "priority", "desc", 5)
	if !strings.HasPrefix(listKey, inboxListCachePrefix) {
		t.Errorf("list key %q is not dropped by prefix %q", listKey, inboxListCachePrefix)
	}
	if !strings.HasPrefix(inboxCountKey("prod"), inboxListCachePrefix) {
		t.Errorf("count key %q is not dropped by prefix %q", inboxCountKey("prod"), inboxListCachePrefix)
	}
	// It must not be so broad that it drops unrelated namespaces.
	if strings.HasPrefix("exc-groups:foo", inboxListCachePrefix) {
		t.Error("prefix is too broad — it would drop unrelated cache entries")
	}
}

// ── v0.9.322 — badge/list status agreement ───────────────────────────────
//
// Found by running the deployed build against real data: the badge said 29
// open incidents while the list showed 2.
//
// Cause: the badge counted `status IN (...)` in SQL, while the list fetched
// EVERY status ordered by started_at DESC, applied the LIMIT, and only then
// dropped the resolved rows in Go. On an install whose history is 99%
// resolved (local: 994 of the newest 1000 incidents), the LIMIT was entirely
// consumed by resolved rows and the open ones never entered the window.
//
// The narrow now runs in SQL for both, from ONE shared status set. This test
// pins the invariant that made them disagree: the SQL narrow and the Go
// keeper must classify every status identically. If they ever drift, the
// badge and the list drift with them.
func TestInboxStatusNarrowMatchesKeepers(t *testing.T) {
	inSQLNarrow := func(status string) bool {
		for _, s := range inboxDoneStatuses {
			if s == status {
				return false // excluded by SQL
			}
		}
		return true
	}
	// Every status either source can hold, including the empty one written
	// before the field existed and a value neither side knows.
	for _, status := range []string{"open", "acknowledged", "resolved", "", "investigating"} {
		sql := inSQLNarrow(status)
		if got := inboxKeepsProblem(status, "open"); got != sql {
			t.Errorf("problem status %q: SQL narrow keeps=%v but Go keeper keeps=%v — badge and list will disagree",
				status, sql, got)
		}
		if got := inboxKeepsIncident(status, "open"); got != sql {
			t.Errorf("incident status %q: SQL narrow keeps=%v but Go keeper keeps=%v — badge and list will disagree",
				status, sql, got)
		}
	}
}

func TestPickExcludedStatuses(t *testing.T) {
	// "all" must apply NO narrow — the pivot exists to show resolved rows.
	if got := pickExcludedStatuses("all"); got != nil {
		t.Errorf(`pickExcludedStatuses("all") = %v, want nil (no narrow)`, got)
	}
	for _, pivot := range []string{"open", "ignored", ""} {
		if got := pickExcludedStatuses(pivot); len(got) != len(inboxDoneStatuses) {
			t.Errorf("pickExcludedStatuses(%q) = %v, want the shared done set", pivot, got)
		}
	}
	// The set must NOT contain the empty status. Excluding '' would hide rows
	// written before the status field existed — the exact defensive case the
	// Go keepers were written for.
	for _, s := range inboxDoneStatuses {
		if s == "" {
			t.Error("inboxDoneStatuses excludes status-less rows — they would vanish silently")
		}
	}
}

// v0.9.322 — the honesty probe has to compare against what the store will
// ACTUALLY return, not what the handler asked for.
//
// Found by an audit of the v0.9.312→321 diff: inboxSourceLimit returns 2000
// under any narrow, but ListExceptionGroups clamps Limit to 500 and
// ListIncidents collapses anything over 1000 to 200. `len(rows) >= 2000` is
// then false for the two sources most likely to be truncated — so the source
// that WAS capped is exactly the one that could never say so, and the page
// stayed silent while answering over a slice.
func TestInboxEffectiveLimit(t *testing.T) {
	cases := []struct {
		name           string
		want, storeMax int
		expect         int
	}{
		{"exceptions clamp the wide scan", inboxNarrowScan, inboxExcStoreMax, 500},
		{"incidents clamp the wide scan", inboxNarrowScan, inboxIncStoreMax, 1000},
		{"unclamped source gets what it asked for", inboxNarrowScan, inboxNoStoreMax, inboxNarrowScan},
		{"base scan is under every ceiling", inboxBaseScan, inboxExcStoreMax, inboxBaseScan},
		{"a page of 300 is under every ceiling", 300, inboxExcStoreMax, 300},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := inboxEffectiveLimit(tc.want, tc.storeMax); got != tc.expect {
				t.Errorf("inboxEffectiveLimit(%d, %d) = %d, want %d",
					tc.want, tc.storeMax, got, tc.expect)
			}
		})
	}

	// The property that makes the probe honest: the effective limit is
	// reachable. If it ever exceeded the store ceiling, `len(rows) >= limit`
	// could not fire even on a fully truncated read.
	for _, storeMax := range []int{inboxExcStoreMax, inboxIncStoreMax} {
		for _, want := range []int{50, 200, 300, 500, 2000, 5000} {
			if got := inboxEffectiveLimit(want, storeMax); got > storeMax {
				t.Errorf("effective limit %d exceeds store ceiling %d — the cap flag could never fire",
					got, storeMax)
			}
		}
	}
}
