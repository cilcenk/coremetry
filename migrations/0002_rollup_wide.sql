-- 0002_rollup_wide.sql — GENİŞ rollup ailesi (drill-down / breakdown)
-- Aşama 1 DDL (2026-07-30). Ingest/API koduna dokunmaz.
--
-- Dimension: service_name, span_kind, status_code, endpoint, channel_code, function_code
-- Taban 1m. Kaskad: 1m → 5m → 1h
-- Latency: tDigest DEĞİL — 20 elemanlı sabit eksponansiyel bucket sayacı
--   bucket i = [1ms·2^i, 1ms·2^(i+1))  (i=0..19; son bucket ≥ ~8.7dk taşmaları toplar)
--   Array(UInt64) + sumForEachState; kaskadlarda sumForEachMergeState.
--
-- ŞEMA DOĞRULAMASI (bu repo):
-- · CHANNEL_CODE / FUNCTION_CODE spans'ta AYRI KOLON DEĞİL — attr_keys/attr_values
--   dizisinde yaşar (store.go:589-590). Opsiyonel promotion katmanı mevcut
--   (traceAttrMaterialized, repo.go:2385 — boş gelir, per-install ALTER + boot
--   probe ile dolar; docs/audit/traces-attribute-columns.md).
--   Bu MV attr-dizisinden okur (gün-1 çalışır); attr_channel_code /
--   attr_function_code MATERIALIZED kolonları promote edilmişse aşağıdaki
--   yorumlu satırlarla değiştirilerek ingest maliyeti düşürülür.
-- · endpoint: http_route doluysa o, değilse span adı (name) — endpoints.go
--   EntryHTTP/EntryRPC ayrımının tek-kolona katlanmış hali.
-- · function_code: distinct sayısı prod'da bilinmiyor. Tasarım kuralı gereği
--   GÜVENLİ varsayılan = düz String + bloom_filter (LowCardinality'ye ancak
--   `SELECT uniq(attr_values[indexOf(attr_keys,'FUNCTION_CODE')]) FROM spans
--    WHERE time > now() - INTERVAL 1 DAY` < 10k ise geçilir).

-- ─────────────────────────────── 1m taban ────────────────────────────────
CREATE TABLE IF NOT EXISTS rollup_spans_wide_1m_local ON CLUSTER uptrace_all
(
    ts            DateTime CODEC(Delta, ZSTD(1)),
    service_name  LowCardinality(String),
    span_kind     LowCardinality(String),
    status_code   LowCardinality(String),
    endpoint      LowCardinality(String),
    channel_code  LowCardinality(String),
    function_code String,
    span_count    SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    error_count   SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    duration_sum  SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),  -- ns
    lat_buckets   AggregateFunction(sumForEach, Array(UInt64)),               -- 20 eksp. bucket, ~160B
    slow_exemplar AggregateFunction(argMax, String, Int64),
    err_exemplar  AggregateFunction(anyIf, String, UInt8),
    INDEX idx_function function_code TYPE bloom_filter(0.01) GRANULARITY 4
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/rollup_spans_wide_1m', '{replica}')
PARTITION BY toDate(ts)
ORDER BY (service_name, span_kind, status_code, channel_code, endpoint, function_code, ts)
TTL ts + INTERVAL 14 DAY
SETTINGS ttl_only_drop_parts = 1, index_granularity = 8192;

CREATE TABLE IF NOT EXISTS rollup_spans_wide_1m ON CLUSTER uptrace_all
    AS rollup_spans_wide_1m_local
ENGINE = Distributed(uptrace_all, currentDatabase(), rollup_spans_wide_1m_local, rand());

-- bucket indeksi: i = clamp(floor(log2(duration_ms)), 0, 19); duration<1ms → 0.
-- greatest(duration, 1000000): log2(0) tanımsızlığına karşı 1ms tabanı.
CREATE MATERIALIZED VIEW IF NOT EXISTS mv_rollup_spans_wide_1m ON CLUSTER uptrace_all
TO rollup_spans_wide_1m_local
AS SELECT
    toStartOfInterval(time, INTERVAL 1 MINUTE)                     AS ts,
    service_name,
    kind                                                           AS span_kind,
    status_code,
    if(http_route != '', http_route, name)                         AS endpoint,
    -- Promotion uygulanmışsa: attr_channel_code AS channel_code
    toLowCardinality(attr_values[indexOf(attr_keys, 'CHANNEL_CODE')])  AS channel_code,
    -- Promotion uygulanmışsa: attr_function_code AS function_code
    attr_values[indexOf(attr_keys, 'FUNCTION_CODE')]               AS function_code,
    toUInt64(count())                                              AS span_count,
    toUInt64(countIf(status_code = 'error'))                       AS error_count,
    toUInt64(sum(duration))                                        AS duration_sum,
    sumForEachState(arrayMap(i -> toUInt64(i = least(19, greatest(0,
        toInt32(floor(log2(greatest(duration, 1000000) / 1000000)))))), range(20)))
                                                                   AS lat_buckets,
    argMaxState(trace_id, duration)                                AS slow_exemplar,
    anyIfState(trace_id, status_code = 'error')                    AS err_exemplar
FROM spans_local
GROUP BY ts, service_name, span_kind, status_code, endpoint, channel_code, function_code;

-- ─────────────────────────────── 5m kaskadı ──────────────────────────────
CREATE TABLE IF NOT EXISTS rollup_spans_wide_5m_local ON CLUSTER uptrace_all
(
    ts            DateTime CODEC(Delta, ZSTD(1)),
    service_name  LowCardinality(String),
    span_kind     LowCardinality(String),
    status_code   LowCardinality(String),
    endpoint      LowCardinality(String),
    channel_code  LowCardinality(String),
    function_code String,
    span_count    SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    error_count   SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    duration_sum  SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    lat_buckets   AggregateFunction(sumForEach, Array(UInt64)),
    slow_exemplar AggregateFunction(argMax, String, Int64),
    err_exemplar  AggregateFunction(anyIf, String, UInt8),
    INDEX idx_function function_code TYPE bloom_filter(0.01) GRANULARITY 4
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/rollup_spans_wide_5m', '{replica}')
PARTITION BY toDate(ts)
ORDER BY (service_name, span_kind, status_code, channel_code, endpoint, function_code, ts)
TTL ts + INTERVAL 90 DAY
SETTINGS ttl_only_drop_parts = 1, index_granularity = 8192;

CREATE TABLE IF NOT EXISTS rollup_spans_wide_5m ON CLUSTER uptrace_all
    AS rollup_spans_wide_5m_local
ENGINE = Distributed(uptrace_all, currentDatabase(), rollup_spans_wide_5m_local, rand());

CREATE MATERIALIZED VIEW IF NOT EXISTS mv_rollup_spans_wide_5m ON CLUSTER uptrace_all
TO rollup_spans_wide_5m_local
AS SELECT
    toStartOfInterval(ts, INTERVAL 5 MINUTE)                       AS ts,
    service_name, span_kind, status_code, endpoint, channel_code, function_code,
    sum(span_count)                                                AS span_count,
    sum(error_count)                                               AS error_count,
    sum(duration_sum)                                              AS duration_sum,
    sumForEachMergeState(lat_buckets)                              AS lat_buckets,
    argMaxMergeState(slow_exemplar)                                AS slow_exemplar,
    anyIfMergeState(err_exemplar)                                  AS err_exemplar
FROM rollup_spans_wide_1m_local
GROUP BY ts, service_name, span_kind, status_code, endpoint, channel_code, function_code;

-- ─────────────────────────────── 1h kaskadı ──────────────────────────────
CREATE TABLE IF NOT EXISTS rollup_spans_wide_1h_local ON CLUSTER uptrace_all
(
    ts            DateTime CODEC(Delta, ZSTD(1)),
    service_name  LowCardinality(String),
    span_kind     LowCardinality(String),
    status_code   LowCardinality(String),
    endpoint      LowCardinality(String),
    channel_code  LowCardinality(String),
    function_code String,
    span_count    SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    error_count   SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    duration_sum  SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    lat_buckets   AggregateFunction(sumForEach, Array(UInt64)),
    slow_exemplar AggregateFunction(argMax, String, Int64),
    err_exemplar  AggregateFunction(anyIf, String, UInt8),
    INDEX idx_function function_code TYPE bloom_filter(0.01) GRANULARITY 4
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/rollup_spans_wide_1h', '{replica}')
PARTITION BY toDate(ts)
ORDER BY (service_name, span_kind, status_code, channel_code, endpoint, function_code, ts)
TTL ts + INTERVAL 13 MONTH
SETTINGS ttl_only_drop_parts = 1, index_granularity = 8192;

CREATE TABLE IF NOT EXISTS rollup_spans_wide_1h ON CLUSTER uptrace_all
    AS rollup_spans_wide_1h_local
ENGINE = Distributed(uptrace_all, currentDatabase(), rollup_spans_wide_1h_local, rand());

CREATE MATERIALIZED VIEW IF NOT EXISTS mv_rollup_spans_wide_1h ON CLUSTER uptrace_all
TO rollup_spans_wide_1h_local
AS SELECT
    toStartOfInterval(ts, INTERVAL 1 HOUR)                         AS ts,
    service_name, span_kind, status_code, endpoint, channel_code, function_code,
    sum(span_count)                                                AS span_count,
    sum(error_count)                                               AS error_count,
    sum(duration_sum)                                              AS duration_sum,
    sumForEachMergeState(lat_buckets)                              AS lat_buckets,
    argMaxMergeState(slow_exemplar)                                AS slow_exemplar,
    anyIfMergeState(err_exemplar)                                  AS err_exemplar
FROM rollup_spans_wide_5m_local
GROUP BY ts, service_name, span_kind, status_code, endpoint, channel_code, function_code;
