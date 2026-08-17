package vmetrics

// v0.9.1164 — the operator-settable rate/last lookbehind floor.
//
// WHY EVERY BRANCH IS TABLED. The floor is a VALUE+SCOPE template: one number
// that applies to some rollups and must NEVER apply to others. That is the
// v0.6.36 unit-mixing shape (`toDate(time) + INTERVAL N HOUR`), where the bug
// was not in the expression but in the branch nobody exercised — so the rule
// from that incident applies here verbatim: a template taking a value plus a
// scope ships with a table that walks EVERY scope, not just the interesting
// one.
//
// The scope that matters most is the NEGATIVE one. `increase` is a window
// TOTAL: if the floor ever reached it, an operator raising the floor from 300
// to 600 to smooth their rate charts would silently DOUBLE every increase()
// number and every heatmap cell count, while the chart still said one bucket.
// Nothing on screen would say so, and the numbers would still look plausible —
// the v0.9.566 class on top of the v0.6.36 class.

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestValidRateWindowFloor(t *testing.T) {
	tests := []struct {
		v    int
		want bool
		why  string
	}{
		{0, true, "the unset sentinel — use the built-in default"},
		{MinRateWindowFloorSec, true, "lower bound is inclusive"},
		{MinRateWindowFloorSec - 1, false, "9s is below any OTLP export interval; rate() would ride scrape jitter"},
		{30, true, "the value a 10s-scrape federation actually wants"},
		{300, true, "the built-in default, explicitly pinned, is also a legal explicit value"},
		{MaxRateWindowFloorSec, true, "upper bound is inclusive"},
		{MaxRateWindowFloorSec + 1, false, "past an hour the floor would BE the window on every chart"},
		{-1, false, "negative is not a window"},
		{-300, false, "and neither is a negative that happens to match the default's magnitude"},
	}
	for _, tc := range tests {
		t.Run(strconv.Itoa(tc.v), func(t *testing.T) {
			if got := ValidRateWindowFloor(tc.v); got != tc.want {
				t.Fatalf("ValidRateWindowFloor(%d) = %v, want %v (%s)", tc.v, got, tc.want, tc.why)
			}
		})
	}
}

// An out-of-bounds persisted value falls back to the DEFAULT, not to a clamp.
//
// The PUT gate makes this unreachable through the UI, so the only way in is a
// hand-edited system_settings blob. For that case the default is the honest
// answer ("your value was not accepted"); clamping would answer with a third
// number the operator never typed and cannot see anywhere.
func TestResolveRateWindowFloor(t *testing.T) {
	tests := []struct {
		v    int
		want int
		why  string
	}{
		{0, promLookbehindFloorSec, "unset → the shipped 5m default"},
		{10, 10, "in-bounds values pass through untouched"},
		{30, 30, ""},
		{300, 300, "explicitly setting the default is a no-op, not a special case"},
		{3600, 3600, "the ceiling is usable"},
		{9, promLookbehindFloorSec, "below bounds → DEFAULT, never clamped up to 10"},
		{3601, promLookbehindFloorSec, "above bounds → DEFAULT, never clamped down to 3600"},
		{-5, promLookbehindFloorSec, "negative → DEFAULT; a `[-5s]` window is a VM error"},
	}
	for _, tc := range tests {
		t.Run(strconv.Itoa(tc.v), func(t *testing.T) {
			if got := resolveRateWindowFloor(tc.v); got != tc.want {
				t.Fatalf("resolveRateWindowFloor(%d) = %d, want %d (%s)", tc.v, got, tc.want, tc.why)
			}
		})
	}
}

// The full (rollup × step × explicit window × floor) matrix.
func TestPromRollupWindowWithConfiguredFloor(t *testing.T) {
	tests := []struct {
		rollup  string
		step    int
		rateWin int
		floor   int
		want    int
		why     string
	}{
		// ── floor=60: the whole reason the setting exists ────────────────────
		{rollupRate, 10, 0, 60, 60, "step below the floor is widened to it"},
		{rollupRate, 60, 0, 60, 60, "step equal to the floor is unchanged"},
		{rollupRate, 120, 0, 60, 120, "step above the floor wins — the floor is a FLOOR"},
		{rollupLast, 10, 0, 60, 60, "last_over_time shares the floor (staleness behaviour)"},
		{rollupLast, 120, 0, 60, 120, ""},
		{rollupLast, 10, 999, 60, 60, "RateWindowSec never reaches last — it is a RATE window"},
		{rollupIncrease, 10, 0, 60, 10, "INCREASE IS NEVER FLOORED: a window total, not a rate"},
		{rollupIncrease, 600, 0, 60, 600, ""},
		{"", 10, 0, 60, 10, "no rollup, no lookbehind widening — the instant-vector path"},

		// ── an EXPLICIT caller window always wins, unfloored ────────────────
		{rollupRate, 10, 180, 60, 180, "the operator's [3m] reference is answered, not promoted"},
		{rollupRate, 10, 30, 60, 30, "even a window BELOW the floor: they asked for 30s"},
		{rollupRate, 10, 30, 300, 30, "same with the default floor — this is pre-existing behaviour"},
		{rollupIncrease, 10, 180, 60, 180, ""},

		// ── the bounds, applied end to end ──────────────────────────────────
		{rollupRate, 10, 0, MinRateWindowFloorSec, 10, "the narrowest legal floor"},
		{rollupRate, 5, 0, MinRateWindowFloorSec, 10, "…and it still widens a finer step"},
		{rollupRate, 10, 0, MaxRateWindowFloorSec, 3600, "the widest legal floor"},

		// ── the DEFAULT PIN: floor=0 must reproduce v0.9.1154 exactly ───────
		{rollupRate, 10, 0, 0, promLookbehindFloorSec, "REGRESSION PIN: unset = the 300s default"},
		{rollupRate, 600, 0, 0, 600, "REGRESSION PIN: step above 300 still wins"},
		{rollupLast, 10, 0, 0, promLookbehindFloorSec, "REGRESSION PIN"},
		{rollupIncrease, 10, 0, 0, 10, "REGRESSION PIN: unfloored, default or not"},
		{"", 10, 0, 0, 10, "REGRESSION PIN"},

		// ── an invalid persisted floor degrades to the default, never to 0 ──
		{rollupRate, 10, 0, 9, promLookbehindFloorSec, "out of bounds → default, NOT a removed floor"},
		{rollupRate, 10, 0, 3601, promLookbehindFloorSec, ""},
		{rollupRate, 10, 0, -60, promLookbehindFloorSec, "the direction a zero-value bug would take"},

		// ── step normalisation is untouched by any of this ──────────────────
		{rollupRate, 0, 0, 60, 60, "[0s] is a VM error; step floors to 1 then the floor applies"},
		{rollupIncrease, 0, 0, 60, 1, "…and with no floor available, to 1"},
		{"", 0, 0, 60, 1, ""},
	}
	for _, tc := range tests {
		name := tc.rollup + "/step=" + strconv.Itoa(tc.step) +
			"/win=" + strconv.Itoa(tc.rateWin) + "/floor=" + strconv.Itoa(tc.floor)
		t.Run(name, func(t *testing.T) {
			got := promRollupWindow(tc.rollup, tc.step, tc.rateWin, tc.floor)
			if got != tc.want {
				t.Fatalf("promRollupWindow(%q, step=%d, win=%d, floor=%d) = %d, want %d (%s)",
					tc.rollup, tc.step, tc.rateWin, tc.floor, got, tc.want, tc.why)
			}
			if got < 1 {
				t.Fatalf("window must never be < 1s ([0s] is a VM error), got %d", got)
			}
		})
	}
}

// End to end: the configured floor reaches the rendered EXPRESSION, on every
// arm that composition can produce.
//
// A unit test on promRollupWindow alone would have passed while the setting
// never left the struct — the `or` composition (v0.9.1160) renders the window
// in up to three places per query, and an arm that kept the constant would be
// invisible in review.
func TestConfiguredFloorReachesEveryArm(t *testing.T) {
	from := time.Unix(1700000000, 0)
	to := from.Add(time.Hour)
	// step=10 is below both the default (300) and the configured floor (60),
	// so the two are distinguishable in the output.
	base := chstore.MetricQueryFilter{
		Name: "http.server.request.duration", Service: "checkout",
		From: from, To: to, StepSeconds: 10,
	}

	tests := []struct {
		name   string
		agg    string
		floor  int
		want   []string // every window that must appear
		absent []string
	}{
		{
			name: "rate — both arms carry the configured floor",
			agg:  "rate", floor: 60,
			want:   []string{"[60s]"},
			absent: []string{"[300s]", "[10s]"},
		},
		{
			name: "rate — default when unset",
			agg:  "rate", floor: 0,
			want:   []string{"[300s]"},
			absent: []string{"[10s]"},
		},
		{
			name: "avg — the _sum/_count ratio arm is floored too",
			agg:  "avg", floor: 60,
			want:   []string{"[60s]"},
			absent: []string{"[300s]"},
		},
		{
			name: "avg — default when unset",
			agg:  "avg", floor: 0,
			want: []string{"[300s]"},
		},
		{
			name: "percentile — the bucket rate carries it",
			agg:  "p99", floor: 60,
			want:   []string{"[60s]"},
			absent: []string{"[300s]"},
		},
		{
			name: "last — floored",
			agg:  "last", floor: 60,
			want:   []string{"[60s]"},
			absent: []string{"[300s]"},
		},
		{
			name: "INCREASE — never floored, whatever the setting says",
			agg:  "increase", floor: 3600,
			want:   []string{"[10s]"},
			absent: []string{"[3600s]", "[300s]"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := base
			f.Aggregation = tc.agg
			expr, err := buildPromQL(f, promOpts{RateWindowFloorS: tc.floor})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(expr, w) {
					t.Fatalf("expression is missing window %s: %s", w, expr)
				}
			}
			for _, w := range tc.absent {
				if strings.Contains(expr, w) {
					t.Fatalf("expression still carries window %s: %s", w, expr)
				}
			}
			// EVERY window in the expression must agree. `or` renders the
			// shape two or three times, and one stale arm would mean two
			// series on one chart measured over different windows.
			for _, m := range promWindowRe.FindAllStringSubmatch(expr, -1) {
				if !contains(tc.want, "["+m[1]+"s]") {
					t.Fatalf("arm window [%ss] is not one of %v: %s", m[1], tc.want, expr)
				}
			}
		})
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// The HEATMAP is exempt, and the exemption is asserted against the setting
// rather than merely documented.
//
// Its window has to TILE — consecutive buckets covering adjacent,
// non-overlapping intervals — because chstore.HistogramSeries.Counts is
// per-interval OBSERVATION COUNTS. Widening it would double-count observations
// into neighbouring cells AND multiply the "N samples" tooltip, while the
// chart still said one bucket.
func TestHeatmapIgnoresTheRateWindowFloor(t *testing.T) {
	from := time.Unix(1700000000, 0)
	to := from.Add(time.Hour)
	f := chstore.MetricQueryFilter{
		Name: "http.server.request.duration", Service: "checkout",
		From: from, To: to, StepSeconds: 10,
	}
	for _, floor := range []int{0, 10, 60, 300, 3600} {
		q, step, err := buildHistogramPromQL(f, promOpts{
			RateWindowFloorS: floor,
			// Scoped by Service, so the guard is satisfied without lifting it.
		})
		if err != nil {
			t.Fatalf("floor=%d: %v", floor, err)
		}
		if step != 10 {
			t.Fatalf("floor=%d: step drifted to %d — the heatmap step is the "+
				"panel's time resolution, not a rate window", floor, step)
		}
		if want := "[" + strconv.Itoa(step) + "s]"; !strings.Contains(q, want) {
			t.Fatalf("floor=%d: heatmap window is not the tiling step %s: %s", floor, want, q)
		}
	}
}
