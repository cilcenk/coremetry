-- 0013_function_id_rollback.sql — attr_function_id geri alma.
-- Sıra: INDEX → COLUMN (indeksi duran kolonu düşürmek Code 47). Uygulama
-- kolonu görmeyince dizi yoluna döner (kırılmaz, yavaşlar).
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP INDEX IF EXISTS idx_attr_function_id;
ALTER TABLE spans ON CLUSTER uptrace_all DROP COLUMN IF EXISTS attr_function_id;
ALTER TABLE spans_local ON CLUSTER uptrace_all DROP COLUMN IF EXISTS attr_function_id;
