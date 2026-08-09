package api

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// truncate_test.go — v0.9.842. Pins the rune-safe cut in truncate().
//
// Original symptom: the cut was a bare s[:n], which lands mid-rune on
// any multi-byte character. The fragment survives as invalid UTF-8
// until json.Marshal replaces it with U+FFFD, so every truncated
// Turkish log body ended in a silent "" — inside the exact evidence
// field the Explain prompts are told to quote codes and values from.
// Nothing errored; the text just quietly lost its last character and
// gained a replacement glyph. Same class as v0.9.414, one layer down.
//
// The budget stays a BYTE budget on purpose: prompt cost is measured
// in bytes, and turning n into runes would have doubled the real size
// of every Turkish body without a single caller asking for it.

func TestTruncateCutsOnRuneBoundary(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
	}{
		{"pure ascii, exact boundary", strings.Repeat("a", 100), 50},
		{"turkish body", strings.Repeat("ağırlıklı ölçüm ", 40), 100},
		{"multi-byte at the cut", "aaağ" + strings.Repeat("b", 50), 4},
		{"emoji (4-byte runes)", strings.Repeat("🔥", 40), 10},
		{"cut of 1 on a 2-byte rune", "ğ" + strings.Repeat("b", 20), 1},
		{"mixed script", "SQLTimeoutException: ORA-01013 işlem iptal edildi", 20},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.in, tc.n)
			if !utf8.ValidString(got) {
				t.Fatalf("truncate(%q, %d) = %q — not valid UTF-8", tc.in, tc.n, got)
			}
			if strings.ContainsRune(got, utf8.RuneError) && !strings.ContainsRune(tc.in, utf8.RuneError) {
				t.Errorf("truncate introduced U+FFFD: %q", got)
			}
			// Never over the byte budget (the ellipsis is the marker
			// that a cut happened and rides outside it, as before).
			body := strings.TrimSuffix(got, "…")
			if len(body) > tc.n {
				t.Errorf("body is %d bytes, budget was %d", len(body), tc.n)
			}
			// The kept prefix must be a real prefix of the input — a cut
			// that "fixes" UTF-8 by rewriting bytes would be worse than
			// the bug.
			if !strings.HasPrefix(tc.in, body) {
				t.Errorf("truncate(%q, %d) body %q is not a prefix", tc.in, tc.n, body)
			}
		})
	}
}

func TestTruncatePassesShortStringsThrough(t *testing.T) {
	for _, s := range []string{"", "kısa", strings.Repeat("ö", 10)} {
		if got := truncate(s, 900); got != s {
			t.Errorf("truncate(%q, 900) = %q, want it untouched", s, got)
		}
		if strings.HasSuffix(truncate(s, 900), "…") {
			t.Errorf("truncate marked %q as cut when it fits", s)
		}
	}
}

// A cut that lands exactly ON a rune start must not back off — the
// boundary case where an off-by-one would silently shrink every body.
func TestTruncateKeepsAnExactBoundary(t *testing.T) {
	in := "ğğğ" + strings.Repeat("b", 10) // each 'ğ' is 2 bytes
	got := strings.TrimSuffix(truncate(in, 6), "…")
	if got != "ğğğ" {
		t.Errorf("truncate(_, 6) = %q, want the three complete runes", got)
	}
}
