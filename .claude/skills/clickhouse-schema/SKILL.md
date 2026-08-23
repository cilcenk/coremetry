---
name: clickhouse-schema
description: Coremetry ClickHouse guardrails — decision tree for new tables/MVs/columns, engine choice, ORDER BY and where high-cardinality columns go, the MV-first invariant, distributed-safe column adds (the two-boot contract), the four rollup families (narrow/wide span + metric/route), and the hard separation between the span-derived RED pipeline and the raw OTLP metrics pipeline. Use BEFORE any change touching internal/chstore/*.go, migrations/*.sql, an SQL string in internal/api or evaluator/anomaly/templater, a CH migration, or a new aggregate read. This skill outranks generic ClickHouse guidance (clickhouse-best-practices, clickhouse-architecture-advisor, signoz-writing-clickhouse-queries) — those describe other products and their table names do not exist here. Do NOT use for Elasticsearch/logstore queries, for diagnosing one slow page (use /perf-triage), or for frontend chart work.
---

# /clickhouse-schema — Coremetry CH sözleşmesi

Ölçek: **48 tablo · 33 MV** (20 `store.go` + 13 `migrations/`) · **13 rollup
kademesi**. Prod dış Distributed CH (2 shard × 2 replica), lokal chc-0/chc-1.

**Sayım tabanını daima yaz** — "ifade" mi, "farklı tablo" mu, "token" mu.
Aynı dosyayı üç kez sayıp üç farklı sonuç üretmek bu repoda gerçekleşti.

## 1. Karar ağacı — yeni tablo

```
Ne saklıyorsun?
├─ Ham telemetri (span/log/metrik/profil/link/exemplar)? ──► DUR.
│    12 tablo zaten var. Yeni sinyal = mevcut tabloya KOLON (§3) ya da MV (§2).
├─ Kullanıcının kaydettiği görünüm/filtre/seçim? ─────────► DUR.
│    saved_views(page='<kind>', …). Yeni şema AÇMA (invariant #5).
├─ Runtime ayarı / operatör vidası? ──────────────────────► DUR.
│    system_settings JSON blob + LoadPersisted/SavePersisted.
├─ Önceden hesaplanmış agregat? ──────────────────────────► §2
└─ Operatör/uygulama state'i
     ENGINE = ReplacingMergeTree(version)
     version UInt64 DEFAULT toUnixTimestamp64Nano(now64(9)); okuma FINAL
     ORDER BY = DEDUP ANAHTARI, münhasıran (filtre için kolon EKLEME)
     PARTITION BY: gerekmiyorsa KOYMA — bkz. Kural P1
```

## 2. Karar ağacı — agregat

```
Veriyi KİM yazacak?
├─ ClickHouse, INSERT anında ──────────► MATERIALIZED VIEW
│    Hedef bağımsız yaşayan gerçek tablo mu (ayrı TTL/purge, kaskad kaynağı)?
│      HAYIR → combined (varsayılan, 19/20)
│      EVET  → TO <tablo> + `_mv` soneki (tek örnek: span_links_reverse_mv)
├─ Go arka plan işçisi, batch INSERT ──► DÜZ TABLO + korelatör
│    ReplacingMergeTree(version), idempotent yeniden-koşum için.
│    ⚠ Semantik last-write-wins: aynı kova iki kez farklı yazılırsa TOPLAM
│      BİRİKMEZ, kaybolur. (topology_edges_5m ve 3 kardeşi böyle.)
└─ Operatör, elle SQL ile ─────────────► migrations/*.sql (§8)
```

## 3. Karar ağacı — yeni kolon

```
Hangi tablo?
├─ spans / metric_points / logs (highVolumeTables)
│   ├─ Değeri CH hesaplayabilir mi (mevcut kolonlardan türev)?
│   │   EVET → MATERIALIZED kolon. TERCİH EDİLEN.
│   │     Distributed forward'ında silinir → ingest kıramaz; rolling
│   │     deploy'da eski pod kolonu anmaz ama sunucu hesaplar → boşluk yok.
│   │   HAYIR (Go yazacak) → EXPLICIT kolon + §9 dağıtık-güvenli blok ZORUNLU.
│   │     Bu sınıf prod'u İKİ KEZ kırdı (v0.8.185 cluster, v0.8.186 op_group).
│   └─ ALTER'ı `alters` dilimine KOYMA — koşullu gönder.
├─ Bir MV'nin state kolonu ──► DROP + RECREATE (§5)
│     ⚠ ÖNCE SOR: şart mı? Yeni state kolonu rolling-deploy okuma-hatası
│       penceresi açar.
└─ Düşük hacimli state tablosu
      `ALTER … ADD COLUMN IF NOT EXISTS` — güvenli sınıf.
      ⚠ Küme kipinde DDL ERTELENİR: kolonu ekleyen boot probe'u false okur,
        o boot INSERT kolonsuz koşmalı. "Yeni kolon İKİ BOOT ister" bilinçli
        bir sözleşmedir.
```

## 4. Kanonik kurallar

### Engine

| Sınıf | Engine | Adet |
|---|---|---|
| Ham telemetri, append-only | `MergeTree` | 12 |
| State / upsert edilen her şey | `ReplacingMergeTree(version)` | 36 |
| MV hedefi | `AggregatingMergeTree` | 19 |
| Korelatör rollup | `ReplacingMergeTree(version)` | 4 |
| migrations rollup | `ReplicatedAggregatingMergeTree` | 13 |

**E1.** `adaptDDL`'in motor takas regex'i (`cluster.go:1315`) **yalnız dört
tabanı** tanır: `MergeTree | ReplacingMergeTree | AggregatingMergeTree |
SummingMergeTree`. Başkası küme kipinde **Replicated'a çevrilmez** ve
sessizce tek-node kalır.
**E2.** Tam-satır replace: bir alanı taşımayı unutursan **sıfırlanır**
(CLAUDE.md invariant #4).

### PARTITION BY

`toDate(<zaman>)` günlük telemetri + MV · `toYYYYMM()` düşük hacimli arşiv ·
**YOK** düşük hacimli state.

> **Kural P1 (en pahalı):** RMT'de PARTITION BY kolonu ORDER BY'da değilse
> **ve yeniden-yazımda değişiyorsa, FINAL kopyaları asla temizleyemez.**
> `ai_feedback`/`rca_verdicts` bunu partition'ı düşürerek çözüyor;
> `root_cause_hypotheses` v0.9.1304'te aynı yola geçti.

**P1'in ölçülmüş şekli — doğruluk bir SUNUCU AYARINA asılı.** CH 24.8'de
aynı anchor iki partition'dayken (v0.9.1304 ölçümü):

| Kol | Sonuç |
|---|---|
| `OPTIMIZE … FINAL` sonrası fiziksel satır | **2** — birleşmez, kopya TTL'e kadar ölümsüz |
| `SELECT … FINAL` (varsayılan) | 1, doğru satır |
| `SELECT … FINAL SETTINGS do_not_merge_across_partitions_select_final=1` | **2** — bayat + taze birlikte |

Yani veri "bugün doğru" olabilir ama doğruluğu yaygın bir FINAL-hızlandırma
vidasının kapalı olmasına bağlıdır. Okuma tarafı sürüm karşılaştırması
yapmıyorsa (`out[id] = h` gibi son-yazan-kazanır) o vida açıldığı an
**sessizce bayat satır** servis edilir. `LIMIT 1`'li tekil okuma maskeler,
batch okuma maskelemez.

**Sicil + test:** `tables` dilimindeki her RMT için PARTITION BY kolonları
ORDER BY'da olmalı ya da gerekçeli sicile yazılmalı — `partition_dedup_test.go`
bunu tarıyor (v0.9.1304). Kural İKİ koşullu (ORDER BY'da değil **VE**
yeniden yazımda değişiyor); tarama yalnız birincisini mekanik görür, ikincisi
gerekçeyle sicile yazılır.

⚠️ **Açık bulgu (v0.9.1304 ölçümü, düzeltilmedi):** `problems` 4560 id'nin
**21'i**, `anomaly_events` 185'in **28'i** birden çok gün-partition'ında.
`started_at` bir yerlerde yeniden yazılıyor. Doğru düzeltme oralarda
PARTITION BY'ı düşürmek DEĞİL, **yazıcının `started_at`'i yeniden yazmasını
durdurmak** — ayrı dilim.

**P2.** `toDate()`'i saatlik/dakikalık matematiğin etrafına **sarma**
(v0.6.36 birim-karıştırma tuzağı; `retention_test.go`).

### ORDER BY

> **Kural "servis önce" DEĞİL: "o yüzeyin FİLTRE ÖZNESİ önce".**
> `service_name` 19 MV'nin yalnız 12'sinde başta. `db_summary_5m`
> `db_system` ile, `messaging_summary_5m` `msg_system` ile,
> `trace_summary_5m` `time_bucket` ile başlar.

**O1 telemetri:** `(kimlik…, zaman)` — zaman **daima en sonda**.
`spans (service_name, time)` · `logs (service_name, severity_num, time)` ·
`metric_points (service_name, metric, time)`.
**O2 MV:** `(GROUP BY boyutları…, time_bucket)`, ilk kolon filtre öznesi.
**O3 state:** ORDER BY = dedup anahtarı, **münhasıran**. 36 RMT'nin 33'ü tek
kolon.
> Bilinen ihlal: `events` `ORDER BY (time, id)` — gerekçesi filtre budaması,
> bugün ölü kod. **State tablosu örneği olarak GÖSTERME.**

**O4 — yüksek kardinalite nerede durur:**

| Kolon | Konum | Gerekçe |
|---|---|---|
| `trace_id` | **SOL BAŞ** (`span_links`, `span_links_reverse`) | Nokta-araması; link satırları span hacminin %1-5'i, kopyalamak ucuz |
| `trace_id` | **SAĞ** (`trace_summary_5m`, `trace_service_index_5m`) | İki aşamalı okuma: önce index MV'den id'ler, sonra `IN (…)` |
| Serbest metin (`db_statement`) | **HİÇ** | Yerine kalıcı `db_stmt_hash` materialized kolonu; anahtarda `stmt_hash` |
| `http_route` | Sağdan bir önce; `spanmetrics_1s`'te **tamamen düşürülmüş** | 1s tier'ında kardinalite sınırı; route filtreleri 10s'e düşer |
| `client_address` | **En sonda** (`service_callers_5m`) | En yüksek kardinalite yaprakta; varyansı üst granülleri şişirmesin |

> **O5 — dağıtık RMT:** `highVolumeTables`'a giren bir RMT'nin **shard
> anahtarı ORDER BY'ın İÇİNDE olmak zorundadır**. Değilse aynı mantıksal
> satırın iki versiyonu ayrı shard'a düşer ve FINAL onları **asla**
> birleştiremez. Bugün 4/4 güvenli (topology_edges_5m, topology_op_edges_5m,
> service_callers_5m, topology_root_flows_5m).

### CODEC

Zaman `Delta, ZSTD(3)` · süre/sayaç `T64, ZSTD(3)` · metin/ID/dizi `ZSTD(3)` ·
float metrik `Gorilla, ZSTD(3)` · rollup `time_bucket` `DoubleDelta, ZSTD(3)` ·
pprof blob `ZSTD(6)`.

**C1.** `store.go` ZSTD(**3**), `migrations/*.sql` ZSTD(**1**) — tam ayrışma,
sıfır örtüşme. Makul (rollup satırları zaten agrege ve küçük) **ama hiçbir
yerde gerekçelendirilmemiş** → §11.
**C2 — LowCardinality NE ZAMAN DEĞİL:** yüksek kardinaliteli ID'ler
(`trace_id`/`span_id` — dict maliyeti değerinden büyük), serbest metin/JSON
(`rca_verdicts.body`, `alert_rules.watcher_json`).
**C3 — ev kuralı: "ölçmediysen LC yapma".** `function_code` prod distinct
sayısı ölçülmediği için bilinçli düz `String` + `bloom_filter(0.01)` skip
index; geçiş kapısı SQL'de yazılı (`uniq(...) < 10k` ölçülürse LC). Bu,
"<10k ise LC" formülünden **farklı ve daha doğru** — `name` (operation)
LC'dir ama distinct sayısı on binlerdedir.
**C4.** Nullable yok — sentinel default (`''`, `0`).

### TTL

Ham telemetri operatör ayarından (`retention.*`, varsayılan spans/logs 30g,
metrics 7g). TTL partition sınırına hizalı; `StartRetentionEnforcer` saatte
bir `DROP PARTITION` ile CH'nin merge-tabanlı TTL'ini beklemez (v0.5.320).

## 5. MV yazım kalıbı

```sql
CREATE MATERIALIZED VIEW IF NOT EXISTS <ad>_5m
ENGINE = AggregatingMergeTree()
PARTITION BY toDate(time_bucket)
ORDER BY (<filtre öznesi>, <diğer boyutlar>, time_bucket)
TTL toDate(time_bucket) + INTERVAL <N> DAY
AS SELECT
  toStartOfFiveMinute(time)                      AS time_bucket,
  service_name,
  countState()                                   AS span_count_state,
  countStateIf(status_code = 'error')            AS error_count_state,
  sumState(duration)                             AS duration_sum_state,
  quantilesTDigestState(0.5,0.95,0.99)(duration) AS duration_q_state
FROM spans
GROUP BY time_bucket, service_name
```

**Zorunlu:** `quantilesTDigestState` — rezervuar `quantilesState` **yasak**
(store.go'da 0 kullanım, doğrulandı). Okuma `*Merge()` ile.

**Kolon eklemek = DROP + RECREATE** (combined MV'de inner tablo önce,
`max_table_size_to_drop=0` ile — `dropCombinedMV`). Rolling deploy'da
okuma-hatası penceresi açılır: **hızlı roll ya da çift-kolon**.
Alternatif (yıkıcı olmayan): inner-table `ALTER` + `MODIFY QUERY` —
`reference-ch-inplace-mv-column-add` memory'sindeki yordam.

## 6. Dört rollup ailesi

> **İki değil DÖRT aile.** İki span + iki metrik. Tümü `migrations/*.sql`,
> `ON CLUSTER uptrace_all` sabit token'ı + `ReplicatedAggregatingMergeTree` +
> `ttl_only_drop_parts = 1`, her `_local` yanında `Distributed(…, rand())`.

| Aile | Dosya | Kademe | Tablo | MV |
|---|---|---|---|---|
| **DAR** (span) | `0001_rollup_narrow.sql` | 10s→1m→5m→1h | 8 | 4 |
| **GENİŞ** (span) | `0002_rollup_wide.sql` | 1m→5m→1h | 6 | 3 |
| **METRİK** | `0003_rollup_metrics.sql` | 1m→5m→1h | 6 | 3 |
| **ROUTE** | `0008_rollup_metrics_route.sql` | 1m→5m→1h | 6 | 3 |

### DAR vs GENİŞ

| | DAR | GENİŞ |
|---|---|---|
| Boyutlar | `service_name`, `span_kind`, `status_code` — **3** | + `endpoint`, `channel_code`, `function_code` — **6** |
| Taban | **10s** | **1m** (10s kademesi YOK) |
| Latency | `quantilesTDigest(0.5,0.95,0.99)` | **tDigest YOK** — `sumForEach(Array(UInt64))`, 20 kovalı SABİT eksponansiyel histogram |
| Sayaçlar | `SimpleAggregateFunction(sum, UInt64)`: span_count / error_count / duration_sum | aynı |
| Exemplar | `argMaxState(trace_id, duration)` + `anyIfState(trace_id, status='error')` | aynı |
| TTL | 7g / 14g / 90g / 13 ay | 14g / 90g / 13 ay |
| PARTITION | 10s → `toStartOfHour(ts)` (tek istisna), diğerleri `toDate(ts)` | `toDate(ts)` |
| ORDER BY | `(service_name, span_kind, status_code, ts)` | `(…, channel_code, endpoint, function_code, ts)` — **channel_code, endpoint'ten ÖNCE** |
| Skip index | — | `bloom_filter(0.01)` on `function_code` |

**Neden iki aile:** geniş ailenin enterpolasyonlu p95/p99'u **±%15-30**
bandında — *breakdown SIRALAMASI için yeterli, hassas SLO ölçümü DAR ailenin
tDigest'inden okunur; iki ailenin var olma sebebi tam bu ayrım.*
Kardinalite: 1000 servis · 1B span/gün · ~15 endpoint · 10 channel · 30
function → DAR **~9k** aktif seri, GENİŞ **~150k** (kötü 500k) = **~17×** →
geniş aile 10s'te yaşayamaz. Disk: `wide_1m` 14g 200-420 GB vs `narrow_10s`
7g 35-70 GB. Ingest CPU: +%1-2 vs +%4-8.

**`sumForEach` neden tDigest yerine:** (a) boyut — `Array(UInt64)` ≈160B/satır
vs tDigest ~1-1.5KB; (b) **tam birleştirilebilirlik** — eleman-bazlı toplama
sıra-bağımsız, tDigest birleşimi sıra-bağımlı ≤%2 hata ve 3 kademe derin
kaskadda birikirdi. Sabit ±%15-30 kuantizasyon hatası bilinçli tercih.

**Sabit kova sınırları hiçbir Go sabitinde DEĞİL** — SQL'e gömülü
(`0002_rollup_wide.sql`): `bucket i = [1ms·2^i, 1ms·2^(i+1))`, i=0..19, son
kova ≥~8.7 dk taşmalarını toplar; `greatest(duration, 1000000)` =
`log2(0)` koruması. Üç yerde tekrar eder: MV, backfill şablonu, okuma
tarafındaki `[20]` indeksi. Okuma log-lineer enterpolasyon
(`rollup_read.go`), sıfır-bölme korumalı.

> ⚠️ **0003/0008'in histogramıyla KARIŞTIRMA.** Orada kova sınırları
> OTLP'den gelir ve **akışa göre değişkendir** — bu yüzden `bucket_bounds
> Array(Float64)` **GROUP BY anahtarına girer**; okuyucu "kanonik bounds =
> ilk görülen, farklı setler ATLANIR" politikası uygular. GENİŞ ailedeki
> 20'lik eksponansiyel kova **sabittir ve Coremetry'nin kendi icadıdır**.

### Hangi soru hangi aileye — 4 ayrı seçici, merkezi router YOK

`PickRollup` (DAR/GENİŞ, `/api/rollup/red`) · `narrowRollupEligible` /
`pickNarrowRollupTier` (DAR fast-path) · `metricRollupPlan` (METRİK) ·
`metricRollupRoutePlan` (ROUTE).

```
Dims ⊆ {service_name, span_kind, status_code}       → DAR   (mode "tdigest")
Dims ∩ {endpoint, channel_code, function_code} ≠ ∅  → GENİŞ (mode "buckets")
bilinmeyen boyut                                     → HATA (sessiz yok-sayma YOK)
GENİŞ && kesin kuantil isteniyor                     → HATA (sessizce DÜŞMEZ)
```

**Kademe seçimi:** `idealStep = ceil(pencere / maxDataPoints)` → retention'ı
kapsayan adaylar → tabanı aşmayanların en büyüğü → step tabanın katına
yuvarlanır → yuvarlanmış step daha kaba bir kademeyi tam bölüyorsa oraya
**yükselt** (çıktı birebir aynı, tarama 6× az) → kapsanmıyorsa en kaba
kademe + `PartialWindow: true` ve `Reason` bunu söyler.

**Fast-path farkı:** step SABİT, yuvarlanamaz — kademe step'e uyar, tersi
değil. Uygunluk dar: yalnız `=`/`IN` dar boyut filtreleri + eşlenebilir
agg'ler; p90/p999/apdex/min/max → ham yol. İlke: **"yanlış hızlıdansa doğru
yavaş."**

**Ortak sözleşme:** metrik seçicileri **fail-open** (ham yola sessizce
düşer); `PickRollup` **fail-closed** (bilinmeyen boyut = HATA).

### Harf-duyarlılık olayı (v0.9.626) — tekrar etme

`0002`'nin ilk hali yalnız `CHANNEL_CODE` okuyordu, prod `channel_code`
yazıyor. Ölçüm: 10 dk penceresinde küçük harfli **2.67M span**, büyük harfli
**sıfır**. İki kolon sabit boş doldu — üstelik ikisi de **ORDER BY önekinde**
ve bloom index'te, yani birincil anahtarın bir bileşeni hiçbir şey
elemiyordu; `/api/rollup/red`'in channel/function filtresi sessizce boş
dönüyordu. Bugünkü `0002` iki yazımı da okur; onarım **`0007`** (MODIFY
QUERY, DROP+CREATE değil). Tam teşhis anlatısı `/perf-triage` örnek vakası.

## 7. İki boru hattı — karıştırma yasağı

| | **HAT A — span-derived RED** | **HAT B — raw OTLP metrics** |
|---|---|---|
| Yol | `spans` → 15 MV + DAR/GENİŞ | `metric_points` → 4 MV + METRİK/ROUTE |
| Okuma | `QuerySpanMetric(Multi)`, `ServiceREDSeries` | **`metricSource` seam'i** (CH \| VM) |
| Popülasyon | `kind IN ('server','consumer')` | Enstrümanın yaydığı ne ise; **`kind` kavramı YOK** |
| Latency | `quantilesTDigestMerge` — gerçek p50/p95/p99 | `sum(_sum)/sum(_count)` = gözlem-ağırlıklı ORTALAMA |
| Sayaç | Yok — her span bir olay | Kümülatif vs delta (`temporality`); cumulative seride `avg()` **anlamsız** |
| Env | `deploy_env` kolonu | CH: `res_keys` coalesce · **VM: ifade EDİLEMİYOR** → `envAmbiguous` |
| Örnekleme | Collector sampling varsa orantılı eksik | Metrikler örneklenmez — tam sayım |

> **Dayatılabilir cümle:** İki hat **adlarını, birimlerini ve çözülmüş
> serilerini birbirine ödünç VEREMEZ.** Bir yüzey ikisini yan yana
> gösterecekse: (a) her seri **kaynağıyla etiketlenir**, (b) bir hattın
> çözdüğü metrik adı diğerinin sorgusuna doğrudan geçmez — **soruya özel bir
> alan** üzerinden geçer, (c) bir hat bir daraltmayı (env, kind) ifade
> edemiyorsa cevap bunu **İLAN eder**, sessizce genişlemez.

**v0.9.1274 dersi:** throughput ucu `…_seconds_count` çözdü (rate için
doğru), `Overview.tsx` **tek** "çözülmüş metrik" alanı okuyup aynı adı
`agg=avg`'a taşıdı, VM çevirisi `_count` sonekini "operatör bilinçli seçti"
sayıp or-kollarını açmadı → kümülatif sayacın ortalaması ≈8,5M, ad
`_seconds` içerdiği için eksende **"14.2 weeks"**. Çözüm adın kendisinde:
uç iki ad döndürüyor (`metric` vs `rtMetric`), FE `rtMetric || metric`.

> **Genelleştirilmiş ders:** *Bir metrik adı, sorulacak SORUYA göre farklı
> bir seriye çözülür.* "Bu servisin metriği hangisi" **en az iki** cevaptır:
> hız-okuyan ad (`_count`, rate'lenir) ve değer-okuyan ad (aile tabanı,
> avg'lenir). Tek alan taşıyan her arayüz bu bug'ı yeniden üretir.

**Karar ağacı — "servis latency'si lazım":**

```
Percentile mi, ortalama mı?
├─ PERCENTILE ──────────────────────────► HAT A. Tartışmasız.
│    quantilesTDigestState yalnız span MV'lerinde.
└─ ORTALAMA
   ├─ Servisin kendi işi, tam kontrol ──► HAT A, agg='avg'
   │    kind filtresi MV'yi kapatır → ham spans. Maliyeti KABUL ET;
   │    alternatifi YANLIŞ POPÜLASYON.
   └─ Operatörün Grafana'sıyla aynı sayı ► HAT B, agg='avg'
        AD: throughput ucunun `rtMetric` alanı — ASLA `metric`
        groupBy yoksa servis geneli doğru; route ortalamalarının
        ortalaması YANLIŞ.

SLO / hata bütçesi ──► HAT A, kind ZORUNLU
JVM/GC, host, infra ─► HAT B ama seam DIŞI, CH-çakılı
İki hattı KIYASLA ───► İKİSİ, ayrı panel + ayrı etiket + kaynak notu
```

**MV'de `kind` yok tuzağı:** `service_summary_5m` / `operation_summary_5m`
`kind` boyutu taşımaz → giriş-span filtresi MV fast-path'ini **kapatır**,
sorgu ham `spans`'a düşer. Giriş-span ilkesinin ölçülmüş bedeli: tüm-span
p50 60ms vs giriş-span p50 807ms = **13×**; prod SLI %99.99 vs %99.85.

**Doğru blend referansı — `/databases`:** span-derived `db_summary_5m`
satırlarının yanına receiver `metric_points` satırları additive eklenir, ama
ayrı `Source="receiver"` etiketiyle + `metric_catalog` tazelik kapısıyla; env
seçiliyken receiver keşfi **tamamen atlanır** ve zarf bunu `ReceiversSkipped`
ile ilan eder — *"daraltılmamış satırları daraltılmış bir listenin yanına
koymak aynı yalan olurdu."*

## 8. Migration

**Üç yol, rakip değil — farklı sahiplik:**

| Yol | Ne | Ne zaman |
|---|---|---|
| `store.go migrate()` | Uygulamanın SAHİP OLDUĞU şema; boot'ta bildirimsel + idempotent | Uygulamanın yönettiği her tablo/kolon/MV |
| `internal/chmigrate/` | **Migration DEĞİL** — tek-node → cluster **veri kopyalayıcı**, sıfır DDL (doğrulandı) | Yalnız `--migrate-from` bayrağı |
| `migrations/*.sql` | **Operatör sahipliğinde** rollup/promotion katmanı | Sihirbaz (0001/0003/0008) veya elle (0002/0004/0005/0006/0007) |

**Adlandırma** `NNNN_<konu>.sql`, 4 haneli. Numara **sıra garantisi değil,
kimlik**: `0003` ile `0003_..._rollback` aynı numarayı paylaşır; `0008`,
`0003`'ün *halefi değil kardeşi* — ikisi yan yana yaşar. Her dosyada zorunlu
"gerekçe + şema doğrulama + tuzak" başlığı. Cluster adı sabit `uptrace_all`
token'ı, `AdaptRollupDDL` tek `ReplaceAll` ile gerçek adla değiştirir.

**Sihirbaz boot'ta ASLA koşmaz:** (a) bozuk MV ingest yoluna yazar ve
`write_failed` üretir (v0.8.185/186 sınıfı); (b) rolling deploy'da N pod aynı
`ON CLUSTER` DDL'ini yarıştırırsa kuyruk tıkanır (v0.9.613).

> **GERİ ALINABİLİRLİK — dürüst cevap: LEDGER YOK, DOWN-MIGRATION YOK.**
> `schema_migrations` / `applied_at` benzeri bir tablo yok; hangi
> migration'ın uygulandığı **şemanın kendisinden** çıkarılıyor. Geri alma =
> yeni ileri-migration (emsal: `0007`, `0002`'yi MODIFY QUERY ile yerinde
> onardı) ya da elle yazılmış `0003_..._rollback`.
> **Bu yüzden migration yazarken idempotency ve ileri-onarılabilirlik
> tasarımın parçasıdır:** `IF NOT EXISTS`, `MODIFY QUERY` tercih, DROP'tan
> kaçın, ve dosya başına "bu nasıl geri alınır" satırı yaz.

**Boot DDL elemesi dilimden BAĞIMSIZ (v0.9.1302).** Tek giriş noktası
`planDDL` iki eleyicinin bileşimi: bir ifade hangi dilimde durursa dursun,
CREATE ise nesne elemesinden, `ADD COLUMN IF NOT EXISTS` ise kolon
elemesinden geçer. Öncesinde her dilime tek eleyici bağlıydı ve iki yönlü
kusur üretiyordu — `alters`'ta 5 CREATE (v0.9.1301'de taşındı), `tables`'ta
7 ADD COLUMN; ikisi de hiç elenmiyordu. Ölçülen kazanç: ertelenen DDL
52 → 45, `tables` elemesi 49/57 → 56/57.

**Yine de kural: `CREATE TABLE` `tables` dilimine, `alters` ALTER/DROP
taşır** — artık performans için değil okunabilirlik için; yerleşimi
`ddl_slice_placement_test.go` (AST gezen) pinliyor.

⚠️ `mvs` dilimine eleyici **bilinçli uygulanmıyor**: ardından ada göre
drop+recreate yükseltme mantığı koşuyor, orada bir "zaten var" kararı tam
da tehlikeli yönde hata yapar.
⚠️ Anlık görüntü tazeliği: `alters` tarafında `system.tables`/`system.columns`
**yeniden okunur**, `tables`'ınki yeniden kullanılmaz — aradaki DDL nesne
düşürebilir (`DROP TABLE IF EXISTS feedbacks` `tables` diliminde) ve bayat
bir kayıt düşürülmüş nesnenin CREATE'ini elerdi.

## 9. Dağıtık checklist

- [ ] Motor dört tanınan tabandan biri mi (yoksa Replicated'a çevrilmez)
- [ ] `_local` + `Distributed(…, rand())` sarmalayıcı çifti kuruldu mu
- [ ] RMT ise shard anahtarı ORDER BY'ın içinde mi (**O5**)
- [ ] Yeni EXPLICIT kolon: `hasXCol` probe + koşullu INSERT + ALTER-skip
- [ ] Probe **VERİYLE** mi kanıtlıyor, yalnız varlıkla mı?
      (kolon VAR ≠ kolon DOLU — v0.9.621 dersi)
- [ ] İki-boot sözleşmesi biliniyor mu (küme kipinde DDL ertelenir)
- [ ] Alt sorgular `GLOBAL IN` / `GLOBAL JOIN` mu (`make audit` CHECK 5/5b)
- [ ] Zaman sınırı JOIN'in **ON'unda değil**, alt sorguda mı (v0.9.1285)
- [ ] `system.*` okuması `clusterAllReplicas` ile mi
- [ ] SQL telemetri tablolarını **niteliksiz** mi anıyor (`coremetry.` öneki YOK)

## 10. Sessiz bozulmalar

| Ne | Sonuç | Yakalayan |
|---|---|---|
| MV varken ham `spans` agregasyonu | Yavaş ama doğru — fatura sessiz | `make audit` CHECK 6 (kısmi) |
| RMT'de değişken partition kolonu | FINAL kopyaları temizleyemez | **hiçbiri** |
| Shard anahtarı ORDER BY dışında | Kopyalar ayrı shard'da, FINAL birleştiremez | **hiçbiri** |
| Kolon ifadesi veriyle eşleşmiyor | Sabit boş kolon, sessiz fallback | **hiçbiri** |
| Yanlış dilime DDL koymak | ~~+20 sn/ifade~~ **v0.9.1302'de kapandı** | `ddl_slice_placement_test.go` (yerleşim) |
| Cumulative seride `avg()` | Anlamsız sayı | **hiçbiri** |
| Rezervuar `quantilesState` | Bellek + hata | insan incelemesi |

## 11. Açık sorular

1. **ZSTD(3) vs ZSTD(1)** ayrışması (store.go vs migrations) hiçbir yerde
   gerekçelendirilmemiş.
2. **`spanmetrics_calls/hist/duration_5m`** (Hat B) okuyucusuz — kalıcı 0
   satır, "bozuk değil" belgeli. Silinsin mi?
3. **`root_cause_hypotheses`** Kural P1 ihlali (değişken partition kolonu) —
   bugfix dilimi mi?
4. ~~`alters` içindeki 5 CREATE TABLE~~ — **KAPANDI** v0.9.1301 (taşıma) + v0.9.1302 (eleme dilimden bağımsızlaştı).
