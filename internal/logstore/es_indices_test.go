package logstore

// Regression tests for v0.8.109 — operator rule: ES queries never run
// against the bare index pattern. narrowIndices resolves concrete daily
// indices for the queried window (one-day slack for ingest-vs-event-date
// skew); clampWindow guarantees a bounded window (zero → last 10 minutes).
// Per the unit-mixing convention, BOTH date-suffix styles (2026.06.10 and
// 2026-06-10) and the undated/rollover branch are exercised.

import (
	"context"
	"strconv"
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

// v0.9.283 — a data-stream backing index is named
// ".ds-<stream>-<YYYY.MM.DD>-<generation>": the day stamp sits in the
// MIDDLE, the name ENDS with the rollover generation. esDateSuffix is
// `$`-anchored, so on a data-stream cluster NO name parsed as dated,
// narrowIndices answered (nil,false) and every /logs read — search,
// histogram, field stats, pattern probe — fanned out to the bare
// pattern, i.e. to every shard of the whole retention.
//
// The narrowing cannot filter a backing index by its own stamp alone:
// the stamp is the ROLLOVER date, so index N holds documents from
// day(N) until the next rollover. With size-triggered rollover that
// span is arbitrarily long, and filtering by the stamp would silently
// drop the one index that actually holds the window. Coverage is
// derived from the generation ordering instead — index N covers
// [day(N), day(N+1)], the newest is open-ended — which needs no extra
// ES call.
func TestNarrowIndicesDataStream(t *testing.T) {
	cases := []struct {
		name   string
		names  []string
		from   time.Time
		to     time.Time
		want   []string
		wantOK bool
	}{
		{
			// The successor's creation day is SHARED: ILM polls every 10
			// minutes by default, so the "1 day old" rollover fires some
			// minutes into 07.26 and gen 2 still holds that morning's
			// first documents. Keeping gen 2 is correctness, not slack —
			// dropping it would lose the top of the day. Two of three,
			// and at 30-day retention two of thirty (see
			// TestNarrowIndicesDataStreamActuallyNarrows).
			name: "daily rollover — window keeps the day's index and its predecessor",
			names: []string{
				".ds-app-2026.07.24-000001",
				".ds-app-2026.07.25-000002",
				".ds-app-2026.07.26-000003",
			},
			from:   d("2026-07-26T09:00:00Z"),
			to:     d("2026-07-26T09:10:00Z"),
			want:   []string{".ds-app-2026.07.25-000002", ".ds-app-2026.07.26-000003"},
			wantOK: true,
		},
		{
			// THE correctness case. Size-triggered rollover: gen 1 was
			// created on 07.01 and stayed open until 07.20, so it holds
			// every document in between. A stamp-only filter drops it and
			// the operator sees an empty log list for a window that has
			// data.
			name: "size rollover — long-lived backing index covers the gap",
			names: []string{
				".ds-app-2026.07.01-000001",
				".ds-app-2026.07.20-000002",
			},
			from:   d("2026-07-10T09:00:00Z"),
			to:     d("2026-07-10T09:10:00Z"),
			want:   []string{".ds-app-2026.07.01-000001"},
			wantOK: true,
		},
		{
			name: "newest backing index is open-ended",
			names: []string{
				".ds-app-2026.07.01-000001",
				".ds-app-2026.07.20-000002",
			},
			from:   d("2026-07-26T09:00:00Z"),
			to:     d("2026-07-26T09:10:00Z"),
			want:   []string{".ds-app-2026.07.20-000002"},
			wantOK: true,
		},
		{
			name: "two rollovers on the same day — both kept",
			names: []string{
				".ds-app-2026.07.26-000001",
				".ds-app-2026.07.26-000002",
			},
			from:   d("2026-07-26T09:00:00Z"),
			to:     d("2026-07-26T09:10:00Z"),
			want:   []string{".ds-app-2026.07.26-000001", ".ds-app-2026.07.26-000002"},
			wantOK: true,
		},
		{
			// Generation ordering is per stream: app's gen-2 must not
			// close app-int's gen-1 coverage.
			name: "streams do not close each other's coverage",
			names: []string{
				".ds-app-2026.07.01-000001",
				".ds-app-2026.07.20-000002",
				".ds-app-int-2026.07.01-000001",
			},
			from:   d("2026-07-26T09:00:00Z"),
			to:     d("2026-07-26T09:10:00Z"),
			want:   []string{".ds-app-2026.07.20-000002", ".ds-app-int-2026.07.01-000001"},
			wantOK: true,
		},
		{
			// Generation, not lexical name order, decides succession —
			// _cat/indices returns rows in cluster order, not sorted.
			name: "generation ordering survives an unsorted listing",
			names: []string{
				".ds-app-2026.07.20-000002",
				".ds-app-2026.07.01-000001",
			},
			from:   d("2026-07-10T09:00:00Z"),
			to:     d("2026-07-10T09:10:00Z"),
			want:   []string{".ds-app-2026.07.01-000001"},
			wantOK: true,
		},
		{
			name: "hyphenated stream name keeps its full identity",
			names: []string{
				".ds-app-checkout-prod-2026.07.03-000001",
				".ds-app-checkout-prod-2026.07.26-000002",
			},
			from: d("2026-07-26T09:00:00Z"),
			to:   d("2026-07-26T09:10:00Z"),
			// gen 1 stayed open until the 07.26 rollover, so it holds
			// that morning too — shared-day rule again.
			want:   []string{".ds-app-checkout-prod-2026.07.03-000001", ".ds-app-checkout-prod-2026.07.26-000002"},
			wantOK: true,
		},
		{
			// Mixed inventory: data stream + classic daily + undated. Each
			// family keeps its own rule, undated stays unconditionally.
			name: "mixed inventory narrows each family by its own rule",
			names: []string{
				"app-meta",
				"app-2026.07.25",
				"app-2026.07.26",
				".ds-other-2026.07.25-000001",
				".ds-other-2026.07.26-000002",
			},
			from: d("2026-07-26T09:00:00Z"),
			to:   d("2026-07-26T09:10:00Z"),
			want: []string{
				"app-meta", "app-2026.07.26",
				".ds-other-2026.07.25-000001", ".ds-other-2026.07.26-000002",
			},
			wantOK: true,
		},
		{
			// Fail-open is preserved: a listing of only-undated names is
			// still the fallback signal, data streams or not.
			name:   "undated rollover names still signal fallback",
			names:  []string{"app-000001", "app-000002"},
			from:   d("2026-07-26T09:00:00Z"),
			to:     d("2026-07-26T09:10:00Z"),
			want:   nil,
			wantOK: false,
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

// The whole point of the slice, stated as an assertion: at retention
// scale a small window must resolve to a small, CONSTANT number of
// backing indices. Before v0.9.283 this returned (nil,false) and the
// caller queried the bare pattern — all 30. A future change that
// quietly widens the coverage rule fails here rather than in prod.
func TestNarrowIndicesDataStreamActuallyNarrows(t *testing.T) {
	base := d("2026-07-01T00:00:00Z")
	names := make([]string, 0, 30)
	for i := 0; i < 30; i++ {
		names = append(names, ".ds-app-"+base.AddDate(0, 0, i).Format("2006.01.02")+
			"-"+pad6(i+1))
	}
	// A ten-minute question on day 20, with the one-day slack queryIndices
	// applies.
	from := d("2026-07-20T09:00:00Z").Add(-24 * time.Hour)
	to := d("2026-07-20T09:10:00Z")
	got, ok := narrowIndices(names, from, to)
	if !ok {
		t.Fatal("30 data-stream backing indices must parse as dated")
	}
	if len(got) > 3 {
		t.Fatalf("10min window over 30 dailies kept %d indices (%v) — narrowing is not narrowing", len(got), got)
	}
	// And it must still contain the day actually asked about.
	want := ".ds-app-2026.07.20-000020"
	found := false
	for _, n := range got {
		if n == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("window's own day %q missing from %v", want, got)
	}
}

// A document written at 00:05 on a rollover day sits in the PREVIOUS
// backing index — ILM polls every 10 minutes, so the rollover lands
// minutes into the new day. Narrowing must never drop that index, or
// the top of every day silently disappears from search.
func TestNarrowIndicesDataStreamKeepsRolloverMorning(t *testing.T) {
	names := []string{
		".ds-app-2026.07.25-000001",
		".ds-app-2026.07.26-000002",
	}
	from := d("2026-07-26T00:00:00Z")
	to := d("2026-07-26T00:10:00Z")
	got, ok := narrowIndices(names, from, to)
	if !ok || len(got) != 2 {
		t.Fatalf("rollover morning must keep both sides of the boundary, got %v ok=%v", got, ok)
	}
}

func pad6(n int) string {
	s := strconv.Itoa(n)
	for len(s) < 6 {
		s = "0" + s
	}
	return s
}

// v0.9.283 — the listing was cached only on SUCCESS. A credential
// without cluster `monitor` (the v0.8.166 bank apikey) therefore fired
// one extra failing _cat/indices per /logs request, forever: pure added
// ES load at the exact moment ES is refusing us. A short negative
// window collapses that to one call per esIndexCacheNegTTL.
func TestESListingSuppressed(t *testing.T) {
	now := d("2026-07-26T09:00:00Z")
	cases := []struct {
		name     string
		failedAt time.Time
		want     bool
	}{
		{"never failed → always try", time.Time{}, false},
		{"failed just now → suppressed", now, true},
		{"failed 1s ago → suppressed", now.Add(-time.Second), true},
		{"failed just inside the window → suppressed", now.Add(-esIndexCacheNegTTL + time.Millisecond), true},
		{"failed exactly at the window → retry", now.Add(-esIndexCacheNegTTL), false},
		{"failed long ago → retry", now.Add(-5 * time.Minute), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := esListingSuppressed(tc.failedAt, now); got != tc.want {
				t.Fatalf("esListingSuppressed = %v, want %v", got, tc.want)
			}
		})
	}
}

// And the gate is actually wired: with the window open, cachedIndexNames
// must return without touching the client. The store carries a nil
// client, so any attempted round-trip panics — caught here and reported
// as the real failure it is.
func TestCachedIndexNamesSkipsCallWhileSuppressed(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("suppression gate did not hold — cachedIndexNames attempted a round-trip: %v", r)
		}
	}()
	s := &ESStore{cfg: ESConfig{Index: "app-*"}}
	s.idxCache.failedAt = time.Now()
	if got := s.cachedIndexNames(context.Background()); got != nil {
		t.Fatalf("suppressed listing must answer nil (caller falls back to the pattern), got %v", got)
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
