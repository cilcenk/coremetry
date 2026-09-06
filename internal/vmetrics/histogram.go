package vmetrics

// Histogram heatmap + raw-query proxy — the VictoriaMetrics half of Faz 2
// (v0.9.1157).
//
// Two read surfaces join the backend seam here, and they are opposites:
//
//	GET /api/metrics/histogram → QueryMetricHistogram. Coremetry BUILDS the
//	  query; the operator only picked a metric. So everything is our
//	  responsibility: the bucket naming rule, the window, the differencing,
//	  the percentile arithmetic — and the answer must land in exactly the
//	  chstore.HistogramSeries shape the ClickHouse sibling produces, because
//	  the same frontend heatmap renders both without knowing which answered.
//	GET /api/metrics/promql   → QueryPromQLRange. The OPERATOR writes the
//	  query and it goes to VM verbatim. Nothing here parses it, and that is
//	  the feature (see the function's own header).
//
// Neither surface falls back to ClickHouse. The rule is the package's
// (client.go header): the numbers would differ and nothing on screen could
// say so.

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/promapi"
)

// maxHistogramTimeBuckets mirrors chstore.maxHistogramBuckets (5000) — the
// ceiling on the Go-side time × bucket grid.
//
// The CH sibling needed it because a caller-pinned `?step=1` over a
// multi-year window made its accum[nTime][nBuckets] allocation ~1e9 wide and
// OOM'd the single binary (v0.9.114). The VM path allocates the same grid
// from the same caller-controlled step, so it inherits the same exposure —
// promStep's own 11000-point ceiling is four times too loose for a grid that
// is also nBuckets deep. 5000 is past any panel's pixel width, so raising the
// step to fit costs no resolution anyone can see.
const maxHistogramTimeBuckets = 5000

// maxHistogramLEBuckets bounds the OTHER dimension.
//
// A real explicit histogram has 10–30 bounds; Coremetry's own exponential→
// explicit materialisation lands around 160 (metrichist.go's bounds-run
// note). 512 is generous for anything intentional and refuses the case this
// guards: a query pointed at a series whose `le` is effectively unbounded
// cardinality, where the grid becomes 5000 × N and the per-time percentile
// loop becomes O(nTime · N).
//
// Refusing rather than truncating, because truncating the bucket set is the
// one thing that CANNOT be done quietly here — dropping high buckets moves
// the tail off the chart and pulls every percentile down with it.
const maxHistogramLEBuckets = 512

// QueryMetricHistogram renders an explicit histogram as the time × bucket
// heatmap + per-bucket p50/p95/p99 that /api/metrics/histogram returns.
//
// Signature-identical to chstore.Store's, which is what lets the API seam
// (internal/api/metricsource.go) hold both behind one interface and turn any
// future drift into a compile error.
//
// The window is normalised HERE, the same way QueryMetric normalises it, so
// a from/to-less call cannot become an unbounded VM query. Note this is one
// place the two backends genuinely differ: the CH path leaves a zero window
// unbounded in the WHERE and returns early with bounds only, relying on its
// LIMIT + max_execution_time. Its behaviour is untouched; VM gets the bound
// because on this side there is no LIMIT to fall back on.
func (s *Service) QueryMetricHistogram(ctx context.Context, f chstore.MetricQueryFilter) (*chstore.HistogramSeries, error) {
	cfg, err := s.ready()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(f.Name) == "" {
		return nil, fmt.Errorf("metric name required")
	}
	now := time.Now()
	if f.To.IsZero() {
		f.To = now
	}
	if f.From.IsZero() {
		f.From = f.To.Add(-24 * time.Hour)
	}
	q, step, err := buildHistogramPromQL(f, promOptions(cfg))
	if err != nil {
		return nil, err
	}
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
	return assembleHistogram(series, bucketNameCandidates(f.Name), f.From, f.To, step)
}

// buildHistogramPromQL renders the heatmap's bucket query and the step it was
// resolved against. Pure — table-tested in histogram_test.go.
//
// THE ROLLUP IS `increase`, NOT `rate`, AND THIS ONE IS NOT COSMETIC.
//
// The percentile translation in promql.go uses rate() because
// histogram_quantile reads bucket RATIOS and any positive rescaling leaves φ
// unchanged, so there the choice is only about which expression an operator
// recognises. Here the numbers ARE the answer:
// chstore.HistogramSeries.Counts is `[][]uint64` — per-interval OBSERVATION
// COUNTS, which is what the heatmap's colour scale and its "N samples"
// tooltip report. rate() would deliver counts-per-SECOND, so a bucket with
// 50 samples over a 60s step would arrive as 0.83 and round to ZERO in a
// uint64. The heatmap would not merely be mislabelled, it would be blank,
// and the p50/p95/p99 computed from an all-zero vector are 0 as well. That is
// the v0.6.36 unit-scale class with an integer truncation on top.
//
// increase() over [step] also TILES the window: consecutive buckets cover
// adjacent, non-overlapping intervals, which is what "per-interval density"
// means and what the CH sibling produces by binning raw samples. A window
// wider than the step would double-count observations into neighbouring
// cells and smear the picture.
//
// The window is deliberately NOT floored to promLookbehindFloorSec. That
// floor exists for gauges and rates, where widening only smooths; widening an
// increase() multiplies the number while the chart still says one bucket
// (promRollupWindow's own reasoning, and VM's default for increase is `step`).
// v0.9.1159 — the selector is a candidate ALTERNATION (bucketNameCandidates),
// same as the percentile path: an operator whose write path applies OTel
// Prometheus naming has `…_seconds_bucket`, not `…_bucket`, and the heatmap
// was blank for exactly that reason.
//
// v0.9.1164 — `opts` arrives for the BUCKET-SCAN GUARD only. The rate-window
// floor cannot reach this expression even in principle: the rollup is
// increase(), the floor is deliberately never applied to increase() (see
// promRollupWindow), and this window is `step` by construction because it has
// to TILE. An operator who raises the floor to smooth their rate charts must
// not find every heatmap cell's sample count multiplied — so this path reads
// the guard field of opts and nothing else, which is the shape that makes the
// exemption impossible to lose in a refactor.
func buildHistogramPromQL(f chstore.MetricQueryFilter, opts promOpts) (string, int, error) {
	cands := bucketNameCandidates(f.Name)
	if len(cands) == 0 {
		return "", 0, fmt.Errorf("metric name required")
	}
	// Same refusal as buildPromQL, same reason: DB instance/engine scoping
	// compiles to a per-engine OR over receiver-specific ClickHouse columns
	// and guessing a VM equivalent would scope the chart to the wrong
	// instance.
	if strings.TrimSpace(f.Instance) != "" || strings.TrimSpace(f.Engine) != "" {
		return "", 0, fmt.Errorf("database instance/engine scoping is %w "+
			"(this drill reads ClickHouse-side receiver attributes)", ErrUnsupported)
	}
	// `extra` is built SEPARATELY from the name matcher — the same shape
	// buildPromQL uses — so the guard can count the narrowing matchers without
	// an index offset into the combined slice. `len(matchers)-1` would have
	// worked today and broken silently the first time anything else was
	// prepended.
	extra := make([]string, 0, len(f.Filters)+1)
	if svc := strings.TrimSpace(f.Service); svc != "" {
		extra = append(extra, serviceLabel()+"="+quotePromString(svc))
	}
	for _, fe := range f.Filters {
		m, err := promMatcher(fe)
		if err != nil {
			return "", 0, err
		}
		extra = append(extra, m)
	}
	// Guarded UNCONDITIONALLY: this selector is always the `_bucket` family, so
	// unlike the avg branch in buildPromQL there is no cheap shape to exempt.
	// Checked after the filters translate, so an unexpressible filter still
	// reports its own (more specific) refusal first.
	if err := guardBucketScan(classHeatmap, len(extra), opts.AllowUnfilteredPercentiles); err != nil {
		return "", 0, err
	}
	step := histogramStep(f.From, f.To, f.StepSeconds, f.MaxDataPoints)
	sel := "{" + strings.Join(append([]string{nameMatcher(cands)}, extra...), ", ") + "}"
	return fmt.Sprintf("sum by (%s) (%s(%s[%ds]))", labelLE, rollupIncrease, sel, step), step, nil
}

// histogramStep resolves the heatmap's step: promStep — so a VM heatmap has
// the same time resolution as every other VM chart on the page — then widened
// until the Go-side grid fits maxHistogramTimeBuckets.
//
// Using promStep rather than mirroring the CH ladder (chstore.autoStepSeconds)
// is a choice, and it is the one that costs less when it is wrong. The two
// resolvers pick different steps for the same window, so ONE of the two
// comparisons has to give: either a VM heatmap matches the CH heatmap, or it
// matches the VM line chart directly above it. A step is not a NUMBER that
// can be wrong — it is a resolution — and the mismatch an operator would
// actually notice is two panels on one screen bucketing time differently.
func histogramStep(from, to time.Time, stepSeconds, maxDataPoints int) int {
	step := promStep(from, to, stepSeconds, maxDataPoints)
	rangeSec := int(to.Sub(from).Seconds())
	if rangeSec > 0 && rangeSec/step > maxHistogramTimeBuckets {
		step = ceilDiv(rangeSec, maxHistogramTimeBuckets)
	}
	if step < 1 {
		step = 1
	}
	return step
}

// assembleHistogram maps VM's `sum by (le) (increase(…))` matrix into
// chstore.HistogramSeries. Pure — table-tested in histogram_test.go.
//
// bucketCandidates is only used to write the empty-result note, and it is
// passed in rather than recomputed so the note can never name a series other
// than the one the query actually asked for. Since v0.9.1159 it is the full
// candidate LIST, because the query is an alternation over all of them.
//
// The GRID is built from (from, step) rather than from the timestamps VM
// returned, so Times is a deterministic function of the request — two polls
// of one panel produce the same x-axis even if a series gained or lost a
// point between them. This mirrors the CH path exactly.
//
// The point→slot mapping ROUNDS where the CH path truncates, and the
// difference is not sloppiness on either side. CH bins RAW sample timestamps,
// which fall anywhere inside a bucket, so truncation is the definition of
// "which bucket is this in". VM returns an ALIGNED grid — start, start+step,
// … — so every timestamp is already a slot, and the only deviation is the
// ~200ns of float64 error in `ts * 1e9` at epoch-nanosecond magnitude.
// Truncating that would push a boundary sample into the PREVIOUS slot and
// shift the whole heatmap one cell left.
func assembleHistogram(series []promapi.Series, bucketCandidates []string, from, to time.Time, stepSec int) (*chstore.HistogramSeries, error) {
	if len(series) == 0 {
		// Honest empty. Same reasoning as the percentile note: the query went
		// to a series name the operator never typed, so a blank heatmap on
		// its own cannot distinguish "no data" from "not a histogram".
		return &chstore.HistogramSeries{Note: emptyBucketNote(bucketCandidates)}, nil
	}
	if len(series) > maxHistogramLEBuckets {
		return nil, fmt.Errorf("histogram has %d le buckets (cap %d) — %w at this cardinality; "+
			"narrow the query with a filter or point it at a metric with a bounded bucket layout",
			len(series), maxHistogramLEBuckets, ErrUnsupported)
	}
	les := make([]string, len(series))
	for i, sr := range series {
		les[i] = sr.Metric[labelLE]
	}
	bounds, slot, err := bucketLayout(les)
	if err != nil {
		return nil, err
	}
	if len(bounds) == 0 {
		// Every member was the +Inf overflow: a total, with no distribution
		// under it. There is no y-axis to draw, so this is the empty shape —
		// not a one-row heatmap that would imply every observation exceeded a
		// bound nothing on screen names.
		return &chstore.HistogramSeries{Note: emptyBucketNote(bucketCandidates)}, nil
	}

	fromNs := from.UnixNano()
	stepNs := int64(stepSec) * 1_000_000_000
	if stepNs <= 0 {
		stepNs = 1_000_000_000
	}
	nTime := int((to.UnixNano()-fromNs)/stepNs) + 1
	if nTime <= 0 {
		nTime = 1
	}
	if nTime > maxHistogramTimeBuckets {
		// histogramStep already widened the step to fit; this is the backstop
		// for a caller that reached assembleHistogram with a mismatched pair.
		nTime = maxHistogramTimeBuckets
	}

	nBuckets := len(bounds) + 1
	cum := make([][]float64, nTime)
	for i := range cum {
		cum[i] = make([]float64, nBuckets)
	}
	for si, sr := range series {
		for _, raw := range sr.Values {
			ts, v, ok := promapi.Sample(raw)
			if !ok {
				// Non-finite or malformed: skip the POINT. A NaN here is a
				// staleness marker, not a zero count.
				continue
			}
			tb := int(math.Round(float64(int64(ts*1e9)-fromNs) / float64(stepNs)))
			if tb < 0 || tb >= nTime {
				continue
			}
			// += rather than = so duplicate `le` values (bucketLayout maps
			// them to one slot) add, matching the sum they stand in for.
			cum[tb][slot[si]] += v
		}
	}

	out := &chstore.HistogramSeries{
		Bounds: bounds,
		Times:  make([]int64, nTime),
		Counts: make([][]uint64, nTime),
		P50:    make([]float64, nTime),
		P95:    make([]float64, nTime),
		P99:    make([]float64, nTime),
		// Skipped stays 0: `sum by (le)` collapsed every attribute set into
		// one layout upstream, so there is no mismatched-bounds series to
		// drop. The CH path's Skipped counts exactly that, and reporting a
		// nonzero value here would invent a problem.
	}
	for i := 0; i < nTime; i++ {
		out.Times[i] = fromNs + int64(i)*stepNs
		out.Counts[i] = deCumulateLE(cum[i])
		// Same estimator the CH path uses (exported for this reason), so the
		// two backends cannot disagree on p99 for the same distribution.
		out.P50[i] = chstore.PercentileFromBuckets(bounds, out.Counts[i], 0.50)
		out.P95[i] = chstore.PercentileFromBuckets(bounds, out.Counts[i], 0.95)
		out.P99[i] = chstore.PercentileFromBuckets(bounds, out.Counts[i], 0.99)
	}
	return out, nil
}

// QueryPromQLRange proxies a RAW operator-written query to VM's query_range.
//
// THERE IS NO PRE-VALIDATION PARSER ON THIS PATH, ON PURPOSE.
//
// The ClickHouse sibling parses first (internal/promql) because it has to:
// its evaluator implements a subset and must reject what it cannot push into
// ClickHouse. Reusing that parser here would make Coremetry the arbiter of a
// dialect it does not implement — VM speaks MetricsQL, a PromQL SUPERSET, so
// `WITH(…)` templates, subqueries, `keep_metric_names`, `rollup_rate` and the
// rest are valid queries our parser rejects. An operator whose query works in
// vmui and 400s in Coremetry would reasonably conclude the endpoint is broken.
//
// So the string travels verbatim and VM answers. A syntax error comes back as
// VM's own envelope error (status != success), which promapi surfaces with
// VM's message intact — a better diagnosis than ours would have been, since
// it comes from the engine that will actually run the query.
//
// The BOUNDS that matter are still ours: the API caps the query length before
// this is reached, the window is normalised here, and the step goes through
// promStep — so the point count per series stays under VM's own ceiling
// instead of earning a 4xx on a wide window.
func (s *Service) QueryPromQLRange(ctx context.Context, query string, from, to time.Time, stepSeconds, maxDataPoints int) ([]chstore.SpanMetricSeries, error) {
	cfg, err := s.ready()
	if err != nil {
		return nil, err
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("query is required")
	}
	now := time.Now()
	if to.IsZero() {
		to = now
	}
	if from.IsZero() {
		// 1h, matching the handler's own default so a direct caller (MCP, a
		// test) gets the window the HTTP surface would have used.
		from = to.Add(-time.Hour)
	}
	step := promStep(from, to, stepSeconds, maxDataPoints)
	params := url.Values{
		"query": {q},
		"start": {promTime(from)},
		"end":   {promTime(to)},
		"step":  {strconv.Itoa(step) + "s"},
	}
	series, err := promapi.QuerySeries(ctx, s.request("/api/v1/query_range", params, cfg))
	if err != nil {
		return nil, err
	}
	out := make([]chstore.SpanMetricSeries, 0, len(series))
	startSec := float64(from.Unix())
	for _, sr := range series {
		row := chstore.SpanMetricSeries{GroupKey: labelSetGroupKey(sr.Metric)}
		for _, raw := range sr.Values {
			ts, v, ok := promapi.Sample(raw)
			if !ok {
				// Skip the POINT, keep the series: a gap renders as a gap,
				// a 0 renders as a measurement.
				continue
			}
			// v0.10.504 (A6) — kova başlangıcı (bucketStartNs); pencere
			// önündeki ilk kısmi kova düşer. runRangeQuery ile aynı kural.
			t := bucketStartNs(ts, step)
			if atRequestStart(ts, startSec) {
				continue
			}
			row.Points = append(row.Points, chstore.SpanMetricPoint{
				Time:  t,
				Value: v,
			})
		}
		out = append(out, row)
	}
	return out, nil
}
