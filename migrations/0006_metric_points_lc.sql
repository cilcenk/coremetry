-- 0006_metric_points_lc.sql — metric_points LowCardinality mutasyonu
-- (Uptrace uyarlamaları / LC-kodek denetimi, 2026-07-30). OPSİYONEL ve
-- OPERATÖR-uygulamalı (0001-0005 sahiplik sözleşmesinin aynısı).
--
-- NE: metric_points'in üç serbest kolonu String → LowCardinality tipine
-- iner (+ ZSTD(1)). Gerekçe: attr/res ANAHTARLARI zaten LC; DEĞERLER
-- politika gereği sınırlı kardinalitede (CLAUDE.md: yüksek-kardinalite
-- String metrik boyutu yasak) ama düz String tutuluyordu. Taban ölçümü
-- (lokal): res_values 568MiB ham → 34.5MiB (oran 16.5, kodeksiz LZ4) —
-- LC sözlüğü + ZSTD hem diski hem merge/INSERT bellek izini düşürür.
--
-- NASIL: MODIFY COLUMN TİP değişimi = MUTATION (kodek-only'nin aksine
-- metadata değil): her canlı part'ta o kolonlar YENİDEN YAZILIR, arka
-- planda part-part ilerler (bloklamaz; system.mutations'tan izlenir).
-- metric_points TTL'i kısa (varsayılan 7g) — maliyet penceresi sınırlı.
-- Mutasyon bitmeden eski part'lar okumada örtük cast ile DOĞRU servis
-- edilir; acele yok.
--
-- UYGULAMA SONRASI: uygulamanın boot'taki kodek-ALTER'ları tip-probe'la
-- kendini KAPATIR (store.go v0.9.439 — tip LowCardinality içerince
-- MODIFY koşulmaz; aksi halde tipi String'e geri mutasyonlardı).
--
-- GERİ DÖNÜŞ: aynı üç MODIFY'ı Array(String)/String tipiyle koşmak
-- (yine mutation). Sonraki boot kodek-ALTER'ları yeniden devreye girer.

-- Ölçüm (ÖNCE — not al):
--   SELECT name, formatReadableSize(data_compressed_bytes),
--          formatReadableSize(data_uncompressed_bytes)
--   FROM clusterAllReplicas('uptrace_all', system.columns)
--   WHERE database = currentDatabase() AND table = 'metric_points_local'
--     AND name IN ('description','attr_values','res_values');

ALTER TABLE metric_points_local ON CLUSTER uptrace_all
    MODIFY COLUMN description LowCardinality(String) DEFAULT '' CODEC(ZSTD(1));
ALTER TABLE metric_points_local ON CLUSTER uptrace_all
    MODIFY COLUMN attr_values Array(LowCardinality(String)) CODEC(ZSTD(1));
ALTER TABLE metric_points_local ON CLUSTER uptrace_all
    MODIFY COLUMN res_values Array(LowCardinality(String)) CODEC(ZSTD(1));

-- Distributed sarmalayıcı (SELECT çözümlemesi + boot probe'unun görmesi):
ALTER TABLE metric_points ON CLUSTER uptrace_all
    MODIFY COLUMN description LowCardinality(String) DEFAULT '' CODEC(ZSTD(1));
ALTER TABLE metric_points ON CLUSTER uptrace_all
    MODIFY COLUMN attr_values Array(LowCardinality(String)) CODEC(ZSTD(1));
ALTER TABLE metric_points ON CLUSTER uptrace_all
    MODIFY COLUMN res_values Array(LowCardinality(String)) CODEC(ZSTD(1));

-- Mutasyon izleme:
--   SELECT table, command, parts_to_do, is_done
--   FROM clusterAllReplicas('uptrace_all', system.mutations)
--   WHERE database = currentDatabase() AND table = 'metric_points_local'
--   ORDER BY create_time DESC LIMIT 10;

-- Ölçüm (SONRA — mutasyon is_done=1 olunca aynı sorguyu tekrar koş,
-- compressed/uncompressed oranlarını ÖNCE ile kıyasla).
