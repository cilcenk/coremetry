package templater

import "testing"

// v0.5.465 — locks the all-digit threshold for LooksLikeOpaqueID
// at ≥4. Operator-reported: Live Patterns panel was grouping
// short numeric values like "1234" as significant tokens, which
// are almost always sequence IDs / request counters and noise
// to a human reader. Dropping the cutoff from ≥10 to ≥4 catches
// those without harming HTTP status codes (3 digits) which
// remain useful.
//
// Re-introducing the bug (raising the threshold back to ≥10 or
// catching 3-digit numbers like "200") fails one of the cases
// below.

func TestLooksLikeOpaqueID_AllDigits(t *testing.T) {
	for _, tc := range []struct {
		tok     string
		want    bool
		comment string
	}{
		// Operator's complaint — 4-digit numbers leak through
		// without the fix.
		{"1234", true, "v0.5.465 regression — 4-digit sequence ID"},
		{"5678", true, "4-digit sequence ID"},
		{"99999", true, "5-digit sequence ID"},
		{"1234567890", true, "10-digit (epoch-shaped)"},

		// 3-digit numbers — HTTP status codes etc. STAY meaningful.
		{"200", false, "HTTP OK — meaningful pattern"},
		{"404", false, "HTTP not found"},
		{"500", false, "HTTP server error"},
		{"42", false, "2-digit too short"},

		// Mixed alphanumeric — only caught by digit-ratio rule
		// (≥60% digits AND length ≥ 10), not the all-digit one.
		{"v2", false, "version tag"},
		{"abc", false, "plain word"},
		{"http2", false, "protocol name"},
	} {
		got := LooksLikeOpaqueID(tc.tok)
		if got != tc.want {
			t.Errorf("LooksLikeOpaqueID(%q) = %v; want %v — %s", tc.tok, got, tc.want, tc.comment)
		}
	}
}
