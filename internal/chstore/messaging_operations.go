package chstore

// messaging_operations.go — v0.10.563 (Messaging Faz 4b, backend yarısı).
//
// Bir topic'in OPERASYON düzeyi RED'i: publish / receive / process / settle
// kırılımında span sayısı, hata oranı ve gecikme yüzdelikleri. Kaynak
// messaging_summary_5m MV'si — o MV v0.10.563'te `operation` boyutunu
// kazandı (DDL store.go, boot geçişi mvDimMigrations). Ham `spans` DEĞİL:
// milyar-span ölçeğinde bir toplamı ham tablodan okumak hatadır.
//
// Kardeşi msgTopOpsSQL (dependencies.go) hâlâ ham spans okuyor çünkü onun
// ana pivotu span ADI; burada pivot yalnız operasyon türü ve o boyut artık
// MV'de materyalize.

import (
	"context"
	"time"
)

// MsgOperationStat — tek bir messaging operasyonunun pencere içi RED'i.
// Operation BOŞ olabilir: SDK üç semconv anahtarından hiçbirini yaymamış
// demektir. Burada '(bilinmiyor)' gibi bir etikete ÇEVİRMİYORUZ — etiketleme
// frontend'in işi; store katmanı ölçtüğünü aynen döndürür.
type MsgOperationStat struct {
	Operation  string  `json:"operation"`
	SpanCount  uint64  `json:"spanCount"`
	ErrorCount uint64  `json:"errorCount"`
	ErrorRate  float64 `json:"errorRate"`
	AvgMs      float64 `json:"avgDurationMs"`
	P50Ms      float64 `json:"p50DurationMs"`
	P95Ms      float64 `json:"p95DurationMs"`
	P99Ms      float64 `json:"p99DurationMs"`
}

// msgOperationREDSQL — SAF sorgu metni (messaging_operation_dim_test.go
// sınırlarını pinler): MV kaynağı + zaman-sınırlı WHERE + LIMIT +
// max_execution_time. error_count_state bu MV'de countIfState olduğundan
// merge'ü countMerge — kardeş okumalarla (getMessaging, GetMessagingDetail)
// birebir aynı biçim, yoksa aynı topic iki yüzeyde iki hata oranı gösterir.
const msgOperationREDSQL = `
		SELECT operation,
		       countMerge(span_count_state)                            AS span_count,
		       countMerge(error_count_state)                           AS error_count,
		       sumMerge(duration_sum_state) / 1e6
		         / nullIf(countMerge(span_count_state), 0)             AS avg_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 1) / 1e6 AS p50_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 2) / 1e6 AS p95_ms,
		       arrayElement(quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state), 3) / 1e6 AS p99_ms
		FROM messaging_summary_5m
		WHERE time_bucket >= ? AND time_bucket < ?
		  AND msg_system = ? AND cluster = ? AND destination = ?
		GROUP BY operation
		ORDER BY span_count DESC
		LIMIT 20
		SETTINGS max_execution_time = 8`

// MessagingOperationRED — (system, cluster, destination) için operasyon
// kırılımı, span sayısına göre azalan, en fazla 20 satır.
//
// Pencere başlangıcı alignBucketStart ile hizalanır (v0.9.813 dersi): MV
// kovaları BAŞLANGIÇLARIYLA etiketli, hizalanmamış bir `>= from` baştaki
// kısmi kovayı tamamen eler ve bu tablo çekmecenin toplamından az sayı
// gösterirdi.
func (s *Store) MessagingOperationRED(
	ctx context.Context, system, cluster, destination string, from, to time.Time,
) ([]MsgOperationStat, error) {
	if system == "" || destination == "" {
		return nil, nil
	}
	if cluster == "" {
		cluster = "(default)"
	}
	if to.IsZero() {
		to = time.Now()
	}
	if from.IsZero() {
		from = to.Add(-time.Hour)
	}
	rows, err := s.telemetryReadConn().Query(ctx, msgOperationREDSQL,
		alignBucketStart(from), to, system, cluster, destination)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []MsgOperationStat{}
	for rows.Next() {
		var r MsgOperationStat
		var avgMs, p50Ms, p95Ms, p99Ms *float64
		if err := rows.Scan(&r.Operation, &r.SpanCount, &r.ErrorCount,
			&avgMs, &p50Ms, &p95Ms, &p99Ms); err != nil {
			continue
		}
		// v0.5.301 — JSON marshal öncesi NaN/Inf temizliği.
		r.AvgMs = safeF(avgMs)
		r.P50Ms = safeF(p50Ms)
		r.P95Ms = safeF(p95Ms)
		r.P99Ms = safeF(p99Ms)
		if r.SpanCount > 0 {
			r.ErrorRate = float64(r.ErrorCount) / float64(r.SpanCount) * 100
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
