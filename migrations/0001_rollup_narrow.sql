-- 0001_rollup_narrow.sql — DAR rollup ailesi (hızlı overview)
-- Aşama 1 DDL (2026-07-30). Ingest/API koduna dokunmaz; tablolar + MV'ler.
--
-- Dimension: service_name, span_kind, status_code
-- Kaskad: spans_local → 10s → 1m → 5m → 1h
-- Latency: quantilesTDigestState(0.5,0.95,0.99)(duration)  [duration = NANOSANİYE, spans.duration Int64]
-- Kaynak şema doğrulaması: internal/chstore/store.go:567 (spans DDL),
-- duration ns teyidi spanmetric.go:376 (okumalar /1e6 → ms).
--
-- NOT (deploy-öncesi kontrol): Coremetry'nin kendi boot probe'u dış
-- distributed prod'da cluster_name'i çoğu zaman BOŞ görür (v0.8.185/186
-- olayları). Bu dosya tasarım gereği `ON CLUSTER uptrace_all` yazar —
-- uygulamadan önce `SELECT * FROM system.clusters WHERE cluster='uptrace_all'`
-- ile 2 shard × 2 replica'yı doğrula; macros ({shard},{replica}) tanımlı olmalı.

-- ─────────────────────────────── 10s taban ───────────────────────────────
CREATE TABLE IF NOT EXISTS rollup_spans_narrow_10s_local ON CLUSTER uptrace_all
(
    ts            DateTime CODEC(Delta, ZSTD(1)),
    service_name  LowCardinality(String),
    span_kind     LowCardinality(String),
    status_code   LowCardinality(String),
    span_count    SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    error_count   SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    duration_sum  SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),  -- ns toplamı
    q_state       AggregateFunction(quantilesTDigest(0.5, 0.95, 0.99), Int64),
    slow_exemplar AggregateFunction(argMax, String, Int64),                   -- en yavaş trace_id
    err_exemplar  AggregateFunction(anyIf, String, UInt8)                     -- bir hatalı trace_id
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/rollup_spans_narrow_10s', '{replica}')
PARTITION BY toStartOfHour(ts)          -- tasarım: 10s tablosunda saatlik partition
ORDER BY (service_name, span_kind, status_code, ts)
TTL ts + INTERVAL 7 DAY
SETTINGS ttl_only_drop_parts = 1, index_granularity = 8192;

CREATE TABLE IF NOT EXISTS rollup_spans_narrow_10s ON CLUSTER uptrace_all
    AS rollup_spans_narrow_10s_local
ENGINE = Distributed(uptrace_all, currentDatabase(), rollup_spans_narrow_10s_local, rand());

-- MV _local'a bağlanır (Distributed'a DEĞİL) ve _local'dan okur:
-- her shard kendi span'lerini kendi rollup'ına yazar, shard'lar arası ağır yok.
CREATE MATERIALIZED VIEW IF NOT EXISTS mv_rollup_spans_narrow_10s ON CLUSTER uptrace_all
TO rollup_spans_narrow_10s_local
AS SELECT
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
GROUP BY ts, service_name, span_kind, status_code;

-- ─────────────────────────────── 1m kaskadı ──────────────────────────────
CREATE TABLE IF NOT EXISTS rollup_spans_narrow_1m_local ON CLUSTER uptrace_all
(
    ts            DateTime CODEC(Delta, ZSTD(1)),
    service_name  LowCardinality(String),
    span_kind     LowCardinality(String),
    status_code   LowCardinality(String),
    span_count    SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    error_count   SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    duration_sum  SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    q_state       AggregateFunction(quantilesTDigest(0.5, 0.95, 0.99), Int64),
    slow_exemplar AggregateFunction(argMax, String, Int64),
    err_exemplar  AggregateFunction(anyIf, String, UInt8)
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/rollup_spans_narrow_1m', '{replica}')
PARTITION BY toDate(ts)
ORDER BY (service_name, span_kind, status_code, ts)
TTL ts + INTERVAL 14 DAY
SETTINGS ttl_only_drop_parts = 1, index_granularity = 8192;

CREATE TABLE IF NOT EXISTS rollup_spans_narrow_1m ON CLUSTER uptrace_all
    AS rollup_spans_narrow_1m_local
ENGINE = Distributed(uptrace_all, currentDatabase(), rollup_spans_narrow_1m_local, rand());

CREATE MATERIALIZED VIEW IF NOT EXISTS mv_rollup_spans_narrow_1m ON CLUSTER uptrace_all
TO rollup_spans_narrow_1m_local
AS SELECT
    toStartOfInterval(ts, INTERVAL 1 MINUTE)                       AS ts,
    service_name, span_kind, status_code,
    sum(span_count)                                                AS span_count,
    sum(error_count)                                               AS error_count,
    sum(duration_sum)                                              AS duration_sum,
    quantilesTDigestMergeState(0.5, 0.95, 0.99)(q_state)           AS q_state,
    argMaxMergeState(slow_exemplar)                                AS slow_exemplar,
    anyIfMergeState(err_exemplar)                                  AS err_exemplar
FROM rollup_spans_narrow_10s_local
GROUP BY ts, service_name, span_kind, status_code;

-- ─────────────────────────────── 5m kaskadı ──────────────────────────────
CREATE TABLE IF NOT EXISTS rollup_spans_narrow_5m_local ON CLUSTER uptrace_all
(
    ts            DateTime CODEC(Delta, ZSTD(1)),
    service_name  LowCardinality(String),
    span_kind     LowCardinality(String),
    status_code   LowCardinality(String),
    span_count    SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    error_count   SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    duration_sum  SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    q_state       AggregateFunction(quantilesTDigest(0.5, 0.95, 0.99), Int64),
    slow_exemplar AggregateFunction(argMax, String, Int64),
    err_exemplar  AggregateFunction(anyIf, String, UInt8)
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/rollup_spans_narrow_5m', '{replica}')
PARTITION BY toDate(ts)
ORDER BY (service_name, span_kind, status_code, ts)
TTL ts + INTERVAL 90 DAY
SETTINGS ttl_only_drop_parts = 1, index_granularity = 8192;

CREATE TABLE IF NOT EXISTS rollup_spans_narrow_5m ON CLUSTER uptrace_all
    AS rollup_spans_narrow_5m_local
ENGINE = Distributed(uptrace_all, currentDatabase(), rollup_spans_narrow_5m_local, rand());

CREATE MATERIALIZED VIEW IF NOT EXISTS mv_rollup_spans_narrow_5m ON CLUSTER uptrace_all
TO rollup_spans_narrow_5m_local
AS SELECT
    toStartOfInterval(ts, INTERVAL 5 MINUTE)                       AS ts,
    service_name, span_kind, status_code,
    sum(span_count)                                                AS span_count,
    sum(error_count)                                               AS error_count,
    sum(duration_sum)                                              AS duration_sum,
    quantilesTDigestMergeState(0.5, 0.95, 0.99)(q_state)           AS q_state,
    argMaxMergeState(slow_exemplar)                                AS slow_exemplar,
    anyIfMergeState(err_exemplar)                                  AS err_exemplar
FROM rollup_spans_narrow_1m_local
GROUP BY ts, service_name, span_kind, status_code;

-- ─────────────────────────────── 1h kaskadı ──────────────────────────────
CREATE TABLE IF NOT EXISTS rollup_spans_narrow_1h_local ON CLUSTER uptrace_all
(
    ts            DateTime CODEC(Delta, ZSTD(1)),
    service_name  LowCardinality(String),
    span_kind     LowCardinality(String),
    status_code   LowCardinality(String),
    span_count    SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    error_count   SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    duration_sum  SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    q_state       AggregateFunction(quantilesTDigest(0.5, 0.95, 0.99), Int64),
    slow_exemplar AggregateFunction(argMax, String, Int64),
    err_exemplar  AggregateFunction(anyIf, String, UInt8)
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/rollup_spans_narrow_1h', '{replica}')
PARTITION BY toDate(ts)
ORDER BY (service_name, span_kind, status_code, ts)
TTL ts + INTERVAL 13 MONTH
SETTINGS ttl_only_drop_parts = 1, index_granularity = 8192;

CREATE TABLE IF NOT EXISTS rollup_spans_narrow_1h ON CLUSTER uptrace_all
    AS rollup_spans_narrow_1h_local
ENGINE = Distributed(uptrace_all, currentDatabase(), rollup_spans_narrow_1h_local, rand());

CREATE MATERIALIZED VIEW IF NOT EXISTS mv_rollup_spans_narrow_1h ON CLUSTER uptrace_all
TO rollup_spans_narrow_1h_local
AS SELECT
    toStartOfInterval(ts, INTERVAL 1 HOUR)                         AS ts,
    service_name, span_kind, status_code,
    sum(span_count)                                                AS span_count,
    sum(error_count)                                               AS error_count,
    sum(duration_sum)                                              AS duration_sum,
    quantilesTDigestMergeState(0.5, 0.95, 0.99)(q_state)           AS q_state,
    argMaxMergeState(slow_exemplar)                                AS slow_exemplar,
    anyIfMergeState(err_exemplar)                                  AS err_exemplar
FROM rollup_spans_narrow_5m_local
GROUP BY ts, service_name, span_kind, status_code;
