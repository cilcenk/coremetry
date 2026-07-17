package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// metricresolve.go — "Every metric is a doorway" Phase D, increment 2
// (v0.8.51). ResolveMetricQuery is the ONE place a MetricQuery descriptor
// (frontend/src/lib/metricQuery.ts) becomes a ClickHouse read. It:
//
//   1. picks the coarsest spanmetrics tier (1s / 10s / 1m) that satisfies the
//      window+step and is still within retention — fewer rows for the same
//      visible resolution;
//   2. emits the agg→SQL projection from a single whitelist ("formulas once")
//      over the AggregatingMergeTree states (countMerge / sumMerge /
//      quantilesMerge / argMaxMerge);
//   3. dual-reads: when the window predates the forward-only cutover (or a
//      filter/groupBy references a dimension the rollups don't carry), it
//      falls back to the existing QuerySpanMetric path
//      (operation_summary_5m → raw spans) so older windows still render.
//
// Keeping the planner pure (selectMetricTier / spanmetricStateAgg /
// tierDimColumn) lets the regression test exercise every grain boundary and
// every metric+agg combination without a live ClickHouse — the unit-mixing /
// off-axis-branch class of bug ([[feedback-unit-mixing-needs-both-branches]]).

// MetricResolveQuery is the backend mirror of the frontend MetricQuery
// descriptor. Filters/GroupBy use the dotted attribute syntax (service.name,
// span.kind, http.route, status, …) — the same keys the frontend emits and
// filterexpr.go's wellKnown map already translates.
type MetricResolveQuery struct {
	Source           string            // "spanmetrics" (D2) | "tracemetrics" (D3+)
	Metric           string            // calls_total | duration_milliseconds_bucket | errors_total …
	Agg              string            // rate|count|sum|avg|errors|error_rate|p50|p90|p95|p99
	Filters          map[string]string // key→value, equality matchers
	GroupBy          []string          // 0..N dotted keys; one series per unique tuple
	From, To         time.Time
	StepSeconds      int  // 0 = auto-pick from the window
	IncludeExemplars bool // fold per-bucket slow/error trace_ids into the result
}

// MetricResolveResult carries the series plus which tier served them (so the
// caller / operator can see whether the fine rollups or the fallback answered)
// and the resolved step.
type MetricResolveResult struct {
	Series      []SpanMetricSeries `json:"series"`
	Tier        string             `json:"tier"` // 1s|10s|1m|operation_summary_5m|spans
	StepSeconds int                `json:"stepSeconds"`
	Exemplars   []MetricExemplar   `json:"exemplars,omitempty"`
}

// MetricExemplar is "click a bucket → open the trace" — the representative
// slow and/or errored trace_id for one (bucket, groupKey) cell, served from
// the rollup's argMax(If)State exemplar columns.
type MetricExemplar struct {
	Time         int64    `json:"time"`     // bucket start, unix nanos
	GroupKey     []string `json:"groupKey"` // matches the series it annotates
	SlowTraceID  string   `json:"slowTraceId,omitempty"`
	ErrorTraceID string   `json:"errorTraceId,omitempty"`
}

const smCovTTL = 60 * time.Second

// spanmetricTier describes one rollup grain. Ordered coarse→fine by the
// resolver so "coarsest that satisfies" is a simple first-match.
type spanmetricTier struct {
	table    string // CH table name
	label    string // wire label (1s|10s|1m)
	grainSec int    // bucket size
	ttl      time.Duration
	hasRoute bool // 1s drops http_route to bound cardinality
}

// spanmetricTiers — coarse first. Mirrors the DDL in store.go (TTLs: 1m=30d,
// 10s=2d, 1s=6h; 1s has no http_route). Keep in sync with the CREATE
// statements — the retention numbers are load-bearing for tier eligibility.
var spanmetricTiers = []spanmetricTier{
	{table: "spanmetrics_1m", label: "1m", grainSec: 60, ttl: 30 * 24 * time.Hour, hasRoute: true},
	{table: "spanmetrics_10s", label: "10s", grainSec: 10, ttl: 2 * 24 * time.Hour, hasRoute: true},
	{table: "spanmetrics_1s", label: "1s", grainSec: 1, ttl: 6 * time.Hour, hasRoute: false},
}

// tierDimColumn maps a descriptor key to the spanmetrics rollup column, or
// returns ok=false when the key isn't one of the five rollup dimensions —
// which forces the fallback (raw spans can resolve arbitrary attributes; the
// rollups only carry service_name / name / kind / status_code / http_route).
func tierDimColumn(key string) (col string, ok bool) {
	switch key {
	case "service.name", "service_name":
		return "service_name", true
	case "name", "operation":
		return "name", true
	case "kind", "span.kind":
		return "kind", true
	case "status", "status_code":
		return "status_code", true
	case "http.route", "http_route":
		return "http_route", true
	default:
		return "", false
	}
}

// Span-metrik okuma çözünürlük sınırları (v0.9.27, second-resolution
// audit R1 + operatör kararı "10 saniye demiştik"):
//   minMetricStepSec — ClickHouse span-metrik TABANI 10s. Bu, Thanos
//     range-query'lerinin 15s tabanından AYRI bir dünya (o
//     internal/thanos/stepForWindow); karıştırılmaz.
//   maxMetricPoints — seri başına nokta tavanı (grafik + CH bütçesi).
const (
	minMetricStepSec = 10
	maxMetricPoints  = 720
)

// clampMetricStep — çözülen step'i iki yönden de sınırlar: 10s tabanı
// (operatör kararı; 1s/5s tier'ları resolver üzerinden okunmaz — yazım
// dokunulmaz) VE ≤maxMetricPoints bütçesi (explicit ?step ve >20.8g
// pencere delikleri). Normal UI davranışı değişmez: stepForWidth ≤720
// üretir ve dar-pencere step'i zaten ≥10'a yuvarlanır.
func clampMetricStep(step int, from, to time.Time) int {
	if step < minMetricStepSec {
		step = minMetricStepSec
	}
	spanSec := int(to.Sub(from).Seconds())
	if spanSec > 0 {
		if minStep := (spanSec + maxMetricPoints - 1) / maxMetricPoints; step < minStep {
			step = minStep
		}
	}
	return step
}

// metricAutoStep mirrors QuerySpanMetric's sub-10s ramp so the resolver's
// auto-step matches the rest of the metric surface (consistent bucket counts).
func metricAutoStep(from, to time.Time) int {
	span := to.Sub(from).Seconds()
	switch {
	case span <= 120:
		return 1
	case span <= 600:
		return 5
	case span <= 1800:
		return 10
	case span <= 3600:
		return 30
	case span <= 6*3600:
		return 60
	case span <= 24*3600:
		return 300
	case span <= 7*24*3600:
		return 1800
	default:
		return 3600
	}
}

// referencesRoute reports whether any filter or groupBy key resolves to the
// http_route column (which the 1s tier drops).
func referencesRoute(filters map[string]string, groupBy []string) bool {
	for k := range filters {
		if col, ok := tierDimColumn(k); ok && col == "http_route" {
			return true
		}
	}
	for _, k := range groupBy {
		if col, ok := tierDimColumn(k); ok && col == "http_route" {
			return true
		}
	}
	return false
}

// dimsFitTiers reports whether every filter+groupBy key maps to a rollup
// dimension. A single off-dimension key (e.g. db.system, a span attribute)
// means the rollups can't answer the query → fallback.
func dimsFitTiers(filters map[string]string, groupBy []string) bool {
	for k := range filters {
		if _, ok := tierDimColumn(k); !ok {
			return false
		}
	}
	for _, k := range groupBy {
		if _, ok := tierDimColumn(k); !ok {
			return false
		}
	}
	return true
}

// selectMetricTier is the pure grain-selection planner. It returns the
// coarsest tier whose grain ≤ step and whose retention + cutover floor cover
// the window, or ok=false to signal the caller must use the fallback path.
//
//   - coverageStart: earliest available bucket (forward-only cutover, also
//     raised by TTL). Windows starting before it must dual-read the fallback.
//   - now: injected so the test can pin "now" deterministically.
func selectMetricTier(from, to time.Time, stepSec int, coverageStart, now time.Time, filters map[string]string, groupBy []string) (spanmetricTier, bool) {
	// Off-dimension filter/groupBy → only raw spans can answer it.
	if !dimsFitTiers(filters, groupBy) {
		return spanmetricTier{}, false
	}
	// Window predates the fine-grain data (fresh cutover or TTL'd away).
	if from.Before(coverageStart) {
		return spanmetricTier{}, false
	}
	step := stepSec
	if step <= 0 {
		step = metricAutoStep(from, to)
	}
	// v0.9.27 — 10s tabanı burada da: doğrudan (step=0) çağıran bir
	// yol 1s tier'ı seçemesin (yazım dokunulmaz, okuma 10s'te taban).
	step = clampMetricStep(step, from, to)
	needRoute := referencesRoute(filters, groupBy)
	// Iterate coarse→fine and take the FIRST tier that fits, so we read the
	// fewest rows for the requested resolution.
	for _, t := range spanmetricTiers {
		if t.grainSec > step {
			continue // tier bucket coarser than the step → can't render at this resolution; try a finer tier
		}
		if needRoute && !t.hasRoute {
			continue // 1s drops http_route, so it can't satisfy a route predicate
		}
		if from.Before(now.Add(-t.ttl)) {
			continue // window starts before this tier's retention horizon
		}
		return t, true
	}
	return spanmetricTier{}, false
}

// spanmetricStateAgg is the single agg→SQL projection over the rollup states
// ("formulas once"). duration_* states are nanoseconds (spans.duration) → /1e6
// for ms. quantilesState was built with (0.5,0.9,0.95,0.99) → indices 1..4.
// rate is per-second (rps) to match the descriptor's defaultUnit; error_rate
// is a percentage. Every result is toNullable(toFloat64(…)) so the scanner
// reads one *float64 for every aggregation.
func spanmetricStateAgg(agg string, stepSec int) (string, error) {
	wrap := func(s string) string { return "toNullable(toFloat64(" + s + "))" }
	switch strings.ToLower(agg) {
	case "", "count":
		return wrap("countMerge(calls_state)"), nil
	case "rate":
		return wrap(fmt.Sprintf("countMerge(calls_state) / %d.0", stepSec)), nil
	case "errors":
		return wrap("countMerge(error_state)"), nil
	case "error_rate":
		return wrap("100.0 * countMerge(error_state) / nullIf(countMerge(calls_state), 0)"), nil
	case "sum":
		return wrap("sumMerge(duration_sum_state) / 1e6"), nil
	case "avg":
		return wrap("sumMerge(duration_sum_state) / nullIf(countMerge(calls_state), 0) / 1e6"), nil
	case "p50":
		return wrap("arrayElement(quantilesTDigestMerge(0.5, 0.9, 0.95, 0.99)(duration_q_state), 1) / 1e6"), nil
	case "p90":
		return wrap("arrayElement(quantilesTDigestMerge(0.5, 0.9, 0.95, 0.99)(duration_q_state), 2) / 1e6"), nil
	case "p95":
		return wrap("arrayElement(quantilesTDigestMerge(0.5, 0.9, 0.95, 0.99)(duration_q_state), 3) / 1e6"), nil
	case "p99":
		return wrap("arrayElement(quantilesTDigestMerge(0.5, 0.9, 0.95, 0.99)(duration_q_state), 4) / 1e6"), nil
	}
	return "", fmt.Errorf("unknown aggregation %q", agg)
}

// spanmetricExemplarCols returns the two trailing SELECT columns that hand
// back per-bucket representative trace_ids. The finalizer MUST match each
// state's combinator: slow_exemplar_state is argMaxState → argMaxMerge;
// error_exemplar_state is argMaxIfState → argMaxIfMerge. argMaxMerge on the
// If-state fails at runtime ("argMax requires two arguments") — the v0.8.51
// runtime-gate catch ([[feedback-unit-mixing-needs-both-branches]] sibling:
// a state's *Merge is part of its contract; a string test can't see the
// mismatch, only CH can).
func spanmetricExemplarCols() string {
	return ",\n\t\t    argMaxMerge(slow_exemplar_state) AS slow_trace," +
		"\n\t\t    argMaxIfMerge(error_exemplar_state) AS error_trace"
}

// ResolveMetricQuery turns a descriptor into a tier-selected metric read,
// dual-reading the legacy path for windows that predate the fine-grain rollups.
func (s *Store) ResolveMetricQuery(ctx context.Context, q MetricResolveQuery) (MetricResolveResult, error) {
	step := q.StepSeconds
	if step <= 0 {
		step = metricAutoStep(q.From, q.To)
	}
	// v0.9.27 (second-resolution audit R1) — nokta-bütçesi kelepçesi:
	// ne olursa olsun seri başına ≤maxMetricPoints. metricAutoStep bu
	// bütçeyi kendi tutar ama (a) explicit q.StepSeconds tüm giriş
	// noktalarında (api ?step=/body.Step, dql plan.StepSeconds)
	// clamp'siz gelir — ölçümde 6h+step=1 = 21.600 nokta (43× bütçe)
	// üretildi; (b) metricAutoStep default dalı >20.8g pencerede
	// sınırsız. Kelepçe her iki deliği de kapatır; normal UI davranışı
	// DEĞİŞMEZ (stepForWidth zaten ≤720 üretiyor, floor'un altında).
	step = clampMetricStep(step, q.From, q.To)

	if q.Source == "tracemetrics" {
		return s.resolveTraceMetric(ctx, q, step)
	}

	// v0.8.410 (Tempo-parity T1) — agg "band": ONE call returns the
	// whole p50/p90/p95/p99 percentile band (+ the usual exemplars),
	// instead of four round-trips that each re-merge the same tdigest
	// state. The Grafana/Tempo RED duration panel is exactly this.
	if strings.EqualFold(q.Agg, "band") {
		return s.resolveBand(ctx, q, step)
	}

	tier, useFine := selectMetricTier(q.From, q.To, step, s.spanmetricsCoverageStart(ctx), time.Now(), q.Filters, q.GroupBy)
	if !useFine {
		series, err := s.QuerySpanMetric(ctx, q.toSpanMetricFilter(step))
		if err != nil {
			return MetricResolveResult{}, err
		}
		return MetricResolveResult{Series: series, Tier: "spans", StepSeconds: step}, nil
	}

	aggExpr, err := spanmetricStateAgg(q.Agg, step)
	if err != nil {
		return MetricResolveResult{}, err
	}

	// GroupBy → an Array(String) tuple, columns in the operator's order.
	groupSelect := "[]::Array(String)"
	if len(q.GroupBy) > 0 {
		cols := make([]string, len(q.GroupBy))
		for i, k := range q.GroupBy {
			col, _ := tierDimColumn(k) // dimsFitTiers already proved ok
			cols[i] = col
		}
		groupSelect = "[" + strings.Join(cols, ", ") + "]"
	}

	// WHERE: time bounds on the indexed bucket + equality dim filters.
	conds := []string{"time_bucket >= ?", "time_bucket <= ?"}
	args := []any{q.From, q.To}
	for k, v := range q.Filters {
		col, _ := tierDimColumn(k)
		conds = append(conds, col+" = ?")
		args = append(args, v)
	}

	exemplarCols := ""
	if q.IncludeExemplars {
		exemplarCols = spanmetricExemplarCols()
	}

	sql := fmt.Sprintf(`
		SELECT
		    toUnixTimestamp(toStartOfInterval(time_bucket, INTERVAL %d SECOND)) * 1000000000 AS bucket,
		    %s AS gk,
		    %s AS v%s
		FROM %s
		WHERE %s
		GROUP BY bucket, gk
		ORDER BY gk, bucket
		LIMIT 50000
		SETTINGS max_execution_time = 30`,
		step, groupSelect, aggExpr, exemplarCols, s.spanmetricsSourceFor(tier.table), strings.Join(conds, " AND "))

	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return MetricResolveResult{}, fmt.Errorf("resolve metric (%s): %w", tier.label, err)
	}
	defer rows.Close()

	seriesMap := make(map[string]*SpanMetricSeries)
	var order []string
	var exemplars []MetricExemplar
	for rows.Next() {
		var bucket uint64
		var gk []string
		var val *float64
		var slowTrace, errTrace string
		if q.IncludeExemplars {
			if err := rows.Scan(&bucket, &gk, &val, &slowTrace, &errTrace); err != nil {
				return MetricResolveResult{}, err
			}
		} else {
			if err := rows.Scan(&bucket, &gk, &val); err != nil {
				return MetricResolveResult{}, err
			}
		}
		key := strings.Join(gk, "|")
		ser, ok := seriesMap[key]
		if !ok {
			ser = &SpanMetricSeries{GroupKey: gk}
			seriesMap[key] = ser
			order = append(order, key)
		}
		v := 0.0
		if val != nil {
			v = *val
		}
		ser.Points = append(ser.Points, SpanMetricPoint{Time: int64(bucket), Value: v})
		if q.IncludeExemplars && (slowTrace != "" || errTrace != "") {
			exemplars = append(exemplars, MetricExemplar{
				Time: int64(bucket), GroupKey: gk,
				SlowTraceID: slowTrace, ErrorTraceID: errTrace,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return MetricResolveResult{}, err
	}
	out := make([]SpanMetricSeries, 0, len(order))
	for _, k := range order {
		out = append(out, *seriesMap[k])
	}
	return MetricResolveResult{Series: out, Tier: tier.label, StepSeconds: step, Exemplars: exemplars}, nil
}

// toSpanMetricFilter maps the descriptor onto the legacy SpanMetricFilter so
// the fallback reuses QuerySpanMetric (operation_summary_5m → raw spans). The
// agg names already line up; only the field needs deriving (latency aggs read
// duration_ms, count-shaped aggs ignore it).
func (q MetricResolveQuery) toSpanMetricFilter(step int) SpanMetricFilter {
	field := ""
	switch strings.ToLower(q.Agg) {
	case "avg", "p50", "p90", "p95", "p99", "p999", "sum", "min", "max":
		field = "duration_ms"
	}
	filters := make([]FilterExpr, 0, len(q.Filters))
	for k, v := range q.Filters {
		filters = append(filters, FilterExpr{Key: k, Op: "=", Values: []string{v}})
	}
	return SpanMetricFilter{
		Filters:     filters,
		Aggregation: q.Agg,
		Field:       field,
		GroupBy:     q.GroupBy,
		From:        q.From,
		To:          q.To,
		StepSeconds: step,
	}
}

// bandQuantileLabels — the fine-tier band lines, index-aligned with
// the doorway states' quantilesTDigestState(0.5, 0.9, 0.95, 0.99).
var bandQuantileLabels = []string{"p50", "p90", "p95", "p99"}

// bandProjection — every band quantile from ONE tdigest merge, in ms.
// arrayMap keeps it a single state finalization per (bucket, gk) row;
// four arrayElement projections would invite CH to merge the digest
// four times.
func bandProjection() string {
	return "arrayMap(x -> toFloat64(x / 1e6), quantilesTDigestMerge(0.5, 0.9, 0.95, 0.99)(duration_q_state))"
}

// relabelBandSeries appends the quantile label to every series'
// GroupKey so a grouped band ("checkout|p95") stays distinguishable
// and the exemplar GroupKey matching in the UI keeps working. Pure —
// table-tested (v0.8.410).
func relabelBandSeries(in []SpanMetricSeries, label string) []SpanMetricSeries {
	out := make([]SpanMetricSeries, len(in))
	for i, ser := range in {
		gk := make([]string, 0, len(ser.GroupKey)+1)
		gk = append(gk, ser.GroupKey...)
		gk = append(gk, label)
		out[i] = SpanMetricSeries{GroupKey: gk, Points: ser.Points}
	}
	return out
}

// resolveBand serves agg="band" (v0.8.410): the full percentile band
// in one read. Fine tiers: single SQL over the doorway rollup, four
// series per group key, exemplars attached to the p99 line (the
// Tempo convention — dots ride the top of the band). Fallback
// (pre-cutover window / off-tier dims): three bounded QuerySpanMetric
// passes — the 5m/raw tdigest carries (0.5, 0.95, 0.99), so the
// fallback band has no p90 line; an honest 3-line band beats a fake
// interpolation.
func (s *Store) resolveBand(ctx context.Context, q MetricResolveQuery, step int) (MetricResolveResult, error) {
	tier, useFine := selectMetricTier(q.From, q.To, step, s.spanmetricsCoverageStart(ctx), time.Now(), q.Filters, q.GroupBy)
	if !useFine {
		out := []SpanMetricSeries{}
		for _, agg := range []string{"p50", "p95", "p99"} {
			fq := q
			fq.Agg = agg
			series, err := s.QuerySpanMetric(ctx, fq.toSpanMetricFilter(step))
			if err != nil {
				return MetricResolveResult{}, err
			}
			out = append(out, relabelBandSeries(series, agg)...)
		}
		return MetricResolveResult{Series: out, Tier: "spans", StepSeconds: step}, nil
	}

	groupSelect := "[]::Array(String)"
	if len(q.GroupBy) > 0 {
		cols := make([]string, len(q.GroupBy))
		for i, k := range q.GroupBy {
			col, _ := tierDimColumn(k)
			cols[i] = col
		}
		groupSelect = "[" + strings.Join(cols, ", ") + "]"
	}
	conds := []string{"time_bucket >= ?", "time_bucket <= ?"}
	args := []any{q.From, q.To}
	for k, v := range q.Filters {
		col, _ := tierDimColumn(k)
		conds = append(conds, col+" = ?")
		args = append(args, v)
	}
	exemplarCols := ""
	if q.IncludeExemplars {
		exemplarCols = spanmetricExemplarCols()
	}

	sql := fmt.Sprintf(`
		SELECT
		    toUnixTimestamp(toStartOfInterval(time_bucket, INTERVAL %d SECOND)) * 1000000000 AS bucket,
		    %s AS gk,
		    %s AS band%s
		FROM %s
		WHERE %s
		GROUP BY bucket, gk
		ORDER BY gk, bucket
		LIMIT 50000
		SETTINGS max_execution_time = 30`,
		step, groupSelect, bandProjection(), exemplarCols, s.spanmetricsSourceFor(tier.table), strings.Join(conds, " AND "))

	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return MetricResolveResult{}, fmt.Errorf("resolve band (%s): %w", tier.label, err)
	}
	defer rows.Close()

	type bandGroup struct {
		lines [4]*SpanMetricSeries
	}
	groups := make(map[string]*bandGroup)
	var order []string
	var exemplars []MetricExemplar
	for rows.Next() {
		var bucket uint64
		var gk []string
		var band []float64
		var slowTrace, errTrace string
		if q.IncludeExemplars {
			if err := rows.Scan(&bucket, &gk, &band, &slowTrace, &errTrace); err != nil {
				return MetricResolveResult{}, err
			}
		} else {
			if err := rows.Scan(&bucket, &gk, &band); err != nil {
				return MetricResolveResult{}, err
			}
		}
		key := strings.Join(gk, "|")
		g, ok := groups[key]
		if !ok {
			g = &bandGroup{}
			for i, lbl := range bandQuantileLabels {
				lgk := make([]string, 0, len(gk)+1)
				lgk = append(lgk, gk...)
				lgk = append(lgk, lbl)
				g.lines[i] = &SpanMetricSeries{GroupKey: lgk}
			}
			groups[key] = g
			order = append(order, key)
		}
		for i := range bandQuantileLabels {
			v := 0.0
			if i < len(band) {
				v = band[i]
			}
			g.lines[i].Points = append(g.lines[i].Points, SpanMetricPoint{Time: int64(bucket), Value: v})
		}
		if q.IncludeExemplars && (slowTrace != "" || errTrace != "") {
			exemplars = append(exemplars, MetricExemplar{
				Time: int64(bucket), GroupKey: g.lines[3].GroupKey,
				SlowTraceID: slowTrace, ErrorTraceID: errTrace,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return MetricResolveResult{}, err
	}
	out := make([]SpanMetricSeries, 0, len(order)*4)
	for _, k := range order {
		for _, ln := range groups[k].lines {
			out = append(out, *ln)
		}
	}
	return MetricResolveResult{Series: out, Tier: tier.label, StepSeconds: step, Exemplars: exemplars}, nil
}

// spanmetricsCoverageStart returns the earliest available spanmetrics bucket —
// the forward-only cutover floor, also raised as TTL drops old partitions.
// Cached for smCovTTL so chart renders don't probe min(time_bucket) every
// time. Fail-safe: on probe error or an empty/sentinel result it returns
// time.Now(), which makes the resolver prefer the reliable fallback rather
// than risk rendering a gap.
func (s *Store) spanmetricsCoverageStart(ctx context.Context) time.Time {
	s.smCovMu.RLock()
	if !s.smCovAt.IsZero() && time.Since(s.smCovAt) < smCovTTL {
		v := s.smCovVal
		s.smCovMu.RUnlock()
		return v
	}
	s.smCovMu.RUnlock()

	earliest := time.Now()
	var probed time.Time
	row := s.conn.QueryRow(ctx, "SELECT min(time_bucket) FROM "+s.spanmetricsSourceFor("spanmetrics_1m")+" SETTINGS max_execution_time = 5")
	if err := row.Scan(&probed); err == nil && probed.Year() >= 2000 {
		earliest = probed
	}

	s.smCovMu.Lock()
	s.smCovAt = time.Now()
	s.smCovVal = earliest
	s.smCovMu.Unlock()
	return earliest
}
