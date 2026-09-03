-- 0014_attr_kvh_rollback.sql — attribute hash indeksi geri alma.
-- Sıra: INDEX → COLUMN (indeksi duran kolonu düşürmek Code 47). Uygulama
-- kolonu görmeyince dizi yoluna döner (kırılmaz, yavaşlar).
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP INDEX IF EXISTS idx_attr_kvh;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP INDEX IF EXISTS idx_attr_keys;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP INDEX IF EXISTS idx_res_kvh;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP INDEX IF EXISTS idx_res_keys;
ALTER TABLE spans ON CLUSTER uptrace_all DROP COLUMN IF EXISTS attr_kvh;
ALTER TABLE spans ON CLUSTER uptrace_all DROP COLUMN IF EXISTS res_kvh;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP COLUMN IF EXISTS attr_kvh;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP COLUMN IF EXISTS res_kvh;
