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
//  1. The METRIC NAME is never REPLACED — but since v0.9.1159 it is
//     ACCOMPANIED. OTLP names live in VM as `jvm.memory.used`, as
//     `jvm_memory_used`, or as `jvm_memory_used_bytes` depending on how the
//     operator's write path named them, and we cannot know which — so we
//     send every spelling the OTel→Prometheus convention could have
//     produced and let the DATA decide which one answers (names.go). That
//     forces the `{__name__=…}` selector form (a bare `jvm.memory.used{}`
//     is not valid MetricsQL), which happens to work for EVERY spelling.
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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/promapi"
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

// ErrUnfilteredBuckets marks a query Coremetry CAN express but REFUSES to
// send, because on a large VM install it would scan the whole bucket family
// (v0.9.1164).
//
// Deliberately a SECOND sentinel rather than a reuse of ErrUnsupported, and
// the difference is the operator's next move. ErrUnsupported means "this
// query has no MetricsQL form" — nothing they change in Settings will make
// it work, they have to ask a different question. This one means "this
// query is fine and this install has it switched off": the fix is either a
// filter or a checkbox, and the message names both. Folding the two into
// one sentinel would make the checkbox undiscoverable, since the refusal
// would read as a permanent limitation of the backend.
//
// Both map to 400 (internal/api/metricsource.go, upstream) — a refusal is a
// statement about the REQUEST, and a 502 here would send the operator to
// check a VictoriaMetrics that is perfectly healthy.
var ErrUnfilteredBuckets = errors.New("unfiltered bucket-family query refused by the VictoriaMetrics guard")

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
//
// v0.9.1157 — p50/p95/p99 joined it (Faz 2). The string is what a refusal
// message prints, so the percentiles being IN it is the operator-visible
// half of "histograms translate now".
const promSupportedAggs = "avg, sum, min, max, count, last, rate, increase, p50, p95, p99"

// bucketSuffix — the series suffix every OTLP→Prometheus write path gives
// an explicit histogram's per-bucket counter.
const bucketSuffix = "_bucket"

// labelLE — the bucket upper-bound label. Prometheus/MetricsQL spell it
// `le` ("less than or equal"), and `histogram_quantile` CONSUMES it: the
// aggregation feeding the function must group by it or the function has no
// distribution to read.
const labelLE = "le"

// labelVMRange — VictoriaMetrics native histogram kova etiketi (v0.10.391).
const labelVMRange = "vmrange"

// promOpts carries the SETTINGS-derived knobs the translation needs
// (v0.9.1164). It exists so promql.go can stay what its header claims:
// pure, a function of its arguments, table-testable without a live VM.
//
// The alternative was a package-level var the Service writes on Configure().
// That would have been two lines shorter and would have broken rule 3 in the
// worst possible way — the expression would depend on when an admin last
// pressed Save, so a table test could pass while a live poll rendered a
// different window, and the cache key upstream (metric-query:v3) would stop
// describing the query it keys. Settings are READ at the calling layer
// (client.go / histogram.go, from the one cfg snapshot ready() returned) and
// DESCEND as an argument; nothing under here reaches for live state.
//
// The zero value is the SHIPPED DEFAULT for both fields — a caller that
// forgets to fill it in gets the pre-v0.9.1164 behaviour (300s floor, guard
// ON), never a silently loosened one.
type promOpts struct {
	// RateWindowFloorS is the persisted override, NOT the resolved floor:
	// 0 means "use promLookbehindFloorSec". The resolution lives in exactly
	// one place (resolveRateWindowFloor, called from promRollupWindow) so a
	// caller cannot skip it and accidentally emit `[0s]`.
	RateWindowFloorS int
	// AllowUnfilteredPercentiles opts OUT of the bucket-scan guard. False —
	// the zero value — is the PROTECTED state; see guardBucketScan.
	AllowUnfilteredPercentiles bool
}

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
	// Quantile > 0 → the whole shape is wrapped in histogram_quantile and
	// the SELECTOR NAME becomes the `_bucket` series (v0.9.1157, Faz 2):
	//
	//	histogram_quantile(0.99, sum by (le) (rate({__name__="x_bucket"}[Ws])))
	//
	// Zero means "not a percentile". A float rather than a bool + value
	// pair because φ is the only thing the branch needs and 0 is not a
	// percentile Coremetry offers — p50 is the smallest.
	Quantile float64
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
// v0.9.1157 (Faz 2) adds p50/p95/p99. Faz 1/1.5 refused them, and the
// stated reason was right for the time: a percentile is not an operator
// swap, it needs the histogram BUCKET series. That is exactly what this
// release supplies —
//
//	histogram_quantile(φ, sum by (le) (rate({__name__="<name>_bucket"}[W])))
//
// — the canonical idiom, in both dialects.
//
// The old refusal also named a SECOND obstacle: choosing between a bucket
// quantile and a value-quantile needs the instrument type, which VM does not
// report. That one is answered by committing rather than by detecting. This
// translation is UNCONDITIONALLY the bucket form; when the metric turns out
// not to be a histogram, the `_bucket` selector matches nothing and the
// result is EMPTY, not wrong. An empty answer with a note that names the
// series we looked for (see QueryMetricNoted) is a diagnosis the operator
// can act on. Guessing a value-quantile from a heuristic would be the other
// kind of answer — plausible, unverifiable, and off by however much the
// distribution is skewed (the v0.9.566 class).
//
// The rollup is `rate`, matching every Grafana dashboard and the Prometheus
// docs. `increase` would give the same φ (histogram_quantile reads bucket
// RATIOS, so any positive rescaling of every bucket leaves the answer
// unchanged), so the choice is entirely about which expression an operator
// recognises in VM's query log. The heatmap path picks the other one, and
// for a reason that is NOT cosmetic — see buildHistogramPromQL.
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
	}
	if q, ok := promPercentile(agg); ok {
		return promAgg{Op: "sum", Rollup: rollupRate, Quantile: q}, nil
	}
	return promAgg{}, fmt.Errorf("aggregation %q is %w (supported: %s)",
		agg, ErrUnsupported, promSupportedAggs)
}

// promPercentile maps a Coremetry percentile label to φ.
//
// The THREE labels are the whole set on purpose: they are what
// MetricQueryFilter.Aggregation can carry and what the CH sibling
// implements, so accepting `p90` here would translate a query the builder
// cannot produce and the other backend cannot answer.
func promPercentile(agg string) (float64, bool) {
	switch strings.ToLower(strings.TrimSpace(agg)) {
	case "p50":
		return 0.50, true
	case "p95":
		return 0.95, true
	case "p99":
		return 0.99, true
	}
	return 0, false
}

// bucketMetricName applies the `_bucket` naming rule.
//
// Rule 1 still holds — the operator's name is not TRANSLATED, only
// SUFFIXED. Whichever spelling their write path produced
// (`http.server.request.duration` or `http_server_request_duration`), the
// bucket series is that same string plus `_bucket`, because the suffix is
// appended by the OTLP→Prometheus conversion AFTER any name sanitisation.
//
// An already-suffixed name is left alone. The catalogue in VM mode lists
// RAW VM series names (ListMetricNames reads /label/__name__/values), so
// `http_server_request_duration_bucket` is a row an operator can click, and
// `…_bucket_bucket` would match nothing while looking like a typo they made.
//
// What this deliberately does NOT do is strip `_sum` / `_count`. Those are
// sibling series of the same histogram, so "did you mean the buckets?" is a
// reasonable guess — but it is a GUESS, and a wrong one silently answers a
// question the operator did not ask. Left alone, `x_count` + p99 asks for
// `x_count_bucket`, finds nothing, and returns the empty-with-a-note shape
// that says which series was missing. The operator then picks the right row.
func bucketMetricName(name string) string {
	return histogramPartName(name, bucketSuffix)
}

// histogramPartName is the rule above, generalised over the part suffix
// (v0.9.1160, when `_sum` and `_count` joined `_bucket` as series the
// translation has to name).
//
// bucketMetricName stays as the named entry point because the `_bucket` rule is
// referenced by name all over this package and its incident history; this is
// the same rule with the suffix lifted out, not a second one.
func histogramPartName(name, part string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.HasSuffix(name, part) {
		return name
	}
	return name + part
}

// formatQuantile renders φ for the expression.
//
// 'g' with -1 precision gives the shortest exact round-trip: 0.5, 0.95,
// 0.99 — never "0.500000". The expression text rides in the API cache key
// only indirectly (the key carries `agg=p99`), but it DOES ride in VM's
// query log, where a stable spelling is what makes two polls of one panel
// recognisable as the same query.
func formatQuantile(q float64) string {
	return strconv.FormatFloat(q, 'g', -1, 64)
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
	case "LIKE":
		// v0.9.1199 — Faz 1'in ertelediği çeviri. CH tarafı LIKE'ı zaten
		// CONTAINS olarak derliyor (filterexpr.go: `%`+v+`%`); parite için
		// AYNI kompozit kalıp ("%v%") SQL-joker→RE2 çevirisinden geçer —
		// iki backend tek tanımdan türediği için ayrışamaz. PromQL =~ tam
		// anchor'lıdır; baş/son `.*` contains'i geri verir.
		return key + "=~" + quotePromString(likePatternToRegex("%"+vals[0]+"%")), nil
	case "NOT LIKE":
		return key + "!~" + quotePromString(likePatternToRegex("%"+vals[0]+"%")), nil
	}
	// Numeric comparisons have no MetricsQL label-matcher equivalent
	// (labels are strings; `n > 5` as a lexicographic regex would be the
	// silent-wrong-answer class). Refusing is the honest answer.
	return "", fmt.Errorf("filter operator %q is %w (supported: =, !=, =~, !~, IN, NOT IN, "+
		"LIKE, NOT LIKE, EXISTS, NOT EXISTS)", fe.Op, ErrUnsupported)
}

// likePatternToRegex — SQL LIKE kalıbının RE2 karşılığı: `%` → `.*`,
// `_` → `.`, `\%`/`\_` → literal, diğer her rune QuoteMeta. Faz 1'in
// "edge case'ler riskli" gerekçesindeki sınıf tam bu escape'lerdi;
// kalıbı rune-rune yürüyerek kapatıyoruz. Sondaki tek `\` literal
// kalır (CH LIKE de öyle okur).
func likePatternToRegex(pattern string) string {
	var b strings.Builder
	rs := []rune(pattern)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\\':
			if i+1 < len(rs) && (rs[i+1] == '%' || rs[i+1] == '_') {
				b.WriteString(regexp.QuoteMeta(string(rs[i+1])))
				i++
				continue
			}
			b.WriteString(regexp.QuoteMeta(`\`))
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(rs[i])))
		}
	}
	return b.String()
}

// ── The unfiltered-bucket-scan guard (v0.9.1164) ────────────────────────────
//
// WHY A GUARD AND NOT A FASTER QUERY. The operator's production VM carries
// ~12M active series and grew +281% in a day. At that size a selector whose
// ONLY matcher is the metric name is not a slow query, it is an unbounded
// one: `{__name__=~"a|b|c|d|e|f"}` is a regex over the whole name index, and
// for the bucket family every matching attribute set fans out again by `le`.
// Nothing downstream can shrink that — the by-clause groups AFTER the scan,
// the step only changes how many times VM runs it, and Coremetry's own
// max_execution_time equivalent does not exist on this path (VM's server-side
// limits are the operator's to set, and we cannot read them).
//
// So the honest options were: send it and let a shared vmselect degrade for
// everyone, or refuse with an actionable message. The refusal is a 400 with
// the class, the fix and the off-switch written out, which is the shape the
// package already uses for every other "we could guess, so we say what we
// looked for instead" case (emptyBucketNote, emptyNameNote).
//
// WHICH CLASSES ARE IN — operator decision, 2026-08-18:
//
//	percentile p50/p95/p99  → ALWAYS guarded. The branch reads
//	                          `<name>_bucket` unconditionally, so the `le`
//	                          fan-out is there even when the operator picked
//	                          an explicitly-suffixed row.
//	histogram heatmap       → ALWAYS guarded. Same `_bucket` selector, and
//	                          it additionally materialises a time × le grid
//	                          in Go (maxHistogramLEBuckets is the backstop
//	                          for cardinality, not for scan cost).
//	avg with no rollup      → guarded ONLY when the name may carry histogram
//	                          parts, i.e. exactly when the `or` composition
//	                          adds the `_sum`/`_count` ratio arm. A plain
//	                          gauge avg is one selector over one family and
//	                          stays free — guarding it would break the most
//	                          ordinary chart on the install to protect
//	                          against a cost it does not have.
//
// AND WHICH ARE DELIBERATELY OUT: rate / increase. Their histogram arm reads
// `_count`, which is ONE series per attribute set — the same cardinality as
// the gauge arm beside it, no `le` multiplication. The asymmetry with avg is
// real and was decided rather than derived (avg is in by operator call; its
// arm is `_sum`/`_count` too), so it is pinned by a test that asserts an
// unfiltered rate on a histogram name still PASSES. A future reader who
// notices the inconsistency should find it recorded here, not fix it silently.
//
// The raw MetricsQL proxy (QueryPromQLRange) is out for a structural reason,
// not a cost one: nothing on that path parses the operator's query, and that
// is the feature (see its header). A guard there would need the parser the
// package refuses to run.

// unfilteredBucketMsg builds the refusal.
//
// It names three things because a refusal with any of them missing is a dead
// end: WHICH query class was refused (the operator has several panels open
// and cannot tell which one 400'd), WHAT to add (service or label filter),
// and WHERE the off-switch is (requirement: the message states the guard in
// force and how to lift it). The Settings path is spelled exactly as the tab
// and checkbox read on screen, so it can be followed by search rather than by
// guesswork.
func unfilteredBucketMsg(class string) string {
	return fmt.Sprintf("%s sorgusu servis ya da etiket filtresi olmadan bu VictoriaMetrics "+
		"kurulumunda tüm kova serilerini taratır — sorguya bir servis ya da etiket filtresi "+
		"ekleyin. Devredeki koruma: “Filtresiz yüzdeliklere izin ver” KAPALI "+
		"(Settings → Metrik okuma backend’i); korumayı oradan açarak bu sorguyu "+
		"olduğu gibi çalıştırabilirsiniz.", class)
}

// Query-class labels the refusal prints. Constants because the decision table
// test asserts on them and a typo would otherwise be a silently different
// message rather than a compile error (the promRollupWindow precedent).
const (
	classPercentile = "Yüzdelik (p50/p95/p99)"
	classHeatmap    = "Histogram ısı haritası"
	classAvgFamily  = "Histogram ailesinde avg"
)

// guardBucketScan refuses an unscoped bucket-family query.
//
// `narrowers` is the COUNT of matchers that actually landed in the selector
// besides the name — service plus every translated filter. Counting the
// rendered matchers rather than the request fields is the load-bearing part:
// it measures what VM will receive, so a filter that failed to translate
// cannot be counted as scope (it 400s on its own first), and a future field
// that adds a matcher is covered without touching this function.
//
// GroupBy is deliberately NOT scope. A by-clause aggregates after the scan;
// counting it would let `by (le)` — which every percentile carries — satisfy
// the guard and disable it everywhere.
func guardBucketScan(class string, narrowers int, allow bool) error {
	if allow || narrowers > 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrUnfilteredBuckets, unfilteredBucketMsg(class))
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

// ── Selector atoms, shared by every builder in the package ─────────────────
//
// Extracted in v0.9.1268, when the service-Overview throughput mapper gained a
// VictoriaMetrics arm and needed to render a `_count`-only rate — the one shape
// buildPromQL cannot express, because its rate branch always composes the base
// family `or` the histogram family.
//
// EXTRACTED RATHER THAN COPIED, and the difference is the whole point. A second
// hand-written matcher loop is where an arm quietly loses a filter: it would
// answer the operator's question about one service with every service's traffic,
// and nothing on screen would say so (the v0.9.566 class this file keeps
// citing). One definition means the throughput arm and the Explore arm cannot
// scope differently.

// selectorMatchers renders the NON-NAME half of a selector: the service scope
// first, then one matcher per filter. Returns the refusal verbatim when a
// filter operator has no MetricsQL equivalent — the caller turns that into a
// 400, never into a dropped conjunct.
func selectorMatchers(f chstore.MetricQueryFilter) ([]string, error) {
	out := make([]string, 0, len(f.Filters)+1)
	if svc := strings.TrimSpace(f.Service); svc != "" {
		out = append(out, serviceLabel()+"="+quotePromString(svc))
	}
	for _, fe := range f.Filters {
		m, err := promMatcher(fe)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// promSelector joins a candidate name matcher with those matchers.
//
// The `append([]string{…}, extra...)` allocates a FRESH slice every call: the
// callers reuse one `extra` across several arms, and appending into it in place
// would let arm N+1 inherit arm N's name matcher.
func promSelector(candidates, extra []string) string {
	return "{" + strings.Join(append([]string{nameMatcher(candidates)}, extra...), ", ") + "}"
}

// groupByLabels maps the caller's group-by keys into label names, dropping the
// ones that sanitize to nothing.
func groupByLabels(groupBy []string) []string {
	out := make([]string, 0, len(groupBy)+1)
	for _, g := range groupBy {
		if l := promLabel(g); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// buildPromQL renders the whole query expression for a MetricQueryFilter.
// Pure — table-tested in promql_test.go.
//
// `opts` carries the two settings-derived knobs (v0.9.1164). They arrive as an
// argument rather than being read here so this stays a pure function of the
// REQUEST plus the CONFIG SNAPSHOT the caller already holds — see promOpts.
func buildPromQL(f chstore.MetricQueryFilter, opts promOpts) (string, error) {
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

	// v0.9.1157 — a percentile reads the BUCKET series, not the metric the
	// operator named. v0.9.1159 — and either way it reads a LIST of candidate
	// spellings rather than one (names.go). v0.9.1160 — and a query may read
	// SEVERAL series families, one per `or` arm, so the selector is built per
	// candidate list rather than once. Resolved here rather than in
	// promAggregator so the name rule has exactly one home and the aggregator
	// stays a pure label→shape map.
	extra, err := selectorMatchers(f)
	if err != nil {
		return "", err
	}
	// Every arm carries the SAME service + filter matchers. Sharing them is not
	// tidiness: an arm that lost a filter would answer the operator's question
	// about one route with data from all of them, and `or` would let that arm
	// fill in wherever the correctly-filtered arm was empty — the v0.9.566 shape
	// with a fallback attached.
	sel := func(cands []string) string {
		return promSelector(cands, extra)
	}

	// The window is resolved from the SAME promStep call the client uses for
	// the query_range `step` param, over the same already-normalized filter.
	// It is recomputed here rather than threaded in as an argument BECAUSE
	// promStep is pure: same inputs, same number, so the expression and the
	// step param cannot drift. Passing it in would be the version that can
	// drift — a caller free to send one number and render another.
	step := promStep(f.From, f.To, f.StepSeconds, f.MaxDataPoints)

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
	labels := groupByLabels(f.GroupBy)

	// PERCENTILES ALWAYS CARRY A BY-CLAUSE, and `le` always comes first.
	//
	// `le` is not one of the operator's grouping dimensions — it is the
	// function's INPUT. histogram_quantile reads the distribution out of the
	// `le`-labelled members of each group, so an aggregation that dropped
	// `le` would hand the function a single collapsed number and the result
	// would be NaN, not a percentile. That is why this branch cannot reuse
	// the "no group-by → bare op(vec)" shape below.
	//
	// First rather than last purely so the expression reads the way the
	// Prometheus documentation writes it; MetricsQL does not care about the
	// order. A caller who literally asked to group by `le` is deduped — a
	// repeated label inside by() is accepted by VM but the intent is already
	// covered, and printing it twice would make two polls of the same panel
	// look like different queries in VM's log.
	if agg.Quantile > 0 {
		// v0.9.1164 — the guard sits BEFORE the expression is rendered, and
		// `len(extra)` is the whole test: `extra` holds the service matcher plus
		// every translated filter, so zero means the selector would go to VM with
		// nothing but the name alternation on it.
		if err := guardBucketScan(classPercentile, len(extra), opts.AllowUnfilteredPercentiles); err != nil {
			return "", err
		}
		// v0.10.391 (dış skill denetimi A3) — `vmrange` de by-listesinde:
		// VictoriaMetrics OTLP exponential histogramı kendi biçimine
		// (`<ad>_bucket{vmrange="lo...hi"}`) çevirir, `le` taşımaz. Yalnız
		// `by (le)` o serileri tek kovaya (le="") çökertip histogram_quantile'a
		// sessizce yanlış bir p95 verdiriyordu. MetricsQL histogram_quantile
		// le VE vmrange kovalarını anlar; klasik kovada vmrange boş etiket
		// olarak zararsız kalır.
		by := make([]string, 0, len(labels)+2)
		by = append(by, labelLE, labelVMRange)
		for _, l := range labels {
			if l != labelLE && l != labelVMRange {
				by = append(by, l)
			}
		}
		w := promRollupWindow(agg.Rollup, step, f.RateWindowSec, opts.RateWindowFloorS)
		vec := agg.Rollup + "(" + sel(bucketNameCandidates(name)) + "[" + strconv.Itoa(w) + "s])"
		return "histogram_quantile(" + formatQuantile(agg.Quantile) + ", " +
			agg.Op + " by (" + strings.Join(by, ", ") + ") (" + vec + "))", nil
	}

	base := plainNameCandidates(name)

	// ── `or` COMPOSITION FOR THE HISTOGRAM FAMILIES (v0.9.1160) ─────────────
	//
	// In VM an OTLP histogram has NO base series — only `_bucket`, `_sum` and
	// `_count`. So `rate(http.server.request.duration)` and
	// `avg(http.server.request.duration) by (http.route)` both resolved to
	// nothing, which is what the operator saw in prod: a working latency chart
	// beside a permanently blank throughput one, and an empty "Response time ·
	// avg (by route)" panel on the service page.
	//
	// The fix is MetricsQL `or`, not a wider alternation, and the difference is
	// the whole design:
	//
	//	`a or b` is a set UNION with LEFT PRECEDENCE PER GROUP. Where the left
	//	arm produces a series, that series wins; groups present only in the right
	//	arm are filled from the right. So a real base series (a plain counter, a
	//	gauge) always beats the guessed histogram arm, and the histogram arm only
	//	speaks where the base was silent.
	//
	// An alternation would instead SUM the two families, which is how the first
	// cut of this release would have double-counted a metric that has both `x`
	// and `x_count`. `or` makes that impossible: it is probe-free
	// self-selection with a deterministic tiebreak, so nothing has to know in
	// advance which family the operator's write path produced.
	//
	// The arms are gated by mayHaveHistogramParts, so a `_total` counter or an
	// explicitly-picked `_bucket`/`_sum`/`_count` row renders EXACTLY the
	// single-arm expression it did before v0.9.1160 — byte for byte.
	//
	// NOT guarded by the bucket-scan check (v0.9.1164): this arm reads
	// `_count`, one series per attribute set — the same cardinality as the
	// gauge arm beside it, with no `le` fan-out. See the guard's header for the
	// full in/out table and why the asymmetry with avg is recorded rather than
	// smoothed over.
	if agg.Rollup == rollupRate || agg.Rollup == rollupIncrease {
		w := promRollupWindow(agg.Rollup, step, f.RateWindowSec, opts.RateWindowFloorS)
		rollup := func(cands []string) string {
			return agg.Rollup + "(" + sel(cands) + "[" + strconv.Itoa(w) + "s])"
		}
		left := aggregateExpr(agg.Op, labels, rollup(base))
		if !mayHaveHistogramParts(name) {
			return left, nil
		}
		// The histogram's throughput IS its `_count` counter — the same
		// expression every Grafana dashboard rates.
		return left + " or " + aggregateExpr(agg.Op, labels, rollup(countNameCandidates(name))), nil
	}

	if agg.Op == "avg" && agg.Rollup == "" {
		left := aggregateExpr("avg", labels, sel(base))
		if !mayHaveHistogramParts(name) {
			// PLAIN GAUGE avg — one arm, one family, no guard (v0.9.1164). This
			// early return is also WHY the guard is checked below rather than at
			// the top of the branch: the protected shape and the free shape are
			// separated by exactly this predicate, and the operator's decision was
			// scoped to "the name carries histogram-part candidates".
			return left, nil
		}
		if err := guardBucketScan(classAvgFamily, len(extra), opts.AllowUnfilteredPercentiles); err != nil {
			return "", err
		}
		// OBSERVATION-WEIGHTED MEAN, and the weighting is the point. A
		// histogram's mean is rate(_sum) / rate(_count): the total observed
		// value over the number of observations. That is the same semantics the
		// ClickHouse sibling computes (v0.9.776), so the two backends agree on
		// "avg latency" by construction rather than by luck — an unweighted
		// average of per-series means would drift from CH by however unevenly
		// traffic was spread across the group.
		//
		// A rate() on both halves rather than the raw counters because the
		// counters are cumulative since process start: their ratio would be the
		// all-time mean, which barely moves and hides every incident. The window
		// is the rate window (floored), since both halves ARE rates.
		w := promRollupWindow(rollupRate, step, f.RateWindowSec, opts.RateWindowFloorS)
		ratePart := func(cands []string) string {
			return aggregateExpr("sum", labels,
				rollupRate+"("+sel(cands)+"["+strconv.Itoa(w)+"s])")
		}
		return left + " or (" + ratePart(sumNameCandidates(name)) +
			" / " + ratePart(countNameCandidates(name)) + ")", nil
	}

	// min / max / sum / count / last — ONE arm, unchanged.
	//
	// Deliberately NOT given a histogram arm: `min(…_seconds_count)` reports a
	// sample COUNT where the operator asked for a measurement, and
	// `last_over_time(…_seconds_count)` returns the cumulative total since
	// process start. Both are large, plausible and wrong under a latency legend
	// (v0.9.566). They return honestly empty for a histogram family and the note
	// names every spelling that was tried, which is a diagnosis the operator can
	// act on — switch the aggregation to avg, or to p95.
	vec := sel(base)
	if agg.Rollup != "" {
		w := promRollupWindow(agg.Rollup, step, f.RateWindowSec, opts.RateWindowFloorS)
		vec = agg.Rollup + "(" + vec + "[" + strconv.Itoa(w) + "s])"
	}
	return aggregateExpr(agg.Op, labels, vec), nil
}

// aggregateExpr wraps a vector in a set-aggregation, with or without a
// by-clause. Extracted in v0.9.1160 because an `or` composition renders the
// same shape two or three times and a hand-repeated `op + " by (" + …` is
// exactly where an arm loses its grouping — which `or` would then paper over by
// filling the ungrouped arm's single series into every group.
func aggregateExpr(op string, labels []string, vec string) string {
	if len(labels) == 0 {
		// No group-by → one series aggregated across everything, which is
		// what the CH path's `GROUP BY bucket` alone produces.
		return op + "(" + vec + ")"
	}
	return op + " by (" + strings.Join(labels, ", ") + ") (" + vec + ")"
}

// emptyBucketNote explains a percentile that came back with ZERO series.
//
// It exists because that outcome is AMBIGUOUS in a way no other empty result
// is. Every other query asks VM for a series the operator picked from the
// catalogue; a percentile asks for `<name>_bucket`, a name they never typed
// and cannot see on screen. So "no data in this window", "this metric is not
// a histogram" and "your write path spelled the buckets differently" all
// render as the same blank chart, and the operator's next move depends on
// which one it was.
//
// v0.9.1159 — it now LISTS the spellings that were tried. Once the selector
// became a candidate alternation, "…_bucket bulunamadı" stopped being the
// whole truth: the query asked for up to six names, none of which the
// operator typed, and the useful next move depends on which ones were missed.
// Seeing the list is also the only way they can tell that the Prometheus-unit
// spellings WERE attempted — otherwise the fix they would reach for
// (v0.9.1159 itself) looks unshipped.
//
// Pure, and it takes the EXACT list the query used rather than a metric name
// to re-derive one from: a note free to name a different series than the
// query asked for is the version that can lie (the assembleHistogram
// precedent).
func emptyBucketNote(candidates []string) string {
	return fmt.Sprintf("Kova serisi bulunamadı — yüzdelikler histogram kova serisini "+
		"(…%s + le etiketi) okur. Denenen yazımlar: %s. Bu metrik histogram olmayabilir, "+
		"pencerede veri olmayabilir, ya da write yolu kovaları başka bir adla yazıyor "+
		"olabilir (VictoriaMetrics'te metrik adını …%s ile arayın).",
		bucketSuffix, strings.Join(candidates, ", "), bucketSuffix)
}

// emptyNameNote explains a NON-percentile query that came back with ZERO series
// after the translation guessed more than one spelling (v0.9.1160).
//
// It exists for the case the live check of v0.9.1159 called out by name: a
// silent empty. The candidates are names the operator never typed and cannot see
// anywhere on screen, so "no data in this window", "this metric is spelled
// differently" and "this aggregation cannot read this metric's shape" all render
// as one blank chart with three different fixes.
//
// The last clause is the one that earns its length. min/max/sum/count/last are
// deliberately given NO histogram arm (see buildPromQL), so on an OTLP histogram
// they are empty BY DESIGN — and without being told, an operator reads that as a
// broken backend rather than as "ask for avg or p95 instead". Naming the fix is
// the difference between a diagnosis and a dead end.
func emptyNameNote(candidates []string) string {
	return fmt.Sprintf("Seri bulunamadı — denenen yazımlar: %s. Metrik bu pencerede hiç "+
		"örnek almamış olabilir, ya da write yolu onu başka bir adla yazıyor olabilir. "+
		"OTLP histogramlarında TABAN seri yoktur (yalnız …%s / …%s / …%s): avg, rate ve "+
		"increase bu parçaları otomatik dener, min/max/sum/count/last denemez — "+
		"histogram bir metrikte bu toplamaları avg ya da p95 ile değiştirin.",
		strings.Join(candidates, ", "),
		bucketSuffix, histogramSumSuffix, histogramCountSuffix)
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

// The bounds an OPERATOR-SET floor must satisfy (v0.9.1164). The setting
// exists because 5m is a substitute for a number VM will not tell us (see
// above), and a substitute is exactly the kind of constant an install with a
// known export interval should be able to correct: a 10s-scrape Prometheus
// federation wants ~30s here, not 300s, and the 5m default is currently
// smoothing five minutes of detail out of every rate chart on that install.
//
//	10s floor  — below the finest OTLP export interval anyone runs, and a
//	             window under ~10s makes rate() dependent on scrape jitter.
//	3600s ceiling — an hour is already wider than any panel's step at the
//	             ranges Coremetry offers; past it the "floor" would BE the
//	             window on every chart, which is a different feature (and
//	             one the caller-supplied RateWindowSec already provides
//	             per-query).
//
// 0 stays the sentinel for "unset — use the default". A separate `null`
// spelling would only add a second way to say the same thing to the JSON
// blob, and the frontend already renders 0 as an empty box (the aiTuning
// contract).
// Exported because the PUT validator lives in internal/api and must reject
// exactly what the reader would ignore. The bounds having ONE spelling is the
// point: a second copy in the handler is how a value gets accepted by the form
// and then silently dropped by the query — the operator sees their number
// saved in Settings and the old window in the chart, with nothing on screen
// connecting the two.
const (
	MinRateWindowFloorSec = 10
	MaxRateWindowFloorSec = 3600
)

// ValidRateWindowFloor reports whether a persisted/submitted floor is
// acceptable: unset (0), or inside the bounds. Shared by the PUT validator
// (which 400s on false) and by resolveRateWindowFloor.
func ValidRateWindowFloor(v int) bool {
	return v == 0 || (v >= MinRateWindowFloorSec && v <= MaxRateWindowFloorSec)
}

// resolveRateWindowFloor maps the persisted setting to the floor to apply.
//
// An out-of-bounds value falls back to the DEFAULT rather than being clamped.
// The PUT gate makes it unreachable through the UI, so the only way to get
// here is a hand-edited system_settings blob — and for that case the default
// is the honest answer ("your value was not accepted"), where clamping would
// silently answer with a third number the operator never typed and cannot see
// anywhere.
func resolveRateWindowFloor(v int) int {
	if v == 0 || !ValidRateWindowFloor(v) {
		return promLookbehindFloorSec
	}
	return v
}

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
//
// v0.9.1164 — the floor is now a PARAMETER (`floorS`, the raw persisted
// setting; 0 = default). Three properties of that plumbing are load-bearing:
//
//   - the value is resolved HERE, once, so a caller who passes the zero value
//     gets 300s rather than a floor of 0 that would silently delete the
//     widening. The unsafe direction is unreachable by omission.
//   - `increase` is untouched by it. The floor multiplies a window TOTAL, and
//     an operator raising the floor to smooth their rate charts must not
//     silently quadruple every increase() number — the v0.6.36 unit-scale
//     class, which is why the setting cannot reach this rollup at all. The
//     heatmap path shares the same exemption for the same reason and does not
//     even take the option (buildHistogramPromQL).
//   - the function stays pure. Same arguments, same window; the expression and
//     the cache key upstream cannot drift apart because an admin pressed Save
//     between two polls of one panel.
func promRollupWindow(rollup string, stepSeconds, rateWindowSec, floorS int) int {
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
		if floor := resolveRateWindowFloor(floorS); step < floor {
			return floor
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

// labelSetGroupKey names a series the RAW-QUERY proxy returned
// (v0.9.1157, Faz 2 — GET /api/metrics/promql on the VM path).
//
// This path has no requested group-by to position a tuple against: the
// operator wrote an arbitrary MetricsQL expression and VM answered with
// whatever label set survived it. So the identity has to come from the
// LABELS THEMSELVES, and every property below is about making that identity
// STABLE across polls, because SpanMetricSeries.GroupKey is what the
// frontend derives a line's colour, legend text and compare-ghost match
// from (seriesGroupLabel):
//
//   - one element per label, `k="v"`, so the legend reads the way
//     Prometheus writes a series and a value containing "|" (the
//     frontend's tuple separator) cannot forge an extra dimension;
//   - keys SORTED, because Go map iteration is randomised per range — an
//     unsorted tuple would relabel and recolour every line on every poll;
//   - `__name__` sorts first for free ('_' < any letter in ASCII), which
//     happens to put the metric name at the head of the legend the way an
//     operator would write it.
//
// An empty label set yields nil, matching the CH evaluator's own shape for
// a result with no grouping (scalarSeries: GroupKey nil).
func labelSetGroupKey(labels map[string]string) []string {
	if len(labels) == 0 {
		return nil
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+quotePromString(labels[k]))
	}
	return out
}

// ── Histogram bucket layout (v0.9.1157, Faz 2) ──────────────────────────────
//
// THE TWO AXES OF "CUMULATIVE", AND WHY ONLY ONE OF THEM IS TEMPORALITY.
//
// chstore.HistogramSeries — the shape /api/metrics/histogram returns and the
// frontend heatmap consumes (histogramHeatmap.ts) — wants, per time bucket, a
// vector of PER-BUCKET observation counts: counts[i] = observations that fell
// in (bounds[i-1], bounds[i]], with counts[N] the +Inf overflow. A Prometheus
// `_bucket` series is cumulative in BOTH directions, and each needs a
// different fix:
//
//  1. OVER TIME, each `<name>_bucket{le=…}` series is a monotonically
//     increasing counter since process start. VM undoes this for us:
//     `increase(…[step])` is a reset-protected window delta. It happens
//     upstream, so nothing here does temporality arithmetic — which is also
//     why the OTLP delta-vs-cumulative question never reaches this file. VM's
//     write path already resolved it when it materialised the counters.
//  2. ACROSS le, `le="0.1"` counts everything ≤ 0.1 — an inclusive PREFIX SUM.
//     This is not temporality at all and no rollup function undoes it; it is
//     what makes histogram_quantile possible. So the differencing below is
//     unconditional: subtract each bucket's prefix sum from the previous
//     bound's. Skipping it would put ~the whole population in the top bucket
//     and paint the heatmap as one bright band at the tail — a picture that
//     looks like a latency crisis.
//
// Both pieces are pure and table-tested (histogram_test.go) because both are
// silent when wrong: axis 2 mis-differenced yields a plausible heatmap, and
// the percentiles computed off it (PercentileFromBuckets) stay finite.

// bucketLayout turns a set of `le` label values into the read model's bucket
// layout.
//
// Returns the finite upper bounds ASCENDING + DEDUPED (len N, becoming
// HistogramSeries.Bounds), and slot[i] = the index in the (N+1)-wide counts
// vector that input i contributes to. `+Inf` maps to slot N — the overflow
// position — and is never a bound, because it is not a number the y-axis can
// place.
//
// THREE input properties are handled rather than assumed, and each has a real
// source:
//
//   - ARBITRARY ORDER. VM returns series in whatever order vmselect merged
//     them; there is no ordering guarantee on a query_range result.
//   - DUPLICATES. Our own `sum by (le)` cannot produce two series with the
//     same le, but a caller pointing this at a recording rule or a federated
//     view can. Two inputs sharing an le share a slot, and their counts ADD —
//     the same semantics the sum would have had.
//   - MISSING / UNPARSEABLE le. This is an ERROR, not a skip. A dropped
//     bucket does not empty the chart, it shifts every percentile and moves
//     mass into a neighbouring band — wrong-but-plausible, the v0.9.566 class.
//     The message names the offending value because the common cause is
//     recognisable on sight: a series with no `le` at all is usually not a
//     histogram bucket (VictoriaMetrics' own native histograms label buckets
//     `vmrange`, not `le`).
func bucketLayout(les []string) (bounds []float64, slot []int, err error) {
	if len(les) == 0 {
		return nil, nil, nil
	}
	parsed := make([]float64, len(les))
	for i, raw := range les {
		v, ok := leBound(raw)
		if !ok {
			return nil, nil, fmt.Errorf("histogram bucket series carries an unusable %q label %q — "+
				"this series is %w as a histogram bucket (a bucket series must label every member "+
				"with a numeric le, or +Inf for the overflow bucket)",
				labelLE, promapi.FirstN(raw, 40), ErrUnsupported)
		}
		parsed[i] = v
	}
	// Distinct finite bounds, ascending. sort.Float64s puts +Inf last, but
	// +Inf is filtered out before sorting so the slice is finite by
	// construction — a bound the frontend can position on an axis.
	seen := make(map[float64]struct{}, len(parsed))
	for _, v := range parsed {
		if math.IsInf(v, 1) {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		bounds = append(bounds, v)
	}
	sort.Float64s(bounds)
	index := make(map[float64]int, len(bounds))
	for i, b := range bounds {
		index[b] = i
	}
	slot = make([]int, len(parsed))
	for i, v := range parsed {
		if math.IsInf(v, 1) {
			// The overflow bucket sits one past the last finite bound. With
			// no finite bounds at all this is slot 0, and the caller treats
			// an empty `bounds` as "no usable layout" before it gets here.
			slot[i] = len(bounds)
			continue
		}
		slot[i] = index[v]
	}
	return bounds, slot, nil
}

// leBound parses one `le` label value into a bucket upper bound.
//
// Prometheus writes the overflow bucket's bound as `+Inf`; VM has emitted
// `Inf` and `inf` across versions, and a remote-write producer may send
// either, so all spellings are accepted. ParseFloat handles them natively —
// including `-Inf` and `NaN`, which is exactly why they are rejected
// explicitly below: a NaN bound cannot be ordered, and a negative-infinity
// bound would sort ahead of every real bucket and swallow the whole
// distribution into slot 0.
func leBound(v string) (float64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, -1) {
		return 0, false
	}
	return f, true
}

// deCumulateLE turns one time bucket's le-CUMULATIVE counts into PER-BUCKET
// counts (axis 2 above). cum is indexed by bucket slot: cum[i] = observations
// with value ≤ bounds[i], and cum[N] = the total including the +Inf overflow.
//
// Two guards, both of which change the answer rather than merely tidying it:
//
//   - A NEGATIVE difference is clamped to zero. It means the prefix sums are
//     not monotonic, which happens when a bucket series churned mid-window
//     (a pod restart lands one member's increase on a different grid slot
//     than its neighbour's). A negative COUNT has no meaning, and uint64
//     would wrap it into ~1.8e19 — a single cell that saturates the entire
//     heatmap's colour scale and makes every real value read as zero.
//   - The running reference only ever moves UP (`if c > prev`). Given
//     cum = [10, 8, 20] the alternative — carrying 8 forward — yields
//     10 + 0 + 12 = 22 observations from a histogram whose own total says 20.
//     Keeping the monotonic reference yields 10 + 0 + 10 = 20: the dip is
//     absorbed where it happened instead of being redistributed into the
//     tail, and the totals still agree with cum[N], which is what the
//     percentile estimator normalises against.
//
// Rounding is at the boundary and nowhere else: `increase()` over an integer
// counter is integral in principle, but it arrives as a float64 that has been
// through JSON, so 49.999999999999996 must become 50 rather than 49.
func deCumulateLE(cum []float64) []uint64 {
	out := make([]uint64, len(cum))
	prev := 0.0
	for i, c := range cum {
		if d := c - prev; d > 0 {
			out[i] = uint64(math.Round(d))
		}
		if c > prev {
			prev = c
		}
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
