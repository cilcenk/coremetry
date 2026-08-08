package chstore

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// MetricQueryFilter is a Grafana-style query against metric_points.
// Same shape as SpanMetricFilter but targets the metrics table.
type MetricQueryFilter struct {
	Name    string // metric name (required)
	Service string // shortcut filter on service_name
	// Instance + Engine — v0.9.279, DB receiver drill scoping. service_name
	// cannot do this job: it names the RECEIVER, so every instance of an
	// engine shares one value. See dbInstanceScopeClause for why it takes an
	// engine and why the predicate is an OR.
	Instance    string
	Engine      string
	Filters     []FilterExpr // arbitrary attribute filters (resource.X / span.X also supported)
	GroupBy     []string     // 0..N attribute keys → multi-line series
	Aggregation string       // avg | sum | min | max | last | p50 | p95 | p99 (default: avg)
	From, To    time.Time
	StepSeconds int
	// MaxDataPoints (F1, v0.9.105) — panel pixel width ≈ target bucket count.
	// When > 0 and StepSeconds is auto (≤0), the step is pixel-adaptive
	// (rangeSec/maxDataPoints, snapped) instead of the fixed span ladder, so
	// wide windows expose the sub-bucket resolution OTLP metrics carry. 0 =
	// px unknown → fixed ladder. clampStepToExport still caps the LOWER bound.
	MaxDataPoints int
	// RateWindowSec (v0.9.718, parite) — PromQL rate([W]) eşdeğeri kayan
	// pencere: her bucket'ta değer, (t-W, t] aralığındaki artışın W'ye
	// bölümü. 0/`<=step` → eski davranış (pencere = tek bucket).
	// Operatörün Grafana referansı [3m]@1s — pencere OLMADAN aynı veri
	// testere dişi görünüyor (kesikli şikâyeti).
	RateWindowSec int
}

// buildMetricQuerySQL builds the metric_points query SQL + bound args for a
// MetricQueryFilter. Extracted as a pure function (v0.8.4) so the CH-bounds
// guards are unit-testable without a live ClickHouse — see metricquery_test.go.
//
// Scale-hardening (v0.8.4, scale-audit): the metric_points GROUP BY scan
// behind the Grafana-style MetricQueryEditor MUST satisfy the CLAUDE.md
// CH-bounds constraint — LIMIT (had it), a time-bounded WHERE that prunes
// partitions (was CONDITIONAL — absent when from/to were zero), and
// SETTINGS max_execution_time (was MISSING, unlike the QueryMetricHistogram
// twin). A degenerate call with no window scanned every partition unbounded.
// Now the window always defaults to the last 24h and max_execution_time caps
// wall-clock at 30s, mirroring metrichist.go.
//
// v0.9.776 — instrument/temporality parametreleri: agg=avg histogramda
// `avgOrNull(value)` ile YANLIŞ hesaplanıyordu (bkz. metricAvgExpr).
// Ayrı argüman, MetricQueryFilter alanı DEĞİL: değerler probe'la
// ÖĞRENİLİR (QueryMetric), çağıranın verdiği bir filtre girdisi değil —
// aynı kalıp metricRollupPlan(f, instrument, temporality, now)'da da var.
// Boş/boş çağrı (testler, ham çağıranlar) eski SQL'i BAYT-BAYT üretir.
//
// v0.9.797 — `ex` (route dışlama kuralları) AYNI gerekçeyle dördüncü
// argüman, MetricQueryFilter alanı DEĞİL: bu bir SUNUCU POLİTİKASI
// (system_settings), çağıranın verdiği bir filtre girdisi değil. nil /
// kuralsız set → SQL bayt-bayt eski (metric_exclusions_test.go pini).
func buildMetricQuerySQL(f MetricQueryFilter, now time.Time, instrument, temporality string, ex *CompiledMetricExclusions) (string, []any, error) {
	// Always time-bound: default the window so `time >= ?` / `time <= ?`
	// below can prune partitions even on a from/to-less call.
	if f.To.IsZero() {
		f.To = now
	}
	if f.From.IsZero() {
		f.From = f.To.Add(-24 * time.Hour)
	}

	var wc whereClause
	wc.add("metric = ?", f.Name)
	if f.Service != "" {
		wc.add("service_name = ?", f.Service)
	}
	if clause, n := dbInstanceScopeClause(f.Engine, f.Instance); clause != "" {
		args := make([]any, n)
		for i := range args {
			args[i] = f.Instance
		}
		wc.add(clause, args...)
	}
	wc.add("time >= ?", f.From)
	wc.add("time <= ?", f.To)
	// v0.8.381 — metric_points lacks most spans-typed columns; the
	// metric-aware variant reroutes those keys to array lookups
	// (deployment.environment picked in the Explore filter 500'd).
	ApplyMetricFilters(&wc, f.Filters)
	// v0.9.797 — operatörün route dışlamaları. KONUM ÖNEMLİ: groupBy
	// argümanlarının başa eklenmesinden (aşağıda) ÖNCE, çünkü o prepend
	// WHERE argümanlarının kendi içindeki sırasını korumak zorunda.
	// Grupsuz sorgular da buradan geçer — "Toplam" çizgisinin temizliği
	// buna bağlı.
	applyMetricExclusionWhere(&wc, ex, f.Name)

	step := f.StepSeconds
	if step <= 0 {
		// Fallback for direct/test callers that didn't pre-resolve the step
		// (QueryMetric sets f.StepSeconds first). v0.9.105 (F1) — pixel-
		// adaptive when MaxDataPoints>0, else the fixed span ladder (which
		// still exposes sub-10s resolution for short windows, v0.5.259).
		step = metricAutoStepPx(f.From, f.To, f.MaxDataPoints)
	}

	aggExpr, err := metricAggToSQL(f.Aggregation, instrument, temporality)
	if err != nil {
		return "", nil, err
	}

	groupSelect := "[]::Array(String)"
	if len(f.GroupBy) > 0 {
		parts := make([]string, len(f.GroupBy))
		var groupArgs []any
		for i, k := range f.GroupBy {
			// metric_points path: op_group isn't a metric dimension, so pass
			// true to keep the expression byte-identical to pre-v0.8.187.
			expr, args := groupKeyExprMetric(k)
			parts[i] = expr
			groupArgs = append(groupArgs, args...)
		}
		groupSelect = "[" + strings.Join(parts, ", ") + "]"
		wc.args = append(groupArgs, wc.args...)
	}

	sql := fmt.Sprintf(`
		SELECT
		    toUnixTimestamp(toStartOfInterval(time, INTERVAL %d SECOND)) * 1000000000 AS bucket,
		    %s AS gk,
		    %s AS v
		FROM metric_points
		%s
		GROUP BY bucket, gk
		ORDER BY gk, bucket
		LIMIT %d
		SETTINGS max_execution_time = 25`, step, groupSelect, aggExpr, wc.sql(), SpanMetricRowCap)
	return sql, wc.args, nil
}

// QueryMetric runs a multi-series time-bucketed query against metric_points.
// Returns the same SpanMetricSeries shape so the UI can reuse MultiLineChart.
func (s *Store) QueryMetric(ctx context.Context, f MetricQueryFilter) ([]SpanMetricSeries, error) {
	if f.Name == "" {
		return nil, fmt.Errorf("metric name required")
	}
	// v0.9.106 (F2) — rate/increase counter'da ayrı per-seri delta yolundan
	// gider (reset-korumalı; PromQL semantiği). Diğer agg'ler tek-pass GROUP BY.
	if f.Aggregation == "rate" || f.Aggregation == "increase" {
		return s.QueryMetricRate(ctx, f, f.Aggregation)
	}
	// v0.9.107 (F3) — histogram metrikte p50/p95/p99 = histogram_quantile
	// (bucket dağılımından, reset-korumalı delta + lineer interp). Eski yol
	// quantile(value): value per-export ORTALAMA olduğundan histogram'da
	// yanlış. Explicit + exponential histogram'ın ikisi de (aynı bucket
	// kolonları). gauge/sum p-quantile'ları value-quantile'da kalır (doğru).
	switch strings.ToLower(f.Aggregation) {
	case "p50", "p95", "p99":
		if isHistogramInstrument(s.metricInstrument(ctx, f.Name, f.Service, f.From, f.To)) {
			return s.QueryMetricHistogramPercentile(ctx, f, f.Aggregation)
		}
	}

	// v0.9.776 — instrument/temporality TEK prob, İKİ tüketici: aşağıdaki
	// rollup yönlendirmesi ve agg=avg'ın SQL ifadesi (metricAvgExpr).
	// Prob yalnız gerektiğinde atılır — avg olmayan + Service'siz sorgu
	// v0.9.775'teki gibi hiç prob atmaz.
	isAvg := f.Aggregation == "" || strings.EqualFold(f.Aggregation, "avg")
	instrument, temporality := "", ""
	if f.Service != "" || isAvg {
		instrument = s.metricInstrument(ctx, f.Name, f.Service, f.From, f.To)
		// sum → rollup planı temporality'ye bakar (yalnız Service dalında).
		// histogram + avg → metricAvgExpr temporality'ye bakar.
		if (f.Service != "" && instrument == "sum") || (isAvg && isHistogramInstrument(instrument)) {
			temporality = s.metricTemporalityFiltered(ctx, f.Name, f.Service, f.Filters)
		}
	}

	// v0.9.777 — 0008 route (endpoint) kırılımlı tier. Aile-C'den ÖNCE
	// denenir çünkü şekilleri ayrık: aile-C GRUPSUZ sorguları alır, bu yol
	// yalnız `group by http.route` olanı. Servis burada bir FİLTRE ÇİPİ
	// olarak da gelebilir — terfi (promoteServiceFilter) yalnız rollup
	// KARARINDA kullanılır, aşağıya giden f'e dokunulmaz, dolayısıyla ham
	// yolun SQL'i bayt-bayt aynı kalır.
	//
	// Konum bilinçli: yukarıdaki (2) numaralı histogram-percentile dalından
	// SONRA. Öne alınsaydı p50/p95/p99 sorguları buradan geçerdi ve 0008
	// yüzdelik taşımadığı için ya yanlış cevap ya da boşuna prob olurdu
	// (v0.9.669 sınıfı: prob penceresini/WHERE'ini oynatan sıra hatası).
	if ser, ok := s.tryMetricRollupRoute(ctx, f); ok {
		return ser, nil
	}

	// v0.9.712 (parite dilim 4B) — aile-C rollup yönlendirmesi. Yalnız
	// 0003 şemasının DOĞRU cevapladığı şekiller (metric_rollup_read.go
	// başlığındaki tablo); cumulative + filtreli/gruplu HER ZAMAN ham.
	// Her uygunsuzluk/hata FAIL-OPEN hama düşer.
	//
	// NOT: histogram için metricRollupPlan default dalında ham'a düşer —
	// yani v0.9.776'nın histogram temporality probu rollup kararını
	// DEĞİŞTİRMEZ (gauge de temporality'ye bakmaz).
	if f.Service != "" {
		if tr, ok := metricRollupPlan(f, instrument, temporality, time.Now(), s.MetricExclusions()); ok {
			if floor, ok2 := s.rollupCoverageFloor(ctx, tr.table); ok2 && !f.From.Before(floor) {
				if ser, err := s.queryMetricRollup(ctx, f, tr); err == nil {
					return ser, nil
				}
				// hata → ham yola devam (fail-open)
			}
		}
	}

	// v0.8.243 — min-step clamp: never bucket finer than the metric's
	// observed export cadence (Grafana's $__rate_interval equivalent —
	// see metric_export_interval.go). Normalize the window + auto-step
	// HERE with the same rules buildMetricQuerySQL applies internally,
	// so the clamp sees the effective step, not the raw 0=auto.
	now := time.Now()
	if f.To.IsZero() {
		f.To = now
	}
	if f.From.IsZero() {
		f.From = f.To.Add(-24 * time.Hour)
	}
	if f.StepSeconds <= 0 {
		// v0.9.105 (F1) — pixel-adaptif; MaxDataPoints=0 ise eski ladder.
		f.StepSeconds = metricAutoStepPx(f.From, f.To, f.MaxDataPoints)
	}
	// v0.9.687 — filtreler proba da iniyor (bkz. metricrate.go'daki not).
	if iv := s.metricExportIntervalFiltered(ctx, f.Name, f.Service, f.Filters, len(f.GroupBy) > 0); iv > 0 {
		f.StepSeconds = clampStepToExport(f.StepSeconds, iv)
	}

	sql, args, err := buildMetricQuerySQL(f, now, instrument, temporality, s.MetricExclusions())
	if err != nil {
		return nil, err
	}

	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("metric query: %w", err)
	}
	defer rows.Close()

	seriesMap := make(map[string]*SpanMetricSeries)
	var order []string
	for rows.Next() {
		var bucket uint64
		var gk []string
		var val *float64
		if err := rows.Scan(&bucket, &gk, &val); err != nil {
			return nil, err
		}
		key := strings.Join(gk, "|")
		s, ok := seriesMap[key]
		if !ok {
			s = &SpanMetricSeries{GroupKey: gk}
			seriesMap[key] = s
			order = append(order, key)
		}
		v := 0.0
		if val != nil {
			v = *val
		}
		s.Points = append(s.Points, SpanMetricPoint{Time: int64(bucket), Value: v})
	}
	out := make([]SpanMetricSeries, 0, len(order))
	for _, k := range order {
		out = append(out, *seriesMap[k])
	}
	return out, rows.Err()
}

// metricAvgExpr — agg=avg'ın İÇ SQL ifadesi (saf, tablo-testli; sarmalama
// çağırana ait). v0.9.776.
//
// Sorun: histogram enstrümanda `value` per-export ÖZET'tir — ingest onu
// sum/count olarak yazar (otlp/convert.go, Metric_Histogram dalı). Bu
// satırların `avg`ı ORTALAMALARIN ORTALAMASI olur: her export penceresi,
// içindeki gözlem sayısına bakılmaksızın eşit ağırlık alır. 3 istek gören
// bir bucket ile 3000 istek gören bir bucket aynı ağırlıkta. Doğrusu
// gözlem-ağırlıklı ortalama: sum(sum_value) / sum(count).
//
// Sapma gözlem dağılımının tekdüzeliğiyle orantılı: lokal demoda ≤%2,5
// (per-export gözlem sayısı neredeyse sabit — demo artefaktı), prod-şekilli
// (diurnal + hata patlamalı) örnekte %28.
//
// Yalnız DELTA. Diğer dallar bilerek `avgOrNull(value)`da kalıyor:
//
//   - gauge / sum — `value` zaten ölçümün kendisi; sum_value/count 0'dır,
//     ağırlıklı ifade 0/0 verirdi.
//   - histogram + CUMULATIVE — sum_value ve count monoton BİRİKİMLİ. Tek
//     bir toplama ifadesiyle doğru yazılamaz: önce per-seri reset-korumalı
//     cross-bucket delta gerekir (metrichist.go'daki makine). Tek ifadeyle
//     yazmak birikmiş toplamları üst üste toplar — bugünkünden DAHA yanlış.
//     Bu yüzden cumulative bilinçli olarak eski (yaklaşık) yolda kalıyor;
//     sınır burada, docs/DECISIONS.md'de değil.
//   - summary — "histogram gibi ama histogram değil": ingest value'yu yine
//     sum/count yazıyor (convert.go:379) ve sum_value/count kolonları da
//     dolu. Teknik olarak aynı düzeltme uygulanabilirdi; bu sürümde
//     BİLİNÇLİ dokunulmuyor — summary üreten SDK'lar (Prometheus köprüleri)
//     kurulumda yok, ve isHistogramInstrument'in kapsamını genişletmek
//     percentile yönlendirmesini de etkilerdi. Ayrı karar.
func metricAvgExpr(instrument, temporality string) string {
	if isHistogramInstrument(instrument) && strings.EqualFold(temporality, "delta") {
		return "sum(sum_value) / nullIf(sum(count), 0)"
	}
	return "avgOrNull(value)"
}

// metricAggToSQL — agg etiketi → metric_points üstünde SQL ifadesi.
// instrument/temporality YALNIZ avg dalında okunur (metricAvgExpr); diğer
// her agg'in ürettiği SQL onlardan bağımsızdır ve v0.9.776 öncesiyle
// bayt-bayt aynıdır.
func metricAggToSQL(agg, instrument, temporality string) (string, error) {
	wrap := func(s string) string { return "toNullable(toFloat64(" + s + "))" }
	switch strings.ToLower(agg) {
	case "", "avg":
		return wrap(metricAvgExpr(instrument, temporality)), nil
	case "sum":
		return wrap("sumOrNull(value)"), nil
	case "min":
		return wrap("minOrNull(value)"), nil
	case "max":
		return wrap("maxOrNull(value)"), nil
	case "last":
		return wrap("argMaxOrNull(value, time)"), nil
	case "p50":
		return wrap("quantile(0.50)(value)"), nil
	case "p95":
		return wrap("quantile(0.95)(value)"), nil
	case "p99":
		return wrap("quantile(0.99)(value)"), nil
	}
	return "", fmt.Errorf("unknown aggregation %q", agg)
}

// MetricAttrKeys returns the distinct DATAPOINT attribute keys observed on a
// metric (v0.9.124) — powers PromQL `without(L)`, which groups by every label
// EXCEPT L and so must first discover what the labels are. Bounded (LIMIT +
// max_execution_time + time-bounded WHERE); the key COUNT is tiny (a metric has
// a handful of attr keys). Resource attrs like service.name aren't in
// attr_keys; the caller adds them to the candidate set separately.
func (s *Store) MetricAttrKeys(ctx context.Context, metric, service string, since time.Duration) ([]string, error) {
	if metric == "" {
		return nil, nil
	}
	now := time.Now()
	cutoff := now.Add(-since)
	q := `SELECT DISTINCT arrayJoin(attr_keys) AS k
	      FROM metric_points
	      WHERE metric = ? AND time >= ? AND time <= ?`
	args := []any{metric, cutoff, now}
	if service != "" {
		q += ` AND service_name = ?`
		args = append(args, service)
	}
	q += ` ORDER BY k LIMIT 100 SETTINGS max_execution_time = 5`
	rows, err := s.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		if k != "" {
			out = append(out, k)
		}
	}
	return out, rows.Err()
}

// MetricLabelValues returns distinct values for a single attribute key
// observed in the given metric — fuels the value-suggestions in the UI.
func (s *Store) MetricLabelValues(ctx context.Context, metric, key string, since time.Duration) ([]string, error) {
	if metric == "" || key == "" {
		return nil, nil
	}
	expr, args := groupKeyExprMetric(key)
	// v0.9.275 — this query had NONE of the three bounds its sibling
	// MetricAttrKeys carries ten lines above (time <= ?, LIMIT,
	// max_execution_time). The LIMIT it did have bounds nothing that matters:
	// ClickHouse computes the DISTINCT over the whole window BEFORE applying
	// LIMIT 200, so the work scales with the window, not with the answer.
	//
	// The real bound is max_execution_time. On a 1000-service install a shared
	// metric (jvm.memory.used) means a multi-GB Array(String) read, and the
	// SWR refresh runs on a 20s background context — so the ceiling was 20
	// seconds of ClickHouse, not infinity, but nothing was stopping it sooner.
	//
	// time <= ? is honest parity rather than protection: metric_points is
	// PARTITION BY toDate(time) and there are no future partitions, so it
	// prunes nothing. Included so the two siblings read the same.
	now := time.Now()
	cutoff := now.Add(-since)
	queryArgs := append(args, metric, cutoff, now)
	rows, err := s.conn.Query(ctx,
		`SELECT DISTINCT `+expr+` AS v
		 FROM metric_points
		 WHERE metric = ? AND time >= ? AND time <= ?
		 ORDER BY v
		 LIMIT 200
		 SETTINGS max_execution_time = 5`, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		if v != "" {
			out = append(out, v)
		}
	}
	return out, rows.Err()
}
