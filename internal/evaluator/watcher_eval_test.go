// Watcher evaluation pure helpers (v0.9.x — ES Watcher import Faz-1):
// due-check pacing (a 5m-interval watch on the 1m evaluator tick runs
// when due, not every tick) and the track_total_hits cap handed to
// logstore.RawSearch (threshold*2 so a compare above the ES 10k count
// saturation still resolves correctly).
package evaluator

import (
	"testing"
	"time"
)

func TestWatcherDue(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		last     time.Time
		interval time.Duration
		want     bool
	}{
		{"never ran → due", time.Time{}, 5 * time.Minute, true},
		{"zero interval → every tick", now.Add(-time.Second), 0, true},
		{"elapsed exactly interval", now.Add(-5 * time.Minute), 5 * time.Minute, true},
		{"elapsed past interval", now.Add(-7 * time.Minute), 5 * time.Minute, true},
		{"mid-interval → not due", now.Add(-2 * time.Minute), 5 * time.Minute, false},
		{"one tick early → not due", now.Add(-4 * time.Minute), 5 * time.Minute, false},
		// Tick jitter: the ticker fires a few ms shy of the exact
		// multiple; without tolerance a 5m watch slips to 6m forever.
		{"jitter shy of interval → due", now.Add(-5*time.Minute + 3*time.Second), 5 * time.Minute, true},
		{"1m interval every tick", now.Add(-time.Minute + time.Second), time.Minute, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := watcherDue(tt.last, tt.interval, now); got != tt.want {
				t.Fatalf("watcherDue(last=%v, interval=%v) = %v, want %v",
					tt.last, tt.interval, got, tt.want)
			}
		})
	}
}

func TestWatcherTotalCap(t *testing.T) {
	tests := []struct {
		threshold float64
		want      int
	}{
		{100, 200},
		{0, 10},        // always-condition watch: tiny cap, only "did anything match"
		{4, 10},        // floor — never below 10
		{15000, 30000}, // the 10k-saturation case: cap must exceed threshold
		{-5, 10},       // nonsense thresholds clamp to the floor
		// Review F11 (v0.9.x): the old flat 2^30 ceiling landed BELOW
		// large thresholds — gte watches went permanently dead, lt
		// watches false-fired. The cap now never drops to or below the
		// threshold: it saturates at the ES numeric limit (2^31-1)
		// while that still clears threshold+1, else the -1 sentinel
		// asks for an exact count (track_total_hits: true).
		{1e9, 2e9},               // threshold*2 still inside the ES int range
		{1.5e9, 1<<31 - 1},       // 2× overflows ES int → clamp, still > threshold
		{5e9, -1},                // journal scenario: gte 5e9 needs an exact count
		{1e18, -1},               // absurd threshold → exact count, no overflow
		{float64(1<<31 - 1), -1}, // boundary: threshold at the ES limit
	}
	for _, tt := range tests {
		if got := watcherTotalCap(tt.threshold); got != tt.want {
			t.Fatalf("watcherTotalCap(%v) = %d, want %d", tt.threshold, got, tt.want)
		}
	}
	// Invariant behind every case: a numeric cap must clear the
	// threshold with margin (gt at the boundary), else be the sentinel.
	for _, thr := range []float64{1, 100, 9999, 1e6, 1e9, 2e9, 2.1e9, 3e9, 1e12} {
		if got := watcherTotalCap(thr); got >= 0 && float64(got) < thr+1 {
			t.Fatalf("watcherTotalCap(%v) = %d — numeric cap below threshold+1 makes the compare lie", thr, got)
		}
	}
}

func TestWatcherTickPlan(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		last    time.Time
		hasOpen bool
		want    watcherTickAction
	}{
		{"due → run regardless of problem", now.Add(-5 * time.Minute), true, watcherTickRun},
		{"due, no problem → run", now.Add(-5 * time.Minute), false, watcherTickRun},
		{"not due, open problem → keep-alive", now.Add(-2 * time.Minute), true, watcherTickKeepAlive},
		{"not due, no problem → idle", now.Add(-2 * time.Minute), false, watcherTickIdle},
		{"never ran → run", time.Time{}, false, watcherTickRun},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := watcherTickPlan(tt.last, 5*time.Minute, now, tt.hasOpen); got != tt.want {
				t.Fatalf("watcherTickPlan = %v, want %v", got, tt.want)
			}
		})
	}
}

// Review F0/F9 (v0.9.x) regression — the watcher flap: a
// continuously-breaching watch with interval > 3× the 1m evaluator
// tick had its open problem auto-resolved "source silent" by
// sweepStaleProblems (cutoff now-3×tick, no metric exemption, cooldown
// stamp wiped) between due runs, then re-opened WITH a fresh
// notification on the next run — a page every interval, forever.
//
// The simulation drives the real pure pieces (watcherDue via
// watcherTickPlan, the sweep's now-3×tick cutoff arithmetic) over 1m
// ticks and asserts the keep-alive contract holds: updated_at never
// goes stale enough for the sweep, so a 30-minute continuously-breached
// 5m watch yields exactly ONE problem and ONE notification. The second
// case pins the due-run-errored path (persistent ES 403/timeout): even
// when due runs refresh nothing, keep-alive bounds staleness below the
// sweep cutoff.
func TestWatcherPacingSurvivesStaleSweep(t *testing.T) {
	const tick = time.Minute // evaluator interval (main.go wiring)
	cases := []struct {
		name string
		interval time.Duration
		// failAfterFirst: the first due run opens the problem, every
		// later due run errors (persistent 403/timeout) — the open
		// problem must survive on keep-alive refreshes alone.
		failAfterFirst bool
	}{
		{"5m watch, continuously breaching", 5 * time.Minute, false},
		{"10m watch, continuously breaching", 10 * time.Minute, false},
		{"5m watch, search errors after the problem opened", 5 * time.Minute, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
			var lastRun, updatedAt time.Time
			problemOpen := false
			opened, notified := 0, 0
			for i := 0; i <= 30; i++ {
				now := start.Add(time.Duration(i) * tick)
				// sweepStaleProblems: resolves open problems with
				// updated_at < now-3×tick and wipes the cooldown stamp.
				if problemOpen && updatedAt.Before(now.Add(-3*tick)) {
					t.Fatalf("tick %d: updated_at %s stale past the 3×tick sweep cutoff — problem would flap 'source silent' and re-page",
						i, now.Sub(updatedAt))
				}
				switch watcherTickPlan(lastRun, tc.interval, now, problemOpen) {
				case watcherTickRun:
					lastRun = now // stamped at attempt (real code)
					if tc.failAfterFirst && problemOpen {
						continue // RawSearch error: no settle, no refresh
					}
					if !problemOpen {
						problemOpen = true
						opened++
						notified++ // SendProblemAlert on open
					}
					updatedAt = now // settleCountAlert open/refresh upsert
				case watcherTickKeepAlive:
					updatedAt = now // FindOpenProblem + Upsert refresh
				case watcherTickIdle:
					// only legal before the first problem opens
					if problemOpen {
						t.Fatalf("tick %d: idle with an open problem — keep-alive contract broken", i)
					}
				}
			}
			if opened != 1 || notified != 1 {
				t.Fatalf("30m continuous breach: opened=%d notified=%d, want 1/1 (flap = regression)",
					opened, notified)
			}
		})
	}
}
