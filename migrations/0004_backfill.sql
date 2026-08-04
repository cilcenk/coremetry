-- 0004_backfill.sql — Zaman aralığı bazlı, parça parça backfill şablonları
-- Aşama 1 (2026-07-30). BU DOSYA OLDUĞU GİBİ ÇALIŞTIRILMAZ — :from/:to
-- yer tutucularıyla GÜN GÜN (10s tabanı için SAAT SAAT) döngüyle koşulur.
-- Strateji ve sıra: docs/rollup-design.md §Backfill.
--
-- KURALLAR
-- · MV'ler yalnız YENİ insert'leri görür — geçmiş, aşağıdaki INSERT...SELECT'lerle doldurulur.
-- · Sıra: önce TABAN tablolar geçmişten (spans_local), sonra kaskadlar ALT
--   kademeden (10s→1m→5m→1h). Kaskadı taban bitmeden koşma.
-- · Her parça kendi zaman aralığında idempotent DEĞİLDİR (AggregatingMergeTree
--   mükerrer state'i toplar) — yarım kalan parçayı tekrarlamadan önce o
--   aralığın partition'ını DROP et (parça sınırlarını partition sınırlarına
--   hizala: 10s → saat, diğerleri → gün).
-- · Her shard'da AYRI koşulur (spans_local shard-yerel) — clusterAllReplicas
--   DEĞİL; ON CLUSTER da DEĞİL (INSERT'ler DDL değildir).
-- · MV'ler CANLI kalırken backfill güvenlidir: canlı MV yeni dakikaları,
--   backfill eski dakikaları yazar; çakışma penceresi (başlangıç anındaki
--   açık dakika) için backfill 'to' sınırını MV'nin devreye girdiği andan
--   ÖNCEKİ tam bucket'a çek.
-- · Öneri ayarlar: max_insert_threads=4, max_execution_time=600,
--   optimize_on_insert=1 (parça içinde ön-birleştirme).

-- ── A) DAR 10s tabanı — SAATLİK parçalar: :from = saat başı, :to = :from + 1h
INSERT INTO rollup_spans_narrow_10s_local
SELECT
    toStartOfInterval(time, INTERVAL 10 SECOND)          AS ts,
    service_name,
    kind                                                 AS span_kind,
    status_code,
    toUInt64(count())                                    AS span_count,
    toUInt64(countIf(status_code = 'error'))             AS error_count,
    toUInt64(sum(duration))                              AS duration_sum,
    quantilesTDigestState(0.5, 0.95, 0.99)(duration)     AS q_state,
    argMaxState(trace_id, duration)                      AS slow_exemplar,
    anyIfState(trace_id, status_code = 'error')          AS err_exemplar
FROM spans_local
WHERE time >= {from:DateTime} AND time < {to:DateTime}
GROUP BY ts, service_name, span_kind, status_code
SETTINGS max_insert_threads = 4, max_execution_time = 600;

-- ── B) GENİŞ 1m tabanı — GÜNLÜK parçalar (yoğun günlerde 6 saatlik)
INSERT INTO rollup_spans_wide_1m_local
SELECT
    toStartOfInterval(time, INTERVAL 1 MINUTE)                     AS ts,
    service_name,
    kind                                                           AS span_kind,
    status_code,
    if(http_route != '', http_route, name)                         AS endpoint,
    -- v0.9.626 — iki yazım da (gerekçe 0002'de; prod küçük harf yazıyor
    -- ve yalnız BÜYÜK harf okuyan bir backfill boyutu boş doldururdu).
    toLowCardinality(coalesce(
        nullIf(attr_values[indexOf(attr_keys, 'CHANNEL_CODE')], ''),
        nullIf(attr_values[indexOf(attr_keys, 'channel_code')], ''), ''))  AS channel_code,
    coalesce(
        nullIf(attr_values[indexOf(attr_keys, 'FUNCTION_CODE')], ''),
        nullIf(attr_values[indexOf(attr_keys, 'function_code')], ''), '')  AS function_code,
    toUInt64(count())                                              AS span_count,
    toUInt64(countIf(status_code = 'error'))                       AS error_count,
    toUInt64(sum(duration))                                        AS duration_sum,
    sumForEachState(arrayMap(i -> toUInt64(i = least(19, greatest(0,
        toInt32(floor(log2(greatest(duration, 1000000) / 1000000)))))), range(20)))
                                                                   AS lat_buckets,
    argMaxState(trace_id, duration)                                AS slow_exemplar,
    anyIfState(trace_id, status_code = 'error')                    AS err_exemplar
FROM spans_local
WHERE time >= {from:DateTime} AND time < {to:DateTime}
GROUP BY ts, service_name, span_kind, status_code, endpoint, channel_code, function_code
SETTINGS max_insert_threads = 4, max_execution_time = 600;

-- ── Kaskad backfill şablonu (örnek: dar 10s → 1m; diğerleri aynı desen) ──
INSERT INTO rollup_spans_narrow_1m_local
SELECT
    toStartOfInterval(ts, INTERVAL 1 MINUTE)                       AS ts,
    service_name, span_kind, status_code,
    sum(span_count), sum(error_count), sum(duration_sum),
    quantilesTDigestMergeState(0.5, 0.95, 0.99)(q_state),
    argMaxMergeState(slow_exemplar),
    anyIfMergeState(err_exemplar)
FROM rollup_spans_narrow_10s_local
WHERE ts >= {from:DateTime} AND ts < {to:DateTime}
GROUP BY ts, service_name, span_kind, status_code
SETTINGS max_insert_threads = 4, max_execution_time = 600;

-- NOT: dar ailenin 5m/1h ve geniş ailenin 5m/1h kaskadları, spans
-- retention'ı (varsayılan 30 gün, store.go:557) İZİN VERDİĞİ KADAR
-- tabandan doldurulur — 90 gün / 13 aylık kademeler ancak sistem canlıya
-- alındıktan sonra doğal akışla dolar; backfill yalnız eldeki ham veriyle
-- sınırlıdır ve doküman bunun beklenen olduğunu açıkça söyler.

-- ── C) Metrics 1m tabanı — GÜNLÜK parçalar
INSERT INTO rollup_metrics_1m_local
SELECT
    toStartOfInterval(time, INTERVAL 1 MINUTE)     AS ts,
    metric, service_name, instrument, unit, bucket_bounds,
    toUInt64(count()), sum(value), min(value), max(value),
    argMaxState(value, time),
    sumForEachState(bucket_counts)
FROM metric_points_local
WHERE time >= {from:DateTime} AND time < {to:DateTime}
GROUP BY ts, metric, service_name, instrument, unit, bucket_bounds
SETTINGS max_insert_threads = 4, max_execution_time = 600;
