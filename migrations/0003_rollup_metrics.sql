-- 0003_rollup_metrics.sql — Raw OTLP metrics için PARALEL ve AYRI rollup zinciri
-- Aşama 1 DDL (2026-07-30). Span rollup'larından bağımsız; metric_points'ten beslenir.
--
-- Kaynak şema (store.go:1238): metric_points(metric LC, instrument LC, unit LC,
-- service_name LC, host_name LC, time DateTime64(9), value Float64,
-- count/sum_value/min_value/max_value, bucket_bounds Array(Float64),
-- bucket_counts Array(UInt64), attr/res dizileri).
--
-- Tasarım notları:
-- · Gauge/sum enstrümanlar: sum+count+min+max+last — avg ve son-değer okuma
--   tarafında türetilir. tDigest YOK (metrik değeri zaten örneklenmiş seri;
--   percentile'ı anlamlı olan tek yer histogram bucket'ları).
-- · Histogram enstrümanlar: OTLP bucket sınırları AKIŞA GÖRE DEĞİŞKEN olduğu
--   için bucket_bounds GROUP BY anahtarına girer — sumForEach yalnız AYNI
--   sınır dizisine sahip satırlar arasında geçerlidir. Sınır değişimi (SDK
--   güncellemesi) yeni seri açar; okuma tarafı sınır setlerini ayrı ayrı
--   quantile'layıp birleştirir (docs/rollup-design.md §histogram).
-- · host_name bilinçli DIŞARIDA (pod başına seri patlaması); pod kırılımı
--   ham metric_points'te kalır (14-30 gün yeter — mevcut retention).
-- · Kaskad 1m → 5m → 1h; 10s tabanı YOK (OTLP export aralığı zaten ≥10s).

-- ─────────────────────────────── 1m taban ────────────────────────────────
CREATE TABLE IF NOT EXISTS rollup_metrics_1m_local ON CLUSTER uptrace_all
(
    ts            DateTime CODEC(Delta, ZSTD(1)),
    metric        LowCardinality(String),
    service_name  LowCardinality(String),
    instrument    LowCardinality(String),
    unit          LowCardinality(String),
    bucket_bounds Array(Float64),                                   -- histogram değilse []
    point_count   SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    value_sum     SimpleAggregateFunction(sum, Float64),
    value_min     SimpleAggregateFunction(min, Float64),
    value_max     SimpleAggregateFunction(max, Float64),
    last_value    AggregateFunction(argMax, Float64, DateTime64(9)),
    hist_counts   AggregateFunction(sumForEach, Array(UInt64))       -- histogram değilse []
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/rollup_metrics_1m', '{replica}')
PARTITION BY toDate(ts)
ORDER BY (metric, service_name, instrument, unit, bucket_bounds, ts)
TTL ts + INTERVAL 14 DAY
SETTINGS ttl_only_drop_parts = 1, index_granularity = 8192;

CREATE TABLE IF NOT EXISTS rollup_metrics_1m ON CLUSTER uptrace_all
    AS rollup_metrics_1m_local
ENGINE = Distributed(uptrace_all, currentDatabase(), rollup_metrics_1m_local, rand());

CREATE MATERIALIZED VIEW IF NOT EXISTS mv_rollup_metrics_1m ON CLUSTER uptrace_all
TO rollup_metrics_1m_local
AS SELECT
    toStartOfInterval(time, INTERVAL 1 MINUTE)     AS ts,
    metric, service_name, instrument, unit,
    bucket_bounds,
    toUInt64(count())                              AS point_count,
    sum(value)                                     AS value_sum,
    min(value)                                     AS value_min,
    max(value)                                     AS value_max,
    argMaxState(value, time)                       AS last_value,
    sumForEachState(bucket_counts)                 AS hist_counts
FROM metric_points_local
GROUP BY ts, metric, service_name, instrument, unit, bucket_bounds;

-- ─────────────────────────────── 5m kaskadı ──────────────────────────────
CREATE TABLE IF NOT EXISTS rollup_metrics_5m_local ON CLUSTER uptrace_all
(
    ts            DateTime CODEC(Delta, ZSTD(1)),
    metric        LowCardinality(String),
    service_name  LowCardinality(String),
    instrument    LowCardinality(String),
    unit          LowCardinality(String),
    bucket_bounds Array(Float64),
    point_count   SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    value_sum     SimpleAggregateFunction(sum, Float64),
    value_min     SimpleAggregateFunction(min, Float64),
    value_max     SimpleAggregateFunction(max, Float64),
    last_value    AggregateFunction(argMax, Float64, DateTime64(9)),
    hist_counts   AggregateFunction(sumForEach, Array(UInt64))
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/rollup_metrics_5m', '{replica}')
PARTITION BY toDate(ts)
ORDER BY (metric, service_name, instrument, unit, bucket_bounds, ts)
TTL ts + INTERVAL 90 DAY
SETTINGS ttl_only_drop_parts = 1, index_granularity = 8192;

CREATE TABLE IF NOT EXISTS rollup_metrics_5m ON CLUSTER uptrace_all
    AS rollup_metrics_5m_local
ENGINE = Distributed(uptrace_all, currentDatabase(), rollup_metrics_5m_local, rand());

CREATE MATERIALIZED VIEW IF NOT EXISTS mv_rollup_metrics_5m ON CLUSTER uptrace_all
TO rollup_metrics_5m_local
AS SELECT
    toStartOfInterval(ts, INTERVAL 5 MINUTE)       AS ts,
    metric, service_name, instrument, unit, bucket_bounds,
    sum(point_count)                               AS point_count,
    sum(value_sum)                                 AS value_sum,
    min(value_min)                                 AS value_min,
    max(value_max)                                 AS value_max,
    argMaxMergeState(last_value)                   AS last_value,
    sumForEachMergeState(hist_counts)              AS hist_counts
FROM rollup_metrics_1m_local
GROUP BY ts, metric, service_name, instrument, unit, bucket_bounds;

-- ─────────────────────────────── 1h kaskadı ──────────────────────────────
CREATE TABLE IF NOT EXISTS rollup_metrics_1h_local ON CLUSTER uptrace_all
(
    ts            DateTime CODEC(Delta, ZSTD(1)),
    metric        LowCardinality(String),
    service_name  LowCardinality(String),
    instrument    LowCardinality(String),
    unit          LowCardinality(String),
    bucket_bounds Array(Float64),
    point_count   SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    value_sum     SimpleAggregateFunction(sum, Float64),
    value_min     SimpleAggregateFunction(min, Float64),
    value_max     SimpleAggregateFunction(max, Float64),
    last_value    AggregateFunction(argMax, Float64, DateTime64(9)),
    hist_counts   AggregateFunction(sumForEach, Array(UInt64))
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/rollup_metrics_1h', '{replica}')
PARTITION BY toDate(ts)
ORDER BY (metric, service_name, instrument, unit, bucket_bounds, ts)
TTL ts + INTERVAL 13 MONTH
SETTINGS ttl_only_drop_parts = 1, index_granularity = 8192;

CREATE TABLE IF NOT EXISTS rollup_metrics_1h ON CLUSTER uptrace_all
    AS rollup_metrics_1h_local
ENGINE = Distributed(uptrace_all, currentDatabase(), rollup_metrics_1h_local, rand());

CREATE MATERIALIZED VIEW IF NOT EXISTS mv_rollup_metrics_1h ON CLUSTER uptrace_all
TO rollup_metrics_1h_local
AS SELECT
    toStartOfInterval(ts, INTERVAL 1 HOUR)         AS ts,
    metric, service_name, instrument, unit, bucket_bounds,
    sum(point_count)                               AS point_count,
    sum(value_sum)                                 AS value_sum,
    min(value_min)                                 AS value_min,
    max(value_max)                                 AS value_max,
    argMaxMergeState(last_value)                   AS last_value,
    sumForEachMergeState(hist_counts)              AS hist_counts
FROM rollup_metrics_5m_local
GROUP BY ts, metric, service_name, instrument, unit, bucket_bounds;
