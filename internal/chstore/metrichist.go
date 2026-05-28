package chstore

import (
	"context"
	"fmt"
)

// HistogramSeries is the read-time shape of an explicit OTel histogram over
// a time window: a shared set of explicit bucket bounds, one summed
// bucket-count vector per time bucket, and p50/p95/p99 estimated from those
// vectors. Drives the /metrics histogram heatmap (v0.6.56).
type HistogramSeries struct {
	Bounds  []float64  `json:"bounds"`  // explicit upper bounds (len N)
	Times   []int64    `json:"times"`   // ns epoch, one per time bucket
	Counts  [][]uint64 `json:"counts"`  // [timeBucket][bucket] summed (len N+1)
	P50     []float64  `json:"p50"`
	P95     []float64  `json:"p95"`
	P99     []float64  `json:"p99"`
	Skipped int        `json:"skipped"` // series dropped for mismatched bounds
}

// QueryMetricHistogram reads explicit-histogram metric_points over a window
// and returns a time × bucket heatmap + per-time-bucket percentiles.
// Cumulative-temporality series are delta'd PER SERIES before binning so the
// heatmap shows per-interval density rather than a monotonically growing
// cumulative. v1 aggregates every matching attribute set into one heatmap;
// series whose bucket layout differs from the canonical one are skipped
// (Skipped surfaced to the UI). CH-bounded: metric-scoped + time-bounded
// WHERE, LIMIT, max_execution_time.
func (s *Store) QueryMetricHistogram(ctx context.Context, f MetricQueryFilter) (*HistogramSeries, error) {
	if f.Name == "" {
		return nil, fmt.Errorf("metric name required")
	}

	var wc whereClause
	wc.add("metric = ?", f.Name)
	if f.Service != "" {
		wc.add("service_name = ?", f.Service)
	}
	if !f.From.IsZero() {
		wc.add("time >= ?", f.From)
	}
	if !f.To.IsZero() {
		wc.add("time <= ?", f.To)
	}
	ApplyFilters(&wc, f.Filters)
	// Only explicit-histogram rows carry buckets; skip gauges/sums that
	// share the metric name (and pre-v0.5.358 rows with empty arrays).
	wc.add("length(bucket_counts) > 0")

	stepSec := f.StepSeconds
	if stepSec <= 0 {
		stepSec = autoStepSeconds(f.To.Sub(f.From).Seconds())
	}

	rows, err := s.conn.Query(ctx, `
		SELECT toUnixTimestamp64Nano(time) AS t,
		       attr_keys, attr_values, bucket_bounds, bucket_counts, temporality
		FROM metric_points
		`+wc.sql()+`
		ORDER BY t
		LIMIT 200000
		SETTINGS max_execution_time = 30`, wc.args...)
	if err != nil {
		return nil, fmt.Errorf("histogram query: %w", err)
	}
	defer rows.Close()

	type pt struct {
		t      int64
		counts []uint64
	}
	type ser struct {
		bounds      []float64
		temporality string
		pts         []pt
	}
	seriesMap := make(map[string]*ser)
	var order []string
	for rows.Next() {
		var t int64
		var attrK, attrV []string
		var bounds []float64
		var counts []uint64
		var temporality string
		if err := rows.Scan(&t, &attrK, &attrV, &bounds, &counts, &temporality); err != nil {
			return nil, err
		}
		key := joinKV(attrK, attrV)
		sp, ok := seriesMap[key]
		if !ok {
			sp = &ser{bounds: bounds, temporality: temporality}
			seriesMap[key] = sp
			order = append(order, key)
		}
		// Query is ORDER BY t, so each series' points land time-ascending —
		// the order cumulativeToDelta needs.
		sp.pts = append(sp.pts, pt{t: t, counts: counts})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Canonical bounds = the first series' bounds. Series whose layout
	// differs (producer changed config / a different metric reused the
	// name) are skipped rather than summed into the wrong bucket.
	var canonical []float64
	for _, k := range order {
		if len(seriesMap[k].bounds) > 0 {
			canonical = seriesMap[k].bounds
			break
		}
	}
	if canonical == nil || f.From.IsZero() || f.To.IsZero() {
		return &HistogramSeries{Bounds: canonical}, nil
	}

	nBuckets := len(canonical) + 1
	fromNs := f.From.UnixNano()
	stepNs := int64(stepSec) * 1_000_000_000
	if stepNs <= 0 {
		stepNs = 1_000_000_000
	}
	nTime := int((f.To.UnixNano()-fromNs)/stepNs) + 1
	if nTime <= 0 {
		nTime = 1
	}

	accum := make([][]uint64, nTime)
	for i := range accum {
		accum[i] = make([]uint64, nBuckets)
	}

	skipped := 0
	for _, k := range order {
		sp := seriesMap[k]
		if len(sp.bounds) != len(canonical) {
			skipped++
			continue
		}
		raw := make([][]uint64, len(sp.pts))
		for i, p := range sp.pts {
			raw[i] = p.counts
		}
		deltas := bucketDeltas(sp.temporality, raw)
		for i, p := range sp.pts {
			tb := int((p.t - fromNs) / stepNs)
			if tb < 0 || tb >= nTime {
				continue
			}
			d := deltas[i]
			for j := 0; j < nBuckets && j < len(d); j++ {
				accum[tb][j] += d[j]
			}
		}
	}

	out := &HistogramSeries{
		Bounds:  canonical,
		Times:   make([]int64, nTime),
		Counts:  accum,
		P50:     make([]float64, nTime),
		P95:     make([]float64, nTime),
		P99:     make([]float64, nTime),
		Skipped: skipped,
	}
	for i := 0; i < nTime; i++ {
		out.Times[i] = fromNs + int64(i)*stepNs
		out.P50[i] = percentileFromBuckets(canonical, accum[i], 0.50)
		out.P95[i] = percentileFromBuckets(canonical, accum[i], 0.95)
		out.P99[i] = percentileFromBuckets(canonical, accum[i], 0.99)
	}
	return out, nil
}

// autoStepSeconds mirrors QueryMetric's window→step rungs so the histogram
// heatmap's time resolution matches the line-chart paths the operator is
// used to (≤2m → 1s … >7d → 1h).
func autoStepSeconds(span float64) int {
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

// joinKV builds a stable per-series identity from the paired attribute
// key/value arrays. \x1f (unit separator) can't appear in attribute text,
// so distinct attribute sets never collide.
func joinKV(keys, vals []string) string {
	b := make([]byte, 0, 32)
	for i := range keys {
		b = append(b, keys[i]...)
		b = append(b, '=')
		if i < len(vals) {
			b = append(b, vals[i]...)
		}
		b = append(b, '\x1f')
	}
	return string(b)
}

// ── pure helpers (TDD'd in metrichist_test.go) ──────────────────────────────

// percentileFromBuckets estimates the p-th percentile (p in [0,1]) of an
// explicit OTel histogram via linear interpolation within the holding
// bucket. bounds = ExplicitBounds (len N, ascending); counts has N+1
// elements (counts[i] = observations in bucket i; counts[N] = the +Inf
// overflow bucket). Returns 0 for an empty histogram. The +Inf bucket has
// no finite upper bound, so a percentile that lands there clamps to the
// last finite bound (we can't claim more).
func percentileFromBuckets(bounds []float64, counts []uint64, p float64) float64 {
	if len(bounds) == 0 || len(counts) == 0 {
		return 0
	}
	var total uint64
	for _, c := range counts {
		total += c
	}
	if total == 0 {
		return 0
	}
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	target := p * float64(total)

	var cum uint64
	for i := 0; i < len(counts); i++ {
		cum += counts[i]
		if float64(cum) < target {
			continue
		}
		if i >= len(bounds) {
			// +Inf overflow bucket — clamp to the last finite bound.
			return bounds[len(bounds)-1]
		}
		lo := 0.0
		if i > 0 {
			lo = bounds[i-1]
		}
		hi := bounds[i]
		cnt := counts[i]
		if cnt == 0 {
			return hi
		}
		cumBefore := float64(cum - counts[i])
		frac := (target - cumBefore) / float64(cnt)
		if frac < 0 {
			frac = 0
		}
		if frac > 1 {
			frac = 1
		}
		return lo + (hi-lo)*frac
	}
	return bounds[len(bounds)-1]
}

// bucketDeltas converts a per-series, time-ordered set of bucket-count
// vectors into per-interval counts. Cumulative temporality is delta'd;
// delta and unknown ("") pass through unchanged (delta-assumed) so we never
// mis-delta a series we couldn't classify.
func bucketDeltas(temporality string, series [][]uint64) [][]uint64 {
	if temporality == "cumulative" {
		return cumulativeToDelta(series)
	}
	return series
}

// cumulativeToDelta turns a cumulative bucket series (counts monotonically
// increasing since start_time) into per-interval deltas. The first point is
// a zero baseline (we can't know the pre-window increment); a counter reset
// (any bucket drops, or the layout changes) uses the post-reset value as
// the delta — standard Prometheus reset handling.
func cumulativeToDelta(series [][]uint64) [][]uint64 {
	out := make([][]uint64, len(series))
	for i := range series {
		if i == 0 {
			out[i] = make([]uint64, len(series[i]))
			continue
		}
		prev, cur := series[i-1], series[i]
		if len(prev) != len(cur) || isCounterReset(prev, cur) {
			out[i] = append([]uint64(nil), cur...)
			continue
		}
		d := make([]uint64, len(cur))
		for j := range cur {
			d[j] = cur[j] - prev[j]
		}
		out[i] = d
	}
	return out
}

func isCounterReset(prev, cur []uint64) bool {
	for j := range cur {
		if cur[j] < prev[j] {
			return true
		}
	}
	return false
}
