-- 0007_rollup_case_repair.sql — v0.9.626
--
-- ZATEN UYGULANMIŞ 0002 için onarım. Taze kurulumda GEREKMEZ (0002
-- artık doğru ifadeyi taşıyor); yalnız 0002'yi v0.9.626'dan ÖNCE
-- uygulamış kurulumlar koşar.
--
-- SORUN
-- 0002'nin GENİŞ aile MV'si boyutları yalnız BÜYÜK harfli anahtardan
-- okuyordu:
--
--     toLowCardinality(attr_values[indexOf(attr_keys, 'CHANNEL_CODE')])
--
-- Prod ise KÜÇÜK harf yazıyor — operatör ölçümü, 10 dakikalık pencerede
-- 'channel_code' taşıyan 2.67M span, 'CHANNEL_CODE' taşıyan sıfır.
-- Sonuç: rollup_spans_wide_* tablolarının channel_code ve function_code
-- kolonları SABİT BOŞ doldu. Üstelik bu iki kolon
--
--   · tablonun ORDER BY önekinde  (service_name, span_kind, status_code,
--     channel_code, endpoint, function_code, ts)
--   · ve bloom skip index'lerinde (idx_function)
--
-- yer alıyor — yani birincil anahtarın bir bileşeni hiçbir şey elemiyor.
-- Okuma tarafındaki görünür belirti: /api/rollup/red'in channel/function
-- filtresi ve groupBy'ı sessizce boş sonuç veriyor
-- (internal/chstore/rollup_read.go:117-118).
--
-- Aynı kök neden spans tarafında v0.9.621-625 ile kapatıldı; bu dosya
-- rollup ayağı.
--
-- ── ADIM 1: 1m MV'nin sorgusunu yerinde değiştir ─────────────────────────
--
-- YALNIZ 1m MV attribute dizilerini okuyor; 5m ve 1h ondan kaskad ediyor
-- (FROM rollup_spans_wide_1m_local), dolayısıyla onlara dokunmak
-- GEREKMİYOR.
--
-- MODIFY QUERY, DROP+CREATE'e tercih edilir: hedef tabloya (TO
-- rollup_spans_wide_1m_local) dokunmaz, veri kaybı yok, ve MV'nin
-- kapalı olduğu pencere yok.
ALTER TABLE mv_rollup_spans_wide_1m ON CLUSTER uptrace_all
MODIFY QUERY
SELECT
    toStartOfInterval(time, INTERVAL 1 MINUTE)                     AS ts,
    service_name,
    kind                                                           AS span_kind,
    status_code,
    if(http_route != '', http_route, name)                         AS endpoint,
    toLowCardinality(coalesce(
        nullIf(attr_values[indexOf(attr_keys, 'CHANNEL_CODE')], ''),
        nullIf(attr_values[indexOf(attr_keys, 'channel_code')], ''), ''))  AS channel_code,
    coalesce(
        nullIf(attr_values[indexOf(attr_keys, 'FUNCTION_CODE')], ''),
        nullIf(attr_values[indexOf(attr_keys, 'function_code')], ''), '')  AS function_code,
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

-- ── ADIM 2: DOĞRULAMA (birkaç dakika bekledikten SONRA) ──────────────────
--
-- MODIFY QUERY yalnız BUNDAN SONRA yazılan satırları etkiler. Boyutun
-- gerçekten dolduğunu görmeden ADIM 3'e geçme.
--
--   SELECT countIf(channel_code != '') AS dolu, count() AS toplam
--   FROM rollup_spans_wide_1m
--   WHERE ts >= now() - INTERVAL 10 MINUTE;
--
-- dolu = 0 ise DUR: ya ADIM 1 uygulanmadı ya da veri iki yazımı da
-- kullanmıyor. Anahtarların gerçek hâli:
--
--   SELECT arrayJoin(attr_keys) AS k, count() FROM spans
--   WHERE time >= now() - INTERVAL 10 MINUTE
--   GROUP BY k ORDER BY count() DESC LIMIT 20;

-- ── ADIM 3: GEÇMİŞİ DÜZELT (isteğe bağlı, PAHALI) ────────────────────────
--
-- ADIM 1 yalnız ileriye dönük. 0002 uygulandığından beri biriken
-- satırların channel_code / function_code'u BOŞ kalır.
--
-- Geçmişi isteyen kurulum 0004_backfill.sql'i ilgilendiği pencere için
-- yeniden koşar. DİKKAT: rollup_spans_wide_1m_local
-- SummingMergeTree/AggregatingMergeTree ailesinden — aynı pencereyi
-- yeniden backfill etmek satırları TOPLAR, yani ÖNCE o pencereyi
-- düşürmek gerekir:
--
--   ALTER TABLE rollup_spans_wide_1m_local ON CLUSTER uptrace_all
--       DROP PARTITION '2026-08-04';
--
-- sonra 0004'ü o gün için koş, sonra 5m/1h kaskadı da aynı şekilde.
-- Pencereyi dar tut: tam geçmişi yeniden kurmak spans'in tamamını
-- yeniden okur.
--
-- KARAR NOTU: bunu otomatik yapmıyoruz. Canlı bir kümede DROP PARTITION
-- + yeniden backfill operatörün kararıdır; yanlış pencere seçilirse
-- rollup'ta veri boşluğu bırakır.
