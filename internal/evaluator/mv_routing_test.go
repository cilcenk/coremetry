package evaluator

// v0.6.12 regression test — pre-v0.6.12 the alert evaluator
// scanned raw `spans` for every basic RED metric on every
// service under every active rule on every minute tick. This
// violated CLAUDE.md Hard Constraint #3 (MV bypass) and at
// billion-span scale would choke CH.
//
// The fix routes basic RED metrics through service_summary_5m
// when the window is at least the MV's 5-minute granularity,
// and adds SETTINGS max_execution_time = 10 to every remaining
// raw-spans query (transport-scoped metrics still have to ride
// raw spans because the MV has no http_method / db_system
// breakdown).
//
// useSummaryMV is the boundary helper. Testing it directly here
// pins the boundary contract: a tweak that lowers the threshold
// below 5min would break the MV bucket alignment and a tweak
// that raises it would put more load on raw spans than
// necessary. Both regressions show up as a failed table row.

import (
	"testing"
	"time"
)

func TestUseSummaryMV_Boundary(t *testing.T) {
	cases := []struct {
		name   string
		window time.Duration
		want   bool
	}{
		// Below the MV's 5-min granularity → raw-spans fallback.
		{"1s",         1 * time.Second,        false},
		{"30s",        30 * time.Second,       false},
		{"1min",       1 * time.Minute,        false},
		{"2min",       2 * time.Minute,        false},
		{"4min",       4 * time.Minute,        false},
		{"4min59s",    4*time.Minute + 59*time.Second, false},
		// At-or-above 5min → MV path. The 5-min bucket alignment
		// gives a faithful aggregate for these windows.
		{"5min",       5 * time.Minute,        true},
		{"5min1s",     5*time.Minute + time.Second, true},
		{"10min",      10 * time.Minute,       true},
		{"15min",      15 * time.Minute,       true},
		{"1h",         time.Hour,              true},
		{"24h",        24 * time.Hour,         true},
		// Edge: zero / negative — treat as raw spans path so an
		// operator typo doesn't accidentally fall into "MV with
		// no data" silent success. (The query would return 0,
		// which a threshold rule would interpret as "all clear".)
		{"zero",       0,                      false},
		{"negative",   -time.Second,           false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := useSummaryMV(tc.window); got != tc.want {
				t.Errorf("useSummaryMV(%v) = %v, want %v", tc.window, got, tc.want)
			}
		})
	}
}
