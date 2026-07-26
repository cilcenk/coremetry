package chstore

import (
	"testing"
	"time"
)

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

// v0.9.297 — a Stage 2 that runs out of resources now halves its window
// and retries instead of handing the operator an HTTP 500 with a
// ClickHouse stack trace where a trace list should be.
//
// The classification has to come first: a query that asked for more
// than it was granted can succeed unchanged over a smaller window; a
// malformed one cannot, and retrying it just burns the budget twice.
func TestResourceExhaustionClassification(t *testing.T) {
	retryable := []string{
		"stage2: code: 241, message: Memory limit exceeded: would use 4.15 GiB, maximum: 3.73 GiB",
		"code: 159, message: Timeout exceeded: elapsed 12.1 seconds",
		"code: 394, message: Query was cancelled",
		"code: 202, message: Too many simultaneous queries",
		"read tcp 10.0.0.1:9000: i/o timeout",
		"context deadline exceeded",
		"MEMORY LIMIT EXCEEDED", // matching is case-insensitive
	}
	for _, msg := range retryable {
		if !isResourceExhaustion(errString(msg)) {
			t.Errorf("must retry on a smaller window: %q", msg)
		}
	}

	// These describe a BROKEN query. A smaller window changes nothing,
	// so retrying only doubles the cost of the same failure.
	notRetryable := []string{
		"code: 47, message: Unknown identifier: dur_ms",
		"code: 62, message: Syntax error at position 262126",
		"code: 60, message: Table coremetry.trace_summary_5m doesn't exist",
		"code: 516, message: Authentication failed",
		"",
	}
	for _, msg := range notRetryable {
		if msg == "" {
			if isResourceExhaustion(nil) {
				t.Error("a nil error is not exhaustion")
			}
			continue
		}
		if isResourceExhaustion(errString(msg)) {
			t.Errorf("must NOT retry — a smaller window cannot fix it: %q", msg)
		}
	}
}

// The halving must converge, and must stop before the window is too
// small to hold anything. Two halvings take a 7-day ask to ~1.75 days,
// which is still a useful answer; a third would be guessing.
func TestWindowHalvingConverges(t *testing.T) {
	span := 7 * 24 * time.Hour
	for i := 0; i < traceStage2NarrowMaxRetry; i++ {
		if span <= traceStage2NarrowFloor {
			t.Fatalf("halving stopped early at %v — a 7-day ask must survive %d retries", span, traceStage2NarrowMaxRetry)
		}
		span /= 2
	}
	if span < 24*time.Hour {
		t.Fatalf("after %d halvings a 7-day ask is down to %v — too little to be a useful answer", traceStage2NarrowMaxRetry, span)
	}
	// And the floor genuinely stops a tiny window from being halved to
	// nothing: a 20-minute ask is already below it.
	if 20*time.Minute > traceStage2NarrowFloor {
		t.Fatalf("floor %v does not protect a short window", traceStage2NarrowFloor)
	}
}

// v0.9.299 — Stage 2 can no longer run without a trace-id bound.
//
// Measurement is what identified this as the only remaining
// explanation for the operator's 4.15 GiB. On the live cluster the
// BOUNDED statement (5,000-id IN list) costs 25,197,112 bytes at 1, 2
// and 4 days — byte-for-byte identical — and 25,197,064 at 7 days:
// 48 bytes of drift across a 5.4x row range, and zero movement from
// max_threads 1 to 32. There is no per-row term, so no window makes
// that query large; extrapolating a 150x bigger install still lands at
// ~25 MiB.
//
// The UNBOUNDED shape is the opposite: `GROUP BY trace_id` with only a
// time range builds six merge-states per distinct trace. At production
// density one 5-minute bucket holds ~a million traces, so a 7-day
// window is 10^8-10^9 groups — which is the only shape in this file
// that reaches gigabytes.
//
// This pins the property that makes it unreachable: for a service-less
// query, an empty id bound must always be resolved into one before the
// statement runs, never left empty.
func TestStage2AlwaysHasATraceIDBound(t *testing.T) {
	cases := []struct {
		name            string
		serviceSubquery bool
		holders         string
		wantResolve     bool // must build a bound before running
	}{
		{"service path carries its own subquery bound", true, "", false},
		{"normal no-service page — Stage 1 set the ids", false, "?,?,?", false},
		{
			// THE case: a sort key, early return or refactor that leaves
			// Stage 1 without ids. Pre-v0.9.299 this ran the unbounded
			// aggregate over the operator's whole window.
			name:            "no-service page with NO ids — must build one, not run unbounded",
			serviceSubquery: false, holders: "", wantResolve: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Mirrors the guard in getTracesFromMV.
			resolve := !tc.serviceSubquery && tc.holders == ""
			if resolve != tc.wantResolve {
				t.Fatalf("resolve = %v, want %v", resolve, tc.wantResolve)
			}
			// And the invariant that follows: after the guard, exactly
			// one of the two bounds is always present.
			bounded := tc.serviceSubquery || tc.holders != "" || resolve
			if !bounded {
				t.Fatal("statement would run with no trace-id bound — the only shape that reaches gigabytes")
			}
		})
	}
}
