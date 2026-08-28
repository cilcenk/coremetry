-- 0011_entity_layer_rollback.sql — 0011'i geri alır. OPERATÖR UYGULAR.
-- v0.10.127. Veri katmanı geri alımı; yüzey (syncer/uçlar/UI) bayrakla
-- kapanır (system_settings["entity_layer"].enabled=false), bu dosya
-- ingest maliyetini de kaldırır.
--
-- SYNC: Atomic DB'de tembel DROP (8 dk) Replicated znode'unu bırakır ve
-- yeniden CREATE 253 REPLICA_ALREADY_EXISTS verir (lokalde ölçüldü).
-- SIRA ZORUNLU: önce MV'ler (kaynak INSERT trigger'ı), sonra state
-- tabloları, sonra index'ler, EN SON kolonlar — indeksi duran kolonu
-- düşürmek Code 47 ile reddedilir (v0.9.623 ölçümü).

DROP TABLE IF EXISTS entity_seen_1m ON CLUSTER uptrace_all SYNC;
DROP TABLE IF EXISTS entity_seen_1m_local ON CLUSTER uptrace_all SYNC;
DROP TABLE IF EXISTS entity_seen_5m ON CLUSTER uptrace_all SYNC;
DROP TABLE IF EXISTS entity_seen_5m_local ON CLUSTER uptrace_all SYNC;

DROP TABLE IF EXISTS entity_sync_runs ON CLUSTER uptrace_all SYNC;
DROP TABLE IF EXISTS entity_relations ON CLUSTER uptrace_all SYNC;
DROP TABLE IF EXISTS entities ON CLUSTER uptrace_all SYNC;

ALTER TABLE spans_local ON CLUSTER uptrace_all DROP INDEX IF EXISTS idx_k8s_pod;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP INDEX IF EXISTS idx_k8s_node;

ALTER TABLE spans ON CLUSTER uptrace_all DROP COLUMN IF EXISTS k8s_node;
ALTER TABLE spans ON CLUSTER uptrace_all DROP COLUMN IF EXISTS k8s_pod;
ALTER TABLE spans ON CLUSTER uptrace_all DROP COLUMN IF EXISTS k8s_namespace;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP COLUMN IF EXISTS k8s_node;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP COLUMN IF EXISTS k8s_pod;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP COLUMN IF EXISTS k8s_namespace;
