package chstore

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// rollup_fastpath.go — Rollup Aşama-3 dilim 1 (v0.9.412): mevcut
// span-metric BATCH okuması (QuerySpanMetricMulti) dar rollup ailesine
// yönlenir. Hedef şekil, Overview/Service'in entry-RED batch'i:
// service + kind IN (server,consumer) filtresi yüzünden operation-MV
// fast-path'ine giremiyor ve HER seferinde ham spans taraması yapıyordu
// — dar rollup (service_name, span_kind, status_code) bu şekli 10s
// granülaritede cevaplar.
//
// Sözleşme:
//   - Uygunluk SAF ve DAR (narrowRollupEligible): yalnız dar boyutlar
//     üzerinde =/IN filtreleri, dar boyutlara groupBy, eşlenebilir
//     agg'ler (count/rate/per_min/errors/error_rate/avg/sum/p50/95/99).
//     Şüphede → ham yol (yanlış hızlıdansa doğru yavaş).
//   - Tablolar migrations ELLE uygulanana kadar YOKTUR (v0.8.185 dersi;
//     rollup_read.go başlığı) — varlık probe'u yoksa SESSİZCE ham yola
//     düşer; prod migrations-öncesi davranış bayt-bayt bugünkü.
//   - KAPSAMA dürüstlüğü: rollup MV'leri uygulandıkları andan itibaren
//     dolar; backfill (0004) opsiyoneldir. Pencere başı tablonun EN ESKİ
//     verisinden önceyse ham yol — yoksa grafik başı sahte boş kalırdı.
//   - Değer semantiği ham yolla birebir (rollupAggValue): rate=count/step,
//     error_rate=100*err/count, avg/sum/pXX ms (ns/1e6). tdigest ±%2
//     toleransı MV fast-path'lerinin de kabul ettiği sözleşme.

// narrowFilterCol — batch filtre anahtarı → dar rollup kolonu.
var narrowFilterCol = map[string]string{
	"service.name": "service_name",
	"service_name": "service_name",
	"kind":         "span_kind",
	"status":       "status_code",
	"status_code":  "status_code",
}

// narrowConjunct — tek AND koşulu: col IN values.
type narrowConjunct struct {
	col    string
	values []string
}

type narrowRollupQuery struct {
	conjuncts []narrowConjunct
	groupCols []string // dar kolon adları, f.GroupBy sırasında
}

// narrowRollupEligible — batch dar rollup'tan cevaplanabilir mi?
// Saf; rollup_fastpath_test.go ile pinli.
func narrowRollupEligible(f SpanMetricBatchFilter) (narrowRollupQuery, bool) {
	var q narrowRollupQuery
	if f.From.IsZero() || f.To.IsZero() || len(f.Aggs) == 0 {
		return q, false
	}
	for _, fe := range f.Filters {
		col, ok := narrowFilterCol[fe.Key]
		if !ok || len(fe.Values) == 0 {
			return q, false
		}
		switch fe.Op {
		case "=":
			if len(fe.Values) != 1 {
				return q, false
			}
		case "IN":
		default:
			return q, false
		}
		q.conjuncts = append(q.conjuncts, narrowConjunct{col: col, values: fe.Values})
	}
	for _, k := range f.GroupBy {
		col, ok := narrowFilterCol[k]
		if !ok {
			return q, false
		}
		q.groupCols = append(q.groupCols, col)
	}
	for _, a := range f.Aggs {
		switch strings.ToLower(a.Aggregation) {
		case "", "count", "rate", "per_min", "errors", "error_rate":
			// satır sayısı temelli — Field yok sayılır (ham yol da öyle)
		case "avg", "sum", "p50", "p95", "p99":
			if a.Field != "" && a.Field != "duration_ms" && a.Field != "duration" {
				return q, false
			}
		default:
			// p90/p999 (dar state'te yok), apdex, min/max, özel alanlar → ham yol
			return q, false
		}
	}
	return q, true
}

// pickNarrowRollupTier — step'i TAM BÖLEN ve retention'ı pencere başını
// KAPSAYAN en kaba dar kademe (en az satır). Yoksa ok=false → ham yol.
// PickRollup'tan farkı: step burada SABİT (clampSpanMetricStep verdi),
// yuvarlanamaz — kademe step'e uyar, tersi değil.
func pickNarrowRollupTier(stepSec int, from time.Time, now time.Time) (rollupTier, bool) {
	var pick rollupTier
	found := false
	oldest := now.Sub(from)
	for _, t := range narrowTiers {
		if int64(stepSec)%t.baseSec == 0 && oldest <= t.retention {
			pick, found = t, true
		}
	}
	return pick, found
}

// narrowRollupSQL — okuma SQL'i. Saf; Scan sözleşmesi SABİT:
// (bucket UInt64, gk Array(String), calls, errs, durNs UInt64,
// qs Array(Float64) [ns]). Agg eşlemesi Go'da (rollupAggValue) —
// SQL, agg listesinden bağımsız kalır.
func narrowRollupSQL(table string, stepSec int, q narrowRollupQuery, needQuantiles bool, from, to time.Time, winK int) (string, []any) {
	// v0.9.727 — kayan pencere: ham yolun (QuerySpanMetricMulti) arrayJoin
	// deseninin rollup karşılığı. Her rollup satırı k ardışık çıktı
	// bucket'ına katkı verir; quantilesTDigestMerge pencerenin TÜM
	// state'lerini birleştirir → gerçek pencereli percentile. Tarama
	// pencere kadar geriye genişler, kenarlar bucket sınırlarıyla kırpılır.
	scanFrom := from
	bucketExpr := fmt.Sprintf("toUnixTimestamp(toStartOfInterval(ts, INTERVAL %d SECOND)) * 1000000000", stepSec)
	arrayJoin := ""
	if winK > 0 {
		scanFrom = from.Add(-time.Duration(winK*stepSec) * time.Second)
		bucketExpr += " + _shift"
		arrayJoin = fmt.Sprintf(
			"ARRAY JOIN arrayMap(x -> toUInt64(x * %d) * 1000000000, range(%d)) AS _shift",
			stepSec, winK)
	}
	where := []string{"ts >= ?", "ts <= ?"}
	args := []any{scanFrom, to}
	if winK > 0 {
		stepNs := int64(stepSec) * 1e9
		where = append(where,
			fmt.Sprintf("bucket >= %d", from.UnixNano()/stepNs*stepNs),
			fmt.Sprintf("bucket <= %d", to.UnixNano()))
	}
	for _, c := range q.conjuncts {
		if len(c.values) == 1 {
			where = append(where, c.col+" = ?")
			args = append(args, c.values[0])
			continue
		}
		ph := strings.TrimSuffix(strings.Repeat("?, ", len(c.values)), ", ")
		where = append(where, c.col+" IN ("+ph+")")
		for _, v := range c.values {
			args = append(args, v)
		}
	}

	gk := "[]::Array(String)"
	if len(q.groupCols) > 0 {
		parts := make([]string, len(q.groupCols))
		for i, col := range q.groupCols {
			parts[i] = "toString(" + col + ")"
		}
		gk = "[" + strings.Join(parts, ", ") + "]"
	}

	qs := "CAST([], 'Array(Float64)') AS qs"
	if needQuantiles {
		qs = "quantilesTDigestMerge(0.5, 0.95, 0.99)(q_state) AS qs"
	}

	sql := fmt.Sprintf(`
		SELECT
			%s AS bucket,
			%s AS gk,
			sum(span_count)   AS calls,
			sum(error_count)  AS errs,
			sum(duration_sum) AS dur_ns,
			%s
		FROM %s
		%s
		WHERE %s
		GROUP BY bucket, gk
		ORDER BY gk, bucket
		LIMIT 50000
		SETTINGS max_execution_time = 15`,
		bucketExpr, gk, qs, table, arrayJoin, strings.Join(where, " AND "))
	return sql, args
}

// rollupAggValue — bir rollup satırından bir agg'in değeri; ham yolun
// aggToSQL semantiğiyle birebir (rate=count/step, error_rate yüzde,
// süreler ms). qs boşsa percentile 0 döner (needQuantiles garantisiyle
// pratikte olmaz).
func rollupAggValue(agg string, calls, errs, durNs uint64, qs []float64, stepSec int) float64 {
	switch strings.ToLower(agg) {
	case "", "count":
		return float64(calls)
	case "rate":
		return float64(calls) / float64(stepSec)
	case "per_min":
		return float64(calls) / float64(stepSec) * 60.0
	case "errors":
		return float64(errs)
	case "error_rate":
		if calls == 0 {
			return 0
		}
		return 100.0 * float64(errs) / float64(calls)
	case "avg":
		if calls == 0 {
			return 0
		}
		return float64(durNs) / float64(calls) / 1e6
	case "sum":
		return float64(durNs) / 1e6
	case "p50":
		return quantileAt(qs, 0)
	case "p95":
		return quantileAt(qs, 1)
	case "p99":
		return quantileAt(qs, 2)
	}
	return 0
}

func quantileAt(qs []float64, i int) float64 {
	if i >= len(qs) {
		return 0
	}
	return qs[i] / 1e6 // ns → ms
}

func needsQuantiles(aggs []SpanMetricAggSpec) bool {
	for _, a := range aggs {
		switch strings.ToLower(a.Aggregation) {
		case "p50", "p95", "p99":
			return true
		}
	}
	return false
}

// rollupEarliest — tablo başına en eski ts önbelleği (5 dk). Kapsama
// dürüstlüğü: MV'ler uygulandıkları andan itibaren dolar; pencere başı
// earliest'ten önceyse ham yol (backfill 0004 koşulduysa earliest
// kendiliğinden geriye gider).
var rollupEarliest = struct {
	mu      sync.Mutex
	ts      map[string]time.Time
	checked map[string]time.Time
}{ts: map[string]time.Time{}, checked: map[string]time.Time{}}

func (s *Store) rollupEarliestTS(ctx context.Context, table string) (time.Time, bool) {
	rollupEarliest.mu.Lock()
	if t, ok := rollupEarliest.checked[table]; ok && time.Since(t) < 5*time.Minute {
		e, has := rollupEarliest.ts[table]
		rollupEarliest.mu.Unlock()
		return e, has
	}
	rollupEarliest.mu.Unlock()

	var n uint64
	var minTS time.Time
	row := s.conn.QueryRow(ctx,
		fmt.Sprintf(`SELECT count(), min(ts) FROM %s SETTINGS max_execution_time = 5`, table))
	if err := row.Scan(&n, &minTS); err != nil {
		return time.Time{}, false
	}
	rollupEarliest.mu.Lock()
	rollupEarliest.checked[table] = time.Now()
	if n > 0 {
		rollupEarliest.ts[table] = minTS
	} else {
		delete(rollupEarliest.ts, table)
	}
	rollupEarliest.mu.Unlock()
	return minTS, n > 0
}

// tryNarrowRollupFastPathMulti — QuerySpanMetricMulti'nin dar rollup
// denemesi. ok=false → çağıran ham yola OLDUĞU GİBİ devam eder (MV
// fast-path'lerle aynı sözleşme; hata sayfayı asla boşaltmaz).
func (s *Store) tryNarrowRollupFastPathMulti(ctx context.Context, f SpanMetricBatchFilter, effWinSec, winK int) (map[string][]SpanMetricSeries, bool) {
	q, ok := narrowRollupEligible(f)
	if !ok {
		return nil, false
	}
	tier, ok := pickNarrowRollupTier(f.StepSeconds, f.From, time.Now())
	if !ok {
		return nil, false
	}
	if exists, err := s.rollupTableExists(ctx, tier.table); err != nil || !exists {
		return nil, false
	}
	earliest, has := s.rollupEarliestTS(ctx, tier.table)
	// Pencere açıkken kapsama GENİŞLETİLMİŞ taramaya göre denetlenir:
	// pencere from-W'ye geriye bakar; rollup orayı kapsamıyorsa ham yol
	// (ilk W'nin rampalanması yerine dürüst tam-pencere).
	coverFrom := f.From
	if winK > 0 {
		coverFrom = coverFrom.Add(-time.Duration(effWinSec) * time.Second)
	}
	if !has || earliest.After(coverFrom) {
		return nil, false
	}

	sql, args := narrowRollupSQL(tier.table, f.StepSeconds, q, needsQuantiles(f.Aggs), f.From, f.To, winK)
	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		log.Printf("[rollup] narrow fast-path %s düştü, ham yol: %v", tier.table, err)
		return nil, false
	}
	defer rows.Close()

	type acc struct {
		seriesMap map[string]*SpanMetricSeries
		order     []string
	}
	accs := make([]acc, len(f.Aggs))
	for i := range accs {
		accs[i].seriesMap = map[string]*SpanMetricSeries{}
	}
	for rows.Next() {
		var bucket, calls, errs, durNs uint64
		var gk []string
		var qsArr []float64
		if err := rows.Scan(&bucket, &gk, &calls, &errs, &durNs, &qsArr); err != nil {
			log.Printf("[rollup] narrow fast-path scan düştü, ham yol: %v", err)
			return nil, false
		}
		key := strings.Join(gk, "|")
		for i, a := range f.Aggs {
			ser, ok := accs[i].seriesMap[key]
			if !ok {
				ser = &SpanMetricSeries{GroupKey: append([]string{}, gk...)}
				accs[i].seriesMap[key] = ser
				accs[i].order = append(accs[i].order, key)
			}
			rateDiv := f.StepSeconds
			if winK > 0 {
				rateDiv = effWinSec
			}
			ser.Points = append(ser.Points, SpanMetricPoint{
				Time:  int64(bucket),
				Value: rollupAggValue(a.Aggregation, calls, errs, durNs, qsArr, rateDiv),
			})
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("[rollup] narrow fast-path rows düştü, ham yol: %v", err)
		return nil, false
	}

	out := make(map[string][]SpanMetricSeries, len(f.Aggs))
	for i, a := range f.Aggs {
		list := make([]SpanMetricSeries, 0, len(accs[i].order))
		for _, k := range accs[i].order {
			list = append(list, *accs[i].seriesMap[k])
		}
		// Ham yolla aynı doktrin (v0.9.723): pencere açıkken sayım-sınıfı
		// seriler sıfır-doldurulur; oran/gecikme boşluk bırakır.
		if winK > 0 && spanAggZeroFills(a.Aggregation) {
			list = zeroFillSpanSeries(list, f.StepSeconds, f.From.UnixNano(), f.To.UnixNano())
		}
		out[a.Name] = list
	}
	return out, true
}
