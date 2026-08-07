package chstore

import (
	"context"
	"fmt"
	"time"
)

// metric_rollup_hist_read.go — v0.9.751: histogram yüzdelikleri rollup'tan.
//
// 0003 tasarımı bunu baştan mümkün kıldı: bucket_bounds GROUP BY
// anahtarında, hist_counts (sumForEach) yalnız AYNI bounds içinde
// birleşir. Okuyucu politikası HAM YOLLA BİREBİR (metrichist.go):
// kanonik bounds = ilk görülen; farklı bounds-set'ler ATLANIR, yanlış
// kovaya toplanmaz. Uygunluk dar ve dürüst: delta histogram + filtresiz/
// kırılımsız servis-geneli okuma (749'un panel şekli) — cumulative
// histogram sumForEach'te anlamsızlaşır (kümülatif sayaç sınıfı), HAM.
// Tablolar yoksa / pencereyi kapsamıyorsa SESSİZCE ham yol (fail-open,
// family-C ile aynı sözleşme).

// metricRollupHistPlan — SAF uygunluk (tablo-testli).
func metricRollupHistPlan(f MetricQueryFilter, instrument, temporality string, now time.Time) (metricRollupTier, bool) {
	if f.Name == "" || f.Service == "" {
		return metricRollupTier{}, false
	}
	if len(f.Filters) > 0 || len(f.GroupBy) > 0 || f.Instance != "" || f.Engine != "" {
		return metricRollupTier{}, false
	}
	if !isHistogramInstrument(instrument) || temporality != "delta" {
		return metricRollupTier{}, false
	}
	step := int64(f.StepSeconds)
	if step <= 0 {
		return metricRollupTier{}, false
	}
	oldest := now.Sub(f.From)
	for _, t := range metricRollupTiers {
		if step%t.grainSec == 0 && oldest <= t.ttl {
			return t, true
		}
	}
	return metricRollupTier{}, false
}

// buildRollupHistSQL — SAF (pinli): bucket + bounds + birleşik sayımlar.
func buildRollupHistSQL(table string, stepSec int) string {
	return fmt.Sprintf(`
		SELECT
			toUnixTimestamp(toStartOfInterval(ts, INTERVAL %d SECOND)) * 1000000000 AS bucket,
			bucket_bounds,
			sumForEachMerge(hist_counts) AS counts
		FROM %s
		WHERE metric = ? AND service_name = ? AND ts >= ? AND ts <= ?
		GROUP BY bucket, bucket_bounds
		ORDER BY bucket
		LIMIT 50000
		SETTINGS max_execution_time = 15`, stepSec, table)
}

type histRollupRow struct {
	Bucket int64
	Bounds []float64
	Counts []uint64
}

// foldRollupHist — SAF: satırlar → tek seri. Ham yol politikası:
// kanonik bounds = İLK boş-olmayan; farklı bounds atlanır (skipped
// sayılır); toplam-0 bucket nokta ÜRETMEZ (gap — PromQL semantiği).
func foldRollupHist(rows []histRollupRow, q float64) (pts []SpanMetricPoint, skipped int) {
	// v0.9.754 — ham yolla aynı politika güncellemesi: kanonik = BASKIN
	// bounds (ağırlık = satırın toplam gözlemi); ilk-görülen politikası
	// azınlık-önce sıralamada paneli boşaltıyordu.
	bw := make([]boundsWeight, 0, len(rows))
	for _, r := range rows {
		bw = append(bw, boundsWeight{Bounds: r.Bounds, Weight: int(bucketTotal(r.Counts))})
	}
	canonical := dominantBounds(bw)
	if canonical == nil {
		return nil, 0
	}
	for _, r := range rows {
		if !boundsEqual(r.Bounds, canonical) {
			skipped++
			continue
		}
		if bucketTotal(r.Counts) == 0 {
			continue
		}
		pts = append(pts, SpanMetricPoint{Time: r.Bucket, Value: percentileFromBuckets(canonical, r.Counts, q)})
	}
	return pts, skipped
}

// tryRollupHistogramQuantile — tekil sarmalayıcı: çoklunun [q] hâli.
func (s *Store) tryRollupHistogramQuantile(ctx context.Context, f MetricQueryFilter, q float64) ([]SpanMetricSeries, bool) {
	out, ok := s.tryRollupHistogramQuantiles(ctx, f, []float64{q})
	if !ok {
		return nil, false
	}
	return out[0], true
}

// tryRollupHistogramQuantiles — ok=false → çağıran ham yola aynen devam.
//
// v0.9.768: SQL + satır çekimi TEK; satırlar belleğe alınıp her q için aynı
// küme üstünde foldRollupHist ile katlanır (fold salt-okur, satırları
// mutasyona uğratmaz — bkz. TestFoldRollupHistMultiQNoMutation). Uygunluk
// probları (instrument/temporality/kapsam) da bir kez koşar.
func (s *Store) tryRollupHistogramQuantiles(ctx context.Context, f MetricQueryFilter, qs []float64) ([][]SpanMetricSeries, bool) {
	if len(qs) == 0 {
		return nil, false
	}
	inst := s.metricInstrument(ctx, f.Name, f.Service, f.From, f.To)
	if !isHistogramInstrument(inst) {
		return nil, false
	}
	temp := s.metricTemporalityFiltered(ctx, f.Name, f.Service, nil)
	tier, ok := metricRollupHistPlan(f, inst, temp, time.Now())
	if !ok {
		return nil, false
	}
	floor, has := s.rollupCoverageFloor(ctx, tier.table)
	if !has || floor.After(f.From) {
		return nil, false
	}
	rows, err := s.telemetryReadConn().Query(ctx,
		buildRollupHistSQL(tier.table, f.StepSeconds), f.Name, f.Service, f.From, f.To)
	if err != nil {
		return nil, false // fail-open ham yola; hata sayfayı boşaltmaz
	}
	defer rows.Close()
	var hrs []histRollupRow
	for rows.Next() {
		var r histRollupRow
		var b uint64
		if err := rows.Scan(&b, &r.Bounds, &r.Counts); err != nil {
			return nil, false
		}
		r.Bucket = int64(b)
		hrs = append(hrs, r)
	}
	if rows.Err() != nil || len(hrs) == 0 {
		return nil, false
	}
	out := make([][]SpanMetricSeries, len(qs))
	for i, q := range qs {
		pts, _ := foldRollupHist(hrs, q)
		// Boş katlama → HEPSİ ham yola (fail-open, tekil davranışla
		// birebir). Kova/bounds elemesi q'dan bağımsız olduğu için
		// pratikte ya hepsi dolu ya hepsi boş.
		if len(pts) == 0 {
			return nil, false
		}
		out[i] = []SpanMetricSeries{{GroupKey: nil, Points: pts}}
	}
	return out, true
}
