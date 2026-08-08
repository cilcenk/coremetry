# Runbook — Rollup backfill (`migrations/0004_backfill.sql`)

Rollup tablolarını **kurulum anından ÖNCEKİ** pencereyle doldurma
prosedürü. Şablonlar: `migrations/0004_backfill.sql`. Strateji:
`docs/rollup-design.md` §6.

---

## 0. Bu runbook ne yapar, ne yapmaz

Materialized view'lar yalnız **yeni** insert'leri görür. DDL'i uyguladığın
an (t₀) rollup tabloları o andan itibaren dolmaya başlar; t₀'dan önceki
geçmiş **boştur** ve kendiliğinden dolmaz. Bu runbook o pencereyi
`INSERT ... SELECT` ile elle doldurur.

Yapmaz:

- **Retansiyonu uzatmaz.** Backfill yalnız ham tabloda HÂLÂ duran veriyi
  okuyabilir. Ham veri TTL ile düşmüşse geri gelmez.
- **Rollup TTL'ini aşamaz.** Hedef tablonun kendi TTL'inden eski bir
  pencereyi doldurmak boşa iştir — yazdığın parçalar ilk TTL turunda
  düşer (§2 tablosu).
- **Otomatik değildir.** Kurulum sihirbazı (`/admin/clickhouse` → "Rollup
  katmanı") 0001/0003/0008 DDL'ini koşar; **0004 bilinçli olarak
  sihirbazın DIŞINDADIR** (`migrations/embed.go`: "operatör kararı
  gerektiren, geri alınması pahalı işler"). Buradaki her komut elle
  koşulur.

---

## 1. Ön koşullar

1. **Hedef tablolar kurulu.** İlgili DDL uygulanmış olmalı — yoksa
   `INSERT` "table doesn't exist" ile düşer.

   ```sql
   SELECT name FROM system.tables
   WHERE database = currentDatabase() AND name LIKE 'rollup_%_local'
   ORDER BY name;
   ```

2. **Distributed kurulum + küme adı.** Rollup zinciri
   `ReplicatedAggregatingMergeTree` + `Distributed` üzerine kuruludur;
   DDL `ON CLUSTER` yazar ve `RollupApply` boş küme adını **reddeder**
   ("cluster adı zorunlu"). Prod'daki gerçek adı kümenin kendisinden oku
   — şablondaki `uptrace_all` yalnız yer tutucudur:

   ```sql
   SELECT cluster, shard_num, replica_num, host_name, port
   FROM system.clusters ORDER BY cluster, shard_num, replica_num;
   ```

3. **Shard listesi.** Backfill `INSERT`'leri **shard-yereldir**:
   `spans_local` / `metric_points_local` okur, `..._local` yazar.
   `ON CLUSTER` **DEĞİL** (INSERT bir DDL değildir), `clusterAllReplicas`
   **DEĞİL**. Her shard'ın **bir** replikasına bağlanıp aynı komutu ayrı
   ayrı koşacaksın; replikasyon aynı shard'ın diğer replikalarını kendisi
   halleder.

4. **Disk payı.** Backfill ham tabloları tarar ve rollup tablolarına yeni
   part'lar yazar; birleşme (merge) tamamlanana kadar geçici olarak hem
   eski hem yeni part'lar diskte durur. Başlamadan önce her shard'da boş
   alanı gör:

   ```sql
   SELECT name, formatReadableSize(free_space) AS free,
          formatReadableSize(total_space)      AS total
   FROM system.disks;
   ```

   Yazılacak hacmin kaba göstergesi ham tarafın aynı penceredeki boyutudur:

   ```sql
   SELECT partition, formatReadableSize(sum(bytes_on_disk)) AS size, sum(rows) AS rows
   FROM system.parts
   WHERE database = currentDatabase() AND table = 'spans_local' AND active
   GROUP BY partition ORDER BY partition;
   ```

5. **Pencerenin tavanını ölç, dokümana güvenme.** Ham retansiyon canlı bir
   ayardır: `internal/config/config.go` varsayılanı `spans_days: 7` /
   `metrics_days: 7`, ama `system_settings` (`retention.spans`,
   `retention.metrics`) bunu ezer ve prod'da genelde ezer.
   `0004_backfill.sql` içindeki "varsayılan 30 gün" notu **bayattır** —
   kaynağa değil, tabloya sor:

   ```sql
   -- Ham veri gerçekten nereye kadar geriye gidiyor?
   SELECT min(time) AS oldest_span FROM spans_local;
   SELECT min(time) AS oldest_point FROM metric_points_local;
   ```

   Backfill `FROM` sınırın bu değerden eski olamaz.

---

## 2. Kapsam — hangi zincir backfill edilir, hangisi EDİLMEZ

`0004_backfill.sql` **üç** taban şablonu + **bir** kaskad şablonu içerir:

| Şablon | Hedef | Kaynak | Parça | Partition | Hedef TTL |
|---|---|---|---|---|---|
| **A** | `rollup_spans_narrow_10s_local` | `spans_local` | **saatlik** | `toStartOfHour(ts)` | 7 gün |
| **B** | `rollup_spans_wide_1m_local` | `spans_local` | **günlük** | `toDate(ts)` | 14 gün |
| **C** | `rollup_metrics_1m_local` | `metric_points_local` | **günlük** | `toDate(ts)` | 14 gün |
| **kaskad** | `rollup_spans_narrow_1m_local` | `rollup_spans_narrow_10s_local` | günlük | `toDate(ts)` | 14 gün |

Kapsam dışında kalanlar — **hepsi bilinçli**:

- **Şablon B yalnız `0002_rollup_wide.sql` elle uygulanmışsa geçerlidir.**
  Geniş aile de sihirbazın dışındadır (`migrations/embed.go`); tablo
  yoksa B'yi atla.

- **5m ve 1h kademeleri için şablon YOK.** 0004 yalnız 10s→1m kaskadını
  örnekler ve dosyanın kendi notu açık: 90 günlük / 13 aylık kademeler
  backfill'le değil, **zamanla** dolar. Aynı desenle (alt kademeden
  `...MergeState` ile) türetilebilirler, ama kaynak kademenin kendi TTL'i
  neyi taşıyorsa onunla sınırlıdırlar.

- **`rollup_spans_narrow_1m` için 7 günden eski pencere.** Kaskadın
  kaynağı 10s tablosu ve onun TTL'i 7 gün. 8-14. günler için 10s'te
  okunacak veri yoktur; o aralığı doldurmak istiyorsan şablon A'yı
  hedefi ve `INTERVAL`'i değiştirerek `spans_local`'dan doğrudan 1m'e
  türetmen gerekir — **0004 bu türevi içermez**, türetirsen §7
  doğrulamasını mutlaka koş.

- **ROUTE ailesi (`0008_rollup_metrics_route.sql`) backfill EDİLMEZ.**
  0004, 0008'den önce yazıldı ve `rollup_metrics_route_1m/5m/1h` için
  **hiçbir şablon içermez**. Sonucu net söyle: bu zincir yalnız kurulum
  anından itibaren dolar, ondan önceki **route geçmişi yalnız
  `metric_points` retansiyonu kadardır — varsayılan 7 gün** (0008'in
  kendi başlığı da bu TTL'i gerekçe gösteriyor) ve o pencere de rollup'a
  değil ham tabloya bakar. "Bu endpoint'in geçen ayki ortalaması"
  sorusunun cevabı backfill'den sonra da **yoktur**; 14g/90g/13ay
  kademeleri t₀'dan sonra doğal akışla dolar.

---

## 3. Sıra — bozma

1. DDL uygulanır, MV'ler canlı akışı yazmaya başlar (**t₀**).
2. **Taban** tablolar geçmişten doldurulur (A, B, C — birbirinden
   bağımsız, paralel koşulabilir).
3. **Kaskadlar** ALT kademeden doldurulur (10s→1m→5m→1h). Taban bitmeden
   kaskadı başlatma — yarım tabandan türetilen kaskad sessizce eksik olur.

---

## 4. `TO` sınırını belirle — çift sayımı önleyen tek adım

MV'ler backfill sırasında **canlı kalabilir**; tehlike yalnız t₀'ın
düştüğü bucket'tadır. `TO` sınırını, canlı MV'nin yazdığı **ilk**
bucket'a eşitle (WHERE `ts < {to}` olduğu için o bucket hariç kalır):

```sql
-- Canlı MV'nin yazdığı en eski bucket = backfill'in TO sınırı
SELECT min(ts) AS t0_bucket FROM rollup_spans_narrow_10s_local;
SELECT min(ts) AS t0_bucket FROM rollup_spans_wide_1m_local;
SELECT min(ts) AS t0_bucket FROM rollup_metrics_1m_local;
```

Bu sınırdaki bucket, MV bucket'ın ortasında devreye girdiyse **kısmi**
kalır (eksik sayar). Kabul edilen taviz bu: kısmi bir bucket, çift
sayılmış bir bucket'tan iyidir — `AggregatingMergeTree` mükerrer state'i
**toplar**, yani çift sayım kalıcı ve sessiz bir hatadır.

---

## 5. Parçalı uygulama

Bağlantı (her shard için ayrı, `<...>` alanlarını doldur):

```bash
CH="clickhouse-client --host <shard-N-host> --port 9000 \
  --user <user> --password <password> --database <db>"
$CH --query "SELECT hostName(), currentDatabase()"
```

Pencere (UTC, parça sınırları partition sınırına **hizalı**):

```bash
FROM='2026-07-01 00:00:00'   # ham verinin izin verdiği en eski nokta (§1.5)
TO='2026-07-08 00:00:00'     # §4'te bulunan t0_bucket
```

> Aşağıdaki döngüler GNU `date` varsayar (Linux / ClickHouse pod'u).
> macOS'ta `gdate` kullan.

### 5.1 Şablon A — dar 10s tabanı (SAATLİK parçalar)

```bash
cur="$FROM"
while [ "$(date -u -d "$cur" +%s)" -lt "$(date -u -d "$TO" +%s)" ]; do
  nxt=$(date -u -d "$cur UTC + 1 hour" '+%Y-%m-%d %H:%M:%S')
  echo "[A] $cur → $nxt"
  $CH --param_from="$cur" --param_to="$nxt" --query "
INSERT INTO rollup_spans_narrow_10s_local
SELECT
    toStartOfInterval(time, INTERVAL 10 SECOND)          AS ts,
    service_name,
    kind                                                 AS span_kind,
    status_code,
    toUInt64(count())                                    AS span_count,
    toUInt64(countIf(status_code = 'error'))             AS error_count,
    toUInt64(sum(duration))                              AS duration_sum,
    quantilesTDigestState(0.5, 0.95, 0.99)(duration)     AS q_state,
    argMaxState(trace_id, duration)                      AS slow_exemplar,
    anyIfState(trace_id, status_code = 'error')          AS err_exemplar
FROM spans_local
WHERE time >= {from:DateTime} AND time < {to:DateTime}
GROUP BY ts, service_name, span_kind, status_code
SETTINGS max_insert_threads = 4, max_execution_time = 600;" || {
    echo "DURDU: $cur parçası yarım — §6'ya git"; break; }
  cur="$nxt"
done
```

### 5.2 Şablon B — geniş 1m tabanı (GÜNLÜK parçalar)

Yalnız `0002_rollup_wide.sql` uygulanmışsa. Yoğun günlerde parçayı
6 saate indir (partition günlük olduğu için kurtarma yine gün bazındadır).

```bash
cur="$FROM"
while [ "$(date -u -d "$cur" +%s)" -lt "$(date -u -d "$TO" +%s)" ]; do
  nxt=$(date -u -d "$cur UTC + 1 day" '+%Y-%m-%d %H:%M:%S')
  echo "[B] $cur → $nxt"
  $CH --param_from="$cur" --param_to="$nxt" --query "
INSERT INTO rollup_spans_wide_1m_local
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
WHERE time >= {from:DateTime} AND time < {to:DateTime}
GROUP BY ts, service_name, span_kind, status_code, endpoint, channel_code, function_code
SETTINGS max_insert_threads = 4, max_execution_time = 600;" || {
    echo "DURDU: $cur parçası yarım — §6'ya git"; break; }
  cur="$nxt"
done
```

> `channel_code` / `function_code` **iki yazımı da** okur (v0.9.626):
> prod küçük harf basıyor, yalnız BÜYÜK harf okuyan bir backfill boyutu
> boş doldururdu. Bu satırları sadeleştirme.

### 5.3 Şablon C — metrik 1m tabanı (GÜNLÜK parçalar)

```bash
cur="$FROM"
while [ "$(date -u -d "$cur" +%s)" -lt "$(date -u -d "$TO" +%s)" ]; do
  nxt=$(date -u -d "$cur UTC + 1 day" '+%Y-%m-%d %H:%M:%S')
  echo "[C] $cur → $nxt"
  $CH --param_from="$cur" --param_to="$nxt" --query "
INSERT INTO rollup_metrics_1m_local
SELECT
    toStartOfInterval(time, INTERVAL 1 MINUTE)     AS ts,
    metric, service_name, instrument, unit, bucket_bounds,
    toUInt64(count()), sum(value), min(value), max(value),
    argMaxState(value, time),
    sumForEachState(bucket_counts)
FROM metric_points_local
WHERE time >= {from:DateTime} AND time < {to:DateTime}
GROUP BY ts, metric, service_name, instrument, unit, bucket_bounds
SETTINGS max_insert_threads = 4, max_execution_time = 600;" || {
    echo "DURDU: $cur parçası yarım — §6'ya git"; break; }
  cur="$nxt"
done
```

### 5.4 Kaskad — dar 10s → 1m

**Yalnız 5.1 bittikten ve §7 doğrulaması geçtikten sonra.** Kaynak 10s
tablosu olduğu için pencere en fazla 10s TTL'i kadar geriye gider (7 gün).

```bash
cur="$FROM"
while [ "$(date -u -d "$cur" +%s)" -lt "$(date -u -d "$TO" +%s)" ]; do
  nxt=$(date -u -d "$cur UTC + 1 day" '+%Y-%m-%d %H:%M:%S')
  echo "[kaskad 10s→1m] $cur → $nxt"
  $CH --param_from="$cur" --param_to="$nxt" --query "
INSERT INTO rollup_spans_narrow_1m_local
SELECT
    toStartOfInterval(ts, INTERVAL 1 MINUTE)                       AS ts,
    service_name, span_kind, status_code,
    sum(span_count), sum(error_count), sum(duration_sum),
    quantilesTDigestMergeState(0.5, 0.95, 0.99)(q_state),
    argMaxMergeState(slow_exemplar),
    anyIfMergeState(err_exemplar)
FROM rollup_spans_narrow_10s_local
WHERE ts >= {from:DateTime} AND ts < {to:DateTime}
GROUP BY ts, service_name, span_kind, status_code
SETTINGS max_insert_threads = 4, max_execution_time = 600;" || {
    echo "DURDU: $cur parçası yarım — §6'ya git"; break; }
  cur="$nxt"
done
```

### Ayarlar hakkında

Şablonların `SETTINGS`'i iki ayar taşır: `max_insert_threads = 4`,
`max_execution_time = 600`. 0004'ün başlık notu üçüncü bir öneri daha
yazar (`optimize_on_insert = 1`) ama ifadelerin kendisinde yoktur —
açıkça istiyorsan `SETTINGS` listesine ekle. Büyük parçalarda 600 sn
yetmezse **süreyi büyütmek yerine parçayı küçült**: zaman aşımına uğrayan
bir `INSERT` yarım parça bırakır ve §6'ya girersin.

---

## 6. ⚠ İDEMPOTENT DEĞİL — yarım parçayı kurtarma

**Bir parçayı iki kez koşmak veriyi ikiye katlar.** Hedefler
`AggregatingMergeTree`'dir: aynı anahtara ikinci kez yazılan state
üzerine yazmaz, **toplanır**. Zaman aşımı / bağlantı kopması / `Ctrl-C`
ile yarım kalmış bir parça geri alınmadan tekrarlanırsa o aralıktaki
sayılar sessizce şişer — ve şişme, ham veriyle karşılaştırmadan (§7)
görünmez.

Kural: **yarım parçayı tekrarlamadan önce o aralığın partition'ını
DROP et.** Bu yüzden parça sınırları partition sınırlarına hizalıdır
(10s → saat, diğerleri → gün): bir parça = bir partition, kurtarma tek
komut.

**Adım 1 — partition kimliğini tabloya sor** (tahmin etme):

```sql
SELECT partition, count() AS parts, sum(rows) AS rows,
       formatReadableSize(sum(bytes_on_disk)) AS size
FROM system.parts
WHERE database = currentDatabase()
  AND table = 'rollup_spans_narrow_10s_local'
  AND active
GROUP BY partition ORDER BY partition;
```

**Adım 2 — o partition'ı düşür.** `partition` kolonundaki değeri
birebir kullan:

```sql
-- Saatlik partition (dar 10s):
ALTER TABLE rollup_spans_narrow_10s_local
  DROP PARTITION '2026-07-01 13:00:00'
  SETTINGS max_partition_size_to_drop = 0;

-- Günlük partition (wide_1m / metrics_1m / narrow_1m):
ALTER TABLE rollup_spans_wide_1m_local
  DROP PARTITION '2026-07-01'
  SETTINGS max_partition_size_to_drop = 0;
```

`max_partition_size_to_drop = 0` hacim koruma eşiğini (varsayılan 50 GB)
devre dışı bırakır — kod tabanının kendi emsali (`internal/chstore/purge.go`
`purgeGuard`, `reset.go`). Bu olmadan büyük bir partition'ın DROP'u
sessizce reddedilir ve sen "düşürdüm" sanarak tekrar yazarsın.

**Adım 3 — parçayı baştan koş.** DROP replike bir ALTER'dır, aynı
shard'ın replikalarına kendisi yayılır; ama **her shard'da ayrı** koşman
gerekir.

> Dikkat: DROP PARTITION o aralıkta **canlı MV'nin** yazdığı satırları da
> siler. §4'e uyulduysa (TO = t₀ bucket'ı) backfill partition'ları ile
> canlı partition'lar zaten ayrışıktır; sınırdaki partition'ı düşürmen
> gerekiyorsa önce içeriğine bak.

---

## 7. Doğrulama

Her parça grubundan sonra, en azından pencerenin başında/ortasında/sonunda
birer örnek aralık için koş. `<f>` / `<t>` doldurulmuş bir aralıktır.

**7.1 — Satır ve bucket sayısı (boş mu, doldu mu):**

```sql
SELECT count() AS rows, min(ts) AS first_bucket, max(ts) AS last_bucket
FROM rollup_spans_narrow_10s_local
WHERE ts >= '<f>' AND ts < '<t>';
```

**7.2 — Ham ↔ rollup EŞİTLİĞİ (asıl kapı).** Sayaç kolonları toplama
agregasyonudur, yani **birebir eşit** olmalıdır; eşit değilse ya parça
eksik ya çift yazılmıştır:

```sql
-- Dar 10s tabanı
SELECT
  (SELECT count()          FROM spans_local
     WHERE time >= '<f>' AND time < '<t>')                       AS raw_spans,
  (SELECT sum(span_count)  FROM rollup_spans_narrow_10s_local
     WHERE ts   >= '<f>' AND ts   < '<t>')                       AS rollup_spans,
  (SELECT countIf(status_code = 'error') FROM spans_local
     WHERE time >= '<f>' AND time < '<t>')                       AS raw_errors,
  (SELECT sum(error_count) FROM rollup_spans_narrow_10s_local
     WHERE ts   >= '<f>' AND ts   < '<t>')                       AS rollup_errors;
```

```sql
-- Metrik 1m tabanı
SELECT
  (SELECT count()           FROM metric_points_local
     WHERE time >= '<f>' AND time < '<t>')                       AS raw_points,
  (SELECT sum(point_count)  FROM rollup_metrics_1m_local
     WHERE ts   >= '<f>' AND ts   < '<t>')                       AS rollup_points;
```

```sql
-- Kaskad: 1m, kaynağı olan 10s ile aynı toplamı vermeli
SELECT
  (SELECT sum(span_count) FROM rollup_spans_narrow_10s_local
     WHERE ts >= '<f>' AND ts < '<t>')                           AS base_10s,
  (SELECT sum(span_count) FROM rollup_spans_narrow_1m_local
     WHERE ts >= '<f>' AND ts < '<t>')                           AS cascade_1m;
```

Okuma:

- **rollup < raw** → parça eksik (yarım kaldı ya da hiç koşulmadı). O
  partition'ı §6 ile düşür, parçayı tekrarla.
- **rollup > raw** → **çift yazım**. Kesinlikle §6: partition DROP + tek
  sefer tekrar. "Fark küçük, geçer" deme — MV canlı olduğu için fark
  büyümez ama kalıcıdır.
- **rollup = raw** → parça sağlam.

**7.3 — Yüzdelikleri eşitlik kapısı olarak KULLANMA.** `q_state`
`quantilesTDigest` state'idir; TDigest **yaklaşıktır** ve rollup'tan
okunan p95, ham `quantile(0.95)` ile bit-bit aynı çıkmaz. Yüzdelikte
beklenen kontrol "aynı büyüklük mertebesinde ve monoton" — birebir
eşitlik değil. Kapı sayaç kolonlarıdır (7.2).

**7.4 — Shard bütünlüğü.** Yukarıdakiler shard-yereldir. Bittikten sonra
Distributed sarmalayıcıdan bütüne bak:

```sql
SELECT toStartOfDay(ts) AS day, sum(span_count) AS spans
FROM rollup_spans_narrow_10s          -- Distributed
WHERE ts >= '<f>' AND ts < '<t>'
GROUP BY day ORDER BY day;
```

Günler arasında bir shard'ın katkısı düşüyorsa o shard'ın parçası eksiktir.

---

## 8. Geri alma

Backfill'in geri alınması = **yazılan partition'ları düşürmek**. Ayrı bir
"undo" yoktur.

```sql
-- Pencerenin tamamı için, tablo tablo, partition partition (§6 adım 1 ile
-- listeyi al), HER SHARD'DA:
ALTER TABLE rollup_spans_narrow_10s_local DROP PARTITION '<partition>'
  SETTINGS max_partition_size_to_drop = 0;
```

Canlı MV'yi de durdurmak gerekiyorsa (backfill ingest'i sıkıştırıyorsa)
`/admin/clickhouse` → **Rollup katmanı → Geri al** yalnız MV'leri düşürür;
tablolar ve veri kalır (`RollupRollback`: "kurulumu geri al" değil,
"yazımı kes" düğmesi). MV'ler düşükken geçen süre rollup'ta **kalıcı
boşluk** bırakır — o aralığı sonradan yine bu runbook'la doldurman gerekir.

Ham tablolara (`spans_local`, `metric_points_local`) bu prosedürün hiçbir
adımı **yazmaz**; backfill'i geri almak ham veriyi etkilemez.

---

## 9. Süre ve kaynak beklentisi

Süre **pencere boyutuna bağlıdır** — ham tablonun o penceredeki satır
sayısı, shard sayısı ve diskin okuma hızı belirler. Bu runbook bir tahmin
vermiyor: ölçülmemiş bir süre, operatörün bakım penceresini yanlış
planlamasına yol açar.

Ölçmek için **ilk parçayı tek başına** koş ve gerçek rakamı oradan
büyüt — parça süresi pencere boyunca kabaca doğrusaldır:

```sql
SELECT query_duration_ms, read_rows, written_rows,
       formatReadableSize(memory_usage) AS mem
FROM system.query_log
WHERE type = 'QueryFinish' AND query LIKE 'INSERT INTO rollup_%'
ORDER BY event_time DESC LIMIT 5;
```

Yük notları:

- Backfill canlı ingest ile **aynı** ClickHouse'u kullanır. Trafiğin düşük
  olduğu pencereyi seç; `max_insert_threads` şablonlarda 4'tür, sıkışma
  görürsen bu sayıyı düşür.
- Parçaları shard'lar arasında paralel koşabilirsin (bağımsızdırlar); aynı
  shard içinde **sıralı** koş — paralel parçalar merge baskısını ve yarım
  parça riskini birlikte artırır.
- Bir parça zaman aşımına uğruyorsa çözüm `max_execution_time`'ı büyütmek
  değil, **parçayı küçültmek** (§5 ayarlar notu).
