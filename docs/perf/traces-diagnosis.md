# Traces yavaşlığı — FAZ 2: teşhis

Tarih: 2026-08-03 · Sürüm: v0.9.600 · Kanıt: [traces-evidence.md](traces-evidence.md)

## Tek cümlelik teşhis

**MV hızlı yolu iyi çalışıyor; sorun onun ne kadar KOLAY diskalifiye
olduğu.** Tek bir filtre, bir arama kutusu girdisi ya da "toplamı göster"
işareti sayfayı 15× daha pahalı bir ham `GROUP BY trace_id` yoluna
düşürüyor — ve operatörün gerçek iş akışı (filtreleyerek daraltmak) tam
olarak o yolu tetikliyor.

## `read_rows` / gösterilen satır oranı

| Yol | Pencere | `read_rows` | Gösterilen | **Oran** |
|---|---|---|---|---|
| MV aşama-1 (recency slice) | 6h | 58.096 | 5.000 | **11.6:1** ✅ |
| MV tam liste | 6h | 157.721 | 50 | 3.154:1 |
| **Ham yol** | 6h | 1.128.772 | 50 | **22.575:1** ❌ |
| **Ham yol** | 24h | 3.803.947 | 50 | **76.078:1** ❌ |

Eşik 1000. MV aşama-1 fazlasıyla altında; ham yol 76 katı üstünde.
**Asıl darboğaz orada** ve teşhisin geri kalanı bunun neresinden
kaynaklandığını ayırıyor.

---

## Hipotez kararları

### H1 — Trace listesi span tablosundan runtime `GROUP BY trace_id` ile türetiliyor
**KISMEN DOĞRU — yola bağlı.**

İki yol var (`repo.go:2046`):
- **MV yolu** (`getTracesFromMV`, `repo.go:2704`): `trace_summary_5m`
  üzerinden, iki fazlı. `GROUP BY trace_id` ham span'lerde **yapılmıyor**.
- **Ham yol** (`buildGetTracesListSQL`, `repo.go:2335`): `spans` üzerinde
  `GROUP BY trace_id`. Hipotez **burada birebir doğru**.

Kanıt: 24h penceresinde ham yol 3.803.947 satır okuyor — `spans_local`
toplam 3.717.545 satır. **Tablonun tamamından fazlası** (2 shard'ın
toplamı), 50 satır döndürmek için.

MV'yi diskalifiye eden koşullar (`repo.go:2092`):
`len(f.Filters) > 0` · `search` · `traceId` · `env` · `filterGroup` ·
`services` · `count != "skip"` · pencere < 5dk

**Kazanç:** ham yolu MV yolunun şekline getirmek → 3.5× satır / 9.7× bayt.
**Efor:** 3/5

---

### H2 — Attribute'lar Map üzerinden okunuyor, indeks kullanılamıyor
**İFADE OLARAK YANLIŞ, ETKİ OLARAK DOĞRU.**

`Map` değil, **paralel diziler**: `attr_keys`/`attr_values` +
`res_keys`/`res_values`. Ama indekslenemezlik iddiası ayakta: fallback
projeksiyonu (`repo.go:2423`) dört şişman dizi kolonunu birden açıyor.

Ölçüm (aynı 50 trace, 1h, tek attribute):

| Yol | `read_rows` | `read_bytes` |
|---|---|---|
| `anyIf(attr_channel_code, …)` | 102.367 | **4.81 MiB** |
| 4 dizi kolonlu fallback | 102.488 | **20.89 MiB** |

Aynı satır, **4.3× bayt** — tamamı dekompresyon.

**ÖNEMLİ DÜZELTME:** `attr_channel_code` ve `attr_function_code`
`spans`'te **MATERIALIZED kolon olarak zaten var** (canlı doğrulandı) ve
boot probe'u (`store.go:2508`) haritayı dolduruyor. Probe `acErr == nil`
ile karar veriyor, satır sayısıyla değil — yani boş kurulumda da doğru.
**Lokalde probe geçiyor.**

Yani CHANNEL_CODE için 4.3× ceza **yalnızca ALTER her shard'a inmediyse**
ödeniyor. Diğer tüm özel anahtarlar (kurumsal alanlar) için her zaman
ödeniyor.

> ## ⚠ AŞAĞIDAKİ SONUÇ YANLIŞTI — düzeltildi 2026-08-04 (v0.9.621-625)
>
> **Hata:** İstediğim probe kolonun **VAR olduğunu** kanıtlıyordu,
> **DOLU olduğunu** değil. İkisini aynı şey sandım ve ekran
> görüntüsünde değerin boş göründüğünü fark edip üstünde durmadım.
>
> **Gerçek:** `attr_channel_code` prod'da v0.9.198'den beri **HEP
> BOŞTU**. Kolonun ifadesi `indexOf(attr_keys, 'CHANNEL_CODE')` —
> BÜYÜK harf; prod ise KÜÇÜK harf yazıyor. Operatör ölçümü
> (2026-08-04): 10 dakikalık pencerede `channel_code` taşıyan
> **2.67M span**, `CHANNEL_CODE` taşıyan **sıfır**.
>
> Yani "prod zaten en ucuz katmanda" cümlesinin tam tersi doğruydu:
> prod **hiçbir zaman** o katmanda olmadı ve her `channel_code`
> sorgusu dört değil **iki** şişman `Array(String)` kolonunu açıyordu.
>
> **Ders:** doğrulanan postkoşul, ihtiyaç duyulan postkoşul olmalı.
> "Sorgu hata vermedi" ile "kolon veriyi taşıyor" farklı iddialar.
>
> Kapatan sürümler: v0.9.621 (kolon onarımı + DOLULUK probe'u),
> v0.9.622 (filtre yönlendirmesi), v0.9.623 (skip index),
> v0.9.624 (iş-boyutu kırılımı), v0.9.625 (ertelenen DDL sonrası
> yeniden probe), v0.9.626 (rollup migration'ları).

**PROD PROBE'U (2026-08-03, operatör ekran görüntüsü) — yanlış okundu:**
```
select attr_channel_code FROM spans WHERE time >= now() - INTERVAL 1 SECOND LIMIT 1
→ 1 rows · 13 ms · 1 columns   (HATA YOK — ama DEĞER BOŞ)
```

**Doğru sonuç:** dizi yolu cezası prod'da CHANNEL_CODE ve
FUNCTION_CODE için de ödeniyordu. Ölçülen fark (CH 24.8, 10M satır
prod şeklinde tablo, 3 koşu medyanı):

| yol | read_rows | read_bytes | ms |
|---|---|---|---|
| dizi açma | 10.000.000 | 3.90 GiB | 362 |
| terfi kolonu | 10.000.000 | 1.98 GiB | 204 |
| kolon + `set(0)` indeks | 1.310.720 | 261 MiB | 81 |

Kolonun tek başına yalnız 2× olmasının sebebi ölçümle bulundu: YENİ
eklenen bir MATERIALIZED kolon eski part'larda **saklanmaz**, okuma
anında diziden hesaplanır. Asıl kazanç skip index'te.

**Öncelik: YÜKSEK** (öncesinde "DÜŞÜK — bugün ödenen bir maliyet
değil" yazıyordu; ödenen bir maliyetti).

---

### H3 — İki fazlı sorgu deseni yok
**ÇÜRÜTÜLDÜ (MV yolunda) / DOĞRU (ham yolda).**

MV yolunda desen zaten var:
- Aşama-1: `trace_slice.go:94` — `GROUP BY` yok, `ORDER BY time_bucket DESC LIMIT`
- Aşama-2: `repo.go:2987` — `trace_id IN (…)` ile zenginleştirme

**Faz-2 pencere daraltması da ZATEN VAR** — ilk taslakta bunu eksik
sanmıştım, kod okunca düzeltildi:

- `runTraceStage2` (`trace_slice.go:247-253`) tabanı
  `floor - lookback`'e çekiyor ve dönen satır tabana oturursa
  **genişletip yeniden deniyor** (kısmi agregasyon tuzağına karşı).
- `fillTraceExtras` (`repo.go:2582`) pencereyi `traceExtrasBounds(rows)`
  ile **dönen satırlardan** türetiyor.

Ölçtüğüm 3.5×/9.7× kazanç, kendi yazdığım naif şekiller arasındaydı —
üretim kodunun MV yolu o kazancı zaten alıyor:

| Şekil | `read_rows` | `read_bytes` |
|---|---|---|
| Tek sorgu, dar pencere yok (naif) | 1.131.157 | 202.57 MiB |
| İki faz, geniş faz-2 (naif) | 779.748 | 119.87 MiB |
| İki faz, dar faz-2 ≈ **üretimdeki MV yolu** | 319.531 | 20.98 MiB |

**Geriye kalan gerçek boşluk: HAM YOL.** Orada ne iki faz var ne
daraltma — tek `GROUP BY trace_id`, `LIMIT/OFFSET`. Ve operatörün
gerçek iş akışı (filtreleyerek daraltmak) tam olarak o yolu tetikliyor.

**Kazanç:** ham yolu MV yolunun şekline getirmek → ölçülen 3.5× satır,
9.7× bayt. **Efor:** 3/5

---

### H4 — Distributed fan-out'ta LIMIT shard'a itilmiyor
**ÇÜRÜTÜLDÜ.**

| Sorgu | `read_rows` | Sonuç | Oran |
|---|---|---|---|
| `trace_summary_5m` (Distributed) | 58.096 | 5.000 | 11.6:1 |
| `trace_summary_5m_local` (tek shard) | 36.426 | 5.000 | 7.3:1 |

LIMIT itiliyor. İtilmeseydi dağıtık sorgu her iki shard'da tüm pencereyi
okurdu (6h MV taraması ≈ 157k+). 58k, shard başına ~29k demek —
`optimize_read_in_order` erken sonlandırıyor.

`optimize_skip_unused_shards = 1` de küme modunda açık (`cluster.go:124`).

**Kazanç:** yok. **Efor:** —

---

### H5 — Sayfalama için `count()` çalışıyor
**VARSAYILANDA ÇÜRÜTÜLDÜ / OPERATÖR AÇINCA DOĞRU VE ÇİFT CEZALI.**

UI varsayılanı `count=skip` (`Traces.tsx:375`,
`showTotal && !traceIdExact ? 'exact' : 'skip'`, `showTotal` başlangıçta
`false`). Varsayılan yolda `count()` **koşmuyor**.

Ama operatör "toplamı göster" derse iki ceza birden:
1. `CountMode=exact` → `countModeAllowsMV` false → **MV diskalifiye**
2. Üstüne ayrı bir `count()` / DISTINCT sorgusu

`approx` kipi var ve sınırlı (`f.Limit*10`, `repo.go:2201`) ama UI onu
hiç kullanmıyor — yalnız `skip` ve `exact` gönderiyor.

**DÜZELTME (uygulama sırasında bulundu):** ilk taslakta "UI'ı `approx`a
bağla, MV diskalifiyesi kalkar" demiştim. **Yanlış.**
`countModeAllowsMV` (`repo.go:2004`) yalnız `"skip"` ve `""` kabul
ediyor — `approx` de ham spans üzerinde `GROUP BY trace_id` sardığı için
MV'yi kapatıyor. Yani `approx`a geçmek sayımı ucuzlatır ama LİSTEYİ ham
yolda bırakır; asıl maliyet orada.

Doğru düzeltme: sayımın da **MV'den** cevaplanması —
`SELECT count() FROM (SELECT trace_id FROM trace_summary_5m WHERE …
GROUP BY trace_id LIMIT 10000)`. Tavana değerse UI "10.000+" gösterir.
Bu, operatörün UX brief'inde zaten karara bağlanmış olan şekil.

**Kazanç:** "toplamı göster" senaryosu MV'de kalır. **Efor:** 3/5
(1/5 değil — yeni bir MV sayım yolu gerekiyor).

---

### H6 — ORDER BY key ile sorgunun sıralaması hizalı değil
**`spans` İÇİN DOĞRU / `trace_summary_5m` İÇİN ÇÜRÜTÜLDÜ.**

`spans` ORDER BY `(service_name, time)`. Trace listesi **servis-bağımsız
ve zaman-öncelikli** — yani birincil anahtarın önekini kullanamıyor,
her servisin zaman aralığını taramak zorunda.

`trace_summary_5m` ORDER BY `(time_bucket, trace_id)` — aşama-1
tam hizalı ve `optimize_read_in_order` çalışıyor (H4'teki 11.6:1 bunun
kanıtı).

**Ama hizalama tek başına yetmiyor:** `GROUP BY time_bucket, trace_id
ORDER BY time_bucket DESC` denemesi 665.094 satır okudu — hizasız
sürümle (665.040) aynı. AggregatingMergeTree state'lerini birleştirmek
penceredeki tüm kovaların okunmasını gerektiriyor, erken sonlanma yok.

`EXPLAIN` bunu doğruluyor: birincil indeks **düzgün buduyor**
(480 → 57 granül, %12). Sorun indeks değil, `GROUP BY`'ın kendisi.

**Sonuç:** indeks ayarıyla çözülmez. Karmaşıklık sınıfını yalnız trace
başına ön-toplama (kova değil) ya da daraltıcı ilk faz değiştirir.
**Efor:** 4/5 (yeni tablo)

---

### H7 — Darboğaz backend değil frontend
**KISMEN — frontend backend'i AĞIRLAŞTIRIYOR.**

Frontend'in kendisi iyi: gerçek sanallaştırma (`@tanstack/react-virtual`),
`keepPreviousData`, aşamalı yükleme, `timeRangeToNs` kurala uygun.
Kolon eklemek ana liste sorgusunu yeniden çalıştırmıyor.

Üç gerçek kusur var ve üçü de **backend maliyetini artırıyor**:

1. **İptal edilebilirlik YOK.** `let cancelled = false` bayrakları
   *yanıtı* atıyor, *isteği* değil. `api.ts:64` `AbortController` yalnız
   60s timeout için; `get<T>()` çağıranın signal'ini almıyor. Hızlı
   aralık değiştiren operatör ClickHouse'ta sorgu **yığıyor** — her biri
   `max_execution_time`'a kadar koşuyor.
2. **`metric-batch` kullanılmıyor.** `POST /api/spans/metric-batch`
   (`api.go:735`) tam olarak bu üçlüyü tek CH taramasına indirmek için
   var — yorumu bile *"dropping cold-cache time from ~3× to ~1×"* diyor.
   Servis detay sayfası kullanıyor, **/traces 3 ayrı GET atıyor.**
3. **Extras seri.** Liste dönmeden başlayamıyor → dolu tablo süresi =
   liste + extras.

Ayrıca `/api/attribute-keys` **debounce'suz** (`FilterBuilder.tsx:67`),
her chip değişiminde ateşleniyor.

**Kazanç:** iptal → CH'de üst üste binen sorgu yükü kalkar;
metric-batch → 3 tarama 1'e iner. **Efor:** 2/5

---

## Tabloya dökülmüş karar

| # | Hipotez | Karar | Kazanç | Efor |
|---|---|---|---|---|
| H1 | Runtime `GROUP BY trace_id` | **Ham yolda DOĞRU** | 3.5× / 9.7× | 3 |
| H2 | Attribute indekslenemiyor | İfade yanlış, **etki doğru** | 4.3× bayt | 2 |
| H3 | İki faz yok | **MV'de tamamen çürük** (daraltma dahil), ham yolda doğru | 3.5× / 9.7× | 3 |
| H4 | LIMIT itilmiyor | **ÇÜRÜK** | — | — |
| H5 | `count()` koşuyor | **Varsayılanda çürük**, "toplam" açıkken doğru | çift ceza kalkar | 1 |
| H6 | ORDER BY hizasız | `spans`'te **doğru**, MV'de çürük | sınıf değişimi | 4 |
| H7 | Darboğaz frontend | **Kısmen** — backend'i ağırlaştırıyor | 3→1 tarama | 2 |

---

## Önerilen dilim sırası (FAZ 3 taslağı — onay bekliyor)

Kural: şema değişikliği gerektirmeyen ve geri alınabilir olanlar önce.

| Dilim | İş | Kazanç | Efor | Şema? |
|---|---|---|---|---|
| **D1** | `/traces`'i `metric-batch`'e geçir | 3 tarama → 1 | 1 | hayır |
| **D2** | İstek iptali (`AbortController` → `get<T>`) | yığılan CH yükü kalkar | 2 | hayır |
| **D3** | "Toplamı göster" için MV tabanlı tavanlı sayım (`approx` YETMEZ — o da MV'yi kapatıyor) | MV'de kalır | 3 | hayır |
| **D4** | `/api/attribute-keys` debounce | istek sayısı | 1 | hayır |
| ~~D5~~ | ~~Ham yolu iki fazlıya çevir~~ — **ÖLÇÜMLE ÇÜRÜTÜLDÜ**, aşağıya bak | — | — | hayır |
| **D5b** | Promoted attribute kolonlarına skip index (filtreyi granül-seçici yap) | ölçülmedi | 2 | **evet** |
| ~~D7~~ | ~~Sıcak attribute promote~~ — **prod'da doğrulandı: CHANNEL_CODE/FUNCTION_CODE ZATEN promoted.** Yalnız yeni anahtarlar için reçete | — | 2 | evet |
| **D8** | Trace başına tek satırlık özet tablo (kova değil) | sınıf değişimi | 4 | **evet** |

**D1-D6 şema değişikliği gerektirmiyor ve her biri tek başına geri
alınabilir.** D7-D8 migrasyon + geri alma scripti ister.

Keyset sayfalamayı (özgün D2 önerisi) listeye **almadım**: `count=skip`
zaten varsayılan ve derin OFFSET yalnızca operatör 10+ sayfa ilerlerse
ısırıyor. Ölçülmüş bir sorun değil — FAZ 3'te ölçüp karar vermek daha
dürüst.

---

## Ölçüm sözleşmesi (her dilim sonrası)

Aynı pencerede, aynı sorguyla, `system.query_log` **medyanı** üzerinden:

| Metrik | Bugün (6h, ham yol) | Hedef |
|---|---|---|
| `read_rows` | 1.128.772 | < 350.000 (D1 sonrası) |
| `read_bytes` | 76.90 MiB | < 25 MiB |
| oran | 22.575:1 | < 7.000:1 |
| p95 | prod'da ölçülecek | < 1.5s |

Tek ad-hoc zamanlama kabul edilmiyor; `ms` önbellek sıcaklığıyla 2-3×
oynuyor (bu turda bir vakada dizi yolu materialized yoldan *hızlı*
göründü). Karar `read_rows`/`read_bytes` üzerinden verilir.


---

## EK: D5 ölçümle çürütüldü (2026-08-03)

Planın en yüksek etkili kalemi olarak yazdığım D5 — "ham yolu iki
fazlıya çevir" — uygulamaya başlarken ölçüldü ve **yapılamaz** çıktı.
Sebebi tek: `spans` ORDER BY `(service_name, time)`.

Senaryo: attribute filtresi (MV'yi kapatan hâl), 6h pencere, 50 satır.

| Şekil | `read_rows` | `read_bytes` | ms |
|---|---|---|---|
| (a) bugünkü tek faz | 1.487.763 | 117.34 MiB | 336 |
| (b) iki faz, aşama-1 **SIRASIZ** | **302.760** | **51.03 MiB** | 118 |
| (c) iki faz, aşama-1 **SIRALI** | **1.645.390** | 105.64 MiB | 137 |

(b) cazip görünüyor — 4.9× satır — ama **yanlış**: `SELECT DISTINCT
trace_id … LIMIT 5000` sıralamasız olduğu için dönen 5000 aday KEYFİ.
Eşleşen trace sayısı 5000'i aştığı anda "en yeni 50" o kümede
olmayabilir ve operatör sessizce YANLIŞ bir liste görür. Hızlı ve
sessizce yanlış, yavaş ve doğrudan kötüdür.

(c) doğruluğu düzeltiyor ve maliyeti **bugünkünden yükseğe** çıkarıyor:
`time`'a göre sıralamak birincil anahtarın öneki OLMADIĞI için tüm
pencere okunup sıralanıyor, erken sonlanma yok. H6'nın bu tasarım için
nicelleştirilmiş hâli.

**Sonuç:** ham yolun maliyeti YAPISAL. İki faz bir sorgu-yazım hilesiyle
kazanılamaz; kazanç ancak filtrenin kendisi granül-seçici olursa ya da
sıralama anahtarı erişim desenine uyarsa gelir. İki aday kalıyor:

- **D5b (yeni, ucuz):** promoted attribute kolonlarına (`attr_channel_code`,
  `attr_function_code`) skip index. Filtre granül düzeyinde budarsa tek
  fazlı sorgu da ucuzlar. Prod'da CHANNEL_CODE gerçek değer taşıdığı için
  ölçüm ORADA yapılmalı — lokalde kolon boş, ölçüm anlamsız.
- **D8 (pahalı, kesin):** trace başına tek satırlık, ZAMANA göre sıralı
  özet tablo — sıcak attribute'ları taşırsa filtre+sıralama+LIMIT üçü de
  birincil anahtara biner.

**Ders:** teşhis kod okumaya dayanıyordu, ölçüm gerçeğe. Bu turda
önerdiğim üç dilimden üçü de uygulanırken değişti (D1'in eforu, D3'ün
tamamı, D5'in yapılabilirliği). Plan, ölçülmeden dilim gönderilmemesi
gerektiğini yeniden kanıtladı.
