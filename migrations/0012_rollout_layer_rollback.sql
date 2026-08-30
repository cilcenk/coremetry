-- 0012_rollout_layer_rollback.sql — 0012'yi geri alır. OPERATÖR UYGULAR.
-- v0.10.197. Veri katmanı geri alımı; yüzey (reconciler/uçlar/UI) ayarla
-- kapanır (system_settings["rollouts"].enabled=false), bu dosya ingest
-- maliyetini de kaldırır. Kaybolan veri: rollout tarihçesi (workload_rollouts)
-- ve MV — span'ler DOKUNULMAZ.
--
-- SYNC: Atomic DB'de tembel DROP (8 dk) Replicated znode'unu bırakır ve
-- yeniden CREATE 253 REPLICA_ALREADY_EXISTS verir (lokalde ölçüldü).
-- SIRA ZORUNLU: önce MV + sarmalayıcı (kaynak INSERT trigger'ı), sonra
-- state tabloları, sonra index'ler, EN SON kolonlar — indeksi duran kolonu
-- düşürmek Code 47 ile reddedilir (v0.9.623 ölçümü).
-- idx_k8s_namespace'i 0012 YARATIR → burada düşer (inceleme S7: kolonu 0011
-- sahiplenir ama indeksi duran kolonu 0011 rollback'i düşüremezdi, Code 47);
-- 0011 rollback'i de IF EXISTS ile düşürür — sıra bağımsız.

DROP TABLE IF EXISTS workload_revision_activity_1m ON CLUSTER uptrace_all SYNC;
DROP TABLE IF EXISTS workload_revision_activity_1m_local ON CLUSTER uptrace_all SYNC;

DROP TABLE IF EXISTS rollout_reconcile_runs ON CLUSTER uptrace_all SYNC;
DROP TABLE IF EXISTS workload_rollouts ON CLUSTER uptrace_all SYNC;

ALTER TABLE spans_local ON CLUSTER uptrace_all DROP INDEX IF EXISTS idx_container_image_tag;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP INDEX IF EXISTS idx_container_image;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP INDEX IF EXISTS idx_k8s_replicaset;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP INDEX IF EXISTS idx_k8s_namespace;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP INDEX IF EXISTS idx_k8s_daemonset;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP INDEX IF EXISTS idx_k8s_statefulset;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP INDEX IF EXISTS idx_k8s_deployment;

ALTER TABLE spans ON CLUSTER uptrace_all DROP COLUMN IF EXISTS container_image_tag;
ALTER TABLE spans ON CLUSTER uptrace_all DROP COLUMN IF EXISTS container_image;
ALTER TABLE spans ON CLUSTER uptrace_all DROP COLUMN IF EXISTS k8s_replicaset;
ALTER TABLE spans ON CLUSTER uptrace_all DROP COLUMN IF EXISTS k8s_daemonset;
ALTER TABLE spans ON CLUSTER uptrace_all DROP COLUMN IF EXISTS k8s_statefulset;
ALTER TABLE spans ON CLUSTER uptrace_all DROP COLUMN IF EXISTS k8s_deployment;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP COLUMN IF EXISTS container_image_tag;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP COLUMN IF EXISTS container_image;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP COLUMN IF EXISTS k8s_replicaset;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP COLUMN IF EXISTS k8s_daemonset;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP COLUMN IF EXISTS k8s_statefulset;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP COLUMN IF EXISTS k8s_deployment;
