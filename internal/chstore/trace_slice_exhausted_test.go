package chstore

import "testing"

// v0.9.296 — operator-reported, /traces?range=7d&sort=duration returned
// HTTP 500 (CH code 241). The diagnosis found that Stage 2's time floor
// has never once engaged, because `exhausted` was measuring the wrong
// thing.
//
// traceRecencySlice's row loop BREAKS the moment it holds `want`
// distinct ids — long before the row budget is spent. A replay of the
// real query: 5,000 ids collected after 7,954 of 15,000 budgeted rows.
// The old test `scanned < budget` then read that ordinary, successful
// ending as "the server ran out of rows", and two things followed:
//
//   1. cut collapsed to f.From. Stage 2 exists because the id set
//      prunes nothing (EXPLAIN keeps 215/218 granules), so its time
//      floor is the ONLY bound it has — and it was being handed the
//      full requested window. Measured at 7 days: 1,611,504 rows in
//      1.0-3.5 s with the collapsed floor, 24,460 rows in 93 ms with
//      the real one.
//   2. RankedWithin was zeroed, which /traces renders as "this ordering
//      is global". A non-time sort ranks WITHIN the newest-N slice; the
//      honesty hint written to say so has never appeared.
//
// The distinction is the whole fix: filling the want is SUCCESS,
// running out of rows is exhaustion.

// sliceOutcome mirrors the decision in traceRecencySlice so the two
// endings are separable without a ClickHouse connection.
func sliceOutcome(scanned, budget, gotIDs, want int) (exhausted bool, cutToWindowStart bool) {
	gotEnough := gotIDs >= want
	exhausted = !gotEnough && scanned < budget
	return exhausted, exhausted
}

func TestSliceExhaustionDistinguishesSuccessFromEmptyWindow(t *testing.T) {
	cases := []struct {
		name              string
		scanned, budget   int
		gotIDs, want      int
		wantExhausted     bool
		wantFloorCollapse bool
	}{
		{
			// THE regression: the ordinary page. Broke out early with a
			// full id set, having consumed roughly half the budget.
			name:    "filled the want early — success, not exhaustion",
			scanned: 7954, budget: 15000, gotIDs: 5000, want: 5000,
			wantExhausted: false, wantFloorCollapse: false,
		},
		{
			// Genuine exhaustion: the server had nothing more to give
			// and the want was never filled. Here the slice really does
			// cover the window, so the ranking IS global and Stage 2
			// must not narrow below what the caller asked for.
			name:    "ran out of rows without filling the want",
			scanned: 812, budget: 15000, gotIDs: 300, want: 5000,
			wantExhausted: true, wantFloorCollapse: true,
		},
		{
			// Spent the whole budget without filling the want — that is
			// a heavily-duplicated window, not an empty one. There ARE
			// more rows; the caller widens the budget and retries.
			name:    "budget spent, want unfilled — retry, not exhaustion",
			scanned: 15000, budget: 15000, gotIDs: 4100, want: 5000,
			wantExhausted: false, wantFloorCollapse: false,
		},
		{
			name:    "empty window",
			scanned: 0, budget: 15000, gotIDs: 0, want: 5000,
			wantExhausted: true, wantFloorCollapse: true,
		},
		{
			// Exactly on both boundaries: the want is filled, so success
			// wins even though scanned == budget.
			name:    "filled the want on the last budgeted row",
			scanned: 15000, budget: 15000, gotIDs: 5000, want: 5000,
			wantExhausted: false, wantFloorCollapse: false,
		},
		{
			// Every row distinct — the want fills with no duplication at
			// all. Still success.
			name:    "no duplicates at all",
			scanned: 5000, budget: 15000, gotIDs: 5000, want: 5000,
			wantExhausted: false, wantFloorCollapse: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exhausted, collapse := sliceOutcome(tc.scanned, tc.budget, tc.gotIDs, tc.want)
			if exhausted != tc.wantExhausted {
				t.Fatalf("exhausted = %v, want %v (scanned=%d/%d, ids=%d/%d)",
					exhausted, tc.wantExhausted, tc.scanned, tc.budget, tc.gotIDs, tc.want)
			}
			if collapse != tc.wantFloorCollapse {
				t.Fatalf("floor collapse = %v, want %v — collapsing it makes Stage 2 re-read the whole window",
					collapse, tc.wantFloorCollapse)
			}
		})
	}
}

// The old expression, kept as an explicit statement of what went wrong:
// on the ordinary page it disagrees with the fixed one. If a refactor
// ever restores it, this fails.
func TestOldExhaustionExpressionMisreadsTheCommonCase(t *testing.T) {
	const scanned, budget, gotIDs, want = 7954, 15000, 5000, 5000

	old := scanned < budget // pre-v0.9.296
	fixed, _ := sliceOutcome(scanned, budget, gotIDs, want)

	if !old {
		t.Fatal("the replayed numbers no longer reproduce the old misread — update them from a fresh replay")
	}
	if fixed {
		t.Fatal("the fix must NOT call a filled want exhausted")
	}
	if old == fixed {
		t.Fatal("fixed expression is indistinguishable from the broken one")
	}
}
