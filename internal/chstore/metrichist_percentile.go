package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// metrichist_percentile.go (F3, v0.9.107 — PromQL histogram_quantile parity) —
// histogram metriğinde agg=p50/p95/p99'u DOĞRU şekilde bucket dağılımından
// hesaplar (metrichist.go'daki reset-korumalı bucket-delta + lineer-interp
// quantile makinesi). Eski yol metricAggToSQL → quantile(value) idi: value
// per-export ORTALAMA olduğundan histogram'da YANLIŞ (ortalamaların quantile'ı,
// dağılımın değil). Roadmap F3.
//
// İki yol:
//   - groupBy YOK  → test'li QueryMetricHistogram'ı (global) kullan, istenen
//     percentile serisini çıkar. PromQL `histogram_quantile(p, sum by (le)(…))`.
//   - groupBy VAR  → per-grup makine (aşağıda): fiziksel seri başına reset-
//     korumalı delta, sonra gk başına toplama → grup başına percentile.
//     PromQL `histogram_quantile(p, sum by (le, <labels>)(…))`.

// metricInstrument — metriğin OTLP instrument tipini probe'lar
// ('gauge'|'sum'|'histogram'|'exp_histogram'|''). histogram_quantile routing'i
// için. Pencere = sorgunun kendi [from,to]'su: 24s'lik bir histogram son 10dk'da
// susmuş olsa bile doğru route'a düşer (sabit 10dk boş dönüp value-quantile'a
// kaymaz). any() ilk satırda kısa devre + max_execution_time=3 = ucuz.
func (s *Store) metricInstrument(ctx context.Context, name, service string, from, to time.Time) string {
	if to.IsZero() {
		to = time.Now()
	}
	if from.IsZero() {
		from = to.Add(-metricIvProbeWindow)
	}
	q := `SELECT any(instrument) FROM metric_points WHERE metric = ? AND time >= ? AND time <= ?`
	args := []any{name, from, to}
	if service != "" {
		q += ` AND service_name = ?`
		args = append(args, service)
	}
	q += ` SETTINGS max_execution_time = 3`
	var inst string
	if err := s.conn.QueryRow(ctx, q, args...).Scan(&inst); err != nil {
		return ""
	}
	return inst
}

// isHistogramInstrument — hem explicit hem exponential histogram aynı
// bucket_bounds/bucket_counts kolonlarına materialize edilir (convert.go);
// read makinesi ikisini de değiştirmeden işler. İkisi de histogram_quantile
// yoluna gitmeli — sadece 'histogram' demek exp'i value-quantile'da (yanlış)
// bırakır.
func isHistogramInstrument(inst string) bool {
	return inst == "histogram" || inst == "exp_histogram"
}

// aggQuantile — agg etiketini quantile oranına (p∈[0,1]) çevirir.
func aggQuantile(agg string) (float64, bool) {
	switch strings.ToLower(agg) {
	case "p50":
		return 0.50, true
	case "p95":
		return 0.95, true
	case "p99":
		return 0.99, true
	default:
		return 0, false
	}
}

// pickPercentile — HistogramSeries'ten agg'e karşılık gelen percentile dizisini
// seçer (saf, test'li). Bilinmeyen agg → nil, false.
func pickPercentile(hs *HistogramSeries, agg string) ([]float64, bool) {
	if hs == nil {
		return nil, false
	}
	switch strings.ToLower(agg) {
	case "p50":
		return hs.P50, true
	case "p95":
		return hs.P95, true
	case "p99":
		return hs.P99, true
	default:
		return nil, false
	}
}

// QueryMetricHistogramPercentile — the p50/p95/p99 agg-string entry (chart
// picker). Maps to a quantile ratio and delegates to the float core.
func (s *Store) QueryMetricHistogramPercentile(ctx context.Context, f MetricQueryFilter, agg string) ([]SpanMetricSeries, error) {
	q, ok := aggQuantile(agg)
	if !ok {
		return nil, fmt.Errorf("histogram percentile: unsupported agg %q", agg)
	}
	return s.queryHistogramQuantile(ctx, f, q)
}

// QueryMetricHistogramQuantile — arbitrary-quantile entry (v0.9.119, PromQL
// histogram_quantile with any q ∈ [0,1], not just 0.5/0.95/0.99).
func (s *Store) QueryMetricHistogramQuantile(ctx context.Context, f MetricQueryFilter, q float64) ([]SpanMetricSeries, error) {
	if q < 0 || q > 1 {
		return nil, fmt.Errorf("histogram_quantile: quantile %g out of range [0,1]", q)
	}
	return s.queryHistogramQuantile(ctx, f, q)
}

// queryHistogramQuantile — histogram_quantile core (bucket dağılımından, q ∈
// [0,1]). Step F1 disiplini (metricAutoStepPx + clampStepToExport + nTime cap)
// uygulanıp alt yollara geçer. Boş zaman-bucket'ları (gözlem yok) nokta
// ÜRETMEZ → sahte p=0 spike yerine gap (PromQL semantiği).
func (s *Store) queryHistogramQuantile(ctx context.Context, f MetricQueryFilter, q float64) ([]SpanMetricSeries, error) {
	now := time.Now()
	if f.To.IsZero() {
		f.To = now
	}
	if f.From.IsZero() {
		f.From = f.To.Add(-24 * time.Hour)
	}
	if f.StepSeconds <= 0 {
		f.StepSeconds = metricAutoStepPx(f.From, f.To, f.MaxDataPoints)
	}
	if iv := s.metricExportInterval(ctx, f.Name, f.Service); iv > 0 {
		f.StepSeconds = clampStepToExport(f.StepSeconds, iv)
	}
	// v0.9.114 (review CRITICAL) — cap Go-side nTime alloc against a
	// caller-pinned tiny step + wide window (the grouped path builds its own
	// accum; the global path is protected inside QueryMetricHistogram).
	f.StepSeconds = clampHistogramStep(f.To.Sub(f.From).Seconds(), f.StepSeconds)

	// groupBy'lı sorgular gruplarını KAYBETMEZ: her grup kendi bucket
	// dağılımından percentile alır (yoksa tüm gruplar tek global satıra
	// karışırdı — sessiz regresyon).
	if len(f.GroupBy) > 0 {
		return s.queryHistogramPercentileGrouped(ctx, f, q)
	}

	// Global: test'li QueryMetricHistogram'ı yeniden kullan; keyfi q'yu onun
	// per-time-bucket accum'undan (hs.Counts) + bounds'undan hesapla
	// (percentileFromBuckets — precomputed p50/p95/p99'a bağlı kalmadan).
	hs, err := s.QueryMetricHistogram(ctx, f)
	if err != nil {
		return nil, err
	}
	if hs == nil || len(hs.Times) == 0 {
		return []SpanMetricSeries{}, nil
	}
	pts := make([]SpanMetricPoint, 0, len(hs.Times))
	for i, t := range hs.Times {
		// Boş bucket (gözlem yok) → nokta atla (gap), sahte 0 DEĞİL.
		if i >= len(hs.Counts) || bucketTotal(hs.Counts[i]) == 0 {
			continue
		}
		pts = append(pts, SpanMetricPoint{Time: t, Value: percentileFromBuckets(hs.Bounds, hs.Counts[i], q)})
	}
	return []SpanMetricSeries{{GroupKey: nil, Points: pts}}, nil
}

// bucketTotal — bir zaman-bucket'ındaki toplam gözlem (delta bucket sayıları).
// 0 ise o dilimde veri yok → percentile tanımsız → gap.
func bucketTotal(counts []uint64) uint64 {
	var t uint64
	for _, c := range counts {
		t += c
	}
	return t
}

// queryHistogramPercentileGrouped — groupBy'lı histogram_quantile. Fiziksel
// seri = (tam attr seti + bounds); her fiziksel seride reset-korumalı
// cumulativeToDelta (counter reset per-seri handle edilir), sonra gk başına
// toplama → grup başına percentile. QueryMetricHistogram'ın global toplamasının
// per-grup muadili; aynı test'li parçaları (bucketDeltas / boundsEqual /
// percentileFromBuckets) kullanır.
func (s *Store) queryHistogramPercentileGrouped(ctx context.Context, f MetricQueryFilter, q float64) ([]SpanMetricSeries, error) {
	// gk ifadesi SELECT'te → arg'ları WHERE arg'larından ÖNCE gelir.
	parts := make([]string, len(f.GroupBy))
	var gkArgs []any
	for i, k := range f.GroupBy {
		expr, args := groupKeyExprMetric(k)
		parts[i] = expr
		gkArgs = append(gkArgs, args...)
	}
	groupSelect := "[" + strings.Join(parts, ", ") + "]"

	var wc whereClause
	wc.add("metric = ?", f.Name)
	if f.Service != "" {
		wc.add("service_name = ?", f.Service)
	}
	wc.add("time >= ?", f.From)
	wc.add("time <= ?", f.To)
	ApplyMetricFilters(&wc, f.Filters)
	wc.add("length(bucket_counts) > 0")

	args := append(gkArgs, wc.args...)
	sql := `SELECT toUnixTimestamp64Nano(time) AS t, ` + groupSelect + ` AS gk,
	               attr_keys, attr_values, bucket_bounds, bucket_counts, temporality
	        FROM metric_points ` + wc.sql() + `
	        ORDER BY t
	        LIMIT 200000
	        SETTINGS max_execution_time = 30`

	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("grouped histogram percentile query: %w", err)
	}
	defer rows.Close()

	type pt struct {
		t      int64
		counts []uint64
	}
	type physSer struct {
		gk          []string
		bounds      []float64
		temporality string
		pts         []pt
	}
	physMap := make(map[string]*physSer)
	var order []string
	for rows.Next() {
		var t int64
		var gk, attrK, attrV []string
		var bounds []float64
		var counts []uint64
		var temporality string
		if err := rows.Scan(&t, &gk, &attrK, &attrV, &bounds, &counts, &temporality); err != nil {
			return nil, err
		}
		// Fiziksel seri = gk + tam attr seti + bounds parmak izi (bkz.
		// QueryMetricHistogram v0.8.440: rescale aynı uzunlukta farklı bound
		// üretebilir; değer bazında ayrıştır).
		key := strings.Join(gk, "\x1f") + "\x00" + joinKV(attrK, attrV) + "\x00" + boundsKey(bounds)
		ps, ok := physMap[key]
		if !ok {
			ps = &physSer{gk: gk, bounds: bounds, temporality: temporality}
			physMap[key] = ps
			order = append(order, key)
		}
		ps.pts = append(ps.pts, pt{t: t, counts: counts})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if f.From.IsZero() || f.To.IsZero() {
		return []SpanMetricSeries{}, nil
	}

	fromNs := f.From.UnixNano()
	stepNs := int64(f.StepSeconds) * 1_000_000_000
	if stepNs <= 0 {
		stepNs = 1_000_000_000
	}
	nTime := int((f.To.UnixNano()-fromNs)/stepNs) + 1
	if nTime <= 0 {
		nTime = 1
	}

	type grp struct {
		gk        []string
		canonical []float64
		accum     [][]uint64 // [timeBucket][bucket]
	}
	grpMap := make(map[string]*grp)
	var grpOrder []string

	for _, k := range order {
		ps := physMap[k]
		if len(ps.bounds) == 0 {
			continue // bucket yok → katkı veremez
		}
		gkKey := strings.Join(ps.gk, "\x1f")
		g, ok := grpMap[gkKey]
		if !ok {
			g = &grp{gk: ps.gk}
			grpMap[gkKey] = g
			grpOrder = append(grpOrder, gkKey)
		}
		if g.canonical == nil {
			g.canonical = ps.bounds
			g.accum = make([][]uint64, nTime)
			for i := range g.accum {
				g.accum[i] = make([]uint64, len(g.canonical)+1)
			}
		} else if !boundsEqual(ps.bounds, g.canonical) {
			continue // farklı layout → grubun canonical'ına karıştırma (dürüst kuyruk)
		}
		nb := len(g.canonical) + 1
		raw := make([][]uint64, len(ps.pts))
		for i, p := range ps.pts {
			raw[i] = p.counts
		}
		deltas := bucketDeltas(ps.temporality, raw)
		for i, p := range ps.pts {
			tb := int((p.t - fromNs) / stepNs)
			if tb < 0 || tb >= nTime {
				continue
			}
			d := deltas[i]
			for j := 0; j < nb && j < len(d); j++ {
				g.accum[tb][j] += d[j]
			}
		}
	}

	out := make([]SpanMetricSeries, 0, len(grpOrder))
	for _, gkKey := range grpOrder {
		g := grpMap[gkKey]
		if g.canonical == nil {
			continue
		}
		pts := make([]SpanMetricPoint, 0, nTime)
		for i := 0; i < nTime; i++ {
			if bucketTotal(g.accum[i]) == 0 {
				continue // gözlem yok → gap (sahte 0 değil)
			}
			t := fromNs + int64(i)*stepNs
			pts = append(pts, SpanMetricPoint{Time: t, Value: percentileFromBuckets(g.canonical, g.accum[i], q)})
		}
		out = append(out, SpanMetricSeries{GroupKey: g.gk, Points: pts})
	}
	return out, nil
}
