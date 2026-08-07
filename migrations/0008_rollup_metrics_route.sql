-- 0008_rollup_metrics_route.sql — ROUTE (endpoint) kırılımlı metrik rollup zinciri.
-- 0003'ün KARDEŞİ, halefi değil: 0003 servis-geneli okur, bu dosya aynı
-- metriği `http.route` boyutuyla saklar. İkisi yan yana yaşar.
--
-- Neden var — RETANSİYON, hız değil: metric_points TTL 7 gün. "Bu endpoint'in
-- p-ortalaması geçen ay neydi" sorusu bugün CEVAPSIZ, yavaş değil. Bu zincir
-- 14g/90g/13ay taşıyor (0003 ile aynı kademe iskeleti).
-- Bağlam: docs/debt/percentile-unification.md "endpoint kırılımlı METRİK
-- rollup tier'ı YOK" başlığı — "0008 adayı" oradan geldi.
--
-- KAPSAM, açıkça: avg / sum / min / max. YÜZDELİK VAADİ YOK.
-- bucket_bounds + hist_counts BİLİNÇLİ OLARAK TAŞINMIYOR. Gerekçe: 0003'ün
-- histogram yolu bounds-set birleşimine dayanıyor ve prod'da bounds
-- kararsızlığı (SDK sürümüne göre değişen sınır dizileri) çözülmüş DEĞİL.
-- Route kırılımı seri sayısını route kadar çarpar; aynı kararsızlığı çarparak
-- taşımak "p95 var" diyip yanlış sayı göstermek olurdu. Yüzdelikler bu
-- zincirde YOK — ham yolda kalıyor.
--
-- Kaynak şema (store.go:1238 metric_points): metric LC, instrument LC, unit LC,
-- service_name LC, time DateTime64(9), value Float64, count/sum_value UInt64/
-- Float64, attr_keys/attr_values + res_keys/res_values dizileri.
--
-- ── Tasarım notları ───────────────────────────────────────────────────────
--
-- · service_key (service_name DEĞİL): prod kimlik tuzağı. k8s.deployment.name
--   varsa O kazanır, yoksa service_name'e düşer — channel_code v0.9.626
--   emsali. Aynı deployment'ın iki farklı service.name basması prod'da
--   görülmüş bir şey; rollup'ta iki ayrı seri olurlarsa panel ikiye bölünür.
--
-- · route NORMALİZE EDİLMİŞ olarak yazılır (ham path DEĞİL). Metrik attr'ı
--   ingest'te normalize EDİLMİYOR — otlp/convert.go:112-125 yalnız SPAN
--   yolunda NormalizeOperation koşuyor. Ham path'ler (/orders/8421,
--   /orders/8422, …) LowCardinality sözlüğünü patlatır: LC(String) 10k
--   ayrık değere kadar kazanç, ötesinde kayıp. Normalizasyon MV İÇİNDE,
--   çünkü yazım anında bir kez koşar; okuma tarafında yapmak her sorguda
--   milyonlarca satıra regex uygulamak olurdu.
--   Regexler endpoints.go OpSigReUUID/OpSigReHex/OpSigReNum sabitlerinin
--   BİREBİR kopyası ve sıra da aynı (UUID ÖNCE — sayı/hex kuralları onun
--   parçalarını yemesin). Ayrışırlarsa /endpoints ile bu tablo aynı
--   endpoint'i farklı adlandırır.
--
-- · route_raw ÇOKLU ANAHTAR: `http.route` (semconv), `url.template` (yeni
--   semconv), `http_route` (eski/kirli SDK'lar). metricEnvExpr (filterexpr.go
--   :380) emsali — tek anahtar aramak, o anahtarı basmayan SDK'nın verisini
--   sessizce yok sayar (v0.9.52 dersi).
--
-- · `WHERE route_raw != ''` — YALNIZ route taşıyan satırlar. Lokalde
--   metric_points'in ~%1'i; MV yazım maliyeti bu oranla sınırlı (0005'in
--   +%4-8 kestirimi referans üst sınır).
--
-- · obs_count + sum_value_sum (v0.9.776 avg düzeltmesinin şeması):
--   histogram enstrümanda `value` per-export ÖZET'tir, `avg(value)`
--   ortalamaların ortalaması olur. Doğrusu gözlem-ağırlıklı:
--   sum(sum_value)/sum(count). Bu iki kolon o iki toplamı taşıyor.
--   value_sum/point_count ise gauge/sum enstrümanların avg'i için —
--   orada `value` ölçümün KENDİSİ ve sum_value/count 0'dır.
--   İkisi ayrı kolon, çünkü okuyucu enstrümana göre farklı çifti seçiyor.
--
-- · host_name / pod kırılımı YOK (0003 ile aynı karar): pod başına seri
--   patlaması. Pod kırılımı ham metric_points'te kalıyor.
--
-- · ORDER BY (metric, service_key, route, instrument, unit, ts) —
--   ⚠ instrument+unit ORDER BY'da olmak ZORUNDA. AggregatingMergeTree
--   birleştirmeyi ORDER BY anahtarı üzerinden yapar; bu iki kolon anahtarda
--   olmasaydı aynı (metric, service_key, route, ts) için 'ms' ve 's'
--   satırları TEK satıra çöker, toplamlar birbirine karışır ve kalan
--   instrument/unit değeri rastgele olurdu — okuyucunun `unit = ?` filtresi
--   de anlamsızlaşırdı. 0003 aynı nedenle ikisini anahtarında taşıyor;
--   /clickhouse-schema §3: "MV hedef tablosunda ORDER BY, MV'nin GROUP
--   BY'ını yansıtır". Okuma öneki (metric, service_key) bozulmadan korunuyor.
--
-- · Kaskad 1m → 5m → 1h; 10s tabanı YOK (OTLP export aralığı zaten ≥10s).
--
-- · SATIR KIRILIMI BİLİNÇLİ (v0.8.356 dersi): clickhouse-go v2, sorgu metni
--   `{.+:.+}` ile eşleşir VE arg verilmişse sunucu-taraflı parametre moduna
--   geçer. Regex nicelik parantezleri (`{8}`) ile ':id' yer tutucusu AYNI
--   satıra düşerse o kalıp oluşur. DDL args'sız koşuyor (RollupApply →
--   conn.Exec(ctx, stmt)) yani bugün zararsız, ama kalıbı satır satır
--   ayrık tutmak bedavaya sigorta.

-- ─────────────────────────────── 1m taban ────────────────────────────────
CREATE TABLE IF NOT EXISTS rollup_metrics_route_1m_local ON CLUSTER uptrace_all
(
    ts            DateTime CODEC(Delta, ZSTD(1)),
    metric        LowCardinality(String),
    service_key   LowCardinality(String),
    route         LowCardinality(String),
    instrument    LowCardinality(String),
    unit          LowCardinality(String),
    point_count   SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    obs_count     SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    sum_value_sum SimpleAggregateFunction(sum, Float64),
    value_sum     SimpleAggregateFunction(sum, Float64),
    value_min     SimpleAggregateFunction(min, Float64),
    value_max     SimpleAggregateFunction(max, Float64)
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/rollup_metrics_route_1m', '{replica}')
PARTITION BY toDate(ts)
ORDER BY (metric, service_key, route, instrument, unit, ts)
TTL ts + INTERVAL 14 DAY
SETTINGS ttl_only_drop_parts = 1, index_granularity = 8192;

CREATE TABLE IF NOT EXISTS rollup_metrics_route_1m ON CLUSTER uptrace_all
    AS rollup_metrics_route_1m_local
ENGINE = Distributed(uptrace_all, currentDatabase(), rollup_metrics_route_1m_local, rand());

CREATE MATERIALIZED VIEW IF NOT EXISTS mv_rollup_metrics_route_1m ON CLUSTER uptrace_all
TO rollup_metrics_route_1m_local
AS WITH coalesce(
        nullIf(attr_values[indexOf(attr_keys, 'http.route')], ''),
        nullIf(attr_values[indexOf(attr_keys, 'url.template')], ''),
        nullIf(attr_values[indexOf(attr_keys, 'http_route')], ''),
        '') AS route_raw
SELECT
    toStartOfInterval(time, INTERVAL 1 MINUTE)                  AS ts,
    metric,
    toLowCardinality(coalesce(
        nullIf(res_values[indexOf(res_keys, 'k8s.deployment.name')], ''),
        service_name))                                          AS service_key,
    toLowCardinality(replaceRegexpAll(replaceRegexpAll(replaceRegexpAll(
        route_raw,
        '[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}',
        ':id'),
        '/[0-9a-fA-F]{16,}',
        '/:id'),
        '/[0-9]+',
        '/:id'))                                                AS route,
    instrument, unit,
    toUInt64(count())                                           AS point_count,
    toUInt64(sum(count))                                        AS obs_count,
    sum(sum_value)                                              AS sum_value_sum,
    sum(value)                                                  AS value_sum,
    min(value)                                                  AS value_min,
    max(value)                                                  AS value_max
FROM metric_points_local
WHERE route_raw != ''
GROUP BY ts, metric, service_key, route, instrument, unit;

-- ─────────────────────────────── 5m kaskadı ──────────────────────────────
CREATE TABLE IF NOT EXISTS rollup_metrics_route_5m_local ON CLUSTER uptrace_all
(
    ts            DateTime CODEC(Delta, ZSTD(1)),
    metric        LowCardinality(String),
    service_key   LowCardinality(String),
    route         LowCardinality(String),
    instrument    LowCardinality(String),
    unit          LowCardinality(String),
    point_count   SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    obs_count     SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    sum_value_sum SimpleAggregateFunction(sum, Float64),
    value_sum     SimpleAggregateFunction(sum, Float64),
    value_min     SimpleAggregateFunction(min, Float64),
    value_max     SimpleAggregateFunction(max, Float64)
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/rollup_metrics_route_5m', '{replica}')
PARTITION BY toDate(ts)
ORDER BY (metric, service_key, route, instrument, unit, ts)
TTL ts + INTERVAL 90 DAY
SETTINGS ttl_only_drop_parts = 1, index_granularity = 8192;

CREATE TABLE IF NOT EXISTS rollup_metrics_route_5m ON CLUSTER uptrace_all
    AS rollup_metrics_route_5m_local
ENGINE = Distributed(uptrace_all, currentDatabase(), rollup_metrics_route_5m_local, rand());

CREATE MATERIALIZED VIEW IF NOT EXISTS mv_rollup_metrics_route_5m ON CLUSTER uptrace_all
TO rollup_metrics_route_5m_local
AS SELECT
    toStartOfInterval(ts, INTERVAL 5 MINUTE)       AS ts,
    metric, service_key, route, instrument, unit,
    sum(point_count)                               AS point_count,
    sum(obs_count)                                 AS obs_count,
    sum(sum_value_sum)                             AS sum_value_sum,
    sum(value_sum)                                 AS value_sum,
    min(value_min)                                 AS value_min,
    max(value_max)                                 AS value_max
FROM rollup_metrics_route_1m_local
GROUP BY ts, metric, service_key, route, instrument, unit;

-- ─────────────────────────────── 1h kaskadı ──────────────────────────────
CREATE TABLE IF NOT EXISTS rollup_metrics_route_1h_local ON CLUSTER uptrace_all
(
    ts            DateTime CODEC(Delta, ZSTD(1)),
    metric        LowCardinality(String),
    service_key   LowCardinality(String),
    route         LowCardinality(String),
    instrument    LowCardinality(String),
    unit          LowCardinality(String),
    point_count   SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    obs_count     SimpleAggregateFunction(sum, UInt64) CODEC(T64, ZSTD(1)),
    sum_value_sum SimpleAggregateFunction(sum, Float64),
    value_sum     SimpleAggregateFunction(sum, Float64),
    value_min     SimpleAggregateFunction(min, Float64),
    value_max     SimpleAggregateFunction(max, Float64)
)
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/rollup_metrics_route_1h', '{replica}')
PARTITION BY toDate(ts)
ORDER BY (metric, service_key, route, instrument, unit, ts)
TTL ts + INTERVAL 13 MONTH
SETTINGS ttl_only_drop_parts = 1, index_granularity = 8192;

CREATE TABLE IF NOT EXISTS rollup_metrics_route_1h ON CLUSTER uptrace_all
    AS rollup_metrics_route_1h_local
ENGINE = Distributed(uptrace_all, currentDatabase(), rollup_metrics_route_1h_local, rand());

CREATE MATERIALIZED VIEW IF NOT EXISTS mv_rollup_metrics_route_1h ON CLUSTER uptrace_all
TO rollup_metrics_route_1h_local
AS SELECT
    toStartOfInterval(ts, INTERVAL 1 HOUR)         AS ts,
    metric, service_key, route, instrument, unit,
    sum(point_count)                               AS point_count,
    sum(obs_count)                                 AS obs_count,
    sum(sum_value_sum)                             AS sum_value_sum,
    sum(value_sum)                                 AS value_sum,
    min(value_min)                                 AS value_min,
    max(value_max)                                 AS value_max
FROM rollup_metrics_route_5m_local
GROUP BY ts, metric, service_key, route, instrument, unit;
