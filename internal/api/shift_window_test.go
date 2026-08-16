package api

import (
	"testing"
	"time"
)

// v0.9.1071 (Faz 3.2) — /shift pencere rung'ları. Sunucu tek otorite:
// bilinmeyen/boş değer 12h varsayılana iner (serbest pencere cache
// kardinalitesini patlatırdı — v0.8.270 sınıfı); üç rung sabit.
func TestShiftWindow(t *testing.T) {
	cases := []struct {
		in       string
		wantRung string
		wantDur  time.Duration
	}{
		{"8h", "8h", 8 * time.Hour},
		{"12h", "12h", 12 * time.Hour},
		{"24h", "24h", 24 * time.Hour},
		{"", "12h", 12 * time.Hour},
		{"3d", "12h", 12 * time.Hour},
		{"1h", "12h", 12 * time.Hour},
	}
	for _, c := range cases {
		rung, dur := shiftWindow(c.in)
		if rung != c.wantRung || dur != c.wantDur {
			t.Fatalf("shiftWindow(%q) = (%q, %v), want (%q, %v)", c.in, rung, dur, c.wantRung, c.wantDur)
		}
	}
}
