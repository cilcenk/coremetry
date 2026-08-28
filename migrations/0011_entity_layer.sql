-- 0011_entity_layer.sql — K8s ENTITY KATMANI şeması (dış Distributed prod).
-- v0.10.127. Boot ASLA koşmaz (ev kuralı: ON CLUSTER DDL'i N pod yarıştırırsa
-- kuyruk tıkanır — v0.9.613). v0.10.134: Admin → ClickHouse → "K8s entity
-- katmanı şeması (0011)" sihirbazı bu dosyayı GÖMÜLÜ olarak, operatörün
-- tıklamasıyla, ifade ifade uygular (uptrace_all token'ı gerçek küme adıyla);
-- elle koşmak da geçerli. Kolon/index/tablo/MV durumu host başına kartta.
--
-- ============================================================
-- GEREKÇE
-- ============================================================
-- Tasarım: docs/plans/entity-layer-design-2026-08-28.md §2. Uygulama aynı
-- nesneleri KENDİ yönettiği kurulumlarda boot'ta kurar (store.go tables
-- dilimi + promoted_attr.go + entity_schema.go). DIŞ Distributed prod'da
-- (v0.8.185/186 dersleri) uygulama spans_local'a ALTER gönderemez ve
-- k8s_pod kolonu yoksa entity_seen MV'lerini BİLİNÇLİ ATLAR — kolon + MV
-- sahipliği bu dosyada, OPERATÖRDE. Tanımlar store.go ile BİREBİR aynı
-- tutuldu: app-managed ve operatör-managed kurulumlar arasında şema
-- sapması olmasın.
--
-- ============================================================
-- ÖN KOŞUL — ŞEMA DOĞRULAMA
-- ============================================================
--   SELECT cluster, count() FROM system.clusters GROUP BY cluster;
--     → uptrace_all 4 host (2 shard × 2 replica) beklenir; farklıysa
--       aşağıdaki `uptrace_all` token'ını düzenle.
--   SELECT name FROM system.columns WHERE table='spans_local' AND name='cluster';
--     → BOŞSA önce ADIM 1'deki `cluster` satırını da aç (uygulamanın
--       MATERIALIZED cluster kolonu; ifade repo.go clusterDeriveExpr).
--   SELECT uniq(res_values[indexOf(res_keys,'k8s.pod.name')]) FROM spans
--   WHERE time > now() - INTERVAL 1 DAY;
--     → LowCardinality kapısı: > ~100k dönerse k8s_pod'u düz String yap
--       (ev kuralı C3: ölçmeden LC yapma).
--
-- ============================================================
-- UYGULAMA SIRASI — SIRA KRİTİK (0005 dersi: kırık MV trigger'ı kaynak
-- INSERT'i de düşürür ve TÜM ingest'i keser)
-- ============================================================
--   1. Terfi kolonlarını her shard'ın spans_local'ına ekle (ON CLUSTER).
--   2. Aynı kolonları Distributed sarmalayıcı spans'a ekle.
--   3. Kolonların her shard'da çözüldüğünü DOĞRULA (ADIM 3 sorgusu).
--   4. Skip index'ler.
--   5. State tabloları (tek replikasyon grubu, 0009 şekli).
--   6. ANCAK ondan sonra entity_seen MV'leri (_local + Distributed).
-- Geri alma: 0011_entity_layer_rollback.sql (DROP INDEX → DROP COLUMN
-- sırası zorunlu — Code 47).

-- ── ADIM 1: spans_local terfi kolonları ────────────────────────────────
-- MATERIALIZED: INSERT anında hesaplanır; eski part'lar okuma anında
-- hesaplar — backfill GEREKMEZ. Yeni part'lar kolon+index taşır.
ALTER TABLE spans_local ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS k8s_namespace LowCardinality(String)
  MATERIALIZED coalesce(nullIf(res_values[indexOf(res_keys, 'k8s.namespace.name')], ''), nullIf(res_values[indexOf(res_keys, 'kubernetes.namespace.name')], ''), '');
ALTER TABLE spans_local ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS k8s_pod LowCardinality(String)
  MATERIALIZED coalesce(nullIf(res_values[indexOf(res_keys, 'k8s.pod.name')], ''), nullIf(host_name, ''), '');
ALTER TABLE spans_local ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS k8s_node LowCardinality(String)
  MATERIALIZED coalesce(nullIf(res_values[indexOf(res_keys, 'k8s.node.name')], ''), '');

-- ── ADIM 2: Distributed sarmalayıcı (SELECT çözümlemesi için) ──────────
ALTER TABLE spans ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS k8s_namespace LowCardinality(String)
  MATERIALIZED coalesce(nullIf(res_values[indexOf(res_keys, 'k8s.namespace.name')], ''), nullIf(res_values[indexOf(res_keys, 'kubernetes.namespace.name')], ''), '');
ALTER TABLE spans ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS k8s_pod LowCardinality(String)
  MATERIALIZED coalesce(nullIf(res_values[indexOf(res_keys, 'k8s.pod.name')], ''), nullIf(host_name, ''), '');
ALTER TABLE spans ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS k8s_node LowCardinality(String)
  MATERIALIZED coalesce(nullIf(res_values[indexOf(res_keys, 'k8s.node.name')], ''), '');

-- ── ADIM 3: DOĞRULA (her host'ta üç kolon; kolon == dizi ifadesi) ──────
-- SELECT hostName(), name FROM clusterAllReplicas('uptrace_all', system.columns)
-- WHERE table='spans_local' AND name IN ('k8s_namespace','k8s_pod','k8s_node') ORDER BY 1,2;
-- SELECT countIf(k8s_pod != res_values[indexOf(res_keys,'k8s.pod.name')]) AS bad, count()
-- FROM spans WHERE time > now() - INTERVAL 10 MINUTE AND has(res_keys,'k8s.pod.name');
--   → bad = 0 olmalı.

-- ── ADIM 4: skip index'ler (yalnız YENİ part'lara; MATERIALIZE INDEX YOK) ──
ALTER TABLE spans_local ON CLUSTER uptrace_all ADD INDEX IF NOT EXISTS idx_k8s_pod  k8s_pod  TYPE set(0) GRANULARITY 4;
ALTER TABLE spans_local ON CLUSTER uptrace_all ADD INDEX IF NOT EXISTS idx_k8s_node k8s_node TYPE set(0) GRANULARITY 4;

-- ── ADIM 5: state tabloları — TEK replikasyon grubu (0009 şekli) ───────
CREATE TABLE IF NOT EXISTS entities ON CLUSTER uptrace_all (
  entity_type  LowCardinality(String),
  cluster_id   LowCardinality(String),
  entity_id    String,
  valid_from   DateTime,
  valid_to     DateTime DEFAULT 0,
  namespace    LowCardinality(String) DEFAULT '',
  name         String,
  uid          String DEFAULT '',
  parent_id    String DEFAULT '',
  label_keys   Array(LowCardinality(String)),
  label_values Array(String),
  source       LowCardinality(String),
  first_seen   DateTime,
  last_seen    DateTime,
  stale        UInt8 DEFAULT 0,
  version      UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))
) ENGINE = ReplicatedReplacingMergeTree('/clickhouse/tables/state/entities', '{shard}-{replica}', version)
ORDER BY (entity_type, cluster_id, entity_id, valid_from)
TTL last_seen + INTERVAL 180 DAY;

CREATE TABLE IF NOT EXISTS entity_relations ON CLUSTER uptrace_all (
  rel_type   LowCardinality(String),
  cluster_id LowCardinality(String),
  parent_id  String,
  child_id   String,
  valid_from DateTime,
  valid_to   DateTime DEFAULT 0,
  last_seen  DateTime,
  source     LowCardinality(String),
  version    UInt64 DEFAULT toUnixTimestamp64Nano(now64(9)),
  INDEX idx_child child_id TYPE bloom_filter(0.01) GRANULARITY 4
) ENGINE = ReplicatedReplacingMergeTree('/clickhouse/tables/state/entity_relations', '{shard}-{replica}', version)
ORDER BY (rel_type, cluster_id, parent_id, child_id, valid_from)
TTL last_seen + INTERVAL 180 DAY;

CREATE TABLE IF NOT EXISTS entity_sync_runs ON CLUSTER uptrace_all (
  cluster_id        LowCardinality(String),
  started_at        DateTime,
  finished_at       DateTime,
  status            LowCardinality(String),
  entities_written  UInt32 DEFAULT 0,
  relations_written UInt32 DEFAULT 0,
  closed            UInt32 DEFAULT 0,
  unmapped_keys     Array(String),
  unmapped_counts   Array(UInt32),
  thanos_ms         UInt32 DEFAULT 0,
  ch_ms             UInt32 DEFAULT 0,
  error             String DEFAULT '',
  version           UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))
) ENGINE = ReplicatedReplacingMergeTree('/clickhouse/tables/state/entity_sync_runs', '{shard}-{replica}', version)
PARTITION BY toYYYYMM(started_at)
ORDER BY (cluster_id, started_at)
TTL started_at + INTERVAL 30 DAY;

-- ── ADIM 6: entity_seen MV'leri — _local'a bağlı, spans_local'dan okur ──
-- (uygulamanın combined MV'sinin adaptDDL çıktısıyla aynı adlar:
--  <ad>_local MV + <ad> Distributed sarmalayıcı; Go okuyucu `FROM entity_seen_5m`.)
CREATE MATERIALIZED VIEW IF NOT EXISTS entity_seen_1m_local ON CLUSTER uptrace_all
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/entity_seen_1m', '{replica}')
PARTITION BY toDate(time_bucket)
ORDER BY (service_name, cluster, k8s_namespace, k8s_pod, time_bucket)
TTL toDate(time_bucket) + INTERVAL 3 DAY
SETTINGS index_granularity = 8192
AS SELECT
  toStartOfInterval(time, INTERVAL 1 MINUTE)  AS time_bucket,
  service_name, cluster, k8s_namespace, k8s_pod,
  anyLastSimpleState(k8s_node)                AS k8s_node,
  countState()                                AS span_count_state,
  countIfState(status_code = 'error')         AS error_count_state,
  sumState(duration)                          AS duration_sum_state,
  minState(time)                              AS first_seen_state,
  maxState(time)                              AS last_seen_state
FROM spans_local
WHERE k8s_pod != ''
GROUP BY time_bucket, service_name, cluster, k8s_namespace, k8s_pod;

CREATE TABLE IF NOT EXISTS entity_seen_1m ON CLUSTER uptrace_all AS entity_seen_1m_local
ENGINE = Distributed(uptrace_all, currentDatabase(), entity_seen_1m_local, cityHash64(service_name));

CREATE MATERIALIZED VIEW IF NOT EXISTS entity_seen_5m_local ON CLUSTER uptrace_all
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/entity_seen_5m', '{replica}')
PARTITION BY toDate(time_bucket)
ORDER BY (service_name, cluster, k8s_namespace, k8s_pod, time_bucket)
TTL toDate(time_bucket) + INTERVAL 30 DAY
SETTINGS index_granularity = 8192
AS SELECT
  toStartOfInterval(time, INTERVAL 5 MINUTE)  AS time_bucket,
  service_name, cluster, k8s_namespace, k8s_pod,
  anyLastSimpleState(k8s_node)                AS k8s_node,
  countState()                                AS span_count_state,
  countIfState(status_code = 'error')         AS error_count_state,
  sumState(duration)                          AS duration_sum_state,
  minState(time)                              AS first_seen_state,
  maxState(time)                              AS last_seen_state
FROM spans_local
WHERE k8s_pod != ''
GROUP BY time_bucket, service_name, cluster, k8s_namespace, k8s_pod;

CREATE TABLE IF NOT EXISTS entity_seen_5m ON CLUSTER uptrace_all AS entity_seen_5m_local
ENGINE = Distributed(uptrace_all, currentDatabase(), entity_seen_5m_local, cityHash64(service_name));

-- ── DOĞRULAMA ──────────────────────────────────────────────────────────
-- SELECT count() FROM entity_seen_5m WHERE time_bucket > now() - INTERVAL 10 MINUTE;
--   → > 0 (k8s.pod.name taşıyan span geliyorsa). 0 ise ADIM 3'ü tekrar bak.
