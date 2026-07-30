-- 0005_attr_promotion.sql — CHANNEL_CODE / FUNCTION_CODE promotion
-- (Rollup Aşama-3, 2026-07-30). Amaç: geniş MV'nin (0002) her span
-- INSERT'inde çalışan iki attr-dizisi taraması (attr_values[indexOf(
-- attr_keys, …)]) yerine spans_local üzerinde MATERIALIZED kolon —
-- ingest maliyeti tasarım kestirimiyle +%4-8 → +%2-4 (docs/rollup-design.md §4).
--
-- BAĞLAM (bu repo):
-- · Uygulama, kendi yönettiği kurulumlarda AYNI kolonları boot'ta zaten
--   ekler (store.go:2231 attrColAlters) ve probe başarılıysa /traces
--   extras'ı bu kolonlardan okur (registerTraceAttrMaterialized).
--   DIŞ Distributed prod'da (cluster_name UNSET) uygulama ALTER'ı
--   BİLİNÇLİ ATLAR (v0.8.185/186 dersleri) — kolon sahipliği bu
--   dosyada, OPERATÖRDE.
-- · Tanımlar store.go'daki ALTER'larla BİREBİR aynı tutuldu
--   (LowCardinality + aynı MATERIALIZED ifadesi) — app-managed ve
--   operatör-managed kurulumlar arasında şema sapması olmasın.
-- · Bu dosya 0002 uygulanmadan da güvenlidir (ADIM 1-3 sorunsuz çalışır,
--   ADIM 4 "UNKNOWN TABLE mv_rollup_spans_wide_1m" verir — 0002 sonrası
--   yalnız ADIM 4 yeniden koşulur; her adım idempotent).
--
-- UYGULAMA SIRASI — SIRA KRİTİK (db_stmt_hash dersi: kırık MV trigger'ı
-- kaynak INSERT'i de düşürür ve TÜM ingest'i keser):
--   1. Kolonları ÖNCE her shard'ın spans_local'ına ekle (ON CLUSTER).
--   2. Distributed sarmalayıcı spans'a ekle (SELECT çözümlemesi için).
--   3. Her shard'da kolonların çözüldüğünü DOĞRULA (aşağıdaki kontrol).
--   4. ANCAK ondan sonra MV'yi MODIFY QUERY ile yeni kolonlara çevir.
-- MODIFY QUERY anlıktır ve yalnız YENİ block'ları etkiler; rollup
-- tablolarının şeması ve mevcut verisi DEĞİŞMEZ. Geri dönüş: ADIM 4'ü
-- 0002'deki orijinal SELECT ile tekrar çalıştırmak yeterli.
--
-- FUNCTION_CODE kardinalite notu: spans kolonu LowCardinality (app
-- ALTER'ıyla birebir). Prod'da şu kontrol > ~100k dönerse operatörle
-- düz String'e geçiş konuşulmalı (rollup tablolarındaki function_code
-- zaten düz String + bloom_filter — 0002 kararı, değişmiyor):
--   SELECT uniq(attr_values[indexOf(attr_keys,'FUNCTION_CODE')])
--   FROM spans WHERE time > now() - INTERVAL 1 DAY;

-- ── ADIM 1: spans_local kolonları (her shard, her replica) ────────────────
-- MATERIALIZED: INSERT anında hesaplanır; eski part'lar okuma anında
-- (ya da merge'te) hesaplar — backfill GEREKMEZ.
ALTER TABLE spans_local ON CLUSTER uptrace_all
    ADD COLUMN IF NOT EXISTS attr_channel_code LowCardinality(String)
    MATERIALIZED attr_values[indexOf(attr_keys, 'CHANNEL_CODE')];

ALTER TABLE spans_local ON CLUSTER uptrace_all
    ADD COLUMN IF NOT EXISTS attr_function_code LowCardinality(String)
    MATERIALIZED attr_values[indexOf(attr_keys, 'FUNCTION_CODE')];

-- ── ADIM 2: Distributed sarmalayıcı (SELECT forwarding çözümlemesi) ──────
ALTER TABLE spans ON CLUSTER uptrace_all
    ADD COLUMN IF NOT EXISTS attr_channel_code LowCardinality(String)
    MATERIALIZED attr_values[indexOf(attr_keys, 'CHANNEL_CODE')];

ALTER TABLE spans ON CLUSTER uptrace_all
    ADD COLUMN IF NOT EXISTS attr_function_code LowCardinality(String)
    MATERIALIZED attr_values[indexOf(attr_keys, 'FUNCTION_CODE')];

-- ── ADIM 3 (ELLE DOĞRULAMA — MODIFY QUERY'den önce ŞART) ─────────────────
-- ŞEMA sorgusu (veri sorgusu DEĞİL — analyzer kullanılmayan kolon
-- referanslarını budayabilir ve yanlış "başarılı" verir): her host
-- satırında İKİ kolon adı da görünmeli. Eksik ad gösteren ya da hiç
-- görünmeyen host'ta ADIM 1 henüz oturmamıştır (ZooKeeper DDL
-- kuyruğu) — bekle, yeniden koş:
--   SELECT hostName(), groupArray(name)
--   FROM clusterAllReplicas('uptrace_all', 'system', 'columns')
--   WHERE database = currentDatabase() AND table = 'spans_local'
--     AND name IN ('attr_channel_code', 'attr_function_code')
--   GROUP BY hostName();
-- Beklenen host sayısı = 2 shard × 2 replica = 4 satır, her biri
-- ['attr_channel_code','attr_function_code'].

-- ── ADIM 4: geniş 1m MV'yi promoted kolonlara çevir ──────────────────────
-- SELECT, 0002'deki mv_rollup_spans_wide_1m gövdesiyle AYNI; yalnız
-- channel_code/function_code kaynağı değişti (yorumlu satırlar 0002'de
-- zaten işaretliydi). Kaskad MV'leri (5m/1h) rollup_spans_wide_1m_local'dan
-- okur — DOKUNULMAZ.
ALTER TABLE mv_rollup_spans_wide_1m ON CLUSTER uptrace_all
MODIFY QUERY
SELECT
    toStartOfInterval(time, INTERVAL 1 MINUTE)                     AS ts,
    service_name,
    kind                                                           AS span_kind,
    status_code,
    if(http_route != '', http_route, name)                         AS endpoint,
    attr_channel_code                                              AS channel_code,
    attr_function_code                                             AS function_code,
    toUInt64(count())                                              AS span_count,
    toUInt64(countIf(status_code = 'error'))                       AS error_count,
    toUInt64(sum(duration))                                        AS duration_sum,
    sumForEachState(arrayMap(i -> toUInt64(i = least(19, greatest(0,
        toInt32(floor(log2(greatest(duration, 1000000) / 1000000)))))), range(20)))
                                                                   AS lat_buckets,
    argMaxState(trace_id, duration)                                AS slow_exemplar,
    anyIfState(trace_id, status_code = 'error')                    AS err_exemplar
FROM spans_local
GROUP BY ts, service_name, span_kind, status_code, endpoint, channel_code, function_code;

-- ── YAN ETKİ (bilgi): bir sonraki uygulama boot'unda store.go probe'u
-- (SELECT attr_channel_code, attr_function_code FROM spans … LIMIT 1)
-- artık BAŞARILI olur → /traces extras CHANNEL_CODE/FUNCTION_CODE
-- kolonlarını promoted yoldan okur (registerTraceAttrMaterialized).
-- Uygulama kodunda değişiklik GEREKMEZ; restart yeterli.
