-- 0013_function_id.sql — spans.attr_function_id terfi kolonu (dış Distributed prod).
-- v0.10.233 (docs/audit/traces-attribute-columns.md D2 — operatör onayı 2026-09-02).
-- Boot ASLA koşmaz (ON CLUSTER DDL'i N pod yarıştırırsa kuyruk tıkanır —
-- v0.9.613); uygulama kendi yönettiği (tek-node / app-managed) kurulumda
-- aynı kolonu promoted_attr.go üzerinden boot'ta kendisi ekler. Bu dosya
-- OPERATÖR elinde koşar (clickhouse-client, ifade ifade); `uptrace_all`
-- token'ı gerçek küme adıyla değiştirilir.
--
-- ============================================================
-- GEREKÇE
-- ============================================================
-- Traces sayfasının varsayılan dört attribute kolonundan üçü (channel_code,
-- function_code, cluster) terfi kolonundan okunuyor; `function_id` tek
-- başına dizi yolundaydı: attr_keys/attr_values/res_keys/res_values dört
-- şişman dizinin dekompresyonu — 50 trace için 84 MiB / 212 ms vs terfi
-- kolonu 5.25 MiB / 66 ms (lokal, query_log medyanı). Kolon MATERIALIZED:
-- INSERT anında hesaplanır; eski part'lar okuma anında hesaplar (backfill
-- GEREKMEZ, kazanç yalnız yeni part'larda — istenirse ADIM 5).
-- Tip düz String (ev kuralı C3 "ölçmediysen LC yapma"): function_id bir
-- kimlik değil serbest değer; `uniq(attr_function_id) < 10k` ölçülürse
-- LowCardinality'ye geçiş ayrı sürümdür (store.go + bu dosya birlikte).
--
-- İfade promoted_attr.go'daki promotedAttrExprFor ile BİREBİR aynı
-- (0011 sözleşmesi): iki yazım (function_id, FUNCTION_ID), boş → ''.
-- Anahtar yazımı prod'da ÖLÇÜLMELİ (v0.9.626 dersi: CHANNEL_CODE vs
-- channel_code 11 gün boş kolon):
--   SELECT k, count() FROM spans ARRAY JOIN attr_keys AS k
--   WHERE time > now() - INTERVAL 10 MINUTE AND lower(k) = 'function_id' GROUP BY k;
--
-- ============================================================
-- UYGULAMA SIRASI
-- ============================================================
--   1. spans_local ON CLUSTER: ADD COLUMN.
--   2. Distributed sarmalayıcı spans: ADD COLUMN (SELECT çözümlemesi).
--   3. DOĞRULA: kolon her host'ta var VE dizi ifadesiyle eşdeğer.
--   4. Skip index (yalnız yeni part'lara).
--   5. (opsiyonel, ayrı komut) MATERIALIZE COLUMN — eski part'ları yazar;
--      disk + merge maliyeti, mesai dışı.
-- Uygulama tarafı: kolon her shard'da çözüldükten sonra boot probe'u
-- (repairPromotedAttrCols) kolonu DOLU görünce haritaya kaydeder; pod
-- yeniden başlatılmadan haritaya girmez (küme kipinde iki-restart tuzağı:
-- /perf-triage örnek vakası). Kolon boşsa dizi yoluna düşer — kırılmaz,
-- hızlanmaz.

-- ── ADIM 1: spans_local terfi kolonu ─────────────────────────────────────
ALTER TABLE spans_local ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS attr_function_id String
  MATERIALIZED coalesce(nullIf(attr_values[indexOf(attr_keys, 'function_id')], ''), nullIf(attr_values[indexOf(attr_keys, 'FUNCTION_ID')], ''), '');

-- ── ADIM 2: Distributed sarmalayıcı ──────────────────────────────────────
ALTER TABLE spans ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS attr_function_id String
  MATERIALIZED coalesce(nullIf(attr_values[indexOf(attr_keys, 'function_id')], ''), nullIf(attr_values[indexOf(attr_keys, 'FUNCTION_ID')], ''), '');

-- ── ADIM 3: DOĞRULA ──────────────────────────────────────────────────────
-- SELECT hostName(), name, type FROM clusterAllReplicas('uptrace_all', system.columns)
-- WHERE table = 'spans_local' AND name = 'attr_function_id' ORDER BY 1;
--   → her host'ta bir satır, type String.
-- SELECT countIf(attr_function_id != coalesce(nullIf(attr_values[indexOf(attr_keys, 'function_id')], ''), nullIf(attr_values[indexOf(attr_keys, 'FUNCTION_ID')], ''), '')) AS bad,
--        countIf(attr_function_id != '') AS filled, count()
-- FROM spans WHERE time > now() - INTERVAL 10 MINUTE;
--   → bad = 0 VE filled > 0 olmalı ("kolon var" ≠ "kolon dolu").

-- ── ADIM 4: skip index (yalnız YENİ part'lara; MATERIALIZE INDEX YOK) ────
ALTER TABLE spans_local ON CLUSTER uptrace_all
  ADD INDEX IF NOT EXISTS idx_attr_function_id attr_function_id TYPE set(0) GRANULARITY 4;

-- ── ADIM 5 (opsiyonel, operatör kararı): eski part'ları yaz ─────────────
-- ALTER TABLE spans_local ON CLUSTER uptrace_all MATERIALIZE COLUMN attr_function_id;
--   İzle: SELECT * FROM system.mutations WHERE table = 'spans_local' AND NOT is_done;
