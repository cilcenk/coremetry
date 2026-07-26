# /metrics — Grafana Metrics Explorer (Drilldown) parite brief'i

Kaynak sürüm: çalışma ağacı, 2026-07-26 (v0.9.256 sonrası). **Hiçbir ürün dosyası
değiştirilmedi.** Tüm satır numaraları bugün okunan ağaçtan.

Kural: `/metrics` **katalog** olarak kalır (Explore v2 Faz 5 kararı, v0.9.123/124) ve
katalog tablosunun düzeni/kolon sırası/satır etkileşimi **aynen** durur — v0.9.61
Operations redesign ve v0.8.428 kart-feed reddi geçerli. Bu brief'teki her yüzey **ek**:
yeni kolon, yeni şerit, yeni sekme, yeni satır-içi link. Mevcut hiçbir tablo yeniden
düzenlenmiyor.

Ortak ship kuralları (her dilim için geçerli, tekrar etmiyorum):
`lib/types.ts` tip → `lib/api.ts` istemci → `s.serveCached` (anahtar TÜM girdileri sıralı+FNV
hash'ler, `len()` değil — v0.5.187) → auth kapısı → loading/error/empty → `npx tsc --noEmit`
+ `go build ./...` + `go test ./...` → saf yardımcı için tablo-güdümlü regresyon testi.
Grafik = **uPlot**, yeni kütüphane yok; renkler `data-theme` değişiminde yeniden çözülür.

---

## Parite boşluğu tek cümlede

Grafana Drilldown daha iyi bir grafik değil — **bir keşif döngüsü**: metriği adıyla
bilmeden şekline bakarak bul, hangi etiketin patladığını otomatik parçala, o etiket=değer
çiftini kalıcı filtre çubuğuna at, sahnedeki her panel daralsın, tekrarla. Coremetry bu
döngünün **analitik yarısını zaten gemide taşıyor** — bucket'lı GROUP BY (`metricquery.go:40`),
histogram_quantile (`metricquery.go:121-126`), reset-korumalı rate (`metricquery.go:112-114`),
gerçek OTLP exemplar'lar, tam PromQL (`internal/api/promql.go:36`) — ve **keşif yarısını hiç
taşımıyor**, çünkü keşif yarısının tamamı bir *seri indeksi* sorusu: Prometheus'ta ters indeks
milisaniyede cevaplar, ClickHouse'ta `metric_points` üzerinde ham `DISTINCT` taramasıdır.
Üstüne, elde olan iki yarıyı birbirine bağlayan kapı **her ekleminde sızdırıyor**: katalog
satırı unit'i düşürüyor (`urlCodec.ts:62-73`), tip sözlüğü ölü olduğu için her counter
`agg=avg` açılıyor (`metricTemplates.ts:191-194`), pencere dört ayrı çıkışta kayboluyor,
DB drill'inin filtresi backend'de sessizce yutuluyor (`filterexpr.go:296-299`). Yani parite
işi iki ayrı iş: **(A) kapıları sızdırmaz yapmak** — saatlik, çoğu düpedüz bug — ve
**(B) keşif yüzeylerini eklemek** — ve B'nin maliyet profili Grafana'nın tersi: Grafana N
ucuz indeks sorgusu atar, ClickHouse tek taramada N breakdown'ı `arrayJoin` ile birlikte
üretir. Grafana'nın panel-başına-sorgu fan-out'unu birebir kopyalamak bu sayfadaki tek
gerçek başarısızlık modudur.

---

# A. Kapılar — önce bunlar (hepsi bug, hepsi saatlik)

## M1 — `classifyMetric`'in tip sözlüğü ölü: canlı kataloğun %70'i Explore'a `agg=avg` açılıyor

**Ne gösteriyor.** Katalog satırına tıklandığında Explore'un açılış agg'ı doğru olur.
Bugün `fallbackForType` (`frontend/src/lib/metricTemplates.ts:187-196`) `counter` /
`updowncounter` kelimelerine göre dallanıyor; backend bu kelimeleri **hiç yazmıyor**.
`Instrument` alanının tek yazarı `internal/otlp/convert.go:270` ve sözlüğü beş kelime:
`gauge` (:288) · `sum` (:295) · `histogram` (:314) · `exp_histogram` (:341) · `summary` (:363).
Yani `case 'counter'` (:191) ve `case 'updowncounter'` (:192) **ölü dallar**; `sum` ve
`exp_histogram` `default: agg='avg'`e (:194) düşüyor. Yalnız `TEMPLATES` içindeki 15 ad-regex'i
(`metricTemplates.ts:44-183`) kaçıyor.

**Veri.** Yeni sorgu yok. `metric_catalog` `instrument`'ı zaten `anyState` ile taşıyor
(`internal/chstore/store.go:2944`), `MetricInfo.type` olarak dönüyor
(`internal/chstore/model.go:298-303`). **Yeni MV / kolon GEREKMİYOR.**

**API.** Yok. Saf frontend fonksiyonu.

**Kardinalite.** Yok — sorgu şekli değişmiyor, seri sayısı değişmiyor.

**Efor.** ~1 saat + tablo-güdümlü test (modül için bugün **hiç test dosyası yok**).

**Riskler — ve reçetenin düzeltilmesi gereken yeri.**
- **`'sum' → agg:'sum'` YANLIŞ, `'last'` doğru.** OTLP `Sum` hem monotonic Counter'ı hem
  UpDownCounter'ı kapsıyor ve canlı minikube kataloğunda 92 `sum` metriğinin **70'i cumulative
  monotonic, 9'u UpDownCounter, delta olanı sıfır**. `metricAggToSQL` (`metricquery.go:186`)
  `sumOrNull(value)` üretiyor; cumulative counter'da `value` koşan toplam olduğu için
  Σ(koşan toplamlar) = toplam × bucket'taki datapoint sayısı — operatör zoom'ladıkça **büyüklüğü
  değişen** bir sayı. `avg`'dan daha kötü bir yalan. `sum` yalnız delta temporality'de doğru.
  Frontend monotonic/up-down ayrımını **yapamaz**: `metric_catalog` yalnız dört state taşıyor
  (`store.go:2941-2945`), `is_monotonic`/`temporality` yok. `last` her iki popülasyonda da
  savunulabilir: UpDownCounter için doğru, cumulative counter için dürüst ham değer.
- `'exp_histogram' → 'p99'` tartışmasız doğru (`isHistogramInstrument`, `metrichist_percentile.go:55-57`
  ikisini zaten aynı sayıyor) ama canlıda **sıfır seri** — risksiz, faydası da ölçülmemiş.
- p99 seçilince okuma yolu değişiyor: `metricInstrument` probe'u + `QueryMetricHistogram`
  200k satırlık dizi çekimi (`metrichist.go:87-94`). Sınırlı (LIMIT + zaman + `max_execution_time=30`)
  ama `avg`'ın bir `avgOrNull` GROUP BY'ıyla **aynı maliyet değil**. `histogram` zaten bugün
  p99 tohumluyor, yani tutarlı — ama sürüm notunda söylenmeli.
- Ölü `counter`/`updowncounter` dalları silinir veya alias olarak bırakılıp üstüne
  `convert.go:270` sözlük kaynağı olarak yorumla sabitlenir — bir sonraki kayma testte yakalansın.

## M2 — `MetricLabelValues` CH-sınır kuralını ihlal ediyor (`max_execution_time` yok, `since` clamp'siz)

**Ne gösteriyor.** Görünür bir şey değil — **M6/M7/M8'in ön koşulu**. Herhangi bir etiket
tarayıcısı bu primitifi dövecek, bugünkü hâliyle CH tarafında devre kesici yok.

**Veri.** `internal/chstore/metricquery.go:252-279`, birebir:
```
SELECT DISTINCT <expr> AS v FROM metric_points
WHERE metric = ? AND time >= ?  ORDER BY v LIMIT 200
```
`SETTINGS` yok, `time <= ?` yok. On satır yukarıdaki kardeşi `MetricAttrKeys` üçünü de taşıyor
(`metricquery.go:231`: `time <= ?` + `LIMIT 100` + `max_execution_time = 5`).
`since` handler'da clamp'siz: `api.go:4107` `parseDuration(q.Get("since"), 24*time.Hour)`.
`metric_points` `ORDER BY (service_name, metric, time)` (`store.go:1271`) olduğu için
metric-only yüklem PK prefix'i **değil** — her servisin o metrik dilimi okunur; `attr_values`
`Array(String)` ve üzerinde data-skipping index yok (`store.go:1252-1253`).

**API.** Yeni uç yok. `/api/metrics/labels` (`api.go:4105-4113`) davranışı aynı; `since`
clamp'lenip cache anahtarına sınırlı rung olarak girer (v0.8.270 disiplini).

**Kardinalite.** Sorgu şekli değişmiyor; `DISTINCT` LIMIT'ten **önce** hesaplandığı için
`LIMIT 200` işi sınırlamıyor — sınırı `max_execution_time` koyacak.

**Efor.** ~30 dk, ~4 satır + `internal/chstore/groupkey_metric_test.go` desenli tablo testi.

**Riskler / dürüstlük notu.**
- Savunmayı **`SETTINGS max_execution_time = 5` + `since` clamp** üzerine kur. `time <= ?`
  burada **kozmetik**: partition `toDate(time)`, gelecek partition yok, `time >= cutoff` zaten
  aynı partition setini buduyor. Kardeşle guard-paritesi dışında bir şey satın almıyor.
- Bugünkü çağrı hacmi küçük: `MetricQueryEditor.tsx:241-248` effect deps'i `[adding, metric, k]`
  — yazılan değer `v` **dep değil**, `<datalist>` istemci tarafında süzüyor. "Her tuş vuruşunda
  bir istek" doğru değil. `LABEL_KEYS` 13 elemanlı sabit `<select>` (`MetricQueryEditor.tsx:47-51`),
  `since` tek literal `'24h'` (`api.ts:1639-1640`). Yani anahtar uzayı küçük, isabet oranı yüksek.
- Modal vaka en ucuzu: `LABEL_KEYS[0] = 'service.name'` → `filterexpr.go:264` `service_name`
  **kolonuna** eşliyor, tamamı PK. Şişman `Array(String)` okuması yalnız diğer 10 anahtarda.
- Kalan gerçek risk, gerekçeyi tek başına taşır: soğuk anahtarda o 10 dizi-anahtarından biri
  1000 servisli kurulumda paylaşılan bir metrikte (`jvm.memory.used`) retention penceresi boyunca
  çok-GB'lık kolon okur; `serveCached` SWR yenilemesi 20s bağlamla koşar (`cache.go:349`), yani
  sınır 20 saniyedir, sonsuz değil. Düşük frekans, yüksek patlama yarıçapı → devre kesici hak ediyor.
- `MetricAttrKeys`'in `service` parametresini **taklit etme** — imza + çağrı + FE değişikliği,
  tahmini şişirir; çağıranın kapsamında servis zaten yok.

## M3 — DB drill modalinin attribute filtresi backend'de sessizce yutuluyor (P0)

**Ne gösteriyor.** Oracle/Postgres/MySQL/Redis drill modali bugün **filtresiz** çiziyor, ama
üstünde `tablespace_name = "SYSTEM"` çipini basıyor (`shared.tsx:220-224`). UI var olmayan bir
filtreyi iddia ediyor.

**Veri / kök neden.** `OracleDrill.filters` tipi `{ key, op, value }[]` (`shared.tsx:26`,
üreticiler `OraclePanel.tsx:34`, `PostgresPanel.tsx:101`, `RedisPanel.tsx:102`), ham
JSON'lanıp gönderiliyor (`shared.tsx:189-191`). Sözleşme `FilterExpr` = `{k, op, v[]}`
(`frontend/src/lib/types.ts:1752-1756`), Go ikizi `internal/chstore/filterexpr.go:11-15`
tag'leri `k`/`op`/`v`. Go tag'e göre eşleştiği için `key`/`value` **hiçbir şeye bağlanmıyor**;
`FilterExpr.sql()` `missing key` hatası döndürüyor (`filterexpr.go:131-132`) ve
`ApplyMetricFilters` hatayı `continue` ile **yutuyor** (`filterexpr.go:296-299`). 400 yok,
operatörün göreceği log yok. Ayrıca modal `service`'i hiç geçmiyor (`shared.tsx:192-197`)
oysa panelin kendi verisi instance-kapsamlı (`internal/chstore/oracle.go:161-168`) — iki
Oracle instance'ında modal ikisini harmanlıyor.

**API.** Yeni uç yok. Tip düzeltmesi + `service: instance` parametresi.

**Kardinalite.** Filtre doğru uygulandığında sorgu **daralıyor**; tarama baytı aynı kalıyor
(`attr_values[indexOf(attr_keys, ?)]` granül-sonrası satır yüklemi, `filterexpr.go:152`),
CPU'ya bir `indexOf` ekliyor. Seri sayısı 1 → 1: `blankQuery`'nin `splitBy`'ı boş
(`model.ts:88-94`), yani bugünkü hâli "her tablespace ayrı çizgi" değil, **hepsi tek
`avgOrNull` çizgisinde ezilmiş** — fan-in'den daha sinsi.

**Efor.** ~2 saat. Tip `shared.tsx:26` + üç üretici + etiket render'ı (`shared.tsx:222`) +
`service`. Regresyon testi: `{"key":…}` şeklinin artık round-trip etmediğini pinleyen saf
`filterexpr` tablosu.

**Riskler.**
- **Sıra önemli.** Bu, M4'ün link yarısından ÖNCE gitmeli. Ters sırada Explore linki doğru
  filtreli, üstündeki grafik filtresiz olur — kapatılmak istenen ayrışma bu sefer gerçekten
  doğar.
- Cache anahtarı `filtersRaw`'ın **tamamını** hash'liyor (`api.go:4057-4058`) — v0.5.187 temiz.
  Tıklanan tablespace başına bir giriş, sınırlı.

## M4 — Unit tek kaynaktan aksın: `normalizeUnit` + katalog hand-off'u + MRU yazımı

**Ne gösteriyor.** Explore'da y ekseni ve tooltip metriğin **gerçek** OTLP unit'iyle
formatlanır; katalogdan açılan metrik Explore'un "Recent" halkasına düşer.

**Veri.** `metric_catalog` unit'i `anyState(unit)` ile taşıyor (`store.go:2944`), katalog
satırı basıyor (`Metrics.tsx:161`), sonra `metricCatalogueHref` atıyor:
`blankQuery`'nin `unit: ''`'ı (`model.ts:91`) geçiyor, `encodeBuilder` boş unit'te `u`'yu
atlıyor (`urlCodec.ts:41`), `queryUnit` metric kaynağında `q.unit`'i aynen döndürüyor
(`model.ts:183-184`). **Yeni MV / kolon GEREKMİYOR** — `metric_points.unit` zaten
`LowCardinality(String)`.

**API.** Yok.

**Kardinalite.** Yok. Tamamen görüntü katmanı.

**Efor.** (b) yarısı ~1,5 gün · (a) yarısı ~30 dk. **Sıra tersine çevrilmeli: (b) önce.**

**Riskler — reçetenin iki yerinde ölümcül hata var.**
- **`classifyMetric().unit`'i OKUMA.** Registry `http.server.request.duration`'a `'ms'`
  damgalıyor (`metricTemplates.ts:48-53`) oysa stabil semconv bunu **saniye** yayınlıyor ve
  `java-demo/Dockerfile:14` agent'ı (2.10.0) stabil hattı yazıyor → `fmtSmart(0.234,'ms')` =
  **"0.23ms"**, gerçek 234ms. `db.client.operation.duration` (:70) ve `jvm.gc.duration` (:132)
  aynı sınıf. `cmd/demo/main.go:1449` yerelde `ms` yayınladığı için **lokalde asla görünmez** —
  klasik local-geçer/prod-yalan (`feedback-ch-vs-es-divergence`). Tek güvenli kaynak
  `MetricInfo.unit` (`model.go:300`), zaten `Metrics.tsx:106`'da elde.
- **(a)'yı (b)'siz göndermek net regresyon.** `fmtSmart` default dalı `fmtCount(v) + ' ' + u`
  (`chartFmt.ts:55`) ve OTLP UCUM yayınlıyor: `1`, `By`, `{sessions}`, `{transfer}/s`.
  Sonuç canlı triage yüzeyinde `"0.85 1"`, `"1.2k {sessions}"`, `"8.6G By"` (bugünkü
  suffix'sizden **daha kötü**). Not: bu hata **bugün de canlı** — `QueryRow.tsx:107` picker'dan
  seçende `unit: m.unit` zaten yazıyor. Yani (b) bug fix, (a) tutarlılık yaması.
- `normalizeUnit(otlp) → {display, scale}` saf yardımcı: `By`→`B`, `{x}`→`''`, `1`→`''`.
  **`1`→`%` otomatik ölçeklemesini bu turda REDDET**: ×100 `fmtSmart` içinde yapılamaz, o
  fonksiyon `exploreCsv.ts:20,28` ile paylaşılıyor ve CSV'yi sessizce bozar. (Latent örnek:
  `metricTemplates.ts:100-104` CPU'yu `unit:'%'` ilan ederken `threshold.value: 0.85` yazıyor.)
- **`feedback-unit-mixing-needs-both-branches` burada zorunlu:** tablo-güdümlü test
  vokabülerin HER dalını kapsar (`ms`, `s`, `%`, `B`, `By`, `1`, `{x}`, `{x}/s`, boş), tek
  `ms` dalı değil.
- Sızıntı **üç eklemli**, biri yamalanırsa diğer ikisi tutarsızlık üretir:
  (1) `Metrics.tsx:106`; (2) `shared.tsx:226` — `drill.unit` iki satır aşağıda kullanılıyor
  (`shared.tsx:246`) ama link'e girmiyor, üstelik `OracleDrill.unit` **ev vokabüleri**
  (`'/s'`,`'B'`,`'s'` — `OraclePanel.tsx:91-118`) yani aynı metrik iki farklı eksen etiketi
  taşır; (3) `pinToDashboard.ts:44-53` `q.unit`'i düşürüyor oysa `MetricPanelConfig.unit`
  var (`types.ts:2362`).
- MRU (`recordMetricPick`, `recentMetrics.ts:36`, 8 derin) bugün **yalnız**
  `GroupedMetricPicker.tsx:79`'dan yazılıyor. Aynı satırda (`Metrics.tsx:105`) yazmak bir
  satır — ama **tasarım kararı**: katalogda satır tıklaması navigasyonun kendisi, sekiz
  gezinme tıklaması Explore-öncelikli operatörün kasıtlı çalışma setini süpürür. Alınırsa
  "halkanın sahibi katalog gezinmesidir" kabul edilmiş olur.
- Test borcu dürüstlük notu: `frontend/` altında 90 `.test.ts`, **sıfır `.test.tsx`**;
  jsdom/RTL bağımlılığı yok. Çağrı-yeri kablolaması bugünkü saf-fonksiyon disiplininde
  test edilemez — `normalizeUnit` testlenir, kablolama testlenmez.

---

# B. Parite yüzeyleri — hepsi EK, hiçbiri mevcut tabloyu değiştirmiyor

## M5 — Katalogda `Last seen` + range picker'ın dürüstleşmesi + facet etiketinin gerçeği

**Ne gösteriyor.** Üç küçük dürüstlük düzeltmesi, tek dilim.
(a) Katalog tablosuna **`Last seen`** hücresi — Grafana'nın metrik listesi tazelik hakkında
hiçbir şey söylemez, bu ucuz bir aşma. (b) Katalog dalında range picker'ın **gizlenmesi**
(`?editor=1` modunda kalır). (c) Facet çiplerine "(ilk 200 içinde)" etiketi.

**Veri.** `metric_catalog` `maxState(time) AS last_seen_state` taşıyor (`store.go:2945`);
okuma onu **yalnız** `HAVING maxMerge(last_seen_state) >= ?` içinde kullanıp projeksiyona
hiç sokmuyor (`repo.go:3891-3895`). SELECT'e `maxMerge(last_seen_state)` eklemek **bedava** —
CH o aggregate'i HAVING için zaten hesaplıyor. **Yeni MV / kolon GEREKMİYOR.**

**API.** `MetricInfo`'ya `lastSeenNs?: number` (`model.go:298` + `types.ts:1709`).
`/api/metrics/names` cache anahtarına **şekil token'ı** eklenmeli (`api.go:3999`) yoksa
rolling deploy'da paylaşımlı Redis eski gövdeyi `ttl*staleFactor` boyunca servis eder
(`cache.go:228-247`) → boş kolon.

**Kardinalite.** `metric_catalog` `ORDER BY (service_name, metric)`, satır sayısı
(servis × metrik) ile sınırlı — datapoint hacmiyle değil. Ek aggregate yok, ek tarama yok.
Dağıtık güvenli: üç registry'de de kayıtlı (`cluster.go:294` / `:356` shard key
`cityHash64(service_name)` / `:942`), servisler shard'lar arası ayrık olduğundan
`maxMerge` initiator'da doğru.

**Efor.** ~yarım gün.

**Riskler — ve neyin BİLİNÇLİ dışarıda bırakıldığı.**
- **Kolon 7 günle kırpık.** `metricNameLookback = 7*24h` (`repo.go:3829`) hem sayım hem
  select'te HAVING olarak uygulanıyor (`repo.go:3884`, `:3894`), yani yanıta çıkan her satır
  7 gün içinde taze. Kolon asla "ölü" diyemez, asla 7g'yi aşamaz. Bu yüzden **sıralanabilir
  yapma** ve başlığı dürüst etiketle: *"son 7 günde aktif"*. HAVING'i gevşetmek v0.8.311 /
  v0.8.396 prod timeout'larının koyduğu sınırı kaldırmak demektir — ayrı karar.
- **`uniqExact(service_name)` kolonunu ALMA.** SQL'i geçerli ve ucuz, ama sayfanın üzerine
  hareket edemeyeceği bir soruyu cevaplıyor: `openMetric` servisi zaten atıyor
  (`Metrics.tsx:105-106`), `MetricNamePicker.tsx:45` çağrısı servis-kapsamlı olduğu için
  sayı trivial 1. Asıl hedef "descriptor'lar ayrışıyor mu" ise ölçüsü `uniqExact(instrument)`
  ve `instrument` `anyState` olduğundan iç içe aggregate **yasak** — alt sorgu (~2× tarama)
  ya da yeni `uniqState` kolonu gerekir. Ayrı dilim.
- **Sıralama sunucuda değil.** `Metrics.tsx:79` alfabetik ilk 200'ü çekiyor
  (`repo.go:3895` `ORDER BY metric` + LIMIT/OFFSET), `useDataTable` tarayıcıda sıralıyor
  (`Metrics.tsx:94-99`). "Last seen'e göre sırala" 200 alfabetik satırı sıralar. Kolonu
  **eylem alınabilir** yapmak sunucu-taraflı sort parametresi + cache anahtarı + server-driven
  sort demek; o ev kuralıyla (server-paged tablo yalnız resize yarısını alır) çakışır → bu
  turda **dekoratif ama dürüst** kolon.
- **Fallback dalı sentinel istiyor.** `ListMetricNames` katalog boş/hatalıyken ham
  `metric_points` GROUP BY'ına düşüyor (`repo.go:3962-3985`); orada `lastSeenNs` sıfır kalır →
  taze yükseltme penceresinde "1970". `0` → hücre `—`.
- (b) için gerekçe: picker bugün katalog dalında **hiçbir işe yaramıyor** — sorgu pencere
  taşımıyor (`Metrics.tsx:79`), sunucu 7 günü sabitliyor (`repo.go:3876`). `?editor=1`'de ise
  gerçekten çalışıyor (`Metrics.tsx:124`), o yüzden **kaldırma, dalda gizle**.
- (b)'nin alternatifi — picker'ı gerçek yapmak — "saatlik" değil: pencere iki store metodu +
  iki where-builder + handler + rung-snap'li cache anahtarı + fallback clamp'inden geçmeli.
  Fallback'e kullanıcı penceresi verilirse en kötü durum **365 gün** (`TimeRangePicker.tsx:136`
  custom cap) ve `metric_names_test.go:75-82` yeşil kalarak bunu **kaçırır** (yalnız `time >= ?`
  varlığını pinliyor, sınırlılığını değil). Ayrıca `metric_catalog` bilerek TTL'siz
  (`store.go:2929-2931`), yani pencereyi retention'ın ötesine açmak **ölü tık üretir**.
  → Bu turda dışarıda (bkz. §5).

## M6 — Shape preview şeridi: metriği adıyla değil **şekliyle** bulmak

**Ne gösteriyor.** Katalog tablosunun **ÜSTÜNE**, açılıp kapanan (`?preview=1`, varsayılan
kapalı) bir sparkline ızgarası: görünen sayfadaki ≤24 metrik adı, her biri seçili pencerede
gerçek bir zaman serisi. Grafana Drilldown'ın açılış duvarının karşılığı ve operatörün
"adını hatırlamıyorum ama şeklini tanırım" davranışının tek çözümü. **Tablo aynen altında
kalır** — ızgara ek bir katman, tablonun yerine geçmiyor.

**Veri.** `metric_points`, ama Grafana'nın yolundan **değil**. Grafana panel başına bir
sorgu atar (Prometheus'ta ucuz, burada ölümcül). CH-yerlisi tek batch:
```
SELECT metric, <bucket>, avgOrNull(value)         -- veya instrument'a göre last/quantile
FROM metric_points
WHERE metric IN (…≤24 ad…) AND time >= ? AND time <= ?
GROUP BY metric, bucket
LIMIT ? SETTINGS max_execution_time = 10
```
Bucket ızgarası için ev otoritesi `sparklineGrid(winSec, grainSec)`
(`internal/chstore/repo.go:902`) — grain hizalama invaryantı periyodik sahte 1.5-2× tepe
hatasını zaten kapatıyor (`sparkline_grid_test.go`). **Yeni MV / kolon GEREKMİYOR.**

**API.** **Yeni uç:** `GET /api/metrics/preview` (`names[] (≤24), from, to, service?`) →
`{ series: {metric, points[]}[] }`. `serveCached` 60s, anahtar `names`'i **sıralı + FNV
digest** ile hash'ler (`fmt.Sprintf("…:%x", fnvDigest(sortedSlice(names)))`) — `n=%d len(names)`
çapraz zehirler (v0.5.187).

**Kardinalite — bu dilimin can alıcı sorusu.** Izgara **hiçbir şeye group by yapmıyor**:
metrik başına **tek** çizgi, `GROUP BY metric, bucket`. Etiket ekseni yok, seri patlaması
yok. Üst sınır 24 × bucket sayısı ≈ 24 × 60 = 1440 nokta. Tarama maliyeti `metric IN (…)`
yükleminde; `metric_points` `ORDER BY (service_name, metric, time)` olduğundan metric-only
yüklem PK prefix'i değil — her servisin dilimi okunur. **Bu yüzden ızgara varsayılan
KAPALI** ve `service` kapsamı verildiğinde (picker'dan gelen kullanım) PK prefix'ine oturur.
24 sabit tavanı istemci tarafında zorlanır; sayfa 200 satır olsa bile bir batch 24'ü aşamaz.

**Efor.** ~1,5 gün (backend uç + batch sorgu + FE ızgara; uPlot mikro-grafik preset'i
`TimeChart` üstünde).

**Riskler.**
- **Grafana'nın fan-out'unu kopyalama.** N panel = N sorgu bu tabloda kabul edilemez;
  tek batch şart. Kod incelemesinde bu maddeye özel bakılmalı.
- Instrument'a göre agg: cumulative counter'da `avg` anlamsız çizgi verir. M1'in `last`
  kararıyla **aynı fonksiyon** kullanılmalı (`classifyMetric`) yoksa ızgara ile tıklandığında
  açılan grafik farklı sayı gösterir — kapı sızıntısının yeni bir kopyası.
- Unit karışımı: ızgara panelleri **bağımsız ölçekli**. Farklı metrikler yan yana olduğu için
  bu doğru; ama her mikro-grafiğin başlığında unit yazmalı (M4'ün `normalizeUnit`'i üzerinden),
  yoksa 2ms ile 2s aynı görünür.
- `document.hidden`'da poll yok — ızgara **tek atışlık**, poller değil.
- Boş hâl: pencerede veri yoksa mikro-grafik yerine ince bir `—` şeridi; uydurma düz çizgi yok.

## M7 — Breakdown-by-label ızgarası: **asıl fark**, ve CH'nin Grafana'yı yendiği yer

**Ne gösteriyor.** Explore'da bir metrik seçiliyken **yeni bir sekme** (`?tab=breakdown`,
`.tab-strip`): metriğin taşıdığı her etiket için bir mikro-panel (`sum by (o etiket)`), üstünde
sıralama kontrolü — *Outlying series · Highest spike · Percentage change · Widest spread ·
Alphabetical*. Panelde bir değere tıklamak o `etiket=değer`i sorgunun filtresine ekler ve
sahne daralır. Grafana Drilldown'ın tek gerçekten **analitik** parçası bu; sıralama sezgileri
seri dizileri üzerinde **saf fonksiyon** olduğu için birebir taşınır ve `/tdd` + tablo-güdümlü
regresyon disiplinine tam oturur.

**Veri.** İki adım.
1. **Aday etiket seti** — bugün `MetricAttrKeys` (`metricquery.go:217-247`, `LIMIT 100`,
   `max_execution_time = 5`, HTTP'de **açık değil**, tek çağıranı PromQL `without()`,
   `internal/promql/eval.go:263`). Bu turda o bounded primitif yeni bir uçla açılır.
   ⚠️ Explore'un bugünkü split-by önerileri `api.attributeKeys`'ten geliyor — **span**
   attribute'ları (`SplitByPicker.tsx:20`), metrik kaynağında bile. Breakdown adayları
   metrik tarafından gelmeli.
2. **Fan-out** — Grafana N ayrı `sum by (label_i)` isteği atar. CH'de **tek geçiş** yeter:
   aday anahtar pozisyonlarını `arrayJoin` ile açıp `(key, value, bucket)` üzerinden gruplamak
   bir taramada tüm ızgarayı üretir. `buildMetricQuerySQL` (`metricquery.go:40`) zaten tam
   bu (bucket, groupKey, value) demetini üretiyor; grup ifadesi anahtar başına
   `groupKeyExprMetric` ile kuruluyor (metrik-farkında: `metric_points`'te olmayan ama
   `spans`'te olan anahtarları yeniden yönlendiriyor — v0.8.381).

**API.** **Yeni iki uç:** `GET /api/metrics/attrkeys` (`metric, service?, from, to`) →
`string[]` (mevcut bounded primitifin ince kabuğu) ve `GET /api/metrics/breakdown`
(`metric, keys[]≤8, from, to, service?, filters?`) → anahtar başına seri kümesi.
İkisi de `serveCached`, anahtar tüm girdileri sıralı+FNV hash'ler.

**Kardinalite — bu sayfanın make-or-break sorusu, dilimin merkezinde.**
- **Üç ayrı tavan, üçü de sunucuda zorlanır.** (1) Kaç etiket fan-out'a girer: aday listesi
  `LIMIT 100`'den geliyor ama ızgaraya **≤8** alınır. (2) Etiket başına kaç değer çizilir:
  Explore zaten `PANEL_SERIES_CAP = 10`, operatör `TOP_N_MAX = 50`'ye açabiliyor
  (`model.ts:73`) — breakdown'da tavan **10**, "+N more" etiketiyle. (3) Hangi anahtarlar
  **teklif** edilir: ham `user_id` / `request_id` picker'a **asla** çıkmamalı.
- **(3) için bugün ucuz bir ön-kontrol YOK.** Prometheus indeksinden bedava gelen "bu split
  kaç seri üretir" sorusunun CH karşılığı, kaçınılmak istenen `DISTINCT`'in ta kendisi.
  Bu turdaki dürüst çözüm **fail-closed whitelist**: `resolverEligibility.ts:20`
  `TIER_DIM_KEYS` deseninin metrik karşılığı — küçük, sabit bir anahtar kümesi teklif edilir,
  dışındaki her şey ham yola değil, **teklife hiç girmez**. Gerçek ön-kontrol §4'teki
  `metric_series` MV'ye bağlı (bkz. M8).
- Sıralama sezgileri veri **döndükten sonra** çalışır (medyan seriden sapma, ardışık bucket'lar
  arası max delta, ilk yarı/ikinci yarı % değişim, max-min açıklığı) — ek sorgu **yok**,
  ek kardinalite **yok**.
- Precedent: BubbleUp (`internal/chstore/bubbleup.go:10`) "hangi attribute değeri bu anomaliyi
  açıklıyor" yüzeyi zaten var, ama `spans` üzerinde. Sıralama katmanı + panel ızgarası
  yeniden kullanılabilir; yalnız veri kaynağı farklı.

**Efor.** ~2 gün (sorgu + iki uç + ızgara + sıralama sezgileri ve tablo testleri).

**Riskler.**
- **Paylaşılan y ekseni varsayılan olmalı.** Grafana'nın bağımsız-ölçekli mikro-panelleri
  burada gerçek bir yanlış-okuma tuzağı: 2ms serisiyle 2s serisi aynı görünür. Breakdown'da
  tüm paneller **aynı metrik, aynı unit** olduğu için paylaşılan y anlamlı; bağımsız ölçek
  opt-out olarak kalır.
- Aday anahtar okuması `MetricAttrKeys` guard'larıyla sınırlı ama HTTP'ye açılınca çağrı
  hacmi artar — M2'nin clamp'i buraya da uygulanmalı.
- Filtre eklemek sahneyi daraltıyorsa **URL'e yazılmalı** (`replace:true`); tek-yönlü okuma
  bu repoda tekrar eden hata sınıfı (v0.8.253/256/265/267).
- Boş/az veri: pencerede 1 değeri olan etiket panel üretmez (gürültü), etiket "tek değerli"
  notuyla listelenir.

## M8 — Ad-hoc etiket filtresi çubuğu + kardinalite ön-kontrolü *(metric_series MV'ye bağlı)*

**Ne gösteriyor.** Grafana'nın kalıcı `etiket = değer` çip çubuğu: bir çip eklendiğinde
**diğer çiplerin verdiği bağlamda** geçerli anahtar/değer listesi daralır, ve bir split
seçilmeden önce "bu ~N seri üretir" rozeti görünür. Drilldown'ı dashboard'dan ayıran şey
tam olarak budur: **filtre durumu her adımda hayatta kalıyor**.

**Veri.** Bu turda **inşa edilmiyor** — ön koşulu §4'teki `metric_series` MV. Bugünkü
primitiflerle "match[] semantiği" (filtre-farkında değer listesi) **hiç uygulanabilir değil**:
`MetricLabelValues` uygulanmış filtre setini almıyor, alsa her çip için ikinci bir ham
tarama demek.

**Kaldıraç — ve bu brief'in en yüksek getirili yapısal gözlemi.** Coremetry zaten ingest'te
Prometheus-eşdeğeri bir **seri kimliği** hesaplıyor: `otlp.SeriesFingerprint` =
xxhash64(metrik + sıralı datapoint attr'ları + `service.name` + `service.instance.id`)
(`internal/otlp/fingerprint.go:51`), `metric_points.series_fingerprint`'te saklanıyor ve
`exemplars` tablosunun birincil anahtarı (`store.go:1300` `ORDER BY (series_fingerprint,
timestamp)`) — metrik→trace pivotunun JOIN'siz PK taraması olmasının sebebi bu. Yani eksik
olan **kimlik şeması değil, katalog**.

**Kardinalite.** MV satır sayısı **seri kardinalitesiyle** sınırlı, datapoint hacmiyle değil —
`metric_catalog`'un adlar için yaptığı sınırlı-durum argümanının aynısı. Ayrıntı ve dağıtık
güvenlik notu §4'te.

**Efor.** MV + probe + okuma yolları ~2 gün; üstündeki çubuk UI ~1 gün. **Bu turda değil**
(bkz. §5) — ama M7'nin whitelist'i bilerek bunun yerine konan geçici çözümdür ve MV
geldiğinde whitelist gerçek ön-kontrole yükseltilir.

---

# §3 Pivotlar — bugün kayıplı ya da ölü olanlar, ve tam düzeltmesi

Bu sayfanın etrafındaki her çıkış aynı iki hatayı tekrarlıyor: **pencere taşınmıyor** ve
**yanlış attribute'a filtreleniyor**. İkisi de `lib/pivotHref.ts:78-93`'te v0.9.256 kök nedeni
olarak **yazılı**; kardeşlere taşınmamış.

| Pivot | Bugünkü hâli | Tam düzeltme | Efor |
|---|---|---|---|
| **`?m=` doorway** (10 MetricPanel: `Overview.tsx:346,349,352,355,369,380,386` + `ServiceCharts.tsx:503,520,539`) | `metricExploreHref` yalnız `/explore?m=<blob>` üretiyor (`metricQuery.ts:122-124`), `&range=` yok. `MetricQuery.range` alanı **var** (`metricQuery.ts:32`) ve `Overview.tsx:334-338` **dolduruyor**, ama `seedFromMetricDescriptor` okumuyor (`urlCodec.ts:203-227`) ve Explore bir tick içinde URL'i kendi range'iyle yeniden yazıyor (`Explore.tsx:222`). Drag-zoom'lu `custom` pencere `persistableRange` tarafından bilerek kalıcılaştırılmadığı için (`useUrlRange.ts:52-54`) operatör **son relatif preset'e** düşüyor. | **Yalnız `MetricPanel.tsx:63`.** Bileşen zaten router içinde (`useNavigate`, `:55`): canlı `?range=`'i `useSearchParams`'tan okuyup href'e ekle (~5 satır). `seedFromMetricDescriptor`'a **DOKUNMA** — `BuilderState`'in range alanı yok (`model.ts:59-66`), dönüş tipi değişikliği gerekir ve gereksiz: Explore range'i URL'den zaten okuyor (`Explore.tsx:88-89`). `serviceRedDescriptors`'a (`resolverEligibility.ts:65-83`) `range` **EKLEME**: o descriptor'lar `api.resolveMetric`'e gidiyor (`ServiceCharts.tsx:202-205`) ve cache anahtarı ham blob'u gömüyor (`metricresolve.go:73-74`) — `custom:` **milisaniye** hassasiyetinde (`urlState.ts:10-12`), dakika bucket'lamasını yok eder, ürünün en sıcak drill-down sayfasında her drag-zoom'da 3 soğuk miss. Aynı düzeltme "Copy link" (`:123`) ve "Edit"i (`:110`) de kapatır. | ~1 saat |
| **Katalog satırı → Explore** (`Metrics.tsx:105-106`) | `metricCatalogueHref`'in range parametresi yok (`urlCodec.ts:62-73`). Aynı sayfadaki legacy dal (`Metrics.tsx:67-71`) `&range=` **ekliyor** — tek sayfadan iki çıkış birbiriyle çelişiyor. | M5(b) picker'ı katalog dalında gizlerse çelişki kaynağı kalkıyor; ayrıca `metricCatalogueHref`'e `unit` (M4) ve `range` opsiyonel arg'ları. Precedent `servicePivotHref` (`urlCodec.ts:87-107`) — pencereyi taşıyor **ve** ters pencereye karşı guard'ı var, yorumu bu hata sınıfını adıyla anıyor. | ~30 dk |
| **DB drill "Open in Explore →"** (`shared.tsx:226`) | `metricCatalogueHref(drill.metric)` — opts **hiç yok**: filtre yok, range yok, service yok. | **M3'ten SONRA.** Filtre şekli `{key,op,value}`→`{k,op,v:[value]}` dönüşümü tipte bir kez yapılınca (M3) hem fetch hem link düzelir. Range için `window` ms opt'u **ekleme**: panelin `TimeRange`'ini geçir ve `&range=${encodeRange(range)}` bas (`urlState.ts:9-14`) — `6h` preset'i **relatif** kalır, `servicePivotHref`'in dondurduğu gibi `custom:`e çevrilmez. | ~1 saat |
| **DB bağımlılık satırı → traces** (`DependenciesTable.tsx:241-247`) | `&range=` yok **ve** `peer.service`e filtreliyor oysa `db_summary_5m.instance` **altı kademeli** coalesce (`store.go:2494-2503`: `peer_service → server.address → net.peer.name → db.host → db.name → service_name → 'unknown'`). Canlı doğrulama: `clickhouse` satırı 6. kademeye (`service_name`) düşüyor, link **0 satır** döndürüyor — üstelik self-telemetry hattı. | Yalnız `&range=` eklemek **yetmez**: ticket kapanır, ölü-link yarısı yaşar (`pivotHref.ts:8-12`'nin uyardığı tam senaryo — boş liste "böyle trace yok" diye okunur). Doğru fix `messagingTracesHref` yanına **`dbTracesHref`**: altı kademeyi OR grupluyor + pencereyi taşıyor + `/traces`'e gidiyor (v0.9.256'nın kasıtlı kararı: orada filtreler düzenlenebilir çip). Testler `pivotHref.test.ts`'e; `encodeFilterGroup`'un boş-dönüş tuzağı (`pivotHref.ts:127-134`) `instance='unknown'`da aynen tetiklenir. Bedava temizlik: bugünkü dal `r.system`/`r.instance`'ı kaçışsız `"`-quoted DSL'e gömüyor (`:244-246`). | ~yarım gün |
| **External host drawer → traces** (`External.tsx:201`) | Aynı string, yorumunda "DependenciesTable'ın kullandığı DSL şekli" yazıyor. `range` kapsamda (`External.tsx:190`), taşınmıyor. Attribute yine `peer.service`, oysa `ext:` düğümü `peer_service` **ya da** `infra_host = coalesce(peer_service, server.address, net.peer.name)` (`topology.go:596-602`, dallar `:650-668`) — v0.8.448 fallback dalı **zaten peer.service yok diye** var. | **`externalTracesHref`**: üç kademeyi OR grupluyor + pencere. Kod tabanının kendi notu: *"peer.service opt-in bir ipucu, çoğu SDK hiç set etmez"* (`topology.go:653-655`). Yerelde 76288/76483 external client span'i `peer.service` taşıyor çünkü demo generator'ları açıkça set ediyor — **prod hedefi auto-instrumented Java/JBoss/.NET**, yani yerel veri iyimser vaka. | ~2 saat (dbTracesHref ile ortak) |
| **MetricPanel "Add to dashboard" / "Create alert"** (`MetricPanel.tsx:132`, `:136`) | **Ölü.** `?m=`'nin tek tüketicisi `urlCodec.ts:244`; `Dashboards.tsx` ve `Alerts.tsx` searchParams'a hiç dokunmuyor. 6 menü kaleminin 2'si × 10 panel = **20 ölü tık**, sessizce. Kod yorumu itiraf ediyor ("full consume is a later phase") ama release-tag'li TODO yok. | Bu turda **dürüst ret**: descriptor hedef modele round-trip edemiyorsa kalemi **gizle**. Tam consume ayrı iş ve operatör kararı gerektiriyor (§5). | ~1 saat |
| **MetricPanel "✎ Edit"** (`MetricPanel.tsx:110`) | `href + '&edit=1'` — `edit` paramını **kimse okumuyor**; Explore mount'ta URL'i yeniden kuruyor (`Explore.tsx:222`), param bir tick içinde siliniyor. "⤢ Explore" ile bit-bit aynı davranış. | Ya param tüketilir ya kalem kaldırılır. Menü şu an yüzeyin yapmadığı bir şeyi vaat ediyor. | ~30 dk (kaldırma) |

---

# §4 Yeni MV / yeni kolon gerektirenler

Bu repo **iki kez** dağıtık-güvensiz kolon ekleyerek prod'u kırdı (v0.8.185 `cluster`,
v0.8.186 `op_group`); kök neden `cluster_name` unset. Desen `internal/chstore/store.go:2118-2135`'te
yazılı ve **kelimesi kelimesine** uygulanmalı: `tableIsExternalDistributed` probe'u → ALTER'ı
**atla + logla** → INSERT kolon listesini probe'a göre dürüst tut → okuma tarafı hata değil
**degrade** etsin.

| # | Ne | Neden gerekli | Kardinalite tahmini | Dağıtık-güvenlik notu |
|---|---|---|---|---|
| **MV-1** | `metric_series` — `AggregatingMergeTree`, `ORDER BY (metric, series_fingerprint)`, taşıdığı: `service_name`, `attr_keys`, `attr_values`, `maxState(time) AS last_seen_state`. `metric_points`'ten MV. | ClickHouse'un **seri indeksi**. M8'in tamamı (filtre-farkında etiket/değer listeleri = Grafana'nın `match[]` semantiği) ve M7'nin gerçek kardinalite ön-kontrolü buna bağlı. Ayrıca M2'nin sınırlarını gereksizleştirir: etiket değerleri ham `DISTINCT` yerine küçük-tablo okuması olur. | Satır sayısı = **seri kardinalitesi**, datapoint hacmi DEĞİL — `metric_catalog`'un adlar için yaptığı sınırlı-durum argümanının aynısı (`store.go:2918-2933`). 1000 servis × birkaç yüz metrik × etiket kombinasyonları: milyon mertebesi üst sınır. **PARTITION BY / TTL yok** (aynı gerekçe: tazelik okuma tarafında `maxMerge(last_seen_state) >= now()-7d` ile). ⚠️ `metric_catalog`'un bugün **hiçbir prune yolu yok** (`internal/` genelinde grep negatif) ve env-suffix'li servis adlarıyla (-int/-uat/-prep) tahmin iyimser; MV-1 için satır-sayısı izleme ve `uniq`/`uniqCombined` (asla `uniqExact`) tercih edilmeli. | **En yüksek riskli kalem.** `series_fingerprint` **kendisi koşullu**: external Distributed + `cluster_name` unset'te ALTER `metric_points_local`'e ulaşamıyor, kolon **bilerek atlanıyor**, `hasSeriesFpCol=false` (`store.go:2107-2108`) ve exemplar pivotları metric+service fallback'ine düşüyor. MV-1 gün-bir: kolonu probe et → yoksa **MV'yi hiç oluşturma** → her okuma yolu ham sınırlı taramaya **degrade etsin, hata dönmesin**. Üç registry'ye kayıt şart: `tablesWithoutTraceID` (`cluster.go:284/294` deseni), `defaultShardPolicy` `cityHash64(service_name)` (`:354/356` — `metric_points` ile co-locate), `highVolumeTables` (`:895/942` — `_local` + Distributed wrapper). `/clickhouse-schema` kapısından geçer. |
| **KOL-1** | `metric_catalog`'a `anyState(is_monotonic)` + `anyState(temporality)` | M1'in **gerçek** Grafana paritesi ("counter'a rate() gelsin") bunsuz imkânsız: frontend monotonic'i UpDownCounter'dan ayıramıyor, `MetricInfo` yalnız `{name,description,unit,type}` (`types.ts:1709`). | Yeni satır yok — mevcut satırlara iki state kolonu. Kardinalite **değişmiyor**. | `temporality` `metric_points`'te **hazır** (`store.go:1268`) ama `is_monotonic` ALTER'ı external Distributed + `cluster_name` unset'te **atlanıyor** (`store.go:2123-2125`) — yani ayırt edici, en çok gereken yerde **hiç olmayabilir**. Ayrıca combined MV'ye kolon eklemek DROP+RECREATE / inner-table-ALTER dansı; forward-only doldurur. **Bu tur değil** (§5). |

Diğer tüm dilimler (M1, M2, M3, M4, M5, M6, M7-v1) **yeni MV / yeni kolon gerektirmiyor.**

---

# §5 Bu turda BİLİNÇLİ dışarıda

| Kalem | Neden bu turda değil |
|---|---|
| **Counter'a `rate()` tohumlama** (M1'in manşet vaadi) | Üç ayrı blokör: (1) ayırt edici veri yok — KOL-1 gerekiyor ve prod'da atlanabiliyor; (2) `MetricCatalogAgg` union'ında `rate`/`increase` **yok** (`model.ts:33-34`), option listeleri `QueryRow.tsx:110` + `PanelEditor.tsx:37`; `agg:'rate'` tohumlamak operatörü değeri listesinde olmayan bir `<select>`'e indirir; (3) unit türetme (`model.ts:109,115`) + `urlCodec` round-trip testleri. "4 string literal" değil, günlük bir dilim. Backend yeteneği zaten var (`metricquery.go:112-114`). |
| **`?m=` → dashboard / alert tam consume** (M10) | Ölü linkleri kapatmak 1 saat (dürüst ret, §3), consume 2-3 gün **ve önce operatör kararı**: descriptor'lar dekoratif — `Overview.tsx:330-331` kodun kendisi *"descriptor only feeds the doorway — it does NOT drive the rendered numbers"* diyor, ve kartların sayıları v0.9.240/253'ten beri **giriş-span kapsamlı** (server+consumer, `Overview.tsx:236-245`). Descriptor'ı pinlemek MV fast-path'in `service.name`-only filtre koşuluna (`spanmetric.go:284-296`) uyduğu için **tüm span'leri** sayan bir tile üretir → DB/HTTP çağrısı yapan her serviste kartın kaç katı. `feedback-entry-span-principle` tuzağı. Ölü link keşfedilebilir, sessizce 3× yanlış dashboard tile'ı değil. Ayrıca `agg:'band'` (`resolverEligibility.ts:79`) `aggToSQL`'de **yok** (`spanmetric.go:1095-1138`) ve `isPinnable` agg'ı hiç kapılamıyor (`pinToDashboard.ts:27-31`) → sert kırık panel. |
| **`metric_series` MV + ad-hoc filtre çubuğu** (M8) | Doğru iş, ama **en riskli** iş (§4 MV-1) ve M6/M7'nin v1'i onsuz gönderilebiliyor. Whitelist'li M7 önce canlıda dursun, gerçek talep ölçülsün, sonra MV. |
| **Range picker'ı katalogda gerçek yapmak** (M3'ün (a) yarısı) | Fallback dalına kullanıcı penceresi vermek en kötü durumda **365 günlük** ham `metric_points` GROUP BY'ı demek (`repo.go:3961-3985`) — v0.8.311 ve v0.8.396'nın **iki ayrı prod olayının** sorgusu, 7 günlük sınır zaten yetersiz çıkmıştı. Üstüne `metric_catalog` bilerek TTL'siz (`store.go:2929-2931`) olduğundan retention'ın ötesini listelemek **ölü tık** üretir, ve `retention.metrics` çalışma-zamanı ayarı (`retention.go:99`) olduğu için "retention tabanına clamp" okuma yolunda canlı spec okumak demek. M5(b) picker'ı dalda gizleyerek illüzyonu bedavaya kaldırıyor. |
| **Katalogda `services` (uniqExact) kolonu** | Sayfanın üzerine hareket edemeyeceği bir soru (M5 riskleri). Descriptor ayrışması hedefse ölçüsü `uniqExact(instrument)` ve `anyState` üzerinde iç içe aggregate yasak → alt sorgu (2× tarama) ya da yeni `uniqState` kolonu. Ayrı dilim, ayrı karar. |
| **Facet sayımlarını sunucuya taşımak** | Prefix taksonomisi frontend'de (`metricGroup`, `Metrics.tsx:36`); sunucuya taşımak vokabüleri ikiye kopyalar. Bu turda dürüst çözüm M5(c): çipe "(ilk 200 içinde)" etiketi. Gerçek çözüm sunucu-taraflı sayım + sayfalama, `hasMore`'un statik ipucundan (`Metrics.tsx:172-176`) fazlası. |
| **"Related metrics" sekmesi** | Grafana'da ad-token örtüşmesi, korelasyon **değil**. Ucuz yarısı bedava (`metric_catalog` zaten bellekte, üstelik `metricTemplates` semconv ailesi Grafana'nın substring'inden daha iyi bir sinyal) ama **korelasyon diye satılmamalı**; gerçek versiyonu (birlikte hareket eden metrikler) ayrı ve pahalı bir ürün. `project-correlation-differentiator` konumu gereği ayrı tutulur. |
| **PromQL ↔ builder köprüsü / explain modu** | İki yarı da gemide (builder: `model.ts:36-57`; PromQL: `internal/api/promql.go:36` + `internal/promql/eval.go:79`), köprü yok. Explain modu üçünün en ucuzu ve tamamen istemci tarafı — ama bu turun keşif temasının dışında. |
| **MetricsExplorer (`?source=metrics`) yetimini emekliye ayırmak** | `Explore.tsx:673` hâlâ mount ediyor ve source kontrolü hâlâ sunuyor (`Explore.tsx:443`), ama **hiçbir sayfa** artık o URL'i kurmuyor. `api.metrics` (ham `metric_points`, `limit:1000`, istemci gruplama) ile `api.metricQuery` (sunucu bucket'lı, histogram_quantile, PromQL rate) arasında **sessiz doğruluk ayrışması** var. Kaldırma kararı ayrı; "backwards-compat shim ekleme" kuralı gereği ya tüketilir ya silinir, arada bırakılmaz. |
| **`/metrics/:name` detay sayfası** | Bir metriğin uygulamada **kendi sayfası yok** (`App.tsx:123` tek rota). Grafana'nın metrik görünümü (Overview/Breakdown/Related sekmeleri) böyle bir sayfa ister; ama M7 aynı sekmeleri **Explore içinde** verebiliyor ve Explore zaten kanonik yüzey (Faz 5 kararı). Yeni rota açmak o kararı geri almak olur — açılırsa ayrı ve açık bir karar olarak açılmalı. |
| **RouteSkeleton düzeltmesi** (`/metrics` `GRID_ROUTES`'ta, `RouteSkeleton.tsx:18`) | Sayfa Faz 5'ten beri tablo (`Metrics.tsx:148-171`), iskelet 8 kart çiziyor → tam da bileşenin önlemek için yazıldığı layout shift. Tek satırlık taşıma (`TABLE_ROUTES`, `:13-17`), kendi sürümünü hak etmiyor; M5 ile birlikte gider. Aynı sınıf: `globals.css:1962-1979`'daki 18 satırlık ölü CSS (`.metric-list`, `.metric-spark*` — tüketicisi sıfır) ve Command Palette'in eski açıklaması ("Time-series explorer", `CommandPalette.tsx:63`). |

---

# §6 Önerilen sıra

| # | Dilim | Efor | Neden bu sırada |
|---|---|---|---|
| 1 | **M2** — `MetricLabelValues` guard'ları + `since` clamp | ~30 dk | Tek CH-sınır ihlali, ve M6/M7'nin ön koşulu. Herhangi bir etiket yüzeyi **üstüne kurulmadan önce** gitmeli. |
| 2 | **M3** — drill filtresinin sessiz düşmesi (P0) | ~2 saat | Düpedüz yanlış grafik + UI'ın var olmayan filtreyi iddia etmesi. M4'ün link yarısından **önce** olmak zorunda. |
| 3 | **M1** — `classifyMetric` tip sözlüğü | ~1 saat | Canlı kataloğun %70'i yanlış agg ile açılıyor. `sum→last`, `exp_histogram→p99`, ölü dallar + tablo testi. |
| 4 | **§3 pivot paketi** — `MetricPanel.tsx:63` range, katalog href, drill href, `dbTracesHref` + `externalTracesHref`, ölü menü kalemlerinin gizlenmesi | ~1 gün | Hepsi aynı iki hata sınıfı (pencere + coalesce attribute), hepsi `pivotHref.ts` deseninde, tek `pivotHref.test.ts` genişlemesi. `serviceRedDescriptors`'a **range EKLEMEDEN**. |
| 5 | **M4** — `normalizeUnit` (b) sonra unit hand-off'u (a) + MRU | ~2 gün | (b) bugün canlı bir bug (`"0.85 1"`, `"8.6G By"` picker'dan seçende zaten görünüyor). Vokabülerin **her dalı** tablo testli. |
| 6 | **M5** — `Last seen` + picker'ın katalog dalında gizlenmesi + facet etiketi (+ RouteSkeleton + ölü CSS) | ~yarım gün | Ucuz dürüstlük katmanı; cache anahtarı şekil-token'ı unutulmayacak. |
| 7 | **M6** — shape preview şeridi | ~1,5 gün | İlk gerçek parite yüzeyi. Tek batch sorgu; fan-out kod incelemesinde özel madde. |
| 8 | **M7** — breakdown ızgarası + sıralama sezgileri (whitelist'li v1) | ~2 gün | Asıl fark. Kardinalite üç tavanla sunucuda zorlanır; ön-kontrol yerine fail-closed whitelist. |
| — | **M8 / MV-1 / KOL-1** | ~3 gün | M7 canlıda ölçüldükten **sonra**, ayrı karar (§4, §5). |

**1-4 arası toplam ~1,5 gün ve tamamı bug** — parite işine başlamadan önce kapıların
sızdırmaz olması gerekiyor, aksi halde M6/M7 aynı sızıntıların üstüne iki yeni yüzey koyar.
