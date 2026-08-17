package vmetrics

// PromQL translation — the pure half of the VictoriaMetrics read backend
// (v0.9.1150, Faz 1). Everything here is a function of its arguments so
// the whole translation is table-testable without a live VM; the client
// wraps these and does I/O only.
//
// Two rules run through the whole file:
//
//  1. The METRIC NAME is never translated. OTLP names may live in VM as
//     `jvm.memory.used` or as `jvm_memory_used` depending on how the
//     operator's write path sanitized them, and we cannot know which —
//     so we send whatever the catalogue gave us, verbatim. That forces
//     the `{__name__="…"}` selector form (a bare `jvm.memory.used{}` is
//     not valid PromQL), which happens to work for BOTH spellings.
//     LABEL names are a different story: PromQL's grammar has no way to
//     express a dotted label name at all, so those must be sanitized.
//
//  2. A filter we cannot express is an ERROR, never a silent drop. The
//     v0.9.566 incident is the reason: a dropped `jvm.memory.type="heap"`
//     filter did not produce an empty panel, it produced heap+non-heap
//     summed and LABELLED heap. A wrong-but-plausible number is worse
//     than a failure, because nobody questions it.

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// ErrUnsupported marks a query Coremetry cannot EXPRESS against VM — a
// refused aggregation, a filter operator with no PromQL matcher, DB
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

// promAggregator maps a Coremetry aggregation label to a PromQL
// aggregation operator.
//
// Faz 1 covers the five set-aggregations that mean the same thing in
// both engines. The rest are REFUSED with a message naming the backend,
// because each would need real work to be correct rather than merely
// plausible:
//
//   - rate / increase: the CH path runs a reset-protected per-series
//     delta (metricrate.go). PromQL's rate() is close but not identical
//     (extrapolation at window edges), and it changes the SHAPE of the
//     query from an aggregation to a range-vector function. Faz 2.
//   - p50 / p95 / p99: the CH path routes histogram instruments through
//     histogram_quantile over bucket columns and value-quantiles for
//     gauges. Picking the right one needs the instrument type, which VM
//     does not report (see ListMetricNames). Histograms are explicitly
//     Faz 2 scope.
//   - last: `last_over_time(x[step])` is the honest translation but it
//     is a range-vector rewrite, not a `by()` aggregation — same shape
//     problem as rate.
func promAggregator(agg string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(agg)) {
	case "", "avg":
		return "avg", nil
	case "sum":
		return "sum", nil
	case "min":
		return "min", nil
	case "max":
		return "max", nil
	case "count":
		return "count", nil
	}
	return "", fmt.Errorf("aggregation %q is %w (supported: avg, sum, min, max, count)",
		agg, ErrUnsupported)
}

var promLabelInvalid = regexp.MustCompile(`[^a-zA-Z0-9_]`)

// promLabel sanitizes an attribute key into a valid PromQL label name.
//
// This is NOT the metric-name translation rule 1 forbids — it is a
// grammar requirement. `{service.name="x"}` does not parse: PromQL label
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

// quotePromString escapes a value for a PromQL double-quoted literal.
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

// promMatcher renders one FilterExpr as a PromQL label matcher.
//
// Multi-value equality becomes a regex alternation because PromQL has no
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
		// PromQL idiom for "label is present".
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
	// LIKE / NOT LIKE and the numeric comparisons have no PromQL label-
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
	// Anchored: PromQL regex matchers are fully anchored already, but
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
	op, err := promAggregator(f.Aggregation)
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

	if len(f.GroupBy) == 0 {
		// No group-by → one series aggregated across everything, which is
		// what the CH path's `GROUP BY bucket` alone produces.
		return op + "(" + sel + ")", nil
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
		return op + "(" + sel + ")", nil
	}
	return op + " by (" + strings.Join(labels, ", ") + ") (" + sel + ")", nil
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
