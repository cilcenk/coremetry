// v0.9.1268 — the VictoriaMetrics arm of the service-Overview throughput
// mapper.
//
// OPERATOR-REPORTED. The "Throughput · metrik (http.server.request.duration)"
// panel said "bu servise eşleşen seri yok" on a prod install whose metric
// backend is VictoriaMetrics — while the avg-by-route panel BESIDE IT, reading
// the SAME metric, drew fine. The left panel went through /api/metrics/query
// and therefore through the source seam; the mapper called *chstore.Store
// directly and was pinned to ClickHouse. So it searched the wrong store,
// truthfully reported finding nothing there, and the answer was honest and
// useless.
//
// metricsource.go's header used to list this mapper as a deliberate
// ClickHouse-only surface ("fixed-name internal readers … each hard-code metric
// names and CH-side column behaviour"). That decision was made in v0.9.1150,
// BEFORE VictoriaMetrics could be the only place a metric lives. Once VM became
// primary, "deliberately CH" stopped meaning "scoped" and started meaning
// "wrong store" — the note is now written against the reasoning that made it
// stale rather than deleted, because the same reasoning is still correct for
// dql.go.
//
// WHAT THIS FILE DOES NOT DO: probe for a metric family and then query it.
// names.go's header ("WHY NOT A PROBE") is load-bearing — a `__name__` lookup
// locks onto a family that stays in the label index for the whole retention.
// The QUERY path here stays probe-free candidate alternation + MetricsQL `or`
// self-selection, exactly like buildPromQL. The two probes below answer
// DIAGNOSTIC questions the mapper asks BEFORE querying ("does this name exist
// at all", "which rate function"), and both are safe in the direction they can
// fail: a stale index over-reports existence, the mapper then queries and finds
// nothing, and the operator gets the tried-spellings diagnosis. Neither probe
// can make a query read the wrong family, because the query never reads their
// answer as a name.
package vmetrics

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/promapi"
)

// Instrument classes, mirroring the strings chstore.Store.MetricInstrument
// returns. They are a WIRE contract with the mapper: it branches on exactly
// these two and treats anything else as "cannot derive throughput from this".
const (
	instrumentSum       = "sum"
	instrumentHistogram = "histogram"
)

// normalizeQueryWindow fills a zero window the same way the ClickHouse path
// does (zero To → now, zero From → 24h back) so an unbounded call cannot
// become an unbounded VM query.
//
// Extracted in v0.9.1268 so every entry point normalizes identically. It
// returns a COPY: the caller's filter is reused across candidate attempts and a
// mutated From/To would make attempt N+1 read a different window than attempt N
// — a difference that renders as "the first identity label matched and the
// second did not" rather than as a bug.
func normalizeQueryWindow(f chstore.MetricQueryFilter) chstore.MetricQueryFilter {
	now := time.Now()
	if f.To.IsZero() {
		f.To = now
	}
	if f.From.IsZero() {
		f.From = f.To.Add(-24 * time.Hour)
	}
	return f
}

// runRangeQuery executes a rendered expression over the filter's window and
// decodes the matrix into Coremetry's series shape.
//
// The `step` param is recomputed from promStep over the SAME already-normalized
// filter the expression was rendered from. promStep is pure, so the rollup
// window inside the expression and the step on the wire cannot drift — the
// property buildPromQL's own comment names as the reason it recomputes rather
// than accepts a step argument.
func (s *Service) runRangeQuery(ctx context.Context, cfg Settings, q string, f chstore.MetricQueryFilter) ([]chstore.SpanMetricSeries, error) {
	step := promStep(f.From, f.To, f.StepSeconds, f.MaxDataPoints)
	params := url.Values{
		"query": {q},
		"start": {promTime(f.From)},
		"end":   {promTime(f.To)},
		"step":  {strconv.Itoa(step) + "s"},
	}
	series, err := promapi.QuerySeries(ctx, s.request("/api/v1/query_range", params, cfg))
	if err != nil {
		return nil, err
	}
	out := make([]chstore.SpanMetricSeries, 0, len(series))
	for _, sr := range series {
		row := chstore.SpanMetricSeries{GroupKey: seriesGroupKey(f.GroupBy, sr.Metric)}
		for _, raw := range sr.Values {
			ts, v, ok := promapi.Sample(raw)
			if !ok {
				// Non-finite or malformed: skip the POINT, keep the series.
				// A gap renders as a gap; a 0 renders as a measurement.
				continue
			}
			row.Points = append(row.Points, chstore.SpanMetricPoint{
				// SpanMetricPoint.Time is unix NANOS (bucket start).
				Time:  int64(ts * 1e9),
				Value: v,
			})
		}
		out = append(out, row)
	}
	return out, nil
}

// rateRollup validates the mode the two rate methods accept.
//
// The set is chstore's ("rate" | "increase", metricrate.go's queryRateFrom) and
// not promAggregator's wider one, so a caller cannot smuggle `avg` through a
// method whose name promises a rate — the two backends refuse the same inputs.
// Tagged ErrUnsupported so the API layer answers 400 rather than blaming a
// healthy VictoriaMetrics with a 502.
func rateRollup(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case rollupRate:
		return rollupRate, nil
	case rollupIncrease:
		return rollupIncrease, nil
	}
	return "", fmt.Errorf("rate mode %q is %w (supported: %s, %s)",
		mode, ErrUnsupported, rollupRate, rollupIncrease)
}

// QueryMetricRate — the counter arm, signature-identical to
// chstore.Store.QueryMetricRate.
//
// It delegates to buildPromQL by setting Aggregation to the mode, which is not
// a shortcut but the CORRECT translation: buildPromQL's rate branch already
// emits `sum(rate(base[W])) or sum(rate(base_count[W]))`, and the `or` is
// exactly the self-selection this panel needs. Where the metric is a real
// counter the left arm answers; where it is an OTLP histogram — which in VM has
// NO base series, only `_bucket`/`_sum`/`_count` — the left arm is silent and
// the `_count` arm speaks. So one expression is right for both instruments,
// with left precedence per group and no summing of the two families.
//
// That also means this method is correct even when MetricInstrument below
// guesses "sum" for a histogram: the expression does not read the guess.
func (s *Service) QueryMetricRate(ctx context.Context, f chstore.MetricQueryFilter, mode string) ([]chstore.SpanMetricSeries, error) {
	cfg, err := s.ready()
	if err != nil {
		return nil, err
	}
	rollup, err := rateRollup(mode)
	if err != nil {
		return nil, err
	}
	f = normalizeQueryWindow(f)
	// The incoming Aggregation is the CH path's intra-bucket op ("sum"), which
	// on that side is applied UNDER the rate. Here the rollup IS the
	// aggregation, so it replaces rather than composes — passing "sum" through
	// would render a bare sum() with no rate() at all: a cumulative counter's
	// lifetime total drawn under a throughput legend.
	f.Aggregation = rollup
	q, err := buildPromQL(f, promOptions(cfg))
	if err != nil {
		return nil, err
	}
	return s.runRangeQuery(ctx, cfg, q, f)
}

// QueryMetricCountRate — the histogram's observation count, signature-identical
// to chstore.Store.QueryMetricCountRate.
//
// For a duration histogram the observation count IS the request count, so its
// rate is throughput — the expression every Grafana dashboard writes.
//
// It renders the `_count` family ALONE rather than reusing QueryMetricRate's
// `or` composition, even though on a real OTLP histogram the two produce the
// same numbers (no base series exists, so the left arm is empty). The
// difference shows on a metric that has BOTH a base counter and a `_count`
// sibling: `or` would answer from the base counter, silently ignoring the
// caller who explicitly asked for the count. A method named CountRate that can
// return a non-count is the kind of near-miss that survives review.
func (s *Service) QueryMetricCountRate(ctx context.Context, f chstore.MetricQueryFilter, mode string) ([]chstore.SpanMetricSeries, error) {
	cfg, err := s.ready()
	if err != nil {
		return nil, err
	}
	f = normalizeQueryWindow(f)
	q, err := buildCountRatePromQL(f, mode, promOptions(cfg))
	if err != nil {
		return nil, err
	}
	return s.runRangeQuery(ctx, cfg, q, f)
}

// buildCountRatePromQL renders `sum[ by (…)](rate({__name__=~"…_count…", …}[Ws]))`.
// Pure — table-tested.
//
// Every atom is shared with buildPromQL (selectorMatchers, promSelector,
// groupByLabels, promRollupWindow, aggregateExpr, countNameCandidates), so this
// arm cannot scope, group or window differently from the one beside it.
//
// The set-aggregation is `sum` for the reason promAggregator gives for mapping
// `rate` to sum: the ClickHouse path rates PER SERIES and re-aggregates the
// group with a sum, so sum is the operator that reproduces its number. avg
// would report per-series average throughput under a total-throughput legend.
func buildCountRatePromQL(f chstore.MetricQueryFilter, mode string, opts promOpts) (string, error) {
	name := strings.TrimSpace(f.Name)
	if name == "" {
		return "", fmt.Errorf("metric name required")
	}
	rollup, err := rateRollup(mode)
	if err != nil {
		return "", err
	}
	extra, err := selectorMatchers(f)
	if err != nil {
		return "", err
	}
	// Same recompute-don't-thread discipline as buildPromQL: promStep is pure,
	// so the window inside the expression matches the step on the wire.
	step := promStep(f.From, f.To, f.StepSeconds, f.MaxDataPoints)
	w := promRollupWindow(rollup, step, f.RateWindowSec, opts.RateWindowFloorS)
	vec := rollup + "(" + promSelector(countNameCandidates(name), extra) +
		"[" + strconv.Itoa(w) + "s])"
	return aggregateExpr("sum", groupByLabels(f.GroupBy), vec), nil
}

// ── Identity, existence and instrument: what the mapper asks before querying ──

// ServiceIdentityLabels — the labels that can carry a service's identity in
// VictoriaMetrics, in try order.
//
// DERIVED from chstore.ServiceIdentityLabels rather than restated, so the two
// backends cannot drift on which identities are attempted. promLabel does the
// whole translation: it strips the `resource.` namespace VM has no concept of
// and maps dots to underscores, which is precisely what every OTLP→Prometheus
// write path (VM's own receiver included) already did to the data.
//
//	resource.k8s.deployment.name → k8s_deployment_name
//	resource.k8s.container.name  → k8s_container_name
//	job / service / name         → unchanged
//
// THEN `service_name` IS APPENDED, and that one candidate is the operator's
// bug. In ClickHouse service.name is a COLUMN, not an attribute, so it was
// never in the identity LIST — the CH mapper reaches it through a separate
// service_name fallback (serviceNameAttempts). In VM there is no column: the
// resource attribute lands as an ordinary label, and on an OTLP-fed install it
// is the MOST likely place the identity lives. Without it the VM path would try
// five labels that install does not carry and report an honest empty.
//
// Appended LAST rather than promoted: trying more labels is safe because every
// match is on the EXACT value (or the anchored two-spelling regex), so an extra
// candidate cannot match the wrong service — only the ORDER among labels that
// all match would change, and k8s_deployment_name is the more precise identity
// when present because it carries the environment suffix.
func (s *Service) ServiceIdentityLabels() []string {
	out := make([]string, 0, len(chstore.ServiceIdentityLabels)+1)
	seen := make(map[string]bool, len(chstore.ServiceIdentityLabels)+1)
	for _, k := range chstore.ServiceIdentityLabels {
		l := promLabel(k)
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	if sl := serviceLabel(); sl != "" && !seen[sl] {
		out = append(out, sl)
	}
	return out
}

// familyNameCandidates — every spelling the throughput path could read for one
// metric: the discovery set (base ∪ `_count`) plus the bucket spellings.
//
// `_bucket` is included HERE and deliberately not in discoveryNameCandidates,
// whose comment explains why label discovery must avoid it (it would hand the
// operator `le`, the histogram's internal dimension, as one of their
// attributes). This probe is asking a different question — WHICH FAMILY EXISTS
// — and the bucket series is the only evidence that distinguishes a histogram
// from a counter.
func familyNameCandidates(name string) []string {
	out := discoveryNameCandidates(name)
	if !mayHaveHistogramParts(name) {
		return out
	}
	seen := make(map[string]bool, len(out))
	for _, c := range out {
		seen[c] = true
	}
	for _, b := range bucketNameCandidates(name) {
		if !seen[b] {
			seen[b] = true
			out = append(out, b)
		}
	}
	return out
}

// metricFamilyNames asks VM which of those spellings it actually carries.
//
// One label-index lookup, no series scan. Scoped by `nameLookback` (7d) —
// the same window ListMetricNames uses, so "this metric exists" means the same
// thing on the catalogue page and here.
func (s *Service) metricFamilyNames(ctx context.Context, name, service string) ([]string, error) {
	cfg, err := s.ready()
	if err != nil {
		return nil, err
	}
	cands := familyNameCandidates(name)
	if len(cands) == 0 {
		return nil, nil
	}
	matchers := []string{nameMatcher(cands)}
	if svc := strings.TrimSpace(service); svc != "" {
		matchers = append(matchers, serviceLabel()+"="+quotePromString(svc))
	}
	now := time.Now()
	params := url.Values{
		"start":   {promTime(now.Add(-nameLookback))},
		"end":     {promTime(now)},
		"match[]": {"{" + strings.Join(matchers, ", ") + "}"},
	}
	return promapi.QueryStrings(ctx, s.request("/api/v1/label/__name__/values", params, cfg))
}

// MetricExists — does any spelling of this name exist in VM.
//
// Signature-identical to chstore.Store.MetricExists. Its answer picks WHICH of
// the mapper's five candidate metric names is used, and the answer is safe in
// the direction it can be wrong: a name kept alive by a stale label index
// returns true, the mapper queries it, finds nothing and renders the
// tried-spellings diagnosis. The opposite failure — a false negative that skips
// the metric the operator is looking at — is the one that produces a silent
// empty panel, and a label-index lookup does not produce it.
func (s *Service) MetricExists(ctx context.Context, name string) (bool, error) {
	if strings.TrimSpace(name) == "" {
		return false, nil
	}
	names, err := s.metricFamilyNames(ctx, name, "")
	if err != nil {
		return false, err
	}
	return len(names) > 0, nil
}

// classifyInstrument — pure: which instrument the present spellings describe.
//
// A `_bucket` spelling is the only positive evidence of a histogram, so it
// wins outright; anything else present means "there is a series to rate".
// Nothing present is "" — the mapper's signal to fall back to an unscoped
// probe, exactly as it does on ClickHouse.
//
// EITHER ANSWER LANDS ON THE RIGHT SERIES, which is what makes a heuristic
// acceptable here: "histogram" routes to QueryMetricCountRate (the `_count`
// arm) and "sum" routes to QueryMetricRate (whose `or` composition ALSO
// resolves to the `_count` arm when no base series exists). The classification
// decides which expression is sent, not which data comes back — so a
// misclassification costs a different query log line, never a wrong number.
func classifyInstrument(present []string) string {
	found := ""
	for _, n := range present {
		if n == "" {
			continue
		}
		if strings.HasSuffix(n, bucketSuffix) {
			return instrumentHistogram
		}
		found = instrumentSum
	}
	return found
}

// MetricInstrument — signature-identical to chstore.Store.MetricInstrument.
//
// The empty string on a transport failure matches the CH sibling, which
// swallows its query error the same way: the mapper reads "" as "unknown, try
// the unscoped probe" and its next call surfaces the real error.
func (s *Service) MetricInstrument(ctx context.Context, name, service string) string {
	names, err := s.metricFamilyNames(ctx, name, service)
	if err != nil {
		return ""
	}
	return classifyInstrument(names)
}

// MetricUnit — the OTLP-ish unit for a metric, signature-identical to
// chstore.Store.MetricUnit.
//
// Derived from the name VM ACTUALLY CARRIES, not from the name the caller
// asked about. That distinction is the whole method: the caller asks about
// `http.server.request.duration`, which carries no unit, while the series VM
// holds is `http_server_request_duration_seconds` — and the `_seconds` suffix
// IS the unit. Prometheus has no metadata channel for this (VM does not
// populate /api/v1/metadata), so the name is the only source, which is the
// same call v0.9.1180 made for the catalogue's Unit column.
//
// It reads a PRESENT spelling rather than guessing over the candidate list,
// because guessing would let a `_seconds` candidate that nothing emits label a
// millisecond metric's axis "s" — plausible, wrong and unquestioned.
func (s *Service) MetricUnit(ctx context.Context, name, service string) string {
	names, err := s.metricFamilyNames(ctx, name, service)
	if err != nil {
		return ""
	}
	return unitFromPresentNames(names)
}

// unitFromPresentNames — pure: the first unit any present spelling describes.
//
// Sorted first so the answer does not depend on VM's label-index ordering: two
// requests a second apart must not label the same axis "s" and then "".
func unitFromPresentNames(present []string) string {
	sorted := append([]string(nil), present...)
	sort.Strings(sorted)
	for _, n := range sorted {
		if u, _ := describeMetricName(n); u != "" {
			return u
		}
	}
	return ""
}

// labelNames — every label carried by a metric family. The uncapped half of
// MetricAttrKeys, split out in v0.9.1268 so MetricPresentKeys can intersect
// against the FULL set: the picker's 100-key cap is a UI bound, and applying it
// to a diagnostic would let it report "this key is absent" about a key that is
// merely the 101st alphabetically.
func (s *Service) labelNames(ctx context.Context, metric, service string, since time.Duration) ([]string, error) {
	cfg, err := s.ready()
	if err != nil {
		return nil, err
	}
	matchers := []string{nameMatcher(discoveryNameCandidates(metric))}
	if svc := strings.TrimSpace(service); svc != "" {
		matchers = append(matchers, serviceLabel()+"="+quotePromString(svc))
	}
	now := time.Now()
	params := url.Values{
		"start":   {promTime(now.Add(-since))},
		"end":     {promTime(now)},
		"match[]": {"{" + strings.Join(matchers, ", ") + "}"},
	}
	keys, err := promapi.QueryStrings(ctx, s.request("/api/v1/labels", params, cfg))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if k == "" || k == "__name__" {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// MetricPresentKeys — which of the asked-about keys this metric actually
// carries. Signature-identical to chstore.Store.MetricPresentKeys.
//
// This is the diagnostic that separates "the collector never sends this
// identity" from "it sends it with a value we did not match" — two situations
// with two different fixes that an empty chart renders identically (v0.9.682).
// It has to be answered by the SAME store the query read, or the note tells the
// operator about ClickHouse's keys while the chart searched VictoriaMetrics.
//
// The asked-about keys arrive in Coremetry's spelling (`resource.k8s…`) and are
// matched through promLabel, but ECHOED BACK VERBATIM: the note prints them
// next to the tried-labels list, and printing two spellings of one key would
// read as two different keys.
func (s *Service) MetricPresentKeys(ctx context.Context, metric string, keys []string, since time.Duration) []string {
	if len(keys) == 0 {
		return nil
	}
	all, err := s.labelNames(ctx, metric, "", since)
	if err != nil {
		return nil
	}
	have := make(map[string]bool, len(all))
	for _, k := range all {
		have[k] = true
	}
	var out []string
	for _, k := range keys {
		if l := promLabel(k); l != "" && have[l] {
			out = append(out, k)
		}
	}
	return out
}
