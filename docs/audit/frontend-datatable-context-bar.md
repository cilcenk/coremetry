# Audit — Ortak DataTable bileşeni + Global bağlam çubuğu (ContextBar)

**Tarih:** 2026-09-02 · **Durum:** ONAY BEKLİYOR (8 soru, sonda) · **Kapsam:** frontend/src (988 ts/tsx, 379 test) ·
**İstek:** design-system çalışmasının ilk teslimatı — (A) ortak DataTable, (B) URL'de tutulan
zaman aralığı + cluster/namespace/service bağlamı. İlk dilim Traces.

Ölçüm tabanı: 109 `useDataTable` çağrı yeri / 103 farklı `storageKey`; 23 ham `<table>`;
3 boş-durum, 4 yükleniyor-durumu uygulaması; `compare=` için 3 uyumsuz kodlama; `cols=`
için 3 ayrı codec. Tüm dosya:satır atıfları 2026-09-02'de doğrulandı.

---

## 0. Özet — beş karar

| # | Karar | Gerekçe (kısa) |
|---|---|---|
| K1 | **Tablo kütüphanesi eklenmez; `@tanstack/react-virtual` (zaten kurulu) + mevcut saf çekirdek `lib/dataTable.ts` korunur**, üstüne `components/ui/DataTable/` sarmalayıcısı yazılır | 109 tablonun sıralama/genişlik kalıcılığı + URL `?s_<key>` sözleşmesi bu çekirdekte; `@tanstack/react-table` bu sözleşmeleri yeniden yazdırır ve 103 localStorage anahtarını göç ettirir — kazanç sütun görünürlük/sıra modeli, o da ~150 satırlık saf ek (§9). Panel kararı §9'da tartışıldı. |
| K2 | **Zaman penceresi kanalı `?range=` olarak TEK kalır** (`1h` göreli / `custom:<fromMs>-<toMs>` mutlak); istenen `?from=&to=` (`now-1h`, epoch ms, epoch ns) **girişte kabul edilir ve ilk yazımda `range=`'e çevrilir**, hiçbir sayfa `from/to` YAZMAZ | İkinci pencere kanalı "adres çubuğu emin, sayfa başka pencere çiziyor" sınıfıdır (dört kez gemiye girdi, `lib/pivotHref.ts:4-16`); `range` 12 hook-dışı okuyucu + 6 yazıcı + kayıtlı görünümler (ham query string) + `navHref` taşıyor; ayrıca **`from=` /pod'da başka bir parametre** (`Pod.tsx:87` drill kaynağı). Grafana/runbook derin linkleri yine çalışır (§8) |
| K3 | **`?cluster=&namespace=&service=&env=&compare=` tek sözleşme, `useContextParams` tek okuyucu/yazıcı**; `compare` normalize (`prior` / yok); **`ns` DEĞİL `namespace`** (dört sayfanın mevcut yazımı; `ns` /clusters çekmece parametresi `Clusters.tsx:177`), `ns`/`compare=1` yalnız giriş takma adı | Bugün `cluster` 12 yerde ham `searchParams.get`, `ns`/`namespace` aynı sayfada iki ad, `compare` üç kodlama. İstenen `ns` kısaltması bir derin linki kırar; `namespace` kırmaz |
| K4 | **Sütun tercihi `saved_views(page='table-prefs')` blobunda, kullanıcı başına** (invariant #5, ai-chat emsali); yeni tablo YOK; `GET/PUT /api/preferences/tables/{key}` `preferences_routes.go`'da | Bugün `traces-extra-cols` / `dt.logs.columns` tarayıcı-yerel, cihazlar arası kaybolur; endpoints yalnız URL |
| K5 | **Sıra: Traces → Logs → Services/Endpoints → geri kalan**; her sayfa kendi sürümü; ContextBar Traces ile birlikte AppShell'e girer ama yalnız sahip sayfalarda aktif | Traces en zengin tablo sözleşmesine sahip (sunucu sıralama, extras, kolon yöneticisi, VirtualTable) — orada çalışan API her yerde çalışır |

---

## A. Tablo envanteri

### 1. Tüm tablo / liste görünümleri

Ortak ilkel: `useDataTable` (`components/DataTable.tsx:70`, saf çekirdek `lib/dataTable.ts`).
Sıralama varsayılan **istemci** (`computeSortedRows`, `lib/dataTable.ts:217`); `serverSort:true`
satırları aynen döner (`:222`). Sıralama durumu URL `?s_<storageKey>` (`DataTable.tsx:112`) +
localStorage `dt.<key>.sort` (`lib/storage.ts:74`); öncelik URL → `urlSortFallback` → LS →
`initialSort` (`DataTable.tsx:255-263`). Genişlikler yalnız LS, `dt.<key>.widths`, imza-mühürlü
(`lib/dataTable.ts:260/283`). **Sütun gizle/sırala ilkelde YOK** (`mobileHide` var, tüketicisi 0 —
`lib/dataTable.ts:87`).

Kısaltmalar: Sıra `C`=istemci `S`=sunucu `N`=yok · Sayfa `srv`=offset/cursor sunucu, `LM`=daha-fazla-yükle · Sütun = seç/gizle UI · Sanal = sanallaştırma.

| Rota | Bileşen | storageKey | Kaynak | Satır beklentisi | Sıra | Sayfa | Sütun | Sanal |
|---|---|---|---|---|---|---|---|---|
| /traces | `pages/Traces.tsx` liste | `traces-list` :900 | `api.traces` :462 | **50/sayfa** :463 | S (`SERVER_SORTABLE` :920-931) | srv offset `<Pager mode="offset" count="skip">` :1349 | ColumnManager + çipler :1262-1277, **8 tavan** :1263 | `<VirtualTable>` :1311, rowHeight 36, `height=44+n·36` :1313 → 50 satırda pencereleme YOK (iç kaydırma reddi, v0.10.225) |
| /traces (aggregate) | aynı dosya | `traces-agg` :743 | `api.tracesAggregate` :654 | 200 :655 | S :746 | yok | yok | yok (ham tbody :1528) |
| /traces (shapes) | `components/traces/ShapesView.tsx` | `trace-shapes` :106 | shapes API | 1000 :41 | C | yok | yok | content-visibility :146 |
| /services | `pages/Services.tsx` | `services` :279 | `api.servicesPage` | 50 :113 | **S** :280 | srv offset, `page` URL :126; iki `<Pager>` :930/:935 | yok | yok (`rowClickHandlers` :813) |
| /endpoints | `pages/Endpoints.tsx` | `endpoints` :394 | `useEndpoints` (30 s poll) | `?limit=` basamak [100…10000], varsayılan 100 :216-218 | **S** :397 | yok (limit seçici :542) | **`<ColumnToggle>`** URL `?cols=` set codec (`pages/endpoints/endpointCols.ts:22/40`) | yok |
| /endpoint | `pages/endpoints/detailSections.tsx` | `endpoint-failing-traces` :243, `endpoint-split` :363, `endpoint-callers` :582 | sunucu dilimi | küçük | C | yok | yok | yok |
| /logs | `components/LogTable.tsx` | `logs` :294 | fetch 100 :408, birikim tavanı **2000** (`Logs.tsx:79`) | — | **N** (keyset yön) | cursor + LM `<Pager mode="cursor">` :1307 | dinamik; LS `dt.logs.columns` :209 + URL `?cols=` :221 | content-visibility :405 |
| /inbox | `pages/Inbox.tsx` | `inbox` :469 | `useInbox({limit:300})` :358 | 300 | S :473 | yok ("capped" rozeti :885) | yok | **bilinçli VirtualTable değil** :895 (değişken yükseklik) |
| /problems | `features/anomalies/AnomaliesPage.tsx` | `exception-inbox` :227 | — | 50 :172 | S :230 | srv `count="exact"` :686 | yok | yok |
| /problems (kurallar) | `ProblemsSection.tsx` | `alert-rules` :333 | 30 s poll :299 | — | C | yok | yok | yok |
| /anomalies | `features/anomalies/streams.tsx` | `anomaly-history-active` :340, `-cleared` :378 | — | — | C | yok | yok | content-visibility :482/:486 |
| /incidents | `pages/Incidents.tsx` | `incidents` :58 | `useIncidents({limit:200})` :45 | 200 | C | yok | yok | VirtualTable reddi :129 |
| /metrics | `pages/Metrics.tsx` | `metric-catalog` :195 | `api.metricNamesSearch` :169 | büyüyen limit :181 | C | LM `<Pager mode="cursor" count="exact">` :447 | yok | yok |
| /databases ailesi | `Databases.tsx:122`, `DependenciesTable.tsx:289`, `databases/detailSections.tsx:295/400`, `SlowQueries.tsx:146` (200 :100), `stmtDetailSections.tsx:206`, `DetailDrawer.tsx:128/593`, `PostgresPanel.tsx:48`, `panels/shared.tsx:306` | `databases-top-statements`, `deps-*`, `database-detail-*`, `slowqueries`, `dbstmt-callers`, `deps-topops-*`, `deps-callers-*`, `deps-postgres-databases`, `deps-topsql` | sunucu | ≤500 | C | yok | yok | content-visibility (DependenciesTable :479, DetailDrawer :641) |
| /messaging | `pages/Messaging.tsx:212` → DependenciesTable | `deps-<kind>` | — | — | C | yok | yok | — |
| /service | `pages/service/*` (11 tablo: `svc-ov-ops` :61, `svc-ov-db` :134, `svc-ov-endpoints` :68, `service-operations` :254, `service-clusters` :68, `service-pods-tab` :69, `service-entity-pods` :88, `svc-attrs-resource/span` :97-98, `svc-anomaly-window` :45) + LogTable | — | — | C | yok | yok | attrs content-visibility :135 |
| /hosts, /external, /clusters, /pod | `hosts` :73, `host-drawer-services` :175; `external` :92; `clusterpods/namespaces/deployments/nodes` :518-536; `pod-siblings` :82, `pod-services` :45, `pod-traces` :74 (**el-yapımı LM** `PodTracesTable.tsx:122`) | — | — | C | LM (pod-traces) | yok | content-visibility (external :134) |
| /explore | `GroupTable.tsx:277`, `RepeatsResult.tsx:50`, `TracesResult.tsx:86` (**ColumnManager** :141, 8 tavan :143), `ExploreViz.tsx:185` (TOP_N 50) | `explore-*` | — | — | C | yok | evet (TracesResult) | yok |
| /profiling, /profile, /trace/compare, /rollouts, /events, /shift, /alerts, /watchers, /slos, /runbooks, /dashboards, /users, /ai, /settings/* | `profiles` :102, `hotspots` :228, `method-hotspots` :55, `trace-compare-diff` :338, `rollouts-live` :99 (**S**, SSE), `notiflog` :149, `events` :309, `shift-*` :97-99, `alert-rules-list` :262, `watchers` :109, `slos` :57, `runbooks` :57, `runbook-executions/audit` :244/:337, `dashboards` :177, `users` :75, `ai-calls` :64, `settings-*` (8) | — | — | C | yok | yok | content-visibility (profiling :175, events :197/:349, compare :399) |
| /system/* | 13 CH tablosu (`ch-*`), stats (3), elastic (3), cardinality (3), k8s (2), catalog/cluster/audit, **`adminsql-result`** (`AdminSql.tsx:429`: tek `<VirtualList>` tüketicisi :544, CSS grid, **el-yapımı sıralama glifi** :518, 10k tavan :481) | — | — | C | yok | yok | VirtualList (yalnız burada) |

**`useDataTable` DIŞINDA kalan ham `<table>` (sıralama/yeniden boyutlandırma yok — 23):**
`ExternalPaths.tsx`, `HeatmapCellExemplars.tsx`, `RolloutDrawer.tsx` (×3), `RootCausePanel.tsx` (×2),
`SpanDetail.tsx` (×3), `ai/ServiceChartsExplainBody.tsx`, `chart/StatsLegend.tsx`, `viz/TimeSeriesPanel.tsx`,
`ExceptionPodsPanel.tsx` (50 pod tavanı :102), `ProblemDetail.tsx` (×2), `EntityDetail.tsx` (×3),
`alerts/NoisyRulesPanel.tsx`, `settings/InfluxTab.tsx`, `LdapTab.tsx` (×3), `LdapUserPicker.tsx`,
`SpanClusterValuesPanel.tsx`, `TeamRoutingTab.tsx`, `ZoomChannelPicker.tsx`. Satır akışı (tablo değil):
`TraceWaterfall.tsx:686`, `LogFieldsPanel.tsx:256`.

### 2. Tekrarlanan pattern'ler ve tutarsızlıklar

| Alan | Uygulama | Tanım | Sayı |
|---|---|---|---|
| **Boş durum** | `<Empty icon title children action compact>` | `components/Spinner.tsx:112`, CSS `globals.css:1957` | 254 |
| | `<QueryError>` / `<SectionUnavailable>` (hata ≠ boş) | `components/QueryError.tsx`, `ui/SectionUnavailable.tsx` | 40 |
| | el-yapımı "No …" div | `ui/VirtualTable.tsx:81` ('No rows.'), `databases/detailSections.tsx:225` | ~22 |
| | `'—'` yer tutucu hücre | 285 literal + 95 JSX | ~380 |
| **Yükleniyor** | `<Spinner label hint>` | `Spinner.tsx:10` | 228 |
| | `Skeleton/TableSkeleton/CardSkeleton/ListSkeleton` | `Skeleton.tsx:12/34/77/99` | 56 |
| | `<RouteSkeleton>` (rota-farkında Suspense) | `ui/RouteSkeleton.tsx:20`, `App.tsx:140` | 1 |
| | satır içi Türkçe "yükleniyor…" | `PodTracesTable.tsx:122`, `detailSections.tsx:225`, `PodInlineResourceCharts.tsx:75` | 23 |
| | bayat tabloyu soldur (`opacity 0.55`) | `Traces.tsx:1255`, `Inbox.tsx:900` | 2 |
| **Sıralama ikonu** | `DataTableHead` `▼/▲/↕` + `aria-sort` | `DataTable.tsx:476/440`, CSS :1252-1255 | kanonik |
| | el-yapımı aynı üçlü | `AdminSql.tsx:518` (CSS grid gövde) | 1 |
| **Satır = link** | `<td class="row-cell"><Link class="row-link">` | `Traces.tsx:1325`, `AnomaliesPage.tsx:570-638`, `ProblemsSection.tsx:631-753`; CSS :1293-1300 | 14 |
| | `rowClickHandlers(href, navigate)` (`window.open` ile orta-tık taklidi, DOM'da `<a>` yok) | `lib/utils.ts:346`; `Services.tsx:813`, `explore/TracesResult.tsx:149` | 2 |
| | çıplak `<tr onClick>` (orta tık yok) | `Traces.tsx:1528`, `Incidents.tsx:140`, `LogTable.tsx:405` … | 11 |
| **Klavye** | `useDataTable({onOpen})` j/k/Enter/o | `DataTable.tsx:212` | 109'da ~15 |
| **Yoğunluk** | `data-density` 4 seviye, LS `coremetry-density` | `DensityToggle.tsx:16-52`; CSS :2589-2620 | tutarlı |
| **content-visibility** | 18 site + `.wf-row` CSS :1441; eşik tutarsız: koşulsuz (`MethodHotspots.tsx:139`, `Events.tsx:197`) vs `>100` (`DBQueriesPanel.tsx:179`, `Profiling.tsx:175`) | | 18 |
| **Tri-state** | `undefined`=yükleniyor / `null`=hata / `[]`=boş | `lib/readState.ts:25` (12 yüzey `null`'ı boş sanıyordu, :10-19) | kural var, ilkel zorlamıyor |

Sonuç: ilkel sıralama+genişlik verir; **boş/yükleniyor/hata/satır-link/klavye/sütun-yönetimi her
sayfada yeniden yazılıyor.** DataTable bunları prop olarak alıp tek yerde çizmeli.

### 3. Traces tablosu özelinde

- **Sabit kolonlar** `FIXED_COLS = [time, operation, service, duration, status, spans]` (`lib/traceColumns.ts:24`);
  etiketler `Traces.tsx:110-113` (operation→Name, time→Start time).
- **Varsayılan attribute kolonları** `DEFAULT_TRACE_COLUMNS = [openshift.cluster.name, channel_code, function_code, function_id]`
  (`lib/traceColumns.ts:65-70`; sıra 2026-08-24 operatör kararı, test pinli). Sıra fonksiyonu
  `traceColumnOrder` :93 → `time · operation · service · <extras> · duration · status · spans`; başlık ve
  gövde aynı `colIds` :1320.
- **Ekleme/çıkarma:** `<ColumnManager>` :1262 (`components/ColumnManager.tsx:18`), çip ile çıkarma :1270-1277,
  "+ K8s columns" toplu :1266. **8 tavanı üç yerde** (`ColumnManager.tsx:70`, `Traces.tsx:263/316`) + Explore
  kopyası (`TracesResult.tsx:143`). Anahtar kataloğu `api.attributeKeys(…, 500)` panel açılınca :34, 50 satır
  render tavanı :66 + sabit semconv listesi :37-47.
- **Kalıcılık:** localStorage `traces-extra-cols` (`Traces.tsx:149/313/324`, `lib/storage.ts` kaydı DIŞINDA);
  URL `?cols=` mount'ta :310, her state yazımında :417; öncelik URL → LS → varsayılan :304-309.
- **Hangi state sorguyu tetikler** (effect bağımlılık dizileri):

| Sorgu | Effect | Bağımlılıklar |
|---|---|---|
| Liste `api.traces` | :433-500 | `[view, listRangeNs, sort, order, page, filter, env, clusterScope, advFilters, advGroupParam, showTotal, retryNonce]` :500 — **`extraCols` bilinçli dışarıda** :482-485 |
| Extras `api.tracesExtras` | :510-543 | `[view, data, extraCols]` :543 — yalnız `missingExtraKeys` doluysa :515; `mergeTraceExtras` `''` damgası yakınsama garantisi (`lib/traceExtrasMerge.ts:26`) |
| Hacim şeridi | :556-620 | `[view, listRangeNs, filter.service, filter.search, env, clusterScope, advFilters, grouped]` |
| Aggregate | :643-679 | 13 bağımlılık :679 |
| Sayım (opt-in) | :764-788 | :787 |
| State→URL | :362-423 | 18 bağımlılık, `rebuildPreserving` :370 |
| dt.sort → sunucu sort | :920-931 | `[dt.sort.id, dt.sort.dir]`, `page` sıfırlanır :928 |

  Liste ve aggregate `AbortController` + `cancelled` bayrağı (:452/:650). Pencere `alignTraceWindow`
  tek `useMemo([range])` :429-432 (liste + şerit + x-ekseni).
- **Sayfalama:** offset `limit 50, offset page·50` :463; `count:'skip'` MV hızlı yol :490; toplam ayrı istek
  :769 ("Show total" :1366); son sayfa yalnız sayım kesin+ulaşılabilirken (`lib/traceReach.ts:16/28`,
  6000 tavanı Go ile pinli).
- **VirtualTable:** :1311-1334; konteyner tam içerik yüksekliği → **pencereleme fiilen yok** (bilinçli:
  iç kaydırma reddi). overscan 12 (`ui/VirtualTable.tsx:50`), `contain:'strict'` :75, spacer-satır :84/:100.

### 4. Kütüphaneler ve öneri

| Paket | Sürüm | Durum |
|---|---|---|
| react / react-dom | 18.3.1 (pinli) | |
| react-router-dom | ^6.30.4 | BrowserRouter `main.tsx:168` |
| @tanstack/react-query | ^5.100.14 | tek client `main.tsx:146`; varsayılanlar `staleTime 10s, gcTime 5m, refetchOnWindowFocus, keepPreviousData` :146-163 |
| **@tanstack/react-virtual** | **^3.13.26 KURULU** | `ui/VirtualTable.tsx:49` (tek tüketici Traces :1311), `ui/VirtualList.tsx:40` (tek tüketici AdminSql :544) |
| **@tanstack/react-table** | **YOK** | tablo mantığı yerli (`lib/dataTable.ts`) |
| uplot | ^1.6.32 | tek grafik motoru |
| @grafana/data, @grafana/ui | ^13.1.2 | ayrı chunk, 2 dosyaya hapsedilmiş |
| state kütüphanesi | **yok** | 3 React Context: Confirm/Auth/QueryClient |
| test | vitest ^4.1.10, jsdom ^29 | **@testing-library YOK**; bileşen testleri `createRoot`+`act` elle (`comboboxEsc.test.tsx:28`) |

Bundle: rolldown `advancedChunks` grupları (`vite.config.ts:63-79`: vendor/charts/grafana/router/tanstack/otel/graph);
48 rota lazy (`App.tsx:16-80`), hover prefetch (`App.tsx:129`), `chunkSizeWarningLimit 1500`.

**Öneri (K1):** `@tanstack/react-table` EKLENMEZ — gerekçe §9. `@tanstack/react-virtual` zaten
`tanstack` chunk'ında; yeni bağımlılık **sıfır**. `@testing-library/react` + `jest-dom` **dev** bağımlılığı olarak
eklenir (§12); bundle'a girmez.

---

## B. Zaman / bağlam durumu

### 5. Zaman aralığı nerede

- Tip `TimeRange {preset, fromMs?, toMs?}` (`lib/types.ts:3578`). Hook `useUrlRange(defaultPreset?)`
  (`lib/useUrlRange.ts:128`): **URL `?range=` kaynak**, öncelik `?range=` → sessionStorage `cm.lastRange` →
  varsayılan (:136, saf `pickRangeString` :104); nesne kimliği ham dizeye memo :138-141 (v0.5.184 tuzağı).
- Kodlama `encodeRange`/`decodeRange` (`lib/urlState.ts:9/47`): preset → `1h`; mutlak → **`custom:<fromMs>-<toMs>`**;
  çıplak `custom` reddedilir :66. `timeRangeToNs` `lib/utils.ts:16-26` (bilinmeyen preset → 24 saat :20);
  ikinci codec `rangeToSince` :28 (Go'da gün birimi yok).
- **Sayfa geçişinde kaybolmuyor — iki kanal:** (1) sessionStorage `cm.lastRange` (`storage.ts:31`, `setRange` :180,
  `rememberRange` :86); oturum-kapsamlı seçim v0.8.409 donuk-mutlak-pencere olayından (:42-57). (2) Sahip
  sayfalar yapışkan aralığı URL'ye geri yazar (:159-165; `owns = defaultPreset !== undefined` :132 — CopilotChat
  ve FilterBuilder salt-okur, `/settings`'i kirletmez).
- Linkler: `navHref(to, search)` (`lib/navHref.ts:40`) **yalnız `custom:` aralığı + `env` taşır** (:47/:58);
  göreli preset yapışkan kanaldan gelir; hedef kazanır. Sidebar her nav öğesinde (`Sidebar.tsx:573/592`).
  Pivot aileleri ayrı: `pivotHref.ts` (6 üretici, pencere ZORUNLU), `serviceHref.ts:64-73`, `entityHref.ts`,
  `dashboardHref.ts`, `logsUrl.ts`, `inboxUrl.ts`.
- Zoom: `usePageZoomRange` (`lib/chart/usePageZoomRange.ts`; Traces :206, Endpoints :177), LIFO geri alma.
- **Global store yok**; aralık URL + sessionStorage'da. Bu iyi bir temel: ContextBar yeni bir store DEĞİL,
  aynı URL sözleşmesinin tek yazıcısı olur.

### 6. Cluster / namespace / service filtreleri

| Param | Sahip / codec | Okuyucular |
|---|---|---|
| `env` | `useUrlEnv` (`lib/useUrlEnv.ts`): URL → LS `coremetry-env` → ''; boş param = "tüm env'ler" :59; yazıcı `EnvPicker` (topbar) | Traces :212, Endpoints :182, Logs, Databases :144, Explore… |
| `cluster` | **hook YOK** — ham `searchParams.get('cluster')` **12 yerde** | `Traces.tsx:222`, `Services.tsx:175`, `Endpoints.tsx:186`, `EndpointDetail.tsx:59`, `Explore.tsx:394`, `Rollouts.tsx:69`, `Pod.tsx:77`, `Clusters.tsx:141`, `ProblemsSection.tsx:227`, `logsUrl.ts:64`, `TopEndpointsCard.tsx:57` |
| `namespace` / `ns` | ham; **aynı sayfada iki ad**: `Clusters.tsx:177/180` (`ns` = çekmece `cluster\|namespace`) ve :214/:435 (`namespace` = filtre) | `Services.tsx:183`, `Rollouts.tsx:70`, `Pod.tsx:78` |
| `service` | sayfa başına state | Traces :267, Logs, Explore, Endpoints |
| `compare` | **üç kodlama**: `'prior'` (`Services.tsx:291`, `Databases.tsx:86`, `Messaging.tsx:45`); `'0'`=kapalı/varsayılan açık (`Endpoints.tsx:229`); `'1'`=açık (`EndpointDetail.tsx:60`, `endpointParam.ts:99`) | |
| `cols` | üç codec: set (`endpointCols.ts`), sıralı liste (`Traces.tsx:310`), sıralı liste (`Logs.tsx:221`) | |

Sayfalar arası taşıma: `env` her nav linkinde (`navHref`); `cluster/ns/service` yalnız pivot üreticilerinin
elle geçirdiği kadar. **Bir sayfada seçilen cluster sidebar'dan başka sayfaya geçince kaybolur** (navHref
taşımıyor). Tek "global" seçim `env`.

### 7. SSE / polling, aralık değişince

- SSE: `useEventStream` (`lib/queries/eventStream.ts:45`), AppShell'de tek mount :91; Web Locks lider seçimi
  (tek EventSource, BroadcastChannel yayını :29-62). Olaylar `problem.*`, `anomaly.*`, `rollout` :43.
  Geçersiz kılma haritası `lib/queries/eventInvalidations.ts:17` (anahtar bazlı, bilinmeyen tür → hiçbir şey :35);
  `open`'da catch-up :44.
- **Aralıkla ilişkisi: yok.** Geçersiz kılma sorgu anahtarına; aralık anahtarın içinde → aralık değişince
  yeni anahtar, SSE yalnız mount'lu anahtarı tazeler. Çakışma yok; ContextBar bunu bozmaz.
- Polling: 75 `refetchInterval` (5 s health; 10 s admin; 15 s rollouts/entities; 30 s services/endpoints/inbox/
  problems/incidents/events/alerts; 60 s anomalies/watchers/slos/runbooks). `document.hidden` React Query
  varsayılanıyla örtük (`refetchIntervalInBackground=false`); 27 açık kontrol de var.
- Aralık değişince: React Query `keepPreviousData` (main.tsx:146-163) eski veriyi tutar, yeni anahtar fetch;
  Traces liste effect'i AbortController ile uçuştaki isteği iptal eder :499. ContextBar'ın tek riski **ardışık
  iki yazım** (from+to ayrı ayrı) → iki fetch; hook tek `setSearchParams` ile atomik yazmalı (§10).

### 8. URL şeması — istenen ↔ uzlaştırılan

İstenen: `?from=&to=&cluster=&ns=&service=&compare=`. Tasarım paneli (3 bağımsız tasarım
+ 2 hakem) üçü de aynı sonuca vardı ve hakemler istenen şemanın **harfiyen** uygulanmasını
"reddedilecek" saydı; uzlaşma aşağıda. Operatör kararı bekleyen kısımlar §12'de.

| Param | Kanonik (yazılan) | Girişte kabul (yazılmaz) | Gerekçe |
|---|---|---|---|
| zaman | `range=1h` · `range=custom:<fromMs>-<toMs>` | `from=now-1h&to=now` (Grafana grameri, yalnız `now-<n><birim>`), `from=<epoch ms>`, `from=<epoch ns>` (basamak sayısıyla ayırt: ≥16 ns, ≥12 ms) | tek üretici `windowRangeParam` (`urlState.ts:28`), tek çözücü `decodeRange` (`:47`); `shareUrl` göreliyi kopyalarken mutlağa dondurur (**paylaşılabilir link garantisi bugün de var**); `navHref` `custom:` taşır; kayıtlı görünümler ham query string saklar (`SavedViewsBar.tsx:24-32`) — kanal değişince 10 sayfanın kayıtlı görünümü yeniden yazılmalı. `from=` /pod'da drill kaynağı (`Pod.tsx:87`) |
| `env` | `env=` (mevcut) | — | istenen listede yok ama global EnvPicker + Traces liste/aggregate/CSV tüketiyor; KALIR |
| `cluster` | `cluster=` (mevcut) | — | 12 okuyucu zaten bu adı kullanıyor; `pivotHref.ts:93` üretiyor |
| namespace | `namespace=` | `ns=` yalnız o rotada başka anlamı yoksa | `ns` = /clusters çekmecesi (`cluster\|namespace` değeri, `Clusters.tsx:177-185`); `namespace` dört sayfada mevcut (`Services.tsx:183`, `Clusters.tsx:214/435`, `Rollouts.tsx:70`, `Pod.tsx:78`). ⚠ `/api/traces` bugün `env`+`cluster` alır, `namespace` almaz (`api.go:4229/4236`) → Traces'ta bu boyut "uygulanmıyor" (devre dışı + ✕) olarak gösterilir ya da ayrı spec ile `k8s.namespace.name` FilterExpr'e itilir |
| `service` | `service=` (mevcut) | — | `pivotHref.ts:91-92` çoklu `services=` de var |
| `compare` | `compare=prior` · yokluk = kapalı | `compare=1` → `prior` | üç kodlama tek; Traces'ta önceki-pencere kıyası yok → dilim 1'de "uygulanmıyor" |
| tablo | `s_<storageKey>`, `cols=`, `page=`, `view=`, `filters=`/`filterGroup=` | — | tablonun kendi parametreleri, bağlam çubuğunun DIŞINDA |

Paylaşılabilirlik değişmezleri: (1) sorguyu değiştiren her seçim `replace:true` + `prev` kopyasıyla
URL'ye yazılır; (2) "Linki kopyala" göreli aralığı mutlağa dondurur, diğer parametreler bayt-bayt; (3)
kayıtlı görünüm tüm query string'i saklar (bir görünümdeki `cols=` sunucu tercihini ezer — ColumnPicker
bunu "bu görünüm kolonları sabitler" diye söyler); (4) `useContextParams` hiçbir zaman takma ad yazmaz;
(5) boş değer parametreyi SİLER (`cluster=` bırakılmaz), yalnız `env` için açık-boş anlamlı
(`useUrlEnv.ts:53-61`).

Yapışkanlık: `range` oturum (mevcut `cm.lastRange`), `env` localStorage (mevcut), **`cluster/namespace/
service/compare` yalnız URL** — bunlar bir LİNKİN kapsamıdır, çalışma alanı ayarı değil; yapışkan
yapmak v0.8.409 sınıfını ("sayfa filtreli açıldı, kimse söylemedi") geri getirir. `navHref` bu üçünü
taşımaz (bir servisin trace'inden `/services` listesine geçince o servis filtresi kalmamalı); yalnız
`env` + zaman global. Hedef sayfa parametreyi anlamıyorsa çubuk onu **gizlemez, devre dışı + ✕ ile
gösterir** (EnvPicker `applies` kuralı, `EnvPicker.tsx:31-47`).

---

## C. Plan

Yöntem: üç bağımsız tasarım (TanStack-first / artımlı / ürün-önce) + iki hakem (ölçütler:
ev kurallarına uyum, geçiş riski, bundle, test edilebilirlik, tech-lead kalitesi). **Kazanan:
artımlı** (25/25 ve 23/25); ürün-önce 21/19; TanStack-first 15/16. Hakemlerin sert reddi:
`@tanstack/react-table` eklemek (ölçülmemiş +12-25 KB gz eager yola, saf çekirdeğin zaten sahip
olduğu sıralama/URL codec/mühürlü genişlik/fit için ikinci bir kolon otoritesi; `project-ui-stack`
"1.0 öncesi bileşen kütüphanesi yok"); ContextBar'ı sayfa düzeyinde ikinci bir şerit olarak çizmek
(Topbar zaten EnvPicker+TimeRangePicker basıyor, `Topbar.tsx:41-46` — iki yazıcı = iki yazım
kayması sınıfı, `feedback-no-floating-strips`); istenen `ns=`/`from=` anahtarlarını kanonik yazmak
(§8). Ürün-önce tasarımdan aşılananlar: boyut başına `applies` (devre dışı + ipucu), `carryContext`,
`/api/preferences` anahtar beyaz listesi, "Varsayılanım yap / Sıfırla" + kaynak ipucu
(url·server·local·default). TanStack-first'ten aşılananlar: `cols=` kendi-kendine-yazım yarışı,
`tracesRowLink.test.ts` yeniden hedefleme, `primitiveClasses` tarayıcısının özyinelemesi, epoch
birimi tespiti, kayıt defteri sayım-pini, yeniden boyutlandırma tutamacı a11y.

### 9. DataTable API

**Yer:** `src/components/ui/DataTable/` — dilim 1'de `index.ts` mevcut yapıştırıcıyı yeniden
ihraç eder (`useDataTable`, `DataTableHead`, `DataTableColgroup`, `ColResizeHandle`,
`ResetLayoutButton`, `VirtualTable`); `DataTable.tsx`/`VirtualTable.tsx`'in fiziksel taşınması
(79 içe aktaran dosya + yolla okuyan kapı testleri) **SON dilim**. Saf çekirdek `lib/dataTable.ts`
değişmez; yeni saf mantık `lib/columnModel.ts`, `lib/rowSelection.ts`, `lib/contextParams.ts`.

```ts
// ui/DataTable/types.ts
export interface ColumnDef<T> extends DataTableColumn<T> {   // lib/dataTable.ts:15-88 aynen
  accessor?: (row: T) => unknown;            // ham değer (kopya/CSV); sortValue'dan bağımsız
  cell?: (ctx: CellContext<T>) => ReactNode; // renderer; varsayılan <TextCell>
  hideable?: boolean;                        // false = kimlik kolonu (traces: time, operation)
  reorderable?: boolean;
  serverSortKey?: string;                    // sunucu ORDER BY anahtarı (Traces SERVER_SORTABLE :158)
  ownLink?: boolean;                         // renderer kendi <a>'sını basar; row-link sarmaz
}
export interface ColumnModel { v: 1; order: string[]; hidden: string[]; widths?: Record<string, number>; sig: string }
// useDataTable seçenekleri ADDİTİF — hepsi yokken bugünkü davranış bayt-bayt (109 çağrı yeri)
columnModel?: { value: ColumnModel | null; onChange: (next: ColumnModel) => void };
selection?:  { mode: 'single' | 'multi'; getRowId: (row: T) => string; value?: ReadonlySet<string>; onChange?: (ids: ReadonlySet<string>) => void };
server?:     { page: number; pageSize: number; hasMore: boolean; onPage: (p: number) => void; onSort?: (s: SortState) => void };
getRowHref?: (row: T) => string | null;    // satır = link (v0.10.216); VirtualTable her ownLink-olmayan hücreyi <Link className="row-link"> ile sarar
```

- **Sunucu sıralama/sayfalama:** `server` demeti Traces'ın el-yapımı `dt.sort → setSort/setOrder/
  setPage(0)` effect'ini (`Traces.tsx:914-931`) yerine geçer; istemci sıralama `serverSort:true`
  ile yasak kalır (`lib/dataTable.ts:222`).
- **Sütun seç/gizle/sırala:** `ColumnPicker` (`Menu`/`MenuItem`, `role="menuitemcheckbox"`,
  filtre girdisi, Esc `escLayer`), kimlik kolonları gizlenemez, "Sıfırla" altbilgide; sıra
  `reconcileColOrder` (`lib/tableColumns.ts:11` — bugün Logs kullanıyor) ile uzlaştırılır.
  Genişlik mühürleme kuralı korunur: `columnLayoutSig` uyuşmazsa genişlik düşer, `order/hidden`
  kalır (TanStack-first'in tüm blobu mühürleme önerisi reddedildi — her genişlik değişikliği
  herkesin kolon seçimini sıfırlardı).
- **Hücre renderer'ları** (`ui/DataTable/renderers/`, token-only, yoğunluk-agnostik — v0.9.243 ve
  v0.10.225 olayları hücrenin kendi font/padding'inden çıktı): `TextCell` (`'—'` yer tutucu),
  `TimeCell` (`tsDateTime`, en solda), `ServiceCell` (`SvcBadge`), `DurationCell` (`DurationBar`,
  `visibleMax` `CellContext.meta` ile), `StatusCell` (`<Badge tone>` + hata-span sayısı), `NumberCell`
  (`.num`), `AttrCell` (`row.extras[id]`, yoksa `'—'`), `LinkCell` (`ownLink:true`), `SelectCell`
  (öncü, 34 px, `Inbox.tsx:906`). Sparkline hücresi mevcut `Sparkline.tsx` ile. Kaynak-tarama testi
  `renderers/` içinde `fontSize/padding/hex` yasaklar.
- **Durumlar:** ilk yük `<TableSkeleton>`; tazeleme satırlar kalır + `opacity .55` + `aria-busy`;
  hata `<Empty>` + `↻ Retry`; boş sayfaya özel `Empty`; tercih GET hatası → yerel/varsayılan model +
  engellemeyen toast; tercih PUT hatası → yerel kalır, sonraki değişimde yeniden dener (tablo asla
  tercih yazımına kilitlenmez).
- **Satır seçimi:** `getRowId` zorunlu (indeks anahtarı sıralama/sayfalamada kırılır), Space /
  Shift+Space / Ctrl+A, `aria-selected`, `aria-multiselectable`.
- **Klavye:** mevcut kayıt defteri (`lib/keyboard.ts`, `useTableNav`): j/k, gg/G, Enter/o, Esc;
  YENİ: son satırda j → `server.onPage(page+1)` (`onPageBoundary` v0.9.1018 — Traces hiç bağlamamış),
  `th[tabIndex=0]` Enter/Space sırala, Shift+←/→ 8 px genişlet, Alt+←/→ taşı, tutamaç
  `role="separator" aria-valuenow` + Home sıfırla; sanal listede `aria-rowcount/rowindex` +
  `scrollToIndex` (bugün `useTableNav.ts:106-111` `querySelector` ile kaydırıyor — mount edilmemiş
  sanal satıra ulaşamaz, **gizli kusur**); `aria-live="polite"` "N satır · X'e göre sıralı".
- **VirtualTable düzeltmeleri (panelin bulduğu dört gizli kusur):** `colCount` `visibleColumns`'ı
  yok sayıyor (`VirtualTable.tsx:69`), nav auto-scroll sanal satıra ulaşamıyor, `aria-rowcount`
  yok, `contain:strict` sınırsız yükseklikte gereksiz.

### 10. ContextBar + useContextParams

- **Yer:** `src/components/ContextBar/` — yeni bir şerit DEĞİL; `Topbar`'ın bugün bastığı
  EnvPicker+TimeRangePicker çiftinin yerine geçer (`Topbar.tsx:41-46`), `#topbar` içinde (uygulama
  kromu). `Topbar` bir `context?: ContextBarProps` prop'u alır; geçmeyen sayfalar bayt-bayt aynı.
  Kontroller soldan sağa mevcut atomlarla: `TimeRangePicker` (aynen) · `EnvPicker applies` (aynen) ·
  Cluster (`useClusters` ≤10 değer → `<select>`, üstü `Combobox serverFiltered`) · Namespace (cluster
  uygulanıyorsa) · `ServicePicker` (sunucu-debounced) · Compare `<Chip>`; her kontrol `Field`
  etiketli ya da `aria-label`; çubuk `role="group" aria-label="Query context"`; dar ekranda üç kapsam
  kontrolü `DisclosureButton "Scope (n)"` arkasına (PageControls emsali). Yoğunluk cascade'i zaten
  kapsıyor (`globals.css:2594-2609`), satır içi ölçü yok.
- **Hook:** `useContextParams({ defaultPreset, applies })` → `{ params, set, sig, windowNs }`.
  Bileşim, kopya değil: `range` ← `useUrlRange(defaultPreset)` (sahiplik + oturum yapışkanlığı),
  `env` ← `useUrlEnv()`, `cluster/namespace/service/compare` ← `useSearchParams` + saf codec'ler
  `lib/contextParams.ts` (`readContextParams(sp, applies)`, `writeContextParams(prev, patch)`,
  `parseNowExpr`, epoch birimi tespiti). `set()` tek `setSearchParams(prev => …, {replace:true})`
  (aralık `setRange`, env `setEnv` üzerinden — oturum/LS yazımı korunur). `sig` = sahip olunan
  parametrelerin FNV'si (Logs `urlSig` emsali) — sayfalar URL→state içe aktarımını bununla korur.
  `windowNs` `useMemo([range])` (v0.5.184). **Global store yok** — URL + iki mevcut yapışkan kanal.
  ⚠ Operatör hedefi `src/hooks/` — depo `use*.ts`'leri `src/lib/`'de tutuyor, `src/hooks/` yok;
  tek dosya için ikinci konvansiyon açmak yerine `src/lib/useContextParams.ts` öneriliyor (§12 soru).
- **Traces ile etkileşim:** Traces'ın State→URL effect'i (`Traces.tsx:361-422`) `range/env/cols`'u
  sahiplenmeye devam eder (aynı hook değerlerini yeniden basar → iki yazıcı hemfikir);
  `cluster/namespace/compare` o effect'e YABANCI kalır ve `rebuildPreserving` ile taşınır (bugün
  `?cluster=` tam böyle yaşıyor, `Traces.tsx:213-222`).

### 11. Kalıcı sütun tercihi + backend

- **Depolama (invariant #5, yeni tablo YOK):** `saved_views` satırı — `page='table:<storageKey>'`
  (örn. `table:traces-list`), `owner_id=claims.UserID`, `name='columns'`, `query_string=JSON(ColumnModel)`,
  deterministik `id='pref:<uid>:<key>'` → `UpsertSavedView` doğal upsert (`saved_view.go:39`),
  `GetSavedView(id)` FINAL nokta okuma (`:84`). `ai-chat` emsali (`ai_conversations.go:16-22`) aynı
  tabloyu blob olarak kullanıyor. `table:` öneki tercihleri `SavedViewsBar`'ın `page='traces'`
  listesinden uzak tutar (`ListSavedViews` tam eşitlik, `:113-116`). Takım-paylaşımlı preset dilim
  1'de yok.
- **İstemci önceliği** (Traces'ın bugünkü üç basamağına bir basamak): URL `?cols=` > sunucu tercihi >
  localStorage (`traces-extra-cols`, `dt.traces-list.widths`) > `DEFAULT_TRACE_COLUMNS`.
  Genişlikler dilim 1'de tarayıcı-yerel kalır (bugünkü tasarım, `DataTable.tsx:108-110`); `order/hidden`
  sunucuya. `useTablePrefs(storageKey)` (`lib/queries/prefs.ts`): `staleTime 5m`, `enabled: !!user`,
  800 ms debounce'lu PUT, iyimser yerel yazım, `BroadcastChannel('coremetry-prefs')` sekmeler arası.
  ⚠ **`cols=` kendi-kendine-yazım yarışı:** Traces mount'ta `cols=`'u URL'ye basıyor (`Traces.tsx:417`);
  tercih sorgusu bitmeden basarsa URL > sunucu önceliği sunucu tercihini sonsuza dek erişilmez kılar
  → `cols` girdisi `prefs.status !== 'pending'` olana dek sahiplenilmez; jsdom testi ilk
  `history.replaceState`'in `cols=` taşımadığını sayar.
- **Endpoint** `internal/api/preferences_routes.go`: `GET/PUT/DELETE /api/preferences/{key}`
  (`{key}` `^[a-z0-9][a-z0-9-]{0,63}$`); her oturum açmış rol (kişisel durum — "viewer salt-okur"
  paylaşılan durumu hedefler; §12 soru); `s.audit` YOK (admin yazımı değil, idempotent, kullanıcı-kapsamlı);
  `serveCached` YOK (kişisel FINAL nokta okuma); gövde 16 KiB; sunucu yalnız şekli doğrular (id regex,
  ≤64 giriş), kolon kimliklerini yorumlamaz; claims yoksa 401 (asla `owner_id=''` paylaşımlı satıra
  düşme).
- **api.go'ya satır EKLEMEDEN kayıt:** depoda kayıt defteri deseni yok (`init()` 0, registrar dilimi 0;
  her domain dosyası `registerRoutes`'a bir satır, `api.go:617`). Mekanizma: `internal/api/route_registry.go`
  — `registerRoutesExtra(name, fn)` adlı harita (çift kayıt panic), `buildMux` `api.go:611-615`'ten
  bu dosyaya TAŞINIR (api.go **5 satır KÜÇÜLÜR**) ve `registerRoutes` sonrası defteri deterministik
  sırayla boşaltır; `preferences_routes.go` `func init() { registerRoutesExtra("preferences",
  (*Server).registerPreferencesRoutes) }`. Hakem aşısı: `rollup_routes.go` + `annotation_routes.go`
  (api-route skill'inin iki sapması) aynı commit'te deftere taşınır (net −1 satır daha).
  Testler: `mux_routes_test.go` `registerRoutes` → `buildMux()` (defter rotaları Go 1.22 çakışma
  testine girer — yoksa çakışma boot'ta panic'ler, v0.9.465-470 sınıfı); `route_registry_test.go`
  (sayım-pini = `registerRoutesExtra(` çağrı sayısı, `api.go` "preferences" içermez,
  `GET /api/preferences/x` çözülür); `scripts/audit.sh` CHECK 7 dosya bağımsız, değişmez.

### 12. Geçiş planı, riskler, test stratejisi

| # | Dilim (her biri kendi vX) | Efor | Regresyon riski |
|---|---|---|---|
| 1 | `ui/DataTable/index.ts` barrel + `lib/columnModel.ts`, `lib/rowSelection.ts` (+vitest) | S | yolla okuyan kapı testleri (`shortcutSearchTarget`, `tracesRowLink`) — yalnız barrel, taşıma yok |
| 2 | `route_registry.go` + `preferences_routes.go` + Go tablo testleri | M | `TestMuxRoutePatterns` `buildMux`'a geçmezse defter rotaları çakışma testinden kaçar |
| 3 | `lib/types.ts` + `lib/api.ts` + `lib/queries/prefs.ts` + `keys.prefs` | S | `cancellation.test.ts` listesine hook kaydı |
| 4 | `useDataTable`/`VirtualTable`/`DataTableHead` yetenekleri (additif) + dört VirtualTable düzeltmesi + jsdom sözleşme testleri | M | 109 tüketici: her yeni seçenek yokken bugünkü davranış; `columnLayoutSig` değişmez |
| 5 | `ContextBar` + `useContextParams` + `lib/contextParams.ts` codec'leri + Topbar `context` prop'u (henüz hiçbir sayfa geçmez) | M | `ns`/`from` çakışmaları (§8) — takma adlar salt-okur ve rota-korumalı |
| 6 | **Traces** (dilim 1'in tek sayfası): `ColumnDef` + renderer'lar (`renderTraceCell` dağılır), `ColumnPicker` (ColumnManager'ın yerine, `Button` atomu), `channel_code`/`function_code` varsayılan (kimlikler küçük harf — prod yazımı; etiket büyük harf gösterilebilir), sunucu tercihi, ContextBar `applies=['range','env','cluster','service']` (namespace/compare uygulanmıyor → devre dışı) | L | State→URL effect sahiplikleri; extras effect `extraCols`'a anahtarlı (`Traces.tsx:507-545`); `tracesRowLink.test.ts` aynı commit'te yeniden hedeflenir |
| 7 | `DataTable.tsx`/`VirtualTable.tsx` fiziksel taşınma (`git mv` + 79 dosya import sed, tsc kapısı, shim yok) | S | mekanik; yolla okuyan testler aynı commit'te |
| 8+ | Diğer sayfalar: Logs (LogTable kolon codec'i birleşir) → Services/Endpoints (`ColumnToggle`+`endpointCols` codec'i) → Inbox (VirtualTable DEĞİL: değişken yükseklik) → geri kalanı | M/sayfa | brief gereği dokunulmaz; Traces şablon |

**Bundle (ölçüldü, 31 Ağu `dist/`):** Traces chunk 50.9 KB / 17.1 KB gz; `@tanstack/react-virtual`
zaten grafikte (virtual-core 44 KB ham ≈ 6-7 KB gz) ve `vendor` catch-all'da eager (grup adı yok,
`vite.config.ts:71`). Artımlı plan ≈ **+5-6 KB gz**, yeni chunk yok, TTFI bütçesi değişmez.
Hijyen (isteğe bağlı): `{ name: 'virtual', test: /@tanstack\/(virtual-core|react-virtual)/,
priority: 80 }` → sanal tablo kullanmayan sayfalarda ≈ −6 KB gz TTFI; `ANALYZE=1 npm run build` ile
doğrula (v0.9.708 uplot/grafana önceliği dersi).

**Riskler (panelin ölçtükleri):** ikinci pencere kanalı (§8) · State→URL effect'i ile ContextBar
yarışı (ikisi de aynı hook'lardan geçtiği için güvenli) · kayıtlı görünümdeki `cols=` sunucu tercihini
ezer (belgelenir) · `init()` kaydı `registerRoutes`'tan daha az görünür (adlı defter + sayım-pini +
`buildMux` testi) · sanallaştırma + klavye (`scrollToIndex` düzeltmesi önce) · kolon-kimliği yazımı
(`channel_code` prod, `CHANNEL_CODE` operatör — terfi kolonu ikisini de okur ama extras istenen
yazımla döner; tercih küçük harfle normalize edilir) · viewer'ın kişisel tercih yazması (operatör
onayı) · `saved_views.query_string` için JSON içerik konvansiyonu (struct yorumuna not).

**Test stratejisi:** (1) saf/node vitest — `columnModel` (bilinmeyen id düşer, yeni id bildirilen
konuma, `hideable:false` gizlenemez; sig uyuşmazlığında genişlik düşer order/hidden kalır; kaynak
öncelik tablosu boş-dize `cols=` dahil), `rowSelection` (yeniden sıralamada çapa), `contextParams`
(from/to takma adları: `now-1h/now`, epoch ms, epoch ns, çöp → yok sayılır; `ns` yalnız rota izin
verirse; `compare=1→prior`; boş değer siler, yabancıyı korur — `rebuildPreserving` idempotans stili);
(2) jsdom sözleşme testleri (`// @vitest-environment jsdom`, `createRoot`+`act` — `@testing-library`
**yok ve eklenmez**, `Button.contract.test.tsx:27-45` emsali): `th tabIndex=0`+`aria-sort`, Enter
sıralar, Shift+→ 8 px, `aria-selected`, `aria-rowcount`; VirtualTable 200 satır + sınırlı yükseklikte
j×150 → `scrollToIndex` çağrılır; ContextBar uygulanmayan boyut devre dışı + ✕ çalışır;
(3) kaynak-tarama kapıları (depo idiomu): `renderersNoInlineMetrics`, `dataTableBarrel` (taşıma sonrası
eski import kalmaz), `contextBarAliases` (hiçbir sayfa `set('from'`/`'to'`/`'ns'` yazmaz),
`primitiveClasses` tarayıcısı `ui/DataTable/` içine özyineler; (4) Go tablo testleri (/tdd):
`preferences_routes_test` (regex, 401, sahip izolasyonu, 16 KiB, tombstone), `route_registry_test`,
`mux_routes_test` → `buildMux`; (5) Storybook YOK — `npm run build:ds` (ui/ paket derlemesi) mevcut
kapı; Playwright yalnız istenirse.

### Onay soruları

1. Zaman kanalı: `range=` tek yazılan kanal + `from/to` yalnız girişte (öneri) — yoksa sayfalar
   `from/to` YAZSIN mı? (o zaman 12 hook-dışı okuyucu + 6 yazıcı + kayıtlı görünümler göç eder)
2. Namespace yazımı: `namespace` (öneri) mi, `/clusters` çekmece parametresi yeniden adlandırılıp `ns` mi?
3. Hook yeri: `src/hooks/useContextParams.ts` (istenen, yeni konvansiyon) mi, `src/lib/` (depo konvansiyonu) mi?
4. Sütun GENİŞLİKLERİ tarayıcı-yerel mi kalsın (bugünkü tasarım), sunucuya mı gitsin?
5. Varsayılan kolon kimlikleri küçük harf (`channel_code`, prod yazımı) + büyük harf ETİKET (öneri) mi, kimlikler `CHANNEL_CODE` mi?
6. `viewer` kendi kolon tercihini yazabilsin mi? (öneri: evet; audit satırı yok)
7. ContextBar sonradan tüm aralık-sahibi sayfalarda EnvPicker+TimeRangePicker çiftinin yerine varsayılan mı olsun, opt-in mi?
8. Traces'ta `namespace` filtresi için backend (`k8s.namespace.name` FilterExpr) ayrı spec mi, boyut "uygulanmıyor" mu kalsın?

---

## Uygulama durumu (2026-09-02, "Sırayla devam" — önerilen varsayılanlar)

| Dilim | Sürüm | Not |
|---|---|---|
| 1 barrel + saf çekirdek | v0.10.246 | `ui/DataTable/index.ts`, `lib/columnModel.ts`, `lib/rowSelection.ts` |
| 2 route defteri + `/api/preferences` | v0.10.247 | `route_registry.go` (buildMux taşındı, api.go −5 satır; rollup/annotation deftere), `preferences_routes.go` |
| 3 prefs istemcisi | v0.10.248 | `lib/queries/prefs.ts` useTablePrefs (debounce, BroadcastChannel) |
| 4 useDataTable additif + VirtualTable | v0.10.249 | columnModel/selection/server/getRowHref; th klavyesi; 4 VirtualTable düzeltmesi; jsdom sözleşmesi |
| 5 ContextBar + useContextParams | v0.10.250 | `src/hooks/useContextParams.ts` (**soru 3: operatörün istediği konum**), Topbar `context` prop'u, codec + alias kapısı |
| 6 Traces | v0.10.251 | ContextBar applies=[range,env,cluster,service]; sunucu tercihi; CHANNEL_CODE/FUNCTION_CODE etiketi; server demeti. **Ertelenen:** ColumnPicker (ColumnManager kalır), hücre renderer ayrıştırması, satır-link sarmalaması (VirtualTable getRowHref) |
| 7 fiziksel taşınma | — | bekliyor |
| 8+ diğer sayfalar | — | brief gereği dokunulmadı |

Onay soruları → uygulanan varsayılanlar: 1 `range=` kanonik, from/to giriş takma adı · 2 `namespace` · 3 `src/hooks/` (operatör isteği) · 4 genişlik tarayıcı-yerel · 5 kimlik küçük harf + etiket büyük · 6 viewer yazabilir · 7 opt-in (Topbar `context`) · 8 namespace backend ayrı spec.
