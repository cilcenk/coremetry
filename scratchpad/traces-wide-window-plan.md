# /traces geniş pencere timeout'u — uygulama planı

**Operatör raporu:** `/traces?range=2d&sort=status&cols=channel_code%2Cfunction_code`
— filtresiz, servissiz. Üstteki histogram hızlı, alttaki liste
`HTTP 500: query processing: failed to read packet from <ch>:9000 … i/o timeout`.
Kurulum ölçeği: pencerede 6747.7M span, 19.0M hata span'i, %0.28 hata oranı.

Bu doküman dört bağımsız tasarımın ve dört adversaryal incelemenin sentezidir;
buradaki her sayı **canlı 2-shard chc-0/chc-1 kurulumunda** (1,528,417 satır
`trace_summary_5m_local`, ~6 gün) `system.query_log` üzerinden yeniden ölçüldü.

---

## 1. Kök neden (iki cümle)

`trace_summary_5m` `ORDER BY (time_bucket, trace_id)` ile sıralı
(`internal/chstore/store.go:2866-2869`), ama Stage 1 `GROUP BY trace_id` yapıyor —
`trace_id` sıralama anahtarının **ikinci** kolonu, yani prefix değil, dolayısıyla
`optimize_aggregation_in_order` devreye giremiyor ve `GROUP BY` varlığı
`optimize_read_in_order`'ı da işlevsiz bırakıyor; ClickHouse `LIMIT`'i uygulayabilmek
için önce **penceredeki her trace_id için hash tablosu kurmak** zorunda, yani maliyet
pencere genişliğiyle doğrusal.

Kanıtı `internal/chstore/repo.go:2476-2485` (`traceStage1LightSQL`):

```sql
SELECT trace_id
FROM trace_summary_5m
WHERE time_bucket >= ? AND time_bucket <= ?
GROUP BY trace_id
ORDER BY max(time_bucket) DESC        -- ← agregat, sıralama anahtarı değil
LIMIT ?
SETTINGS max_execution_time = 15,
         optimize_read_in_order = 1,          -- dekoratif
         optimize_aggregation_in_order = 1    -- dekoratif
```

**Ölçüm (2 gün penceresi, canlı chc-0):** 875 ms / **1,145,198 read_rows** / 41 MiB —
`read_rows` penceredeki satır sayısının **%100'ü**. `LIMIT 5000` çıktıyı sınırlıyor,
işi asla sınırlamıyor. Operatörün "sanki son 2 günün hepsini sorgulamaya çalışıyor"
tespiti kelimesi kelimesine doğru.

### Brief'teki hipotezin düzeltmesi (önemli, çünkü hata sort'a özgü değil)

Hipotez sonucu doğru, **atfı yanlış**. `sort=status` Stage 1'in `has_error` dalına
hiç ulaşmıyor: `repo.go:2656` `traceSortRecencySliced("status")` → true, ardından
`repo.go:2667` `s1f.Sort, s1f.Order = "time", "desc"` ile sort'u eziyor ve
budget'ı `traceRecencySliceN = 5000` yapıyor. Yani:

- `traceStage1LightSQL`'in `duration` / `spans` / `status` dalları
  (`repo.go:2459-2464`) **prod'da erişilemez** — tek çağıran `repo.go:2670` her zaman
  `Sort ∈ {"", "time"}` geçiriyor. Sadece `traces_stage1_test.go` onları görüyor.
- `has_error` yalnızca **Stage 2'nin** `ORDER BY`'ı olarak, ≤5000 önceden daraltılmış
  grup üzerinde çalışıyor — bedava.
- Dolayısıyla **varsayılan `sort=time` de tam aynı derecede sınırsız**. Bu bir
  "status sıralaması bug'ı" değil, iki aşamanın da tamamını kapsayan bir plan hatası.

### 500'ün neden `i/o timeout` olarak göründüğü

`repo.go:1965-1969` fast-path'in **her** hatasını yutuyor (`max_execution_time = 15`
abort'u dahil) ve ham `spans` yoluna düşüyor. Ham yol
`SETTINGS max_execution_time = 60` (`repo.go:2200`) taşıyor, ama clickhouse-go
`ReadTimeout: 30 * time.Second` (`store.go:376`) ve bu deadline okuma fazı başına
**bir kez** kuruluyor, progress paketlerinde tazelenmiyor — yani sunucu tarafı koruma
**yapısal olarak erişilemez**, istemci her zaman önce ölür. Ham yol sürücü hatasını
sarmalamadan döndürüyor (`repo.go:2131` / `:2148`), bu yüzden operatörün gövdesinde
`stage1-light:` / `stage2:` öneki yok. Toplam bekleme ≈ 15s + 30s.

### Histogram neden hızlı

`internal/chstore/spanmetric.go:403-414` `service_summary_5m`'den `GROUP BY bucket, gk`
okuyor: grup kardinalitesi = bucket × servis (binler), ve GROUP BY anahtarı sıralama
anahtarının **prefix'i** → `AggregatingInOrderTransform` devrede. Liste ise `trace_id`'ye
göre gruplar: yüz milyonlarca grup, in-order yol yok. Aynı pencere, iki farklı büyüklük
mertebesi.

---

## 2. Seçilen tasarım ve neden diğerlerini yendi

### Seçilen: **şema değişikliği olmayan sorgu yeniden yazımı — read-in-order dilim + zaman-taban daraltmalı Stage 2 + clipping DETEKSİYONU**

Üç parça:

1. **Stage 1'den `GROUP BY`'ı kaldır.** `ORDER BY time_bucket DESC` sıralama anahtarının
   prefix'i; agregasyon yoksa CH en yeni granülleri okur ve LIMIT'te durur.
   Doğrulandı (`EXPLAIN PIPELINE`, chc-0):
   `MergeTreeSelect(pool: ReadPoolInOrder, algorithm: InReverseOrder)` + `ReverseTransform`,
   `AggregatingTransform` **yok**.
2. **Stage 2'yi dilimin gerçek zaman aralığıyla darallt.** `trace_id IN (…)` tek başına
   hiçbir granül budamıyor (`trace_summary_5m`'de `trace_id` üzerinde skip index **yok**
   — `store.go:2896`'daki "uses the bloom filter on trace_id" yorumu **bayat ve yanlış**,
   o bloom `spans` DDL'inde). Budamayı yapan şey zaman tabanı.
3. **Clipping'i varsayma, TESPİT et.** Stage 2'nin alt sınırını daraltmak, tabanın
   altında satırı olan trace'lerin `dur_ms` / `span_count` / `trace_start` /
   `has_error` değerlerini **sessizce kırpar**. Stage 2 artık `min(time_bucket)`'ı da
   döndürür; sayfa satırlarından herhangi biri taban bucket'ındaysa taban geometrik
   olarak genişletilip sorgu tekrarlanır (en fazla 2 kez, `f.From`'da klemplenir).

**Ölçümler (canlı, `system.query_log`, 2 gün penceresi):**

| sorgu | ms | read_rows | bellek |
|---|---|---|---|
| gemideki Stage 1 (`GROUP BY trace_id`) | 875 | **1,145,198** (pencerenin %100'ü) | 41.2 MiB |
| yeni dilim (read-in-order, LIMIT 15000 satır) | **19** | 350,574 | **6.0 MiB** |
| gemideki Stage 2 (4000 id, tam pencere) | 151 | **1,145,778** | 20.0 MiB |
| yeni Stage 2 (aynı 4000 id, daraltılmış taban) | **69** | **99,094** | 20.0 MiB |

Yani uçtan uca 1,026 ms → 88 ms ve okunan satır 2.29M → 0.45M — **prod'dan ~500×
küçük** bir tabloda. Asıl önemli olan mutlak sayı değil, **ölçekleme**: eski maliyet
∝ penceredeki trace sayısı, yeni maliyet ∝ (part sayısı + sayfa).

Satır→trace şişmesi ölçüldü: 15,000 satır → **8,593 farklı trace = 1.75×** (2 shard ×
birleştirilmemiş part'lar). Bu sayı tasarımdaki over-provisioning katsayısını besliyor.

### Reddedilen açılar ve tam olarak neyi yapamadıkları

**(A) Zaman-dilimleme (count() probe ile pencere daraltıp tek atışta agregasyon).**
Fikir doğru — `count()` probe'unun gerçekten O(1) olduğu doğrulandı — ama ölümcül
kusuru kendi doğruluk yamasında: sınır-üstü trace'lerin kırpılmaması için eklediği
1 saatlik lookback **`HAVING` ile, yani agregasyondan SONRA** uygulanıyor. Sonuç:
`GROUP BY` hash tablosu 1 bucket'ı değil **13 bucket'ı** tutuyor. Kendi tablosunda
ölçüldüğünde 53 KiB → 16.95 MiB (**325× bellek**). Prod'da bu 7.6–30M grup ve
projeksiyona göre 1.2–17 GB'lık spill ayarsız bir `GROUP BY` demek — timeout'u
**code 241 / OOM**'a çevirir, üstelik bu sayfa v0.8.70'te tam da bu yüzden
`tracesSpillSettings` almıştı (`repo.go:1863`). Ayrıca `traceStage2MaxIDs`'i silmeyi
öneriyordu; `exportTracesCSV` `Limit`'i 50,000'e kadar çıkarabildiği için
(`api.go:3508-3514`) bu doğrudan v0.8.363'ün code 62'sini geri getirirdi.
**Grafted:** ölçülen genişlet-ve-tekrarla mekanizması (aşağıda clipping deteksiyonunun
motoru olarak).

**(B) Şema açısı (`trace_recent_index`, 100 ms slot'lu yeni MV).**
En hızlı çözüm ve gerçek bir kusuru buluyor: prod yoğunluğunda tek bir 5 dakikalık
bucket ~1.22M trace içeriyor, dolayısıyla 5 dakikalık granülerlik "en yeni N"i
**keyfi** kılıyor. Ama şema **taşıyıcı değil**: ölçtüğüm gibi hızın iki yarısı da
sıfır DDL ile elde ediliyor. Karşılığında ~0.8–1.5 TB depolama, en sıcak ingest
yolunda 12. MV, yeni bir `spans` insert hata modu, coverage-gate + shard başına
backfill runbook'u istiyor. CLAUDE.md "şema değişikliği gerektirmeyen bir çözüm
varsa onu tercih et" diyor; bir timeout ticket'ına binmesin.
**Grafted:** bayat `store.go:2896` yorumunun düzeltilmesi + alt-bucket sıralama
kusurunun **açık soru** olarak kayda geçmesi (§7).

**(C) Semantik açı (Status başlığını sort'tan filtreye çevirmek + keyset paging).**
`finalizeAggregation(error_count_state) > 0` satır-düzeyi ön filtresi gerçek bir
buluş ve kanıtlanabilir şekilde tam (countIf partial'ları negatif olamaz, dolayısıyla
"herhangi bir partial > 0" ⟺ "trace'te ≥1 hata span'i var"). Ama S1 (Status başlığının
anlamını canlıda değiştirmek) iki operatör vetosunun tam sınıfına giriyor —
`feedback-operations-table-classic` (v0.9.61 canlıda reddedildi, v0.9.67 revert) ve
`feedback-exception-detail-classic` — ve brief'in "frontend redesign yok" kısıtını
ihlal ediyor. S3 (keyset cursor) kendi S2'siyle çelişiyor: 60 dakikalık slack ile
Stage 1'in 510 satırlık bütçesi prod'da tamamen cursor'dan **yeni** satırlarla dolar,
Stage 2'nin `HAVING (trace_start, trace_id) < cursor`'ı hepsini eler → "Older" ikinci
sayfada **boş** döner.
**Grafted:** `finalizeAggregation` ön filtresi (S2), hasError yolu için.

---

## 3. Kesin düzenlemeler

Tüm dosya yolları `/Users/cenk/Documents/gotrace/` altında.

### 3.1 `internal/chstore/repo.go` — `traceSliceScanSQL` (YENİ, `traceStage1LightSQL`'in yanına, ~2451)

```go
// traceSliceScanSQL — v0.9.X Stage 1: GROUP BY YOK.
// trace_summary_5m ORDER BY (time_bucket, trace_id) olduğu için
// ORDER BY time_bucket sıralama anahtarının PREFIX'i; agregasyon araya
// girmediğinde CH en yeni granülleri okuyup LIMIT'te durur
// (MergeTreeSelect pool: ReadPoolInOrder, algorithm: InReverseOrder).
// Eski GROUP BY trace_id şekli penceredeki HER trace için hash tablosu
// kuruyordu — 2g penceresinde ölçülen 1,145,198 read_rows (pencerenin
// %100'ü) vs bu şeklin 350,574'ü.
// LIMIT SATIR sayar, trace değil: bir trace her (5dk bucket × shard ×
// birleşmemiş part) için bir satır tutar (yerelde ölçülen 1.75×).
// Çağıran Go tarafında dedup eder ve kesim bucket'ını ("cut") döndürür.
func traceSliceScanSQL(order string, errorsOnly bool, shardSkip string) string
```

Üretilen SQL:

```sql
SELECT trace_id, time_bucket
FROM trace_summary_5m
WHERE time_bucket >= ? AND time_bucket <= ?
  /* errorsOnly iken: AND finalizeAggregation(error_count_state) > 0 */
ORDER BY time_bucket DESC          -- order=asc ise ASC
LIMIT ?
SETTINGS max_execution_time = 10,
         optimize_read_in_order = 1,
         <shardSkipSetting()>
```

Notlar:
- `optimize_aggregation_in_order` **yazılmaz** — agregasyon yok, yazmak yalan olur.
- `s.shardSkipSetting()` eklenir; her kardeş cluster okuması taşıyor
  (`cluster.go:115-132`, v0.8.213 external-Distributed kurtarması).
- `max_execution_time = 10 < ReadTimeout 30s` → sunucu koruması gerçekten erişilebilir.
- `finalizeAggregation(error_count_state) > 0` satır düzeyinde çalışır, `GROUP BY`
  gerektirmez; countIf partial'ları negatif olamayacağı için yanlış negatif/pozitif
  yok. Stage 2'deki `HAVING countMerge(error_count_state) > 0` **kalır** (kesinlik
  emniyeti; Stage 1 tanım gereği üst küme).

### 3.2 `internal/chstore/repo.go` — `traceRecencySlice` (YENİ metot)

```go
// traceRecencySlice penceredeki en yeni `want` farklı trace id'sini ve
// dilimin kesildiği bucket'ı döndürür. Read-in-order, agregasyon yok.
// exhausted=true → sunucu satırları bitti, dilim = pencerenin tamamı
// (sıralama global, "ranked within" ipucu bastırılır).
func (s *Store) traceRecencySlice(ctx context.Context, f TraceFilter, want int) (
    ids []any, cut time.Time, exhausted bool, err error)
```

Bütçe sabitleri (saf, tablo-testli):

```go
// Bir trace her (5dk bucket × shard × birleşmemiş part) için bir
// trace_summary_5m satırı işgal eder. Yerel 2-shard Distributed
// kurulumda ölçülen: 15,000 satır → 8,593 farklı trace = 1.75×.
// Shard sayısıyla ve part parçalanmasıyla büyür; 3× ilk tahmin,
// dilim kısa gelirse ÖLÇÜLEN oranla BİR kez tekrar denenir — yanlış
// tahminin bedeli bir ucuz sorgu, asla yanlış sayfa değil.
const traceSliceOverprovision = 3
const traceSliceMaxRows       = 250_000  // patolojik şişme dilimi
                                         // tekrar taramaya çeviremesin

func traceSliceRetryBudget(scanned, kept, want int) (int, bool)
```

### 3.3 `internal/chstore/repo.go:2652-2698` — no-service dalının gövdesi

**Önce** (özet): `traceStage1LightSQL` → `GROUP BY trace_id` → ids.

**Sonra:**

```go
if f.Service == "" && holders == "" {
    ranked := traceSortRecencySliced(f.Sort)
    budget, budgetOK := traceStage1Budget(f.Offset, pageLimit, stage1Limit)
    if ranked {
        budget, budgetOK = traceRecencySliceN, true   // v0.8.369 semantiği KORUNUR
    }
    if len(having) == 0 {
        // Hızlı yol: agregatsız dilim.
        ids, cut, exhausted, err := s.traceRecencySlice(ctx, f, budget)
        ...
    } else {
        // hasError → dilim + finalizeAggregation ön filtresi (satır düzeyi).
        // rootOnly / minMs / maxMs → agregat Stage 1 KALIR, ama dilimin
        // cut'ıyla pencere daraltılır ve yetersiz id gelirse ×4 geometrik
        // genişletilir (≤3 deneme, f.From'da taban).
    }
}
```

`traceStage1LightSQL` **silinmez** — `rootOnly` / `minMs` / `maxMs` yolu onu kullanmaya
devam eder. Ama ölü `duration` / `spans` / `status` dalları (`:2459-2464`) ve
`:2448-2450`'deki bayat "caller falls back to the single-stage scan" yorumu silinir
(tek çağıran artık yalnız `time` geçiriyor).

`traceStage2MaxIDs` (6000) ve `traceStage1Budget` **korunur** — CSV export yolu
(`api.go:3508-3514`, `Limit` 50,000'e kadar) IN-list byte bütçesini hâlâ onlara
dayandırıyor.

### 3.4 `internal/chstore/repo.go:2742-2765` — Stage 2 zaman tabanı + clipping deteksiyonu

**Önce:**

```sql
FROM trace_summary_5m
WHERE trace_id IN (…) AND time_bucket >= ? AND time_bucket <= ?   -- ? = f.From, f.To
GROUP BY trace_id HAVING …
ORDER BY <sortExpr> <dir> LIMIT ? OFFSET ?
SETTINGS max_execution_time = 15, optimize_read_in_order = 1, optimize_aggregation_in_order = 1
```

**Sonra:**

```sql
SELECT trace_id,
       argMaxIfMerge(root_name_state)                              AS root_name,
       argMaxIfMerge(root_service_state)                           AS root_svc,
       minMerge(trace_start_state)                                 AS trace_start,
       (maxMerge(trace_end_state) -
        toUnixTimestamp64Nano(minMerge(trace_start_state))) / 1e6  AS dur_ms,
       countMerge(span_count_state)                                AS span_count,
       toUInt8(countMerge(error_count_state) > 0)                  AS has_error,
       min(time_bucket)                                            AS first_bucket  -- YENİ
FROM trace_summary_5m
WHERE trace_id IN (…) AND time_bucket >= ? AND time_bucket <= ?   -- ? = s2From, f.To
GROUP BY trace_id HAVING …
ORDER BY <sortExpr> <dir> LIMIT ? OFFSET ?
SETTINGS max_execution_time = 12,
         <shardSkipSetting()>,
         <tracesSpillSettings>            -- IN-list'siz derin-sayfa varyantı için
```

Go tarafı:

```go
// Stage 2, dilim DIŞINDAKİ satırları da görmeli; yoksa
// minMerge(trace_start_state) / dur_ms / span_count / has_error KIRPILIR.
// Sabit bir lookback VARSAYIMDIR ve bu kurulumda test edilemez: yerelde
// ölçülen maksimum trace bucket-açıklığı 5 dakika, 565,546 trace'in
// SIFIRI 5 dakikayı aşıyor. Bu yüzden varsaymıyoruz — tespit ediyoruz.
const traceSliceLookbackBuckets = 12          // ilk deneme = 1 saat
const traceSliceLookbackMaxRetry = 2          // ×8, ×64 → f.From'da klemp

s2From := cut.Add(-5 * time.Minute * traceSliceLookbackBuckets)
if s2From.Before(f.From) { s2From = f.From }

// Sayfa satırlarından herhangi biri taban bucket'ında oturuyorsa
// (first_bucket <= s2From) o satır KIRPILMIŞ olabilir → tabanı genişlet
// ve tekrar çalıştır. s2From == f.From iken kırpma imkânsız, tekrar yok.
```

`order=asc` için ayna: `s2To = cut + lookback`, `s2From = f.From`.

**Derin-sayfa uçurumu** (`repo.go:2523-2536`): `budgetOK == false` iken bugün
IN-list'siz **tam pencere** taraması yapılıyor — dosyadaki en pahalı sorgu, tam da
operatörün geriye sayfalama isteğinde tetikleniyor. Artık her zaman bir `cut` var,
dolayısıyla o tek-aşamalı Stage 2 `[cut − lookback, to]` üzerinde çalışır.
Doğruluk argümanı (kandidatın verdiğinden farklı ve gerçekten geçerli): dilim
`budget = 2*(offset+pageLimit)` farklı trace verecek şekilde kesildiği için
daraltılmış pencere istenen sayfayı **tam olarak kapsar**; dışarıda kalan trace'lerin
`max(time_bucket)`'ı `cut − lookback`'ten küçük, dolayısıyla `trace_start`'ları da
küçük → sıralamada sayfadan sonra gelirler. Kırpma riski aynı deteksiyonla kapatılır.

### 3.5 `internal/chstore/repo.go:2762-2783` — eksik `rows2.Err()`

Stage 2 döngüsünden sonra `rows2.Err()` **hiç kontrol edilmiyor** (Stage 1 için
`:2619` ve `:2685`'te var). ClickHouse header bloğunu hemen gönderdiği için
Stage 2 istisnası (159 / 241) **akış sırasında** gelir: `Query()` başarılı olur,
`rows2.Next()` sadece `false` döner, fonksiyon kısa/boş sayfayı `err == nil` ve
**HTTP 200** ile döndürür. Bu, perf düzeltmesinden bağımsız bir doğruluk bug'ı ve
düzeltilmezse yeni tasarım "timeout"u "sessizce boş liste"ye çevirir.

```go
if err := rows2.Err(); err != nil {
    return nil, 0, false, fmt.Errorf("stage2: %w", err)
}
```

### 3.6 `internal/chstore/repo.go:1965-1969` — yutma-ve-tekrar deneme kapısı

**Bu tek başına operatörün 500'ünü üreten şey.** Bugün fast-path'in *her* hatası
(15s abort dahil) loglanıp 6.7B ham span üzerinde `GROUP BY trace_id`'ye düşüyor;
o sorgu `max_execution_time = 60` ile 30s'lik istemci ReadTimeout'una karşı
**tanım gereği bitemez**. Sonuç: bir yavaş sorgu yerine iki, ve gerçek sebep gizli.

```go
// v0.9.X — fast-path hatasında ham yola SADECE "MV yok / kolon yok"
// sınıfında düş. Timeout (159) / bellek (241) / iptal (394) hatasında
// ham yol AYNI işi 6.7B satır üzerinde yapar ve istemci ReadTimeout'u
// (store.go:376, 30s) sunucu korumasından ÖNCE koptuğu için asla
// bitemez — operatöre code 159 yerine transport "i/o timeout" döner.
if mvFallbackEligible(err) {
    log.Printf("[chstore] trace_summary fast path failed, falling back to raw: %v", err)
} else {
    return nil, 0, false, fmt.Errorf("traces: %w", err)
}
```

`mvFallbackEligible(err error) bool` — saf, tablo-testli; CH exception kodlarına ve
"Table … doesn't exist" / "Unknown identifier" metinlerine bakar.

### 3.7 `internal/chstore/repo.go` — istemci bütçesinin altına inen sunucu kapakları

`ReadTimeout = 30s` (`store.go:376`) okuma fazı başına bir kez kurulup progress
paketlerinde tazelenmediği için ≥30 olan her `max_execution_time` **erişilemez**:

| konum | önce | sonra |
|---|---|---|
| `repo.go:2200` ham liste | `max_execution_time = 60` | `25` |
| `repo.go:2069` exact count | `30` | `25` |
| `repo.go:2071` exact count (alt dal) | `30` | `25` |
| `repo.go:2045` approx count | `30` | `25` |

### 3.8 `internal/chstore/repo.go:2318-2322` — `traceExtrasWindow`'a alt sınır slack'i

Bugün slack yalnız `to`'ya uygulanıyor; yorum "the lower bound stays exact" diyor ve
bu **kırpılmamış** `trace_start` varsayımına dayanıyor. Kırpılmış (fazla yeni) bir
`trace_start`, extras sorgusunu trace'in **kök span'inden sonra** başlatır ve
`channel_code` / `function_code` — operatörün URL'indeki tam `cols=` — sessizce boş
gelir. Deteksiyon bunu zaten onarıyor; bu savunma katmanı ikinci sıra:

```go
const traceExtrasFromSlack = 5 * time.Minute   // MV bucket genişliği
func traceExtrasWindow(from, to time.Time) (time.Time, time.Time) {
    return from.Add(-traceExtrasFromSlack), to.Add(traceExtrasToSlack)
}
```

### 3.9 `internal/chstore/store.go:2896` — bayat yorum

"2. … bounded to N traces, **uses the bloom filter on trace_id**" — `trace_summary_5m`
DDL'inde skip index **yok** (`store.go:2865-2885`); `idx_trace` bloom `spans` DDL'inde
(`store.go:597`). Yorum, Stage 2'nin neden budayamadığını yıllardır gizliyor.
Düzeltilir ve yeni zaman-tabanı mekanizmasına atıf verilir.

### 3.10 `internal/chstore/repo.go:2695` — `RankedWithin` gerçeği söylesin

```go
*f.RankedWithin = len(ids)              // sabit 5000 değil, gerçek dilim
if exhausted { *f.RankedWithin = 0 }    // dilim pencereyi tüketti → global sıralama
```

**Frontend değişikliği yok.** `Traces.tsx:1127` ipucu aynı yerde, aynı stille kalır;
sadece taşıdığı sayı doğrulaşır ve dar pencerelerde (dilim pencereyi tükettiğinde)
görünmez olur — çünkü artık yalan değil.

---

## 4. Operatörün NE fark edeceği

1. **`/traces?range=2d` 500 yerine sayfa döner.** ~15s + ~30s + HTTP 500 yerine
   yerel ölçeklemeye göre sub-saniye. Aynı düzeltme **varsayılan `sort=time`** için de
   geçerli — bug hiçbir zaman sort'a özgü değildi, dar pencereler de hızlanır.
2. **Geriye sayfalama uçurumdan düşmeyi bırakır.** Bugün ~118. sayfadan sonra dosyanın
   en pahalı sorgusu çalışıyor; artık maliyet sayfa numarasıyla orantılı. Operatörün
   birebir istediği davranış.
3. **`sort=status`'un anlamı DEĞİŞMEZ — ve zaten istedikleri şey değil.** v0.8.369
   semantiği korunuyor: en yeni 5000 trace içinde sıralanıyor. %0.28 hata oranında bu
   dilim ~14 hata trace'i içerir, yani `Status ▼` 1. sayfası ~14 ERROR rozeti gösterip
   sonra OK satırlarıyla dolar. **Bu bugün de böyle** ve düzeltmeden sonra da böyle.
   "Son 2 günün hata trace'leri" isteniyorsa doğru affordance **Errors only**
   (`hasError`) filtresidir — ve o yol bu sürümde ilk kez gerçekten hızlanıyor
   (`finalizeAggregation` ön filtresi). *Bunun sürüm notunda Türkçe yazılması şart;
   yoksa "düzeltme işe yaramadı" raporu gelmesi en muhtemel senaryo budur.*
4. **Dilim sınırındaki satır kompozisyonu değişebilir.** Hem eski hem yeni Stage 1
   5 dakikalık granülerlikte kesiyor; **hangi** trace'lerin sınır bucket'ından
   dilime girdiği farklılaşabilir. 1. sayfayı öncesi/sonrası ekran görüntüsüyle
   karşılaştıran biri bazı satırların değiştiğini görür. İki küme de eşit derecede
   geçerli "en yeni N"dir; hiçbiri yanlış değil.
5. **`order=asc` + `sort=time` seçim anahtarı kayar.** Gemideki ASC Stage 1
   `max(time_bucket) ASC` ile seçiyor; yeni ASC dilim ilk-görülene göre dedup ediyor,
   yani `min(time_bucket)`. Çok-bucket'lı trace'ler için (yerelde %0.09) farklı küme,
   sınırda 5 dakikalık kayma. Ranked sort'lar bundan etkilenmez — onlar zaten
   `desc`'e eziliyor (`repo.go:2667`), dolayısıyla `sort=duration&order=asc` hâlâ
   "**en yeni 5000 içinde** en hızlısı" demek; dilim çapası `f.Order`'a **bağlanmaz**.
6. **"ranked within newest 5,000" ipucu dar pencerelerde kaybolur** ve dilim kısa
   geldiğinde gerçek sayıyı gösterir. Sabit 5000 yalanı biter.
7. **Değişmeyen:** kolonlar, sort menüsü, URL sözleşmesi, `cols=` zenginleştirme
   çağrısı, tablo düzeni. Frontend'de tek satır redesign yok.

---

## 5. Test planı

CLAUDE.md ship-checklist #11: her bug-fix bir regresyon testi ister; ev kalıbı saf,
tablo-sürümlü SQL builder testleri (kanonik: `internal/api/cache_key_test.go`,
kardeşler: `traces_stage1_test.go`, `traces_stage2_budget_test.go`).

### 5.1 `internal/chstore/traces_slice_test.go` (YENİ)

Başlık yorumu v0.9.X'i ve operatör raporunu anmalı.

- `TestTraceSliceScanSQL`
  - `GROUP BY` içermez (**bu testin varlık sebebi** — regresyon buraya geri sızarsa
    kırılsın), `argMaxIfMerge` / `countMerge` içermez.
  - `ORDER BY time_bucket DESC` (asc varyantı `ASC`).
  - `LIMIT ?` + `max_execution_time` + zaman sınırlı `WHERE` üçlüsü.
  - `optimize_aggregation_in_order` **yazmaz** (agregasyon yok — kargo kült yasak).
  - `shardSkipSetting` değeri gömülü.
  - `errorsOnly=true` → `finalizeAggregation(error_count_state) > 0`;
    `false` → içermez.
- `TestTraceSliceRetryBudget` — tablo: dilim doluyor / kısa geliyor / ölçülen oranla
  yakınsıyor / `traceSliceMaxRows` tavanına çarpıyor / `kept == 0`.
- `TestTraceSliceLookbackWiden` — saf `nextLookback(cur, from, cut)`: ×8 büyür,
  `f.From`'da klemplenir, klemplendikten sonra `retry=false` döner.
- `TestTraceStage2ClipDetect` — saf `pageClipped(rows []TraceRow, floor time.Time) bool`:
  taban bucket'ında satır var → true; hepsi yukarıda → false; `floor == f.From` → her
  zaman false (kırpma imkânsız).

### 5.2 `internal/chstore/traces_fallback_test.go` (YENİ)

- `TestMVFallbackEligible` — tablo: CH code 159 (timeout) → false, 241 (memory) → false,
  394 (query cancelled) → false, "Table coremetry.trace_summary_5m doesn't exist" → true,
  "Unknown identifier: entry_route_state" → true, `nil` → false, sarmalanmış
  (`fmt.Errorf("stage1-light: %w", …)`) timeout → false.

### 5.3 `internal/chstore/traces_timeout_test.go` (YENİ)

- `TestServerCapsUnderClientReadTimeout` — `buildGetTracesListSQL` ve count SQL'lerini
  üretip her `max_execution_time = N`'i regex ile çekerek `N < 30` doğrular. Bu, "kapak
  istemci bütçesinin üstüne çıktı" sınıfını kalıcı olarak kapatır.

### 5.4 Mevcut testlerin güncellenmesi

- `traces_stage1_test.go`: `duration` / `spans` / `status` dalları silindiği için o
  alt-test'ler kaldırılır; `time` dalı ve `having` push-down'ı kalır (hâlâ
  `rootOnly` / `minMs` / `maxMs` yolunda kullanılıyor).
- `traces_stage2_budget_test.go`: `budgetOK == false` dalının artık daraltılmış
  pencerede tek-aşamalı çalıştığı yeni beklentiyle genişletilir.
- `traces_extras_test.go`: `traceExtrasWindow` artık alt sınıra da slack uyguluyor.

### 5.5 Kapılar

`cd frontend && npx tsc --noEmit` (FE değişikliği yok ama kapı zorunlu) →
`go build ./...` → `go test ./...` → `make audit`.

---

## 6. Gerçek veride nasıl doğrulanır

Yerel kurulum prod'dan ~500× küçük, dolayısıyla **mutlak süreler taşınmaz; taşınan şey
ölçekleme eğrisidir.** Ölçülecek şey ve hangi sayının kanıt sayılacağı:

### 6.1 Yerelde (chc-0), sürümden önce ve sonra

`SYSTEM FLUSH LOGS` sonrası `system.query_log` **medyanı** (tek ad-hoc zamanlama yalan
söyler — `feedback-perf-benchmark-discipline`), her şekil için ≥6 örnek, araya
serpiştirilerek:

1. **Stage 1 pencere-bağımsızlığı.** Dilim sorgusunu 1h / 6h / 1d / 2d / 6d için
   çalıştır. **Kanıt:** eski şeklin `read_rows`'u penceredeki satır sayısının %100'ünü
   izler (2g'de ölçülen 1,145,198), yeni şeklinki `LIMIT` + part sayısıyla sınırlı
   kalır ve pencere 6× genişlerken **6× artmaz**. Ölçülen referans:
   eski 875 ms / 1,145,198 satır → yeni 19 ms / 350,574 satır.
2. **Stage 2 granül budaması.** Aynı 4000 id'lik IN-list ile
   `EXPLAIN indexes = 1`. **Kanıt:** `Granules` satırı bugün ~208/210; daraltılmış
   tabanla tek haneli/onlu olmalı. Ölçülen: 151 ms / 1,145,778 satır →
   69 ms / 99,094 satır.
3. **Satır→trace şişmesi.** `LIMIT N` satır → kaç farklı trace.
   **Kanıt:** yerelde 15,000 → 8,593 = 1.75×. `traceSliceOverprovision = 3` bunun
   üstünde; retry'nin **tetiklenmemesi** beklenir.
4. **Kırpma deteksiyonunun yanlış-pozitif oranı.** Yerelde maksimum trace bucket
   açıklığı **5 dakika** ve 565,546 trace'in **sıfırı** 5 dakikayı aşıyor —
   yani 1 saatlik lookback burada **test edilemez**. Beklenen: deteksiyon hiç
   tetiklenmez. Bir sayaç logla (`[chstore] trace slice lookback widened …`) ki
   prod'da tetiklenme oranı görülebilsin.
5. **Sonuç kümesi eşdeğerliği.** Aynı `f` için eski ve yeni yolu çalıştırıp
   `(trace_id, trace_start, dur_ms, span_count, has_error)` yedi kolonunu karşılaştır.
   **Kanıt:** `sort=time&order=desc`'te sayfa 1'de fark 0; sınır bucket'ında
   kompozisyon farkı **beklenir ve kabul**, ama aynı trace_id için **değer** farkı
   asla kabul edilmez (kırpma = 0 olmalı).

### 6.2 Prod'da (asıl kanıt burada)

1. **Öncesi:** prod loglarında `[chstore] trace_summary fast path failed` grep'le.
   Bu satırın varlığı §3.6'nın teşhisini doğrular; sürümden sonra **sıfırlanmalı**
   (ya da yalnız gerçek MV-eksik hatalarında kalmalı).
2. **`system.query_log`'da `/traces` şekli**, sürüm öncesi/sonrası aynı saat dilimi:
   - `read_rows` **penceredeki satır sayısıyla orantılı olmaktan çıkmalı**. Kanıt
     sayısı: `?range=6h` ile `?range=2d` arasındaki `read_rows` oranı — bugün ~8×,
     sonrasında **< 2×** olmalı. Bu tek sayı, "artık son 2 günün hepsini sorgulamıyor"
     iddiasının kanıtıdır.
   - `memory_usage` yüzlerce MiB'den on MiB mertebesine düşmeli.
   - `query_duration_ms` p50/p99 (medyan, tek atış değil).
3. **Trace açıklığı gerçeği** — lookback sabitini prod verisi belirlesin:
   ```sql
   SELECT quantiles(0.999, 0.9999)(dateDiff('minute', mn, mx)), max(dateDiff('minute', mn, mx))
   FROM (SELECT trace_id, min(time_bucket) mn, max(time_bucket) mx
         FROM trace_summary_5m WHERE time_bucket >= now() - INTERVAL 1 DAY
         GROUP BY trace_id)
   SETTINGS max_execution_time = 30
   ```
   `traceSliceLookbackBuckets`'ın **başlangıç** değeri p99.99'un ≥3× üstü olmalı.
   Deteksiyon zaten emniyet ağı; bu ölçüm sadece genişletmenin nadir kalmasını sağlar.
4. **Self-telemetry** (`feedback-selftelemetry-dogfood`): `coremetry-monolithic`
   servisinde `hasError=true` span'lerde `stage1-slice:` / `stage2:` önekli hata var mı.
   Yeni öneklerin görünmesi iyi haber — hata artık sarmalanıyor ve nereden geldiği
   belli.
5. **Operatör duman testi:** tam olarak raporlanan URL
   (`/traces?range=2d&sort=status&cols=channel_code%2Cfunction_code`), sonra
   `sort=time`, sonra 5 sayfa "Older", sonra `hasError` açık aynı pencere.
   `channel_code` / `function_code` hücrelerinin **dolu** geldiği görsel olarak
   doğrulanmalı (kırpma → boş hücre sınıfının canlı kontrolü).

---

## 7. Çözülmemiş kalanlar (açık sorular, üstü örtülmüyor)

1. **Prod yoğunluğunda 5 dakikalık bucket bir taban ve alt-bucket sıralama keyfî.**
   6747.7M span / 2g ve ölçülen ~5.4–9.6 span/trace ile tek bir 5 dakikalık bucket
   ~1M trace satırı tutuyor. Dilim satırları `(time_bucket, trace_id)` sırasında
   okuduğu için **en yeni bucket'ın içinde sıralama `trace_id`'ye göre**, gerçek
   zamana göre değil. Yani `sort=time` 1. sayfası "en yeni 50" değil, "en yeni
   bucket'tan deterministik ama keyfî 50". **Bu bugün de böyle** (gemideki şekil
   `max(time_bucket)` ile aynı 5 dakikalık belirsizliği taşıyor, üstelik thread
   planlamasına bağlı olarak *deterministik bile değil*), yani bu düzeltme durumu
   kötüleştirmiyor — ama **düzeltmiyor da**. Gerçek onarım şema açısıdır
   (`(time_bucket, ince_zaman, trace_id)` sıralı bir trace indeksi, ~0.8–1.5 TB).
   **Açık soru: operatör "canlı kuyruk" olarak /traces'i okuyor mu? Okuyorsa bu
   depolama maliyeti ayrı bir karar olarak önüne konmalı.**
2. **Prod satır şişmesi bilinmiyor.** Yerelde 1.75× (2 shard, iyi birleşmiş part'lar).
   Gerçek şişme = shard sayısı × bucket'a dokunan part sayısı ve dilimin okuduğu **en
   yeni** bucket'lar en az birleşmiş olanlar. 6–20× makul. `traceSliceOverprovision = 3`
   + ölçülen tek retry yakınsar ama sayfa başına iki gidiş-dönüş demek olabilir.
   Prod'da retry oranını logla; kalıcı olarak >%20 ise sabiti yükselt.
3. **`minMs` / `maxMs` / `rootOnly` geniş pencerede hâlâ yavaş.** Agregatsız dilim bir
   `HAVING` değerlendiremez. Bu yol dilimin `cut`'ıyla daraltılıp geometrik genişletilse
   de en kötü durumda **bugünkü maliyete** eşit kalır, altına inmez. `hasError`
   `finalizeAggregation` sayesinde kurtuldu; süre için satır-düzeyi eşdeğeri **yok**
   (bir satırın kısmi süresi trace süresinin yalnızca alt sınırıdır), o yüzden
   uydurmuyoruz.
4. **`count=exact` dokunulmadı.** `showTotal` hâlâ 6.7B span üzerinde
   `count(DISTINCT trace_id)` tetikliyor ve kapağı 25s'ye çekmek onu sadece **temiz**
   hata verir hale getiriyor, hızlandırmıyor. Liste kullanılabilir hale geldikçe
   operatörün o linke tıklama olasılığı **artacak**. Ayrı bir kalem olarak kuyruğa
   alınmalı mı?
5. **`getTraceAggregate` (`repo.go:3161`) aynı hastalıktan muzdarip ve daha kötü:**
   kendi tam-pencere `GROUP BY trace_id`'sine ek olarak ham `spans` üzerinde
   `trace_id GLOBAL IN (SELECT DISTINCT trace_id FROM spans WHERE time BETWEEN …)`
   çalıştırıyor. Bu sürümün kapsamı dışı, ama operatör `view=aggregate`'e geçerse
   listeden **daha kötü** bir deneyim bulacak. Ayrı dilim.
6. **`order=asc` seçim anahtarı kayması** (§4.5) bilinçli kabul edildi. Alternatif,
   ASC dilimini `ORDER BY time_bucket ASC` yerine tam eşdeğerlik için ikinci bir
   geçişle düzeltmek — bedeli, faydasına değmiyor gibi görünüyor. **Operatör onayı
   isteniyor mu, yoksa dokümante edip geçilsin mi?**
7. **`traceStage1LightSQL`'in kalan `time` dalı** artık yalnızca post-agregat filtre
   yolunda kullanılıyor ve orada da `GROUP BY trace_id` sınırsızlığını taşıyor.
   Kısa vadede `cut` ile daraltılıyor; **uzun vadede o dalın da tamamen elenip
   elenemeyeceği açık.**
