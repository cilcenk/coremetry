package api

// Regression test for the PR #17 review (feedback page, v0.8.106).
// Original symptom: submitFeedback validated len(message) — BYTES —
// while the frontend's maxLength={2000} counts characters. Turkish
// text (2-byte runes) or emoji passed the form but got a 400 from
// the backend. Validation must count runes, not bytes.

import (
	"strings"
	"testing"
)

func TestValidateFeedbackMessage(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"empty", "", "", true},
		{"whitespace only", "  \n\t ", "", true},
		{"trims surrounding space", "  hello  ", "hello", false},
		{"2000 ascii ok", strings.Repeat("a", 2000), strings.Repeat("a", 2000), false},
		{"2001 ascii too long", strings.Repeat("a", 2001), "", true},
		// 1500 chars × 2 bytes = 3000 bytes — the regression case.
		{"1500 two-byte turkish ok", strings.Repeat("ğ", 1500), strings.Repeat("ğ", 1500), false},
		// 2000 chars × 4 bytes = 8000 bytes, still 2000 runes.
		{"2000 emoji ok", strings.Repeat("🚀", 2000), strings.Repeat("🚀", 2000), false},
		{"2001 emoji too long", strings.Repeat("🚀", 2001), "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateFeedbackMessage(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("got %d chars, want %d", len(got), len(tc.want))
			}
		})
	}
}
