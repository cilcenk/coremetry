-- 0012_rollout_layer.sql — ROLLOUTS veri katmanı şeması (dış Distributed prod).
-- v0.10.197 (docs/audits/rollouts-audit.md §1, §5 — operatör onayı 2026-08-30).
-- Boot ASLA koşmaz (ev kuralı: ON CLUSTER DDL'i N pod yarıştırırsa kuyruk
-- tıkanır — v0.9.613). Admin → ClickHouse → "Rollouts katmanı şeması (0012)"
-- sihirbazı bu dosyayı GÖMÜLÜ olarak, operatörün tıklamasıyla, ifade ifade
-- uygular (uptrace_all token'ı gerçek küme adıyla); elle koşmak da geçerli.
--
-- ============================================================
-- GEREKÇE
-- ============================================================
-- Uygulama aynı nesneleri KENDİ yönettiği kurulumlarda boot'ta kurar
-- (promoted_attr.go 6 terfi kolonu, store.go tables dilimi, rollout_schema.go
-- MV). DIŞ Distributed prod'da (v0.8.185/186 dersleri) uygulama spans_local'a
-- ALTER gönderemez ve k8s_replicaset kolonu yoksa MV'yi BİLİNÇLİ ATLAR —
-- kolon + MV sahipliği bu dosyada, OPERATÖRDE. Tanımlar store.go ile
-- BİREBİR aynı (0011 sözleşmesi): app-managed ve operatör-managed
-- kurulumlar arasında şema sapması olmasın.
--
-- ============================================================
-- ÖN KOŞUL — ŞEMA DOĞRULAMA
-- ============================================================
--   SELECT cluster, count() FROM system.clusters GROUP BY cluster;
--     → uptrace_all 4 host (2 shard × 2 replica) beklenir; farklıysa
--       aşağıdaki `uptrace_all` token'ını düzenle.
--   SELECT name FROM system.columns WHERE table='spans_local'
--     AND name IN ('cluster','k8s_namespace','k8s_pod','k8s_node');
--     → 0011 ADIM 1 uygulanmış olmalı (MV cluster + k8s_namespace okur).
--   SELECT uniq(res_values[indexOf(res_keys,'container.image.name')]),
--          uniq(res_values[indexOf(res_keys,'k8s.replicaset.name')])
--   FROM spans WHERE time > now() - INTERVAL 1 DAY;
--     → LowCardinality kapısı (ev kuralı C3): > ~100k dönen kolonu düz
--       String yap (bu dosyada ve store.go'da birlikte).
--   Admin → System → K8s Coverage (v0.10.192): CLUSTER BAŞINA
--     replicaset ≥ %95, image ≥ %95 — 2026-08-30'da bir cluster'ın
--     namespace bile basmadığı görüldü; kapsama sağlanmadan ADIM 6 (MV)
--     UYGULANMAZ (boş MV yanlış değil ama işe yaramaz — 0007 sınıfı).
--
-- ============================================================
-- UYGULAMA SIRASI — SIRA KRİTİK (0005 dersi: kırık MV trigger'ı kaynak
-- INSERT'i de düşürür ve TÜM ingest'i keser)
-- ============================================================
--   1. Altı terfi kolonunu her shard'ın spans_local'ına ekle (ON CLUSTER).
--   2. Aynı kolonları Distributed sarmalayıcı spans'a ekle.
--   3. Kolonların her shard'da çözüldüğünü DOĞRULA (ADIM 3 sorgusu).
--   4. Skip index'ler (+ 0011'in atladığı idx_k8s_namespace).
--   5. State tabloları (tek replikasyon grubu, 0009 şekli).
--   6. ANCAK ondan sonra ve kapsama kapısı geçince MV (_local + Distributed).
-- Geri alma: 0012_rollout_layer_rollback.sql (MV → tablo → INDEX → COLUMN;
-- indeksi duran kolonu düşürmek Code 47).

-- ── ADIM 1: spans_local terfi kolonları ────────────────────────────────
-- MATERIALIZED: INSERT anında hesaplanır; eski part'lar okuma anında
-- hesaplar — backfill GEREKMEZ. Distributed forward'ında silinir → ingest
-- kıramaz (CH PR #7377; DEFAULT olsaydı kırardı — v0.8.186).
ALTER TABLE spans_local ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS k8s_deployment LowCardinality(String)
  MATERIALIZED coalesce(nullIf(res_values[indexOf(res_keys, 'k8s.deployment.name')], ''), '');
ALTER TABLE spans_local ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS k8s_statefulset LowCardinality(String)
  MATERIALIZED coalesce(nullIf(res_values[indexOf(res_keys, 'k8s.statefulset.name')], ''), '');
ALTER TABLE spans_local ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS k8s_daemonset LowCardinality(String)
  MATERIALIZED coalesce(nullIf(res_values[indexOf(res_keys, 'k8s.daemonset.name')], ''), '');
ALTER TABLE spans_local ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS k8s_replicaset LowCardinality(String)
  MATERIALIZED coalesce(nullIf(res_values[indexOf(res_keys, 'k8s.replicaset.name')], ''), '');
ALTER TABLE spans_local ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS container_image LowCardinality(String)
  MATERIALIZED coalesce(nullIf(res_values[indexOf(res_keys, 'container.image.name')], ''), nullIf(res_values[indexOf(res_keys, 'k8s.container.image.name')], ''), '');
ALTER TABLE spans_local ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS container_image_tag LowCardinality(String)
  MATERIALIZED coalesce(nullIf(res_values[indexOf(res_keys, 'container.image.tag')], ''), nullIf(res_values[indexOf(res_keys, 'k8s.container.image.tag')], ''), '');

-- ── ADIM 2: Distributed sarmalayıcı (SELECT çözümlemesi için) ──────────
ALTER TABLE spans ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS k8s_deployment LowCardinality(String)
  MATERIALIZED coalesce(nullIf(res_values[indexOf(res_keys, 'k8s.deployment.name')], ''), '');
ALTER TABLE spans ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS k8s_statefulset LowCardinality(String)
  MATERIALIZED coalesce(nullIf(res_values[indexOf(res_keys, 'k8s.statefulset.name')], ''), '');
ALTER TABLE spans ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS k8s_daemonset LowCardinality(String)
  MATERIALIZED coalesce(nullIf(res_values[indexOf(res_keys, 'k8s.daemonset.name')], ''), '');
ALTER TABLE spans ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS k8s_replicaset LowCardinality(String)
  MATERIALIZED coalesce(nullIf(res_values[indexOf(res_keys, 'k8s.replicaset.name')], ''), '');
ALTER TABLE spans ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS container_image LowCardinality(String)
  MATERIALIZED coalesce(nullIf(res_values[indexOf(res_keys, 'container.image.name')], ''), nullIf(res_values[indexOf(res_keys, 'k8s.container.image.name')], ''), '');
ALTER TABLE spans ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS container_image_tag LowCardinality(String)
  MATERIALIZED coalesce(nullIf(res_values[indexOf(res_keys, 'container.image.tag')], ''), nullIf(res_values[indexOf(res_keys, 'k8s.container.image.tag')], ''), '');

-- ── ADIM 3: DOĞRULA (her host'ta altı kolon; kolon == dizi ifadesi) ─────
-- SELECT hostName(), name FROM clusterAllReplicas('uptrace_all', system.columns)
-- WHERE table='spans_local' AND name IN ('k8s_deployment','k8s_statefulset','k8s_daemonset','k8s_replicaset','container_image','container_image_tag') ORDER BY 1,2;
-- SELECT countIf(k8s_replicaset != res_values[indexOf(res_keys,'k8s.replicaset.name')]) AS bad, count()
-- FROM spans WHERE time > now() - INTERVAL 10 MINUTE AND has(res_keys,'k8s.replicaset.name');
--   → bad = 0 olmalı.

-- ── ADIM 4: skip index'ler (yalnız YENİ part'lara; MATERIALIZE INDEX YOK) ──
ALTER TABLE spans_local ON CLUSTER uptrace_all ADD INDEX IF NOT EXISTS idx_k8s_namespace       k8s_namespace       TYPE set(0) GRANULARITY 4;
ALTER TABLE spans_local ON CLUSTER uptrace_all ADD INDEX IF NOT EXISTS idx_k8s_deployment      k8s_deployment      TYPE set(0) GRANULARITY 4;
ALTER TABLE spans_local ON CLUSTER uptrace_all ADD INDEX IF NOT EXISTS idx_k8s_statefulset     k8s_statefulset     TYPE set(0) GRANULARITY 4;
ALTER TABLE spans_local ON CLUSTER uptrace_all ADD INDEX IF NOT EXISTS idx_k8s_daemonset       k8s_daemonset       TYPE set(0) GRANULARITY 4;
ALTER TABLE spans_local ON CLUSTER uptrace_all ADD INDEX IF NOT EXISTS idx_k8s_replicaset      k8s_replicaset      TYPE set(0) GRANULARITY 4;
ALTER TABLE spans_local ON CLUSTER uptrace_all ADD INDEX IF NOT EXISTS idx_container_image     container_image     TYPE set(0) GRANULARITY 4;
ALTER TABLE spans_local ON CLUSTER uptrace_all ADD INDEX IF NOT EXISTS idx_container_image_tag container_image_tag TYPE set(0) GRANULARITY 4;

-- ── ADIM 5: state tabloları — TEK replikasyon grubu (0009 şekli) ───────
-- started_at DONUK ve deterministik (aktif kümeye giren ilk 5 dk kovanın
-- başı); PARTITION YOK (Kural P1); RMT(version) + FINAL; TTL 180 gün.
CREATE TABLE IF NOT EXISTS workload_rollouts ON CLUSTER uptrace_all (
  cluster_id           LowCardinality(String),
  namespace            LowCardinality(String),
  workload             String,
  workload_kind        LowCardinality(String),
  revision             String,
  started_at           DateTime64(3),
  status               LowCardinality(String),
  prev_revision        String        DEFAULT '',
  image                String        DEFAULT '',
  image_tag            String        DEFAULT '',
  prev_image           String        DEFAULT '',
  prev_image_tag       String        DEFAULT '',
  first_span_at        DateTime64(3) DEFAULT 0,
  traffic_confirmed_at DateTime64(3) DEFAULT 0,
  ksm_started_at       DateTime64(3) DEFAULT 0,
  pods_ready_at        DateTime64(3) DEFAULT 0,
  ksm_not_ready_since  DateTime64(3) DEFAULT 0,
  completed_at         DateTime64(3) DEFAULT 0,
  detected_by          LowCardinality(String),
  span_count           UInt64        DEFAULT 0,
  note                 String        DEFAULT '',
  updated_at           DateTime64(3) DEFAULT now64(3),
  version              UInt64        DEFAULT toUnixTimestamp64Nano(now64(9))
) ENGINE = ReplicatedReplacingMergeTree('/clickhouse/tables/state/workload_rollouts', '{shard}-{replica}', version)
ORDER BY (cluster_id, namespace, workload, revision, started_at)
TTL toDate(started_at) + INTERVAL 180 DAY;

CREATE TABLE IF NOT EXISTS rollout_reconcile_runs ON CLUSTER uptrace_all (
  started_at       DateTime64(3),
  host             LowCardinality(String) DEFAULT '',
  finished_at      DateTime64(3),
  status           LowCardinality(String),
  clusters         UInt16  DEFAULT 0,
  rollouts_written UInt32  DEFAULT 0,
  span_ms          UInt32  DEFAULT 0,
  ksm_ms           UInt32  DEFAULT 0,
  error            String  DEFAULT '',
  version          UInt64  DEFAULT toUnixTimestamp64Nano(now64(9))
) ENGINE = ReplicatedReplacingMergeTree('/clickhouse/tables/state/rollout_reconcile_runs', '{shard}-{replica}', version)
ORDER BY (started_at, host)
TTL toDate(started_at) + INTERVAL 30 DAY;

-- ── ADIM 6: MV — KAPSAMA KAPISI GEÇİNCE (cluster başına replicaset ≥ %95) ──
-- _local'a bağlı, spans_local'dan okur (uygulamanın combined MV'sinin
-- adaptDDL çıktısıyla aynı adlar; Go okuyucu `FROM workload_revision_activity_1m`).
-- Yalnız terfi kolonu okur — INSERT başına indexOf YOK.
CREATE MATERIALIZED VIEW IF NOT EXISTS workload_revision_activity_1m_local ON CLUSTER uptrace_all
ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/workload_revision_activity_1m', '{replica}')
PARTITION BY toDate(bucket)
ORDER BY (cluster, k8s_namespace, workload, revision, service_name, bucket)
TTL toDate(bucket) + INTERVAL 7 DAY
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1
AS SELECT
  toStartOfInterval(time, INTERVAL 1 MINUTE)                      AS bucket,
  cluster,
  k8s_namespace,
  multiIf(k8s_deployment != '', k8s_deployment,
          k8s_statefulset != '', k8s_statefulset,
          k8s_daemonset != '', k8s_daemonset, '')                  AS workload,
  anyLastSimpleState(multiIf(k8s_deployment != '', 'Deployment',
          k8s_statefulset != '', 'StatefulSet',
          k8s_daemonset != '', 'DaemonSet', ''))                   AS workload_kind,
  k8s_replicaset                                                  AS revision,
  service_name,
  anyLastSimpleState(container_image)                             AS image,
  anyLastSimpleState(container_image_tag)                         AS image_tag,
  countState()                                                    AS span_count_state,
  minState(time)                                                  AS first_seen_state,
  maxState(time)                                                  AS last_seen_state
FROM spans_local
WHERE k8s_replicaset != '' AND workload != ''
GROUP BY bucket, cluster, k8s_namespace, workload, revision, service_name;

CREATE TABLE IF NOT EXISTS workload_revision_activity_1m ON CLUSTER uptrace_all AS workload_revision_activity_1m_local
ENGINE = Distributed(uptrace_all, currentDatabase(), workload_revision_activity_1m_local, cityHash64(cluster, k8s_namespace, workload));

-- ── DOĞRULAMA ──────────────────────────────────────────────────────────
-- SELECT count() FROM workload_revision_activity_1m WHERE bucket > now() - INTERVAL 10 MINUTE;
--   → > 0 (k8s.replicaset.name taşıyan span geliyorsa). 0 ise ADIM 3'ü ve
--     K8s Coverage kartını (cluster başına) tekrar bak.
