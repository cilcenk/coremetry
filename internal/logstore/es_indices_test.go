package logstore

// Regression tests for v0.8.109 — operator rule: ES queries never run
// against the bare index pattern. narrowIndices resolves concrete daily
// indices for the queried window (one-day slack for ingest-vs-event-date
// skew); clampWindow guarantees a bounded window (zero → last 10 minutes).
// Per the unit-mixing convention, BOTH date-suffix styles (2026.06.10 and
// 2026-06-10) and the undated/rollover branch are exercised.

import (
	"testing"
	"time"
)

func d(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNarrowIndices(t *testing.T) {
	cases := []struct {
		name   string
		names  []string
		from   time.Time
		to     time.Time
		want   []string
		wantOK bool
	}{
		{
			name:   "dot suffix, 10min window hits one daily",
			names:  []string{"app-2026.06.08", "app-2026.06.09", "app-2026.06.10"},
			from:   d("2026-06-10T09:00:00Z"),
			to:     d("2026-06-10T09:10:00Z"),
			want:   []string{"app-2026.06.10"},
			wantOK: true,
		},
		{
			name:   "dash suffix works the same",
			names:  []string{"app-2026-06-09", "app-2026-06-10"},
			from:   d("2026-06-10T09:00:00Z"),
			to:     d("2026-06-10T09:10:00Z"),
			want:   []string{"app-2026-06-10"},
			wantOK: true,
		},
		{
			name:   "cross-midnight window spans two dailies",
			names:  []string{"app-2026.06.09", "app-2026.06.10", "app-2026.06.11"},
			from:   d("2026-06-09T23:55:00Z"),
			to:     d("2026-06-10T00:05:00Z"),
			want:   []string{"app-2026.06.09", "app-2026.06.10"},
			wantOK: true,
		},
		{
			name:   "undated names always kept alongside dated",
			names:  []string{"app-meta", "app-2026.06.09", "app-2026.06.10"},
			from:   d("2026-06-10T09:00:00Z"),
			to:     d("2026-06-10T09:10:00Z"),
			want:   []string{"app-meta", "app-2026.06.10"},
			wantOK: true,
		},
		{
			name:   "no dated names at all → fallback signal",
			names:  []string{"app-000001", "app-000002"},
			from:   d("2026-06-10T09:00:00Z"),
			to:     d("2026-06-10T09:10:00Z"),
			want:   nil,
			wantOK: false,
		},
		{
			name:   "window with no matching daily → empty but ok (caller falls back)",
			names:  []string{"app-2026.06.01"},
			from:   d("2026-06-10T09:00:00Z"),
			to:     d("2026-06-10T09:10:00Z"),
			want:   []string{},
			wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := narrowIndices(tc.names, tc.from, tc.to)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestQueryIndicesSlack(t *testing.T) {
	// The resolver applies one day of slack BEFORE from — an event
	// timestamped 00:05 can sit in yesterday's index when the shipper
	// rotates on ingest date. Exercised via narrowIndices with the
	// slack the resolver applies.
	names := []string{"app-2026.06.09", "app-2026.06.10"}
	from := d("2026-06-10T00:02:00Z").Add(-24 * time.Hour)
	to := d("2026-06-10T00:12:00Z")
	got, ok := narrowIndices(names, from, to)
	if !ok || len(got) != 2 {
		t.Fatalf("slack window should keep both dailies, got %v ok=%v", got, ok)
	}
}

func TestClampWindow(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name     string
		from, to time.Time
		wantSpan time.Duration // 0 = expect untouched
	}{
		{"both zero → 10min ending now", time.Time{}, time.Time{}, 10 * time.Minute},
		{"zero from only → 10min before to", time.Time{}, now.Add(-time.Hour), 10 * time.Minute},
		{"both set → untouched", now.Add(-2 * time.Hour), now.Add(-time.Hour), time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from, to := clampWindow(tc.from, tc.to)
			if from.IsZero() || to.IsZero() {
				t.Fatalf("clamped window still has zero bound: %v %v", from, to)
			}
			if got := to.Sub(from); got != tc.wantSpan {
				t.Fatalf("span = %v, want %v", got, tc.wantSpan)
			}
			if !tc.to.IsZero() && !to.Equal(tc.to) {
				t.Fatalf("non-zero to was modified: %v → %v", tc.to, to)
			}
		})
	}
}
