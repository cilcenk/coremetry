# Audit — /traces attribute kolonları yavaşlığı (2. tur)

**Tarih:** 2026-09-02 · **Durum:** ONAY BEKLİYOR — kod değişikliği yok
**Semptom (operatör):** "CHANNEL_CODE / FUNCTION_CODE gibi span attribute
kolonları eklendiğinde sayfa çok yavaşlıyor; 50 trace yerine tüm zaman
aralığı taranıyor gibi davranıyor." Ekran: prod, `range=6h`,
`sort=operation&order=asc`, `cols=openshift.cluster.name,channel_code,function_code,http.status_code`.
**Önceki tur:** [2026-07-23 audit](traces-attribute-columns-2026-07-23.md) →
FAZ 2 (liste dar, attribute'lar ayrı istekle, v0.9.19x), FAZ 2C terfi
kolonları (v0.9.198), v0.9.621-623 (kolon doldurma + filtre bağlama +
`set(0)` skip index). Bu tur o düzeltmelerin ÜSTÜNDE kalan maliyeti ölçüyor.
**Ortam notu:** ölçümler lokal minikube CH'de (6 saatte 120 K span, 7 part,
27 mark — bir FİXTURE; prod 1 B span/gün). Granül geometrisi prod'a
taşınmaz, kolon-maliyeti oranları taşınır. Prod doğrulama SQL'i §7'de.

---

## 0. Özet

| Bulgu | Kanıt |
|---|---|
| **Liste sorgusu kolon eklenince DEĞİŞMİYOR.** Kolon = ayrı istek (`GET /api/traces?traceIds=…&extraAttrs=…`), yalnız projeksiyon, filtre değil. | `Traces.tsx:482-483` ("list fetch is ALWAYS narrow"), liste effect dep dizisinde `extraCols` YOK (`:502`); `TestBuildGetTracesListSQL_NoInlineExtras` |
| **Yavaşlığın kaynağı 2. aşama (extras) sorgusu**: `FROM spans WHERE time BETWEEN [50 satırın min başlangıcı, max bitişi] AND trace_id IN (50) GROUP BY trace_id LIMIT 50`. `sort=operation` gibi zaman-dışı sıralamada 50 satır pencerenin TAMAMINA yayılır → zaman sınırı 6 saatin tamamı olur; bloom index (`idx_trace`, GRANULARITY 4) hangi granüllerin okunacağını seçer. | `traceExtrasSQL` (`repo.go:2683`), FE sınırları satırlardan türetiyor (`Traces.tsx:515-520`), EXPLAIN §1 |
| **Asıl fatura satır değil KOLON:** aynı satır kümesinde dizi yolu 84 MiB / 212 ms, terfi kolonu yolu 5.25 MiB / 66 ms — **16× bayt, 3.2× süre**. | query_log medyanı (3 koşu) §1 |
| **CHANNEL_CODE / FUNCTION_CODE ZATEN terfi kolonunda** (iki yazım da, dolu: 115 638 / 119 450) ve `set(0)` index'li. `deployment.environment.name` → `deploy_env` (dolu). | `system.columns`, `promoted_attr.go:86-87`, `WellKnownTraceCol` |
| **İki varsayılan kolon ŞİŞMAN yolda:** `openshift.cluster.name` — `cluster` MATERIALIZED kolonu VAR ve dolu ama projeksiyon yalnız `key == "cluster"` yazımını kolona yönlendiriyor (`repo.go:2650`); `function_id` — terfi kolonu yok, `attr_values[indexOf(...)]`. Operatörün varsayılan dört kolonunun İKİSİ her sayfada dizi dekompresyonu ödüyor. | `traceExtrasProjection` (`repo.go:2642-2670`), `DEFAULT_TRACE_COLUMNS` |
| Pencereyi daraltmak (per-trace / 5-dk kova) lokalde ölçülemedi (granüller dakika genişliğinde) ve prod'da ikincil: PK `(service_name, time)` olduğundan zaman aralığı granül budamaz, yalnız part budar; bloom zaten granül seçiyor. | EXPLAIN §1 (PrimaryKey `Keys: time`, generic exclusion) |

**Öneri (§6):** (1) projeksiyonu kolona bağla: `openshift.cluster.name` /
`k8s.cluster.name` → `cluster`; (2) `function_id` için terfi kolonu (String,
LC DEĞİL — kardinalite ölçülmedi; index YOK, projeksiyon için gerekmez);
(3) 7 günlük opt-in benchmark testi; (4) pencere daraltma = ölçüm sonrası
karar (prod query_log ile).

---

## 1. Sorgunun tam SQL'i + EXPLAIN

### 1a. Liste (aşama 1) — kolon durumundan BAĞIMSIZ

MV yolu (`getTracesFromMV`, `repo.go:2957`): stage-1 `trace_service_index_5m`
→ id listesi, stage-2 `trace_summary_5m` (`repo.go:3247-3262`, `LIMIT ?
OFFSET ?`, `max_execution_time = 12`). Ham yol (`buildGetTracesListSQL`,
`repo.go:2526`): `FROM spans … GROUP BY trace_id … LIMIT ? OFFSET ?
SETTINGS max_execution_time = 25`. **İkisinde de attribute projeksiyonu
YOK** — FAZ 2 testi pinli.

### 1b. Extras (aşama 2) — kolon açıkken gelen tek ek sorgu

```sql
SELECT trace_id,
       anyIf(attr_channel_code, attr_channel_code != '')      AS extra_0,   -- terfi
       anyIf(attr_function_code, attr_function_code != '')    AS extra_1,   -- terfi
       anyIf(coalesce(nullIf(attr_values[indexOf(attr_keys, 'function_id')], ''),
                      nullIf(res_values[indexOf(res_keys, 'function_id')], '')),
             has(attr_keys,'function_id') OR has(res_keys,'function_id')) AS extra_2, -- DİZİ
       anyIf(coalesce(nullIf(attr_values[indexOf(attr_keys, 'openshift.cluster.name')], ''),
                      nullIf(res_values[indexOf(res_keys, 'openshift.cluster.name')], '')),
             …)                                                AS extra_3   -- DİZİ (kolon varken!)
FROM spans
WHERE time >= ? AND time <= ?            -- 50 satırın [min başlangıç − 1 ms, max bitiş + 5 dk]
  AND trace_id IN (?, … ×50)
GROUP BY trace_id
LIMIT 50
SETTINGS max_execution_time = 15
```
Sunucu: `serveTracesExtras` (`traces_extras.go`) — id ≤ 200, attr ≤ 8,
pencere ≤ 35 gün (`clampExtrasFrom`), `serveCached` 20 s, anahtar = FNV(sıralı
id'ler + attr'lar) + from/to.

**EXPLAIN indexes=1** (`spans_local`, 6 saat, 50 dağınık kök trace, lokal):
```
MinMax      Keys: time          Parts: 7/75   Granules: 20/2352
Partition   Keys: toDate(time)  Parts: 7/7    Granules: 20/20
PrimaryKey  Keys: time          Parts: 7/7    Granules: 20/20   Search Algorithm: generic exclusion search
Skip  idx_trace bloom_filter GRANULARITY 4    Parts: 5/7    Granules: 18/20
```
Okuma: **PrimaryKey `Keys:` listesinde `service_name` YOK** (sorgu servis
bilmiyor) → birincil anahtar öneki kullanılmıyor, zaman ikinci anahtar
olarak yalnız "generic exclusion" ile bakılıyor; MinMax part'ları buduyor
(75→7), granülleri **bloom seçiyor** (20→18 — lokalde her granül dakikalar
genişliğinde, 50 id neredeyse her granülde var). Prod'da 6 saat ≈ 30 K
granül; bloom 50 id için ≈ 100-200 index bloğu (×4 granül ×8192 satır) seçer
→ **~1-6 M satır** okunur, bu satırların ŞİŞMAN dizileri dekomprese edilir.

### 1c. Ölçüm — query_log medyanı (3 koşu, aynı 50 id, aynı pencere)

| Sorgu | read_rows | read_bytes | p50 |
|---|---|---|---|
| Liste stage-1 (`trace_service_index_5m`, 51 satır) | 140 792 | 1.61 MiB | 38 ms |
| Extras — **terfi kolonları** (channel_code + function_code) | 119 518 | **5.25 MiB** | **66 ms** |
| Extras — **dizi yolu** (function_id + openshift.cluster.name) | 119 518 | **84.10 MiB** | **212 ms** |

`/perf-triage` yorumlama tablosu: *"read_rows aynı, read_bytes 16× → maliyet
satır değil kolon."* 2026-07 olayının (CHANNEL_CODE 6.97×) aynı sınıfı,
bu kez `function_id` ve `openshift.cluster.name` üzerinde.

---

## 2. Kolon eklenince plan nasıl değişiyor

- **Attribute FİLTRE değil, PROJEKSİYON.** Liste isteği aynen; FE yalnız
  eksik anahtarlar için extras isteği atar (`missingExtraKeys`, `Traces.tsx:513`).
  Kolon eklemek = 1 ek istek (yalnız yeni anahtar), sayfa/sıralama/aralık
  değişimi = liste + extras (beklenen).
- Extras'ın WHERE'i satırlardan türeyen pencere + `trace_id IN`. Zaman-sıralı
  ilk sayfada pencere dakikalar; **zaman-dışı sıralamada (operation, duration…)
  ve derin sayfalarda pencere tüm aralık.** Operatörün "tüm aralık taranıyor
  gibi" algısı bu: pencere gerçekten tüm aralık, ama tarama bloom'un seçtiği
  granüllerle sınırlı — pahalı olan o granüllerin dizi kolonları.
- Kolon başına maliyet eşit değil: terfi/well-known kolon (`attr_channel_code`,
  `attr_function_code`, `deploy_env`, `cluster`, `http_status`…) LC/küçük;
  dizi anahtarı (`function_id`, `openshift.cluster.name`, herhangi özel attr)
  `attr_keys/attr_values/res_keys/res_values` dört dizinin tamamını okur.

---

## 3. Seçenek A — terfi kolonu + skip index

| Anahtar | Bugün | Gerekli |
|---|---|---|
| `CHANNEL_CODE`/`channel_code` | `attr_channel_code` MATERIALIZED (iki yazım), `set(0) GRANULARITY 4` index, dolu | — |
| `FUNCTION_CODE`/`function_code` | `attr_function_code` aynı | — |
| `deployment.environment.name` | `deploy_env` (ingest'te dolu, `WellKnownTraceCol`) | — |
| `openshift.cluster.name` / `k8s.cluster.name` | `cluster` MATERIALIZED (altı yazımı coalesce eder), dolu | **projeksiyon eşlemesi yok** → 1 satır kod |
| `function_id` | yok — dizi yolu | **yeni terfi kolonu** |

**`function_id` kolonu:** `attr_function_id String MATERIALIZED
coalesce(nullIf(attr_values[indexOf(attr_keys,'function_id')],''), nullIf(…'FUNCTION_ID'…), '')`.
- Tip: **`String`, LC DEĞİL** — kardinalite ölçülmedi (id'ler on binlerce
  olabilir; C3 "ölçmediysen LC yapma"; `function_code` emsali LC ama o kod).
- Index: **projeksiyon için gerekmez** (filtre trace_id ile). Operatör
  `function_id` FİLTRESİ de istiyorsa `bloom_filter(0.01)` — ayrı karar.
- **Migration maliyeti (dağıtık prod):** `ALTER TABLE spans_local ON
  CLUSTER … ADD COLUMN IF NOT EXISTS` + Distributed sarmalayıcıya aynı ADD;
  0012 sözleşmesi: boot koşmaz, `migrations/0013_function_id.sql` sihirbaz/elle,
  `store.go` `alters` dilimi app-managed kurulumlar için koşullu.
- **Backfill gerçeği (v0.9.621 dersi):** MATERIALIZED kolon eski part'larda
  saklanmaz, okuma anında DİZİDEN hesaplanır → eski veri için bayt kazancı
  SIFIR, yalnız yeni part'lar ucuz. Tam kazanç için `ALTER … MATERIALIZE
  COLUMN attr_function_id` (part yeniden yazımı; prod'da retention × 1 B
  satır — saatler, IO). Alternatif: backfill'siz yaşa, kazanç retention
  boyunca kendiliğinden dolar (7 günde tam).
- Distributed güvenlik: probe + koşullu projeksiyon (`promotedCols()` zaten
  probe-tabanlı, `store.go:3249`) — kolon yoksa dizi yoluna düşer, KIRILMAZ.

---

## 4. Seçenek B — iki aşamalı sorgu

**Bugün ZATEN iki aşamalı** (FAZ 2): liste `LIMIT 50` PK/MV yoluyla id'leri
verir, extras yalnız o id'ler için projeksiyon yapar. Operatörün tarif
ettiği B, mevcut tasarımdır; kalan soru pencere geometrisi:

| Varyant | Lokal ölçüm (aynı 50 id) | Prod'da beklenen |
|---|---|---|
| Global pencere (bugün) | 120 003 satır · 84 MiB · 145 ms | bloom seçimi ~100-200 blok |
| 5-dk kova gruplu OR yüklemleri | 120 003 · 84 MiB · 172 ms | part budaması iyileşir; granül seçimi aynı (bloom) |
| Trace başına ±1 s pencere (50 OR) | 119 943 · 84 MiB · 258 ms | aynı; yüklem maliyeti artar |

Lokalde granüller dakika genişliğinde olduğu için fark görünmez; prod'da
da kazanç sınırlı çünkü **PK öneki `service_name`** — zaman aralığı granül
seviyesinde budamaz (generic exclusion), bloom zaten granülü seçmiş. Pencere
daraltma yalnız **part** sayısını düşürür (saatlik/günlük part'larda 6
saatlik pencere zaten az part). **Karar: ölçmeden dilim AÇMA** — prod
query_log'da `SelectedParts`/`SelectedMarks` (§7) pencere varyantını haklı
çıkarırsa B2 (5-dk kova gruplu, FE satır zamanlarını gönderir) ayrı dilim.

---

## 5. Frontend tetikleme

- Kolon ekle/çıkar: liste refetch **YOK** (`Traces.tsx:482-502` dep dizisi);
  yalnız `extraCols`+`data` effect'i (`:508-540`) eksik anahtarlar için
  extras ister — gereksiz refetch yok. ✅
- Pencere: FE satırların min/max'ından türetiyor (`:515-520`) → zaman-dışı
  sıralamada tüm aralık (§2). Sunucu `to+5 dk` slack (`traceExtrasWindow`).
- Extras yanıtı `mergeTraceExtras` ile satıra basılır; boş değer `""` →
  tekrar istenmez. ✅
- Cache: sunucu 20 s `serveCached`, anahtar id+attr+pencere → sayfa
  değişiminde MISS (doğru — farklı id'ler).
- `DEFAULT_TRACE_COLUMNS = [openshift.cluster.name, channel_code,
  function_code, function_id]` (2026-08-24 kararı) → **taze oturumda dördü
  de açık, ikisi şişman yolda** — yavaşlık "kolon ekleyince" değil, "varsayılan
  açılışta" da var; operatör kolon ekleyince FARK ediyor.

---

## 6. Öneri ve dosya planı

**Tek en etkili değişiklik:** projeksiyonu şişman yoldan çıkarmak (16× bayt).
Pencere işi ikincil ve ölçüm ister.

| Dilim | Ne | Dosyalar | Test |
|---|---|---|---|
| **D1 (bugfix)** `openshift.cluster.name`/`k8s.cluster.name` → `cluster` kolonu | `traceExtrasProjection`'a `clusterAliases` eşlemesi (mevcut `key == "cluster"` dalının yanına) | `internal/chstore/repo.go` | `traces_extras_test.go`: üç yazım da `anyIf(cluster, …)` üretir, dizi ifadesi YOK |
| **D2** `function_id` terfi kolonu | `promoted_attr.go` tablosuna `{col: attr_function_id, keys: [function_id, FUNCTION_ID]}` (String); `store.go` alters (app-managed, koşullu); `migrations/0013_function_id.sql` (+rollback; ON CLUSTER + Distributed ADD; MATERIALIZE COLUMN opsiyonel adım, ayrı komut) | `internal/chstore/promoted_attr.go`, `store.go`, `migrations/0013_*.sql`, `migrations/embed.go` (sihirbaz kapsamı) | `promoted_attr_test.go` (iki yazım), `ddl_slice_placement_test`, dağıtık checklist (§9 /clickhouse-schema) |
| **D3** varsayılan kolonlar | `DEFAULT_TRACE_COLUMNS` zaten `channel_code, function_code` içeriyor; gizleme ColumnManager + `traces-extra-cols` localStorage ✅ — **değişiklik gerekmiyor**; yalnız sıra: `channel_code, function_code` başa (attr sırası) | `lib/traceColumns.ts` | `traceColumns.test.ts` |
| **D4** benchmark regresyon testi | Opt-in canlı test (`COREMETRY_BENCH_CH_DSN` yoksa `t.Skip`): 7 günlük aralıkta liste (kolonsuz) vs liste+extras (varsayılan 4 kolon) query_log medyanı (≥3 koşu) + read_bytes; eşik: extras read_bytes ≤ 3× liste, süre ≤ 500 ms; sonuç `t.Logf` + `testdata/bench-traces-extras.md`'ye satır | `internal/chstore/traces_extras_bench_test.go` | kendisi |
| **D5 (ölçüm sonrası)** pencere daraltma B2 | FE satır zamanları → sunucu 5-dk kova OR yüklemleri; anahtar kovaları içerir | `traces_extras.go`, `repo.go`, `Traces.tsx`, `api.ts` | `traceExtrasSQL` kova testi; prod ölçümü kabul eşiği |

Sıra: D1 (10 dk, hemen bugfix sınıfı) → D4 (ölçüm altyapısı, prod'da
koşulabilir) → D2 (migration operatörde) → D5 ölçüme bağlı.

---

## 7. Prod doğrulama sözleşmesi

Operatör prod CH'de (query_log açıksa) koşar — extras sorgusunu bulmak için
`log_comment` yok (bilinen boşluk), metin imzası: `anyIf(` + `trace_id IN`:

```sql
SELECT round(quantile(0.5)(query_duration_ms)) p50_ms, max(query_duration_ms) max_ms,
       max(read_rows) read_rows, formatReadableSize(max(read_bytes)) read_bytes,
       max(ProfileEvents['SelectedMarks']) marks, max(ProfileEvents['SelectedParts']) parts, count() runs
FROM clusterAllReplicas('<küme>', system.query_log)
WHERE event_time > now() - INTERVAL 1 DAY AND type = 'QueryFinish' AND query_kind = 'Select'
  AND query LIKE '%trace_id IN (%' AND query LIKE '%anyIf(%' AND query LIKE '%GROUP BY trace_id%'
FORMAT Vertical;
```
Kabul eşiği (D1+D2 sonrası, aynı sorguda): `read_bytes` en az **5×** düşer;
p50 < 500 ms. Düşmezse §4 pencere varyantı (D5) ölçülür — `SelectedMarks`
yüksek + `read_bytes` düşükse pencere, tersi kolon.

---

## 8. Riskler

| # | Risk | Önlem |
|---|---|---|
| R1 | `function_id` kardinalitesi → LC sözlük şişmesi | String tipi; LC'ye geçiş `uniq(function_id) < 10k` ölçülünce |
| R2 | MATERIALIZED kolon eski part'larda kazanç sağlamaz | Dokümante; MATERIALIZE COLUMN ayrı, operatör kararı |
| R3 | Dağıtık prod'da kolon yalnız `_local`'a eklenir, Distributed'a değil | 0012 kalıbı: iki ADD; probe kolonu Distributed'da görmezse dizi yoluna düşer (kırılmaz, hızlanmaz — v0.9.621 "kolon var ≠ dolu/kullanılıyor") |
| R4 | `openshift.cluster.name` → `cluster` eşlemesi yanlış değer (cluster coalesce sırası: `k8s.cluster.name` önce) | İki anahtar aynı kolona coalesce edilmiş; farklıysa `k8s.cluster.name` kazanır — dokümante |
| R5 | Benchmark testi CI'da CH ister | opt-in env; CI'da skip |
