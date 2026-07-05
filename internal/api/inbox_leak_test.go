package api

import "testing"

// v0.8.287 — resolved Problems leaked into the OPEN inbox. pickStatus returns
// "" for both "open" and "all" (a single-value filter can't express
// open+acknowledged, so it fetches every status and intends to narrow in Go) —
// but problemToInbox never dropped resolved rows (its "Skip resolved" comment
// had no implementing code). Contract of inboxKeepsProblem:
//   - inbox "open"  → keep open + acknowledged, DROP resolved (+ empty status
//     treated as active, so a pre-status row is never silently hidden);
//   - inbox "all"   → keep everything, including resolved;
//   - unknown inbox mode falls back to "open" semantics (defensive).
func TestInboxKeepsProblem(t *testing.T) {
	cases := []struct {
		name     string
		problem  string
		inbox    string
		wantKeep bool
	}{
		{"open inbox keeps open problem", "open", "open", true},
		{"open inbox keeps acknowledged problem", "acknowledged", "open", true},
		{"open inbox DROPS resolved problem", "resolved", "open", false},
		{"open inbox keeps empty-status problem", "", "open", true},
		{"all inbox keeps resolved problem", "resolved", "all", true},
		{"all inbox keeps open problem", "open", "all", true},
		{"all inbox keeps acknowledged problem", "acknowledged", "all", true},
		{"unknown inbox mode drops resolved (open-like)", "resolved", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := inboxKeepsProblem(c.problem, c.inbox); got != c.wantKeep {
				t.Fatalf("inboxKeepsProblem(%q, %q) = %v, want %v", c.problem, c.inbox, got, c.wantKeep)
			}
		})
	}
}
