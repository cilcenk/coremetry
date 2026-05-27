package anomaly

// v0.6.27 regression test — the truncTemplate helper has a
// silent contract: cut at the nearest whitespace past max
// bytes, but if no whitespace exists in the first half of the
// budget, hard-cut instead. The Drain template body can be one
// 5000-char SQL statement with no spaces, and a hard-cut path
// is the only way to keep the anomaly_event row size bounded.
// Re-regressing this would let an unbounded template ID land
// in the pattern column.

import (
	"strings"
	"testing"
)

func TestTruncTemplate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short — unchanged", "User logged in", 50, "User logged in"},
		{"exact len — unchanged", "Hello world!", 12, "Hello world!"},
		{
			"long with spaces — cut at word boundary",
			"User <*> logged in from <*> at <*> via <*> after retrying <*> times",
			40,
			// max=40 → look back from index 40 for last whitespace;
			// expect cut + ellipsis
			"User <*> logged in from <*> at <*> via…",
		},
		{
			"long no spaces — hard cut",
			strings.Repeat("a", 200),
			50,
			strings.Repeat("a", 50) + "…",
		},
		{
			"long with one early space — hard cut (boundary < max/2)",
			"a " + strings.Repeat("b", 200),
			50,
			"a " + strings.Repeat("b", 48) + "…",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncTemplate(tc.in, tc.max)
			if got != tc.want {
				t.Errorf("truncTemplate(%q, %d) =\n  got:  %q\n  want: %q", tc.in, tc.max, got, tc.want)
			}
			// The bounded-size contract — never exceed max+1 byte
			// (the ellipsis is a single multi-byte rune but we
			// budget the cut accordingly).
			if len(got) > tc.max+3 {
				t.Errorf("output longer than max+ellipsis: len=%d, max=%d", len(got), tc.max)
			}
		})
	}
}
