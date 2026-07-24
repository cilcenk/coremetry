package chstore

import "testing"

// Granular-sparklines sweep (M4, 2026-07-24) — SparklineBuckets 30 → 120
// with a grain-ALIGNED slot width (v0.9.207, review finding 6: the
// original sweep only floored at the grain, so 24h/120 = 720s slots over
// the 300s operations MV packed 3 MV rows into some slots and 2 into
// others — a periodic fake 1.5-2x spike on every sparkline).
// sparklineGrid is the single grid authority for the operations
// sparklines (MV + raw paths) and, via endpointsSparkGrid, the
// endpoints MV read; this table pins:
//   - the grain-multiple invariant: bucketSec is always an integer
//     multiple of grainSec, so uniform traffic maps a constant number
//     of MV rows into every full slot (no aliasing sawtooth);
//   - the grain floor: slots never go finer than the source bucket, so
//     short MV windows return FEWER, real slots instead of the
//     pre-sweep comb of always-empty sub-grain slots (1h @ 30 slots =
//     12 filled, 18 zero);
//   - the SparklineBuckets cap on long windows;
//   - the degenerate-input clamps (zero/negative window, zero grain).
func TestSparklineGrid(t *testing.T) {
	cases := []struct {
		name          string
		winSec        int64
		grainSec      int64
		wantBucketSec int64
		wantN         int
	}{
		// 5-min MV grain (operations MV path).
		{"30m MV window = 6 real MV slots", 1800, 300, 300, 6},
		{"1h MV window = 12 real MV slots (comb regression)", 3600, 300, 300, 12},
		{"10h MV window hits the cap exactly", 36000, 300, 300, 120},
		// v0.9.207 finding 6 — the common windows that aliased:
		// 12h ceil'd to 360s (1.2x grain → 2x spike every 5th slot),
		// 24h to 720s (2.4x grain → 1.5x ripple). Both now round UP
		// to the next grain multiple and ship fewer, honest slots.
		{"12h MV window = 72 x 10min grain-aligned slots", 43200, 300, 600, 72},
		{"24h MV window = 96 x 15min grain-aligned slots", 86400, 300, 900, 96},
		{"7d MV window = 119 x 85min grain-aligned slots", 604800, 300, 5100, 119},
		// Misaligned window: winSec measured from the aligned
		// bucketStart may exceed a whole multiple — n still covers
		// the final partial slot via the ceil.
		{"misaligned 33m MV window covers the 7th slot", 1980, 300, 300, 7},
		// 1m grain (endpoints spanmetrics_1m tier).
		{"1h @ 1m grain rounds 30s ideal up to one full minute", 3600, 60, 60, 60},
		{"3h @ 1m grain = 120s slots not 90s (2:1 sawtooth regression)", 10800, 60, 120, 90},
		{"7d @ 1m grain is already aligned", 604800, 60, 5040, 120},
		// 10s grain (endpoints spanmetrics_10s tier).
		{"30m @ 10s grain = 20s slots not 15s (2:1 sawtooth regression)", 1800, 10, 20, 90},
		{"45m @ 10s grain = 30s slots not 22s", 2700, 10, 30, 90},
		{"90m @ 10s grain = 50s slots not 45s", 5400, 10, 50, 108},
		// Raw-spans grain (fallback path).
		{"15m raw window densifies to 8s slots", 900, 1, 8, 113},
		{"2m raw window = per-second slots at the cap", 120, 1, 1, 120},
		{"50s raw window shorter than the cap", 50, 1, 1, 50},
		// Clamps.
		{"zero window clamps to one grain slot", 0, 300, 300, 1},
		{"negative window clamps to one grain slot", -60, 300, 300, 1},
		{"zero grain treated as 1s", 600, 0, 5, 120},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotSec, gotN := sparklineGrid(tc.winSec, tc.grainSec)
			if gotSec != tc.wantBucketSec || gotN != tc.wantN {
				t.Fatalf("sparklineGrid(%d, %d) = (%d, %d), want (%d, %d)",
					tc.winSec, tc.grainSec, gotSec, gotN, tc.wantBucketSec, tc.wantN)
			}
			if gotN > SparklineBuckets {
				t.Fatalf("n %d exceeds SparklineBuckets %d", gotN, SparklineBuckets)
			}
			grain := tc.grainSec
			if grain < 1 {
				grain = 1
			}
			if gotSec%grain != 0 {
				t.Fatalf("bucketSec %d is not a multiple of grain %d (v0.9.207 finding 6 invariant)",
					gotSec, grain)
			}
		})
	}
}

// The constant itself is contract: payload sizing, the endpoints MV
// 10s-tier switch, and the infra auto-scale all key off it. Pin the
// sweep's value so an accidental revert (or an ad-hoc local grid)
// fails loudly.
func TestSparklineBucketsValue(t *testing.T) {
	if SparklineBuckets != 120 {
		t.Fatalf("SparklineBuckets = %d, want 120 (granular-sparklines sweep M4)", SparklineBuckets)
	}
}

// v0.9.207 — endpointsSparkGrid tier routing + grid (review findings
// 5 + 9). Two invariants pinned:
//   - finding 9 (tier ∩ retention): spanmetrics_10s has TTL
//     toDate(time_bucket) + INTERVAL 2 DAY (store.go), whose
//     guaranteed floor is 24h from the bucket's own timestamp. The
//     10s tier is therefore only eligible when the window START is
//     ≤ 24h old — a short absolute window over older data (drag-zoom
//     on a 3-day-old incident) must route to spanmetrics_1m (30-day
//     TTL) instead of returning an empty table from an evicted MV.
//   - finding 5 (slot ⊇ grain multiple): the returned slot width is
//     an exact multiple of the chosen tier's grain, so the default
//     30m window no longer renders steady traffic as a 2:1 sawtooth
//     (pre-fix: 15s slots over the 10s MV grid).
func TestEndpointsSparkGrid(t *testing.T) {
	const day = int64(24 * 3600)
	cases := []struct {
		name          string
		windowSec     int64
		fromAgeSec    int64
		wantMV        string
		wantBucketSec int64
		wantN         int
	}{
		// Live windows under 2h → 10s tier, grain-aligned slots.
		{"default 30m live window = 10s tier, 20s slots (finding 5)", 1800, 1800, "spanmetrics_10s", 20, 90},
		{"5m live window = 10s tier, real 10s slots", 300, 300, "spanmetrics_10s", 10, 30},
		{"45m live window = 10s tier, 30s slots", 2700, 2700, "spanmetrics_10s", 30, 90},
		{"90m live window = 10s tier, 50s slots", 5400, 5400, "spanmetrics_10s", 50, 108},
		// Boundary of the coarsening check: 2h ideal slot is exactly
		// 60s — the 1m tier serves it natively.
		{"2h live window stays on the 1m tier", 7200, 7200, "spanmetrics_1m", 60, 120},
		{"3h live window = 1m tier, 120s slots not 90s (finding 5)", 10800, 10800, "spanmetrics_1m", 120, 90},
		{"7d live window = 1m tier, aligned 84min slots", 604800, 604800, "spanmetrics_1m", 5040, 120},
		// Finding 9 — window AGE, not length, gates the 10s tier.
		{"30m window ending 2h ago is still inside retention", 1800, 1800 + 2*3600, "spanmetrics_10s", 20, 90},
		{"1h window starting exactly 24h ago keeps the 10s tier", 3600, day, "spanmetrics_10s", 30, 120},
		{"1h window starting just past 24h falls back to 1m", 3600, day + 1, "spanmetrics_1m", 60, 60},
		{"1h zoom on a 3-day-old incident = 1m tier, non-empty (finding 9)", 3600, 3 * day, "spanmetrics_1m", 60, 60},
		{"30m window a week back = 1m tier, one real slot per bucket", 1800, 7 * day, "spanmetrics_1m", 60, 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotMV, gotSec, gotN := endpointsSparkGrid(tc.windowSec, tc.fromAgeSec)
			if gotMV != tc.wantMV || gotSec != tc.wantBucketSec || gotN != tc.wantN {
				t.Fatalf("endpointsSparkGrid(%d, %d) = (%q, %d, %d), want (%q, %d, %d)",
					tc.windowSec, tc.fromAgeSec, gotMV, gotSec, gotN,
					tc.wantMV, tc.wantBucketSec, tc.wantN)
			}
			grain := int64(60)
			if gotMV == "spanmetrics_10s" {
				grain = 10
			}
			if gotSec%grain != 0 {
				t.Fatalf("bucketSec %d not a multiple of the %s grain %d", gotSec, gotMV, grain)
			}
			if gotN > SparklineBuckets {
				t.Fatalf("n %d exceeds SparklineBuckets %d", gotN, SparklineBuckets)
			}
			if gotMV == "spanmetrics_10s" && tc.fromAgeSec > spanmetrics10sSafeAgeSec {
				t.Fatalf("10s tier chosen for a window start %ds old — outside the guaranteed TTL floor %ds",
					tc.fromAgeSec, spanmetrics10sSafeAgeSec)
			}
		})
	}
}
