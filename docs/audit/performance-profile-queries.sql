-- performance-profile-queries.fixed.sql — docs/audit/performance-profile-queries.sql
-- DÜZELTİLMİŞ sürümü (lokal minikube CH 26.2.4.23, cluster 'coremetry' üzerinde
-- her ifade çalıştırılarak doğrulandı, 2026-09-02). __CLUSTER__ → prod adı.
--
-- DEĞİŞİKLİKLER (orijinal → neden → düzeltme)
--   Q1/Q2  30 s TIMEOUT (159). Maliyet `any(query)` = 2 GiB `query` kolonunun 9 M
--          satır için okunması (metinsiz agg 6 s, metinli 33 s tek düğüm). İki geçiş:
--          agg metinsiz → yalnız ilk 20 hash için metin. event_date budaması eklendi
--          (query_log PK/partition event_date; event_time tek başına aylık partition
--          budamaz). Bütçe 120 s: bunlar çevrimdışı denetim sorguları.
--   Q3     Code 43 ILLEGAL_TYPE: `... AS read_bytes` alias'ı ORDER BY sum(read_bytes)
--          içinde kolonu GÖLGELİYOR (yeni analyzer). Alias'lar *_h oldu. 7 gün 120 s'de
--          bile bitmedi (ProfileEvents Map kolonu her satır için okunuyor) → 1 gün.
--          NOT: prefer_localhost_replica=1 iken yerel shard bacağı initiator satırının
--          İÇİNDE sayılır; Q3 yalnız UZAK bacakları görür.
--   Q3c    exception_code tek başına gruplayınca `any(query)` rastgele bir örnek
--          döndürüyor (159'un 14.8k'sı tek sorguya ait DEĞİL). (code, hash) grubu.
--   Q5     Code 427 CANNOT_COMPILE_REGEXP: re2 lookahead `(?=` desteklemez →
--          yakalama grubu + `(?: SETTINGS|$)`.
--   Q7     Code 47: system.data_skipping_indices'te `marks` yok (26.2) → marks_bytes.
--   Q10    async_insert=1 iken written_rows `Insert` satırlarında 0, satırlar
--          `AsyncInsertFlush` türünde → iki tür + written_rows > 0.
--   Q11    Code 43: `... AS rows` alias'ı avg(rows) içinde gölgeleme → rows_h/bytes_h.
--          (system.asynchronous_insert_log 26.2'de VAR.)
--   Q11b/Q12#3  part_log da (event_date, event_time) → event_date budaması.
--   Q13c   Code 288 DISTRIBUTED_IN_JOIN_SUBQUERY_DENIED (distributed_product_mode=deny;
--          `spans` rand() ile shard'lanıyor, ebeveyn/çocuk aynı shard'da DEĞİL →
--          yerel join yanlış olurdu) → küçük ebeveyn kümesi GLOBAL alt sorgu.
-- NOTLAR
--   • normalized_query_hash LİTERALLERİ siler: farklı token listeli tüm log
--     desenleri TEK hash altında toplanır (calls = desen × tik).
--   • Alias olarak `sample`, CTE adı olarak `top` YAZMA (rezerve, Code 62).

-- DOĞRULAMA (2026-09-02, lokal 2-node küme, CH 26.2.4): 22/22 ifade temiz koşar.
-- İlk sürümün 7 hatası ve iki sessiz hatası düzeltildi:
--   • Q1/Q2: any(query) 7 günde 9 M satırın 2 GiB metin kolonunu okuyordu (159 timeout)
--     → iki geçiş (metinsiz agregat + top-20 hash için kısa metin geçişi), event_date
--     önek filtresi, 120 s bütçe.
--   • Q3/Q11: çıktı takma adı kaynak kolonu gölgeliyordu (Code 43, analyzer) → _h son-eki.
--   • Q5: re2 lookahead yok (427); Q7: data_skipping_indices'te `marks` yok (47, marks_bytes).
--   • Q13c: dağıtık self-join reddi (288) + rand() shard'da yerel join yanlış → GLOBAL alt sorgu.
--   • Q10 (sessiz): async_insert=1 iken satırlar query_kind='AsyncInsertFlush'ta — 'Insert'
--     yalnız 0 satır gösteriyordu. Q3c (sessiz): yalnız exception_code ile gruplamak tüm
--     timeout'ları rastgele tek örneğe bağlıyordu → hash eklendi.

-- ═══════════════════════════════════════════════════════════════════════
-- 1. SORGU PROFİLİ (7 gün)
-- ═══════════════════════════════════════════════════════════════════════

-- Q1 — normalized_query_hash bazında, EN PAHALI 20 (toplam süre) — iki geçiş
WITH agg AS (
  SELECT normalized_query_hash AS h,
         count()                                              AS calls,
         round(sum(query_duration_ms) / 1000, 1)              AS total_s,
         round(avg(query_duration_ms))                        AS avg_ms,
         round(quantile(0.95)(query_duration_ms))             AS p95_ms,
         formatReadableQuantity(sum(read_rows))               AS read_rows_h,
         formatReadableSize(sum(read_bytes))                  AS read_bytes_h,
         formatReadableSize(max(memory_usage))                AS max_mem,
         sum(query_duration_ms)                               AS total_ms
  FROM clusterAllReplicas('__CLUSTER__', system.query_log)
  WHERE event_date >= today() - 7 AND event_time > now() - INTERVAL 7 DAY
    AND type = 'QueryFinish' AND is_initial_query AND query_kind = 'Select'
  GROUP BY h
  ORDER BY total_ms DESC
  LIMIT 20
)
SELECT h, calls, total_s, avg_ms, p95_ms, read_rows_h, read_bytes_h, max_mem,
       substring(replaceRegexpAll(q, '\\s+', ' '), 1, 240) AS q_sample
FROM agg
LEFT JOIN (
  SELECT normalized_query_hash AS h, any(substring(query, 1, 240)) AS q
  FROM clusterAllReplicas('__CLUSTER__', system.query_log)
  WHERE event_date >= today() - 1
    AND type = 'QueryFinish' AND is_initial_query AND query_kind = 'Select'
    AND normalized_query_hash IN (SELECT h FROM agg)
  GROUP BY h
) AS txt USING (h)
ORDER BY total_ms DESC
SETTINGS max_execution_time = 120;

-- Q2 — EN YAVAŞ 20 (p95), en az 20 çağrı — iki geçiş (metin 7 gün: seyrek sorgular)
WITH agg AS (
  SELECT normalized_query_hash AS h,
         count()                                              AS calls,
         round(quantile(0.95)(query_duration_ms))             AS p95_ms,
         round(quantile(0.5)(query_duration_ms))              AS p50_ms,
         round(max(query_duration_ms))                        AS max_ms,
         formatReadableQuantity(quantile(0.95)(read_rows))    AS p95_read_rows,
         formatReadableSize(quantile(0.95)(read_bytes))       AS p95_read_bytes,
         formatReadableSize(max(memory_usage))                AS max_mem
  FROM clusterAllReplicas('__CLUSTER__', system.query_log)
  WHERE event_date >= today() - 7 AND event_time > now() - INTERVAL 7 DAY
    AND type = 'QueryFinish' AND is_initial_query AND query_kind = 'Select'
  GROUP BY h
  HAVING calls >= 20
  ORDER BY p95_ms DESC
  LIMIT 20
)
SELECT h, calls, p95_ms, p50_ms, max_ms, p95_read_rows, p95_read_bytes, max_mem,
       substring(replaceRegexpAll(q, '\\s+', ' '), 1, 240) AS q_sample
FROM agg
LEFT JOIN (
  SELECT normalized_query_hash AS h, any(substring(query, 1, 240)) AS q
  FROM clusterAllReplicas('__CLUSTER__', system.query_log)
  WHERE event_date >= today() - 7
    AND type = 'QueryFinish' AND is_initial_query AND query_kind = 'Select'
    AND normalized_query_hash IN (SELECT h FROM agg)
  GROUP BY h
) AS txt USING (h)
ORDER BY p95_ms DESC
SETTINGS max_execution_time = 120;

-- Q3 — shard bacakları: tarama maliyeti (1 gün; yalnız UZAK bacaklar, bkz. başlık)
SELECT normalized_query_hash AS h,
       count()                                              AS legs,
       formatReadableQuantity(sum(read_rows))               AS read_rows_h,
       formatReadableSize(sum(read_bytes))                  AS read_bytes_h,
       max(ProfileEvents['SelectedMarks'])                  AS max_marks,
       max(ProfileEvents['SelectedParts'])                  AS max_parts,
       round(quantile(0.95)(query_duration_ms))             AS p95_ms,
       substring(replaceRegexpAll(any(substring(query, 1, 300)), '\\s+', ' '), 1, 160) AS q_sample
FROM clusterAllReplicas('__CLUSTER__', system.query_log)
WHERE event_date >= today() - 1 AND event_time > now() - INTERVAL 1 DAY
  AND type = 'QueryFinish' AND is_initial_query = 0 AND query_kind = 'Select'
GROUP BY h
ORDER BY sum(read_bytes) DESC
LIMIT 20
SETTINGS max_execution_time = 120;

-- Q3b — bellek: en çok bellek kullanan 15 (tekil sorgu)
SELECT event_time, query_duration_ms, formatReadableSize(memory_usage) AS mem,
       formatReadableQuantity(read_rows) AS read_rows_h,
       substring(replaceRegexpAll(substring(query, 1, 400), '\\s+', ' '), 1, 200) AS q
FROM clusterAllReplicas('__CLUSTER__', system.query_log)
WHERE event_date >= today() - 7 AND event_time > now() - INTERVAL 7 DAY
  AND type = 'QueryFinish' AND query_kind = 'Select'
ORDER BY memory_usage DESC
LIMIT 15
SETTINGS max_execution_time = 120;

-- Q3c — hata/timeout: son 7 günde istisnayla biten sorgular (kod + hash)
SELECT exception_code, normalized_query_hash AS h, count() AS n,
       substring(replaceRegexpAll(any(substring(query, 1, 300)), '\\s+', ' '), 1, 160) AS q_sample,
       substring(any(exception), 1, 120) AS exc
FROM clusterAllReplicas('__CLUSTER__', system.query_log)
WHERE event_date >= today() - 7 AND event_time > now() - INTERVAL 7 DAY
  AND type = 'ExceptionWhileProcessing'
GROUP BY exception_code, h
ORDER BY n DESC
LIMIT 25
SETTINGS max_execution_time = 120;

-- Q4 — EXPLAIN şablonu (elle): EXPLAIN indexes = 1 <sorgu, spans → spans_local>;
-- PrimaryKey "Keys:" listesinde service_name YOKSA ve Granules N/N ise birincil
-- anahtar KULLANILMIYOR. Skip bölümünde "Condition:" satırı YOKSA index o
-- fonksiyon için uygulanamıyor demektir (örn. tokenbf + multiSearchAnyCaseInsensitive).

-- ═══════════════════════════════════════════════════════════════════════
-- 2. ŞEMA VE INDEX
-- ═══════════════════════════════════════════════════════════════════════

-- Q5 — tablolar (her host; engine, ORDER BY, PARTITION BY, TTL, boyut)
SELECT hostName() AS host, name, engine,
       partition_key, sorting_key, primary_key,
       formatReadableQuantity(total_rows) AS rows_h, formatReadableSize(total_bytes) AS bytes_h,
       extract(create_table_query, 'TTL ([^\n]*?)(?: SETTINGS|$)') AS ttl,
       extract(create_table_query, 'Distributed\\([^)]*\\)')        AS distributed_def
FROM clusterAllReplicas('__CLUSTER__', system.tables)
WHERE database = currentDatabase()
ORDER BY total_bytes DESC, host
LIMIT 200
SETTINGS max_execution_time = 30;

-- Q6 — telemetri tablolarının kolonları: MATERIALIZED/DEFAULT, sıkıştırılmış boyut
SELECT table, name, type, default_kind,
       substring(default_expression, 1, 100) AS expr,
       formatReadableSize(data_compressed_bytes)   AS compressed,
       formatReadableSize(data_uncompressed_bytes) AS uncompressed,
       round(data_uncompressed_bytes / greatest(data_compressed_bytes, 1), 1) AS ratio
FROM system.columns
WHERE database = currentDatabase()
  AND table IN ('spans_local', 'logs_local', 'metric_points_local', 'spans', 'logs', 'metric_points')
ORDER BY table, data_compressed_bytes DESC
LIMIT 300;

-- Q7 — skip index'ler (trace_id bloom, attr set(0) vb.)
SELECT hostName() AS host, table, name, type, expr, granularity,
       formatReadableSize(data_compressed_bytes) AS idx_bytes,
       formatReadableSize(marks_bytes)           AS marks_bytes_h
FROM clusterAllReplicas('__CLUSTER__', system.data_skipping_indices)
WHERE database = currentDatabase()
ORDER BY host, table, name
LIMIT 200
SETTINGS max_execution_time = 30;

-- Q8 — Distributed/okuma ayarları (oturum varsayılanları; profil düzeyi)
SELECT name, value, changed, description
FROM system.settings
WHERE name IN ('optimize_skip_unused_shards', 'distributed_group_by_no_merge', 'distributed_product_mode',
               'prefer_localhost_replica', 'load_balancing', 'max_parallel_replicas', 'use_skip_indexes',
               'async_insert', 'async_insert_max_data_size', 'async_insert_busy_timeout_ms', 'wait_for_async_insert',
               'max_execution_time', 'max_memory_usage', 'max_threads', 'max_bytes_before_external_group_by');

-- Q8b — Distributed tablolar: sharding key + ayarlar (create_table_query)
SELECT name, extract(create_table_query, 'Distributed\\([^)]*\\)') AS def,
       extract(create_table_query, 'SETTINGS [^\n]*') AS settings
FROM system.tables
WHERE database = currentDatabase() AND engine = 'Distributed'
ORDER BY name;

-- ═══════════════════════════════════════════════════════════════════════
-- 3. INGEST SAĞLIĞI
-- ═══════════════════════════════════════════════════════════════════════

-- Q9 — aktif part'lar: tablo/host başına sayı, ortalama part, satır, en büyük partition
SELECT hostName() AS host, table,
       count()                                    AS parts,
       formatReadableQuantity(sum(rows))          AS rows_h,
       formatReadableSize(sum(bytes_on_disk))     AS bytes_h,
       formatReadableSize(avg(bytes_on_disk))     AS avg_part,
       max(level)                                 AS max_level,
       uniq(partition)                            AS partitions,
       max(pc) AS max_parts_in_partition
FROM (
  SELECT hostName() AS h, table, rows, bytes_on_disk, level, partition,
         count() OVER (PARTITION BY hostName(), table, partition) AS pc
  FROM clusterAllReplicas('__CLUSTER__', system.parts)
  WHERE active AND database = currentDatabase()
)
GROUP BY host, table
ORDER BY parts DESC
LIMIT 80
SETTINGS max_execution_time = 30;

-- Q10 — son 24 saat INSERT'ler: async_insert=1 iken satırlar AsyncInsertFlush'ta
SELECT query_kind AS kind,
       substring(arrayStringConcat(arrayFilter(t -> NOT startsWith(t, concat(currentDatabase(), '.`.inner')), tables), ','), 1, 80) AS tbl,
       count()                                        AS inserts,
       round(avg(written_rows))                       AS rows_per_insert,
       formatReadableSize(avg(written_bytes))         AS bytes_per_insert,
       round(quantile(0.95)(query_duration_ms))       AS p95_ms,
       round(sum(written_rows) / 86400)               AS rows_per_sec
FROM clusterAllReplicas('__CLUSTER__', system.query_log)
WHERE event_date >= today() - 1 AND event_time > now() - INTERVAL 1 DAY
  AND type = 'QueryFinish' AND query_kind IN ('Insert', 'AsyncInsertFlush') AND written_rows > 0
GROUP BY kind, tbl
ORDER BY inserts DESC
LIMIT 30
SETTINGS max_execution_time = 60;

-- Q11 — async insert: 24 saatlik flush kaydı + anlık kuyruk
SELECT hostName() AS host, table, count() AS flushes,
       formatReadableQuantity(sum(rows)) AS rows_h, formatReadableSize(sum(bytes)) AS bytes_h,
       round(avg(rows)) AS rows_per_flush, countIf(status != 'Ok') AS failed
FROM clusterAllReplicas('__CLUSTER__', system.asynchronous_insert_log)
WHERE event_date >= today() - 1 AND event_time > now() - INTERVAL 1 DAY
GROUP BY host, table
ORDER BY flushes DESC
LIMIT 40
SETTINGS max_execution_time = 30;
SELECT hostName() AS host, table, count() AS pending, formatReadableSize(sum(total_bytes)) AS bytes_h
FROM clusterAllReplicas('__CLUSTER__', system.asynchronous_inserts)
GROUP BY host, table
SETTINGS max_execution_time = 30;

-- Q11b — part oluşturma sıklığı (part_log NewPart, saat başına): part churn
SELECT hostName() AS host, table, toStartOfHour(event_time) AS hr,
       countIf(event_type = 'NewPart') AS new_parts,
       countIf(event_type = 'MergeParts') AS merges,
       round(avgIf(rows, event_type = 'NewPart')) AS rows_per_new_part
FROM clusterAllReplicas('__CLUSTER__', system.part_log)
WHERE event_date >= today() - 1 AND event_time > now() - INTERVAL 1 DAY AND database = currentDatabase()
  AND table IN ('spans_local', 'logs_local', 'metric_points_local')
GROUP BY host, table, hr
ORDER BY host, table, hr
LIMIT 300
SETTINGS max_execution_time = 30;

-- Q12 — merge backlog: koşan merge'ler + replikasyon kuyruğu + son 24 saat merge süreleri
SELECT hostName() AS host, table, count() AS running_merges,
       round(max(elapsed)) AS max_elapsed_s, formatReadableSize(sum(total_size_bytes_compressed)) AS bytes_h,
       round(avg(progress), 2) AS avg_progress
FROM clusterAllReplicas('__CLUSTER__', system.merges)
GROUP BY host, table
ORDER BY running_merges DESC
LIMIT 40
SETTINGS max_execution_time = 30;
SELECT hostName() AS host, table, count() AS queue, countIf(num_tries > 3) AS retrying,
       max(num_tries) AS max_tries, substring(any(last_exception), 1, 200) AS exc
FROM clusterAllReplicas('__CLUSTER__', system.replication_queue)
GROUP BY host, table
ORDER BY queue DESC
LIMIT 40
SETTINGS max_execution_time = 30;
SELECT hostName() AS host, table,
       count() AS merges_24h, round(quantile(0.95)(duration_ms)) AS p95_ms,
       formatReadableSize(sum(size_in_bytes)) AS merged_bytes
FROM clusterAllReplicas('__CLUSTER__', system.part_log)
WHERE event_date >= today() - 1 AND event_time > now() - INTERVAL 1 DAY
  AND event_type = 'MergeParts' AND database = currentDatabase()
GROUP BY host, table
ORDER BY merges_24h DESC
LIMIT 40
SETTINGS max_execution_time = 30;

-- Q12b — asenkron metrikler: part sayısı tavanı, bekleyen mutasyon, replika gecikmesi
SELECT hostName() AS host, metric, value
FROM clusterAllReplicas('__CLUSTER__', system.asynchronous_metrics)
WHERE metric IN ('MaxPartCountForPartition', 'ReplicasMaxQueueSize', 'ReplicasMaxAbsoluteDelay',
                 'NumberOfTables', 'TotalPartsOfMergeTreeTables', 'MemoryResident', 'OSMemoryTotal')
ORDER BY host, metric
SETTINGS max_execution_time = 30;

-- ═══════════════════════════════════════════════════════════════════════
-- 4. API KATMANI — self-observability span'leri
-- ═══════════════════════════════════════════════════════════════════════

-- Q13 — endpoint bazında p95/p99 + çağrı sıklığı (7 gün; en yavaş 15)
SELECT if(http_route != '', http_route, name) AS route,
       count()                                          AS calls,
       round(count() / (7 * 24 * 60), 2)                AS calls_per_min,
       round(quantile(0.5)(duration) / 1e6)             AS p50_ms,
       round(quantile(0.95)(duration) / 1e6)            AS p95_ms,
       round(quantile(0.99)(duration) / 1e6)            AS p99_ms,
       countIf(status_code = 'error')                   AS errors
FROM spans
WHERE time > now() - INTERVAL 7 DAY
  AND service_name LIKE 'coremetry%' AND kind = 'server'
GROUP BY route
HAVING calls >= 50
ORDER BY p95_ms DESC
LIMIT 15
SETTINGS max_execution_time = 30;

-- Q13b — en sık çağrılan 15 endpoint (yük dağılımı)
SELECT if(http_route != '', http_route, name) AS route, count() AS calls,
       round(quantile(0.95)(duration) / 1e6) AS p95_ms
FROM spans
WHERE time > now() - INTERVAL 7 DAY AND service_name LIKE 'coremetry%' AND kind = 'server'
GROUP BY route ORDER BY calls DESC LIMIT 15
SETTINGS max_execution_time = 30;

-- Q13c — endpoint → CH sorgusu ilişkisi: server span'inin altındaki client/internal span'leri
-- (spans rand() ile shard'lı → ebeveyn kümesi GLOBAL yayınlanır; küme küçük: 1 gün self-obs server)
SELECT if(p.http_route != '', p.http_route, p.name) AS route,
       c.name AS child, count() AS n, round(quantile(0.95)(c.duration) / 1e6) AS p95_ms
FROM spans AS c
GLOBAL INNER JOIN (
  SELECT trace_id, span_id, http_route, name
  FROM spans
  WHERE time > now() - INTERVAL 1 DAY AND service_name LIKE 'coremetry%' AND kind = 'server'
) AS p ON p.trace_id = c.trace_id AND p.span_id = c.parent_id
WHERE c.time > now() - INTERVAL 1 DAY AND c.kind IN ('client', 'internal')
GROUP BY route, child
ORDER BY n DESC
LIMIT 60
SETTINGS max_execution_time = 30;
