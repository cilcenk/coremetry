# /explore — DT Data Explorer parite brief'i

Kaynak sürüm: **v0.9.266**. Hiçbir ürün dosyası değiştirilmedi.
Karşılaştırma hedefi: klasik **metric-first Data Explorer** (Grail/DQL kuşağı değil —
o kuşağın `makeTimeseries` / `fetch spans | ...` özgürlüğü bizim MV-first invariantımızla
doğrudan çatışır, §4'te ayrıca yazıyor).

Kural: mevcut tablo/panel düzeni, kolon sırası ve satır etkileşimi **aynen kalır**
(v0.9.61 Operations redesign reddi, v0.8.428 kart-feed reddi, v509 drawer revert'i geçerli).
Her yüzey **ek**: yeni durum metni, yeni rozet, yeni overlay, yeni tooltip satırı, yeni URL parametresi.

Ortak ship kuralları (her dilim için geçerli, tekrar etmiyorum):
`lib/types.ts` tip → `lib/api.ts` istemci → `s.serveCached` (anahtar TÜM girdileri FNV ile
hash'ler — v0.5.187) → loading/error/empty → `npx tsc --noEmit` + `go build ./...` +
`go test ./...` → `make audit` → saf yardımcılar için tablo-güdümlü regresyon testi.

---

## 1. Parite boşluğu, tek fikir olarak

**Sorgu gücü değil, sorgunun kendisi hakkındaki dürüstlüğü eksik.**
Analiz katmanı fiilen gemide: A–D çoklu sorgu + formül (`pages/explore/model.ts:36`,
`FormulaRow.tsx:16`), OR/nested filtre grupları (`QueryRow.tsx:123`), 16 span agg'ı
(`presets.ts:28`), rollup tier resolver'ı + exemplar tek çağrıda
(`useExploreQueries.ts:100`), band agg, top-N, log-y, CSV, pin, kaydedilmiş görünüm.
DT'nin bizde olmayan tarafı bir sorgu daha çalıştırmak değil; **sorgunun kendi durumunu
rapor etmesi**: hangi depo cevapladı, istenen interval ne, *efektif* interval ne, son
bucket tamam mı, kaç seri düştü, sorgu ne kadara mal oldu, ve — en basiti — sorgu
başarısız olduğunda bunu söylemesi. Bugün /explore bu soruların hepsine sessiz:
backend 500'ü "veri yok" diye çiziyor (`Explore.tsx:330-335`, `:305-306`), 10 kat
yukarı yuvarlanmış step'i sessizce yutuyor (`clampMetricStep`,
`internal/chstore/metricresolve.go:124-136`), yarım bucket'ı gerçek düşüş gibi
çiziyor, DSL kutusunda her tuş vuruşunda uygulamanın en ağır okumasını fırlatıyor
(`Explore.tsx:584` → `internal/api/api.go:3442`), ve kapıların yarısında operatörün
zaman penceresini düşürüyor. Bu turun tamamı **sorgu öz-dürüstlüğü + link bütünlüğü** —
yeni analiz yeteneği değil.

**ZATEN GEMİDE — önerilmiyor, kodda doğrulandı:**
Explore v2 P1–P5 (giriş kartları `QuestionCards.tsx:61`, geçmiş halkası
`useQueryHistory.ts:44`, A–D builder `model.ts:36`, `?q=` codec `urlCodec.ts:29`,
stat/toplist/pie/table/stacked `SummaryViz.tsx:24`) · doorway resolver D5
(`useExploreQueries.ts:100`, `internal/api/metricresolve.go:57`) · çift-tık-geri
(`lib/chart/zoomHistory.ts:17` + `Explore.tsx:115-127` + `TimeSeriesPanel.tsx:862`) ·
tooltip pin (`lib/chart/tooltipPin.ts:11-19`, `TimeSeriesPanel.tsx:788`) · x-ekseni
drag-select/zoom (`lib/chart/cursorOpts.ts:50` → `Explore.tsx:117`) · region ÇİZİM
motoru (`lib/chart/overlays.ts:39,63,149`, dört preset'e bağlı) · rate/increase
**backend'i** (`internal/chstore/metricquery.go:106-112` → `QueryMetricRate`) ve
Dashboard MQE'sinin agg listesi (`components/viz/MetricQueryEditor.tsx:33,38`) ·
gruplu OR builder, CSV (`exploreCsv.ts:19`), pin (`pinToDashboard.ts:27`),
BubbleUp kutu-seçimi (`Explore.tsx:784`).

---

## 2. Dilimler

### X1 — Advanced-DSL kutusuna debounce (operatör öz-DoS'unu kapatır)

**Ne gösteriyor.** Görsel olarak hiçbir şey. Filtre yazmak artık uygulamanın en ağır
önbelleklenmemiş okumasını karakter başına fırlatmıyor.

**Veri.** Yeni veri yok. `<textarea onChange={e => setDsl(e.target.value)}>`
(`frontend/src/pages/Explore.tsx:584`) doğrudan hem fetch effect'inin dep listesine
(`Explore.tsx:353` — `[resultMode, range, filters, dsl, mode, …]`) hem URL-yazma
effect'ine (`Explore.tsx:245`) besleniyor. Sunucu tarafında önbellek yardım edemez:
anahtar `"traces:" + r.URL.RawQuery` (`internal/api/api.go:3442`), yani her ön-ek ayrı
anahtar, garantili MISS, singleflight işe yaramaz. Ve herhangi bir filtre
`trace_summary_5m` hızlı yolunu diskalifiye ediyor (`internal/chstore/repo.go:1949`,
`len(f.Filters) == 0` şartı) → ham `GROUP BY trace_id`. Handler'ın kendi yorumu bunu
zaten *"the heaviest uncached read"* diye adlandırıyor (`internal/api/api.go:3429`).
Aranan desen aynı dosyada: builder 300ms debounce'lu (`Explore.tsx:103-107`); eski konsol
o desenden önce (v0.8.113) yazıldı ve hiç kapsanmadı.

**API.** Yeni uç yok, backend değişmez.

**Efor.** ~30 dk çekirdek, testle ~1 saat.

**Riskler.**
- **Başlık abartılmasın.** Karakter başına bir tarama DEĞİL: kısmi girdinin çoğu
  `parseDSLLine` regex'inde takılıp 400 dönüyor (`internal/chstore/dsl.go:92,95`;
  `api.go:3411`). `service.name` de neredeyse bedava — `service_name` PK'nın ilk kolonu
  (`store.go:601`). Gerçek maliyet `duration`, `http.status_code` ve attr-array'e düşen
  anahtarlarda. **Ama** kaza sorguları nihai sorgudan DAHA geniş: `parseDurationToMs("5")`
  birimsiz girdiyi aynen döndürüyor (`dsl.go:158-164`), yani `duration > 5` penceredeki
  her span'i eşliyor. Gerekçe "karakter başına sorgu" değil, **"pencerede 24 eşzamanlı
  MaxOpenConns havuzunu (`store.go:215-226,377`, ingest 3×worker rezerve) tam-pencere
  agregasyonlarla doldurup `/api/health` `/api/services` `/api/problems`'in <50ms sıcak
  bütçesini patlatmak"**.
- **İptal yok.** `api.traces` `AbortSignal` almıyor (`lib/api.ts:393`; `request()` sadece
  60s timeout controller kuruyor `:66-72`) ve miss yolu `fn(r.Context())` çağırıyor
  (`internal/api/cache.go:263`). Effect'in `cancelled` bayrağı yalnız SONUCU atıyor;
  soket açık kalıyor, sorgu sonuna kadar koşuyor. Ayrıca her çöp anahtar L1+Redis'e
  20s TTL ile yazılıyor (`cache.go:289-294`) → faydalı girdileri tahliye ediyor.
- **Kapsam iki effect.** Aynı `dsl` `/api/spans/repeats` dalını da sürüyor
  (`Explore.tsx:337-350` → `api.go:4592`, kendi ham-dsl anahtarıyla) VE URL yazımını.
  Sadece fetch'i debounce etmek `navigate(replace:true)` spam'ini bırakır. Çözüm
  state-seviyesinde ayna (`dslDebounced`, `useState(dsl)` ile tohumlanır ki derin link
  hemen fetch etsin — `Explore.tsx:103` ile aynı şekil), İKİ effect'e de bağlanır.
- **Builder modu dokunulmaz** — `FilterBuilder` `onChange`'i yalnız commit'te çağırıyor
  (`FilterBuilder.tsx:100-110`); `useQueryHistory` localStorage yazımını zaten debounce
  ediyor (`useQueryHistory.ts:117-121`). Bu gerçekten sadece DSL textarea'sı.
- **Test kapısı.** Debounce saf fonksiyon değil; mevcut Explore testleri saf-yardımcı
  testleri. Paylaşılan `useDebounced<T>(value, ms)` hook'u çıkarılıp vitest fake-timer
  testi yazılmazsa dilim testsiz gider.
- `switchResultMode` (`Explore.tsx:368-377`) `dsl`+`mode`'u birlikte set ediyor; artık
  300ms sonra fetch edecek — beklenen, ama sürüm notunda yazsın.

---

### X2 — Sonuç alanının dört (aslında altı) kör noktası

**Ne gösteriyor.** Başarısız bir sorgu başarısız olduğunu söylüyor; boş bir sonuç boş
olduğunu söylüyor; yükleniyor olan yükleniyor diyor. Bugün üçü de aynı şey: **bembeyaz alan.**

**Veri.** Yeni veri yok — saf frontend durum yönetimi.
1. **traces/repeats DSL-dışı hata → tamamen boş.** `Explore.tsx:330-335` ve `:345-350`:
   `setTraces(null)` + `setQueryError(msg.includes('DSL') ? msg : null)`. `null`
   `TracesResult.tsx:74` (`undefined` → Spinner), `:75` (`length === 0` → Empty) ve tablo
   dalının hiçbirine düşmüyor → React hiçbir şey çizmiyor. `RepeatsResult.tsx:48-49` aynı.
2. **`queryError`'ın TEK render noktası advanced bloğun içinde** (`Explore.tsx:596`,
   `:581-603` DSL textarea bloğu). Builder modunda mount noktası bile YOK — yani
   DSL-gated mesaj bile orada ulaşılamaz. "Mevcut banner'ı yeniden kullan" mümkün değil;
   sonuç alanına yeni bir tane gerekiyor.
3. **heatmap hatası → "veri yok, aralığı genişlet".** `Explore.tsx:305-306` hem null
   gövdeyi hem reddedilen promise'i `setHeatmap(null)`'a eşliyor; `:755-759` Empty
   çiziyor. Gerçek-boş hâl AYRI dal (`:760`, `maxCount === 0`), yani `null` ağırlıklı
   olarak HATA yolu.
4. **table/stat/toplist/pie → boş.** `GroupTable.tsx:84` ve `SummaryViz.tsx:27` ikisi de
   `if (rows.length === 0) return null;`. `viz='table'`'da GroupTable görünümün KENDİSİ
   (`Explore.tsx:741`) → boş sayfa.
5. **(map'te yoktu) `Explore.tsx:719` ölü kod:** `{panels.length === 0 && anyLoading && <Spinner/>}`
   `anyProduces &&` bloğunun içinde (`:702`), ama `buildPanels` üreten her sorgu için bir
   panel push ediyor (`PanelStack.tsx:96-104`) → `anyProduces ⟹ panels.length ≥ 1`.
   Koşul asla ateşlenemez.
6. **(map'te yoktu) stat/toplist/pie/table'ın hiç loading göstergesi yok.** İlk mount'ta
   `loading:true` panel → `buildGroupRows` atlıyor (`GroupTable.tsx:46`) → 0 satır →
   ikisi de `null`. Yüklenirken de boş, boşken de boş, ayırt edilemiyor.

**API.** Yok. Tamamen istemci.

**Efor.** ~yarım gün. 5 dosya (`Explore.tsx`, `TracesResult.tsx`, `RepeatsResult.tsx`,
`GroupTable.tsx`, `SummaryViz.tsx`), ~80 satır.

**Riskler.**
- **"boş → `<Empty>`" koşulsuz yapılamaz.** GroupTable heatmap DIŞINDAKİ HER viz'de
  çiziliyor (`Explore.tsx:742`), line/area/bars dahil — orada `QueryPanel` zaten kendi
  "Bu pencerede veri yok"unu basıyor (`QueryPanel.tsx:71-78`). Koşulsuz eklersen üst üste
  iki boş durum. Prop ya da çağrı-yeri koşulu gerekiyor (birincil görünüm mü, refakatçi
  tablo mu).
- **Önce `panels.some(p => p.loading)` dalı**, yoksa her soğuk yüklemede "veri yok"
  yanıp sönüyor.
- **"Mevcut şekli ungate et" YANLIŞ.** `Explore.tsx:263` `builderFrom = builderActive &&
  debounced.viz !== 'heatmap' ? exploreRange.from : 0` ve `useExploreQueries.ts:134`
  `enabled: produces(q) && from > 0` — heatmap modunda builder sorguları hiç KOŞMUYOR,
  yani `builderError` yapısal olarak hep `null`. `Explore.tsx:690`'daki render'ı açmak
  hiçbir şey getirmez; heatmap'e `:306`'daki catch'ten beslenen kendi `heatmapError`
  state'i gerekiyor.
- **Hata metni yüzeye çıksın.** `lib/api.ts:79` `HTTP ${status}: ${body}` fırlatıyor,
  `:86` ise *"Request timed out after 60s — try a narrower time range or fewer filters"*
  (`DEFAULT_REQUEST_TIMEOUT_MS`, `api.ts:59`). 1B span/gün'de CH `max_execution_time`
  kaynaklı en olası hata tam olarak bu ve bugün üç çağrı yerinde de yere atılıyor.
  401 zaten global (`api.ts:76` `onUnauthorized?.()`), regresyon riski yok.
- Ship-checklist md.11 için saf `resultState(data, error, loading) → 'loading'|'empty'|'error'|'ok'`
  yardımcısı çıkarılsın; yoksa dilim testsiz gider.

---

### X3 — Provenance: efektif step + DOĞRU tier rozeti

**Ne gösteriyor.** Panel başlığındaki mevcut `"N seri · +M daha · unit"` satırının
(`QueryPanel.tsx:55-60`) sonuna sessiz bir ek: **`10s bucket · spanmetrics_1m`**.
"Bu p99 neden servis sayfasınınkinden farklı?" sorusunun tek-bakış cevabı — DT'nin
*suggested vs effective interval* raporlamasının bizdeki karşılığı.

**Veri.** Zaten telde, zaten tipli, sadece atılıyor.
`MetricResolveResult{Series, Tier, StepSeconds, Exemplars}`
(`internal/chstore/metricresolve.go:49-54`), tümü `s.serveCached` ile JSON'a giriyor
(`internal/api/metricresolve.go:77-89`), FE'de tipli (`frontend/src/lib/types.ts:1867-1871`).
Düşürüldüğü yer tek satır: `useExploreQueries.ts:103`
`.then(r => ({ series: r?.series ?? [], exemplars: r?.exemplars ?? [] }))`.
`frontend/src` içinde `tier` render eden hiçbir şey yok.

**Efektif step yarısı bedava ve en değerli parça:** `stepForWidth` 1/2/5 basamakları
üretiyor (`lib/chartStep.ts:16-37`), `clampMetricStep` 10s'e tabanlıyor
(`metricresolve.go:113,124-136`). Yani **~2 saatin altındaki HER Explore penceresi 1–5s
istiyor, sessizce 10s alıyor.** İstemci bunu türetemez — `clampMetricStep` Go-only.

**API.** Dar dilim için backend değişmez. Geniş versiyon için bkz. riskler.

**Efor.** Dar dilim (span-kaynaklı resolver panelleri: tier + efektif step, ~30 LOC +
rozet) ~yarım gün — **ama `Tier:"spans"` yalanı önce düzeltilmek zorunda**, o da
`QuerySpanMetric` imza değişikliği (~yarım gün daha).

**Riskler.**
- **KATİL: tier string'i yalan söylüyor.** `metricresolve.go:352` ve `:523` fallback
  dalında `Tier: "spans"` sabitliyor. Ama fallback `QuerySpanMetric` çağırıyor, o da
  içeride `tryServiceMVFastPath` / `tryOperationMVFastPath` ile `service_summary_5m` /
  `operation_summary_5m`'e route ediyor (`internal/chstore/spanmetric.go:155-162`).
  Yani gerçekte `service_summary_5m`'den gelen panel **"spans"** rozeti alıyor.
  Struct'ın kendi yorumu `1s|10s|1m|operation_summary_5m|spans` diyor
  (`metricresolve.go:51`) ama `operation_summary_5m` hiçbir kod yolundan çıkmıyor.
  Motive eden sorunun (`"neden servis sayfasından farklı"`) gerçek cevabı genellikle
  *"ikisi de aynı 5m tdigest'ini farklı bucket genişliğinde okuyor"* — rozet bunun
  TERSİNİ iddia ediyor. **Deponun yanlış raporlandığı bir provenance rozeti, hiç
  rozet olmamasından kötüdür.** Düzeltme sınırlı: `QuerySpanMetric`'in yalnız 4 dış
  çağıranı var (`red_series.go:32`, `metricresolve.go:348`, `:517`, `spanmetric.go:123`).
- **Kapsam deliği: 3 fetch yolundan 2'si iki alanı da taşımıyor.**
  `api.spanMetricTopN` → `SpanMetricResult{series, totalSeries?}` (`types.ts:1818-1821`);
  `api.metricQuery` → **çıplak dizi** (`lib/api.ts:1595-1596`, handler
  `internal/api/api.go:4059-4071`). Zarf'a çevirmek 7 çağrı yerini kırıyor
  (`features/dependencies/panels/shared.tsx`, `components/viz/MetricQueryEditor.tsx`,
  `components/dashboard/PanelRenderer.tsx`, `pages/explore/model.ts`,
  `useExploreQueries.ts`, `pages/explore/QueryRow.tsx`, `pages/service/RuntimeCharts.tsx`)
  + aynı şekli paylaşan PromQL kardeşi (`api.ts:1604`). **Bu turda YAPILMASIN** — X8 ile
  ortak, ayrı karar.
- **İlginç sapma tam da görünmeyen yolda.** `QueryMetric` step'i `clampStepToExport` ile
  sessizce yükseltiyor (`metricquery.go:145`) ve `QueryMetricRate` /
  `QueryMetricHistogramPercentile`'a çatallanıyor, her birinin kendi clamp'i var.
  DT'nin "suggested vs effective" ayrımının tam yeri burası ve hiç telde değil.
- **Uygunluk dar:** `exemplarDescriptor` (`model.ts:263-286`) DSL'i, OR grubunu, `=`
  dışı op'u ve `TIER_DIM_KEYS` dışı anahtarı reddediyor (`lib/resolverEligibility.ts:19-37`).
  Tek `http.status_code` filtresi → fallback → yanlış etiketli "spans".
- **Küçük çırpınma:** `selectMetricTier` `time.Now()` + 60s TTL'li
  `spanmetricsCoverageStart` kullanıyor (`smCovTTL`, `metricresolve.go:65`) — TTL
  ufkunda rozet operatör hiçbir şey yapmadan `1m` ↔ `spans` arası atlayabilir.
- **Düzen kuralı:** iki revert'in ışığında yeni bir başlık SATIRI değil, mevcut desc
  satırına soluk sonek + `title` hover'ı. Bu türetilmiş görüntü durumu — `?q=`'ya
  **koyulmaz**.
- **Maliyet sıfır:** yeni sorgu yok, yeni MV yok, resolver yolunda ek round-trip yok.

---

### X4 — Tamamlanmamış bucket saat gibi okunsun, kesinti gibi değil

**Ne gösteriyor.** Serinin sağ ucundaki yarım bucket soluk bir bantla işaretli; düşüş
"outage" değil "henüz dolmadı" diye okunuyor. Sol uç aynı muameleyi görüyor.

**Veri.** Yeni sorgu yok. **Çizim motoru zaten gemide** (Grafana-parite M3):
`ChartTimeRegion` + `drawTimeRegions` (`lib/chart/overlays.ts:39,149`), dört preset'e
bağlı (`TimeSeriesPanel.tsx:18,105,463`; `MultiLineChart.tsx:22,199`;
`charts/TimeChart.tsx:18,64`; `service/charts/OverviewChart.tsx:20,58`), `clampRegion`
canlı zoom ölçeğine kırpıyor (`overlays.ts:63`), `chartBuildSig.ts:53-54` bölgeleri
**değere göre** digest'liyor (`regionsDigest`) → sabit-değerli dizi chart rebuild'i
tetiklemiyor. Audit'in "hiçbir bileşende yok" notu (`docs/audit/chart-consolidation-audit.md`
§7) ÖZELLİĞİ kastediyor, tesisatı değil.

**API.** Yok — doğru uygulamada.

**Efor.** ~yarım gün (`PanelData`'ya alan + `buildPanels`'da hesap + `QueryPanel`'de
iletim + step türetme testi). Üç dosya.

**Riskler.**
- **`effStep` YANLIŞ girdi — üç sunucu clamp'i onu eziyor.** `useExploreQueries.ts:84`
  step'i *istek* olarak gönderiyor: resolver `clampMetricStep` uyguluyor
  (`metricresolve.go:333`, taban 10s / tavan 720 nokta), metric yolu
  `clampStepToExport` (`metricquery.go:145`), eski span yolu
  `service_summary_5m`'i yeniden bucket'lıyor ama alttaki slot 5m
  (`spanmetric.go:303`). Somut: 5dk pencere / 1200px → `stepForWidth` **1** döndürüyor
  (`lib/chartStep.ts:31-38`), sunucu **10** ile bucket'lıyor → `effStep`'ten çizilen
  bant **10 kat dar**. Tam `feedback-unit-mixing-needs-both-branches` sınıfı: göz kararı
  bakacağın dalda (geniş pencere, kaba step) doğru, clamp'lenen her dalda sessizce yanlış.
- **Doğru çözüm: step'i DÖNEN bucket zaman damgalarından türet**
  (`points[n-1].time − points[n-2].time`). Saf, üç yolda da doğru, "istemci-only, saat
  mertebesi" iddiasını korur. Sunucudan step echo'lamak iki handler + iki yanıt tipi
  demek — artık istemci-only değil (X3'ün geniş versiyonuyla aynı zarf işi).
- **SOL kenar da yarım.** `lib/utils.ts:22-24` `from`'u step sınırına snap'lemiyor ve her
  okuma `toStartOfInterval` ile **taban**a yuvarlıyor (`metricquery.go:93`,
  `metricresolve.go:387`, `spanmetric.go:236`), `WHERE time >= from` ile birlikte ilk
  bucket ortalama yarım step veri tutuyor. Yalnız sağ kenarı gönderirsen ilk bucket aynı
  sebeple düşmeye devam eder.
- **Ingest gecikmesi.** OTel batch + `async_insert` flush gerçek tamamlanmamış bölgeyi
  `[now − (step + ingestLag), now]`'a itiyor. 10s step + 10s collector batch'te saf
  step-genişliği bant gözle görünür sahte düşüş bırakır.
- **`drawTimeRegions` varsayılanı `var(--err)`** ve üstte "başlık çubuğu" + `▮ label`
  şeridi çiziyor (`overlays.ts:160,165-180`). Aynen yeniden kullanılırsa yarım bucket
  **P1 problem penceresi** gibi görünür — amaçlananın tam tersi. Nötr renk + şeritsiz
  varyant gerekiyor → `overlays.ts`'e küçük bir ekleme, sıfır değişiklik değil.
- **Pitch'in yarısı kurgu:** "period-over-period delta'lar" /explore'da YOK. `compare`
  D5'in bilinçli düşürdüğü legacy param (`urlCodec.ts:249`; test
  `urlCodec.test.ts:230-231` *"D5 — legacy compare param decodes the rest and drops
  compare"*; `Explore.tsx:677,682` `compare={false}` sabit). Yalnız görsel-dürüstlük
  yarısı gerçek.
- **Fiyatlanmamış kapsam:** aynı yalan heatmap'te (`Explore.tsx:299-303`, `buckets: 80` —
  son kolon eksik sayılıyor), `GroupTable`'da ve `SummaryViz`'de de var; hiçbiri `regions`
  almıyor. Bu tur chart-only, notta yazsın.

---

### X5 — `rate` / `increase` Explore'un agg listesine (iki string)

**Ne gösteriyor.** Katalog metriğinde counter seçince PromQL-benzeri reset-korumalı
rate çizilebiliyor. Bugün Dashboard MQE'sinde var, Explore'da yok — aynı backend.

**Veri.** Backend tamamen hazır: `agg` doğrulanmamış passthrough
(`internal/api/api.go:4052`), `metricquery.go:106-112` `rate`/`increase` →
`QueryMetricRate`, `:113-121` histogram percentile'ı. Kardeş bileşen zaten listeliyor:
`components/viz/MetricQueryEditor.tsx:33,38` `['avg','sum','min','max','last','p50','p95','p99','rate','increase']`.
Explore'unki `pages/explore/model.ts:33-34` — son iki eleman eksik. Zaten elle yazılmış
URL ile ulaşılabiliyor: `urlCodec.ts:152` `agg`'ı sıfır doğrulamayla geçiriyor.

**API.** Yeni uç yok, backend değişmez.

**Efor.** ~1 saat (iki string + `MetricCatalogAgg` union + tsc).

**Riskler.**
- **Ölçek tuzağı — bu dilim onu her operatörün önüne koyuyor.** `queryRateCumulative`
  (`internal/chstore/metricrate.go:225-240`) `(bucket, sk, gk)` ile grupluyor; `sk`
  **seri başına**, grup başına değil. Satır = bucket × seri. Explore'un 6sa varsayılanı →
  `metricAutoStep` 60 (`metricresolve.go:150-151`) → 360 bucket → **~138 seriden sonra
  `LIMIT 50000` `ORDER BY sk, bucket` kuyruğundan TAM SERİLER düşürmeye başlıyor.**
  Düşenler Go tarafındaki seri-üstü toplama içine giriyor (`metricrate.go:270-285`) →
  sonuç **sessizce eksik sayılmış bir rate, tamamen normal görünen bir grafik**.
  Bu yolda kesinti bayrağı YOK (`buildRateSeries`, `metricrate.go:362-381`; span
  yolundaki `totalSeries`'in karşılığı yok). Explore'un varsayılan kapsamı boş
  (`model.ts:87-93`, `scope: ''`) → kapsamsız 1000-servis sorgusu operatörün YAPTIĞI
  İLK ŞEY.
  → **Ön koşul: X8'in kesinti bayrağı.** Ya X8 önce gider, ya X5 sürüm notunda
  "kapsamsız rate'te seri kesintisi mümkün" uyarısıyla ve `scope` boşken uyarı rozetiyle
  gider.
- **Prod'da daha ağır:** dış Distributed CH'de `cluster_name` boşsa `series_fingerprint`
  ALTER'ı atlanıyor (`store.go:2091-2107`, `hasSeriesFpCol=false`) → anahtar satır başına
  `cityHash64(concat(...arraySort(arrayMap(...))))` sentetiğine düşüyor, ham tarama
  üstünde. Ders kitabı `feedback-distributed-column-safety` şekli.
- **Birim yanlış çıkar.** `model.ts:184` metric kaynağı için `q.unit`'i aynen döndürüyor.
  bytes counter'ı rate olarak çizince y ekseni hâlâ `By` diyor, `By/s` değil. Span
  kaynağının `spanAggUnit`'i (`model.ts:114-121`) var, metric kaynağının karşılığı yok.
- **Varsayılanı DEĞİŞTİRME.** `urlCodec.ts:42` `a`'yı `q.agg === 'avg'` **literal**
  karşılaştırmasıyla atlıyor ve `BuilderQuery`'de `type` alanı yok (`model.ts:41-53`),
  yani encode enstrüman-varsayılanını bilemez. Literal karşılaştırma korunur; "düzeltmek"
  URL round-trip'ini sessizce kırar. Mevcut decode edilmiş agg'lar **asla** yeniden
  yazılmaz, yoksa eski kaydedilmiş görünümler sayı değiştirir.
- **Enstrüman-farkındalıklı varsayılan bu dilimde DEĞİL** — bkz. §4 (ayrı `/bugfix`).

---

### X6 — Problem bantları (`regions`) Explore panellerine

**Ne gösteriyor.** "Bu ani yükseliş zaten bilinen bir problem miydi?" sorusu, operatörün
zaten baktığı grafiğin üstünde. Service ve ProblemDetail'de var olan tek overlay'in
Explore'da eksiği.

**Veri.** `TimeSeriesPanel` `regions`'ı alıyor ve çiziyor (`TimeSeriesPanel.tsx:105`,
`:463` → `overlays.ts:149`); `QueryPanel` hiç geçirmiyor (`QueryPanel.tsx:80-98`),
`PanelData`'da alan yok (`PanelStack.tsx:19-32`). Overlay taşıyıcısı hazır:
`ExploreOverlay` zaten harf başına deploys/events/thresholds fanlıyor
(`useExploreQueries.ts:227-231`) — 4. alan ~5 dosya. Rebuild güvenliği çözülmüş:
`regions` build imzasında değere göre digest'leniyor (`chartBuildSig.ts:41-53`,
tüketim `TimeSeriesPanel.tsx:306`). Tarif `ServiceCharts.tsx:318-327`'de.

**API.** Ucuz sürüm için yok. **Vaat edilen değer için VAR:** `ProblemFilter`'a `From`/`To`
+ SQL overlap yüklemi.

**Efor.** Ucuz sürüm (yalnız açık problemler, pinlenmiş servis) ~yarım gün.
Vaat edilen sürüm (geçmiş pencere + hafif yanıt + URL toggle) **1–2 gün, backend dahil**.

**Riskler.**
- **`status=open` sorulan soruyu CEVAPLAYAMAZ.** Soru retrospektif; open-only + `toSec = to`
  yalnız "şu anda açık bir problem var" der. Geçmiş bir olayı bantlamak için
  **[from,to] ile ÖRTÜŞEN** problemler lazım — `ProblemFilter`'da From/To **yok**
  (`internal/chstore/problem.go:881-911`, doğrulandı) ve `ListProblems` zaman yüklemi
  olmadan `ORDER BY started_at DESC LIMIT 100` (`:1023-1032`). `status=resolved` "en yeni
  100 çözülmüş satır" demek → yoğun serviste pencerenin problemlerini sessizce kaçırır;
  tam olarak v0.5.398'in sidebar rozeti için kapattığı kesinti sınıfı
  (`frontend/src/lib/api.ts:1727-1731`).
- **Kapsam kapısı en çok işe yarayacağı yeri öldürüyor.** Bantlar yalnız `pinnedService(q)`
  ile binebilir (`model.ts:234-239`). `blankQuery()` `scope: ''` (`model.ts:88-93`) ve
  her `splitBy: ['service.name']` "top servisler by p99" sorgusu pinsiz — yani bandın en
  çok istendiği panel tam da hiçbir şey almayan panel. Kapıyı kaldırmak 1000s servisten
  100 açık problemi tek panele fanlamak demek; `drawTimeRegions`'ın üst sınırı yok ve
  her redraw'da bölge başına `measureText` yapıyor.
- **`/api/problems` overlay ucu DEĞİL, ağır triage ucu.** `listProblems` `problems FINAL`
  koşuyor (tablo `ORDER BY id`, `store.go:942-944` — zaman-sıralı anahtar yok), ardından
  dört okuma-zamanı zenginleştirme round-trip'i (runbook/team/cluster/deploy,
  `internal/api/api.go:9464-9490`). Kendi yorumu **p95 903ms / max 3s** kaydediyor
  (`api.go:9452`). Explore bunlardan harf başına 4 tanesini asacak
  (`useExploreQueries.ts:254-283` şekli), `refetchInterval: 30_000` poll'la
  (`lib/queries/problems.ts:41-42`), ve zenginleştirmenin %100'ünü kırmızı bant çizmek
  için atacak. /service bunu BEDAVA alıyor (`Service.tsx:249` bundle'ında zaten var) —
  port'un ucuz görünmesinin tek sebebi bu asimetri.
- **Mayın:** `useExploreOverlays` elle yazılmış `sig` string'i üzerinden memo'luyor,
  `eslint-disable exhaustive-deps` ile (`useExploreQueries.ts:285-307`). Problems
  sorgusunu ekleyip `sig`'i genişletmeyi unutmak = kalıcı bayat bant, hiçbir test yakalamaz.
- **Kapatma anahtarı yok.** Geçen salıdan beri açık bir problem her paneli %100 kırmızıya
  boyar. /service'te dürüst ("şimdi"ye çapalı), Explore'un keyfi pencerelerinde gürültü.
  Toggle şart ve sert kurala göre URL'e `replace:true` ile yazılmalı.

---

### X7 — "Bu bucket'ın arkasındaki kayıtlar" — pinli tooltip satırı olarak

**Ne gösteriyor.** Herhangi bir sorgu için — exemplar ◆'i HİÇ olmayanlar dahil (OR
grupları, tier-dışı filtreler) — bir bucket'ın arkasındaki trace'lere geçiş.

**Veri.** `GET /api/traces` `filterGroup`'u kabul ediyor (`lib/api.ts:2321-2324`,
parse `internal/api/api.go:3421`), yeni MV yok. Exemplar boşluğu iddia edilenden BÜYÜK:
`exemplarDescriptor` (`model.ts:263-286`) DSL, OR grubu, `=` dışı op, `TIER_DIM_KEYS`
dışı anahtar, tier-dışı agg (p999/min/max/last) ve `duration_ms` dışı ölçülen alan için
null dönüyor → gerçek Explore sorgularının çoğunda ◆ yok. `/api/spans/exemplar` yalnız
`service`/`op`/`kind` alıyor (`api.go:5026-5032`) — Explore sorgusuna hizmet edemez.

**API.** Yeni uç yok; mevcut `/api/traces` + mevcut modal genelleştirilir.

**Efor.** **1.5–2 gün** (yarım gün DEĞİL). Ama önce **1 dosyalık hata düzeltmesi** var,
bkz. riskler.

**Riskler.**
- **ÖNCE ŞUNU GÖNDER (ayrı, ucuz):** 3-tık eşdeğeri zaten var — drag-zoom →
  "Bu pencereyi getir →" (`Explore.tsx:386-397`) → Traces sekmesi. `switchResultMode`
  (`Explore.tsx:365-377`) A sorgusunun `effectiveFilters` + `dsl`'ini taşıyor ama
  **yalnız düz filtreleri**; gerçek bir OR `filterGroup` sessizce düşüyor
  (`model.ts:44-53`: fetch'te filterGroup düz filtreleri eziyor, `effectiveFilters` geri
  uyumluluk yolu). Yani BUGÜN Traces sekmesi, X7'nin hedeflediği OR-grup sorgularında
  panelden **daha geniş** bir küme gösteriyor. Tek dosya, ~1 saat.
- **Düz tıklama TimeSeriesPanel'de zaten SAHİPLİ.** `TimeSeriesPanel.tsx:765-798`: önce
  exemplar ◆ isabeti, sonra `decidePinClick` → tooltip pin. Preset başına jest sözleşmesi
  belgelenmiş ve teste sabitlenmiş (`lib/chart/tooltipPin.ts:11-19` + `tooltipPin.test.ts`).
  MLC aynı çakışmayı Alt+click'e çevirerek çözmüş (`MultiLineChart.tsx:594-605`). Yani bu
  "eksik prop eklemek" değil, günlük bir yüzeyde **gönderilmiş bir jesti yeniden
  atamak** + test edilmiş sözleşmeyi düzenlemek + `TSPBuildSigInput`
  (`chartBuildSig.ts:239-265`, `hasBucketClick` yok) ve `chartBuildSig.test.ts`.
- **ÖNERİ: jesti hiç alma — aksiyonu PİNLİ TOOLTIP'in içine koy.** Pinli tooltip zaten
  kalıcı, `pointerEvents:auto` bir DOM düğümü (`tooltipPin.ts:79-83`), imleç indeksini VE
  seri başına satırları biliyor. Satır başına "Bu bucket'taki trace'ler →" linki tamamen
  ek, hiçbir jesti yeniden atamıyor ve seri-atıf boşluğunu bedavaya çözüyor.
- **MLC'nin bucket-genişliği kodu kopyalanamaz.** TSP uPlot'a LTTB ile seyreltilmiş birleşik
  ızgara veriyor (`TimeSeriesPanel.tsx:236-252`, `MAX_POINTS=2000`); MLC bucket'ı
  `u.data[0]` komşu farklarından türetiyor → TSP'de o dizi seyreltilmiş, türetilen pencere
  yanlış. Doğrusu `effStep`'i aşağı geçirmek (`useExploreQueries.ts:84`) — X4'ün step
  türetmesiyle ortak.
- **Seri atfı yok.** `onBucketClick(fromNs, toNs)` seri indeksi taşımıyor; splitBy paneli
  `TOP_N_MAX = 50` seri çizebiliyor ve `PanelData` `BuilderQuery`'yi bile taşımıyor
  (`PanelStack.tsx:21-32`). label→groupKey→FilterExpr tersine çevirmek mümkün
  (`model.ts:210-226`) ama fiyatlanmamış tasarım.
- **Agg başına semantik tanımsız.** Heatmap hücresi çalışıyor çünkü **latency bandı**
  taşıyor (`HeatmapCellExemplars.tsx:62-76`, `minMs`/`maxMs`). Çizgi bucket'ında yalnız
  zaman var. `error_rate` sivrisi `hasError=true` ister; p95 sivrisi bant ister;
  `sort=duration desc` en uzun **trace**'leri döndürür, agregayı oynatan yavaş
  **span**'leri değil.
- **Ölçek.** Herhangi bir filtre MV hızlı yolunu diskalifiye ediyor (`repo.go:1944-1955`)
  → ham `GROUP BY trace_id`; handler'ın kendi yorumu *"the heaviest uncached read"*
  (`api.go:3432-3434`). Bucket genişliği operatörün aralığıyla ölçekleniyor:
  `stepForWidth` 6sa'te 60s, **7g'de 1800s** (`lib/chartStep.ts:29-38`). Servis-pinsiz
  30 dakikalık bir bucket 1B span/gün'de ~20M span demek, `max_execution_time=60` +
  512 MiB spill (`repo.go:1871`) altında. Her panelin her bucket'ına tek-tık tetikleyici
  koyuyorsun → pencere clamp'i ya da servis-pin kapısı ŞART.
- **Devralınan doğruluk hatası:** `repo.go:1760-1764` pencere >5dk olduğunda `from`'u
  **5 dakikalık sınıra aşağı yuvarlıyor**. Step ≥5dk'da (≳2 gün aralık) modal, tıklanan
  bucket'tan 5 dakika ÖNCEKİ trace'leri "bu bucket'ın arkasındaki kayıtlar" diye listeler.
  Heatmap buna hiç çarpmıyor (hücre ≤ birkaç dakika).
- Kod tabanı bu hamleyi zaten sınıflandırmış: `docs/audit/chart-consolidation-audit.md:47`
  bucket-click'i MLC-only / TSP "yok" diye kaydediyor ve §7 (`:345-369`) açıkça
  "bir preset'in özelliği sessizce yayılmaz; her biri ayrı onaylı kalem" diyor.

---

### X8 — Kesinti dürüstlüğü: metric yolunda `totalSeries` + rate kesinti bayrağı

**Ne gösteriyor.** "+N daha" doğru sayıyı söylüyor; kesilmiş bir rate sessizce normal
görünmüyor. DT'nin *"top 20 of 1,412 series"* etiketinin karşılığı — ve DT'nin
kaybettiği yerde (o kuyruğu sessizce atıyor) bizim strictly-better olabileceğimiz nokta.

**Veri.** Span yolu doğru yapıyor: sunucu tarafı top-50-by-area trim + trim ÖNCESİ
`totalSeries` (`internal/chstore/spanmetric.go:14,122`). Metric yolunda yok:
`Store.QueryMetric` yalnız SATIR üstünde `LIMIT 50000` (`metricquery.go:96`), seri sayısı
sınırsız; `useExploreQueries.ts:126-134` `totalSeries`'i `undefined` bırakıyor
(`:213`), `PanelStack.tsx:162` `total`'ı `ranked.length`'e düşürüyor — yani **"+N daha"
yalnız GELEN'i sayabiliyor ve gelen, 50000-satır kesiminde seri düşürmek yerine seri
BAŞINA noktaları sessizce kırpılmış olan her şey.**
Rate yolunda daha kötü: `queryRateCumulative` seri-başına anahtarla grupluyor
(`metricrate.go:225-240`), düşenler Go tarafı seri-üstü toplamanın İÇİNE giriyor
(`:270-285`), `buildRateSeries`'te (`:362-381`) hiçbir bayrak yok.

**API.** `GET /api/metrics/query` yanıtını **çıplak diziden zarfa** çevirmek gerekiyor
(`lib/api.ts:1595-1596`, handler `internal/api/api.go:4059-4071`) → 7 çağrı yeri +
PromQL kardeşi (`api.ts:1604`). X3'ün geniş versiyonuyla **aynı göç** — birlikte gitmeli.

**Efor.** ~1 gün (zarf göçü + iki alan + `PanelStack` etiketi + testler).

**Riskler.**
- **Rolling deploy:** eski FE yeni zarfı dizi sanıp `.map` patlatır. Zarfı `series` alanıyla
  ekle ve FE'de `Array.isArray(r) ? r : r.series` şeklinde tolere et — backend ÖNCE, FE sonra.
- Dashboard `PanelRenderer` ve `MetricQueryEditor` aynı yanıtı tüketiyor; tsc kapısı
  hepsini yakalar ama regresyon testi dashboard tarafında da olmalı.
- Etiket dürüstlüğü: "+N daha" ile "N seri kesildi" AYRI şeyler. İlki trim (kasıtlı,
  alan sıralı), ikincisi LIMIT kesintisi (kaza). İkisi tek sayıya karıştırılırsa yeni
  bir yalan üretilmiş olur.

---

## 3. İÇERİ pivotlar — hangisi ölü, hangisi kayıplı, tam düzeltme

12 canlı üretici, 5 URL lehçesi. Doğru olanlar §3.7'de — **onlara dokunulmayacak.**

### P1 — `/databases` DB satırı → Explore linki **ÖLÜ** (canlı CH'de kanıtlı)

v0.9.256'da operatörün bildirdiği messaging hatasının aynısı, aynı fonksiyonun `db` dalında
düzeltilmemiş hâli.

`db_summary_5m` `instance`'ı altı adımlı coalesce ile üretiyor: `peer_service` →
`server.address` → `net.peer.name` → `db.host` → `db.name` → `service_name` → `'unknown'`
(`internal/chstore/store.go:2494-2502`; ikizi `db_caller_summary_5m` `:2528-2536`).
Link YALNIZ `peer.service` filtreliyor (`frontend/src/components/DependenciesTable.tsx:241-247`):
```ts
const dsl = `db.system = "${r.system}"\n` +
  (r.instance && r.instance !== 'unknown' ? `peer.service = "${r.instance}"` : '');
return `/explore?dsl=${encodeURIComponent(dsl)}&mode=advanced&result=traces`;
```
`peer.service` tipli `peer_service` kolonuna eşleniyor (`internal/chstore/filterexpr.go:48`),
o kolon boş → link garantili boş liste. Canlı doğrulama (chc-0, 2sa): `db_system='clickhouse'`
→ **8985 span**, aynı pencerede `peer_service='coremetry-monolithic'` → **0**.

**Bu tek hata değil, ÜÇ kusur:** (a) yanlış attribute; (b) **`range=` yok** → hedef kendi
30dk'sına düşüyor; (c) **`db.name` düşürülmüş** — satır kimliği `(db_system, instance, db_name)`
(`store.go:2479`), yani `postgres/host-A/orders` ile `postgres/host-A/billing` **bayt-bayt
aynı** linki üretiyor.

**Düzeltme.** `lib/pivotHref.ts`'e `databaseTracesHref` — `messagingTracesHref`
(`pivotHref.ts:88,117-141`) deseninin birebiri: MV'nin kabul ettiği her adı OR'la,
pencereyi **zorunlu argüman** yap, ve v0.9.257 emsaline uyarak **`/traces`'e** hedefle
(operatör orada yüklemleri çip olarak GÖRÜP düzenleyebiliyor; `/explore` DSL'i opak).
⚠️ `/explore` linkiyle OR mümkün DEĞİL zaten: `ParseDSL` sadece-AND
(`internal/chstore/dsl.go:29-56`, `splitDSLAnd` yalnız `" AND "` böler) ve `Explore.tsx:153`
yalnız düz `filters` okuyor (`grep -c filterGroup pages/Explore.tsx` = **0**). Yani bu
dilim "satır tıklaması artık Explore açmıyor"u İÇERİYOR — emsal var, ama açıkça söylensin.

**Mayınlar.**
- Coalesce'ın 6. bacağı `service_name` (`store.go:2500`). `service.name = <instance>`'ı
  koşulsuz OR'lamak o çağıran servisin TÜM db span'lerini, tüm host'larda eşler.
  `db_name != 'default'` iken **AND `db.name`** eklenmeli ve `service.name` bacağı
  koşullanmalı.
- `encodeFilterGroup` düz-AND grubu için `''` döndürüyor (`pivotHref.ts:130-136`'da
  belgelenmiş tuzak) — replike edilmezse `'unknown'` satır **filtresiz** trace listesine iner.
- **Backend ikizi, kimse bahsetmemiş:** `internal/chstore/dependencies.go:229-233`
  `instancePredicate := "peer_service = ?"` ve bunu `:347-348`'deki ham Top-statements
  taramasına AND'liyor, ama üstündeki agregat + caller okumaları MV'nin coalesce'lı
  `instance`'ını kullanıyor (`:270`, `:294`). Aynı ölü filtre: drawer aynı satır için
  caller'ları ve P99'u gösterip **Top statements'ı boş** gösteriyor. İkisi düzeltilmezse
  yüzeyin yarısı düzelir.
- **Lokalde REPRO EDİLEMEZ.** `cmd/demo` db span'lerine hep `peer.service` yazıyor
  (`cmd/demo/bank_extra.go:233` ve devamı) → 1. bacak hep kazanıyor → link çalışıyor.
  Hata ajan verisinde yaşıyor: `java-demo`'da tek `peer.service` referansı elle bir attr
  (`CoreBankingGateway.java:74`); JDBC autoinstrumentation `server.address`/`net.peer.name`
  yazıyor. Ayrıca "fallback" çipi yalnız `instance === 'unknown'` iken çiziliyor
  (`DependenciesTable.tsx:449`) — yani `server.address`'ten çözülen satırlar **sağlıklı
  görünüp** sıfır döndürüyor. Ölülük ORANI ölçülmedi; commit gövdesi oran iddia etmesin.
- **Ölçek temiz:** yeni MV yok, yeni index yok. OR grubu `db_system = ?` ile AND'leniyor,
  o da `set(0)` skip index taşıyor (`store.go:1509-1515`); traces listesi zaman-sınırlı +
  `LIMIT 5000` + `max_execution_time = 20` (`internal/chstore/repo.go:846-849`). Ek maliyet
  yaşayan satır başına 4 `indexOf` dizi taraması — gönderilmiş messaging linkiyle aynı
  profil. `FilterGroup` derinlik tavanı 1 (`filterexpr.go:380-397`); AND-kök + tek-OR-çocuk
  şekli tam oturuyor.

**Efor:** ~yarım gün (helper + çağrı yeri + `pivotHref.test.ts` yanına test +
`dependencies.go` düzeltmesi).

---

### P2 — `/metrics` kataloğu → Explore: pencere **%100 deterministik** kayıp

Maskeleyecek hiçbir yol yok, çünkü /metrics range'ini ne URL'e ne localStorage'a yazıyor:
`Metrics.tsx:60-61` `useState(() => decodeRange(searchParams.get('range') ?? storedRangeString(), …))`,
`setRange` çıplak setter, `openMetric` (`Metrics.tsx:106`)
`navigate(metricCatalogueHref(m.name, { agg: classifyMetric(m)?.agg }))`.
Aynı dosya doğrusunu biliyor: legacy-bookmark yönlendirmesi üç satır yukarıda range'i
ekliyor (`Metrics.tsx:68-72`). Kök sebep `metricCatalogueHref`'in **hiç pencere
parametresi olmaması** (`pages/explore/urlCodec.ts:62-73`) — kardeşi `servicePivotHref`
`fromNs`/`toNs`'i zorunlu argüman alıyor ve ters pencereyi guard'lıyor (`urlCodec.ts:87-107`).

**Düzeltme:** `metricCatalogueHref(name, opts, window)` — pencere **zorunlu** argüman, ki
sonraki üretici unutamasın. İki çağrı yeri: `Metrics.tsx:106` ve
`features/dependencies/panels/shared.tsx:226`. İkincisi ayrıca **`drill.filters`'ı da
düşürüyor**, oysa modalin kendi grafiği o filtrelerle sorguluyor (`shared.tsx:186-193`) →
"Open in Explore" operatörün baktığından FARKLI, harmanlanmış bir seri gösteriyor
(ör. tek tablespace yerine hepsi).

---

### P3 — `?m=` MetricPanel kapısı: pencere **iki kez** atılıyor, `&edit=1` no-op

`MetricQuery` `range?: TimeRange` taşıyor (`lib/metricQuery.ts:33`) ve
`pages/service/Overview.tsx:333-338` bunu 7 panel descriptor'ının hepsine dolduruyor
(paneller `:346,349,352,355,369,380,386`). İki uçta da atılıyor:
`metricExploreHref` yalnız `/explore?m=<base64>` döndürüyor (`metricQuery.ts:122-124`),
`seedFromMetricDescriptor` `mq.range`'e hiç dokunmuyor (`urlCodec.ts:203-227`), Explore
`searchParams.get('range') ?? storedRangeString()`'e düşüyor (`Explore.tsx:88`).

**localStorage bunu maskeleyemez:** `persistableRange` `custom:` pencereleri bilinçli
persist etmiyor (`lib/useUrlRange.ts:51-53`, v0.8.409 operatör hatası). Ve /service'te
drag-zoom geçici chart state'i DEĞİL — `Service.tsx:75-82` `handleZoom`
`setRange({ preset: 'custom', … })` çağırıyor. Yani: 90 saniyelik bir olaya zoom yap →
⋮ → Explore → **son 30 dakika**, olay ekran dışında. "Copy link" de aynı delikte.

**Düzeltme — önerilen yol:** `seedFromMetricDescriptor`'a dokunma; `BuilderState`'in
range slotu YOK (`queries/formula/viz/step/topN/logY`), yani "codec `mq.range` okusun"
yapısal olarak imkânsız. `MetricPanel`'e `range?: TimeRange` prop'u:
`href = metricExploreHref(mq) + (range ? '&range=' + encodeRange(range) : '')`,
`mq.range`'e fallback. **Descriptor'a range EKLEME:** aynı objeler fetch descriptor'ı
(`ServiceCharts.tsx:202-205` → `api.resolveMetric`) ve
`internal/api/metricresolve.go:73-74` önbellek anahtarını ham base64 `m` blob'undan
kuruyor, from/to'yu bilinçli dakika-bucket'lıyor ("polling penceresinde sıcak girdi
paylaşmak için"). Milisaniye `fromMs/toMs`'i `m`'e enjekte etmek o paylaşımı bozar ve
`lib/resolverEligibility.test.ts:79-80`'deki descriptor-eşitlik assert'lerini kırar.

**10 kapının 3'ü ayrıca eksik:** `components/ServiceCharts.tsx:503,520,539`
`serviceRedDescriptors`'tan (`lib/resolverEligibility.ts:76-84`) kuruluyor, orada range
hiç yok. Yarım düzeltme = KPI tile'larından pencere korunur, bir sekme ötedeki RED
grafiklerinden sessizce kaybolur — tutarsız, tekdüze bozuktan kötü. Prop yolu onuna
birden çözüm.

**`&edit=1`** (`components/MetricPanel.tsx:110`): Explore hiçbir yerde `edit` okumuyor
(yalnız `Dashboard.tsx:35`, `Dashboards.tsx:49/217`). Bozuk değil, **ölü** — ve Explore
zaten builder olduğu için ⋮ Edit ile ⋮ Explore bugün aynı yere iniyor, operatör bir şey
kaybetmiyor. Menü değişikliğini pencere düzeltmesine paketleme; ya ölü string'i sil ya
hiç dokunma.

**Kırılacak test:** `lib/metricQuery.test.ts:96` `href.split('?m=')[1]`'i
`decodeMetricQuery`'ye veriyor — `&range=` eklenince null döner, güncellenmeli.
`urlCodec.test.ts:190-205` param'ları doğrudan kurduğu için etkilenmiyor.

---

### P4 — Explore'un KENDİ giriş kartları, o ekranda seçilen range'i atıyor

Paramsız `/explore` giriş ekranı çalışan bir range picker'la geliyor (`Explore.tsx:414-418`)
ve URL yazıcısı range'i ekliyor (`:220`), `hasMeaningfulParams` `range`'i yok sayıyor
(`:887-892`) → sayfa 'entry' anahtarında kalıyor. Kart tıklaması
`navigate('/explore' + c.to)` yapıyor (`QuestionCards.tsx:115`) — mutlak yol + search,
**tüm query string'i, range dahil, değiştiriyor**. URL artık "meaningful" → `ExplorePage`
anahtarı 'entry'→'workspace' çevirip `ExploreInner`'ı **remount** ediyor (`Explore.tsx:919`)
→ `:88` initializer'ı range'siz URL'e karşı yeniden koşuyor. Aynı bileşenin hemen altındaki
"Son sorgular" tam search string'ini replay ettiği için (`QuestionCards.tsx:148`) kayıpsız —
yani aynı ekranda iki farklı davranış.

**Düzeltme:** `QuestionCards`'a `range` geçir (bugün yalnız `{ history }` alıyor,
`:100`), `c.to`'ya `&range=` ekle.

---

### P5 — Kalan iki `?dsl=` linki

`DependenciesTable.tsx:247` (P1) ve `pages/External.tsx:201`
(`peer.service = "<host>"`, range yok). İkisi de v0.9.256/257 messaging düzeltmesinin
göç etmemiş kardeşleri. External'ın filtre DEĞERİ semantik olarak doğru
(`topology_edges_5m` node'u `concat('ext:', peer_service)` kuruyor), ama
**latent risk:** `internal/chstore/topology.go:661-668` (v0.8.448) `infra_host`'tan
kurulan İKİNCİ bir `ext:` dalı ekledi — `peer.service` yazmayan semconv-only SDK'lar için
o host'ların External drill'i P1'in aynısı gibi ölü olur. Şu anki demo verisinde repro
yok; link range için ellenirken guard'lansın. Emsale uyup `tracesPivotHref`'e yönlendir.

### P6 — Kök sebep: iki yetim sayfa range'i persist etmiyor

`lib/useUrlRange.ts:32-40` global sticky pencereyi zaten uyguluyor ve **24 dosya hook'u
benimsiyor. Tam iki tanesi range'i persist edilmeyen yerel `useState`'te tutuyor — ve
onlar şikâyetin kaynağı ile hedefi:** `pages/Metrics.tsx:60` ve `pages/Explore.tsx:88`.
İkisi de `storedRangeString()`'i OKUYOR, hiç geri YAZMIYOR. Sonuçlar: /metrics'te F5
pencereyi kaybediyor; Explore → başka sayfa kaybediyor; giriş ekranı Topbar seçimi
downstream'e görünmez (P4'ün maskelenmemesinin sebebi). `useUrlRange.ts:68-74` yetimliği
"bespoke URL-range pipeline'ı olan sayfalar (Explore, Metrics)" diye kasıtlı belgeliyor —
Explore için doğru (kendi yazıcısı `:192-245` `setSearchParams` ile kavga eder), ama
**Metrics'in bespoke yazıcısı hiç yok**, sadece persist etmiyor. Dışa açık yazıcı da yok
(`writeStoredRange` modül-private) → tek satırlık düzeltme bile yeni bir export istiyor.

**Öneri (P1'den sonra ilk):** önce bu ikisini persist et (pick'te `RANGE_STORE_KEY`'e
yaz, `useUrlRange.ts`'ten writer export et). Göreli preset'ler için şikâyet sınıfını iki
yönde birden, maliyetin küçük bir kısmıyla öldürür. **Sonra** href'leri mutlak-pencere
(`custom:`) vakası için yamala.

**Ölçek notu:** "veri kaynağı yok" çerçevesi bir maliyeti gizliyor — 30dk yerine 24sa
taşımak her kapı tıklamasında Explore'un fan-out'unu daha geniş pencerede koşturur.
"Neden yavaş?" kartı bir heatmap (`QuestionCards.tsx:66`) ve bütçe `/api/spans/heatmap`
< 3s @ ≤6sa. Doğru davranış, ama bedava değil.

### P7 — DOĞRU olan linkler: dokunulmayacak

`/services` sparkline drill'i (`pages/Services.tsx:419` — `['range', encodeRange(range)]`) ·
operasyon popup drill'i (`pages/service/OperationsTable.tsx:540`, aynı satır) ·
korelasyon drawer'ı (`CorrelationContextDrawer.tsx:402-404` → `servicePivotHref`,
ters pencere guard'lı, testler `urlCodec.test.ts:274-313`) · "Son sorgular" halkası ve
`<SavedViewsBar page="explore">` (`SavedViewsBar.tsx:80-85`, tam search string'i replay) ·
`Metrics.tsx:68-72` legacy-bookmark yönlendirmesi.

---

## 4. Bu turda BİLİNÇLİ OLARAK YOK

| Kalem | Neden bu turda değil |
|---|---|
| **`fallbackForType` counter'ları `avg`'a düşürüyor** — X5'in adını aldığı hata | `metricTemplates.ts:187-195` `'counter'`/`'updowncounter'` eşliyor, ama ingest yalnız `gauge`/`sum`/`histogram`/`exp_histogram`/`summary` yazıyor (`internal/otlp/convert.go:288,295,314,341,363`; katalog `repo.go:3909-3911`, MV DDL `store.go:2943`). Yani iki dal **ölü**, gerçek counter (`sum`) `default: {agg:'avg'}`'a düşüyor — monoton değerlerin anlamsız ortalaması. Üstelik `MetricTemplate['agg']` union'ında `'rate'` **yok** (`metricTemplates.ts:26`), yani "classifyMetric zaten doğru varsayılanı türetiyor" iddiası tam da bu vakada yanlış. v0.5.487'den beri latent; tek diğer tüketicisi `pages/Metrics.tsx:106` olduğu için **gönderilmiş bir yüzeyde davranış değişikliği** → kendi `/bugfix`'i, kendi sürümü. X5'in ön koşulu değil. |
| DQL benzeri metin sorgu dili / `makeTimeseries` eşdeğeri | Tek gerçek mimari boşluk, ama MV-first invariantıyla doğrudan çatışıyor ("ham spans'ten agregat = bug"). Uygulanabilir olması için açık bir tarama bütçesi (sampling ratio + scan cap + zaman-sınırlı WHERE) ve o maliyetin operatöre gösterilmesi gerekiyor — DT'nin `samplingRatio:`/`scanLimitGBytes:` modeli. Kendi brief'ini hak ediyor. |
| `fieldsSummary` tarzı facet kenar çubuğu | Listedeki en ucuz yüksek-değerli DT kopyası ("dimension adını bilmek zorundasın" → keşif). Ama YENİ bir yüzey (yeni panel), seçili alanlar üstünde tam tarama, ve sampling+extrapolation gerektiriyor. Ayrı dilim. `api.attributeKeys/attributeValues` (200k satır örneklemli, `api.go:3018,3069`) yarı-altyapıyı zaten veriyor. |
| Takvim hizalı karşılaştırma (`@h`/`@d`, `shift:-7d`) | `compare` D5'in **bilinçli düşürdüğü** param (`urlCodec.ts:249`, test `urlCodec.test.ts:230-231`, `Explore.tsx:677,682` `compare={false}` sabit). Geri getirmek D5 kararını iptal etmek demek — operatör kararı, teknik dilim değil. |
| Davis benzeri forecast bandı | Yeni analitik; refusal davranışı (min 20 nokta, CV>%5, training ≥ 2× horizon) doğru kopyalanmazsa güvenilmez bant üretir. Ayrı iş. |
| Honeycomb viz'i | 9 viz'lik set zaten var; honeycomb yalnız yüzlerce entity'de değer katıyor ve yeni bir renderer. |
| Top-N kuyruğu için `other` bucket'ı | DT kuyruğu sessizce ATIYOR — burada strictly better olabiliriz. Ama `trimTopNByArea` sunucu tarafında (`spanmetric.go:14`) ve `other` toplamı ek bir agregasyon gerektiriyor. X8'in dürüst etiketi bunun ucuz yarısı; `other` serisi ayrı dilim. |
| Pre- vs post-aggregation filtre yerleşimi | DT'nin en sessiz-yanlış-sayı önleyici özelliği. Bizde `filter` her zaman pre-aggregation; post-aggregation kavramı UI'da yok. Gerçek bir boşluk ama yeni sorgu semantiği + yeni UI + eğitim maliyeti. |
| `source=metrics` / `source=logs` panellerinin yarım tesisatı | `legacyViz` salt-okunur (`Explore.tsx:93`), tek viz kontrolü (`VizRail`) `source==='spans'` fragmanının içinde → o modda viz değiştirilemiyor. Ama **grep hiçbir üretici bulmuyor** — bu URL'leri kuran hiçbir sayfa kalmamış (`MetricsExplorer.tsx:39` artık var olmayan bir ServiceInfra derin linkine atıf yapıyor). Ölü decode dalı; düzeltmek değil, **emeklilik** kararı gerektiriyor. |
| Browser Back builder state'ini yeniden tohumlamıyor | URL→state mount başına BİR KEZ okunuyor (`Explore.tsx:74-96`), tek yeniden-tohumlama yolu entry↔workspace remount anahtarı (`:919`). Klasik tek-yönlü-okuma sınıfı (v0.8.253/256/265/267), ama her yazım `replace:true` olduğu için büyük ölçüde maskeli (az history girdisi birikiyor). Gerçek düzeltme sig-guard'lı URL→state import'u — orta büyüklükte refactor, bu turun teması değil. |
| `/api/problems`'a `From`/`To` + overlap yüklemi | X6'nın vaat edilen değerinin ön koşulu. Backend işi (`problem.go:881-911` + `:1023-1032`) ve `problems` tablosunun `ORDER BY id` olması (`store.go:942-944`) yüzünden zaman yüklemi PK'dan faydalanamıyor. X6'nın ucuz sürümü gönderilirse bu ayrı kalemdir. |

---

## 5. Önerilen sıra

| # | Dilim | Efor | Neden bu sırada |
|---|---|---|---|
| 1 | **P1** — /databases ölü linki (+ `dependencies.go` ikizi) | ~yarım gün | Operatörün bir kez bildirdiği hatanın aynısı, bugün provably sıfır satır döndürüyor. Bug > perf > feature. |
| 2 | **X1** — DSL debounce | ~1 saat | En yüksek oran. CH okuma havuzunu koruyan tek dosyalık değişiklik. |
| 3 | **P6 + P2** — iki yetim sayfanın range persist'i + `metricCatalogueHref` zorunlu pencere | ~yarım gün | Şikâyet sınıfını iki yönde birden öldürüyor; P3/P4'ün maliyetini düşürüyor. |
| 4 | **X2** — sonuç alanının kör noktaları | ~yarım gün | Backend hatası "veri yok" diye raporlanıyor; doğruluk hatası, kozmetik değil. |
| 5 | **X5** — rate/increase (iki string) | ~1 saat | Kardeş bileşenle parite, sıfır risk — **ama** kapsamsız rate uyarısı ya X8 sonrası. |
| 6 | **P3 + P4 + P5** — kalan pencere kayıpları | ~1 gün | Prop yolu (descriptor'a değil), 10 kapı birden; `metricQuery.test.ts:96` güncellenir. |
| 7 | **X8** — kesinti dürüstlüğü (`/api/metrics/query` zarfı) | ~1 gün | X5'in sessiz-eksik-rate riskini kapatıyor; X3'ün geniş versiyonuyla ortak göç. |
| 8 | **X3** — provenance rozeti | ~1 gün | `Tier:"spans"` yalanı ÖNCE düzeltilir; yanlış depo raporlayan rozet net-negatif. |
| 9 | **X4** — tamamlanmamış bucket | ~yarım gün | Step bucket farklarından türetilir, `effStep`'ten DEĞİL; nötr renk + şeritsiz varyant. |
| 10 | **X7-öncü** — `switchResultMode`'un `filterGroup` düşürmesi | ~1 saat | Tek dosya; OR-grup sorgularında Traces sekmesi bugün panelden geniş küme gösteriyor. |
| 11 | **X6** — problem bantları (ucuz sürüm) | ~yarım gün | Modest parite; vaat edilen değer için `ProblemFilter{From,To}` gerekiyor (§4). |
| 12 | **X7** — bucket → kayıtlar (pinli tooltip satırı) | 1.5–2 gün | En büyük dilim; jesti çalmadan, pencere clamp'i / servis-pin kapısıyla. |
