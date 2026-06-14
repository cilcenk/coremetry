package chstore

// v0.6.24 regression test for the stale-auto-resolve decision.
//
// Operator-reported: /problems "Resolved" tab stayed empty
// forever because nothing transitioned groups out of `new`
// without a manual click. The fix introduced
// AutoResolveStaleExceptionGroups, gated on a pure-function
// decision (`shouldAutoResolveStale`). This test pins the
// boundary so a future "tighten the eligible states" tweak
// can't silently revert to "everything stays new forever".

import (
	"testing"
	"time"
)

func TestShouldAutoResolveStale(t *testing.T) {
	now := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	dayAgo := now.Add(-24 * time.Hour).UnixNano()
	fifteenDaysAgo := now.Add(-15 * 24 * time.Hour).UnixNano()
	oneDayThreshold := 1 * 24 * time.Hour
	fourteenDayThreshold := 14 * 24 * time.Hour

	cases := []struct {
		name        string
		state       string
		lastSeenNs  int64
		staleAfter  time.Duration
		want        bool
	}{
		// Eligible-state × age combinations.
		{"new & old enough",          ExStateNew,          fifteenDaysAgo, fourteenDayThreshold, true},
		{"acknowledged & old enough", ExStateAcknowledged, fifteenDaysAgo, fourteenDayThreshold, true},
		{"regressed & old enough",    ExStateRegressed,    fifteenDaysAgo, fourteenDayThreshold, true},
		// State filter — already-resolved + ignored stay put.
		{"already resolved → skip", ExStateResolved, fifteenDaysAgo, fourteenDayThreshold, false},
		{"ignored → skip",          ExStateIgnored,  fifteenDaysAgo, fourteenDayThreshold, false},
		// Age filter — recent groups stay open.
		{"new but recent (1 day < 14 day)", ExStateNew, dayAgo, fourteenDayThreshold, false},
		{"new at exactly threshold (1 day ≥ 1 day)", ExStateNew, dayAgo, oneDayThreshold, true},
		// Pathological inputs.
		{"zero threshold → never sweep",      ExStateNew, fifteenDaysAgo, 0,                   false},
		{"negative threshold → never sweep",  ExStateNew, fifteenDaysAgo, -time.Hour,          false},
		{"empty state → never sweep",         "",         fifteenDaysAgo, fourteenDayThreshold, false},
		{"garbage state → never sweep",       "garbage",  fifteenDaysAgo, fourteenDayThreshold, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldAutoResolveStale(tc.state, tc.lastSeenNs, tc.staleAfter, now)
			if got != tc.want {
				t.Errorf("shouldAutoResolveStale(state=%q, age=%v, threshold=%v) = %v, want %v",
					tc.state, now.Sub(time.Unix(0, tc.lastSeenNs)), tc.staleAfter, got, tc.want)
			}
		})
	}
}

// v0.8.99 regression test — operator-reported: a manually-resolved exception
// group flipped back to "regressed" within the 60s refresh because the
// continuously-firing exception recurred immediately, so a manual resolve never
// stuck. shouldRegress gates regression with a grace window: a manual resolve
// holds through in-flight occurrences; only a fingerprint still firing PAST the
// grace genuinely regressed.
func TestShouldRegress(t *testing.T) {
	resolvedAt := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC).UnixNano()
	ra := &resolvedAt
	grace := 15 * time.Minute
	within := resolvedAt + (5 * time.Minute).Nanoseconds()  // in-grace recurrence
	past := resolvedAt + (20 * time.Minute).Nanoseconds()   // still firing past grace
	before := resolvedAt - (5 * time.Minute).Nanoseconds()  // last occurrence pre-resolve

	cases := []struct {
		name       string
		state      string
		resolvedAt *int64
		lastSeen   int64
		want       bool
	}{
		{"resolved, recurs within grace → hold", ExStateResolved, ra, within, false},
		{"resolved, still firing past grace → regress", ExStateResolved, ra, past, true},
		{"resolved, last occurrence before resolve → hold", ExStateResolved, ra, before, false},
		{"not resolved (new) → never regress", ExStateNew, ra, past, false},
		{"acknowledged → never regress", ExStateAcknowledged, ra, past, false},
		{"resolved but no resolved_at → never regress", ExStateResolved, nil, past, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRegress(tc.state, tc.resolvedAt, tc.lastSeen, grace); got != tc.want {
				t.Errorf("shouldRegress(state=%q, lastSeen) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}

// v0.8.x regression test — operator-reported: a Problem/exception took far too
// long to fall into resolved/regressed (staleHorizon was 14 DAYS, exResolveGrace
// 15m). Pin the windows fast so a future bump back to slow values is caught
// here, while preserving the v0.8.99 invariant (grace > the 60s refresh so a
// manual resolve isn't undone within one tick).
func TestExceptionTransitionWindowsStayFast(t *testing.T) {
	if DefaultExceptionStaleHorizon > 48*time.Hour {
		t.Errorf("DefaultExceptionStaleHorizon = %v; want <= 48h so a fixed exception clears promptly (was 14d)", DefaultExceptionStaleHorizon)
	}
	if exResolveGrace > 10*time.Minute {
		t.Errorf("exResolveGrace = %v; want <= 10m so a still-firing fingerprint regresses promptly (was 15m)", exResolveGrace)
	}
	if exResolveGrace <= time.Minute {
		t.Errorf("exResolveGrace = %v; must stay > the 60s refresh so a manual resolve sticks (v0.8.99)", exResolveGrace)
	}

	now := time.Now()
	// Idle past the horizon → auto-resolves; a recent occurrence does not.
	stale := now.Add(-DefaultExceptionStaleHorizon - time.Minute).UnixNano()
	if !shouldAutoResolveStale(ExStateNew, stale, DefaultExceptionStaleHorizon, now) {
		t.Error("a group idle past DefaultExceptionStaleHorizon should auto-resolve")
	}
	fresh := now.Add(-DefaultExceptionStaleHorizon + time.Hour).UnixNano()
	if shouldAutoResolveStale(ExStateNew, fresh, DefaultExceptionStaleHorizon, now) {
		t.Error("a group with a recent occurrence must NOT auto-resolve")
	}
	// Resolved group firing past the grace regresses; within the grace it sticks.
	resolvedAt := now.Add(-time.Hour).UnixNano()
	if !shouldRegress(ExStateResolved, &resolvedAt, resolvedAt+exResolveGrace.Nanoseconds()+int64(time.Minute), exResolveGrace) {
		t.Error("a resolved group firing past exResolveGrace should regress")
	}
	if shouldRegress(ExStateResolved, &resolvedAt, resolvedAt+exResolveGrace.Nanoseconds()/2, exResolveGrace) {
		t.Error("a resolved group firing within the grace must NOT regress (resolve sticks)")
	}
}
