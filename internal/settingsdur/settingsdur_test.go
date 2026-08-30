package settingsdur

import (
	"testing"
	"time"
)

// v0.6.36 kuralı: şablonun kabul ettiği HER birim test edilir.
func TestParseEveryUnit(t *testing.T) {
	def := 7 * time.Second
	cases := []struct {
		name, in string
		want     time.Duration
	}{
		{"ns", "900ns", 900 * time.Nanosecond}, {"us", "1500us", 1500 * time.Microsecond}, {"µs", "1µs", time.Microsecond},
		{"ms", "500ms", 500 * time.Millisecond}, {"s", "90s", 90 * time.Second}, {"m", "5m", 5 * time.Minute},
		{"h", "6h", 6 * time.Hour}, {"h ondalık", "1.5h", 90 * time.Minute}, {".5h", ".5h", 30 * time.Minute},
		{"bileşik", "1h30m", 90 * time.Minute}, {"bileşik 3", "2h45m30s", 2*time.Hour + 45*time.Minute + 30*time.Second},
		{"d", "2d", 48 * time.Hour}, {"d ondalık", "0.5d", 12 * time.Hour}, {"d boşluklu", " 2d ", 48 * time.Hour},
		{"boşluk", " 30s ", 30 * time.Second}, {"artı", "+5m", 5 * time.Minute},
		{"boş → def", "", def}, {"çöp → def", "abc", def}, {"negatif → def", "-5m", def}, {"sıfır → def", "0s", def},
		{"0d → def", "0d", def}, {"xd → def", "xd", def}, {"büyük M → def", "5M", def},
		{"taşan gün → def", "1000000000000d", def}, {"1e300d → def", "1e300d", def},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Parse(tc.in, def); got != tc.want {
				t.Fatalf("Parse(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestClamp(t *testing.T) {
	lo, hi := time.Minute, time.Hour
	for in, want := range map[time.Duration]time.Duration{time.Second: lo, time.Minute: lo, 5 * time.Minute: 5 * time.Minute, 2 * time.Hour: hi, hi: hi} {
		if got := Clamp(in, lo, hi); got != want {
			t.Errorf("Clamp(%v) = %v, want %v", in, got, want)
		}
	}
}
