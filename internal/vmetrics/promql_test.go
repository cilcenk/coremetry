package vmetrics

// v0.9.1150 — VictoriaMetrics read backend, Faz 1.
// v0.9.1154 — Faz 1.5: last / rate / increase translation + the rollup
// lookbehind rules.
//
// The MetricsQL translation is the whole correctness surface of this
// backend: everything past it is HTTP. The aggregation × group-by matrix
// is exhaustive on purpose (the value+unit lesson from v0.6.36 applied to
// a different two-axis template: one untested combination is a silently
// wrong chart, not a crash). Faz 1.5 adds a THIRD axis with the same
// lesson attached — the lookbehind window — and the three rollups
// deliberately do not share one rule, so every (rollup, step, rateWindow)
// branch is exercised rather than sampled.
//
// Three properties are pinned harder than the rest because breaking any of
// them produces a WRONG NUMBER rather than an error:
//
//   - an inexpressible filter must ERROR, never vanish (v0.9.566: a
//     dropped jvm.memory.type="heap" charted heap+non-heap AS "heap"),
//   - GroupKey order must follow the REQUESTED order, not VM's map
//     iteration, or series get relabelled between polls,
//   - the window written INTO the expression must be the one derived from
//     the step the client sends (rule 3 in promql.go): a window resolved
//     from a different step is a rate over a period nothing on screen
//     names.

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestPromAggregator(t *testing.T) {
	ok := map[string]promAgg{
		"":         {Op: "avg"},
		"avg":      {Op: "avg"},
		"AVG":      {Op: "avg"},
		" avg ":    {Op: "avg"},
		"sum":      {Op: "sum"},
		"min":      {Op: "min"},
		"max":      {Op: "max"},
		"count":    {Op: "count"},
		"last":     {Op: "max", Rollup: "last_over_time"},
		"LAST":     {Op: "max", Rollup: "last_over_time"},
		" last ":   {Op: "max", Rollup: "last_over_time"},
		"rate":     {Op: "sum", Rollup: "rate"},
		"Rate":     {Op: "sum", Rollup: "rate"},
		"increase": {Op: "sum", Rollup: "increase"},
		// v0.9.1157 (Faz 2) — the percentiles translate. The rollup is rate
		// and the set-aggregation is sum, because histogram_quantile reads
		// `sum by (le, vmrange) (rate(<bucket>[W]))`; the φ rides in Quantile and is
		// what makes the shape a quantile rather than a plain sum.
		"p50":   {Op: "sum", Rollup: "rate", Quantile: 0.50},
		"p95":   {Op: "sum", Rollup: "rate", Quantile: 0.95},
		"p99":   {Op: "sum", Rollup: "rate", Quantile: 0.99},
		"P99":   {Op: "sum", Rollup: "rate", Quantile: 0.99},
		" p95 ": {Op: "sum", Rollup: "rate", Quantile: 0.95},
	}
	for in, want := range ok {
		got, err := promAggregator(in)
		if err != nil {
			t.Fatalf("promAggregator(%q): unexpected error %v", in, err)
		}
		if got != want {
			t.Fatalf("promAggregator(%q) = %+v, want %+v", in, got, want)
		}
	}
	// Refused: aggregations Coremetry does not have. p90 is the interesting
	// one — it LOOKS like the three above and is refused anyway, because
	// MetricQueryFilter.Aggregation cannot carry it and the ClickHouse
	// sibling does not implement it. Translating a percentile the builder
	// cannot produce would make VM answer a query the other backend
	// silently could not.
	for _, in := range []string{"p90", "p999", "median", "quantile", "nonsense"} {
		_, err := promAggregator(in)
		if err == nil {
			t.Fatalf("promAggregator(%q): want error, got nil", in)
		}
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("promAggregator(%q) refusal not tagged ErrUnsupported — the API would 502 and "+
				"blame a healthy VictoriaMetrics: %v", in, err)
		}
		// Every refusal names the supported set, so the operator can fix
		// the query without reading the source.
		if !strings.Contains(err.Error(), promSupportedAggs) {
			t.Fatalf("promAggregator(%q) message does not list the supported set: %v", in, err)
		}
	}
}

// ── Percentiles (v0.9.1157, Faz 2) ─────────────────────────────────────────

// The φ × group-by × name-suffix matrix, pinned as LITERAL expressions.
//
// Exhaustive rather than sampled for the reason this file's header gives
// about the aggregation matrix (the v0.6.36 two-axis lesson): one untested
// combination here is a silently WRONG LATENCY NUMBER, which is the single
// figure an operator escalates on. Three things can go wrong quietly and each
// axis exists to catch one:
//
//	φ            — a mis-rendered 0.95 charts p50 under a "p95" legend.
//	`le` in by() — dropped, histogram_quantile has no distribution to read
//	               and returns NaN; present but LAST, and the expression
//	               drifts from the documented idiom for no reason.
//	_bucket      — appended twice matches nothing (empty chart); not appended
//	               at all queries the sum/count series and yields a number
//	               that is not a percentile of anything.
func TestBuildPromQLPercentileShapes(t *testing.T) {
	// Window floor: no explicit step → promStep's 300 buckets over the
	// default 24h window is 288s, which promRollupWindow floors to 300 for
	// rate. Literal, not derived — a test that asked the implementation what
	// the window should be would pass while both were wrong.
	const w = "300s"
	tests := []struct {
		name string
		f    chstore.MetricQueryFilter
		want string
	}{
		{
			name: "p99, no group-by — le is STILL the by-clause",
			f:    chstore.MetricQueryFilter{Name: "http.server.request.duration", Aggregation: "p99"},
			want: `histogram_quantile(0.99, sum by (le, vmrange) (rate({` + mcHTTPDurBucket + `}[` + w + `])))`,
		},
		{
			name: "p50 renders φ as 0.5, not 0.500000",
			f:    chstore.MetricQueryFilter{Name: "m", Aggregation: "p50"},
			want: `histogram_quantile(0.5, sum by (le, vmrange) (rate({` + mcMBucket + `}[` + w + `])))`,
		},
		{
			name: "p95",
			f:    chstore.MetricQueryFilter{Name: "m", Aggregation: "p95"},
			want: `histogram_quantile(0.95, sum by (le, vmrange) (rate({` + mcMBucket + `}[` + w + `])))`,
		},
		{
			name: "group-by joins le, le FIRST",
			f: chstore.MetricQueryFilter{
				Name: "m", Aggregation: "p99", GroupBy: []string{"pod"},
			},
			want: `histogram_quantile(0.99, sum by (le, vmrange, pod) (rate({` + mcMBucket + `}[` + w + `])))`,
		},
		{
			name: "two group-by keys, dotted one sanitized",
			f: chstore.MetricQueryFilter{
				Name: "m", Aggregation: "p95", GroupBy: []string{"pod", "host.name"},
			},
			want: `histogram_quantile(0.95, sum by (le, vmrange, pod, host_name) (rate({` + mcMBucket + `}[` + w + `])))`,
		},
		{
			name: "an explicit le group-by is deduped, not printed twice",
			f: chstore.MetricQueryFilter{
				Name: "m", Aggregation: "p99", GroupBy: []string{"le", "pod"},
			},
			want: `histogram_quantile(0.99, sum by (le, vmrange, pod) (rate({` + mcMBucket + `}[` + w + `])))`,
		},
		{
			name: "already-suffixed name is NOT double-suffixed",
			f: chstore.MetricQueryFilter{
				Name: "http_server_request_duration_bucket", Aggregation: "p99",
			},
			want: `histogram_quantile(0.99, sum by (le, vmrange) (rate({__name__="http_server_request_duration_bucket"}[` + w + `])))`,
		},
		{
			name: "_sum is left alone — the suffix rule never GUESSES a sibling",
			f:    chstore.MetricQueryFilter{Name: "m_sum", Aggregation: "p99"},
			want: `histogram_quantile(0.99, sum by (le, vmrange) (rate({__name__="m_sum_bucket"}[` + w + `])))`,
		},
		{
			name: "service + filters land on the BUCKET selector",
			f: chstore.MetricQueryFilter{
				Name: "m", Aggregation: "p99", Service: "cart",
				Filters: []chstore.FilterExpr{{Key: "http.route", Op: "=", Values: []string{"/api"}}},
			},
			want: `histogram_quantile(0.99, sum by (le, vmrange) (rate({` + mcMBucket + `, service_name="cart", http_route="/api"}[` + w + `])))`,
		},
		{
			name: "explicit step drives the rate window, unfloored when > floor",
			f: chstore.MetricQueryFilter{
				Name: "m", Aggregation: "p99", StepSeconds: 600,
			},
			want: `histogram_quantile(0.99, sum by (le, vmrange) (rate({` + mcMBucket + `}[600s])))`,
		},
		{
			name: "an explicit rate window wins over the floor",
			f: chstore.MetricQueryFilter{
				Name: "m", Aggregation: "p99", StepSeconds: 60, RateWindowSec: 180,
			},
			want: `histogram_quantile(0.99, sum by (le, vmrange) (rate({` + mcMBucket + `}[180s])))`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildTranslate(tc.f)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

// The former refusal must be GONE from the operator-facing text too. A
// translation that works while the message still says "Faz 2" would send an
// operator whose percentile now charts back to ClickHouse for nothing.
func TestPercentileNoLongerDeferredToFaz2(t *testing.T) {
	if strings.Contains(promSupportedAggs, "p99") == false {
		t.Fatal("promSupportedAggs does not list p99 — every refusal message prints this string, " +
			"so the percentiles being absent from it advertises Faz 2 as unshipped")
	}
	_, err := promAggregator("nonsense")
	if err == nil {
		t.Fatal("want a refusal")
	}
	if strings.Contains(err.Error(), "Faz 2") {
		t.Fatalf("a refusal still defers to Faz 2, which shipped in v0.9.1157: %v", err)
	}
	// And the percentiles no longer error at all.
	for _, agg := range []string{"p50", "p95", "p99"} {
		if _, err := promAggregator(agg); err != nil {
			t.Fatalf("agg=%s still refused: %v", agg, err)
		}
	}
	_ = errors.Is // keep the import meaningful if the block above changes
}

// bucketMetricName is the naming RULE, tested apart from the expressions
// because it is the piece an operator meets as "empty chart" when it is
// wrong — never as an error.
func TestBucketMetricName(t *testing.T) {
	cases := map[string]string{
		// Dots kept: rule 1 — the name is not translated, only suffixed. VM
		// holds whichever spelling the write path produced, and the suffix is
		// appended after any sanitisation on that side too.
		"http.server.request.duration": "http.server.request.duration_bucket",
		"http_server_request_duration": "http_server_request_duration_bucket",
		// Idempotent: the VM catalogue lists raw series names, so
		// `…_bucket` is a row an operator can click.
		"m_bucket": "m_bucket",
		// Siblings are NOT rewritten — a guess that silently answers a
		// different question is worse than an empty result with a note.
		"m_sum":   "m_sum_bucket",
		"m_count": "m_count_bucket",
		// Whitespace trimmed; empty stays empty so the caller's own
		// "name required" check is the one that fires.
		"  m  ": "m_bucket",
		"":      "",
		"   ":   "",
	}
	for in, want := range cases {
		if got := bucketMetricName(in); got != want {
			t.Errorf("bucketMetricName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The empty-percentile note has one job: turn a blank chart into a diagnosis.
// It must name the series that were actually queried, because those names are
// the one thing the operator cannot see anywhere on screen.
//
// v0.9.1159 — it lists EVERY candidate. Once the selector became an
// alternation, naming one spelling stopped being the whole truth, and the
// list is also the only on-screen evidence that the Prometheus-unit
// spellings were tried at all: without it, an operator staring at an empty
// p99 has no way to tell this release from the one before it.
func TestEmptyBucketNote(t *testing.T) {
	cands := nameCandidates("http.server.request.duration", "p99")
	note := emptyBucketNote(cands)
	for _, want := range []string{
		"http.server.request.duration_bucket",         // the verbatim spelling
		"http_server_request_duration_seconds_bucket", // the OTel-Prometheus one
		"_bucket", // what to look for in VM
		"le",      // and how a bucket series is shaped
	} {
		if !strings.Contains(note, want) {
			t.Fatalf("note does not mention %q: %s", want, note)
		}
	}
	// EVERY candidate, not a sample: an operator who sees four of six tried
	// spellings cannot tell whether the two missing ones were skipped or just
	// unprinted, and the whole point of the note is to end that guessing.
	for _, c := range cands {
		if !strings.Contains(note, c) {
			t.Fatalf("note omits candidate %q: %s", c, note)
		}
	}
	// The suffix rule is shared with the query, so an already-suffixed name
	// must not be doubled in the note either — an operator told to look for
	// `x_bucket_bucket` would conclude their write path is broken.
	if n := emptyBucketNote(nameCandidates("x_bucket", "p99")); strings.Contains(n, "x_bucket_bucket") {
		t.Fatalf("note double-suffixed the name: %s", n)
	}
}

// Full aggregation × group-by matrix.
//
// v0.9.1160 — avg (and its `""` spelling) left this shape: it now carries the
// observation-weighted histogram arm. min/max/sum/count are UNCHANGED and that
// is deliberate, not an oversight — `min(…_seconds_count)` would report a
// sample count under a latency legend (v0.9.566). The `mean` column below is
// what encodes the split, so a change that gave every aggregation a histogram
// arm fails on four rows at once.
func TestBuildPromQLAggregationGroupByMatrix(t *testing.T) {
	aggs := []struct {
		in, op string
		mean   bool // avg → base arm OR rate(_sum)/rate(_count)
	}{
		{in: "", op: "avg", mean: true}, {in: "avg", op: "avg", mean: true},
		{in: "sum", op: "sum"}, {in: "min", op: "min"},
		{in: "max", op: "max"}, {in: "count", op: "count"},
	}
	groupBys := []struct {
		name string
		in   []string
		by   string // "" = no by-clause
	}{
		{name: "no group-by", in: nil, by: ""},
		{name: "empty slice", in: []string{}, by: ""},
		{name: "single", in: []string{"pod"}, by: "pod"},
		{name: "two", in: []string{"pod", "host.name"}, by: "pod, host_name"},
		{name: "dotted key sanitized", in: []string{"jvm.memory.type"}, by: "jvm_memory_type"},
	}
	for _, a := range aggs {
		for _, g := range groupBys {
			t.Run(a.in+"/"+g.name, func(t *testing.T) {
				got, err := buildTranslate(chstore.MetricQueryFilter{
					Name:        "jvm.memory.used",
					Aggregation: a.in,
					GroupBy:     g.in,
				})
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				// The two by-clause spellings differ in more than the label
				// list: ungrouped is `avg(…)`, grouped is `avg by (pod) (…)`
				// with a space. Written out here rather than composed from the
				// implementation's helper, so a lost space is a failure.
				open := `(`
				if g.by != "" {
					open = ` by (` + g.by + `) (`
				}
				want := a.op + open + `{` + mcJVM + `})`
				if a.mean {
					// No From/To → promStep resolves 1s, which promRollupWindow
					// floors to the 300s rate floor. Literal, not derived.
					want += ` or (sum` + open + `rate({` + mcJVMSum + `}[300s]))` +
						` / sum` + open + `rate({` + mcJVMCount + `}[300s])))`
				}
				if got != want {
					t.Fatalf("got  %s\nwant %s", got, want)
				}
			})
		}
	}
}

// Faz 1.5 rollup shapes (v0.9.1154). Windows are pinned as LITERAL strings
// rather than derived from promRollupWindow — a test that asked the
// implementation what the window should be would pass while both were
// wrong. The interesting axes: which set-aggregation wraps which rollup,
// that the rollup wraps the WHOLE selector (matchers inside the brackets),
// and that the floor applies per rollup rather than uniformly.
func TestBuildPromQLRollupShapes(t *testing.T) {
	from := time.Unix(1700000000, 0)
	to := from.Add(time.Hour)
	mk := func(f chstore.MetricQueryFilter) chstore.MetricQueryFilter {
		f.From, f.To = from, to
		return f
	}
	tests := []struct {
		name string
		f    chstore.MetricQueryFilter
		want string
	}{
		{
			// last collapses with max() — identity when the grouping is
			// exact, one real member when it is not (see buildPromQL).
			name: "last, no group-by",
			f:    mk(chstore.MetricQueryFilter{Name: "jvm.memory.used", Aggregation: "last", StepSeconds: 60}),
			want: `max(last_over_time({` + mcJVM + `}[300s]))`,
		},
		{
			name: "last, one group-by",
			f: mk(chstore.MetricQueryFilter{Name: "jvm.memory.used", Aggregation: "last",
				StepSeconds: 60, GroupBy: []string{"host.name"}}),
			want: `max by (host_name) (last_over_time({` + mcJVM + `}[300s]))`,
		},
		{
			name: "last, two dotted group-bys, step above the floor",
			f: mk(chstore.MetricQueryFilter{Name: "jvm.memory.used", Aggregation: "last",
				StepSeconds: 900, GroupBy: []string{"host.name", "jvm.memory.pool.name"}}),
			want: `max by (host_name, jvm_memory_pool_name) (last_over_time({` + mcJVM + `}[900s]))`,
		},
		{
			// The rollup wraps the FULL selector: service + filters live
			// INSIDE the brackets. Outside, the range vector would be the
			// unfiltered metric and the filters would apply to the rollup's
			// result — silently the v0.9.566 shape again.
			name: "last with service + filter",
			f: mk(chstore.MetricQueryFilter{Name: "m", Aggregation: "last", Service: "api",
				StepSeconds: 60, GroupBy: []string{"pod"},
				Filters: []chstore.FilterExpr{{Key: "jvm.memory.type", Op: "=", Values: []string{"heap"}}}}),
			want: `max by (pod) (last_over_time({` + mcM + `, service_name="api", jvm_memory_type="heap"}[300s]))`,
		},
		{
			// RateWindowSec is a RATE window; it must not bend `last`.
			name: "last ignores RateWindowSec",
			f: mk(chstore.MetricQueryFilter{Name: "m", Aggregation: "last",
				StepSeconds: 60, RateWindowSec: 180}),
			want: `max(last_over_time({` + mcM + `}[300s]))`,
		},
		{
			// sum(rate(...)), not avg — the CH path re-aggregates per-series
			// rates with a sum and names this idiom as its semantics.
			name: "rate, no group-by, floored",
			f:    mk(chstore.MetricQueryFilter{Name: "http.server.requests", Aggregation: "rate", StepSeconds: 60}),
			want: `sum(rate({` + mcHTTPReq + `}[300s])) or sum(rate({` + mcHTTPReqCount + `}[300s]))`,
		},
		{
			name: "rate, group-by",
			f: mk(chstore.MetricQueryFilter{Name: "http.server.requests", Aggregation: "rate",
				StepSeconds: 60, GroupBy: []string{"http.route"}}),
			want: `sum by (http_route) (rate({` + mcHTTPReq + `}[300s])) or sum by (http_route) (rate({` + mcHTTPReqCount + `}[300s]))`,
		},
		{
			name: "rate, step above the floor keeps the step",
			f:    mk(chstore.MetricQueryFilter{Name: "m", Aggregation: "rate", StepSeconds: 600}),
			want: `sum(rate({` + mcM + `}[600s])) or sum(rate({` + mcMCount + `}[600s]))`,
		},
		{
			// The operator's Grafana reference is [3m]. An explicit window
			// wins and is NOT promoted to the 5m floor.
			name: "rate honours an explicit RateWindowSec below the floor",
			f:    mk(chstore.MetricQueryFilter{Name: "m", Aggregation: "rate", StepSeconds: 60, RateWindowSec: 180}),
			want: `sum(rate({` + mcM + `}[180s])) or sum(rate({` + mcMCount + `}[180s]))`,
		},
		{
			name: "rate honours an explicit RateWindowSec above the floor",
			f:    mk(chstore.MetricQueryFilter{Name: "m", Aggregation: "rate", StepSeconds: 600, RateWindowSec: 900}),
			want: `sum(rate({` + mcM + `}[900s])) or sum(rate({` + mcMCount + `}[900s]))`,
		},
		{
			// <= step means "unexpressed" (the CH sibling's own rule), so
			// the floor still applies.
			name: "rate ignores a RateWindowSec below the step",
			f:    mk(chstore.MetricQueryFilter{Name: "m", Aggregation: "rate", StepSeconds: 60, RateWindowSec: 30}),
			want: `sum(rate({` + mcM + `}[300s])) or sum(rate({` + mcMCount + `}[300s]))`,
		},
		{
			// increase is a window TOTAL — flooring it would quadruple the
			// number while the chart still says one bucket (v0.6.36 class).
			name: "increase is NOT floored",
			f:    mk(chstore.MetricQueryFilter{Name: "m", Aggregation: "increase", StepSeconds: 60}),
			want: `sum(increase({` + mcM + `}[60s])) or sum(increase({` + mcMCount + `}[60s]))`,
		},
		{
			name: "increase at a sub-minute step stays at the step",
			f:    mk(chstore.MetricQueryFilter{Name: "m", Aggregation: "increase", StepSeconds: 12}),
			want: `sum(increase({` + mcM + `}[12s])) or sum(increase({` + mcMCount + `}[12s]))`,
		},
		{
			name: "increase, group-by, explicit window",
			f: mk(chstore.MetricQueryFilter{Name: "m", Aggregation: "increase", StepSeconds: 60,
				RateWindowSec: 180, GroupBy: []string{"error.type"}}),
			want: `sum by (error_type) (increase({` + mcM + `}[180s])) or sum by (error_type) (increase({` + mcMCount + `}[180s]))`,
		},
		{
			name: "case and padding are normalized like the set-aggregations",
			f:    mk(chstore.MetricQueryFilter{Name: "m", Aggregation: " RATE ", StepSeconds: 600}),
			want: `sum(rate({` + mcM + `}[600s])) or sum(rate({` + mcMCount + `}[600s]))`,
		},
		{
			// A group-by that sanitizes to nothing falls back to the
			// ungrouped form — same rule the Faz 1 aggregations follow.
			name: "group-by that sanitizes away collapses to the ungrouped form",
			f: mk(chstore.MetricQueryFilter{Name: "m", Aggregation: "rate", StepSeconds: 600,
				GroupBy: []string{"  "}}),
			want: `sum(rate({` + mcM + `}[600s])) or sum(rate({` + mcMCount + `}[600s]))`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildTranslate(tc.f)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

// The Faz 1 set-aggregations must stay a BARE selector — no range vector
// crept in with the rollup rewrite. A stray window would change avg from
// "the value at this bucket" to "the average of the last 5 minutes" on
// every existing panel, with nothing on screen saying so.
// ── `or` composition (v0.9.1160) ───────────────────────────────────────────
//
// The two reported prod failures were both "200 with zero series" on an
// OTLP-named metric, because in VM an OTLP histogram has no base series. The fix
// composes a second arm with MetricsQL `or`, and the FULL expressions are pinned
// here — every other test in this file uses the constants, so this is the one
// place the whole shape is legible at once.
//
// Four properties, each of which fails silently if broken:
//
//	ARM ORDER      — `or` takes the LEFT arm per group. Base first means a real
//	                 counter or gauge always beats the guessed histogram arm;
//	                 swapped, a metric with both families would report its
//	                 histogram estimate and hide the exact series.
//	BOTH ARMS      — drop the right one and the reported bug is back; drop the
//	                 left and every plain gauge starts answering from a
//	                 nonexistent `_sum`/`_count` pair.
//	GROUPING       — the by-clause must be on EVERY arm. An arm that lost it
//	                 produces one ungrouped series, and `or` would fill that
//	                 single series into every group the other arm did not cover.
//	RATIO, NOT SUM — avg's histogram arm divides; a `+` there would report the
//	                 total observed value as a mean.
func TestOrCompositionShapes(t *testing.T) {
	from := time.Unix(1700000000, 0)
	to := from.Add(time.Hour)
	tests := []struct {
		name string
		f    chstore.MetricQueryFilter
		want string
	}{
		{
			// THE FIRST REPORTED CASE, ungrouped.
			name: "rate — base arm OR count arm",
			f: chstore.MetricQueryFilter{
				Name: "http.server.request.duration", Aggregation: "rate",
				From: from, To: to, StepSeconds: 600,
			},
			want: `sum(rate({` + mcHTTPDur + `}[600s]))` +
				` or sum(rate({` + mcHTTPDurCount + `}[600s]))`,
		},
		{
			name: "rate, grouped — the by-clause is on BOTH arms",
			f: chstore.MetricQueryFilter{
				Name: "http.server.request.duration", Aggregation: "rate",
				GroupBy: []string{"http.route"}, From: from, To: to, StepSeconds: 600,
			},
			want: `sum by (http_route) (rate({` + mcHTTPDur + `}[600s]))` +
				` or sum by (http_route) (rate({` + mcHTTPDurCount + `}[600s]))`,
		},
		{
			name: "increase — same composition, its own rollup",
			f: chstore.MetricQueryFilter{
				Name: "http.server.request.duration", Aggregation: "increase",
				From: from, To: to, StepSeconds: 600,
			},
			want: `sum(increase({` + mcHTTPDur + `}[600s]))` +
				` or sum(increase({` + mcHTTPDurCount + `}[600s]))`,
		},
		{
			name: "increase, grouped",
			f: chstore.MetricQueryFilter{
				Name: "http.server.request.duration", Aggregation: "increase",
				GroupBy: []string{"http.route"}, From: from, To: to, StepSeconds: 600,
			},
			want: `sum by (http_route) (increase({` + mcHTTPDur + `}[600s]))` +
				` or sum by (http_route) (increase({` + mcHTTPDurCount + `}[600s]))`,
		},
		{
			// THE SECOND REPORTED CASE — the service page's "Response time ·
			// avg (by route)" panel, verbatim. The left arm is a bare instant
			// selector (so a gauge behaves exactly as before v0.9.1160); the
			// right is the observation-weighted mean, which is the same
			// semantics the ClickHouse sibling computes (v0.9.776).
			name: "avg, grouped — gauge arm OR rate(_sum)/rate(_count)",
			f: chstore.MetricQueryFilter{
				Name: "http.server.request.duration", Aggregation: "avg",
				GroupBy: []string{"http.route"}, From: from, To: to, StepSeconds: 600,
			},
			want: `avg by (http_route) ({` + mcHTTPDur + `})` +
				` or (sum by (http_route) (rate({` + mcHTTPDurSum + `}[600s]))` +
				` / sum by (http_route) (rate({` + mcHTTPDurCount + `}[600s])))`,
		},
		{
			name: "avg, ungrouped",
			f: chstore.MetricQueryFilter{
				Name: "http.server.request.duration", Aggregation: "avg",
				From: from, To: to, StepSeconds: 600,
			},
			want: `avg({` + mcHTTPDur + `})` +
				` or (sum(rate({` + mcHTTPDurSum + `}[600s]))` +
				` / sum(rate({` + mcHTTPDurCount + `}[600s])))`,
		},
		{
			// An OMITTED aggregation is avg, and that is what the reported panel
			// sends. A gate keyed on the literal "avg" would leave the bug live
			// while every explicit-label test passed.
			name: "an empty aggregation composes the mean arms too",
			f: chstore.MetricQueryFilter{
				Name: "http.server.request.duration",
				From: from, To: to, StepSeconds: 600,
			},
			want: `avg({` + mcHTTPDur + `})` +
				` or (sum(rate({` + mcHTTPDurSum + `}[600s]))` +
				` / sum(rate({` + mcHTTPDurCount + `}[600s])))`,
		},
		{
			// Service + filters ride on EVERY arm. An arm that lost the filter
			// would answer about one route with data from all of them, and `or`
			// would let it fill in wherever the filtered arm was empty — the
			// v0.9.566 shape with a fallback attached.
			name: "service and filters are repeated on every arm",
			f: chstore.MetricQueryFilter{
				Name: "http.server.request.duration", Aggregation: "rate", Service: "cart",
				Filters: []chstore.FilterExpr{{Key: "http.route", Op: "=", Values: []string{"/api"}}},
				From:    from, To: to, StepSeconds: 600,
			},
			want: `sum(rate({` + mcHTTPDur + `, service_name="cart", http_route="/api"}[600s]))` +
				` or sum(rate({` + mcHTTPDurCount + `, service_name="cart", http_route="/api"}[600s]))`,
		},

		// ── SINGLE-ARM REGRESSIONS: byte-identical to pre-v0.9.1160 ─────────
		{
			// A `_total` name is a monotonic SUM: no histogram siblings exist,
			// so no arm is composed and the expression is exactly what shipped
			// before. Without this gate every `avg(x_total)` panel would grow a
			// ratio over two names nothing emits.
			name: "avg on a _total counter stays ONE arm",
			f: chstore.MetricQueryFilter{
				Name: "http_requests_total", From: from, To: to, StepSeconds: 600,
			},
			want: `avg({__name__="http_requests_total"})`,
		},
		{
			name: "rate on a _total counter stays ONE arm",
			f: chstore.MetricQueryFilter{
				Name: "http_requests_total", Aggregation: "rate",
				From: from, To: to, StepSeconds: 600,
			},
			want: `sum(rate({__name__="http_requests_total"}[600s]))`,
		},
		{
			// An explicitly-picked histogram part: the operator chose this row
			// off VM's catalogue, so guessing siblings for it is the refusal
			// bucketMetricName documents.
			name: "avg on a _count row the operator picked stays ONE arm",
			f: chstore.MetricQueryFilter{
				Name: "http_server_request_duration_seconds_count",
				From: from, To: to, StepSeconds: 600,
			},
			want: `avg({__name__="http_server_request_duration_seconds_count"})`,
		},
		{
			// min/max/sum/count/last NEVER compose an arm, even on a name whose
			// histogram parts exist. `min(…_seconds_count)` reports a sample
			// count under a latency legend (v0.9.566).
			name: "min composes no arm on a histogram-shaped name",
			f: chstore.MetricQueryFilter{
				Name: "http_server_request_duration_seconds", Aggregation: "min",
				From: from, To: to, StepSeconds: 600,
			},
			want: `min({__name__="http_server_request_duration_seconds"})`,
		},
		{
			name: "last composes no arm either",
			f: chstore.MetricQueryFilter{
				Name: "http_server_request_duration_seconds", Aggregation: "last",
				From: from, To: to, StepSeconds: 600,
			},
			want: `max(last_over_time({__name__="http_server_request_duration_seconds"}[600s]))`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildTranslate(tc.f)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

// A set-aggregation reads an INSTANT vector: no `[Ws]` window anywhere.
//
// v0.9.1160 — avg is the exception now, and only in its histogram arm. Its LEFT
// arm is still a bare instant selector (that is what makes a gauge behave
// exactly as before), while the right arm is a ratio of two rates and therefore
// windowed by construction. The split is asserted rather than the window being
// merely tolerated: an avg whose left arm grew a window would silently turn
// every gauge panel into a 5-minute smoothing.
func TestSetAggregationsCarryNoWindow(t *testing.T) {
	for _, agg := range []string{"", "avg", "sum", "min", "max", "count"} {
		got, err := buildTranslate(chstore.MetricQueryFilter{
			Name: "m", Aggregation: agg, StepSeconds: 60,
			From: time.Unix(1700000000, 0), To: time.Unix(1700003600, 0),
		})
		if err != nil {
			t.Fatalf("agg=%q: %v", agg, err)
		}
		left := got
		if i := strings.Index(got, " or "); i >= 0 {
			left = got[:i]
		}
		if strings.Contains(left, "[") {
			t.Fatalf("agg=%q rendered a range vector in its BASE arm: %s", agg, got)
		}
		if isMeanAgg(agg) {
			// And the histogram arm must be there, windowed. Without it the
			// reported avg-by-route panel stays empty.
			if !strings.Contains(got, " or (sum") || !strings.Contains(got, "[300s])") {
				t.Fatalf("agg=%q lost its windowed histogram arm: %s", agg, got)
			}
		} else if strings.Contains(got, " or ") {
			t.Fatalf("agg=%q grew an `or` arm — only avg/rate/increase may: %s", agg, got)
		}
	}
}

// A refused FILTER must still fail a rollup-shaped query. The rollup path
// is a second code path through the same selector, which is exactly how the
// v0.9.566 class comes back.
func TestRollupStillRefusesAnInexpressibleFilter(t *testing.T) {
	for _, agg := range []string{"last", "rate", "increase"} {
		_, err := buildTranslate(chstore.MetricQueryFilter{
			Name: "m", Aggregation: agg, StepSeconds: 60,
			Filters: []chstore.FilterExpr{{Key: "n", Op: ">", Values: []string{"5"}}},
		})
		if err == nil {
			t.Fatalf("agg=%s: an inexpressible filter was silently dropped", agg)
		}
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("agg=%s: filter refusal not tagged ErrUnsupported: %v", agg, err)
		}
	}
}

// promRollupWindow — the whole (rollup × step × caller window) matrix. The
// three rollups do not share a rule, so a change that "simplifies" them
// into one has to break a row here.
func TestPromRollupWindow(t *testing.T) {
	tests := []struct {
		rollup  string
		step    int
		rateWin int
		want    int
		why     string
	}{
		// rate: floored, honours an explicit window.
		{rollupRate, 12, 0, 300, "sub-export step widened to the staleness floor"},
		{rollupRate, 60, 0, 300, "still under the floor"},
		{rollupRate, 300, 0, 300, "exactly the floor"},
		{rollupRate, 600, 0, 600, "above the floor → the step wins"},
		{rollupRate, 60, 180, 180, "explicit window wins, unfloored"},
		{rollupRate, 60, 30, 300, "window <= step is unexpressed → floor"},
		{rollupRate, 60, 60, 300, "window == step is unexpressed → floor"},
		{rollupRate, 600, 900, 900, "explicit window above the step"},
		{rollupRate, 0, 0, 300, "degenerate step still legal"},
		{rollupRate, -5, 0, 300, "negative step still legal"},

		// last_over_time: floored, but RateWindowSec never reaches it.
		{rollupLast, 12, 0, 300, "gauge chart stays dense at a sub-export step"},
		{rollupLast, 300, 0, 300, "exactly the floor"},
		{rollupLast, 900, 0, 900, "wide step → bucket-last, like CH's argMax"},
		{rollupLast, 60, 180, 300, "RateWindowSec is a RATE window, not a lookback"},
		{rollupLast, 900, 3600, 900, "…even when it is wider than the step"},
		{rollupLast, 0, 0, 300, "degenerate step still legal"},

		// increase: never floored — the window IS the reported quantity.
		{rollupIncrease, 12, 0, 12, "flooring would multiply the number by 25"},
		{rollupIncrease, 60, 0, 60, "one bucket"},
		{rollupIncrease, 600, 0, 600, "one bucket"},
		{rollupIncrease, 60, 180, 180, "explicit window wins"},
		{rollupIncrease, 60, 30, 60, "window <= step is unexpressed"},
		{rollupIncrease, 0, 0, 1, "degenerate step floors at 1s, not at 300s"},
	}
	for _, tc := range tests {
		name := tc.rollup + "/step=" + strconv.Itoa(tc.step) + "/win=" + strconv.Itoa(tc.rateWin)
		t.Run(name, func(t *testing.T) {
			got := promRollupWindow(tc.rollup, tc.step, tc.rateWin, 0)
			if got != tc.want {
				t.Fatalf("promRollupWindow(%q, step=%d, win=%d) = %d, want %d (%s)",
					tc.rollup, tc.step, tc.rateWin, got, tc.want, tc.why)
			}
			if got < 1 {
				t.Fatalf("window must never be < 1s ([0s] is a VM error), got %d", got)
			}
		})
	}
}

var promWindowRe = regexp.MustCompile(`\[(\d+)s\]`)

// Rule 3: the window in the EXPRESSION is derived from the same resolved
// step the client sends as query_range's `step` param. The step the caller
// ASKED for is not that number — promStep widens it past the points ceiling
// and invents it entirely when the caller sent none — so a translation that
// read f.StepSeconds directly would render a rate over a period nothing on
// screen names, and only on wide windows.
func TestRollupWindowFollowsTheResolvedStep(t *testing.T) {
	from := time.Unix(1700000000, 0)
	tests := []struct {
		name     string
		rangeSec int
		agg      string
		rollup   string
		step     int
		maxDP    int
		rateWin  int
		want     int
	}{
		{name: "auto step, 1h window", rangeSec: 3600, agg: "increase", rollup: rollupIncrease, want: 12},
		{name: "pixel-adaptive step", rangeSec: 3600, agg: "increase", rollup: rollupIncrease, maxDP: 400, want: 9},
		{
			// 30d at step=10 is 259200 points; promStep widens to 236. The
			// window must follow the WIDENED step.
			name: "step widened past the points ceiling", rangeSec: 30 * 24 * 3600,
			agg: "increase", rollup: rollupIncrease, step: 10, want: 236,
		},
		{
			// Same request as a rate: 236 is still under the floor.
			name: "widened step then floored", rangeSec: 30 * 24 * 3600,
			agg: "rate", rollup: rollupRate, step: 10, want: 300,
		},
		{
			name: "wide auto step needs no floor", rangeSec: 30 * 24 * 3600,
			agg: "last", rollup: rollupLast, want: 8640,
		},
		{
			name: "explicit caller window on a widened step", rangeSec: 30 * 24 * 3600,
			agg: "rate", rollup: rollupRate, step: 10, rateWin: 3600, want: 3600,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			to := from.Add(time.Duration(tc.rangeSec) * time.Second)
			f := chstore.MetricQueryFilter{
				Name: "m", Aggregation: tc.agg, From: from, To: to,
				StepSeconds: tc.step, MaxDataPoints: tc.maxDP, RateWindowSec: tc.rateWin,
			}
			expr, err := buildTranslate(f)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			m := promWindowRe.FindStringSubmatch(expr)
			if m == nil {
				t.Fatalf("no [Ns] window in %s", expr)
			}
			got, _ := strconv.Atoi(m[1])
			if got != tc.want {
				t.Fatalf("window = %ds, want %ds (expr %s)", got, tc.want, expr)
			}
			// And the same number the client would derive from the step it
			// actually sends — the two must not be able to disagree.
			step := promStep(f.From, f.To, f.StepSeconds, f.MaxDataPoints)
			if want := promRollupWindow(tc.rollup, step, tc.rateWin, 0); got != want {
				t.Fatalf("window %ds drifted from promRollupWindow(step=%d) = %ds",
					got, step, want)
			}
		})
	}
}

func TestBuildPromQLSelector(t *testing.T) {
	tests := []struct {
		name    string
		f       chstore.MetricQueryFilter
		want    string
		wantErr string
	}{
		{
			// v0.9.1159 — the metric name is still never REPLACED, but it is
			// now ACCOMPANIED: the verbatim spelling leads the alternation and
			// the OTel-Prometheus forms follow it. Before this release the
			// selector was `__name__="http.server.request.duration"` alone,
			// which is why every panel in the operator's install was empty —
			// their VM holds `http_server_request_duration_seconds*`.
			name: "dotted metric name leads the candidate alternation",
			f:    chstore.MetricQueryFilter{Name: "http.server.request.duration", Aggregation: "max"},
			want: `max({` + mcHTTPDur + `})`,
		},
		{
			name: "underscored metric name gets the unit spellings too",
			f:    chstore.MetricQueryFilter{Name: "http_server_request_duration", Aggregation: "max"},
			want: `max({` + mcHTTPDurUnderscore + `})`,
		},
		{
			// SINGLE-CANDIDATE REGRESSION. A name that already carries
			// Prometheus naming needs no guessing, so it must render the `=`
			// form this file pinned before v0.9.1159 — byte for byte. The
			// candidate machinery is not allowed to change a query it had
			// nothing to add to (the upstream cache key and VM's query log
			// both read this text).
			name: "an already-named metric keeps the pre-candidate = form",
			f:    chstore.MetricQueryFilter{Name: "http_server_request_duration_seconds_bucket"},
			want: `avg({__name__="http_server_request_duration_seconds_bucket"})`,
		},
		{
			// The same, with the rest of the selector still riding alongside:
			// one candidate changes the NAME matcher's form and nothing else.
			name: "single candidate with service and filters",
			f: chstore.MetricQueryFilter{
				Name: "http_requests_total", Service: "cart",
				Filters: []chstore.FilterExpr{{Key: "http.route", Op: "=", Values: []string{"/api"}}},
			},
			want: `avg({__name__="http_requests_total", service_name="cart", http_route="/api"})`,
		},
		{
			name: "service becomes service_name",
			f:    chstore.MetricQueryFilter{Name: "m", Service: "api-gateway", Aggregation: "max"},
			want: `max({` + mcM + `, service_name="api-gateway"})`,
		},
		{
			name: "quotes in a value are escaped",
			f:    chstore.MetricQueryFilter{Name: `m"x`, Service: `a\b`, Aggregation: "max"},
			want: `max({` + mcQuoted + `, service_name="a\\b"})`,
		},
		{
			name:    "empty metric name",
			f:       chstore.MetricQueryFilter{Name: "  "},
			wantErr: "metric name required",
		},
		{
			// DB-receiver instance scoping is a CH-side per-engine OR over
			// receiver attributes. Guessing a VM equivalent would scope the
			// chart to the WRONG instance — the exact cross-poisoning the
			// CH clause was added for.
			name:    "instance scoping refused",
			f:       chstore.MetricQueryFilter{Name: "m", Instance: "pg-1", Engine: "postgresql"},
			wantErr: "instance/engine scoping is unsupported",
		},
		{
			name:    "engine alone also refused",
			f:       chstore.MetricQueryFilter{Name: "m", Engine: "postgresql"},
			wantErr: "instance/engine scoping is unsupported",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildTranslate(tc.f)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v (%q)", tc.wantErr, err, got)
				}
				// The SENTINEL is the contract the API classifies on (400 vs
				// 502); the wording is only for the operator. "metric name
				// required" is a caller bug, not an untranslatable request,
				// so it is exempt.
				if !strings.Contains(tc.wantErr, "required") && !errors.Is(err, ErrUnsupported) {
					t.Fatalf("refusal not tagged ErrUnsupported — the API would 502 and blame "+
						"a healthy VictoriaMetrics: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestPromMatcher(t *testing.T) {
	tests := []struct {
		name    string
		fe      chstore.FilterExpr
		want    string
		wantErr string
	}{
		{name: "eq single", fe: chstore.FilterExpr{Key: "pod", Op: "=", Values: []string{"api-1"}},
			want: `pod="api-1"`},
		{name: "neq single", fe: chstore.FilterExpr{Key: "pod", Op: "!=", Values: []string{"api-1"}},
			want: `pod!="api-1"`},
		{name: "dotted key sanitized", fe: chstore.FilterExpr{Key: "jvm.memory.type", Op: "=", Values: []string{"heap"}},
			want: `jvm_memory_type="heap"`},
		{name: "resource prefix stripped", fe: chstore.FilterExpr{Key: "resource.host.name", Op: "=", Values: []string{"h1"}},
			want: `host_name="h1"`},
		{name: "span prefix stripped", fe: chstore.FilterExpr{Key: "span.http.route", Op: "=", Values: []string{"/x"}},
			want: `http_route="/x"`},
		{
			// PromQL has no IN — the alternation must be regexp-QUOTED, or
			// `api-1.2` would also match `api-1x2`.
			name: "multi-value eq becomes an escaped alternation",
			fe:   chstore.FilterExpr{Key: "pod", Op: "=", Values: []string{"api-1.2", "api-2"}},
			want: `pod=~"api-1\\.2|api-2"`,
		},
		{name: "IN is multi eq", fe: chstore.FilterExpr{Key: "pod", Op: "IN", Values: []string{"a", "b"}},
			want: `pod=~"a|b"`},
		{name: "IN single collapses to eq", fe: chstore.FilterExpr{Key: "pod", Op: "IN", Values: []string{"a"}},
			want: `pod="a"`},
		{name: "NOT IN", fe: chstore.FilterExpr{Key: "pod", Op: "NOT IN", Values: []string{"a", "b"}},
			want: `pod!~"a|b"`},
		{
			// Already a regex — must NOT be QuoteMeta'd, or the operator's
			// `.*` becomes a literal.
			name: "regex passes through unquoted",
			fe:   chstore.FilterExpr{Key: "pod", Op: "=~", Values: []string{"api-.*"}},
			want: `pod=~"api-.*"`,
		},
		{name: "negative regex", fe: chstore.FilterExpr{Key: "pod", Op: "!~", Values: []string{"canary-.*"}},
			want: `pod!~"canary-.*"`},
		{name: "EXISTS", fe: chstore.FilterExpr{Key: "pod", Op: "EXISTS"}, want: `pod!=""`},
		{name: "NOT EXISTS", fe: chstore.FilterExpr{Key: "pod", Op: "NOT EXISTS"}, want: `pod=""`},
		{name: "lowercase exists", fe: chstore.FilterExpr{Key: "pod", Op: "exists"}, want: `pod!=""`},
		// LIKE — v0.9.1199: CH'nin CONTAINS derlemesiyle ("%v%") birebir
		// parite. Kullanıcı jokerleri yaşar, regex metakarakterleri ölür.
		{name: "LIKE is contains", fe: chstore.FilterExpr{Key: "pod", Op: "LIKE", Values: []string{"api"}},
			want: `pod=~".*api.*"`},
		{name: "NOT LIKE is not-contains", fe: chstore.FilterExpr{Key: "pod", Op: "NOT LIKE", Values: []string{"api"}},
			want: `pod!~".*api.*"`},
		{name: "LIKE inner percent stays a wildcard", fe: chstore.FilterExpr{Key: "pod", Op: "LIKE", Values: []string{"api%1"}},
			want: `pod=~".*api.*1.*"`},
		{name: "LIKE underscore is single char", fe: chstore.FilterExpr{Key: "pod", Op: "LIKE", Values: []string{"api-_"}},
			want: `pod=~".*api-..*"`},
		{name: "LIKE escaped wildcard is literal", fe: chstore.FilterExpr{Key: "pod", Op: "LIKE", Values: []string{`50\%`}},
			want: `pod=~".*50%.*"`},
		{name: "LIKE regex metachars are quoted", fe: chstore.FilterExpr{Key: "route", Op: "LIKE", Values: []string{"v1.0(beta)"}},
			want: `route=~".*v1\\.0\\(beta\\).*"`},
		{
			// İnce nokta: değer `\` ile bitince kompozit "%v%" kalıbında o
			// ters bölü KAPANIŞ %'ini escape'ler — CH LIKE `%a\%`yi "a% ile
			// biter" okur. Çeviri kalıp-bazlı olduğu için aynı anlamı verir;
			// değer-bazlı olsaydı burada CH'den ayrışırdı (parite kanıtı).
			name: "LIKE trailing backslash escapes the closing wrapper — CH parity",
			fe:   chstore.FilterExpr{Key: "pod", Op: "LIKE", Values: []string{`a\`}},
			want: `pod=~".*a%"`,
		},
		// Refusals — a filter we cannot express must never be dropped.
		{name: "gt refused", fe: chstore.FilterExpr{Key: "n", Op: ">", Values: []string{"5"}},
			wantErr: "unsupported"},
		{name: "gte refused", fe: chstore.FilterExpr{Key: "n", Op: ">=", Values: []string{"5"}},
			wantErr: "unsupported"},
		{name: "lt refused", fe: chstore.FilterExpr{Key: "n", Op: "<", Values: []string{"5"}},
			wantErr: "unsupported"},
		{name: "lte refused", fe: chstore.FilterExpr{Key: "n", Op: "<=", Values: []string{"5"}},
			wantErr: "unsupported"},
		{name: "empty key refused", fe: chstore.FilterExpr{Key: "", Op: "=", Values: []string{"a"}},
			wantErr: "empty key"},
		{name: "no value refused", fe: chstore.FilterExpr{Key: "pod", Op: "=", Values: nil},
			wantErr: "has no value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := promMatcher(tc.fe)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v (%q)", tc.wantErr, err, got)
				}
				// An UNSUPPORTED OPERATOR is tagged (→ 400); a malformed
				// filter (empty key / no value) is a caller bug and is not.
				if strings.Contains(tc.wantErr, "unsupported") && !errors.Is(err, ErrUnsupported) {
					t.Fatalf("operator refusal not tagged ErrUnsupported: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

// A refused filter must fail the WHOLE query, not get skipped on the way
// into the selector.
func TestBuildPromQLRefusedFilterFailsTheQuery(t *testing.T) {
	_, err := buildTranslate(chstore.MetricQueryFilter{
		Name: "m",
		Filters: []chstore.FilterExpr{
			{Key: "pod", Op: "=", Values: []string{"api-1"}},
			{Key: "n", Op: ">", Values: []string{"5"}},
		},
	})
	if err == nil {
		t.Fatal("an inexpressible filter was silently dropped — this is the v0.9.566 class")
	}
}

func TestBuildPromQLFilterOrderPreserved(t *testing.T) {
	got, err := buildTranslate(chstore.MetricQueryFilter{
		Name:        "m",
		Service:     "svc",
		Aggregation: "max",
		Filters: []chstore.FilterExpr{
			{Key: "a", Op: "=", Values: []string{"1"}},
			{Key: "b", Op: "=", Values: []string{"2"}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `max({` + mcM + `, service_name="svc", a="1", b="2"})`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestPromLabel(t *testing.T) {
	tests := map[string]string{
		"pod":                "pod",
		"service.name":       "service_name",
		"http.route":         "http_route",
		"k8s.pod.name":       "k8s_pod_name",
		"resource.host.name": "host_name",
		"span.http.method":   "http_method",
		"weird-key!":         "weird_key_",
		"2xx":                "_2xx",
		"":                   "",
		"  ":                 "",
	}
	for in, want := range tests {
		if got := promLabel(in); got != want {
			t.Fatalf("promLabel(%q) = %q, want %q", in, got, want)
		}
	}
	if serviceLabel() != "service_name" {
		t.Fatalf("serviceLabel() = %q", serviceLabel())
	}
}

func TestPromStep(t *testing.T) {
	from := time.Unix(1700000000, 0)
	tests := []struct {
		name       string
		rangeSec   int
		step       int
		maxDP      int
		want       int
		wantReason string
	}{
		{name: "explicit step honoured", rangeSec: 3600, step: 30, want: 30},
		{name: "explicit step of 1s honoured", rangeSec: 600, step: 1, want: 1},
		{
			// 30 days at 10s would be 259200 points — VM 4xxs past ~11k.
			name:     "explicit step widened past the points ceiling",
			rangeSec: 30 * 24 * 3600, step: 10, want: 236,
			wantReason: "ceil(2592000/11000)",
		},
		{name: "pixel-adaptive", rangeSec: 3600, maxDP: 400, want: 9},
		{name: "pixel-adaptive rounds up", rangeSec: 100, maxDP: 30, want: 4},
		{name: "pixel-adaptive never below 1s", rangeSec: 10, maxDP: 4000, want: 1},
		{name: "no step no maxDP falls back to bucket count", rangeSec: 3600, want: 12},
		{name: "fallback on a 24h window", rangeSec: 86400, want: 288},
		{name: "explicit beats maxDP", rangeSec: 3600, step: 60, maxDP: 4000, want: 60},
		{name: "zero-length window still yields a legal step", rangeSec: 0, want: 1},
		{name: "negative window still yields a legal step", rangeSec: -60, want: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			to := from.Add(time.Duration(tc.rangeSec) * time.Second)
			got := promStep(from, to, tc.step, tc.maxDP)
			if got != tc.want {
				t.Fatalf("promStep(range=%ds, step=%d, maxDP=%d) = %d, want %d %s",
					tc.rangeSec, tc.step, tc.maxDP, got, tc.want, tc.wantReason)
			}
			if got < 1 {
				t.Fatalf("step must never be < 1s, got %d", got)
			}
			if tc.rangeSec > 0 && tc.rangeSec/got > maxPromPoints {
				t.Fatalf("step %d leaves %d points, over the %d ceiling",
					got, tc.rangeSec/got, maxPromPoints)
			}
		})
	}
}

// GroupKey is POSITIONAL — the frontend joins it with "|" to label the
// line. Reading VM's label map in map order would relabel series between
// polls at random.
func TestSeriesGroupKey(t *testing.T) {
	labels := map[string]string{
		"service_name":    "api",
		"pod":             "api-1",
		"jvm_memory_type": "heap",
		"__name__":        "jvm.memory.used",
	}
	tests := []struct {
		name    string
		groupBy []string
		want    []string
	}{
		{name: "nil group-by yields nil", groupBy: nil, want: nil},
		{name: "single", groupBy: []string{"pod"}, want: []string{"api-1"}},
		{
			name:    "order follows the REQUEST not the map",
			groupBy: []string{"jvm.memory.type", "pod", "service.name"},
			want:    []string{"heap", "api-1", "api"},
		},
		{
			name:    "reversed request reverses the tuple",
			groupBy: []string{"service.name", "pod"},
			want:    []string{"api", "api-1"},
		},
		{
			// A missing label yields "" IN PLACE. Skipping it would shift
			// the tuple left and mislabel every remaining dimension.
			name:    "missing label holds its slot",
			groupBy: []string{"pod", "nope", "service.name"},
			want:    []string{"api-1", "", "api"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := seriesGroupKey(tc.groupBy, labels)
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

// pageNames carries the whole {names,total,hasMore} contract for VM,
// because VM's label-values endpoint has neither a substring filter nor
// an offset. `hasMore` is derived by the handler as
// offset+len(names) < total, so `total` must count MATCHES, never the
// returned page.
func TestPageNames(t *testing.T) {
	all := []string{"a.one", "a.two", "b.one", "b.two", "c.one"}
	tests := []struct {
		name      string
		pattern   string
		limit     int
		offset    int
		unlimited bool
		want      []string
		wantTotal int
	}{
		{
			name: "unlimited returns everything", unlimited: true,
			want: all, wantTotal: 5,
		},
		{
			name: "first page", limit: 2,
			want: []string{"a.one", "a.two"}, wantTotal: 5,
		},
		{
			name: "second page", limit: 2, offset: 2,
			want: []string{"b.one", "b.two"}, wantTotal: 5,
		},
		{
			// total stays 5 so hasMore = 4+1 < 5 = false. If total were the
			// page length the picker would never stop paging.
			name: "last partial page", limit: 2, offset: 4,
			want: []string{"c.one"}, wantTotal: 5,
		},
		{
			name: "offset past the end is empty, not an error", limit: 2, offset: 99,
			want: []string{}, wantTotal: 5,
		},
		{
			name: "pattern narrows total too", pattern: "a.", limit: 10,
			want: []string{"a.one", "a.two"}, wantTotal: 2,
		},
		{
			name: "pattern is case-insensitive", pattern: "A.ONE", limit: 10,
			want: []string{"a.one"}, wantTotal: 1,
		},
		{
			name: "pattern matches mid-name", pattern: "one", limit: 10,
			want: []string{"a.one", "b.one", "c.one"}, wantTotal: 3,
		},
		{
			name: "no match", pattern: "zzz", limit: 10,
			want: []string{}, wantTotal: 0,
		},
		{
			name: "zero limit defaults to 200", limit: 0, offset: 1,
			want: []string{"a.two", "b.one", "b.two", "c.one"}, wantTotal: 5,
		},
		{
			name: "negative offset clamps to 0", limit: 1, offset: -5,
			want: []string{"a.one"}, wantTotal: 5,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, total := pageNames(all, tc.pattern, tc.limit, tc.offset, tc.unlimited)
			if total != tc.wantTotal {
				t.Fatalf("total = %d, want %d", total, tc.wantTotal)
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

// Empty names are dropped rather than becoming a blank catalogue row.
func TestPageNamesDropsEmpties(t *testing.T) {
	got, total := pageNames([]string{"", "a", "", "b"}, "", 10, 0, false)
	if total != 2 || len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("got %v (total %d)", got, total)
	}
}
