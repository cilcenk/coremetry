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
	// Refused. The percentiles need the histogram bucket series (Faz 2);
	// the rest are simply not aggregations Coremetry has.
	for _, in := range []string{"p50", "p95", "p99", "p90", "median", "nonsense"} {
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

// The percentile refusal has to POINT SOMEWHERE. "unsupported" alone reads
// as "never", and the operator's next move is to hunt for a workaround that
// does not exist — the templates hand p99 to every histogram family
// (metricTemplates.ts), so this is the message they will actually meet.
func TestPercentileRefusalNamesFaz2(t *testing.T) {
	for _, agg := range []string{"p50", "p95", "p99"} {
		_, err := buildPromQL(chstore.MetricQueryFilter{
			Name: "http.server.request.duration", Aggregation: agg,
		})
		if err == nil {
			t.Fatalf("agg=%s: want a refusal", agg)
		}
		msg := err.Error()
		for _, want := range []string{"Faz 2", "histogram", "bucket", agg} {
			if !strings.Contains(msg, want) {
				t.Fatalf("agg=%s: refusal does not mention %q: %s", agg, want, msg)
			}
		}
		// It must not still advertise last/rate/increase as missing.
		for _, nowSupported := range []string{"last", "rate", "increase"} {
			if !strings.Contains(msg, nowSupported) {
				t.Fatalf("agg=%s: supported set lost %q: %s", agg, nowSupported, msg)
			}
		}
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("agg=%s: not tagged ErrUnsupported: %v", agg, err)
		}
	}
}

// Full aggregation × group-by matrix.
func TestBuildPromQLAggregationGroupByMatrix(t *testing.T) {
	aggs := []struct{ in, op string }{
		{"", "avg"}, {"avg", "avg"}, {"sum", "sum"},
		{"min", "min"}, {"max", "max"}, {"count", "count"},
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
				got, err := buildPromQL(chstore.MetricQueryFilter{
					Name:        "jvm.memory.used",
					Aggregation: a.in,
					GroupBy:     g.in,
				})
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				want := a.op + `({__name__="jvm.memory.used"})`
				if g.by != "" {
					want = a.op + ` by (` + g.by + `) ({__name__="jvm.memory.used"})`
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
			want: `max(last_over_time({__name__="jvm.memory.used"}[300s]))`,
		},
		{
			name: "last, one group-by",
			f: mk(chstore.MetricQueryFilter{Name: "jvm.memory.used", Aggregation: "last",
				StepSeconds: 60, GroupBy: []string{"host.name"}}),
			want: `max by (host_name) (last_over_time({__name__="jvm.memory.used"}[300s]))`,
		},
		{
			name: "last, two dotted group-bys, step above the floor",
			f: mk(chstore.MetricQueryFilter{Name: "jvm.memory.used", Aggregation: "last",
				StepSeconds: 900, GroupBy: []string{"host.name", "jvm.memory.pool.name"}}),
			want: `max by (host_name, jvm_memory_pool_name) (last_over_time({__name__="jvm.memory.used"}[900s]))`,
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
			want: `max by (pod) (last_over_time({__name__="m", service_name="api", jvm_memory_type="heap"}[300s]))`,
		},
		{
			// RateWindowSec is a RATE window; it must not bend `last`.
			name: "last ignores RateWindowSec",
			f: mk(chstore.MetricQueryFilter{Name: "m", Aggregation: "last",
				StepSeconds: 60, RateWindowSec: 180}),
			want: `max(last_over_time({__name__="m"}[300s]))`,
		},
		{
			// sum(rate(...)), not avg — the CH path re-aggregates per-series
			// rates with a sum and names this idiom as its semantics.
			name: "rate, no group-by, floored",
			f:    mk(chstore.MetricQueryFilter{Name: "http.server.requests", Aggregation: "rate", StepSeconds: 60}),
			want: `sum(rate({__name__="http.server.requests"}[300s]))`,
		},
		{
			name: "rate, group-by",
			f: mk(chstore.MetricQueryFilter{Name: "http.server.requests", Aggregation: "rate",
				StepSeconds: 60, GroupBy: []string{"http.route"}}),
			want: `sum by (http_route) (rate({__name__="http.server.requests"}[300s]))`,
		},
		{
			name: "rate, step above the floor keeps the step",
			f:    mk(chstore.MetricQueryFilter{Name: "m", Aggregation: "rate", StepSeconds: 600}),
			want: `sum(rate({__name__="m"}[600s]))`,
		},
		{
			// The operator's Grafana reference is [3m]. An explicit window
			// wins and is NOT promoted to the 5m floor.
			name: "rate honours an explicit RateWindowSec below the floor",
			f:    mk(chstore.MetricQueryFilter{Name: "m", Aggregation: "rate", StepSeconds: 60, RateWindowSec: 180}),
			want: `sum(rate({__name__="m"}[180s]))`,
		},
		{
			name: "rate honours an explicit RateWindowSec above the floor",
			f:    mk(chstore.MetricQueryFilter{Name: "m", Aggregation: "rate", StepSeconds: 600, RateWindowSec: 900}),
			want: `sum(rate({__name__="m"}[900s]))`,
		},
		{
			// <= step means "unexpressed" (the CH sibling's own rule), so
			// the floor still applies.
			name: "rate ignores a RateWindowSec below the step",
			f:    mk(chstore.MetricQueryFilter{Name: "m", Aggregation: "rate", StepSeconds: 60, RateWindowSec: 30}),
			want: `sum(rate({__name__="m"}[300s]))`,
		},
		{
			// increase is a window TOTAL — flooring it would quadruple the
			// number while the chart still says one bucket (v0.6.36 class).
			name: "increase is NOT floored",
			f:    mk(chstore.MetricQueryFilter{Name: "m", Aggregation: "increase", StepSeconds: 60}),
			want: `sum(increase({__name__="m"}[60s]))`,
		},
		{
			name: "increase at a sub-minute step stays at the step",
			f:    mk(chstore.MetricQueryFilter{Name: "m", Aggregation: "increase", StepSeconds: 12}),
			want: `sum(increase({__name__="m"}[12s]))`,
		},
		{
			name: "increase, group-by, explicit window",
			f: mk(chstore.MetricQueryFilter{Name: "m", Aggregation: "increase", StepSeconds: 60,
				RateWindowSec: 180, GroupBy: []string{"error.type"}}),
			want: `sum by (error_type) (increase({__name__="m"}[180s]))`,
		},
		{
			name: "case and padding are normalized like the set-aggregations",
			f:    mk(chstore.MetricQueryFilter{Name: "m", Aggregation: " RATE ", StepSeconds: 600}),
			want: `sum(rate({__name__="m"}[600s]))`,
		},
		{
			// A group-by that sanitizes to nothing falls back to the
			// ungrouped form — same rule the Faz 1 aggregations follow.
			name: "group-by that sanitizes away collapses to the ungrouped form",
			f: mk(chstore.MetricQueryFilter{Name: "m", Aggregation: "rate", StepSeconds: 600,
				GroupBy: []string{"  "}}),
			want: `sum(rate({__name__="m"}[600s]))`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildPromQL(tc.f)
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
func TestSetAggregationsCarryNoWindow(t *testing.T) {
	for _, agg := range []string{"", "avg", "sum", "min", "max", "count"} {
		got, err := buildPromQL(chstore.MetricQueryFilter{
			Name: "m", Aggregation: agg, StepSeconds: 60,
			From: time.Unix(1700000000, 0), To: time.Unix(1700003600, 0),
		})
		if err != nil {
			t.Fatalf("agg=%q: %v", agg, err)
		}
		if strings.Contains(got, "[") {
			t.Fatalf("agg=%q rendered a range vector: %s", agg, got)
		}
	}
}

// A refused FILTER must still fail a rollup-shaped query. The rollup path
// is a second code path through the same selector, which is exactly how the
// v0.9.566 class comes back.
func TestRollupStillRefusesAnInexpressibleFilter(t *testing.T) {
	for _, agg := range []string{"last", "rate", "increase"} {
		_, err := buildPromQL(chstore.MetricQueryFilter{
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
			got := promRollupWindow(tc.rollup, tc.step, tc.rateWin)
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
			expr, err := buildPromQL(f)
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
			if want := promRollupWindow(tc.rollup, step, tc.rateWin); got != want {
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
			// The metric name is NEVER translated: whichever spelling the
			// catalogue handed us is the spelling VM has. __name__ selector
			// form is what makes a dotted name expressible at all.
			name: "dotted metric name goes through verbatim",
			f:    chstore.MetricQueryFilter{Name: "http.server.request.duration"},
			want: `avg({__name__="http.server.request.duration"})`,
		},
		{
			name: "underscored metric name also verbatim",
			f:    chstore.MetricQueryFilter{Name: "http_server_request_duration"},
			want: `avg({__name__="http_server_request_duration"})`,
		},
		{
			name: "service becomes service_name",
			f:    chstore.MetricQueryFilter{Name: "m", Service: "api-gateway"},
			want: `avg({__name__="m", service_name="api-gateway"})`,
		},
		{
			name: "quotes in a value are escaped",
			f:    chstore.MetricQueryFilter{Name: `m"x`, Service: `a\b`},
			want: `avg({__name__="m\"x", service_name="a\\b"})`,
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
			got, err := buildPromQL(tc.f)
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
		// Refusals — a filter we cannot express must never be dropped.
		{name: "LIKE refused", fe: chstore.FilterExpr{Key: "pod", Op: "LIKE", Values: []string{"api%"}},
			wantErr: "unsupported"},
		{name: "NOT LIKE refused", fe: chstore.FilterExpr{Key: "pod", Op: "NOT LIKE", Values: []string{"api%"}},
			wantErr: "unsupported"},
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
	_, err := buildPromQL(chstore.MetricQueryFilter{
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
	got, err := buildPromQL(chstore.MetricQueryFilter{
		Name:    "m",
		Service: "svc",
		Filters: []chstore.FilterExpr{
			{Key: "a", Op: "=", Values: []string{"1"}},
			{Key: "b", Op: "=", Values: []string{"2"}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `avg({__name__="m", service_name="svc", a="1", b="2"})`
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
