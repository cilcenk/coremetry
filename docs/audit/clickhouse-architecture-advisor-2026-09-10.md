# ClickHouse mimari değerlendirmesi — Coremetry (2026-09-10, v0.10.645)

`/clickhouse-architecture-advisor` skill'i ile, koddan okunan mevcut durum üzerine.
Her öneri **kaynak sınıfı** taşır: `official` (resmî doküman doğrudan söyler) ·
`derived` (dokümante davranıştan çıkarım) · `field` (deneyime dayalı, duruma bağlı —
sezgisel, doğrulanmadan uygulanmaz). Resmî doküman her zaman önceliklidir.

Mevcut durum kaynakları: `internal/chstore/store.go` (DDL), `repo.go:41-92`
(insert ayarları), `migrations/*.sql` (rollup aileleri), `retention_enforce.go`,
`cluster.go`; sayımlar `git grep` ile (FINAL 328, JOIN ~90, dictGet 0, projection 0,
mutasyon 19 — hepsi DELETE, küçük durum tabloları). Ayrıntılı şema sözleşmesi:
`.claude/skills/clickhouse-schema/SKILL.md`.

## Workload Summary

- **workload:** observability (OTel span / log / metrik / profil), tek-kiracı APM
- **latency target:** `/api/*` p99 < 200 ms sıcak, hot uçlar < 50 ms; ingest ucu
  5 s flush penceresi kabul (`FlushInterval` varsayılan 5 s)
- **data shape:** append-only telemetri (spans ~1B/gün hedef, `metric_points`
  çift yazım VM ile), 36 `ReplacingMergeTree(version)` durum tablosu, 33 artımlı MV
  (20 combined + 13 replicated rollup), 4 korelatör rollup'ı (Go batch → RMT)
- **primary query patterns:** MV-first servis/operasyon RED (5 dk kovalar), trace
  arama (iki aşamalı: index MV → `IN (…)`), log arama (CH ya da ES), problem/
  anomali durumu (`FINAL`), rollup kademesi seçimi (DAR/GENİŞ/METRİK/ROUTE)
- **operational constraints:** prod dış Distributed CH (2 shard × 2 replica,
  Keeper), lokal chc-0/1; tek binary, rolling deploy (MV tip değişimi = okuma
  penceresi); retention TTL + saatlik `DROP PARTITION` enforcer; ES yalnız log
  okuma; VictoriaMetrics metrik okuma (CH `metric_points` çift yazımı AÇIK)

## Key Decisions

1. **Ingest = doğrudan batch + `async_insert`** (Kafka engine değil): OTel
   collector zaten üretici tarafında ayrıştırıyor; ClickHouse önünde ikinci kuyruk
   gerekmiyor. Kalan soru batch boyutu ve pod başına async tampon çarpanı.
2. **Günlük partition, hafta/ay değil**: retention saatlik `DROP PARTITION` ile
   partition sınırında çalışıyor; günlük `toDate` bu operasyona hizalı. Advisor'ın
   "aylık varsayılan" önerisi bu iş yükünde geçerli değil (aşağıda gerekçe).
3. **Artımlı MV'ler doğru; refreshable MV yalnız korelatör rollup'ları için aday**
   (Go'nun RMT'ye last-write-wins yazdığı 4 tablo).
4. **Sözlük (dictionary) hiç kullanılmıyor** — küçük, yavaş değişen arama tabloları
   (`service_metadata`, `team_contacts`, `metric_catalog`) FINAL'lı okuma/JOIN yerine
   sözlük adayı.
5. **RMT + FINAL doğru desen; partition sürüklenmesi bulgusu kapalı** (v0.9.1306/1335,
   0009/0010 — bu dokümanın ilk sürümü bayat bulguyu tekrar etmişti, v0.10.665 düzeltti).
6. **Muhtemel darboğaz: parça (part) baskısı** — 33 MV × günlük partition × N ingest
   pod'unun async tamponu, 2 shard üzerinde merge yükü. Önce ölç, sonra tasarla.

## Recommendations

### 1. Insert yolu: doğrudan batch + async_insert'i koru; batch boyutunu ölçerek yükselt

**What** Mevcut: Go batcher `BatchSize` 10 000 satır / 5 s / 8 işçi →
`async_insert=1`, `wait_for_async_insert=1`, `async_insert_max_data_size` 10 MB,
`busy_timeout` 1 s. Kafka engine EKLEME; batch boyutunu 10k → 50k'ya çıkarmayı
ölçümle değerlendir.

**Why** Resmî rehber 10k–100k satırlık batch'leri önerir; 10k alt sınırda. Async
insert zaten küçük yazımları sunucuda birleştiriyor; Kafka engine "çok bağımsız
üretici / replay" durumunda anlamlı — burada collector o rolü üstleniyor.

**How** `system.query_log` üzerinden insert başına `written_rows` medyanı ve
`system.parts` aktif parça sayısı ölçülür; parça oluşturma hızı yüksekse önce
`BatchSize`, sonra `async_insert_max_data_size` artırılır. `wait_for_async_insert=1`
korunur (istemci hata görür; `0` yapmak sessiz kayıp riski).

**Category** official (batch/async) · field (Kafka engine gereksiz — collector
topolojisine bağlı; farklı bir ingest topolojisinde yeniden değerlendirilir)

**Confidence** high (batch/async) · heuristic (Kafka kararı)

**Source**
- https://clickhouse.com/docs/best-practices/selecting-an-insert-strategy
- https://clickhouse.com/docs/optimize/asynchronous-inserts
- https://clickhouse.com/docs/en/operations/settings/settings#async_insert

**Validation**
```sql
SELECT quantile(0.5)(written_rows) AS rows_per_insert, count() AS inserts
FROM system.query_log
WHERE type = 'QueryFinish' AND query_kind = 'Insert' AND event_time > now() - INTERVAL 1 HOUR
  AND tables = ['spans'];
SELECT table, count() AS active_parts FROM system.parts WHERE active GROUP BY table ORDER BY active_parts DESC LIMIT 10;
SELECT * FROM system.asynchronous_inserts;   -- pod başına tampon × N pod = sunucu belleği
```

### 2. Partition: günlük `toDate` doğru; partition sayısını ve 10 s kademesinin saatlik partition'ını izle

**What** Telemetri + MV'ler günlük, 10 s rollup kademesi saatlik (`toStartOfHour`,
7 g TTL → ~168 partition), düşük hacimli arşivler aylık, durum tabloları
partition'sız. Değişiklik ÖNERİLMİYOR; izleme ekle.

**Why** Advisor'ın "aylık partition" önerisi retention'ın merge-tabanlı TTL ile
yürüdüğü kurulumlar içindir. Coremetry retention'ı saatte bir `DROP PARTITION`
ile uyguluyor (`retention_enforce.go`); günlük partition bu operasyonun doğal
birimi (resmî: partition, yaşam döngüsü yönetimi için). 30 g retention × 2 shard =
tablo başına ~30 partition; 33 MV ile toplam makul. Saatlik kademe 168 partition ile
sınırda değil ama her partition ayrı parça kümesi taşır.

**How** Aylık partition'a geçiş = `DROP PARTITION` granülaritesini kaybetmek
(aylık silme = 30 günlük veri bir anda) → uygulanmaz. Yalnız uyarı: aktif partition
sayısı tablo başına > 300 ya da toplam parça > 10k ise merge ayarlarına bak.

**Category** official (partition = lifecycle) · derived (günlük seçimin gerekçesi)

**Confidence** high

**Source**
- https://clickhouse.com/docs/en/engines/table-engines/mergetree-family/custom-partitioning-key
- https://clickhouse.com/docs/partitions

**Validation**
```sql
SELECT table, uniqExact(partition) AS partitions, count() AS parts, sum(rows) AS rows
FROM system.parts WHERE active AND database = currentDatabase()
GROUP BY table ORDER BY parts DESC LIMIT 15;
```

### 3. Ön-toplama: artımlı MV'ler doğru; korelatör rollup'larını refreshable MV'ye taşımayı değerlendir

**What** 33 artımlı MV kalsın. Go korelatörünün batch INSERT ile beslediği 4
rollup (`topology_edges_5m`, `topology_op_edges_5m`, `service_callers_5m`,
`topology_root_flows_5m`; RMT, last-write-wins) için **refreshable MV** adayı.

**Why** Resmî doküman: tekrar eden agregasyon → artımlı MV; karmaşık/zamanlanmış
yeniden hesap → refreshable MV. Korelatör tabloları "kova yeniden yazılırsa toplam
birikmez, kaybolur" semantiği taşıyor (şema skill'i §2); refreshable MV kovayı
kaynaktan yeniden hesaplar, idempotentlik uygulama koduna değil motora ait olur.
Bedel: refreshable MV Distributed/Replicated kurulumda tek koordinatörde koşar,
dakika altı tazelik sağlamaz (5 dk kova için yeterli).

**How** Önce tek tablo (`service_callers_5m`) ile pilot: `REFRESH EVERY 5 MINUTE
OFFSET 30 SECOND` + `APPEND` değil tam yeniden yazım; Go korelatörü kapatılır,
çıktı 24 saat A/B karşılaştırılır. CH 24+ gerekli (mevcut şart).

**Category** official (MV türleri) · derived (bu 4 tablonun uygunluğu)

**Confidence** medium — kümede refreshable MV koordinasyonu ve `ON CLUSTER`
davranışı lokalde doğrulanmalı (lokal küme askıda, operatör "C").

**Source**
- https://clickhouse.com/docs/materialized-view/incremental-materialized-view
- https://clickhouse.com/docs/materialized-view/refreshable-materialized-view

**Validation**
- Pilot tablo için 24 saatlik kova toplamları: Go korelatör vs refreshable MV,
  `sum(count)` farkı 0 olmalı; `system.view_refreshes` durum/süre.

### 4. Zenginleştirme: küçük ve yavaş değişen arama tabloları için sözlük (dictionary)

**What** `dictGet` kullanımı 0. Adaylar: `service_metadata` (sahip/ekip/depo pini),
`team_contacts`, `metric_catalog` tazelik, `entity` isim çözümü — okuma yolunda
FINAL'lı alt sorgu/JOIN yerine `dictGet('svc_meta', 'owner', service_name)`.

**Why** Resmî rehber: çok sorguda tekrar eden, küçük, yavaş değişen anahtar
aramaları için sözlük. Bu tablolar RMT olduğu için sözlük kaynağı `SELECT … FINAL`
ile tanımlanır; `LIFETIME(MIN 60 MAX 300)` yeterli (ayar değişimi 5 dk içinde görünür).

**How** Önce `system.query_log`'da JOIN/alt sorgu taşıyan en pahalı 10 sorguyu
`ProfileEvents` ile sırala; yalnız `service_metadata`'ya giden olanlarla başla.
Sözlük DDL boot'ta `migrate()` içinde (bildirimsel, `CREATE DICTIONARY IF NOT
EXISTS`), küme kipinde `ON CLUSTER` + her düğümde yerel.

**Category** official

**Confidence** high (desen) · medium (kazanç büyüklüğü — ölçülmedi)

**Source**
- https://clickhouse.com/docs/en/sql-reference/dictionaries
- https://clickhouse.com/docs/best-practices/minimize-optimize-joins

**Validation**
```sql
SELECT normalizedQueryHash(query) AS h, count() AS n, avg(query_duration_ms) AS ms,
       any(substring(query, 1, 120)) AS sample
FROM system.query_log
WHERE type = 'QueryFinish' AND event_time > now() - INTERVAL 1 DAY AND query ILIKE '%JOIN%'
GROUP BY h ORDER BY n * ms DESC LIMIT 10;
```

### 5. Değişken durum: RMT(version)+FINAL doğru — "partition sürüklenmesi" bulgusu KAPALI (düzeltme v0.10.665)

**What** 36 RMT tablosu `FINAL` ile okunuyor (328 kullanım) — desen resmî. Bu
dokümanın ilk sürümü v0.9.1304'ün açık bulgusunu ("`problems`/`anomaly_events`
çok-partition, `started_at` yeniden yazılıyor, yazıcıyı düzelt") tekrar
ediyordu; koda karşı doğrulandı ve BAYAT: v0.9.1306 teşhisi kök nedeni
topolojide buldu (shard-yerel state tabloları + bağlantı kayması — yazıcı
suçsuz), 0009 birleştirmesi kapattı, v0.9.1335 iki tablonun PARTITION BY'ını
söktü (mevcut kurulumlar `migrations/0010`). Bugün DDL'de partition yok →
FINAL id'ye göre kesin; `do_not_merge_across_partitions_select_final` riski
kalmadı. Yazıcılar `started_at`'i taşıyor (anomaly.go `hasOpen` → mevcut
satır; `UpsertAnomalyEvents` FINAL taşıma okuması).

**Why** Yanlış reçete dokümanda durdukça uygulanır; kalan tek iş prod'da
0010'un uygulandığını doğrulamak.

**How** Aşağıdaki sorgu prod'da her iki tablo için 1 dönmeli; >1 ise
`migrations/0010` uygulanmamıştır (veri koruyan repartition göçü, operatör).

**Category** official (RMT+FINAL) · derived (kapanış tespiti)

**Confidence** high

**Source**
- https://clickhouse.com/docs/en/guides/replacing-merge-tree
- `internal/chstore/partition_dedup_test.go` (teşhis ve muhafız)

**Validation**
```sql
SELECT table, uniqExact(partition) AS partitions, count() AS parts
FROM system.parts WHERE active AND table IN ('problems', 'anomaly_events')
GROUP BY table;   -- beklenen: partitions = 1 (partition'sız tablo)
```

### 6. Mutasyonlar: 19 DELETE küçük durum tablolarında — sınırlı, kabul; lightweight DELETE'e geçiş düşük öncelik

**What** `ALTER TABLE … DELETE` 19 çağrı (system_settings, dashboards,
saved_views, rag_chunks, alert_rules, monitors, runbooks, …); UPDATE mutasyonu yok;
telemetri tablolarında mutasyon yok.

**Why** Resmî rehber mutasyonlardan kaçınmayı söyler; burada mutasyon yalnız
yönetici eylemleriyle, satır sayısı küçük tablolarda ve düşük sıklıkta. Darboğaz
değil. `DELETE FROM` (lightweight delete, CH ≥ 23.3) aynı işi parça yeniden
yazımı olmadan yapar; RMT'de "tombstone + version" alternatifi de var ama okuma
yolunu karmaşıklaştırır.

**How** Mutasyon sayısını `system.mutations` ile izle; günde > 100 ya da
`is_done=0` birikimi görülürse lightweight DELETE'e geç. Aksi hâlde bırak.

**Category** field — heuristik; resmî bağlam mutasyon kaçınma rehberi. Karar
tablo boyutu ve sıklığa bağlı; bugünkü ölçek için değişiklik gerektirmiyor.

**Confidence** heuristic

**Source**
- https://clickhouse.com/docs/en/guides/replacing-merge-tree (bağlam)
- https://clickhouse.com/docs/sql-reference/statements/delete

**Validation**
```sql
SELECT table, count() AS mutations, countIf(NOT is_done) AS pending
FROM system.mutations WHERE create_time > now() - INTERVAL 1 DAY GROUP BY table;
```

### 7. Distributed: `rand()` shard anahtarı 2 shard'da doğru; ≥ 4 shard'da yeniden değerlendir

**What** Telemetri `Distributed(…, rand())`; durum tabloları shard bölünmesi
(migrasyon 0009) ve shard anahtarı ORDER BY içinde (şema skill'i O5). Bugün
değişiklik yok; büyüme kararı için eşik tanımı.

**Why** `rand()` yazımı dengeler, servis-kapsamlı sorgular tüm shard'lara yayılır
(2 shard'da ihmal edilebilir). `cityHash64(service_name)` MV yerelliği sağlar ama
sıcak servisler shard'ı eğriltir; 1000 servisli kurulumda eğrilik ölçülmeden
seçilmez. GROUP BY itmesi bilinçli yok (kopya kısmi satır tuzağı, memory notu).

**How** Shard sayısı ≥ 4 olduğunda: `system.query_log` üzerinden shard başına
okunan satır dağılımı ölçülür; en büyük 10 servisin hacim payı < %30 ise
hash-sharding pilotu, değilse `rand()` kalır.

**Category** derived (sharding) · field (eşik değerleri — kurulum hacmine bağlı)

**Confidence** medium

**Source**
- https://clickhouse.com/docs/en/engines/table-engines/special/distributed
- https://clickhouse.com/docs/optimize/query-optimization

**Validation**
```sql
SELECT service_name, sum(span_count) AS n FROM service_summary_5m
WHERE time_bucket > now() - INTERVAL 1 DAY GROUP BY service_name ORDER BY n DESC LIMIT 10;
```

### 8. Muhtemel darboğaz: parça baskısı ve merge kuyruğu — önce ölç

**What** 33 MV × günlük partition × N ingest pod'unun async tamponu → parça
oluşturma hızı; 2 shard'da merge kapasitesi. Bugün ölçüm yok; haftalık bir bakış.

**Why** Advisor çerçevesi: yapısal yeniden tasarımdan önce çalışan darboğazı
adlandır. Coremetry'de sorgu tarafı MV-first ve sınırlı; yazım tarafı en çok
sayıda hareketli parça içeriyor.

**How** Aşağıdaki üç sorgu `/admin/clickhouse` sağlık paneline eklenebilir
(`clickhouse_health.go` zaten `system.merges` okuyor); `DelayedInserts` > 0 ya da
`parts_to_delay_insert`'e yaklaşma = önce batch boyutu (öneri 1), sonra MV sayısı.

**Category** derived

**Confidence** medium

**Source**
- https://clickhouse.com/docs/optimize/query-optimization
- https://clickhouse.com/docs/use-cases/time-series/basic-operations

**Validation**
```sql
SELECT event, value FROM system.events WHERE event IN ('DelayedInserts','RejectedInserts','InsertedRows','MergedRows');
SELECT count() AS running, sum(progress < 0.5) AS early FROM system.merges;
SELECT table, max(active_parts) FROM (SELECT table, count() AS active_parts FROM system.parts WHERE active GROUP BY table, partition) GROUP BY table ORDER BY 2 DESC LIMIT 10;
```

## Hemen yapılacaklar vs yapısal

| Şimdi (ölçüm, düşük risk) | Yapısal (spec + pilot ister) |
|---|---|
| ~~Öneri 1/2/8 doğrulama sorgularını `/admin/clickhouse`'a taşı~~ **GEMİDE v0.10.683** (`/api/admin/clickhouse/measure`: host × tablo parça baskısı, DelayedInserts/RejectedInserts, async tampon, insert boyutu — query_log kapalıysa "kullanılamıyor" ilan edilir; BatchSize çipi). `BatchSize` 10k→50k A/B **operatörde**: `COREMETRY_INGEST_BATCH_SIZE=50000` ile 24 sa, panelde max parts/partition ↓, DelayedInserts ↓, satır/insert ↑ beklenir | Öneri 3: korelatör rollup'ları refreshable MV pilotu (tek tablo, 24 s A/B) |
| Öneri 5: prod'da `migrations/0010` doğrulaması (tek sorgu) | Öneri 4: `service_metadata` sözlüğü (boot DDL + okuma yolu değişimi) |
| Öneri 6: `system.mutations` izlemesi | Öneri 7: ≥ 4 shard'da shard anahtarı kararı |

Belirsiz olanlar açıkça belirtildi: refreshable MV'nin kümedeki koordinasyonu ve
sözlük kazancının büyüklüğü ölçülmeden bilinmiyor; ikisi de pilot ister.
