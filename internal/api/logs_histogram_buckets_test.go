package api

import (
	"testing"
	"time"
)

// v0.9.287 — /api/logs/timeseries clamped bucketSec to [1, 86400].
// That bounds the VALUE, not the bucket COUNT — and the count is what
// the backend aggregates, what the cache entry holds and what the wire
// carries. bucketSec=1 over a 30-day window is 2,592,000 buckets, times
// six severity bands.
//
// The floor is derived from the window so the count is bounded by the
// question's own size. Per the unit-mixing convention, every window
// magnitude is exercised — minutes, hours, days, months — not just the
// one that motivated the fix.
func TestFloorBucketByWindow(t *testing.T) {
	base := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	win := func(d time.Duration) (time.Time, time.Time) { return base, base.Add(d) }

	cases := []struct {
		name      string
		span      time.Duration
		bucketSec int
		wantMax   int // 0 = expect bucketSec returned untouched
	}{
		{"15min at 5s — the UI's finest ask, untouched", 15 * time.Minute, 5, 0},
		{"1h at 30s — untouched", time.Hour, 30, 0},
		{"6h at 60s — untouched", 6 * time.Hour, 60, 0},
		{"24h at 5m — untouched", 24 * time.Hour, 300, 0},
		{"30d at 15m — the UI's widest ask, untouched", 30 * 24 * time.Hour, 900, 0},

		// The abusive shapes: a fine bucket over a wide window.
		{"24h at 1s — floored", 24 * time.Hour, 1, logsHistogramMaxBuckets},
		{"7d at 1s — floored", 7 * 24 * time.Hour, 1, logsHistogramMaxBuckets},
		{"30d at 1s — the headline case", 30 * 24 * time.Hour, 1, logsHistogramMaxBuckets},
		{"90d at 15m — even the UI heuristic gets floored here", 90 * 24 * time.Hour, 900, logsHistogramMaxBuckets},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from, to := win(tc.span)
			got := floorBucketByWindow(tc.bucketSec, from, to)

			if got < tc.bucketSec {
				t.Fatalf("floor must never LOWER the bucket (that would raise the count): %d → %d", tc.bucketSec, got)
			}
			if tc.wantMax == 0 {
				if got != tc.bucketSec {
					t.Fatalf("bucket %ds over %v should pass through, got %d", tc.bucketSec, tc.span, got)
				}
				return
			}
			buckets := int(tc.span.Seconds()) / got
			if buckets > tc.wantMax {
				t.Fatalf("%v at %ds still yields %d buckets, want ≤ %d", tc.span, got, buckets, tc.wantMax)
			}
		})
	}
}

// Index resolution must never be the reason a query fails — same rule
// as the ES index narrowing. An unbounded or inverted window passes the
// caller's bucket through untouched rather than dividing by zero or
// inventing a floor.
func TestFloorBucketByWindowDegenerateWindows(t *testing.T) {
	base := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		from, to time.Time
	}{
		{"zero from", time.Time{}, base},
		{"zero to", base, time.Time{}},
		{"both zero", time.Time{}, time.Time{}},
		{"inverted window", base, base.Add(-time.Hour)},
		{"empty window", base, base},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := floorBucketByWindow(30, tc.from, tc.to); got != 30 {
				t.Fatalf("degenerate window must pass the bucket through, got %d", got)
			}
		})
	}
}
