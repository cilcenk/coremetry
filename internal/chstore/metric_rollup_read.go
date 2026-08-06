package chstore

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// metric_rollup_read.go — aile-C OKUYUCUSU (v0.9.712, parite dilim 4B).
//
// Yazım tarafı v0.9.385'te gemiye girdi (migrations/0003_rollup_metrics:
// 1m→5m→1h kaskadı) ama okuyucusu HİÇ yazılmadı — parite taban çizgisi
// §C: "metric_points HER pencerede %100 ham". Bu dosya o boşluğu
// kapatıyor.
//
// SPEC SAPMASI, AÇIKÇA (operatöre bildirildi): onaylı spec seri-kimlikli
// (series_fingerprint) ORDER BY öngörüyordu; mevcut 0003 şeması prod'da
// uygulanmış olabilir ve attr SERİLERİNİ BİRLEŞTİRİYOR (GROUP BY'da
// kimlik yok). Yıkıcı recreate yerine okuyucu YALNIZ bu şemanın DOĞRU
// cevapladığı şekilleri yönlendirir:
//
//	gauge  + avg/min/max/sum/last  → value_sum/point_count, min, max, argMax
//	sum(delta) + sum/avg           → value_sum (delta'da bucket toplamı)
//	histogram                      → BU DİLİMDE YOK (bounds-set birleşimi
//	                                 ayrı iş; hist_counts hazır bekliyor)
//	sum(cumulative)                → HER ZAMAN HAM. argMax çok-serili
//	                                 sayaçta "son yazan seri"yi seçer —
//	                                 rate bundan kurulamaz. Kimlikli tier
//	                                 (0008 adayı) gelene kadar ham yol.
//
// Filtre/GroupBy/Instance/Engine varsa → ham (rollup attr taşımıyor).
// Uygunsuzlukta her yol FAIL-OPEN hama düşer: yavaş cevap, yanlış
// cevaptan iyidir (metricCatalog kapılarıyla aynı duruş).

// metricRollupTier — 0003'ün kademeleri. Grain step'i TAM BÖLMELİ
// (rollup_fastpath ile aynı kural: kademe step'e uyar, tersi değil).
type metricRollupTier struct {
	table    string
	grainSec int64
	ttl      time.Duration
}

// Kaba→ince: en az satır okuyan İLK uygun kademe kazanır
// (selectMetricTier ile aynı yürüyüş yönü).
var metricRollupTiers = []metricRollupTier{
	{"rollup_metrics_1h", 3600, 13 * 30 * 24 * time.Hour},
	{"rollup_metrics_5m", 300, 90 * 24 * time.Hour},
	{"rollup_metrics_1m", 60, 14 * 24 * time.Hour},
}

// rollupEligibleAggs — gauge'da güvenli agregasyonlar. p-quantile YOK:
// value-quantile ham noktalar ister, rollup'ta noktalar yok.
var rollupEligibleAggs = map[string]bool{
	"": true, "avg": true, "sum": true, "min": true, "max": true, "last": true,
}

// metricRollupPlan — SAF uygunluk kararı (tablo-testli). ok=false → ham.
//
// instrument ve temporality ÇAĞIRANDAN gelir (Store zaten çözüyor);
// burada CH'ye gidilmez — karar saf kalır.
func metricRollupPlan(f MetricQueryFilter, instrument, temporality string, now time.Time) (metricRollupTier, bool) {
	if f.Name == "" || len(f.Filters) > 0 || len(f.GroupBy) > 0 || f.Instance != "" || f.Engine != "" {
		return metricRollupTier{}, false
	}
	agg := strings.ToLower(f.Aggregation)
	switch instrument {
	case "gauge":
		if !rollupEligibleAggs[agg] {
			return metricRollupTier{}, false
		}
	case "sum":
		// Cumulative ASLA (dosya başı gerekçe). Delta'da yalnız toplamsal
		// agg'ler: min/max/last delta parçaları üstünde anlamsız.
		if temporality != "delta" || (agg != "sum" && agg != "avg" && agg != "") {
			return metricRollupTier{}, false
		}
	default:
		// histogram bu dilimde ham; bilinmeyen enstrüman ham.
		return metricRollupTier{}, false
	}
	step := int64(f.StepSeconds)
	if step <= 0 {
		return metricRollupTier{}, false // auto-step ham merdivende kalır (bu dilim)
	}
	oldest := now.Sub(f.From)
	for _, t := range metricRollupTiers {
		if step%t.grainSec == 0 && oldest <= t.ttl {
			return t, true
		}
	}
	return metricRollupTier{}, false
}

// rollupCoverageFloor — kademenin EN ESKİ dolu bucket'ı (ileri-yalnız
// cutover: tablo dün kurulduysa geçen haftanın sorgusu hama düşmeli).
// 5 dk önbellek — her sorguda min(ts) taraması olmasın.
type rollupCoverage struct {
	mu    sync.Mutex
	at    time.Time
	floor map[string]time.Time
}

var rollupCov rollupCoverage

func (s *Store) rollupCoverageFloor(ctx context.Context, table string) (time.Time, bool) {
	rollupCov.mu.Lock()
	defer rollupCov.mu.Unlock()
	if time.Since(rollupCov.at) > 5*time.Minute {
		rollupCov.floor = nil
	}
	if rollupCov.floor != nil {
		t, ok := rollupCov.floor[table]
		return t, ok
	}
	rollupCov.floor = map[string]time.Time{}
	rollupCov.at = time.Now()
	for _, tr := range metricRollupTiers {
		var mn time.Time
		// Tablo yoksa (migration uygulanmamış kurulum) sorgu hata verir →
		// haritaya girmez → o kademe kapalı kalır. FAIL-OPEN hama.
		err := s.telemetryReadConn().QueryRow(ctx,
			`SELECT min(ts) FROM `+tr.table+` SETTINGS max_execution_time = 3`).Scan(&mn)
		if err == nil && !mn.IsZero() {
			rollupCov.floor[tr.table] = mn
		}
	}
	t, ok := rollupCov.floor[table]
	return t, ok
}

// queryMetricRollup — uygun şekli rollup'tan okur. Bir SpanMetricSeries
// döndürür (groupKey boş — bu yol yalnız GRUPSUZ sorgulara açık).
//
// EXPLAIN indexes=1 (lokal, 1m kademe, 1 saat pencere):
//
//	PrimaryKey (metric, service_name, …): 1/1 part, 1/8 granül —
//	okunan satır ~60 (bucket sayısı), ham yolun ~19k satırına karşı.
func (s *Store) queryMetricRollup(ctx context.Context, f MetricQueryFilter, tr metricRollupTier) ([]SpanMetricSeries, error) {
	agg := strings.ToLower(f.Aggregation)
	var valExpr string
	switch agg {
	case "", "avg":
		valExpr = "sum(value_sum) / nullIf(sum(point_count), 0)"
	case "sum":
		valExpr = "sum(value_sum)"
	case "min":
		valExpr = "min(value_min)"
	case "max":
		valExpr = "max(value_max)"
	case "last":
		valExpr = "argMaxMerge(last_value)"
	default:
		return nil, fmt.Errorf("rollup: desteklenmeyen agg %q", agg)
	}
	q := fmt.Sprintf(`
		SELECT toInt64(toUnixTimestamp(toStartOfInterval(ts, INTERVAL ? SECOND))) AS tb,
		       toFloat64(%s) AS v
		FROM %s
		WHERE metric = ? AND service_name = ?
		  AND ts >= ? AND ts <= ?
		GROUP BY tb ORDER BY tb
		LIMIT 5000
		SETTINGS max_execution_time = 10`, valExpr, tr.table)
	rows, err := s.telemetryReadConn().Query(ctx, q,
		f.StepSeconds, f.Name, f.Service, f.From, f.To)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ser := SpanMetricSeries{GroupKey: []string{}}
	for rows.Next() {
		var tb int64
		var v *float64
		if err := rows.Scan(&tb, &v); err != nil {
			return nil, err
		}
		if v == nil {
			continue // boş bucket üretmiyoruz — eksik veri null kalır (grid yok)
		}
		ser.Points = append(ser.Points, SpanMetricPoint{Time: tb * 1e9, Value: *v})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return []SpanMetricSeries{ser}, nil
}
