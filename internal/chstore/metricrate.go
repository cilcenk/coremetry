package chstore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// metricrate.go (F2, v0.9.106 — PromQL rate/increase parity) — OTLP kümülatif
// counter'lardan (instrument='sum', temporality='cumulative') reset-korumalı
// per-saniye hız (rate) ve pencere-artışı (increase). Roadmap F2
// (scratchpad/metrics-chart-parity-roadmap.md).
//
// TASARIM (Go-side delta): SQL yalnız per-(SERİ, bucket) SON kümülatif değeri
// (argMax(value,time)) getirir; delta + reset-telafisi + kullanıcı-groupBy'a
// yeniden-toplama Go'da yapılır. Neden Go-side, SQL window-fn değil:
//   - ClickHouse runningDifference()/neighbor() her DATA-BLOK sınırında state
//     sıfırlar → çok-bloklu seride yanlış delta (issues #6353/#10334). Go-side
//     bu sınıfı TAMAMEN atlar (lagInFrame OVER PARTITION doğru olurdu ama
//     Go-side hem daha test-edilebilir hem fingerprint=0 fallback'i temiz).
//   - SERİ kimliği series_fingerprint (xxhash64, otlp). Prod-distributed'da
//     cluster_name unset ise kolon DEFAULT 0 kalır (store.go:2029) → per-satır
//     COALESCE ile (fingerprint!=0 ? fp : synthetic attr-hash) fallback; ASLA
//     sessizce tüm serileri tek partition'a çökertip yanlış delta üretme.
//   - rate PER SERİ hesaplanır, SONRA kullanıcı-groupBy'a toplanır (PromQL
//     `sum(rate(counter)) by(label)` semantiği).
//
// VictoriaMetrics gerekçesiyle EXTRAPOLASYON ATLANIR: bucket'lar zaten delta;
// yavaş integer counter'da extrapole kesirli/yanıltıcı sonuç verir.

// resetSafeDelta — Prometheus reset-korumalı per-interval artış: cur < prev
// (counter restart) ise post-reset değeri (cur) artış say; aksi halde cur-prev.
// Telescoping ile toplam = (last-first) + Σ(reset'lerdeki düzeltme) — Prometheus
// counterCorrection'la aynı. Tek-örnekli reset atfı doğru (prev_max+cur ÇİFT
// sayardı).
func resetSafeDelta(prev, cur float64) float64 {
	if cur < prev {
		return cur
	}
	return cur - prev
}

type ratePoint struct {
	bucket uint64 // unix ns
	value  float64
}

// seriesRatePoints — TEK serinin (buckets ns-artan, vals kümülatif; nil=gap)
// per-bucket rate/increase'ini üretir. Review-fix'leri (v0.9.106):
//   - GERÇEK dt: delta / (bucket_i - prevBucket_i) saniye — sabit step'e
//     bölmek gap'te (eksik bucket) rate'i FAZLA gösteriyordu (over-division
//     spike). mode="rate" → delta/dt_sn; "increase" → ham delta.
//   - SEED lookback: ilk DOLU örnek prev'i primer ama emit EDİLMEZ. dropBefore
//     (< orijinal From) seed bölgesi — pencere-öncesi bir bucket çekilip prev
//     primer, böylece pencere-içi İLK bucket gerçek delta alır (sol-kenar
//     sahte-sıfır kalkar, PromQL lookback semantiği). Seed yoksa (gerçekten
//     yeni seri) ilk pencere-içi bucket baseline'dır — doğru.
func seriesRatePoints(buckets []uint64, vals []*float64, mode string, dropBeforeNs uint64) []ratePoint {
	var out []ratePoint
	havePrev := false
	var prevV float64
	var prevB uint64
	for i := range buckets {
		if vals[i] == nil {
			continue
		}
		cur := *vals[i]
		curB := buckets[i]
		if !havePrev {
			prevV, prevB, havePrev = cur, curB, true
			continue // baseline: primer, emit yok
		}
		delta := resetSafeDelta(prevV, cur)
		dtSec := float64(curB-prevB) / 1e9
		prevV, prevB = cur, curB
		if curB < dropBeforeNs {
			continue // seed bölgesi — primer ama emit yok
		}
		val := delta
		if mode == "rate" && dtSec > 0 {
			val = delta / dtSec
		}
		out = append(out, ratePoint{bucket: curB, value: val})
	}
	return out
}

// isRateableInstrument — rate/increase yalnız MONOTONIC COUNTER'da (sum) anlamlı.
// gauge (anlık) / histogram (dağılım) reddedilir.
func isRateableInstrument(instrument string) bool {
	return instrument == "sum"
}

// metricSeriesKeyExpr — per-seri PARTITION anahtarı SQL ifadesi. series_
// fingerprint (xxhash64) birincil; 0 ise (prod-distributed cluster_name unset,
// store.go:2029, ya da eski data) sıralı attr-set + service üzerinden
// cityHash64 synthetic'e DÜŞER — asla tüm serileri tek anahtara çökertme.
// hasFp=false (kolon şardlara hiç ulaşmamış) → doğrudan synthetic.
func metricSeriesKeyExpr(hasFp bool) string {
	// SeriesFingerprint (otlp/fingerprint.go:51) kimliği: metric + sorted
	// (dpAttrs) + service.instance.id + service.name. Synthetic fallback AYNI
	// kimliği kurmalı — service.instance.id (res_values) ATLANIRSA farklı
	// pod'lar (aynı dpAttr + service.name, farklı instance) tek sk'ya çöker →
	// pod'lar arası bağımsız-reset'li kümülatif counter'lar karışıp YANLIŞ
	// delta üretir. metric zaten WHERE'de sabit; service.instance.id +
	// service.name + sıralı dpAttr yeterli.
	synthetic := `cityHash64(concat(` +
		`service_name, '||', ` +
		`res_values[indexOf(res_keys, 'service.instance.id')], '||', ` +
		`arrayStringConcat(arraySort(arrayMap((k, v) -> concat(k, '=', v), attr_keys, attr_values)), ',')))`
	if hasFp {
		return `toString(if(series_fingerprint != 0, series_fingerprint, ` + synthetic + `))`
	}
	return `toString(` + synthetic + `)`
}

// metricTemporality — metriğin OTLP aggregation temporality'sini probe'lar
// ('cumulative' | 'delta' | ''). Boş/bilinmeyen → çağıran cumulative sayar
// (OTLP default). Bounded (max_execution_time=3, time-pruned).
func (s *Store) metricTemporality(ctx context.Context, name, service string) string {
	to := time.Now()
	from := to.Add(-metricIvProbeWindow)
	q := `SELECT any(temporality) FROM metric_points WHERE metric = ? AND time >= ? AND time <= ?`
	args := []any{name, from, to}
	if service != "" {
		q += ` AND service_name = ?`
		args = append(args, service)
	}
	q += ` SETTINGS max_execution_time = 3`
	var temp string
	if err := s.conn.QueryRow(ctx, q, args...).Scan(&temp); err != nil {
		return ""
	}
	return temp
}

// QueryMetricRate — PromQL rate()/increase() muadili (F2). mode: "rate"
// (per-saniye) | "increase" (pencere-artışı, ham delta toplamı). Yalnız
// counter (instrument='sum'); gauge/histogram boş döner. Cumulative
// temporality → per-seri reset-korumalı cross-bucket delta; delta temporality
// → per-bucket sum (değer zaten interval-artışı). Aynı step disiplini
// (metricAutoStepPx + clampStepToExport). Sonuç SpanMetricSeries — UI aynı.
func (s *Store) QueryMetricRate(ctx context.Context, f MetricQueryFilter, mode string) ([]SpanMetricSeries, error) {
	if f.Name == "" {
		return nil, fmt.Errorf("metric name required")
	}
	if mode != "rate" && mode != "increase" {
		return nil, fmt.Errorf("unknown rate mode %q", mode)
	}

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
	step := f.StepSeconds
	if step <= 0 {
		step = 60
	}

	// Delta temporality: değer zaten per-interval artışı → per-bucket sum;
	// cumulative (OTLP default; probe boşsa varsay) → per-seri cross-bucket delta.
	isDelta := s.metricTemporality(ctx, f.Name, f.Service) == "delta"

	// Cumulative: pencere-öncesi bir SEED bucket (From-step) çek ki pencere-içi
	// İLK bucket gerçek delta alsın (sol-kenar sahte-sıfır kalkar, PromQL
	// lookback). Delta yolu seed'e ihtiyaç duymaz. originalFromNs seed'i emit
	// dışı tutar.
	lowerBound := f.From
	originalFromNs := uint64(f.From.UnixNano())
	if !isDelta {
		lowerBound = f.From.Add(-time.Duration(step) * time.Second)
	}

	// WHERE: metric + service + window + instrument='sum' (counter) + filtreler.
	var wc whereClause
	wc.add("metric = ?", f.Name)
	if f.Service != "" {
		wc.add("service_name = ?", f.Service)
	}
	wc.add("time >= ?", lowerBound)
	wc.add("time <= ?", f.To)
	wc.add("instrument = 'sum'")
	// v0.9.106 (F2 review-fix #3) — yalnız MONOTONIC counter; UpDownCounter
	// (is_monotonic=0: active_requests/queue-depth) rate'te her düşüşü "reset"
	// sanıp garbage üretiyordu → filtrele (boş döner, yanlış değil). Kolon
	// yoksa (external-distributed cluster_name unset) guard'sız best-effort.
	if s.hasIsMonotonicCol {
		wc.add("is_monotonic = 1")
	}
	ApplyMetricFilters(&wc, f.Filters)

	// Kullanıcı groupBy ifadesi (yeniden-toplama anahtarı).
	groupSelect := "[]::Array(String)"
	if len(f.GroupBy) > 0 {
		parts := make([]string, len(f.GroupBy))
		var groupArgs []any
		for i, k := range f.GroupBy {
			expr, args := groupKeyExpr(k, true)
			parts[i] = expr
			groupArgs = append(groupArgs, args...)
		}
		groupSelect = "[" + strings.Join(parts, ", ") + "]"
		wc.args = append(groupArgs, wc.args...)
	}

	if isDelta {
		return s.queryRateDelta(ctx, wc, groupSelect, step, mode)
	}
	return s.queryRateCumulative(ctx, wc, groupSelect, step, mode, originalFromNs)
}

// queryRateCumulative — per-(seri, bucket) SON kümülatif değeri çeker; Go'da
// per-seri reset-korumalı delta (scalarSeriesDelta) + kullanıcı-groupBy'a
// yeniden-toplama. CH bounds korunur (LIMIT + max_execution_time + time WHERE).
func (s *Store) queryRateCumulative(ctx context.Context, wc whereClause, groupSelect string, step int, mode string, originalFromNs uint64) ([]SpanMetricSeries, error) {
	sql := fmt.Sprintf(`
		SELECT
		    toUnixTimestamp(toStartOfInterval(time, INTERVAL %d SECOND)) * 1000000000 AS bucket,
		    %s AS sk,
		    %s AS gk,
		    argMaxOrNull(value, time) AS v
		FROM metric_points
		%s
		GROUP BY bucket, sk, gk
		ORDER BY sk, bucket
		LIMIT 50000
		SETTINGS max_execution_time = 30`, step, metricSeriesKeyExpr(s.hasSeriesFpCol), groupSelect, wc.sql())

	rows, err := s.conn.Query(ctx, sql, wc.args...)
	if err != nil {
		return nil, fmt.Errorf("rate query: %w", err)
	}
	defer rows.Close()

	// Per seri (sk): zaman-sıralı (bucket, cumulative-value) + gk. ORDER BY
	// sk,bucket olduğundan her sk'nin satırları zaten sıralı gelir.
	type skSeries struct {
		gk      []string
		buckets []uint64
		vals    []*float64
	}
	bySk := map[string]*skSeries{}
	var skOrder []string
	for rows.Next() {
		var bucket uint64
		var sk string
		var gk []string
		var v *float64
		if err := rows.Scan(&bucket, &sk, &gk, &v); err != nil {
			return nil, err
		}
		ss := bySk[sk]
		if ss == nil {
			ss = &skSeries{gk: gk}
			bySk[sk] = ss
			skOrder = append(skOrder, sk)
		}
		ss.buckets = append(ss.buckets, bucket)
		ss.vals = append(ss.vals, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Per-seri rate/increase (gerçek dt + seed lookback) → kullanıcı-groupBy'a
	// bucket bazında topla. rate ZATEN per-seri /dt yapıldı (sum(rate) =
	// Σ delta_i/dt_i, dt seri-başı değişebilir; delta'ları toplayıp step'e
	// bölmek YANLIŞ olurdu — review-fix #2).
	byGk := map[string]map[uint64]float64{}
	gkKeys := map[string][]string{}
	var gkOrder []string
	for _, sk := range skOrder {
		ss := bySk[sk]
		pts := seriesRatePoints(ss.buckets, ss.vals, mode, originalFromNs)
		gkKey := strings.Join(ss.gk, "\x00")
		acc := byGk[gkKey]
		if acc == nil {
			acc = map[uint64]float64{}
			byGk[gkKey] = acc
			gkKeys[gkKey] = ss.gk
			gkOrder = append(gkOrder, gkKey)
		}
		for _, rp := range pts {
			acc[rp.bucket] += rp.value
		}
	}

	return buildRateSeries(byGk, gkKeys, gkOrder, 1), nil
}

// queryRateDelta — delta-temporality counter: değer zaten per-interval artışı,
// per-(gk, bucket) sumOrNull yeter (cross-bucket delta YOK).
func (s *Store) queryRateDelta(ctx context.Context, wc whereClause, groupSelect string, step int, mode string) ([]SpanMetricSeries, error) {
	sql := fmt.Sprintf(`
		SELECT
		    toUnixTimestamp(toStartOfInterval(time, INTERVAL %d SECOND)) * 1000000000 AS bucket,
		    %s AS gk,
		    sumOrNull(value) AS v
		FROM metric_points
		%s
		GROUP BY bucket, gk
		ORDER BY gk, bucket
		LIMIT 50000
		SETTINGS max_execution_time = 30`, step, groupSelect, wc.sql())

	rows, err := s.conn.Query(ctx, sql, wc.args...)
	if err != nil {
		return nil, fmt.Errorf("rate(delta) query: %w", err)
	}
	defer rows.Close()

	byGk := map[string]map[uint64]float64{}
	gkKeys := map[string][]string{}
	var gkOrder []string
	for rows.Next() {
		var bucket uint64
		var gk []string
		var v *float64
		if err := rows.Scan(&bucket, &gk, &v); err != nil {
			return nil, err
		}
		if v == nil {
			continue
		}
		gkKey := strings.Join(gk, "\x00")
		acc := byGk[gkKey]
		if acc == nil {
			acc = map[uint64]float64{}
			byGk[gkKey] = acc
			gkKeys[gkKey] = gk
			gkOrder = append(gkOrder, gkKey)
		}
		acc[bucket] += *v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Delta counter: değer zaten bucket'ın per-interval artışı. rate → /step
	// (delta bucket'ları düzenli, gap-over-division sorunu YOK — review notu);
	// increase → ham. Cumulative yol /dt'yi seriesRatePoints'te zaten yaptı.
	divBy := 1.0
	if mode == "rate" {
		divBy = float64(step)
	}
	return buildRateSeries(byGk, gkKeys, gkOrder, divBy), nil
}

// buildRateSeries — bucket-bazlı (ZATEN nihai) değerleri SpanMetricSeries'e
// çevirir; divBy ile böler (cumulative=1 çünkü /dt seriesRatePoints'te
// yapıldı; delta-rate=step; delta-increase=1).
func buildRateSeries(byGk map[string]map[uint64]float64, gkKeys map[string][]string, gkOrder []string, divBy float64) []SpanMetricSeries {
	if divBy == 0 {
		divBy = 1
	}
	out := make([]SpanMetricSeries, 0, len(gkOrder))
	for _, gkKey := range gkOrder {
		acc := byGk[gkKey]
		buckets := make([]uint64, 0, len(acc))
		for b := range acc {
			buckets = append(buckets, b)
		}
		sort.Slice(buckets, func(i, j int) bool { return buckets[i] < buckets[j] })
		pts := make([]SpanMetricPoint, 0, len(buckets))
		for _, b := range buckets {
			pts = append(pts, SpanMetricPoint{Time: int64(b), Value: acc[b] / divBy})
		}
		out = append(out, SpanMetricSeries{GroupKey: gkKeys[gkKey], Points: pts})
	}
	return out
}
