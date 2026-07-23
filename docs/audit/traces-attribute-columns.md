# Audit: Traces sayfası dinamik span-attribute kolonları performans sorunu

**Tarih:** 2026-07-23 · **Durum:** FAZ 1 — audit, kod değişikliği yok · **Semptom (operatör):**
CHANNEL_CODE / FUNCTION_CODE gibi attribute kolonları açıkken sayfa çok yavaş; davranış
"sayfadaki 50 trace için değil, seçili aralıktaki TÜM span'ler için attribute çekiliyormuş"
gibi. Kolonlar kapalıyken normal.

---

## 1. Kök neden — özet

İki ayrı mekanizma var ve **ikisi de yalnız kolon AÇIKKEN devreye giriyor**. Hangisinin
çalıştığı, listenin MV fast-path'te mi ham yolda mı olduğuna bağlı:

| Yol | Ne zaman | Kolon açıkken ne oluyor | Maliyet |
|---|---|---|---|
| **A. MV fast-path** (`trace_summary_5m`) | Filtre yok + pencere ≥5dk + `env` boş + search boş | İkinci sorgu `fillTraceExtras` çalışır — **zaman sınırı YOK, LIMIT YOK** ([repo.go:2243](../../internal/chstore/repo.go#L2243)) | Partition pruning imkânsız → TÜM retention'ın granülleri bloom'dan süzülür; geçen her granülde 4 şişman array kolonu decompress |
| **B. Ham yol** (spans `GROUP BY trace_id`) | `?env=` doluysa (**prod'da global env picker yüzünden neredeyse HER ZAMAN**), search/filters varsa, count=exact ise | Attribute projeksiyonu liste sorgusunun İÇİNE inline edilir ([repo.go:2089-2104](../../internal/chstore/repo.go#L2089)) | Penceredeki **her span satırı** için `attr_keys+attr_values+res_keys+res_values` decompress — **LIMIT'ten ÖNCE** |

Operatörün algısı doğru ve hatta iyimser: B yolunda pencerenin tüm span'leri, A yolunda
**pencere bile değil, tüm retention** taranıyor.

Prod'da baskın acı **B**: global `?env=` seçili olduğu için liste hep ham yolda; kolon
açmak, pencere-genelinde fat-array taramasına dönüşüyor.

---

## 2. Frontend akışı

- Kolon seçimi `extraCols: string[]` state'i; URL `?cols=` ile senkron
  ([Traces.tsx:208-209, 294](../../frontend/src/pages/Traces.tsx#L208)); localStorage'da
  **kolon seçimi tutulmuyor** (yalnız genişlik/sort, `storageKey:'traces-list'`).
- **Toggle = tam liste refetch.** `extraCols` liste-fetch effect'inin dep dizisinde
  ([Traces.tsx:355](../../frontend/src/pages/Traces.tsx#L355)); ayrı enrichment isteği yok.
  Kolon ekle/çıkar → aynı `GET /api/traces` sorgusu yalnız `extraAttrs=` parametresi
  farkıyla baştan atılır.
- İstekte **trace_id listesi gitmiyor** — yalnız zaman aralığı + filtreler + sayfalama;
  `extraAttrs=CHANNEL_CODE,FUNCTION_CODE` tek virgüllü parametre
  ([Traces.tsx:346](../../frontend/src/pages/Traces.tsx#L346), api.ts:2282). Sunucu 8 kolonla
  sınırlar (isSafeAttrKey allow-list, [api.go:3291-3302](../../internal/api/api.go#L3291)).
- Sayfa 50 satır (`limit:50, offset:page*50`); total varsayılan kapalı (`count:skip`).
- Yan etki: her toggle, `extraAttrs`'lı ve `extraAttrs`'sız iki AYRI server-cache girdisi üretir.

## 3. Backend — iki yol, yan yana SQL

Rota `GET /api/traces` [api.go:594](../../internal/api/api.go#L594) → `s.getTraces`
[api.go:3230](../../internal/api/api.go#L3230) → `Store.GetTraces`
[repo.go:1866](../../internal/chstore/repo.go#L1866); WHERE üretici `buildGetTracesWhere`
[repo.go:1708](../../internal/chstore/repo.go#L1708); MV yolu `getTracesFromMV`
[repo.go:2390](../../internal/chstore/repo.go#L2390); extras doldurucu `fillTraceExtras`
[repo.go:2219](../../internal/chstore/repo.go#L2219).

### 3a. Ham yol — kolon KAPALI (repo.go:2114-2139, interpolasyon çözülmüş)

```sql
SELECT trace_id,
       coalesce(nullIf(anyIf(name, (parent_id = '' OR parent_id = '0000000000000000')), ''), any(name)) AS root_name,
       coalesce(nullIf(anyIf(service_name, (parent_id = '' OR parent_id = '0000000000000000')), ''), any(service_name)) AS root_svc,
       min(time) AS trace_start,
       (max(toUnixTimestamp64Nano(time) + duration) - toUnixTimestamp64Nano(min(time))) / 1e6 AS dur_ms,
       count() AS span_count,
       max(if(status_code = 'error', 1, 0)) AS has_error
FROM spans
WHERE time >= ? AND time <= ?          -- SEÇİLİ PENCERENİN TAMAMI (sayfa aralığı değil)
GROUP BY trace_id
ORDER BY trace_start DESC
LIMIT 51 OFFSET ?                       -- LIMIT, GROUP BY + ORDER BY'dan SONRA
SETTINGS max_execution_time = 30
```

Dar kolonlar (çoğu LowCardinality) — hızlı taban durum.

### 3b. Ham yol — kolon AÇIK (aynı sorgu + inline projeksiyon, repo.go:2089-2104)

```sql
  ..., max(if(status_code='error',1,0)) AS has_error,
  anyIf(coalesce(nullIf(attr_values[indexOf(attr_keys, 'CHANNEL_CODE')], ''),
                 nullIf(res_values[indexOf(res_keys, 'CHANNEL_CODE')], '')),
        has(attr_keys,'CHANNEL_CODE') OR has(res_keys,'CHANNEL_CODE')) AS extra_0,
  ...extra_1 (FUNCTION_CODE)...
FROM spans WHERE time >= ? AND time <= ?  GROUP BY trace_id ...
```

- Attribute erişimi **Map değil paralel array**: `attr_values[indexOf(attr_keys, k)]` —
  dokunulan her granülde `attr_keys + attr_values + res_keys + res_values` **dört kolonun
  tamamı** okunup ZSTD(3) açılır (`attr_values` tek başına tablonun ~%25'i, store.go:1852).
- Bu projeksiyon **LIMIT'ten önce, penceredeki her span için** hesaplanır.
- Root-span sınırı yok: WHERE'de kind/parent_id filtresi yok; root yalnız `anyIf` ile seçilir.
- Well-known key'ler (http.method→`http_method` vb., WellKnownTraceCol repo.go:1592) tek
  dar kolon okur — sorun yalnız custom key'lerde (CHANNEL_CODE/FUNCTION_CODE bu sınıfta).

### 3c. MV yolu — kolon AÇIK: fillTraceExtras (repo.go:2243, İHLAL)

```sql
SELECT trace_id,
       anyIf(coalesce(nullIf(attr_values[indexOf(attr_keys, ?)], ''),
                      nullIf(res_values[indexOf(res_keys, ?)], '')),
             has(attr_keys, ?) OR has(res_keys, ?)) AS extra_0
FROM spans
WHERE trace_id IN (?, ?, ..., ?)        -- ~51 id; ZAMAN SINIRI YOK, LIMIT YOK
GROUP BY trace_id
SETTINGS max_execution_time = 15
```

CLAUDE.md sabit kuralı ("spans sorgusu: time-bounded WHERE + LIMIT") ihlal.
`PARTITION BY toDate(time)` budanamaz → tüm retention taranır; `idx_trace
bloom_filter(0.01)` granül başına ~%1 yanlış-pozitifi 51 id ile bileştirir (~%40'a kadar
granül geçirir) ve geçen her granülde 4 şişman kolon açılır. Distributed'da ayrıca
**tüm shard'lara fan-out** (spans shard anahtarı `service_name`, trace_id değil).

## 4. Şema gerçekleri

- `spans`: `PARTITION BY toDate(time)`, `ORDER BY (service_name, time)` (store.go:586-590).
  trace_id PK'de yok; erişim `idx_trace trace_id TYPE bloom_filter(0.01) GRANULARITY 4`
  skip index'iyle (yalnız eşitlik; prefix v0.9.82'de bu yüzden kaldırıldı).
- `attr_keys Array(LowCardinality(String))` + `attr_values Array(String) CODEC(ZSTD(3))`
  (+ res_* ikizi). Tek attribute erişimi = 4 array kolonunun tamamı.
- **Materialized-promote emsali 3 dalga:** `cluster` (v0.8.132), `db_stmt_hash` (v0.8.375),
  `ex_match/ex_type/ex_msg/ex_stack` (v0.8.566) — FAZ 2C önerisinin kanıtlanmış şablonu.
- Skip index'ler: idx_trace(bloom), idx_name/kind/db_system/status(set), idx_http_status(minmax).
  ALTER'la eklenenler yalnız yeni part'lara işler (MATERIALIZE INDEX bilinçli yok).
- Distributed: `spans` = Distributed wrapper, shard anahtarı `cityHash64(service_name)` →
  trace_id sorguları shard-budamasız fan-out; trace-level MV'ler (`trace_summary_5m`
  `ORDER BY (time_bucket, trace_id)`, shard `cityHash64(trace_id)`) tam bu açığı kapatmak
  için var.

## 5. Ölçüm (lokal CH, gerçek koşum)

Ortam: docker CH 24.8, 36.030 span; pencere 2026-06-27 09:00-18:00 (10.718 span / 10.603
trace). Key: `http.url` (9.924 kez var; WellKnownTraceCol'da YOK → gerçek array yolu).
3'er koşum, `system.query_log` `type='QueryFinish'`, `log_comment` ile ayıklandı:

| Senaryo | read_rows | read_bytes | avg dur | avg memory |
|---|---|---|---|---|
| Dar liste (3a) | 11.590 | 831.615 (812 KiB) | 26,0 ms | 8,72 MiB |
| Attr erişimli (3b) | 11.590 | **5.795.760 (5,53 MiB)** | 52,3 ms | 9,76 MiB |
| **Oran** | 1,00× | **6,97×** | ~2× | 1,12× |

read_rows aynı (pruning aynı) — maliyet satır sayısından değil, **her satırda fat-array
decompress**'inden. 36k-span lokalde 6,97× byte; milyar-span prod penceresinde bu oran
sayfayı kilitleyen fark. (Süre lokalde gürültülü; asıl kanıt read_bytes.)

**Prod ölçüm talimatı:** (1) Yoğun pencere: `SELECT toStartOfHour(time) h, count() FROM spans
WHERE time > now()-INTERVAL 24 HOUR GROUP BY h ORDER BY count() DESC LIMIT 5 SETTINGS
max_execution_time=30`. (2) Gerçek key (zaman sınırlı): `SELECT arrayJoin(attr_keys) k,
count() FROM spans WHERE time > now()-INTERVAL 1 HOUR GROUP BY k ORDER BY count() DESC
LIMIT 10` — seçilen key WellKnownTraceCol'da OLMAMALI. (3) §3a/§3b SQL'lerini pencereyle
güncelleyip 3'er kez koş (`SETTINGS log_comment='olcum_narrow|olcum_attr'`).
(4) `SYSTEM FLUSH LOGS`; sonra: `SELECT log_comment, count(), round(avg(read_rows)),
round(avg(read_bytes)), formatReadableSize(avg(read_bytes)), round(avg(query_duration_ms),1)
FROM system.query_log WHERE type='QueryFinish' AND event_time > now()-INTERVAL 15 MINUTE
AND log_comment LIKE 'olcum_%' GROUP BY log_comment`. Distributed'da initiator node'da koş;
`clusterAllReplicas(<cluster>, system.query_log)` ile shard-bazlı kırılım alınabilir.

## 6. Önerilen çözüm (FAZ 2 — onay sonrası)

**A. İki fazlı sorgu, tek handler** (operatör spec'i birebir uygulanabilir, kod destekliyor):
1. Faz-1: dar liste sorgusu (3a) → 50 satır + trace_id'ler + satırların **gerçek
   min/max timestamp'i**.
2. Faz-2: extras SADECE o trace_id'ler için; WHERE'e **faz-1 min/max'ı** (±güvenlik payı,
   trace süresi için max'a +5dk) → partition + bloom pruning birlikte çalışır. Ham yoldan
   inline projeksiyon KALDIRILIR; `fillTraceExtras` zaman-sınırı + LIMIT kazanır ve TEK
   sorguda tüm key'ler (zaten öyle). İki yol da (MV + ham) aynı faz-2'yi kullanır → tek kod yolu.
   - `span_id IN` hedeflemesi: attribute'ların yalnız root'ta olduğu garanti değil (anyIf
     tüm span'lara bakıyor) — Faz-2'de `GROUP BY trace_id` korunur; id+zaman sınırlı sorguda
     bu zaten ucuz. (Root-only'ye daraltma, davranış değişikliği olur — reddedildi, §7.)

**B. Frontend:** toggle'da tam refetch yok — eldeki 50 satır + bilinen trace_id'ler +
timestamp aralığıyla yalnız faz-2'yi tetikleyen hafif istek (aynı endpoint, opsiyonel
`trace_ids` parametresi faz-1'i atlatır) → dönen extras satırlara merge. `DEFAULT_TRACE_COLUMNS`
+ localStorage persist (aşağıda AÇIK SORU).

**C. Materialized kolon (opsiyonel, ayrı adım):** CHANNEL_CODE/FUNCTION_CODE için
`LowCardinality(String) MATERIALIZED attr_values[indexOf(attr_keys,'…')]` migration'ı
hazırlanır ama UYGULANMAZ; kod tarafında materialize-map (cluster kolonunun
`hasClusterCol` probe + fallback şablonu, repo.go:326) — sorgu üretici materialize ise
native kolon, değilse array erişimi (tek kod yolu). Distributed-column-safety kuralı
geçerli (probe + koşullu ifade; prod `spans_local`'a chstore ALTER işlemeyebilir).

### Reddedilen alternatifler
- **Kolon başına ayrı sorgu:** N kolon = N tarama; tek faz-2 sorgusu zaten tüm key'leri alıyor.
- **Yalnız materialize (A'sız):** her yeni attribute için migration ister; import edilen
  keyfi key'lerde (ColumnManager serbest seçim) ölçeklenmez. Materialize, sık kullanılan
  2-3 key için optimizasyon katmanı olarak kalmalı.
- **trace_summary MV'sine attribute eklemek:** MV `ORDER BY (time_bucket, trace_id)`
  agregasyon durumları taşıyor; keyfi key seti MV şemasına gömülemez (kardinalite + şema churn).
- **GLOBAL IN / shard-hedefleme:** spans shard anahtarı service_name — trace_id ile
  shard budaması yapısal olarak yok; v0.5.440'ta bilinçli terk edilmiş.

## 7. Değişecek dosyalar + tahmini etki

| Dosya | Değişiklik | Tahmin |
|---|---|---|
| `internal/chstore/repo.go` | Ham yoldan inline extras'ı çıkar; `fillTraceExtras`'a zaman-sınırı+LIMIT + iki yolun ortaklaşması | ~80 satır |
| `internal/api/traces_extras.go` (yeni) | `registerXxxRoutes` desenine uygun ayrı dosya; `extra_attributes`+`trace_ids` parametreleri (api.go BÜYÜMEZ) | ~60 satır |
| `internal/chstore/gettraces_where_test.go` (+yeni test) | Üretilen SQL assert'leri: faz-2 zaman-sınırı, inline projeksiyon yokluğu | ~60 satır |
| `frontend/src/pages/Traces.tsx` | toggle→enrichment (dep'ten extraCols çıkar), merge, DEFAULT_TRACE_COLUMNS + localStorage | ~50 satır |
| `frontend/src/lib/api.ts` | enrichment çağrısı / trace_ids parametresi | ~10 satır |
| (C, ayrı) `internal/chstore/store.go` | Hazır ama uygulanmayan migration + materialize-map | ~40 satır |

Etki: ham yolda kolon-açık maliyeti dar sorguya iner (ölçülen 6,97× byte farkı yok olur;
extras yalnız ~51 trace'in dar zaman diliminde). MV yolunda retention-genelinde tarama →
sayfa-dilimi taramasına iner. Toggle UX'i anında (liste yeniden çekilmez).

## 8. Risk / geriye dönük uyumluluk

- `extraAttrs` API sözleşmesi korunur (opsiyonel kalır); `trace_ids` yeni opsiyonel parametre.
- CSV export aynı üreticiden geçiyor — extras'lı export faz-2'yi büyük id listesiyle
  çağırır; export'ta id listesi sayfayla sınırlı değil → export yolu için pencere-sınırlı
  faz-2 varyantı korunmalı (aynı fonksiyon, farklı bound kaynağı).
- Cache: extras ayrı istekte → liste cache'i kolon setinden bağımsızlaşır (HIT oranı artar);
  extras isteğinin kendi kısa TTL'li cache key'i (trace_id set digest'i — FNV, v0.5.187 kuralı).
- Faz-2 zaman payı: trace'in son span'i pencereden taşabilir → max'a +5dk pay (trace süre
  cap'iyle uyumlu); test edilecek.
- Davranış değişikliği yok: extras semantiği (anyIf, attr→res fallback) birebir korunur.

## 9. AÇIK SORU (operatöre — FAZ 2B için)

`DEFAULT_TRACE_COLUMNS` ne olsun? Mevcut sabit kolonlar: Trace/Root, Service, Start,
Duration, Spans, Status. Öneri seçenekleri:
1. Default = yalnız mevcut sabit set (attribute kolonu default'ta YOK — en hızlı taban).
2. Default'a CHANNEL_CODE + FUNCTION_CODE eklensin (senin iş akışın için; faz-2 maliyeti
   artık sayfa-sınırlı olduğundan makul).
Karar senin — hangi kolonlar default olsun?

> **KARAR (operatör, 2026-07-23):** Seçenek 1 — default yalnız mevcut sabit set;
> attribute kolonu default'ta YOK (`DEFAULT_TRACE_COLUMNS = []`, Traces.tsx).
> Kullanıcı seçimi `traces-extra-cols` localStorage anahtarında persist;
> öncelik URL `?cols=` > localStorage > default.

---

## 10. FAZ 2C — hazır migration (UYGULANMADI)

FAZ 2 kod tarafını hazırladı: `traceAttrMaterialized` map'i
(internal/chstore/repo.go, `traceExtrasProjection`'ın 1. katmanı) **boş**
gemide. Aşağıdaki migration'ı uygulayıp map'e girdiyi ekleyen kod değişikliği
yapılana dek sorgu üretici array yolunu kullanmaya devam eder — davranış
değişmez, risk sıfır.

### Migration SQL (CHANNEL_CODE / FUNCTION_CODE)

```sql
ALTER TABLE spans ADD COLUMN IF NOT EXISTS attr_channel_code
  LowCardinality(String)
  MATERIALIZED attr_values[indexOf(attr_keys, 'CHANNEL_CODE')];

ALTER TABLE spans ADD COLUMN IF NOT EXISTS attr_function_code
  LowCardinality(String)
  MATERIALIZED attr_values[indexOf(attr_keys, 'FUNCTION_CODE')];
```

Uygulandığında kod tarafındaki tek değişiklik:

```go
var traceAttrMaterialized = map[string]string{
    "CHANNEL_CODE":  "attr_channel_code",
    "FUNCTION_CODE": "attr_function_code",
}
```

(+ boot-probe, aşağıda). Projeksiyon üretici o andan itibaren
`anyIf(attr_channel_code, attr_channel_code != '')` üretir — 4 şişman array
kolonu decompress'i yerine tek dar LowCardinality kolon.

### Distributed-column-safety (ZORUNLU — prod'u iki kez kıran sınıf)

Feedback `distributed-column-safety`: yeni spans kolonu okuyan HER sorgu yolu
**gün-1 distributed-güvenli** olmalı. `cluster` kolonu emsali
(repo.go:306-342, `hasClusterCol` + `clusterExpr`):

1. **Boot probe:** `hasXCol` probe'u (örn. `SELECT attr_channel_code FROM
   spans LIMIT 0` try/catch ya da `system.columns` sorgusu) — kolon okunabilir
   DEĞİLSE map'e girdi EKLENMEZ, üretici array yoluna düşer. Map'in doldurulması
   sabit literal değil, probe sonucuna bağlı olmalı.
2. **Prod gerçeği:** canlı hedef external Distributed CH (`cluster_name`
   çoğu zaman UNSET) — chstore'un ALTER'ı `spans_local`'a **işlemeyebilir**;
   Distributed wrapper kolonu görse bile per-shard tablo görmeyince sorgu
   code 47 ile düşer. Bu yüzden probe şart, `IF NOT EXISTS` yetmez.
3. **Koşullu ifade:** eski part'lar materialized kolonda '' okur (cluster
   emsalindeki gözlem) — `anyIf(col, col != '')` boşları zaten atlar; retention
   penceresi kolonu tanımayan part kalmayana dek eski trace'lerde değer eksik
   görünebilir. Tam eşdeğerlik istenirse cluster emsalindeki
   `coalesce(nullIf(col,''), <array-fallback>)` şekli kullanılabilir (o zaman
   array kolonları da okunur — kazanım yeni part'larda kalır).
4. **MV yok:** bu kolonlar hiçbir MV'ye girmiyor → `dropCombinedMV` /
   MODIFY QUERY riski yok; ALTER tek başına yeterli.
5. Kod migration'ı **ÇALIŞTIRMAZ** — bu bölüm, operatör onayıyla ayrı bir
   v0.9.X diliminde uygulanmak üzere hazır bekler.
