package vmetrics

// Metric-NAME candidates — the bridge between Coremetry's OTLP-shaped metric
// names and the Prometheus-shaped names VictoriaMetrics actually stores
// (v0.9.1159).
//
// WHY THIS EXISTS: operator-reported, live in prod. Every VM panel came back
// empty. Nothing was broken in the translation — the SPELLINGS did not meet.
// Coremetry's panels, dashboards and Explore queries were built against the
// ClickHouse/OTLP catalogue, so they ask for `http.server.request.duration`.
// VM's live family is `http_server_request_duration_seconds_bucket` /
// `_sum` / `_count`. Rule 1 in promql.go ("the metric name is never
// translated") was the honest answer while we could not know which spelling
// the write path produced — but it made the VM backend unusable for the write
// path operators actually run, which is the OTel Prometheus naming
// convention:
//
//	http.server.request.duration (unit s, histogram)
//	  → dots sanitized:      http_server_request_duration
//	  → unit appended:       http_server_request_duration_seconds
//	  → histogram exploded:  …_seconds_bucket / …_seconds_sum / …_seconds_count
//
// So rule 1 is now narrower and sharper: we still never REPLACE the
// operator's name, we ADD the spellings the convention would have produced
// and let the DATA decide. The selector becomes a `__name__` alternation and
// whichever member has samples answers.
//
// WHY NOT A PROBE. The obvious alternative is to ask VM "which of these
// names exists?" (/label/__name__/values) and lock onto the hit. That is the
// wrong shape here, and the operator's own install is the counter-example:
// VM holds the dotted family TOO — it stopped two days ago when their write
// path switched to Prometheus naming, but the NAME is still in the label
// index for the whole retention. An existence probe locks onto the stale
// family and charts a flat line with total confidence. Existence is not
// liveness; only DATA is. An alternation asks the question the right way
// round, in the same round trip, with no extra state to go stale.
//
// THE COST, NAMED. An alternation SUMS its members, so a window that spans
// both a live and a stale family double-counts for the SUM-shaped
// aggregations (sum / count / rate / increase) over the overlap. Three things
// make that the better trade rather than a hidden second bug:
//
//   - it is BOUNDED and self-healing: the overlap shrinks every day the stale
//     family ages out of retention, and it is zero for any window after the
//     cutover — which is every default window on the page.
//   - it does not affect avg / min / max / last (two identical families
//     average to the same number) nor the percentiles (histogram_quantile
//     reads bucket RATIOS, so scaling every bucket leaves φ unchanged).
//   - the alternative fails in the direction that CANNOT be noticed. A
//     doubled counter on a 7d window is visible against the same panel at
//     24h; a probe that silently picked the dead family looks exactly like
//     "the service is idle".
//
// THE UNIT-MIXING COROLLARY (v0.6.36's class, in a new shape). The candidates
// include both `_seconds` and `_milliseconds` because we cannot know the
// instrument's unit from here — MetricQueryFilter does not carry one and VM
// reports none. They are ALTERNATIVES for one logical metric, so at most one
// exists and the alternation resolves it. If a write path ever published the
// same base name in two units at once, the alternation would sum seconds onto
// milliseconds, and no rescaling makes that meaningful. Nothing here can detect
// it, so it is stated rather than guarded — and it is the reason the suffix
// table is the CONVENTION's short list rather than every unit Prometheus knows:
// each extra unit is another way for two spellings of one metric to coexist.
// Every unit in the table is exercised individually by TestNameCandidates.
//
// And the operator keeps an exact pin: GET /api/metrics/promql takes raw
// MetricsQL and is deliberately untouched by any of this (histogram.go's
// QueryPromQLRange). If they write the name, they get the name.

import (
	"regexp"
	"strings"
)

// promUnitSuffixes — the unit suffixes the OTel→Prometheus name translation
// appends, in the order they go into the alternation.
//
// This is the CONVENTION's list, not a wishlist: the spec maps a metric's
// unit to a Prometheus base unit and appends it. `s`→`_seconds`,
// `ms`→`_milliseconds`, `By`→`_bytes`, `1`→`_ratio` (gauges only) cover every
// unit Coremetry's own instrumentation and the receivers it ingests actually
// emit. Deliberately NOT included: `_percent`, `_hertz`, `_per_second` and
// the rest of the long tail — each one widens the alternation for every
// query, and the double-count exposure in this file's header grows with it.
// Add one when a real metric needs it, not in advance.
var promUnitSuffixes = []string{"_seconds", "_milliseconds", "_bytes", "_ratio"}

// counterSuffix — appended to a MONOTONIC sum, and appended LAST, after the
// unit. That ordering is why the compounds below exist rather than being
// derivable: `process.cpu.time` (s, counter) is `process_cpu_time_seconds_total`
// in VM, not `…_total_seconds`.
const counterSuffix = "_total"

// promNamedSuffixes — the marker that a name has ALREADY been through
// Prometheus naming. When a candidate name ends in one of these, no suffix is
// guessed on top of it.
//
// The rule matters in both directions and each half has a real input:
//
//   - a name from VM's OWN catalogue (ListMetricNames lists raw series names,
//     and every picker in VM mode is fed from it) is already exact. Guessing
//     `…_seconds_seconds` or `…_bucket_ratio` would only widen the
//     alternation with names no write path can produce.
//   - the guess is not merely useless there, it is a RISK. `x_seconds` (a
//     gauge) and `x_seconds_total` (a counter) can both exist as genuinely
//     different metrics; alternating them sums two unrelated series into one
//     line. So an already-named name is taken as EXACT, and the guessing is
//     reserved for names that CANNOT exist in VM as written.
//
// Known cost, accepted: a metric legitimately named `queue.message.count`
// sanitizes to `queue_message_count`, ends in `_count`, and therefore never
// gets its `_total` candidate. That spelling is indistinguishable from a
// histogram's count series from here, and mis-reading a histogram part as a
// base metric is the more expensive mistake (it charts a sample COUNT as
// though it were the measurement). The operator's escape is the raw-query
// path.
var promNamedSuffixes = []string{
	"_seconds", "_milliseconds", "_bytes", "_ratio",
	counterSuffix, bucketSuffix, "_sum", "_count",
}

// histogramCountSuffix stands in for the whole histogram family during LABEL
// discovery — see discoveryNameCandidates.
const histogramCountSuffix = "_count"

var promMetricNameInvalid = regexp.MustCompile(`[^a-zA-Z0-9_:]`)

// promMetricName sanitizes an OTLP metric name the way every
// OTLP→Prometheus write path does: characters outside [a-zA-Z0-9_:] become
// `_`, and a leading digit gets an underscore prefix (a Prometheus metric
// name cannot start with one).
//
// This is the metric-name twin of promLabel and it is deliberately a
// CANDIDATE rather than a replacement — promMetricName's output goes into the
// alternation NEXT TO the verbatim name, never instead of it, because VM
// stores dotted names happily and the pre-cutover family in the operator's
// install proves it.
//
// Consecutive underscores are NOT collapsed. Older OTel exporters did
// collapse them, but the input that would expose the difference (two adjacent
// invalid characters, `a..b`) does not occur in OTel metric names — the
// dot-separated segments are non-empty by construction. A candidate for a
// name nothing emits is alternation width with no hit behind it.
func promMetricName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	name = promMetricNameInvalid.ReplaceAllString(name, "_")
	if name[0] >= '0' && name[0] <= '9' {
		name = "_" + name
	}
	return name
}

// nameSpellings builds the candidate list for a metric name.
//
// ORDER IS FIXED AND DEDUPED, and that is a correctness property rather than
// tidiness: the rendered expression rides in VM's query log, and rule 3 in
// promql.go requires two polls of one panel to produce the same text. A list
// assembled from a map, or one that let a duplicate through, would make one
// panel look like several queries.
//
// withCounter selects the instrument class. A histogram is never `_total`
// (the convention appends it to monotonic sums only), so the bucket path
// passes false and keeps four candidates instead of eight — every candidate
// it drops is a name that cannot exist.
func nameSpellings(name string, withCounter bool) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	out := make([]string, 0, 10)
	add := func(c string) {
		if c == "" {
			return
		}
		for _, existing := range out {
			if existing == c {
				return
			}
		}
		out = append(out, c)
	}

	// The operator's spelling always comes first: it is the only candidate we
	// KNOW someone asked for, and on a pre-cutover install it is the one that
	// answers.
	add(name)
	san := promMetricName(name)
	add(san)

	if hasAnySuffix(san, promNamedSuffixes) {
		return out
	}
	for _, u := range promUnitSuffixes {
		add(san + u)
	}
	if !withCounter {
		return out
	}
	add(san + counterSuffix)
	for _, u := range promUnitSuffixes {
		// `_ratio` marks a unit-1 GAUGE; a monotonic counter is not one, so
		// `_ratio_total` is a spelling the convention cannot produce.
		if u == "_ratio" {
			continue
		}
		add(san + u + counterSuffix)
	}
	return out
}

func hasAnySuffix(s string, suffixes []string) bool {
	for _, suf := range suffixes {
		if strings.HasSuffix(s, suf) {
			return true
		}
	}
	return false
}

// plainNameCandidates — the spellings for a query that reads the metric
// itself (avg / sum / min / max / count / last / rate / increase).
//
// A histogram metric has NO base series in VM: only `_bucket`, `_sum` and
// `_count` exist. So an avg over the operator's `http.server.request.duration`
// still comes back empty here, and that is the accepted outcome — deriving
// `_sum / _count` would be a second aggregation shape (a division of two
// range vectors) rather than a name, and the note mechanism from v0.9.1157
// already exists to explain a bucket-shaped miss.
func plainNameCandidates(name string) []string { return nameSpellings(name, true) }

// bucketNameCandidates — the spellings for the histogram BUCKET series,
// used by the percentile translation and by the heatmap.
//
// Composition order is unit-suffix FIRST, `_bucket` LAST, because that is the
// order the write path applies them: the unit belongs to the metric, the
// `_bucket` belongs to the histogram explosion that happens after naming.
// `…_seconds_bucket` is the real spelling; `…_bucket_seconds` is not a thing.
//
// bucketMetricName stays the single home of the `_bucket` rule (including its
// idempotence, which is what keeps a VM-catalogue row like
// `http_server_request_duration_seconds_bucket` from becoming `…_bucket_bucket`).
func bucketNameCandidates(name string) []string {
	base := nameSpellings(name, false)
	out := make([]string, 0, len(base))
	for _, c := range base {
		b := bucketMetricName(c)
		dup := false
		for _, existing := range out {
			if existing == b {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, b)
		}
	}
	return out
}

// nameCandidates is the aggregation-aware façade: it answers "which series
// names could satisfy THIS query?".
//
// The percentile branch reads the bucket series, so it gets the bucket
// spellings — derived from the same promPercentile map promAggregator uses,
// so the name rule and the query SHAPE can never disagree about whether a
// request is a percentile.
func nameCandidates(name, agg string) []string {
	if _, isPercentile := promPercentile(agg); isPercentile {
		return bucketNameCandidates(name)
	}
	return plainNameCandidates(name)
}

// discoveryNameCandidates — the spellings for LABEL discovery
// (MetricLabelValues, MetricAttrKeys).
//
// Discovery had the same empty-panel bug as the query path and one extra
// twist: for a histogram metric, the base name has no series at all, so
// "which keys can I filter on?" came back EMPTY for exactly the metric the
// operator cares about. A filter picker with nothing in it reads as "this
// metric has no attributes", which is a stronger and more wrong claim than a
// blank chart.
//
// `_count` alone stands in for the whole histogram family, and the choice is
// deliberate on both counts:
//
//   - `_bucket`, `_sum` and `_count` carry IDENTICAL attribute sets, so one
//     of them is enough and three would triple the alternation.
//   - it must not be `_bucket`, because `le` is a label on that series. Union
//     it into MetricAttrKeys and the operator is offered `le` as a filter
//     key — the histogram's own internal dimension, presented as if it were
//     one of their attributes.
func discoveryNameCandidates(name string) []string {
	base := plainNameCandidates(name)
	out := make([]string, 0, len(base)*2)
	out = append(out, base...)
	for _, c := range base {
		if hasAnySuffix(c, []string{bucketSuffix, "_sum", histogramCountSuffix}) {
			// Already a histogram part: its own labels are what discovery
			// wants, and `x_count_count` is not a series.
			continue
		}
		if strings.HasSuffix(c, counterSuffix) {
			// `_total` marks a monotonic SUM. It has no histogram siblings, so
			// `…_total_count` is a name nothing emits — and every candidate
			// with no hit behind it is alternation width paid on every
			// keystroke in the filter picker.
			continue
		}
		out = append(out, c+histogramCountSuffix)
	}
	return out
}

// nameMatcher renders the `__name__` matcher for a candidate list.
//
// ONE candidate keeps the `=` form. Not cosmetic: it is the form every
// pre-v0.9.1159 expression used, so a name that needs no guessing produces a
// byte-identical query to the one that shipped before — the upstream cache
// key, VM's query log and the existing tests all stay pinned, and this
// release cannot change the behaviour of a query it had nothing to add to.
//
// MANY candidates become a fully anchored literal alternation. Anchors are
// explicit even though MetricsQL and PromQL both anchor `=~` implicitly:
// both engines pattern-match an anchored literal alternation into a set
// lookup on the name index, and writing the anchors keeps that intent legible
// to whoever reads the query log next.
//
// Each candidate is regexp-quoted BEFORE the alternation is joined. A dotted
// name is the common case and an unescaped `.` would make
// `http.server.request.duration` match `httpxserverxrequestxduration` too —
// the same class of silently-wider match promMatcher quotes its values
// against.
func nameMatcher(candidates []string) string {
	if len(candidates) == 1 {
		return "__name__=" + quotePromString(candidates[0])
	}
	if len(candidates) == 0 {
		// Unreachable: every caller rejects an empty metric name first. The
		// safe direction anyway — `__name__=""` matches nothing, where an
		// empty regex would match everything.
		return `__name__=""`
	}
	return "__name__=~" + quotePromString(nameAlternation(candidates))
}

// nameAlternation builds the anchored regex. Split out from nameMatcher so
// the ESCAPING can be asserted at the regex level, where a `.` is spelled
// `\.` — one layer below the PromQL string literal, where quotePromString
// doubles the backslash to `\\.`.
func nameAlternation(candidates []string) string {
	quoted := make([]string, len(candidates))
	for i, c := range candidates {
		quoted[i] = regexp.QuoteMeta(c)
	}
	return "^(" + strings.Join(quoted, "|") + ")$"
}
