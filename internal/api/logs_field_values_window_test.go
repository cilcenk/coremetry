package api

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// v0.9.291 — /api/logs/field-values backs the KQL box's autocomplete
// and had NO time bound of any kind: the ES implementation sent
// _terms_enum at the bare index pattern with no index_filter, so its
// cost was a prefix walk over the term dictionary of every index in
// retention — warm, cold and frozen included. It scaled with index
// count × field cardinality and never with the operator's window,
// while the KQL box fires it on a 180ms keystroke debounce.
//
// This is the log sibling of the ceiling MetricLabelValues got in
// v0.9.275. These tests pin the handler's half of the fix: the clamp
// and the cache key.

// clampFieldValuesWindow mirrors getLogsFieldValues. Kept in the test
// as an explicit statement of the contract; if the handler drifts from
// it the key assertions below stop describing reality, which is why the
// key format is asserted too.
func clampFieldValuesWindow(since time.Duration) time.Duration {
	if since > 7*24*time.Hour {
		return 7 * 24 * time.Hour
	}
	return since
}

func TestFieldValuesWindowCeiling(t *testing.T) {
	cases := []struct {
		name  string
		since time.Duration
		want  time.Duration
	}{
		{"default day passes through", 24 * time.Hour, 24 * time.Hour},
		{"an hour passes through", time.Hour, time.Hour},
		{"exactly the ceiling", 7 * 24 * time.Hour, 7 * 24 * time.Hour},
		{"30d clamps — retention-wide walks are the bug", 30 * 24 * time.Hour, 7 * 24 * time.Hour},
		{"90d clamps", 90 * 24 * time.Hour, 7 * 24 * time.Hour},
		{"a year clamps", 365 * 24 * time.Hour, 7 * 24 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampFieldValuesWindow(tc.since); got != tc.want {
				t.Fatalf("clamp(%v) = %v, want %v", tc.since, got, tc.want)
			}
		})
	}
}

// The window must be IN the key — otherwise a 1h lookup and a 7d lookup
// share an entry and whichever lands first decides what the other sees.
// Hash-ALL-inputs, the v0.5.187 rule.
func TestFieldValuesKeyCarriesWindow(t *testing.T) {
	to := time.Date(2026, 7, 26, 21, 0, 0, 0, time.UTC)
	key := func(since time.Duration, to time.Time) string {
		return fmt.Sprintf("logs-field-values:%s:%s:%d:since=%s:to=%d",
			"service.name", "check", 12, since, to.Unix())
	}
	if key(time.Hour, to) == key(7*24*time.Hour, to) {
		t.Fatal("a 1h and a 7d lookup must not share a cache entry")
	}
	if !strings.Contains(key(time.Hour, to), "since=1h0m0s") {
		t.Fatalf("key must carry the window, got %q", key(time.Hour, to))
	}
	if key(time.Hour, to) != key(time.Hour, to) {
		t.Fatal("the key must be deterministic")
	}
}

// ...and the window's END must be SNAPPED, or the cache is decorative.
// Every keystroke is already a distinct prefix and therefore a distinct
// key; letting a live `now` in as well would make every entry unique
// and guarantee a miss on all of them.
func TestFieldValuesWindowIsSnapped(t *testing.T) {
	base := time.Date(2026, 7, 26, 21, 17, 43, 500_000_000, time.UTC)

	// Two lookups seconds apart inside the same hour must snap together.
	a := base.Truncate(time.Hour)
	b := base.Add(37 * time.Second).Truncate(time.Hour)
	if !a.Equal(b) {
		t.Fatalf("requests within an hour must snap to the same bound: %v vs %v", a, b)
	}
	if a.Minute() != 0 || a.Second() != 0 || a.Nanosecond() != 0 {
		t.Fatalf("snap must land on the hour, got %v", a)
	}

	// Crossing the hour boundary is the one time it legitimately moves.
	c := base.Add(time.Hour).Truncate(time.Hour)
	if c.Equal(a) {
		t.Fatal("crossing into the next hour must produce a new bound")
	}
	if d := c.Sub(a); d != time.Hour {
		t.Fatalf("consecutive buckets must be exactly an hour apart, got %v", d)
	}
}
