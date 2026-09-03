-- 0014_attr_kvh.sql — spans attribute HASH indeksi (dış Distributed prod).
-- v0.10.299 (docs/audit/trace-attribute-search.md Dilim 1a — operatör onayı
-- 2026-09-03: "önerin ok"). Boot ASLA koşmaz (ON CLUSTER DDL'i N pod
-- yarıştırırsa kuyruk tıkanır — v0.9.613); uygulama kendi yönettiği
-- kurulumda aynı kolonları attr_index.go üzerinden boot'ta ekler. Bu dosya
-- OPERATÖR elinde koşar (clickhouse-client, ifade ifade); `uptrace_all`
-- token'ı gerçek küme adıyla değiştirilir.
--
-- ============================================================
-- GEREKÇE (ölçülmüş)
-- ============================================================
-- Terfi etmemiş HER attribute filtresi `attr_values[indexOf(attr_keys,'k')]=v`
-- hiçbir skip index kullanmıyor: pencerenin tamamı okunur (868k/868k satır,
-- değer nadir de olsa yok da olsa). Her span için cityHash64(k <0x1F> v)
-- dizisi + bloom_filter(0.01): nadir değerde 868k → 65k satır, 155 MiB →
-- 4.9 MiB (query_log 3 koşu medyanı). Depolama ≈ 10 B/satır (≈ +10 GB/gün
-- @1B span, ZSTD(3)) + 0.3 B/satır indeks — operatör kabul etti.
-- Yaygın değerde bloom budamaz (Datadog facet davranışı) → facet kaydı
-- ayrı dilim.
--
-- Kolon MATERIALIZED: INSERT anında sunucu hesaplar; Distributed forward
-- düşürür (dağıtık-güvenli); eski part'lar okuma anında hesaplar (kolon
-- DOĞRU, indeks yalnız yeni part'larda → kazanç retention boyunca dolar;
-- operatör kararı: kademeli, ADIM 6 opsiyonel).
--
-- İfade attr_index.go attrIndexExpr ile BİREBİR (attr_index_migration_test).
-- Ayırıcı '\x1F' (unit separator) — anahtar/değerde `=` olabilir, 0x1F olmaz.
--
-- ============================================================
-- UYGULAMA SIRASI
-- ============================================================
--   1. spans_local ON CLUSTER: ADD COLUMN attr_kvh, res_kvh (anında; metadata).
--   2. Distributed sarmalayıcı spans: aynı iki ADD COLUMN (SELECT çözümlemesi).
--   3. DOĞRULA: kolon her host'ta var VE ifadeyle eşdeğer.
--   4. Skip index'ler (yalnız YENİ part'lara; metadata).
--   5. Pod'ları yeniden başlat: probe kolonu görünce derleyici bloom yoluna
--      geçer (attr_index.go registerAttrIndex; küme kipinde iki-restart).
--   6. (opsiyonel, operatör kararı, mesai dışı) MATERIALIZE COLUMN + INDEX.

-- ── ADIM 1: spans_local hash kolonları ───────────────────────────────────
ALTER TABLE spans_local ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS attr_kvh Array(UInt64)
  MATERIALIZED arrayMap((k, v) -> cityHash64(concat(k, '\x1F', v)), attr_keys, attr_values) CODEC(ZSTD(3));
ALTER TABLE spans_local ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS res_kvh Array(UInt64)
  MATERIALIZED arrayMap((k, v) -> cityHash64(concat(k, '\x1F', v)), res_keys, res_values) CODEC(ZSTD(3));

-- ── ADIM 2: Distributed sarmalayıcı ──────────────────────────────────────
ALTER TABLE spans ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS attr_kvh Array(UInt64)
  MATERIALIZED arrayMap((k, v) -> cityHash64(concat(k, '\x1F', v)), attr_keys, attr_values) CODEC(ZSTD(3));
ALTER TABLE spans ON CLUSTER uptrace_all
  ADD COLUMN IF NOT EXISTS res_kvh Array(UInt64)
  MATERIALIZED arrayMap((k, v) -> cityHash64(concat(k, '\x1F', v)), res_keys, res_values) CODEC(ZSTD(3));

-- ── ADIM 3: DOĞRULA ──────────────────────────────────────────────────────
-- SELECT hostName(), name, type FROM clusterAllReplicas('uptrace_all', system.columns)
-- WHERE table = 'spans_local' AND name IN ('attr_kvh','res_kvh') ORDER BY 1, 2;
--   → her host'ta iki satır, type Array(UInt64).
-- SELECT countIf(attr_kvh != arrayMap((k, v) -> cityHash64(concat(k, '\x1F', v)), attr_keys, attr_values)) AS bad, count()
-- FROM spans WHERE time > now() - INTERVAL 10 MINUTE;
--   → bad = 0.
-- SELECT count() FROM spans WHERE time > now() - INTERVAL 10 MINUTE
--   AND has(attr_kvh, cityHash64(concat('<anahtar>', '\x1F', '<değer>')))
--   AND attr_values[indexOf(attr_keys, '<anahtar>')] = '<değer>';
--   → dizi yoluyla aynı sayı (bloom yanlış-pozitifi kesin eşitlik eler).

-- ── ADIM 4: skip index'ler (yalnız YENİ part'lara; MATERIALIZE INDEX YOK) ─
ALTER TABLE spans_local ON CLUSTER uptrace_all
  ADD INDEX IF NOT EXISTS idx_attr_kvh attr_kvh TYPE bloom_filter(0.01) GRANULARITY 4;
ALTER TABLE spans_local ON CLUSTER uptrace_all
  ADD INDEX IF NOT EXISTS idx_attr_keys attr_keys TYPE bloom_filter(0.01) GRANULARITY 4;
ALTER TABLE spans_local ON CLUSTER uptrace_all
  ADD INDEX IF NOT EXISTS idx_res_kvh res_kvh TYPE bloom_filter(0.01) GRANULARITY 4;
ALTER TABLE spans_local ON CLUSTER uptrace_all
  ADD INDEX IF NOT EXISTS idx_res_keys res_keys TYPE bloom_filter(0.01) GRANULARITY 4;

-- ── ADIM 5: pod'ları yeniden başlat (probe → bloom yolu) ─────────────────
-- kubectl rollout restart deployment/coremetry -n <ns>   (ingest+api rolleri)

-- ── ADIM 6 (opsiyonel, operatör kararı): eski part'lar ───────────────────
-- ALTER TABLE spans_local ON CLUSTER uptrace_all MATERIALIZE COLUMN attr_kvh;
-- ALTER TABLE spans_local ON CLUSTER uptrace_all MATERIALIZE COLUMN res_kvh;
-- ALTER TABLE spans_local ON CLUSTER uptrace_all MATERIALIZE INDEX idx_attr_kvh;
-- ALTER TABLE spans_local ON CLUSTER uptrace_all MATERIALIZE INDEX idx_res_kvh;
--   İzle: SELECT * FROM system.mutations WHERE table = 'spans_local' AND NOT is_done;
--   Bedel: retention × satır yeniden yazımı (disk + merge); mesai dışı.
