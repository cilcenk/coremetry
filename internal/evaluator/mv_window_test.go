package evaluator

import (
	"testing"
	"time"
)

// v0.8.315 — regression: the MV read path systematically under-sampled the
// rule window. service_summary_5m is keyed on time_bucket = the bucket
// START (toStartOfInterval 5m), but the evaluator filtered
// `time_bucket >= now-window` with an UNALIGNED cutoff: at now=10:07 a
// 5-minute rule got cutoff 10:02, which excludes the 10:00 bucket and
// reads only the still-filling 10:05 bucket — ~2 minutes of data sold as
// five. request_rate then divided that partial count by the FULL window
// (false traffic-drop alerts), and the MinSamples gate tripped on healthy
// services most ticks.
//
// Contract: mvWindowStart aligns the cutoff DOWN to the bucket grid (the
// bucket containing now-window is included — over-cover, never
// under-cover), mvCoveredSeconds reports the real span read, and
// scaleToWindow normalizes absolute counts back to the nominal window so
// count/rate thresholds keep their configured meaning.
func TestMvWindowStart(t *testing.T) {
	at := func(h, m, s int) time.Time {
		return time.Date(2026, 7, 6, h, m, s, 0, time.UTC)
	}
	cases := []struct {
		name   string
		now    time.Time
		window time.Duration
		want   time.Time
	}{
		{"mid-bucket 5m window includes the cutoff's bucket", at(10, 7, 0), 5 * time.Minute, at(10, 0, 0)},
		{"boundary-exact stays on the grid", at(10, 5, 0), 5 * time.Minute, at(10, 0, 0)},
		{"worst-case drift, still down-aligned", at(10, 9, 59), 5 * time.Minute, at(10, 0, 0)},
		{"10m window", at(10, 7, 0), 10 * time.Minute, at(9, 55, 0)},
		{"1h window", at(10, 2, 30), time.Hour, at(9, 0, 0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mvWindowStart(c.now, c.window); !got.Equal(c.want) {
				t.Fatalf("mvWindowStart(%s, %s) = %s, want %s", c.now, c.window, got, c.want)
			}
		})
	}
}

func TestMvCoveredSeconds(t *testing.T) {
	at := func(h, m, s int) time.Time {
		return time.Date(2026, 7, 6, h, m, s, 0, time.UTC)
	}
	cases := []struct {
		name   string
		now    time.Time
		window time.Duration
		want   float64
	}{
		{"mid-bucket: window + drift", at(10, 7, 0), 5 * time.Minute, 420},
		{"boundary-exact: exactly the window", at(10, 5, 0), 5 * time.Minute, 300},
		{"worst drift: window + 299s", at(10, 9, 59), 5 * time.Minute, 599},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mvCoveredSeconds(c.now, c.window)
			if got != c.want {
				t.Fatalf("mvCoveredSeconds(%s, %s) = %v, want %v", c.now, c.window, got, c.want)
			}
			if got < c.window.Seconds() {
				t.Fatalf("covered %vs may never under-cover the %vs window", got, c.window.Seconds())
			}
		})
	}
}

func TestScaleToWindow(t *testing.T) {
	// 840 spans observed over 420 real seconds, nominal window 300s →
	// normalized 600 (the count a true 5-minute read would have seen at
	// this rate). Guard: covered <= 0 falls back to the raw count.
	if got := scaleToWindow(840, 300, 420); got != 600 {
		t.Fatalf("scaleToWindow(840,300,420) = %v, want 600", got)
	}
	if got := scaleToWindow(840, 300, 300); got != 840 {
		t.Fatalf("no-drift scale must be identity, got %v", got)
	}
	if got := scaleToWindow(840, 300, 0); got != 840 {
		t.Fatalf("zero covered must fall back to raw, got %v", got)
	}
}
