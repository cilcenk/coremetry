-- 0003_rollup_metrics_rollback.sql — aile-C GERİ ALMA (v0.9.712).
-- Sıra ÖNEMLİ: önce MV'ler (yazımı kes), sonra Distributed sarmalayıcı,
-- en son _local (dropCombinedMV deseni; ters sıra yazımı yarıda keser ve
-- INSERT hatası üretir — v0.8.185 sınıfı).
-- Uygulama: yalnız operatör, elle. Veri kaybı: rollup içeriği (ham
-- metric_points'e DOKUNMAZ — kaynak tablo bağımsız).
DROP VIEW IF EXISTS mv_rollup_metrics_1h ON CLUSTER uptrace_all;
DROP VIEW IF EXISTS mv_rollup_metrics_5m ON CLUSTER uptrace_all;
DROP VIEW IF EXISTS mv_rollup_metrics_1m ON CLUSTER uptrace_all;
DROP TABLE IF EXISTS rollup_metrics_1h ON CLUSTER uptrace_all;
DROP TABLE IF EXISTS rollup_metrics_5m ON CLUSTER uptrace_all;
DROP TABLE IF EXISTS rollup_metrics_1m ON CLUSTER uptrace_all;
DROP TABLE IF EXISTS rollup_metrics_1h_local ON CLUSTER uptrace_all SETTINGS max_table_size_to_drop = 0;
DROP TABLE IF EXISTS rollup_metrics_5m_local ON CLUSTER uptrace_all SETTINGS max_table_size_to_drop = 0;
DROP TABLE IF EXISTS rollup_metrics_1m_local ON CLUSTER uptrace_all SETTINGS max_table_size_to_drop = 0;
