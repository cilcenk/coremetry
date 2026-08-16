package anomaly

import (
	"testing"
	"unicode/utf8"
)

// v0.9.1115 regresyon — renderEvidence'ın örnek-satır kesimi
// (eski s[:120]) Türkçe log örneğini çok baytlı karakterin ortasından
// bölüp prompt'a geçersiz UTF-8 sokuyordu (truncStmt kardeş vakası;
// shared_exception.go'daki rune-doğru kesimin ikizi).
func TestTruncSampleRunes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"kısa aynen", "rejim kayması", 120, "rejim kayması"},
		{"tam sınır aynen", "şşş", 3, "şşş"},
		{"rune'dan kırpar, bayttan değil", "ışığışığışığ", 4, "ışığ…"},
		{"ascii kırpma", "abcdef", 3, "abc…"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := truncSampleRunes(c.in, c.max)
			if got != c.want {
				t.Errorf("truncSampleRunes(%q,%d) = %q, beklenen %q", c.in, c.max, got, c.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("geçersiz UTF-8: %q", got)
			}
		})
	}
}
