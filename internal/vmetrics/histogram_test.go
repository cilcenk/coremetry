package vmetrics

// v0.9.1157 — VictoriaMetrics Faz 2: the histogram heatmap + the raw-query
// proxy.
//
// The heatmap is the most silent surface in this backend. Everything else
// either charts a number an operator can sanity-check against a known value,
// or errors. A heatmap assembled from mis-differenced buckets renders as a
// perfectly plausible picture — usually one bright band at the tail, which
// reads as a latency crisis — and the p50/p95/p99 computed off the same
// vectors stay finite and in-range. Nothing about it looks broken.
//
// So the pure pieces are pinned separately and exhaustively:
//
//	leBound        — what counts as a bound, including the three spellings of
//	                 infinity and the two that must be REJECTED.
//	bucketLayout   — unordered input, duplicate le, missing/garbage le, and
//	                 the +Inf-only degenerate case.
//	deCumulateLE   — the le-prefix-sum differencing, its clamp, and its
//	                 monotonic reference (the case where the two plausible
//	                 implementations disagree on the observation TOTAL).
//	buildHistogram — the expression, pinned as a literal so the `increase`
//	  PromQL         vs `rate` decision and the deliberate ABSENCE of the 5m
//	                 floor are both visible in one string.
//
// Then two wire tests over httptest, because the properties that remain are
// about what actually leaves the process: the query param the heatmap sends,
// and the fact that the raw proxy sends the operator's string UNTOUCHED.

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/promapi"
)

// ── leBound ─────────────────────────────────────────────────────────────────

func TestLeBound(t *testing.T) {
	ok := map[string]float64{
		"0":     0,
		"0.1":   0.1,
		"1":     1,
		"7.5":   7.5,
		"1e3":   1000,
		"1E-3":  0.001,
		"  5  ": 5, // whitespace happens through proxies and hand-written rules
		// The overflow bucket, in every spelling seen in the wild.
		// Prometheus writes "+Inf"; VictoriaMetrics has emitted "Inf" and
		// "inf" across versions and a remote-write producer may send either.
		// Getting this wrong does not error — it drops the overflow bucket,
		// which pulls every percentile DOWN because the tail vanishes.
		"+Inf": math.Inf(1),
		"Inf":  math.Inf(1),
		"inf":  math.Inf(1),
		"+inf": math.Inf(1),
	}
	for in, want := range ok {
		got, valid := leBound(in)
		if !valid {
			t.Errorf("leBound(%q): rejected a usable bound", in)
			continue
		}
		if got != want {
			t.Errorf("leBound(%q) = %v, want %v", in, got, want)
		}
	}

	// Rejected. NaN cannot be ordered, and -Inf would sort ahead of every
	// real bucket and swallow the whole distribution into slot 0 — both are
	// values ParseFloat accepts, which is exactly why the guard is explicit
	// rather than left to the parse error.
	for _, in := range []string{"", "   ", "abc", "0.1.2", "NaN", "nan", "-Inf", "-inf"} {
		if v, valid := leBound(in); valid {
			t.Errorf("leBound(%q) accepted, returning %v", in, v)
		}
	}
}

// ── bucketLayout ────────────────────────────────────────────────────────────

func TestBucketLayout(t *testing.T) {
	tests := []struct {
		name       string
		les        []string
		wantBounds []float64
		wantSlot   []int
	}{
		{
			name:       "ascending with +Inf last — the ordinary case",
			les:        []string{"0.1", "0.5", "1", "+Inf"},
			wantBounds: []float64{0.1, 0.5, 1},
			wantSlot:   []int{0, 1, 2, 3},
		},
		{
			// VM gives no ordering guarantee on a query_range result: vmselect
			// merges shards in whatever order they answer. An implementation
			// that trusted the arrival order would map counts to the wrong
			// bounds and the heatmap's y-axis would be scrambled — with no
			// error anywhere.
			name:       "arbitrary order is sorted, +Inf still lands last",
			les:        []string{"+Inf", "1", "0.1", "0.5"},
			wantBounds: []float64{0.1, 0.5, 1},
			wantSlot:   []int{3, 2, 0, 1},
		},
		{
			// Our own `sum by (le)` cannot produce duplicates, but a caller
			// pointed at a recording rule or a federated view can. Sharing a
			// slot makes the counts ADD, which is the semantics the sum they
			// stand in for would have had.
			name:       "duplicate le values share one slot",
			les:        []string{"0.5", "0.1", "0.5", "+Inf"},
			wantBounds: []float64{0.1, 0.5},
			wantSlot:   []int{1, 0, 1, 2},
		},
		{
			name:       "duplicate +Inf also shares the overflow slot",
			les:        []string{"0.1", "+Inf", "Inf"},
			wantBounds: []float64{0.1},
			wantSlot:   []int{0, 1, 1},
		},
		{
			name:       "no +Inf at all — the layout is still valid, just capped",
			les:        []string{"0.1", "0.5"},
			wantBounds: []float64{0.1, 0.5},
			wantSlot:   []int{0, 1},
		},
		{
			// Degenerate: a total with no distribution under it. bounds is
			// empty and the CALLER turns that into the honest empty shape —
			// see TestAssembleHistogramInfOnlyIsEmptyWithNote.
			name:       "+Inf only yields no bounds",
			les:        []string{"+Inf"},
			wantBounds: nil,
			wantSlot:   []int{0},
		},
		{
			name:       "zero is a legitimate bound, not an absent one",
			les:        []string{"0", "0.1", "+Inf"},
			wantBounds: []float64{0, 0.1},
			wantSlot:   []int{0, 1, 2},
		},
		{
			name:       "empty input",
			les:        nil,
			wantBounds: nil,
			wantSlot:   nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bounds, slot, err := bucketLayout(tc.les)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !floatsEqual(bounds, tc.wantBounds) {
				t.Fatalf("bounds = %v, want %v", bounds, tc.wantBounds)
			}
			if !intsEqual(slot, tc.wantSlot) {
				t.Fatalf("slot = %v, want %v", slot, tc.wantSlot)
			}
			// Structural invariant, checked on every row rather than spelled
			// out per case: no slot may point past the counts vector, or the
			// assembly indexes out of range.
			for i, s := range slot {
				if s < 0 || s > len(bounds) {
					t.Fatalf("slot[%d] = %d is outside [0,%d]", i, s, len(bounds))
				}
			}
		})
	}
}

// A missing or garbage `le` is an ERROR, not a skip.
//
// This is the v0.9.566 rule applied to a bucket layout: silently dropping a
// bucket does not empty the chart, it shifts mass into the neighbouring band
// and moves every percentile. Wrong-but-plausible, and nobody questions it.
func TestBucketLayoutRefusesUnusableLE(t *testing.T) {
	for _, les := range [][]string{
		{"0.1", "", "+Inf"},     // a series with no le label at all
		{"0.1", "abc", "+Inf"},  // unparseable
		{"0.1", "NaN", "+Inf"},  // parseable but unorderable
		{"0.1", "-Inf", "+Inf"}, // parseable but would swallow slot 0
		{""},                    // the whole result is not a bucket series
	} {
		_, _, err := bucketLayout(les)
		if err == nil {
			t.Fatalf("bucketLayout(%v): want a refusal", les)
		}
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("bucketLayout(%v): refusal not tagged ErrUnsupported (%v) — the API would "+
				"502 and blame a healthy VictoriaMetrics", les, err)
		}
	}
	// The message names the offending value, because the common cause is
	// recognisable on sight: a series with no `le` is usually not a histogram
	// bucket at all (VM's own native histograms label buckets `vmrange`).
	_, _, err := bucketLayout([]string{"0.1", "wat", "+Inf"})
	if !strings.Contains(err.Error(), "wat") {
		t.Fatalf("refusal does not echo the bad value: %v", err)
	}
	if !strings.Contains(err.Error(), labelLE) {
		t.Fatalf("refusal does not name the le label: %v", err)
	}
}

// ── deCumulateLE ────────────────────────────────────────────────────────────

func TestDeCumulateLE(t *testing.T) {
	tests := []struct {
		name string
		cum  []float64
		want []uint64
	}{
		{
			// The whole point of the function. `le` is an inclusive PREFIX
			// SUM: 30 observations ≤ 0.5 includes the 10 that were ≤ 0.1.
			// Skipping the differencing would put ~everything in the top
			// bucket and paint the heatmap as one bright tail band.
			name: "prefix sums become per-bucket counts",
			cum:  []float64{10, 30, 32},
			want: []uint64{10, 20, 2},
		},
		{
			name: "an all-zero column stays zero, not garbage",
			cum:  []float64{0, 0, 0},
			want: []uint64{0, 0, 0},
		},
		{
			name: "everything in the first bucket",
			cum:  []float64{50, 50, 50},
			want: []uint64{50, 0, 0},
		},
		{
			name: "everything in the overflow bucket",
			cum:  []float64{0, 0, 50},
			want: []uint64{0, 0, 50},
		},
		{
			// increase() over an integer counter is integral in principle but
			// arrives as a float64 that has been through JSON. Truncation
			// would turn 50 observations into 49, every column, forever.
			name: "float noise rounds to the nearest count",
			cum:  []float64{9.999999999999998, 30.000000000000004, 32.00000000000001},
			want: []uint64{10, 20, 2},
		},
		{
			// THE CASE THE TWO PLAUSIBLE IMPLEMENTATIONS DISAGREE ON.
			// cum dips at index 1 (a bucket series churned mid-window). With a
			// monotonic reference: 10 + 0 + 10 = 20, matching cum[2] — the
			// total the percentile estimator normalises against. Carrying the
			// dip forward instead gives 10 + 0 + 12 = 22 observations from a
			// histogram whose own total says 20, and the extra two land in the
			// TAIL, which is where they move p99.
			name: "a non-monotonic dip is absorbed, not redistributed into the tail",
			cum:  []float64{10, 8, 20},
			want: []uint64{10, 0, 10},
		},
		{
			name: "a negative first entry clamps to zero rather than wrapping uint64",
			cum:  []float64{-5, 10},
			want: []uint64{0, 10},
		},
		{
			name: "empty input",
			cum:  nil,
			want: []uint64{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := deCumulateLE(tc.cum)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d (%v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// The clamp is not cosmetic: uint64(-1.0) is ~1.8e19, which in the heatmap is
// one cell that saturates the entire colour scale and makes every real value
// render as zero. Pinned separately so the reason survives a refactor of the
// table above.
func TestDeCumulateLENeverWrapsUint64(t *testing.T) {
	for _, c := range [][]float64{{5, 1}, {0, -3}, {100, 1, 2}} {
		for i, v := range deCumulateLE(c) {
			if v > 1e15 {
				t.Fatalf("cum %v produced counts[%d] = %d — a negative difference wrapped", c, i, v)
			}
		}
	}
}

// ── buildHistogramPromQL ────────────────────────────────────────────────────

// The expression, as a literal. Two decisions are visible in it and both are
// load-bearing:
//
//	`increase`, NOT `rate` — HistogramSeries.Counts is [][]uint64, per-interval
//	  OBSERVATION COUNTS. rate() delivers counts-per-second, so 50 samples over
//	  a 60s step arrive as 0.83 and TRUNCATE TO ZERO in a uint64: the heatmap
//	  goes blank and the percentiles computed from an all-zero vector are 0
//	  too. (The percentile translation in promql.go uses rate() for the
//	  opposite reason — histogram_quantile reads bucket ratios, so scale
//	  cancels there.)
//	NO 5m FLOOR — the window equals the step exactly, so consecutive buckets
//	  TILE the window. A wider window double-counts observations into
//	  neighbouring cells; and widening an increase() multiplies the number
//	  while the chart still says one bucket (the v0.6.36 unit-scale class).
func TestBuildHistogramPromQL(t *testing.T) {
	from := time.Unix(1_700_000_000, 0)
	tests := []struct {
		name     string
		f        chstore.MetricQueryFilter
		want     string
		wantStep int
	}{
		{
			name: "1h window, auto step — window == step, and it is increase()",
			f: chstore.MetricQueryFilter{
				Name: "http.server.request.duration",
				From: from, To: from.Add(time.Hour),
			},
			// 3600s / 300 buckets = 12s. Deliberately NOT 300s: proof the
			// rate floor does not apply here.
			want:     `sum by (le) (increase({__name__="http.server.request.duration_bucket"}[12s]))`,
			wantStep: 12,
		},
		{
			name: "explicit step is honoured",
			f: chstore.MetricQueryFilter{
				Name: "m", From: from, To: from.Add(time.Hour), StepSeconds: 60,
			},
			want:     `sum by (le) (increase({__name__="m_bucket"}[60s]))`,
			wantStep: 60,
		},
		{
			name: "already-suffixed name is not doubled",
			f: chstore.MetricQueryFilter{
				Name: "m_bucket", From: from, To: from.Add(time.Hour), StepSeconds: 60,
			},
			want:     `sum by (le) (increase({__name__="m_bucket"}[60s]))`,
			wantStep: 60,
		},
		{
			name: "service scoping",
			f: chstore.MetricQueryFilter{
				Name: "m", Service: "cart", From: from, To: from.Add(time.Hour), StepSeconds: 60,
			},
			want:     `sum by (le) (increase({__name__="m_bucket", service_name="cart"}[60s]))`,
			wantStep: 60,
		},
		{
			name: "filters go INSIDE the selector, dotted keys sanitized",
			f: chstore.MetricQueryFilter{
				Name: "m", From: from, To: from.Add(time.Hour), StepSeconds: 60,
				Filters: []chstore.FilterExpr{
					{Key: "http.route", Op: "=", Values: []string{"/api/cart"}},
					{Key: "pod", Op: "IN", Values: []string{"a", "b"}},
				},
			},
			want: `sum by (le) (increase({__name__="m_bucket", http_route="/api/cart", ` +
				`pod=~"a|b"}[60s]))`,
			wantStep: 60,
		},
		{
			// maxDataPoints drives the step when none is pinned — the F1
			// pixel-adaptive contract, same as every other VM read.
			name: "maxDataPoints drives the step",
			f: chstore.MetricQueryFilter{
				Name: "m", From: from, To: from.Add(time.Hour), MaxDataPoints: 60,
			},
			want:     `sum by (le) (increase({__name__="m_bucket"}[60s]))`,
			wantStep: 60,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, step, err := buildHistogramPromQL(tc.f)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
			if step != tc.wantStep {
				t.Fatalf("step = %d, want %d", step, tc.wantStep)
			}
			// The step the expression WRITES must be the step the caller
			// SENDS as the query_range param, or the heatmap's buckets stop
			// tiling the window (rule 3 in promql.go).
			if !strings.Contains(got, "["+strconv.Itoa(step)+"s]") {
				t.Fatalf("expression window disagrees with the returned step %d: %s", step, got)
			}
		})
	}
}

// The same refusals buildPromQL makes, for the same reasons — a filter that
// cannot be expressed must fail the QUERY rather than vanish from it.
func TestBuildHistogramPromQLRefusals(t *testing.T) {
	from := time.Unix(1_700_000_000, 0)
	base := chstore.MetricQueryFilter{Name: "m", From: from, To: from.Add(time.Hour)}

	inst := base
	inst.Instance = "pg-1"
	eng := base
	eng.Engine = "postgres"
	bad := base
	bad.Filters = []chstore.FilterExpr{{Key: "n", Op: ">", Values: []string{"5"}}}

	for name, f := range map[string]chstore.MetricQueryFilter{
		"instance scoping":           inst,
		"engine scoping":             eng,
		"LIKE-class filter operator": bad,
	} {
		if _, _, err := buildHistogramPromQL(f); err == nil {
			t.Errorf("%s: want a refusal", name)
		} else if !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: refusal not tagged ErrUnsupported: %v", name, err)
		}
	}

	// An empty name is a CLIENT error, not an untranslatable query — the API
	// gate answers 400 before this is reached, and the message must not
	// pretend VictoriaMetrics refused something.
	if _, _, err := buildHistogramPromQL(chstore.MetricQueryFilter{}); err == nil {
		t.Error("want an error for an empty metric name")
	} else if errors.Is(err, ErrUnsupported) {
		t.Errorf("a missing name is tagged ErrUnsupported: %v", err)
	}
}

// histogramStep widens a pathological step until the Go-side grid fits.
//
// The exposure is real and was measured on the CH sibling first (v0.9.114):
// ?step=1 over a multi-year window made accum[nTime][nBuckets] ~1e9 wide and
// OOM'd the single binary. promStep's own 11000-point ceiling is four times
// too loose for a grid that is also nBuckets deep.
func TestHistogramStepClampsTheGrid(t *testing.T) {
	from := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name        string
		window      time.Duration
		step, maxDP int
		want        int
	}{
		{name: "1h auto", window: time.Hour, want: 12},
		{name: "1h explicit 60", window: time.Hour, step: 60, want: 60},
		{
			// promStep first widens 1s to 236s (the 11000-point ceiling),
			// then the grid clamp widens it again to 519s (5000 buckets).
			name:   "30d at step=1 — both ceilings bite, in order",
			window: 30 * 24 * time.Hour, step: 1, want: 519,
		},
		{
			name:   "1y at step=1 stays bounded",
			window: 365 * 24 * time.Hour, step: 1, want: 6308,
		},
		{name: "degenerate zero window never yields step 0", window: 0, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := histogramStep(from, from.Add(tc.window), tc.step, tc.maxDP)
			if got != tc.want {
				t.Fatalf("histogramStep = %d, want %d", got, tc.want)
			}
			if got < 1 {
				t.Fatal("step < 1 — [0s] is a VM error and a zero stepNs divides by zero downstream")
			}
			// The property the number stands for.
			if sec := int(tc.window.Seconds()); sec > 0 && sec/got > maxHistogramTimeBuckets {
				t.Fatalf("grid would be %d buckets deep (cap %d)", sec/got, maxHistogramTimeBuckets)
			}
		})
	}
}

// ── assembleHistogram ───────────────────────────────────────────────────────

// The full assembly over a realistic cumulative matrix: three le series, two
// time slots, cumulative in BOTH axes on the way in and per-bucket on the way
// out.
func TestAssembleHistogram(t *testing.T) {
	from := time.Unix(1_700_000_000, 0)
	to := from.Add(60 * time.Second)
	t0 := float64(from.Unix())
	t1 := float64(to.Unix())

	got, err := assembleHistogram([]promapi.Series{
		vmLESeries("0.1", [2]float64{t0, 10}, [2]float64{t1, 20}),
		vmLESeries("0.5", [2]float64{t0, 30}, [2]float64{t1, 45}),
		vmLESeries("+Inf", [2]float64{t0, 32}, [2]float64{t1, 50}),
	}, "m_bucket", from, to, 60)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !floatsEqual(got.Bounds, []float64{0.1, 0.5}) {
		t.Fatalf("bounds = %v", got.Bounds)
	}
	// The grid comes from (from, step), NOT from the timestamps VM returned,
	// so two polls of one panel share an x-axis even if a series gained or
	// lost a point between them.
	if want := []int64{from.UnixNano(), to.UnixNano()}; !int64sEqual(got.Times, want) {
		t.Fatalf("times = %v, want %v", got.Times, want)
	}
	// counts[t][i]: the le prefix sums differenced.
	//   t0: 10, 30-10=20, 32-30=2
	//   t1: 20, 45-20=25, 50-45=5
	for ti, want := range [][]uint64{{10, 20, 2}, {20, 25, 5}} {
		for i := range want {
			if got.Counts[ti][i] != want[i] {
				t.Fatalf("counts[%d] = %v, want %v", ti, got.Counts[ti], want)
			}
		}
	}
	// Percentiles come from chstore.PercentileFromBuckets — the SAME
	// estimator the ClickHouse path uses, which is why it was exported. p50
	// at t0: total 32, target 16, lands in bucket 1 at
	// 0.1 + (0.5-0.1)*((16-10)/20) = 0.22.
	if math.Abs(got.P50[0]-0.22) > 1e-9 {
		t.Fatalf("p50[0] = %v, want 0.22", got.P50[0])
	}
	// p95/p99 land in the +Inf bucket, which has no finite upper bound — the
	// estimator clamps to the last finite bound rather than inventing one.
	if got.P95[0] != 0.5 || got.P99[0] != 0.5 {
		t.Fatalf("p95/p99[0] = %v/%v, want 0.5/0.5", got.P95[0], got.P99[0])
	}
	// Skipped counts mismatched-BOUNDS series, which `sum by (le)` already
	// collapsed upstream. A nonzero value here would invent a problem.
	if got.Skipped != 0 {
		t.Fatalf("skipped = %d, want 0", got.Skipped)
	}
	if got.Note != "" {
		t.Fatalf("a populated heatmap carries a note: %q", got.Note)
	}
}

// Series order must not matter: vmselect merges shards in whatever order they
// answer, so an assembly that trusted arrival order would scramble the y-axis
// intermittently — on some polls and not others.
func TestAssembleHistogramIsOrderIndependent(t *testing.T) {
	from := time.Unix(1_700_000_000, 0)
	to := from.Add(60 * time.Second)
	t0 := float64(from.Unix())

	ascending := []promapi.Series{
		vmLESeries("0.1", [2]float64{t0, 10}),
		vmLESeries("0.5", [2]float64{t0, 30}),
		vmLESeries("+Inf", [2]float64{t0, 32}),
	}
	shuffled := []promapi.Series{ascending[2], ascending[0], ascending[1]}

	a, err := assembleHistogram(ascending, "m_bucket", from, to, 60)
	if err != nil {
		t.Fatal(err)
	}
	b, err := assembleHistogram(shuffled, "m_bucket", from, to, 60)
	if err != nil {
		t.Fatal(err)
	}
	if !floatsEqual(a.Bounds, b.Bounds) {
		t.Fatalf("bounds differ: %v vs %v", a.Bounds, b.Bounds)
	}
	for i := range a.Counts[0] {
		if a.Counts[0][i] != b.Counts[0][i] {
			t.Fatalf("counts differ: %v vs %v", a.Counts[0], b.Counts[0])
		}
	}
	if a.P99[0] != b.P99[0] {
		t.Fatalf("p99 differs: %v vs %v", a.P99[0], b.P99[0])
	}
}

// Duplicate le series ADD into one slot — the semantics the `sum by (le)` they
// stand in for would have had.
func TestAssembleHistogramAddsDuplicateLE(t *testing.T) {
	from := time.Unix(1_700_000_000, 0)
	to := from.Add(60 * time.Second)
	t0 := float64(from.Unix())

	got, err := assembleHistogram([]promapi.Series{
		vmLESeries("0.1", [2]float64{t0, 4}),
		vmLESeries("0.1", [2]float64{t0, 6}),
		vmLESeries("+Inf", [2]float64{t0, 10}),
	}, "m_bucket", from, to, 60)
	if err != nil {
		t.Fatal(err)
	}
	if got.Counts[0][0] != 10 {
		t.Fatalf("duplicate le did not add: %v", got.Counts[0])
	}
}

// An empty result is HONEST, not blank. Same reasoning as the percentile note:
// the query went to `<name>_bucket`, a series the operator never typed, so a
// bare empty heatmap cannot distinguish "no data in this window" from "this
// metric is not a histogram".
func TestAssembleHistogramEmptyCarriesTheNote(t *testing.T) {
	from := time.Unix(1_700_000_000, 0)
	got, err := assembleHistogram(nil, "http.server.request.duration_bucket", from, from.Add(time.Hour), 60)
	if err != nil {
		t.Fatalf("an empty result must not be an error: %v", err)
	}
	if got.Note == "" {
		t.Fatal("empty heatmap carries no note — the operator sees a silent blank panel and " +
			"cannot tell 'no data' from 'not a histogram'")
	}
	if !strings.Contains(got.Note, "http.server.request.duration_bucket") {
		t.Fatalf("note does not name the series that was queried: %s", got.Note)
	}
	// The shape stays renderable: the frontend's adapter reads bounds/counts
	// with ?? fallbacks, and a half-built grid would draw an empty axis with
	// no explanation next to it.
	if len(got.Bounds) != 0 || len(got.Counts) != 0 || len(got.Times) != 0 {
		t.Fatalf("empty result is not empty: %+v", got)
	}
}

// +Inf with no finite bounds is a TOTAL with no distribution under it: there
// is no y-axis to draw. Reported as the empty-with-a-note shape rather than a
// one-row heatmap, which would imply every observation exceeded a bound
// nothing on screen names.
func TestAssembleHistogramInfOnlyIsEmptyWithNote(t *testing.T) {
	from := time.Unix(1_700_000_000, 0)
	got, err := assembleHistogram([]promapi.Series{
		vmLESeries("+Inf", [2]float64{float64(from.Unix()), 99}),
	}, "m_bucket", from, from.Add(time.Hour), 60)
	if err != nil {
		t.Fatal(err)
	}
	if got.Note == "" || len(got.Bounds) != 0 {
		t.Fatalf("+Inf-only result is not the honest empty shape: %+v", got)
	}
}

// A grid slot mapping that TRUNCATED would shift the whole heatmap one cell
// left, because `ts * 1e9` carries ~200ns of float64 error at
// epoch-nanosecond magnitude and VM's grid timestamps sit exactly on slot
// boundaries. (The CH sibling truncates on purpose — it bins RAW sample
// times, which fall anywhere inside a bucket.)
func TestAssembleHistogramRoundsToTheAlignedSlot(t *testing.T) {
	from := time.Unix(1_700_000_000, 0)
	to := from.Add(120 * time.Second)
	// Three aligned slots, with the middle one nudged by a hair in each
	// direction — the shape float64 error takes.
	got, err := assembleHistogram([]promapi.Series{
		vmLESeries("0.1",
			[2]float64{float64(from.Unix()), 1},
			[2]float64{float64(from.Unix()) + 60 - 1e-7, 2},
			[2]float64{float64(from.Unix()) + 120 + 1e-7, 3}),
		vmLESeries("+Inf",
			[2]float64{float64(from.Unix()), 1},
			[2]float64{float64(from.Unix()) + 60 - 1e-7, 2},
			[2]float64{float64(from.Unix()) + 120 + 1e-7, 3}),
	}, "m_bucket", from, to, 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Times) != 3 {
		t.Fatalf("grid has %d slots, want 3", len(got.Times))
	}
	for i, want := range []uint64{1, 2, 3} {
		if got.Counts[i][0] != want {
			t.Fatalf("slot %d holds %d, want %d — a boundary sample landed in the wrong bucket "+
				"(counts: %v)", i, got.Counts[i][0], want, got.Counts)
		}
	}
}

// Points outside the window are dropped rather than folded into an edge slot.
// Folding them would pile a neighbouring window's traffic onto the first or
// last column and read as a spike.
func TestAssembleHistogramDropsOutOfWindowPoints(t *testing.T) {
	from := time.Unix(1_700_000_000, 0)
	to := from.Add(60 * time.Second)
	t0 := float64(from.Unix())

	got, err := assembleHistogram([]promapi.Series{
		vmLESeries("0.1",
			[2]float64{t0 - 600, 999}, // before
			[2]float64{t0, 5},
			[2]float64{t0 + 600, 999}), // after
		vmLESeries("+Inf", [2]float64{t0, 5}),
	}, "m_bucket", from, to, 60)
	if err != nil {
		t.Fatal(err)
	}
	if got.Counts[0][0] != 5 {
		t.Fatalf("out-of-window points leaked into the grid: %v", got.Counts)
	}
}

// A bucket cardinality this high is refused rather than truncated: dropping
// high buckets moves the tail off the chart and pulls every percentile down
// with it, which is the one thing that cannot be done quietly here.
func TestAssembleHistogramRefusesUnboundedBucketCardinality(t *testing.T) {
	from := time.Unix(1_700_000_000, 0)
	series := make([]promapi.Series, maxHistogramLEBuckets+1)
	for i := range series {
		series[i] = vmLESeries(strconv.Itoa(i), [2]float64{float64(from.Unix()), 1})
	}
	_, err := assembleHistogram(series, "m_bucket", from, from.Add(time.Hour), 60)
	if err == nil {
		t.Fatal("want a refusal past the bucket cap")
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("refusal not tagged ErrUnsupported: %v", err)
	}
}

// ── labelSetGroupKey (the raw-query proxy's series identity) ─────────────────

func TestLabelSetGroupKey(t *testing.T) {
	// Sorted, so a poll cannot relabel and recolour every line: Go randomises
	// map iteration per range, and the frontend derives colour, legend text
	// and compare-ghost matching from this tuple.
	got := labelSetGroupKey(map[string]string{
		"pod": "api-1", "__name__": "http_requests_total", "status": "200",
	})
	want := []string{`__name__="http_requests_total"`, `pod="api-1"`, `status="200"`}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	// Ten shuffles of the same map must produce the identical tuple.
	for i := 0; i < 10; i++ {
		again := labelSetGroupKey(map[string]string{
			"status": "200", "pod": "api-1", "__name__": "http_requests_total",
		})
		for j := range want {
			if again[j] != want[j] {
				t.Fatalf("iteration %d produced %v, want %v — map order leaked into the identity", i, again, want)
			}
		}
	}
	// No labels → nil, matching the CH evaluator's shape for an ungrouped
	// result (scalarSeries: GroupKey nil).
	if labelSetGroupKey(nil) != nil || labelSetGroupKey(map[string]string{}) != nil {
		t.Fatal("an empty label set must yield a nil GroupKey")
	}
	// A value containing the frontend's tuple separator must not be able to
	// forge an extra dimension — quoting is what stops that.
	if k := labelSetGroupKey(map[string]string{"a": "x|y"}); k[0] != `a="x|y"` {
		t.Fatalf("unexpected element %q", k[0])
	}
}

// ── Wire contract ───────────────────────────────────────────────────────────

// The heatmap's query actually leaves the process, with the step it computed.
func TestQueryMetricHistogramWireContract(t *testing.T) {
	var gotQuery, gotStep, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("query")
		gotStep = r.URL.Query().Get("step")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"le":"0.1"},"values":[[1700000000,"10"]]},
			{"metric":{"le":"+Inf"},"values":[[1700000000,"12"]]}
		]}}`))
	}))
	defer srv.Close()

	s := New()
	s.Configure(Settings{BaseURL: srv.URL})
	from := time.Unix(1_700_000_000, 0)

	out, err := s.QueryMetricHistogram(context.Background(), chstore.MetricQueryFilter{
		Name: "http.server.request.duration", Service: "cart",
		From: from, To: from.Add(time.Hour), StepSeconds: 60,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/api/v1/query_range" {
		t.Fatalf("path = %q", gotPath)
	}
	want := `sum by (le) (increase({__name__="http.server.request.duration_bucket", service_name="cart"}[60s]))`
	if gotQuery != want {
		t.Fatalf("query =\n  %s\nwant\n  %s", gotQuery, want)
	}
	// The step param must be the step the expression's window names, or the
	// buckets stop tiling.
	if gotStep != "60s" {
		t.Fatalf("step param = %q, want 60s", gotStep)
	}
	if !floatsEqual(out.Bounds, []float64{0.1}) {
		t.Fatalf("bounds = %v", out.Bounds)
	}
	if out.Counts[0][0] != 10 || out.Counts[0][1] != 2 {
		t.Fatalf("counts = %v (le differencing did not run)", out.Counts[0])
	}
}

// VM's own error envelope surfaces VERBATIM: a 200 with status != success is
// an ERROR, not an empty heatmap. Treating it as empty is the "no data" lie.
func TestQueryMetricHistogramSurfacesVMErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"unknown label le"}`))
	}))
	defer srv.Close()

	s := New()
	s.Configure(Settings{BaseURL: srv.URL})
	from := time.Unix(1_700_000_000, 0)
	_, err := s.QueryMetricHistogram(context.Background(), chstore.MetricQueryFilter{
		Name: "m", From: from, To: from.Add(time.Hour),
	})
	if err == nil {
		t.Fatal("a status!=success envelope must be an error, not an empty heatmap")
	}
	if !strings.Contains(err.Error(), "unknown label le") {
		t.Fatalf("VM's own message was swallowed: %v", err)
	}
}

// THE RAW PROXY SENDS THE OPERATOR'S STRING UNTOUCHED.
//
// This is the property Faz 2 exists to provide on this endpoint. Every query
// below is valid MetricsQL that Coremetry's own PromQL parser REJECTS, so if
// any pre-validation crept back onto this path the test fails — and it fails
// the way an operator would experience it: a query that works in vmui does
// not work through Coremetry.
func TestQueryPromQLRangeForwardsMetricsQLVerbatim(t *testing.T) {
	queries := []string{
		`WITH (cpu = rate(node_cpu_seconds_total[5m])) sum(cpu)`,
		`sum(rate(m[5m:1m]))`,
		`rollup_rate(m[5m])`,
		`sum(m) keep_metric_names`,
		`quantile_over_time(0.9, m[1h])`,
		`m offset -1h`,
	}
	for _, q := range queries {
		t.Run(q, func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Query().Get("query")
				_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
			}))
			defer srv.Close()

			s := New()
			s.Configure(Settings{BaseURL: srv.URL})
			from := time.Unix(1_700_000_000, 0)
			if _, err := s.QueryPromQLRange(context.Background(), q, from, from.Add(time.Hour), 60, 0); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != q {
				t.Fatalf("query was rewritten:\n  sent %s\n  want %s", got, q)
			}
		})
	}
}

// The proxy's response mapping: labels become the series identity, non-finite
// samples are dropped as POINTS (a gap is a gap), and timestamps land in nanos.
func TestQueryPromQLRangeShapeMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"__name__":"m","pod":"a"},"values":[[1700000000,"1.5"],[1700000060,"NaN"],[1700000120,"2.5"]]},
			{"metric":{},"values":[[1700000000,"9"]]}
		]}}`))
	}))
	defer srv.Close()

	s := New()
	s.Configure(Settings{BaseURL: srv.URL})
	from := time.Unix(1_700_000_000, 0)
	out, err := s.QueryPromQLRange(context.Background(), "m", from, from.Add(time.Hour), 60, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d series, want 2", len(out))
	}
	if want := []string{`__name__="m"`, `pod="a"`}; len(out[0].GroupKey) != 2 ||
		out[0].GroupKey[0] != want[0] || out[0].GroupKey[1] != want[1] {
		t.Fatalf("GroupKey = %v, want %v", out[0].GroupKey, want)
	}
	// NaN dropped the POINT, kept the SERIES: 3 samples in, 2 points out.
	if len(out[0].Points) != 2 {
		t.Fatalf("points = %+v, want the NaN dropped and the series kept", out[0].Points)
	}
	if out[0].Points[0].Time != from.UnixNano() {
		t.Fatalf("time = %d, want %d (unix nanos)", out[0].Points[0].Time, from.UnixNano())
	}
	if out[0].Points[1].Value != 2.5 {
		t.Fatalf("value = %v", out[0].Points[1].Value)
	}
	// A labelless series is still a series, with a nil identity.
	if out[1].GroupKey != nil {
		t.Fatalf("labelless series got GroupKey %v", out[1].GroupKey)
	}
}

// Both Faz 2 reads must refuse an unconfigured backend rather than dial
// nowhere — and refuse it the way trial mode expects, on the URL rather than
// on Enabled.
func TestFaz2ReadsRefuseUnconfigured(t *testing.T) {
	s := New()
	ctx := context.Background()
	from := time.Unix(1_700_000_000, 0)

	if _, err := s.QueryMetricHistogram(ctx, chstore.MetricQueryFilter{Name: "m"}); err == nil {
		t.Error("QueryMetricHistogram answered with no base URL")
	}
	if _, err := s.QueryPromQLRange(ctx, "m", from, from.Add(time.Hour), 60, 0); err == nil {
		t.Error("QueryPromQLRange answered with no base URL")
	}
	// Disabled WITH a URL must still attempt the call — trial mode.
	s.Configure(Settings{BaseURL: "http://127.0.0.1:1"})
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := s.QueryMetricHistogram(ctx, chstore.MetricQueryFilter{
		Name: "m", From: from, To: from.Add(time.Hour),
	}); err == nil || strings.Contains(err.Error(), "not configured") {
		t.Errorf("histogram short-circuited on configuration instead of dialling: %v", err)
	}
}

// The empty-percentile note travels through the READ, not just the formatter.
func TestQueryMetricNotedAttachesThePercentileNote(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	defer srv.Close()

	s := New()
	s.Configure(Settings{BaseURL: srv.URL})
	from := time.Unix(1_700_000_000, 0)
	ctx := context.Background()

	// A percentile with no bucket series → note.
	_, note, err := s.QueryMetricNoted(ctx, chstore.MetricQueryFilter{
		Name: "jvm.memory.used", Aggregation: "p99", From: from, To: from.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if note == "" {
		t.Fatal("an empty percentile carries no note — the operator gets a silent blank chart")
	}
	if !strings.Contains(note, "jvm.memory.used_bucket") {
		t.Fatalf("note does not name the series queried: %s", note)
	}

	// A non-percentile empty result carries NO note. Scoped that tightly on
	// purpose: an empty gauge query is honestly empty and we know nothing the
	// operator does not, so a note there would be noise on every quiet metric.
	for _, agg := range []string{"", "avg", "sum", "last", "rate"} {
		_, note, err := s.QueryMetricNoted(ctx, chstore.MetricQueryFilter{
			Name: "jvm.memory.used", Aggregation: agg, From: from, To: from.Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		if note != "" {
			t.Fatalf("agg=%q attached a note to an ordinary empty result: %s", agg, note)
		}
	}

	// And the plain QueryMetric wrapper still satisfies the seam signature,
	// dropping the note without changing the series.
	series, err := s.QueryMetric(ctx, chstore.MetricQueryFilter{
		Name: "jvm.memory.used", Aggregation: "p99", From: from, To: from.Add(time.Hour),
	})
	if err != nil || len(series) != 0 {
		t.Fatalf("QueryMetric = %v, %v", series, err)
	}
}

// A percentile that DOES find buckets returns series and no note — the
// positive half, so "always attach a note" cannot creep in.
func TestQueryMetricNotedSilentWhenBucketsExist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("query"), "histogram_quantile") {
			t.Errorf("percentile did not translate to histogram_quantile: %s", r.URL.Query().Get("query"))
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"pod":"a"},"values":[[1700000000,"0.42"]]}
		]}}`))
	}))
	defer srv.Close()

	s := New()
	s.Configure(Settings{BaseURL: srv.URL})
	from := time.Unix(1_700_000_000, 0)
	series, note, err := s.QueryMetricNoted(context.Background(), chstore.MetricQueryFilter{
		Name: "http.server.request.duration", Aggregation: "p99",
		GroupBy: []string{"pod"}, From: from, To: from.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if note != "" {
		t.Fatalf("a populated percentile carries a note: %s", note)
	}
	if len(series) != 1 || len(series[0].GroupKey) != 1 || series[0].GroupKey[0] != "a" {
		t.Fatalf("series = %+v — the group-by tuple must survive histogram_quantile", series)
	}
	if series[0].Points[0].Value != 0.42 {
		t.Fatalf("value = %v", series[0].Points[0].Value)
	}
}

// ── fixtures ────────────────────────────────────────────────────────────────

// vmLESeries builds one `le`-labelled query_range series. Values are encoded
// the way Prometheus actually sends them — timestamp as a NUMBER, value as a
// STRING — because a struct with float64 for the value silently fails to
// unmarshal, which is one of the decoding rules promapi exists to hold.
func vmLESeries(le string, pts ...[2]float64) promapi.Series {
	s := promapi.Series{Metric: map[string]string{labelLE: le}}
	for _, p := range pts {
		s.Values = append(s.Values, []json.RawMessage{
			json.RawMessage(strconv.FormatFloat(p[0], 'f', -1, 64)),
			json.RawMessage(`"` + strconv.FormatFloat(p[1], 'f', -1, 64) + `"`),
		})
	}
	return s
}

func floatsEqual(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func intsEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func int64sEqual(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
