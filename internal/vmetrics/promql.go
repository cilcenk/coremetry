package vmetrics

// MetricsQL translation — the pure half of the VictoriaMetrics read
// backend (v0.9.1150, Faz 1; rollup aggregations v0.9.1154, Faz 1.5).
// Everything here is a function of its arguments so the whole translation
// is table-testable without a live VM; the client wraps these and does I/O
// only.
//
// THE TARGET DIALECT IS MetricsQL, NOT STRICT PromQL (operator correction,
// 2026-08-17). MetricsQL is a PromQL superset, so every grammar statement
// below — label-name charset, no IN operator, fully anchored regex
// matchers — still holds unchanged. But where the two dialects DIFFER,
// VictoriaMetrics' behaviour is the one that decides, and Faz 1.5 leans on
// two of those differences on purpose:
//
//   - rate()/increase() take the last raw sample BEFORE the lookbehind
//     window into account (that is why `increase_pure` exists as the
//     opt-out). So they need no extrapolation and still produce a value
//     when only one sample falls inside the window. This is exactly the
//     shape the CH path computes — a reset-protected per-series delta over
//     REAL dt, with extrapolation deliberately skipped and VictoriaMetrics
//     named as the reason (metricrate.go:31) — so the two backends agree
//     by construction rather than by luck.
//   - the lookbehind window is OPTIONAL. Omitted, VM fills in `step` for
//     most rollups but max(step, scrape_interval) for rate() and
//     default_rollup(). We write it EXPLICITLY anyway (rule 3), which
//     means the widening is now OUR job — see promRollupWindow.
//
// The file keeps its promql.go name: the dialect note belongs in the code,
// and the history of the file is worth more than the rename.
//
// Three rules run through the whole file:
//
//  1. The METRIC NAME is never translated. OTLP names may live in VM as
//     `jvm.memory.used` or as `jvm_memory_used` depending on how the
//     operator's write path sanitized them, and we cannot know which —
//     so we send whatever the catalogue gave us, verbatim. That forces
//     the `{__name__="…"}` selector form (a bare `jvm.memory.used{}` is
//     not valid MetricsQL), which happens to work for BOTH spellings.
//     LABEL names are a different story: the grammar has no way to
//     express a dotted label name at all, so those must be sanitized.
//
//  2. A filter we cannot express is an ERROR, never a silent drop. The
//     v0.9.566 incident is the reason: a dropped `jvm.memory.type="heap"`
//     filter did not produce an empty panel, it produced heap+non-heap
//     summed and LABELLED heap. A wrong-but-plausible number is worse
//     than a failure, because nobody questions it.
//
//  3. A rollup window is a deterministic function of the REQUEST — the
//     step and the caller's rate window, nothing else. Never VM's
//     scrape-interval auto-detection, never the wall clock. Two polls of
//     the same panel must render the same expression, or the cache key
//     upstream (internal/api, metric-query:v3) stops describing the query
//     it keys.

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// ErrUnsupported marks a query Coremetry cannot EXPRESS against VM — a
// refused aggregation, a filter operator with no MetricsQL matcher, DB
// instance scoping. It is deliberately distinct from a transport failure:
// VM is healthy and reachable, the REQUEST is the thing that does not
// translate.
//
// The API maps it to 400, not 502. Reporting it as 502 would tell the
// operator their VictoriaMetrics is broken and send them to check a
// cluster that is fine — a wrong diagnosis is worse than a blunt one.
var ErrUnsupported = errors.New("unsupported by the VictoriaMetrics backend")

// maxPromPoints mirrors the points-per-timeseries ceiling Prometheus and
// VictoriaMetrics both enforce on query_range. Hitting it is a 4xx, so
// the step is widened until the window fits rather than letting the
// operator's wide range fail outright.
const maxPromPoints = 11000

// defaultPromBuckets — the bucket count used when the caller supplied
// neither an explicit step nor a panel width. Matches the order of
// magnitude of the CH path's auto-step ladder for typical windows.
const defaultPromBuckets = 300

// MetricsQL rollup functions this translation emits. Named constants
// because promRollupWindow switches on them and a typo there would be a
// silently different window, not a compile error.
const (
	rollupLast     = "last_over_time"
	rollupRate     = "rate"
	rollupIncrease = "increase"
)

// promSupportedAggs is the operator-facing list, shared by both refusal
// messages so they can never drift out of sync with each other.
const promSupportedAggs = "avg, sum, min, max, count, last, rate, increase"

// promAgg is the SHAPE a Coremetry aggregation compiles to: an optional
// rollup function wrapped around the selector, then a set-aggregation over
// the resulting instant vector.
//
//	{Op: "avg"}                           → avg({…})
//	{Op: "sum", Rollup: "rate"}           → sum(rate({…}[Ws]))
//	{Op: "max", Rollup: "last_over_time"} → max(last_over_time({…}[Ws]))
type promAgg struct {
	Op     string // avg | sum | min | max | count — the set-aggregation
	Rollup string // "" = none, the selector is aggregated directly
}

// promAggregator maps a Coremetry aggregation label to its MetricsQL shape.
//
// The five set-aggregations mean the same thing in both engines and stay a
// bare `op(selector)` — byte-identical to Faz 1.
//
// v0.9.1154 (Faz 1.5) adds the three ROLLUP-shaped aggregations. Faz 1
// refused them for being a shape change rather than an operator swap. That
// was accurate and is no longer a reason: metricTemplates.ts seeds `last`
// on every gauge family it recognises (runtime levels, memory, thread
// counts, DB pool state), so an operator in VM mode earned a 400 by
// CLICKING A CATALOGUE ROW — the most ordinary thing there is to do on that
// page.
//
//   - last → max(last_over_time(sel[W])). The max() wrap is a group
//     COLLAPSE, not a change of aggregation; the argument is in buildPromQL.
//   - rate → sum(rate(sel[W])). SUM, not avg: the CH path computes the rate
//     PER SERIES and then re-aggregates into the caller's group-by with a
//     sum, naming `sum(rate(counter)) by(label)` as the semantics it
//     reproduces (metricrate.go:28). Same idiom, same number.
//   - increase → sum(increase(sel[W])), the same idiom over the window
//     TOTAL instead of the per-second rate.
//
// p50/p95/p99 stay refused, and NOT because nobody got to them: a
// percentile is not an operator swap at all. It needs the histogram BUCKET
// series (histogram_quantile over …_bucket / le), and choosing between that
// and a value-quantile needs the instrument type, which VM does not report
// (see ListMetricNames). Histograms are Faz 2 scope. The message says so
// out loud, because a bare "unsupported" reads as "never" and sends the
// operator hunting for a workaround that does not exist.
func promAggregator(agg string) (promAgg, error) {
	switch strings.ToLower(strings.TrimSpace(agg)) {
	case "", "avg":
		return promAgg{Op: "avg"}, nil
	case "sum":
		return promAgg{Op: "sum"}, nil
	case "min":
		return promAgg{Op: "min"}, nil
	case "max":
		return promAgg{Op: "max"}, nil
	case "count":
		return promAgg{Op: "count"}, nil
	case "last":
		return promAgg{Op: "max", Rollup: rollupLast}, nil
	case "rate":
		return promAgg{Op: "sum", Rollup: rollupRate}, nil
	case "increase":
		return promAgg{Op: "sum", Rollup: rollupIncrease}, nil
	case "p50", "p95", "p99":
		return promAgg{}, fmt.Errorf("percentile aggregation %q is %w here: it needs the histogram "+
			"bucket series (histogram_quantile over …_bucket), which is Faz 2 — supported today: %s",
			agg, ErrUnsupported, promSupportedAggs)
	}
	return promAgg{}, fmt.Errorf("aggregation %q is %w (supported: %s)",
		agg, ErrUnsupported, promSupportedAggs)
}

var promLabelInvalid = regexp.MustCompile(`[^a-zA-Z0-9_]`)

// promLabel sanitizes an attribute key into a valid MetricsQL label name.
//
// This is NOT the metric-name translation rule 1 forbids — it is a
// grammar requirement. `{service.name="x"}` does not parse: label
// names are [a-zA-Z_][a-zA-Z0-9_]*. Every OTLP→Prometheus write path
// (including VictoriaMetrics' own OTLP receiver) applies the same
// dot→underscore mapping, so this reproduces what the data already looks
// like on the other side.
func promLabel(k string) string {
	k = strings.TrimSpace(k)
	if k == "" {
		return ""
	}
	// Coremetry's filter keys carry `resource.` / `span.` namespaces that
	// name WHERE the attribute lived in OTLP. VM has no such distinction —
	// resource attributes become plain labels — so the prefix is dropped
	// rather than baked into a label nobody has.
	for _, p := range []string{"resource.", "span."} {
		if strings.HasPrefix(k, p) {
			k = strings.TrimPrefix(k, p)
			break
		}
	}
	k = promLabelInvalid.ReplaceAllString(k, "_")
	if k != "" && k[0] >= '0' && k[0] <= '9' {
		k = "_" + k
	}
	return k
}

// serviceLabel is where service.name lands in VM. Derived through
// promLabel rather than hard-coded so the two can never disagree.
func serviceLabel() string { return promLabel("service.name") }

// quotePromString escapes a value for a MetricsQL double-quoted literal.
func quotePromString(v string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range v {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// promMatcher renders one FilterExpr as a MetricsQL label matcher.
//
// Multi-value equality becomes a regex alternation because the dialect has no
// IN: `k=~"a|b"`. The values are regexp-quoted first — an unescaped `.`
// in `pod=~"api-1.2"` would match `api-1x2` too, which is the same class
// of silent-wrong-answer as a dropped filter.
func promMatcher(fe chstore.FilterExpr) (string, error) {
	key := promLabel(fe.Key)
	if key == "" {
		return "", fmt.Errorf("filter with an empty key")
	}
	vals := make([]string, 0, len(fe.Values))
	for _, v := range fe.Values {
		if v != "" {
			vals = append(vals, v)
		}
	}
	op := strings.ToUpper(strings.TrimSpace(fe.Op))
	switch op {
	case "EXISTS":
		// The dialect idiom for "label is present".
		return key + `!=""`, nil
	case "NOT EXISTS":
		return key + `=""`, nil
	}
	if len(vals) == 0 {
		return "", fmt.Errorf("filter %q %s has no value", fe.Key, fe.Op)
	}
	switch op {
	case "=", "IN":
		if len(vals) == 1 {
			return key + "=" + quotePromString(vals[0]), nil
		}
		return key + "=~" + quotePromString(regexAlternation(vals)), nil
	case "!=", "NOT IN":
		if len(vals) == 1 {
			return key + "!=" + quotePromString(vals[0]), nil
		}
		return key + "!~" + quotePromString(regexAlternation(vals)), nil
	case "=~":
		// Already a regex — pass through verbatim, no QuoteMeta.
		return key + "=~" + quotePromString(vals[0]), nil
	case "!~":
		return key + "!~" + quotePromString(vals[0]), nil
	}
	// LIKE / NOT LIKE and the numeric comparisons have no MetricsQL label-
	// matcher equivalent. LIKE could be rewritten to a regex, but the
	// `%`/`_` → `.*`/`.` mapping has enough edge cases (escaped literals)
	// that a wrong rewrite would filter silently-wrongly; refusing is the
	// honest answer for Faz 1.
	return "", fmt.Errorf("filter operator %q is %w (supported: =, !=, =~, !~, IN, NOT IN, "+
		"EXISTS, NOT EXISTS)", fe.Op, ErrUnsupported)
}

func regexAlternation(vals []string) string {
	quoted := make([]string, len(vals))
	for i, v := range vals {
		quoted[i] = regexp.QuoteMeta(v)
	}
	// Anchored: the dialect's regex matchers are fully anchored already, but
	// being explicit keeps the intent readable in VM's query log.
	return strings.Join(quoted, "|")
}

// buildPromQL renders the whole query expression for a MetricQueryFilter.
// Pure — table-tested in promql_test.go.
func buildPromQL(f chstore.MetricQueryFilter) (string, error) {
	name := strings.TrimSpace(f.Name)
	if name == "" {
		return "", fmt.Errorf("metric name required")
	}
	// Instance/Engine encode Coremetry's DB-receiver scoping, which
	// compiles to a per-engine OR over receiver-specific attribute
	// columns (chstore.dbInstanceScopeClause). There is no VM equivalent
	// and guessing one would scope the chart to the wrong instance —
	// exactly the cross-poisoning the CH path added these for.
	if strings.TrimSpace(f.Instance) != "" || strings.TrimSpace(f.Engine) != "" {
		return "", fmt.Errorf("database instance/engine scoping is %w "+
			"(this drill reads ClickHouse-side receiver attributes)", ErrUnsupported)
	}
	agg, err := promAggregator(f.Aggregation)
	if err != nil {
		return "", err
	}

	matchers := []string{"__name__=" + quotePromString(name)}
	if svc := strings.TrimSpace(f.Service); svc != "" {
		matchers = append(matchers, serviceLabel()+"="+quotePromString(svc))
	}
	for _, fe := range f.Filters {
		m, err := promMatcher(fe)
		if err != nil {
			return "", err
		}
		matchers = append(matchers, m)
	}
	sel := "{" + strings.Join(matchers, ", ") + "}"

	// Rollup-shaped aggregations (last / rate / increase) wrap the selector
	// in a range-vector function before the set-aggregation runs.
	//
	// The window is resolved from the SAME promStep call the client uses for
	// the query_range `step` param, over the same already-normalized filter.
	// It is recomputed here rather than threaded in as an argument BECAUSE
	// promStep is pure: same inputs, same number, so the expression and the
	// step param cannot drift. Passing it in would be the version that can
	// drift — a caller free to send one number and render another.
	vec := sel
	if agg.Rollup != "" {
		w := promRollupWindow(agg.Rollup,
			promStep(f.From, f.To, f.StepSeconds, f.MaxDataPoints), f.RateWindowSec)
		vec = agg.Rollup + "(" + sel + "[" + strconv.Itoa(w) + "s])"
	}

	// GROUP COLLAPSE — and why `last` is wrapped in max().
	//
	// Every aggregation here renders as ONE series per group tuple (or one
	// series total with no group-by). That is the shape the CH path produces
	// and the shape SpanMetricSeries.GroupKey describes. `last` is the only
	// one where holding that shape costs an argument, so here it is.
	//
	// last_over_time is a per-SERIES rollup. Left bare it returns one series
	// per source series, and a group-by naming fewer labels than the data
	// carries — group by host.name over jvm.memory.used, which splits by
	// memory type AND pool — hands the frontend several series with the SAME
	// GroupKey. That is not a cosmetic collision: PanelStack derives the
	// series colour, the exemplar attribution, the GroupTable rows and the
	// compare-ghost matching from the tuple's label (seriesGroupLabel), so N
	// colliding series draw as N identically-named, identically-coloured
	// lines that all receive the same ◆ markers.
	//
	// max() collapses them, and the wrap is:
	//
	//   - IDENTITY when the grouping is exact. max over a one-element group
	//     is that element, unchanged — so "per-series last_over_time is
	//     already correct" stays true, byte for byte. Arithmetic, not a
	//     trade-off.
	//   - a REAL MEASUREMENT when it is not. CH's argMaxOrNull(value, time)
	//     also returns one member of the collapsed group (the newest
	//     sample); max returns one member too (the largest current value).
	//     Same class of answer — and the more stable of the two, since CH's
	//     "newest" flips between members as export order jitters.
	//   - never a FABRICATION. sum/avg would report a number no series ever
	//     measured, off from the CH backend by the group's cardinality:
	//     plausible, wrong, unquestioned — the v0.9.566 class.
	if len(f.GroupBy) == 0 {
		// No group-by → one series aggregated across everything, which is
		// what the CH path's `GROUP BY bucket` alone produces.
		return agg.Op + "(" + vec + ")", nil
	}
	labels := make([]string, 0, len(f.GroupBy))
	for _, g := range f.GroupBy {
		l := promLabel(g)
		if l == "" {
			continue
		}
		labels = append(labels, l)
	}
	if len(labels) == 0 {
		return agg.Op + "(" + vec + ")", nil
	}
	return agg.Op + " by (" + strings.Join(labels, ", ") + ") (" + vec + ")", nil
}

// promStep resolves the query_range step in seconds.
//
// Ordering matters and each branch is exercised by the table test:
//
//	explicit step   → honoured, then widened if it would exceed the
//	                  points ceiling (a 30d window at 10s is a 4xx).
//	maxDataPoints>0 → pixel-adaptive, the F1 (v0.9.105) contract.
//	neither         → fixed bucket count.
//
// Always ≥ 1s: step=0 is a VM error, and a fractional step would make
// bucket timestamps non-reproducible across polls.
func promStep(from, to time.Time, stepSeconds, maxDataPoints int) int {
	rangeSec := int(to.Sub(from).Seconds())
	if rangeSec <= 0 {
		rangeSec = 1
	}
	step := stepSeconds
	if step <= 0 {
		buckets := maxDataPoints
		if buckets <= 0 {
			buckets = defaultPromBuckets
		}
		step = ceilDiv(rangeSec, buckets)
	}
	if step < 1 {
		step = 1
	}
	if rangeSec/step > maxPromPoints {
		step = ceilDiv(rangeSec, maxPromPoints)
	}
	if step < 1 {
		step = 1
	}
	return step
}

func ceilDiv(a, b int) int {
	if b <= 0 {
		return a
	}
	return int(math.Ceil(float64(a) / float64(b)))
}

// promLookbehindFloorSec — the floor for a rollup window the caller did not
// express. Five minutes.
//
// Not a taste constant: it stands in for a number VM will not tell us.
// VictoriaMetrics itself widens an OMITTED window to
// max(step, scrape_interval) for rate() and default_rollup(), precisely so a
// step finer than the sample interval does not punch holes in the chart. We
// write windows explicitly (rule 3), so that widening becomes our job — and
// the scrape interval is not readable from VM, so the floor substitutes for
// it. 5m clears every OTLP export interval seen in practice (10s…120s) and
// is Prometheus' own staleness constant, so it is the one number an operator
// would guess.
//
// The CH sibling solves the identical problem from the other end: it PROBES
// the export interval and clamps the STEP up to it (clampStepToExport,
// metric_export_interval.go). VM has no such probe, so on this side the
// clamp lives in the window.
const promLookbehindFloorSec = 300

// promRollupWindow resolves the `[W]` lookbehind for a rollup translation.
//
// The three rollups deliberately do NOT share one rule, and the asymmetry is
// VM's own:
//
//	rate           → max(step, RateWindowSec), then floored. A wider window
//	                 only SMOOTHS a per-second rate, it cannot rescale it, so
//	                 the floor is free. VM's own default for an omitted rate
//	                 window is max(step, scrape_interval) — same widening.
//	last_over_time → max(step, floor). Widening is a NO-OP whenever the
//	                 narrow window already held a sample (last_over_time
//	                 takes the last one either way), so the floor changes
//	                 exactly one thing: a bucket that would have been a hole
//	                 now carries the previous reading — which is what the
//	                 gauge's value at that instant actually was. That is
//	                 default_rollup's staleness behaviour, written out.
//	increase       → max(step, RateWindowSec), NEVER floored. increase is a
//	                 window TOTAL: quadrupling the window quadruples the
//	                 number while the chart still says one bucket. That is
//	                 the v0.6.36 unit-scale class, and VM agrees — its
//	                 default for increase() is `step`, not
//	                 max(step, scrape_interval).
//
// An EXPLICIT caller window always wins, unfloored: RateWindowSec is the
// operator's Grafana reference ([3m]) arriving through the API, and silently
// promoting it to 5m would answer a question they did not ask. A window
// `<= step` counts as unexpressed, which is the CH sibling's own rule
// (rollingRate returns its input untouched when windowSec <= stepSec).
// RateWindowSec never reaches last_over_time — it is a RATE window, and
// bending `last` with it would be a second meaning for one field.
func promRollupWindow(rollup string, stepSeconds, rateWindowSec int) int {
	step := stepSeconds
	if step < 1 {
		// Mirrors promStep's floor: [0s] is a VM error.
		step = 1
	}
	if rollup == rollupRate || rollup == rollupIncrease {
		if rateWindowSec > step {
			return rateWindowSec
		}
	}
	if rollup == rollupRate || rollup == rollupLast {
		if step < promLookbehindFloorSec {
			return promLookbehindFloorSec
		}
	}
	return step
}

// seriesGroupKey rebuilds the CH path's GroupKey tuple from a VM series'
// label set: one entry per requested group-by key, in the SAME ORDER the
// caller asked for.
//
// Order is the whole point. SpanMetricSeries.GroupKey is a positional
// tuple the frontend joins with "|" to label the line; iterating VM's
// label MAP instead would relabel series between polls at random. A
// group-by key VM has no label for yields "" rather than shifting the
// tuple left — a shifted tuple mislabels every remaining dimension.
func seriesGroupKey(groupBy []string, labels map[string]string) []string {
	if len(groupBy) == 0 {
		return nil
	}
	out := make([]string, 0, len(groupBy))
	for _, g := range groupBy {
		out = append(out, labels[promLabel(g)])
	}
	return out
}

// pageNames applies the client-side pattern filter + pagination envelope
// for ListMetricNames. Pure — VM's /label/__name__/values has neither a
// substring filter nor an offset, so both live here.
//
// `unlimited` reproduces the CH path's defaultUnlimited branch (no
// pattern, no limit, no offset → the whole list, total = its length) so
// the legacy /api/metrics/names shape is unchanged.
func pageNames(all []string, pattern string, limit, offset int, unlimited bool) (names []string, total int) {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	matched := make([]string, 0, len(all))
	for _, n := range all {
		if n == "" {
			continue
		}
		if pattern != "" && !strings.Contains(strings.ToLower(n), pattern) {
			continue
		}
		matched = append(matched, n)
	}
	total = len(matched)
	if unlimited {
		return matched, total
	}
	if limit <= 0 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return []string{}, total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return matched[offset:end], total
}
