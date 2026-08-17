package chstore

import (
	"context"
	"fmt"
	"math"
	"strings"
)

// HistogramSeries is the read-time shape of an explicit OTel histogram over
// a time window: a shared set of explicit bucket bounds, one summed
// bucket-count vector per time bucket, and p50/p95/p99 estimated from those
// vectors. Drives the /metrics histogram heatmap (v0.6.56).
type HistogramSeries struct {
	Bounds  []float64  `json:"bounds"` // explicit upper bounds (len N)
	Times   []int64    `json:"times"`  // ns epoch, one per time bucket
	Counts  [][]uint64 `json:"counts"` // [timeBucket][bucket] summed (len N+1)
	P50     []float64  `json:"p50"`
	P95     []float64  `json:"p95"`
	P99     []float64  `json:"p99"`
	Skipped int        `json:"skipped"` // series dropped for mismatched bounds
	// RowCapped (v0.9.473, dürüstlük A13) — 200k satır tavanı doldu:
	// ORDER BY t ASC + LIMIT pencerenin SAĞ kenarını keser; heatmap
	// "trafik düştü" gibi okunmasın diye UI bunu söyler.
	RowCapped bool `json:"rowCapped,omitempty"`
	// Note (v0.9.1157, VM Faz 2) — bu ısı haritası neden BOŞ döndü.
	//
	// Yalnız VictoriaMetrics yolu doldurur ve yalnız BOŞ sonuçta: orada
	// sorgu, operatörün YAZMADIĞI bir seri adına gider (`<ad>_bucket`),
	// yani "veri yok" ile "bu metrik histogram değil" birbirinden
	// ayrılamaz. ClickHouse yolu bunu HİÇ set etmez — orada kova
	// düzeni satırın kendisinde, tahmin edilen bir ad yok — ve
	// `omitempty` sayesinde CH gövdesi bayt-bayt eskisi kalır.
	Note string `json:"note,omitempty"`
}

// maxHistogramBuckets bounds the Go-side time-bucket allocation in the
// histogram assembly (accum[nTime][nBuckets]). 5000 is well beyond any panel's
// pixel width, so raising a caller's step to fit costs no visible resolution.
const maxHistogramBuckets = 5000

// clampHistogramStep raises stepSec, if needed, so a (spanSec / stepSec) time-
// bucket count stays ≤ maxHistogramBuckets — the memory guard for a
// caller-pinned tiny step over a wide window (PromQL Phase-2 review, CRITICAL).
// A no-op for sane windows; only bites the pathological step=1 / multi-year
// case. stepSec ≤ 0 (auto) is left to the auto-step resolver.
func clampHistogramStep(spanSec float64, stepSec int) int {
	if stepSec <= 0 || spanSec <= 0 {
		return stepSec
	}
	minStep := int(math.Ceil(spanSec / float64(maxHistogramBuckets)))
	if minStep > stepSec {
		return minStep
	}
	return stepSec
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
	// v0.9.797 — route dışlamaları ısı haritasında da geçerli. Bu yol
	// AYNI ZAMANDA grupsuz histogram_quantile'ın çekirdeği
	// (queryHistogramQuantiles → QueryMetricHistogram), yani tek satır
	// iki yüzeyi birden temizliyor.
	applyMetricExclusionWhere(&wc, s.MetricExclusions(), f.Name)
	// Only explicit-histogram rows carry buckets; skip gauges/sums that
	// share the metric name (and pre-v0.5.358 rows with empty arrays).
	wc.add("length(bucket_counts) > 0")

	stepSec := f.StepSeconds
	if stepSec <= 0 {
		stepSec = autoStepSeconds(f.To.Sub(f.From).Seconds())
	}
	// v0.9.114 (PromQL Phase-2 review, CRITICAL) — cap the Go-side time-bucket
	// allocation (accum[nTime][nBuckets] below) against a CALLER-PINNED tiny
	// step over a wide window. The CH LIMIT/max_execution_time bound the SCAN,
	// not this post-processing; without this, ?step=1&from=<years-ago> made
	// nTime ~1e9 and OOM'd the single binary (reachable via /api/metrics/promql
	// AND the sibling /api/metrics/histogram). Raising the step keeps nTime
	// under maxHistogramBuckets — you can't render more points than that anyway.
	stepSec = clampHistogramStep(f.To.Sub(f.From).Seconds(), stepSec)

	rows, err := s.conn.Query(ctx, `
		SELECT toUnixTimestamp64Nano(time) AS t,
		       attr_keys, attr_values, bucket_bounds, bucket_counts, temporality
		FROM metric_points
		`+wc.sql()+`
		ORDER BY t
		LIMIT 200000
		SETTINGS max_execution_time = 25`, wc.args...)
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
	rowsScanned := 0
	for rows.Next() {
		rowsScanned++
		var t int64
		var attrK, attrV []string
		var bounds []float64
		var counts []uint64
		var temporality string
		if err := rows.Scan(&t, &attrK, &attrV, &bounds, &counts, &temporality); err != nil {
			return nil, err
		}
		// v0.8.440 (review-confirmed) — bounds parmak izi anahtara girer:
		// exp-histogram materializasyonu (v0.8.435) rescale/offset
		// kaymasında AYNI seri için FARKLI bound dizileri üretir; tek
		// anahtar altında toplansalar sayımlar pozisyonel olarak yanlış
		// bucket'lara akar ve cumulativeToDelta uyumsuz layout'lar
		// arasında çıkarma yapar. Her bounds-run kendi serisi olur;
		// canonical'a değer bazında uymayanlar aşağıda dürüstçe
		// Skipped'e düşer (yanlış quantile yerine görünmez kuyruk).
		key := joinKV(attrK, attrV) + "\x00" + boundsKey(bounds)
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

	// v0.9.754 (operatör: 749 sonrası RT·metrik boş) — kanonik bounds
	// artık İLK değil BASKIN seri düzeni: 19+ fiziksel serili prod
	// servisinde ilk karşılaşılan seri azınlık bounds-set'indeyse eski
	// politika kalan her şeyi atlayıp paneli BOŞALTIYORDU (749 gecikmeyi
	// gruplu yoldan bu global yola taşıyınca görünür oldu). Ağırlık =
	// serinin nokta sayısı; eşitlikte ilk görülen (deterministik).
	// Uyumsuz serilerin atlanması (yanlış kovaya toplamama) DEĞİŞMEDİ.
	bw := make([]boundsWeight, 0, len(order))
	for _, k := range order {
		bw = append(bw, boundsWeight{Bounds: seriesMap[k].bounds, Weight: len(seriesMap[k].pts)})
	}
	canonical := dominantBounds(bw)
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
		// Uzunluk YETMEZ (v0.8.440): rescale aynı uzunlukta farklı
		// değerli bound'lar üretebilir — değer bazında karşılaştır.
		if !boundsEqual(sp.bounds, canonical) {
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
		Bounds:    canonical,
		Times:     make([]int64, nTime),
		Counts:    accum,
		P50:       make([]float64, nTime),
		P95:       make([]float64, nTime),
		P99:       make([]float64, nTime),
		Skipped:   skipped,
		RowCapped: rowsScanned >= 200000,
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

// PercentileFromBuckets is the EXPORTED wrapper over the estimator above
// (v0.9.1157, VM Faz 2).
//
// It exists so the VictoriaMetrics heatmap path (internal/vmetrics) can
// estimate p50/p95/p99 from ITS assembled bucket vectors with the SAME
// arithmetic the ClickHouse path uses, rather than carrying a second copy.
// A second copy is the thing to avoid here specifically: the two backends
// answer the same panel, so a divergence in the interpolation would show up
// as "VictoriaMetrics reports a different p99" and be read as a data
// problem rather than as two implementations of one formula.
//
// Thin on purpose — no behaviour of its own, so there is nothing here that
// can drift from the unexported original.
func PercentileFromBuckets(bounds []float64, counts []uint64, p float64) float64 {
	return percentileFromBuckets(bounds, counts, p)
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

// boundsKey — bounds dizisinin seri-anahtarı parçası (v0.8.440).
// Float64 bit desenleri üzerinden deterministik; ayırıcı 0x1F.
func boundsKey(b []float64) string {
	if len(b) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, v := range b {
		if i > 0 {
			sb.WriteByte(0x1f)
		}
		fmt.Fprintf(&sb, "%x", math.Float64bits(v))
	}
	return sb.String()
}

// boundsEqual — değer bazında bound karşılaştırması (v0.8.440). CH
// Float64 roundtrip'i bit-exact olduğundan tam eşitlik doğru ölçüt.
func boundsEqual(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// boundsWeight — dominantBounds girdisi: bir serinin bounds düzeni +
// ağırlığı (nokta sayısı).
type boundsWeight struct {
	Bounds []float64
	Weight int
}

// dominantBounds — SAF: en çok toplam ağırlığa sahip bounds-set;
// eşitlikte ilk görülen kazanır. Boş bounds aday değildir.
func dominantBounds(entries []boundsWeight) []float64 {
	type cand struct {
		bounds []float64
		weight int
	}
	var cands []cand
	for _, e := range entries {
		if len(e.Bounds) == 0 {
			continue
		}
		found := false
		for i := range cands {
			if boundsEqual(cands[i].bounds, e.Bounds) {
				cands[i].weight += e.Weight
				found = true
				break
			}
		}
		if !found {
			cands = append(cands, cand{e.Bounds, e.Weight})
		}
	}
	var best []float64
	bestW := -1
	for _, c := range cands {
		if c.weight > bestW {
			best, bestW = c.bounds, c.weight
		}
	}
	return best
}
