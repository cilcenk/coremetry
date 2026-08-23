---
name: perf-triage
description: Diagnose ONE concrete Coremetry slowness complaint ("X sayfası yavaş", "bu uç 3 saniye") with measurements instead of guesses — split backend vs frontend by TTFB + X-Cache, then EXPLAIN indexes=1 + system.query_log read_rows/read_bytes on the ClickHouse side, then the known frontend slowness classes, ending in a report with a before/after measurement contract. Use when the operator reports one slow surface and the cause is unknown. Do NOT use for a whole-repo sweep (that is /scale-audit), for a slowness whose root cause is already named (go straight to /bugfix), or to propose an optimisation you have not measured.
---

# /perf-triage — tek şikâyet, ölçülmüş teşhis

**Tetikleyici:** operatörün TEK somut şikâyeti.
**Yasak:** ölçmeden dilim önermek · tek ad-hoc zamanlamayla karar vermek ·
repo geneline yayılmak (o `/scale-audit`).
**Bitiş:** ölçülmüş kök neden + öncesi/sonrası ölçüm sözleşmesi →
`/bugfix` ya da `/spec`. **Ya da "çürütüldü, dilim düşer"** — bu da
geçerli bir sonuçtur.

## Akış

```
ŞİKÂYET
  │
  ├─ ADIM 1  TTFB + X-Cache ölç
  │
  │   ttfb bütçe İÇİNDE + HIT-*  ────────────────► ADIM 4 (frontend)
  │   ttfb bütçe DIŞI + MISS/BYPASS ─────────────► ADIM 2 (ClickHouse)
  │   ttfb bütçe DIŞI ama HIT-*  ────────────────► serialization/gövde boyutu
  │
  ├─ ADIM 2  sorguyu bul → EXPLAIN indexes=1 → query_log
  │            read_rows/dönen satır oranı ≫ 10.000:1 ──► ADIM 3
  │
  ├─ ADIM 3  sayfalama/tarama: LIMIT var ama aralık taranıyor mu
  ├─ ADIM 4  frontend sınıfları S1..S7
  └─ ADIM 5  rapor + doğrulama ölçümü
```

## ADIM 1 — Backend mi frontend mi

Kimlik cookie ile; **jar kurulmadan ölçüm geçersizdir** (401 döner ve
`X-Cache` hiç basılmaz):

```bash
JAR=/tmp/cm.jar
curl -sS -c $JAR -X POST http://localhost:8090/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@coremetry.local","password":"admin"}' \
  -o /dev/null -w 'login:%{http_code}\n'

curl -sS -b $JAR -o /dev/null -D /tmp/h.txt \
  -w 'ttfb=%{time_starttransfer}s total=%{time_total}s size=%{size_download}B\n' \
  'http://localhost:8090/api/services'
grep -i '^x-cache' /tmp/h.txt
```

Soğuk yolu isteyerek ölçmek için `?refresh=1` (okuma atlanır → `BYPASS`).

**`X-Cache` katmanları** (`internal/api/cache.go:298-311`) — ölçümün yarısı
burada:

| Değer | Anlam |
|---|---|
| `HIT-L1` | süreç-içi L1, ≤5s (`l1TTL`, cache.go:183) |
| `HIT` | Redis, taze |
| `STALE` | bayat gövde + arkada tazeleme (`staleFactor` 3, cache.go:190) |
| `HIT-LEGACY` | zarfsız eski kayıt |
| `MISS` | CH'ye gerçekten gidildi |
| `BYPASS` | `?refresh=1`, cache'e bakılmadı |

`MISS` ≠ `BYPASS`: biri "yoktu", diğeri "bakmadım bile". İkisini
karıştırmak soğuk-yol ölçümünü bozar.

⚠️ `/api/health` bu araca uygun değil — `serveCached`'den geçmiyor,
`X-Cache` basmıyor.

**Bütçeler** (CLAUDE.md): hot uçlar (`/api/services`, `/api/problems`,
`/api/health`) p99 < 50ms sıcak · diğer `/api/*` < 200ms sıcak, < 1s
soğuk · `/api/spans/heatmap` < 3s (≤6h) · `/api/logs/patterns` < 2s ·
TTFI < 1.5s.

**Karar:**

| Gözlem | Karar |
|---|---|
| ttfb bütçede + `HIT-*`, sayfa yine yavaş | **frontend** → ADIM 4 |
| ttfb bütçe dışı + `MISS`/`BYPASS` | **backend soğuk yol** → ADIM 2 |
| ttfb bütçe dışı ama `HIT-*` | cache sonrası: serialization → `size_download` |
| `tcp` yüksek, `ttfb` düşük | bağlantı tavanı → SSE/HTTP sayımı |
| 401 | jar kurulmamış, ölçüm geçersiz — baştan |

Yük altında p99 için:
`scripts/loadtest/read-load.sh -u http://localhost:8090 -c 20 -d 120`
(sürücü sırası hey → vegeta → bash curl döngüsü; bütçe kıyaslamasını
kendisi basar, `--fresh-window` soğuk okuma).

## ADIM 2 — ClickHouse teşhisi

Ortam çıpası: ns `coremetry`, küme adı `coremetry`, DB `coremetry`,
`spans` = Distributed / `spans_local` = ReplicatedMergeTree.
**`-d coremetry` şart**, yoksa `Code: 60 Unknown table`.

### Sorguyu bulma — sert gerçek

**Uygulama sorguları işaretlenmiyor**: `query_id` / `log_comment`
kullanımı repoda sıfır, SQL log'a da basılmıyor. `query_log` satırını API
ucuna bağlayan tek bağ sorgu metnidir. Üstelik bind farkı arama
stratejisini belirler:

| Kaynak | Bind |
|---|---|
| span `db.statement` | **öncesi** — `WHERE time >= ?` |
| `system.query_log.query` | **sonrası** — `WHERE time >= toDateTime('…')` |

→ WHERE literal'ine göre değil, **kolon listesi / tablo adı / agregat
ifadesine** göre ara.

### EXPLAIN indexes=1

**Dağıtık `spans` üzerinde işe yaramaz** — tek satır `ReadFromRemote`
döner, sıfır bilgi. **`spans_local` yaz:**

```bash
kubectl exec -n coremetry chc-0 -- clickhouse-client -d coremetry -q "
EXPLAIN indexes=1 SELECT count() FROM spans_local
WHERE time >= now()-3600 AND time < now() AND service_name='checkout-api'"
```

```
ReadFromMergeTree (coremetry.spans_local)
Indexes:
  MinMax      Keys: time          Parts: 6/55   Granules: 13/1635
  Partition   Keys: toDate(time)  Parts: 6/6    Granules: 13/13
  PrimaryKey  Keys: service_name, time
                                  Parts: 6/6    Granules: 6/13
```

Sayılar **kaskad**: her aşama öncekini daraltır, `seçilen/aday`. Asıl
kazanç genelde MinMax'te.

> **ORDER BY öneki imzası:** `spans_local` `ORDER BY (service_name, time)`.
> `PrimaryKey` altındaki `Keys:` listesinde `service_name` YOKSA **ve**
> `Granules: N/N` (1:1) ise → sorgu birincil anahtarı kullanmıyor.

Skip index'ler de aynı çıktıda görünür
(`Skip / Name: idx_trace / Parts: 4/52 / Granules: 16/1632`).
`spans_local`'da 8 tane: `idx_trace, idx_name, idx_kind, idx_db_system,
idx_http_status, idx_status, idx_attr_channel_code, idx_attr_function_code`.

### query_log — dağıtık okuma ve `is_initial_query` tuzağı

`query_log` **node-düzeyidir**; tek node'a bakmak 4-node prod'da tablonun
çeyreğini gösterir.

```bash
kubectl exec -n coremetry chc-0 -- clickhouse-client -d coremetry -q "
SELECT normalized_query_hash AS h, count() runs,
       round(quantile(0.9)(query_duration_ms)) p90_ms,
       formatReadableQuantity(sum(read_rows)) total_rows,
       substring(replaceRegexpAll(any(query),'\\\\s+',' '),1,60) q_sample
FROM clusterAllReplicas('coremetry', system.query_log)
WHERE event_time > now()-1800 AND type='QueryFinish'
  AND is_initial_query AND query_kind='Select'
GROUP BY h ORDER BY sum(read_rows) DESC LIMIT 5 FORMAT Vertical"
```

`normalized_query_hash` bind literal'leri farklı olsa da aynı sorguyu
gruplar. Alias olarak `sample` YAZMA — rezerve kelime, `Code: 62`.

> **KURAL:** `is_initial_query=1` **kullanıcı-algılanan süreyi** verir;
> **tarama maliyeti (`SelectedMarks`/`SelectedParts`) `is_initial_query=0`
> bacaklarındadır.** Initiator'da `marks=0` + yüksek
> `NetworkReceiveBytes` → yavaşlık initiator'da değil, shard'da ya da ağda.

Sıralama ölçütü seçimi: `sum(read_rows)` = "toplam yük",
`quantile(0.5)(query_duration_ms)*count()` = "toplam bekleme". Farklı
sorguları öne çıkarırlar.

### Yorumlama tablosu — `read_rows` / dönen satır

| Oran | Ne demek | İlk bakılacak yer |
|---|---|---|
| ≈1–100:1 | Sağlıklı, granül eleme çalışıyor | Darboğaz sorgu değil: havuz, N+1, serialization |
| ~1.000:1 | MV yolu ya da PK öneki var, pencere geniş | Pencere daraltma / MV tier |
| ~10.000:1 | **PK öneki kullanılmıyor** ya da ham yola düşülmüş | `EXPLAIN` → `Keys:` listesi |
| ~50.000:1+ | Zaman sınırı **budamıyor** | ADIM 3 |
| `read_rows` ≈ tablo toplamı | Tam tarama | Zaman sınırının **yeri** |
| `read_rows` normal ama `read_bytes` 5–7× | Satır değil **kolon** maliyeti: şişman `Array(String)` dekompresyonu | PREWHERE / terfi kolonu |
| `read_rows` düşük, süre yüksek | CPU/RAM: agregat state, merge, `FINAL` | `MemoryUsage`, `system.merges` |
| Ham `spans`'ten aggregate | **MV-bypass — mimari ihlali** | `/clickhouse-schema` |

## ADIM 3 — Sayfalama: LIMIT var ama tüm aralık taranıyor

`LIMIT` **taramayı sınırlamaz**, dönen satırı sınırlar. Üç imza:

1. **Zaman sınırı JOIN'in `ON` yan tümcesinde.** ON-clause yüklemleri join
   koşuludur, tarama filtresi değil — CH partition/PK budayamaz.
   *Taze emsal v0.9.1285:* 10 dakikalık soru 10 günlük tabloyu tarıyordu,
   **22.69M → 75.65K satır, 27.9s → 0.099s**. Çare: sınırı JOIN'in
   içindeki alt sorguya taşı.
2. **Zaman sınırı ifade içinde sarılı** (`toDate(time) = …`) → indeks
   kullanılamaz. Çıplak kolon karşılaştırması yaz.
3. **Sıralama tüm aralığı gerektiriyor** — `ORDER BY duration DESC LIMIT 50`
   pencerenin tamamını okumak zorunda. Çare: MV/rollup tier ya da
   cursor/`search_after` sayfalama.

## ADIM 4 — Frontend teşhisi

| # | Sınıf | Teşhis |
|---|---|---|
| S1 | `timeRangeToNs` JSX'te → sonsuz refetch (v0.5.184) | Network'te **aynı istek tekrar tekrar**; `useMemo` dışında `timeRangeToNs` ara |
| S2 | Chart yeniden mount / uPlot hooks-in | Etkileşimde chart flaş; hook dizisi her render yeniden kuruluyor mu |
| S3 | >100 satır tabloda `content-visibility` yok | Scroll takılması; `useDataTable` + `contentVisibility:'auto'` var mı |
| S4 | Polling <10s ya da `document.hidden` guard'ı yok | Sekme arkadayken Network akıyor mu |
| S5 | Bundle / eager chunk | **İlk açılış** yavaş, etkileşim değil; chunk boyutlarına bak |
| S6 | React Query anahtar kararsızlığı (her render yeni obje) | S1 ile aynı semptom; anahtar dizisinde inline obje/dizi ara |
| S7 | ES-maliyet: liste prefetch'i | Liste açılışında N istek; kural **expand/open'da fetch** |

Karar kısayolu: *aynı istek tekrarlanıyor* → S1/S6 · *ilk açılış* → S5 ·
*etkileşim* → S2/S3/S7.

## ADIM 5 — Rapor formatı

```markdown
## Semptom
<operatörün cümlesi> · yüzey: <sayfa/uç> · pencere: <range> · ortam: <lokal|prod>

## Ölçüm
| | değer |
|---|---|
| TTFB (sıcak/soğuk) | … / … |
| X-Cache | … |
| read_rows / dönen | … |
| read_bytes | … |
| EXPLAIN PrimaryKey Keys | … |
| Granules seçilen/aday | … |

## Kök neden
<tek cümle — ölçüme dayanan, hipotez değil>

## Düzeltme
<en yüksek etkili TEK değişiklik; alternatifleri neden elediğini yaz>

## Doğrulama sözleşmesi
Düzeltmeden SONRA aynı ölçüm tekrarlanacak; kabul eşiği: <read_rows < X, ttfb < Y>
```

Rapor tek başına bir sonuçtur — dilim açmak zorunda değilsin.
**"Ölçtüm, darboğaz burada değil" tam bir cevaptır.**

## ÖRNEK VAKA — CHANNEL_CODE / FUNCTION_CODE

**Tek cümle:** Terfi kolonları `CHANNEL_CODE` yazımını okuyordu, prod
`channel_code` yazıyordu — kolonlar **11 gün boş kaldı**, her okuma
sessizce şişman `Array(String)` yoluna düştü, ve ilk teşhis yanlış yöne
baktı çünkü **doğrulanan postkoşul (kolon VAR mı) ihtiyaç duyulan
postkoşul (kolon DOLU mu) değildi.**

**ADIM 1 — iki dalga, ikisi de backend.** Dalga 1: "attribute kolonları
açıkken sayfa çok yavaş, kapalıyken normal" — kolon aç/kapa bir FE
durumu ama fatura sunucuda. Dalga 2: `?filters=[{"k":"channel_code"…}]`
**26 sn**, `max_execution_time=25` tavanına takılıp sayfa hiç dönmüyor.
Dalga 2'nin tarihi tesadüf değil: v0.9.542 kolonları tablonun sağından
Time↔Service arasına almıştı — *görünür kolon kullanılır, kullanılan
kolon filtrelenir, filtrelenen kolon timeout eder.*

**ADIM 2 — ölçüm.** Dalga 1 (36.030 span, 3 koşum medyanı):

| Senaryo | read_rows | read_bytes | avg |
|---|---|---|---|
| Kolon kapalı | 11.590 | 812 KiB | 26.0 ms |
| Kolon açık | 11.590 | **5.53 MiB** | 52.3 ms |
| Oran | **1.00×** | **6.97×** | ~2× |

Yorumlama tablosunun *"read_rows normal ama read_bytes 5–7×"* satırı:
maliyet satırdan değil, her satırda dört şişman dizinin
dekompresyonundan. Dalga 2'de ham yol 24 saatlik pencerede **3.80M satır**
okuyordu — tablonun tamamından fazlası, 50 sonuç için (**76.078:1**).
Ham yola düşmek için tek bir filtre yetiyordu.

**İlk hipotez ve neden yanlıştı.** Teşhis şunu koşmuştu:
`SELECT attr_channel_code FROM spans WHERE time >= now()-1s LIMIT 1`
→ hata yok → *"prod'da zaten en ucuz katmanda koşuyor, öncelik DÜŞÜK."*
Bu sonuç plan dokümanına da sızdı ve bir iş kalemi düştü.

Hata: sorgu **kolonun var olduğunu** kanıtlar; teşhis onu **kolonun dolu
olduğunun** kanıtı saydı. `LowCardinality(String)` boş dizi araması `''`
döndürür — hata yok, satır var, sorgu "başarılı". Kanıt gözün önündeydi:
operatörün ekran görüntüsünde **değer boştu**. Düzeltme commit'inin kendi
ifadesi: *"ekran görüntüsünde değerin boş göründüğünü fark edip üstünde
durmadım."*

Nasıl kırıldı: 10 dakikalık pencerede `channel_code` taşıyan **2.67M
span**, `CHANNEL_CODE` taşıyan **sıfır**. Öncelik DÜŞÜK → YÜKSEK.

**Gerçek kök neden — dört katman, hiçbiri tek başına yeterli değil:**
kolon ifadesi yanlış yazımı okuyor · yönlendirme haritası da büyük-harf
anahtarlı · **filtre yolu haritaya hiç bakmıyor** (en pahalı taraf) ·
skip index hiç yok.

Kimse fark etmedi çünkü hata modu **sessiz ve doğruydu**: yavaş ama doğru
sonuç. Yanlış sayı yok, boş ekran yok, log satırı yok — yalnız aylarca
ödenen fatura.

**Ölçülmüş fatura** (10M satır, 3 koşu medyanı):

| Yol | read_rows | read_bytes | ms |
|---|---|---|---|
| dizi açma | 10.000.000 | 3.90 GiB | 362 |
| terfi kolonu | 10.000.000 | 1.98 GiB | 204 |
| kolon + `set(0)` indeks | **1.310.720** | **261 MiB** | **81** |

> **Kritik bulgu: kolon tek başına yalnız 2×.** Yeni eklenen MATERIALIZED
> kolon eski part'larda saklanmaz, okuma anında diziden hesaplanır. Asıl
> kazanç skip index'te. *"Kolonu düzelttik" ≠ "hızlandı"* — ölçmeden
> bilmenin yolu yoktu.

**Düzeltme — sıra zorunlu:** v0.9.621 kolonu DOLDUR (iki yazımı da oku;
onarım MODIFY değil DROP+ADD, çünkü MODIFY eski part'ları onarmıyor) →
v0.9.622 filtreyi kolona BAĞLA (harita boşken dizi yolunda kal: *boş bir
kolona yönlenmek "hiç trace yok" derdi; yanlış sonuç yavaş sonuçtan
kötüdür*) → v0.9.623 **granülü ELE** (`set(0) GRANULARITY 4`). 26 sn
timeout üçüncü adımda kalktı. DDL sırası CH kısıtı:
`DROP INDEX → DROP COLUMN → ADD COLUMN → ADD INDEX`, aksi hâlde `Code: 47`.

**Aynı kök nedenin dört kurbanı daha:** AI iş-boyutu kırılımı prod'da
sıfır satır · küme kipinde düzeltme iki restart istedi · GENİŞ rollup
ailesinin boyutları sabit boş (ve bu kolonlar `ORDER BY` önekindeydi —
birincil anahtarın bileşeni hiçbir şey elemiyordu) · Aggregated sekmesi
haritaya bakmıyordu.

### Ders — yeni kolon/boyut check-list

Sorulan soru *"kolon oluştu mu, her shard'a indi mi?"*.
Sorulmayan soru ***"bu ifade gerçek veriyle eşleşiyor mu, kolon DOLU mu?"***

| İddia | Yanlış kanıt | Doğru kanıt |
|---|---|---|
| Kolon **var** | `SELECT col … LIMIT 1` hata vermedi | `system.columns`'ta satır |
| Kolon **dolu** | ~~aynı sorgu~~ | `countIf(has(attr_keys,'k')) > 0 AND countIf(col != dizi_ifadesi) = 0` |
| Kolon **kullanılıyor** | ~~gösterim haritaya bakıyor~~ | filtre/kırılım/gruplama/rollup dallarının **her biri ayrı** |

```
[ ] İfadenin okuduğu anahtar CANLI VERİDE var mı?  → arrayJoin(attr_keys) sayımı
[ ] Kolon DOLU mu?                                 → countIf(col != '')
[ ] Kolon dizi ifadesiyle EŞDEĞER mi?              → uyuşmazlık = 0
[ ] Kolonu okuyan KAÇ dal var? (gösterim/filtre/kırılım/gruplama/rollup/MV)
[ ] Kolon tek başına yeterli mi, skip index gerekiyor mu? → ÖLÇ
[ ] DDL sırası CH kısıtına uygun mu? (Code 47)
[ ] Ertelenen DDL kipinde probe ne zaman koşuyor? (iki-restart tuzağı)
```

Türev dersler: türetilmiş kolonun ifadesi bir **varsayımdır**
(harf-düzeni, önek, birim, tip aynı sınıf) · **sessiz-ve-doğru fallback
en pahalı hata modudur** — fallback'e düşüldüğünde log bas · bir
optimizasyon eklendiğinde onu okuyan **tüm dallar sayılmalı** ·
**kolon ≠ hız** · teşhis dokümanı yanlışsa silinmez, **üstü çizilir**
(silinseydi aynı yanlış probe aynı gerekçeyle tekrar yazılırdı).

## Ölçüm disiplini

**Tek ad-hoc zamanlama yalan söyler.** Kanıtlı olay: bir sorgu yeniden
yazımı ilk ölçümde 0.428s → 0.141s (**3× hızlanma**) göründü; `query_log`
medyanı + gerçek ifadeyle eski **58 ms**, yeni **65 ms** — yani
**regresyon**. Ad-hoc zamanlamaya güvenilseydi prod'a gidecekti.

Kurallar: `query_log` medyanı (≥3 koşu) · soğuk/sıcak ayrımını `X-Cache`
ile açıkça belirt (`?refresh=1` ile soğuk zorla) · gerçek ifadeyi ölç,
sadeleştirilmiş kopyasını değil · belirsizse **gönderme**.

## `/scale-audit` ile sınır

`/scale-audit` *"başka nerede aynı hata var?"* sorusunu **grep'le**;
`/perf-triage` *"bu yavaşlığın nedeni ne?"* sorusunu **ölçümle** cevaplar.

| | `/scale-audit` | `/perf-triage` |
|---|---|---|
| Tetikleyici | çeyreklik tarama | **tek somut şikâyet** |
| Kapsam | TÜM repo | **tek istek zinciri** |
| Yöntem | statik grep, **hiç ölçmez** | **ölçüm önce** |
| Bitiş | `/kuyruk`'a scale kalemi | ölçülmüş dilim ya da "çürütüldü" |

**Devir kuralı:** `/perf-triage` bir kök neden bulup aynı sınıfın başka
yerlerde de olabileceğini görürse (v0.9.1285'te 5 site, CHANNEL_CODE'da 4
kurban), yayılım taramasını `/scale-audit`'e devreder — kendi kapsamını
genişletmez.

⚠️ `make audit`'in dokuz CHECK'inin **hiçbiri ölçüm yapmaz** (grep/awk).
`make audit` yeşilken `/api/services` 2 saniyeye çıkabilir ve script
göremez. Bilinen kapsam boşlukları: `FROM metric_points` / `FROM logs`
sınır denetimi yok · `refetchInterval` polling'i denetlenmiyor ·
CHECK 5b GLOBAL'ı görüyor ama alt sorgudaki zaman sınırını denetlemiyor.

## Bilinen ölçüm altyapısı boşlukları

- **Prod'da `query_log` muhtemelen KAPALI** (`log_queries=0`). Bu
  doğrulanmadı; prod'da `SELECT count() FROM system.query_log` yoklanmalı.
  Kapalıysa ADIM 2 prod'da kullanılamaz, elde yalnız kümülatif
  `system.events` kalır.
- **`query_id`/`log_comment` yok** — API ucu ↔ `query_log` bağı bugün
  yalnız metin eşleştirmesiyle kuruluyor. Tek satırlık düzeltme
  (`SETTINGS log_comment`) bu akışın en yüksek getirili iyileştirmesi.
- **`/admin/clickhouse` yavaş-sorgu paneli `clusterAllReplicas`
  kullanmıyor** — çok-node prod'da tek node gösterir.
