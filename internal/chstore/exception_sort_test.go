package chstore

import "testing"

// v0.8.318 — regression: the exception inbox is SERVER-paginated
// (LIMIT/OFFSET, fixed ORDER BY last_seen DESC) but the frontend
// client-sorted and client-searched the loaded 50-row page. Clicking
// "Occurrences" reordered only the most-recent 50 groups — a
// high-volume-but-older exception on page 2 never surfaced (operator
// mis-prioritizes), and search showed zero results for matches on other
// pages. Sort + search move server-side.
//
// Contract of exceptionGroupsOrderBy: whitelist mapping of the UI sort ids
// onto ORDER BY clauses — never string-interpolate caller input into SQL —
// with a deterministic `fingerprint ASC` tiebreak so equal-key rows can't
// duplicate/skip across OFFSET pages. Unknown ids/dirs fall back to the
// historical last_seen DESC.
func TestExceptionGroupsOrderBy(t *testing.T) {
	cases := []struct {
		name string
		sort string
		dir  string
		want string
	}{
		{"default", "", "", "ORDER BY last_seen DESC, fingerprint ASC"},
		{"lastSeen asc", "lastSeen", "asc", "ORDER BY last_seen ASC, fingerprint ASC"},
		{"occurrences desc", "occurrences", "desc", "ORDER BY occurrences DESC, fingerprint ASC"},
		{"type asc", "type", "asc", "ORDER BY ex_type ASC, fingerprint ASC"},
		{"service desc", "service", "desc", "ORDER BY service DESC, fingerprint ASC"},
		{"state asc sorts by severity rank, not lexically", "state", "asc",
			"ORDER BY multiIf(state='new',5, state='regressed',4, state='acknowledged',3, state='resolved',2, 1) ASC, fingerprint ASC"},
		{"firstSeen desc", "firstSeen", "desc", "ORDER BY first_seen DESC, fingerprint ASC"},
		{"assignee asc", "assignee", "asc", "ORDER BY assignee ASC, fingerprint ASC"},
		{"unknown column falls back", "; DROP TABLE spans", "asc", "ORDER BY last_seen DESC, fingerprint ASC"},
		{"unknown dir falls back to desc", "occurrences", "sideways", "ORDER BY occurrences DESC, fingerprint ASC"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := exceptionGroupsOrderBy(c.sort, c.dir); got != c.want {
				t.Fatalf("exceptionGroupsOrderBy(%q,%q) = %q, want %q", c.sort, c.dir, got, c.want)
			}
		})
	}
}
