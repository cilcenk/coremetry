# Chart katmanı — teknik audit (2026-09-02)

**Durum:** SALT AUDIT. Kod yok, refactor yok, ONAY BEKLİYOR.
**Kapsam:** Coremetry frontend'inin tüm grafik yüzeyi + onu besleyen sunucu nokta bütçeleri.
**Yöntem:** kod okuması (Explore ajanı + ana oturum grep doğrulaması) + `dist/assets` üzerinde gerçek gzip ölçümü + `npm view` (ağ ÇALIŞTI, 2026-09-02).
Her iddia `dosya:satır`. Ölçülmemiş kaleme sayı verilmedi.

## 0. Özet ve brief düzeltmeleri

- **`@grafana/ui` zaten bağımlılıkta ve kullanımda** (`frontend/package.json:20-21`, v0.9.701 "CorePanel: @grafana/ui üstüne TEK sarmalayıcı"). Brief "gerekirse kullanılabilir" diyordu; gerçek durum: ana çizim hattı (hat A, 12 tüketici) `UPlotChart` + `UPlotConfigBuilder` üstünde, ikinci hat (hat B, 4 tüketici) kendi `engine.ts` uPlot'u. Karar: **yeni bileşen alınmaz, mevcut sınır korunur.**
- 🔴 **En acil kalem kod değil bir caret:** `^13.1.2` npm `latest` **13.2.1**'i kapsıyor ve 13.2.x **React ≥19** peer istiyor; repo React `18.3.1` pin. Lock koruyor, sözleşme korumuyor → **Dilim 0: tam pin / overrides.**
- **Server-side LTTB Go'da YOK**; olan step-bucketing + 2000 nokta bütçesi. Dilim 1 için LTTB gerekmiyor; asıl açık üç ucun piksel bütçesini görmemesi (Thanos trendleri, sparkline 120 sabit, LogsExplorer).
- Önceki kapalı audit'ler (motor tekleştirme, @grafana göç kararı, drag-zoom URL sahipliği, annotation ucu) **yeniden önerilmiyor** (aşağıdaki tablo).

**Bu audit NEYİ TEKRAR ÖNERMİYOR** (kapalı iş; `docs/audit/chart-consolidation-audit.md` + `docs/charts/00-feasibility.md`):

| Kapanmış | Kanıt |
|---|---|
| Dört uPlot motorunun tekleştirilmesi | `v0.9.844` "eski chart motoru söküldü"; `MultiLineChart.tsx:9-21` artık adaptör |
| `@grafana/ui` göç kararı | `components/chart/CorePanel.tsx:1-29` (FAZ 2 uygulandı) |
| drag-zoom → URL sahipliği | `lib/chart/usePageZoomRange.ts:8-32`, 10 sayfa tüketicisi |
| çift-tık zoom-geri · tooltip pin · x-bölge overlay | `lib/chart/engine.ts:113-128`, `lib/chart/tooltipPin.ts`, `lib/chart/overlays.ts:149` |
| annotation şeridi (ayrı endpoint) | `internal/api/annotation_routes.go:48`, `components/charts/AnnotationLane.tsx` |
| span-metrik nokta bütçesi | `chartStep.ts:66-76` + `internal/chstore/spanmetric.go:944-988` |

Aşağıdaki her madde **bugün açık** olan borçtur.

---

## 1. Uygulamadaki TÜM chart kullanımları

Motor sütunu: **CP** = `components/chart/CorePanel.tsx` (@grafana/ui `UPlotChart`), **EN** = `lib/chart/engine.ts` (kendi `new uPlot`), **SVG/CANVAS** = uPlot dışı.

| # | Bileşen (dosya) | Motor | Sayfa / yüzey | Veri kaynağı | Nokta / adım kaynağı |
|---|---|---|---|---|---|
| 1 | `chart/CorePanel.tsx:1312` | CP | *tüm* v2 panelleri | çağırandan `DataFrame[]` | çağıran |
| 2 | `chart/corePanelEntry.tsx:88` `CorePanelMulti` | CP | v2 çok-serili giriş | `SpanMetricSeries[]` | çağıran |
| 3 | `MultiLineChart.tsx:8-21` (adaptör) | CP | 13 dosya / 17 JSX: Clusters, EntityDetail, Pod, TrendPanel, MetricArea, OperationsTable, PodJmxInline, ExploreViz, AdminQuery, ConditionPreview, CorrelationContextDrawer, ServiceCharts, dependencies/shared | çağıran | `foldTopN` + çağıranın bütçesi |
| 4 | `ServiceCharts.tsx:622-693` | CP | `/service` RED üçlüsü | `/api/metrics/resolve` → fallback `/api/spans/metric-batch` | `stepForWidth(contentW)` `:181` + `mdp=quantizeWidth/2` `:230` |
| 5 | `pages/service/Overview.tsx:868/915/972` | CP | `/service` Overview | `spanMetricBatch` `:221,:296` | `panelMaxDataPoints(3)` `:213` |
| 6 | `pages/Pod.tsx:388-419` | CP | `/pod` RED + gecikme | `spanMetricBatch` `:188,:199` | `panelMaxDataPoints(3)` `:185` |
| 7 | `PodResourceCharts.tsx:84-93` / `PodInlineResourceCharts` / `ServiceInfraTab.tsx:242-280` (→ `MetricArea`) | CP | pod/infra CPU-Mem-Net | `/api/clusters/deploy-trend` (Thanos) | **sunucu merdiveni** `internal/thanos/client.go:1906` (≤480 nokta) — px-kör |
| 8 | `dashboard/PanelRenderer.tsx:445,610,821` (`DashChart`) | CP | `/dashboard` panelleri | metric / promql / span-metric | `dashboard/panelStep.ts:30` + `usePanelWidth.ts:31` |
| 9 | `explore/QueryPanel.tsx:160-198` | CP | `/explore` | `useExploreQueries.ts:212` | `stepForWidth(rangeSec, contentWidth)` |
| 10 | `CosreChart.tsx:49` | CP | Copilot sohbet grafiği | `spanMetricBatch` | **SABİT `maxDataPoints: 300`** |
| 11 | `charts/TimeChart.tsx:401` | **EN** | — | çağıran | çağıran |
| 12 | `traces/VolumeChart.tsx` (→ TimeChart) | **EN** | `/traces` hacim şeridi | `spanMetricBatch` | `stepForPoints(windowSec, barPanelMaxDataPoints(1))` `Traces.tsx:624` |
| 13 | `LogsHistogram.tsx:265-268` (→ TimeChart) | **EN** | `/logs`, servis Logs sekmesi, anomali çekmecesi | logstore timeseries | `logsBucketSec` `chartStep.ts:102` |
| 14 | `anomalies/ProblemDetail.tsx:551` (→ TimeChart) | **EN** | problem detayı | problems occurrence | **kendi step türetimi** (`occRaw[1]-occRaw[0]`) |
| 15 | `anomalies/ExternalEvidencePanel.tsx` (→ TimeChart) | **EN** | dış kanıt | dış kaynak | çağıran |
| 16 | `viz/TimeSeriesPanel.tsx:855` | **EN** | `RuntimeCharts.tsx:331`, `MetricQueryEditor.tsx:1100` | `/api/metrics/query` | `panelMaxDataPoints(1)` `RuntimeCharts:207`; MQE önizleme |
| 17 | `service/charts/OverviewChart.tsx:384` | **EN** | `ChartCard.tsx:110`, `pages/Incident.tsx` | span-metrik | çağıran |
| 18 | `Sparkline.tsx:80` | **SVG** | 11 dosya / 15 JSX: Services, Endpoints, Watchers, Dashboards, OperationsTable ×4, OverviewTables, DependenciesTable, Drawer, stmtDetail | `/api/services/sparklines` vb. | `maxBarsForWidth` / `maxLinePointsForWidth` (`lib/sparkline.ts:29`) + sunucu `SparklineBuckets = 120` (`internal/chstore/repo.go:1004`) |
| 19 | `LatencyHeatmap.tsx:100` | **CANVAS** | Explore, ServiceLatencyHeatmap, PanelRenderer | `/api/spans/heatmap` | `heatmapBucketCount` `chartStep.ts:111`; sunucu tavanı 240 `internal/chstore/heatmap.go:128` |
| 20 | `traces/LatencyScatter.tsx` | **CANVAS/SVG** | `Traces.tsx:1153` | trace satırları | istemci LTTB (hata noktaları atılmaz) |
| 21 | `charts/AnnotationLane.tsx` + `ServiceAnnotationLane.tsx` | DOM | `Service.tsx` | **`/api/annotations`** (ayrı uç) | `clusterAnnotations(…, buckets=120)` |
| 22 | `LogsExplorer.tsx:53-55` | (viz) | Logs explorer | logs timeseries | **`windowSec/60`, px-kör** |
| 23 | `Dashboards.tsx:160` | Sparkline | dashboard küçük resmi | — | **SABİT `step: 60`** |

**`MetricLineChart` / `MiniSparkline` / `ScatterPlot` / `TrendPanel` / `ChartCard`→`OverviewChart` zinciri:** ilk üçü repoda YOK (0 JSX); `ChartCard` yalnız `ServiceCharts.tsx`'te 3 site.

### Tekrar eden kod (bugün ölçülen)

| Kopya | Nerede | Not |
|---|---|---|
| **İki lejant uygulaması** | `chart/CorePanel.tsx` iç lejantı ↔ `chart/StatsLegend.tsx:37` (yalnız TimeChart `:1` + OverviewChart `:1`) | Aynı jest sözleşmesi (`legendVisibility.ts`) iki DOM'da; design-system K6 |
| **İki tooltip HTML üreteci** | CP tooltip (`CorePanel.tsx`) ↔ TC tooltip (`TimeChart.tsx:216+`) ↔ TSP | Saf çekirdek (`tooltipModel.ts`, `placeTooltip`, `tooltipPin.ts`) PAYLAŞILIYOR; `.ov-tt` şablonu değil |
| **İki y-eksen/formatter yolu** | CP: `@grafana/data` display processor (`lib/chart/dataFrame.ts:39`) ↔ EN presetleri: `lib/chartFmt.ts` `fmtSmart`/`fmtAxisTick` | Aynı seri iki sayfada farklı yazılabilir |
| **İki yarım birim haritası** | `lib/chart/metricUnit.ts:17-27` (9 giriş) ↔ `service/charts/routeSeries.ts` (yalnız `s|ms`) | design-system AS-4, hâlâ açık |
| **İki nokta bütçesi ailesi** | `chartStep.ts` (FE) ↔ `metricStepLadder` (`metricresolve.go:184`) | `route_pins_test.go` iki listeyi çiviliyor — iyi |

---

## 2. Sarmalayıcı mimarisi ve teknik borç

**Mevcut gerçek: TEK `new uPlot` var ve o CorePanel'de DEĞİL.**

- `new uPlot(` = **1** → `lib/chart/engine.ts:87`
- `<UPlotChart` = **1** → `components/chart/CorePanel.tsx:1312`

Yani **iki çizim hattı** yaşıyor:

```
hat A (v2, ana yol)   API → dataFrame.ts (birim/eşik/null köprüsü, @grafana/data)
                          → framesToAligned → UPlotConfigBuilder → UPlotChart
hat B (v1 kalıntı)    props → preset (TimeChart / TimeSeriesPanel / OverviewChart)
                          → buildOptions/buildData/signature → useChartEngine → new uPlot
```

Hat B'nin canlı yüzeyleri: **6** — `LogsHistogram`(3 sayfa), `VolumeChart`(/traces), `ProblemDetail`, `ExternalEvidencePanel`, `RuntimeCharts`, `MetricQueryEditor`, `ChartCard`/`Incident`.

### Mimari artıları (korunmalı)
- `engine.ts:69-107` rebuild deps yalnız `[signature, themeTick]`; `:83-86` `undefined` hook anahtarlarını siler (uPlot `fire()` `in`-check çökmesine merkezî savunma); `:134-154` setData fast-path + zoom-koruyucu y refit.
- `lib/chart/` = **30 saf modül + 24 vitest dosyası**. Tema `resolveVar.ts` + `useThemeTick.ts` (modül düzeyinde tek `MutationObserver`).
- Zoom sahipliği tek: `usePageZoomRange.ts` (yığın **efemer**, URL'e yazılmaz — copy-link temiz).

### Teknik borç

| # | Borç | Kanıt |
|---|---|---|
| B1 | **Prop patlaması** — `CorePanelProps` **30 alan** | `CorePanel.tsx:122-262` |
| B2 | **4 ölü prop kanalı** (0 tüketici): `bands`, `connectNulls`, `headerExtra`, `logScaleToggle` | ölçüldü; `bands` `:151`, `connectNulls` `:159`, `headerExtra` `:167`, `logScaleToggle` `:176`. CLAUDE.md "geriye-uyum şimi YOK" + `corePanelEntry.tsx:52-55`'in kendi kuralı ("yarım kablo bırakmıyoruz") ihlal |
| B3 | **İki lejant / iki tooltip HTML / iki formatter** (§1 tablosu) | — |
| B4 | **Re-init maliyeti config bağımlılık dizisinde saklı** — 15 elemanlı dep array; `thresholds={[...]}` inline verilirse her parent render'da destroy+recreate | `CorePanel.tsx:288`, `:995` |
| B5 | **Seri tipi zayıf** — `viz` panelin TAMAMI için tek string; per-seri mark (TimeChart'ın `type: 'bar'\|'line'\|'area'`) v2'de YOK | `CorePanel.tsx:261` vs `TimeChart.tsx:41` |
| B6 | **Dual y-axis v2'de YOK** — TimeChart `y`/`y2` taşıyor, CorePanel tek `y`. VolumeChart/LogsHistogram bu yüzden hat B'de kilitli | `TimeChart.tsx:42` |
| B7 | **`framesToAligned` sameGrid hızlı-yolu** yalnız uzunluk + uç timestamp'e bakar; iç kafes farklıysa **yanlış zamana çizer** (latent) | `lib/chart/dataFrame.ts:150-189`, parity-baseline D.1 #15 |
| B8 | **Canvas yüzeyler uPlot.sync'e giremiyor** — heatmap `cursorBus` üzerinden bağlı, scatter hiç bağlı değil | `LatencyHeatmap.tsx:5-9`, `lib/chart/cursorBus.ts` |
| B9 | `ref` tutamağı: CP `plotRef` yalnız stub testinde gözlemleniyor; hat B `useChartEngine` döndürüyor — iki farklı imza | `engine.ts:59`, `CorePanel.smoke.test.tsx:80` |

---

## 3. `@grafana/ui` değerlendirmesi

**Ölçüm 2026-09-02, `npm view` ağ ÇALIŞTI.**

| Kalem | Değer |
|---|---|
| repo `package.json` | `"@grafana/ui": "^13.1.2"`, `"@grafana/data": "^13.1.2"` (`frontend/package.json:20-21`) |
| lock'ta çözülen | `13.1.2` (`package-lock.json:1425`) |
| **npm `latest`** | **`13.2.1`** |
| **13.2.1 peerDependencies** | **`react: >=19`, `react-dom: >=19`** |
| 13.1.2 peerDependencies | `react: ^18.0.0`, `react-dom: ^18.0.0` |
| repo React | `18.3.1` **tam pin** (`package.json:37-38`) |
| dist.unpackedSize | 13.1.2 → 11.541.004 B · 13.2.1 → 12.069.611 B |
| lisans (npm metadata) | Apache-2.0 |
| diskte | `node_modules/@grafana` = **71 MB** |

> 🔴 **EN YÜKSEK ÖNCELİKLİ BULGU (yeni, bu oturumda ölçüldü):** `^13.1.2` caret'i **13.2.0/13.2.1'i kapsıyor** ve o sürümler React ≥19 peer'ı istiyor. Lockfile'sız/temiz bir `npm install`, `npm update`, ya da Dependabot benzeri bir bump **peer çakışması** üretir; `--legacy-peer-deps` ile geçilirse React 18 üstünde React-19-hedefli bir ağaç koşar. Bugün lock koruyor, **sözleşme korumuyor**. `00-feasibility.md §3.6` bunu tam olarak öngörmüştü ("peer tavanı `^18.0.0`; depo React 19'a çıkmak isterse bu paket KİLİTLER") — kilit ters yönden geldi.

### Sert bağımlılıklar (kurulu ağaçtan okundu, `node_modules/@grafana/ui/package.json`)

`@grafana/data 13.1.2`, `@grafana/schema 13.1.2`, `@grafana/i18n 13.1.2` (üçü de **dependency**, peer değil — ağaçtan çıkarılamaz), `@emotion/css 11.13.5` + `@emotion/react 11.14.0` + `@emotion/serialize` (**ikinci CSS-in-JS runtime**), `rxjs 7.8.2`, `lodash ^4.17.23`, `monaco-editor 0.34.1`, `jquery 3.7.1`, `ol 10.7.0`, `slate`, `prismjs`, `react-select`, `react-table`, `date-fns 4.1.0`, **`react-router-dom 5.3.4`** (repo v6 kullanıyor → major çakışma), `uplot 1.6.32` **exact pin**.

### Bundle — gerçek build ölçümü (`frontend/dist/assets`, 2026-08-31 build, gzip -6)

| chunk | raw | gzip |
|---|---:|---:|
| `grafana-BZI-xSD5.js` | 1.150.045 B | **173.039 B** |
| `charts-2ynqPV6l.js` (bizim uPlot katmanı) | 51.292 B | 21.930 B |
| `vendor-BL7nLE1G.js` | 139.927 B | 44.903 B |
| tüm `dist/assets/*.js` (birleştirilmiş, yaklaşık) | 3.893.461 B | ~961.602 B |

Yani `@grafana/*` **lazy chunk'ta ve tek başına gzip'in ~%18'i**. Statik bağlanırsa vendor 35 KB → 1 MB (`corePanelEntry.tsx:1-6` ölçümü).

### Tema entegrasyonu — bugünkü çözüm doğru, ama tek yönlü

- Bizim: `data-theme` DOM-push → `useThemeTick` → rebuild → `resolveVar` draw-anında CSS var → hex.
- Grafana: React-context-pull, `createTheme()` default'u **Grafana dark**; provider'sız kullanım **sessizce yanlış çizer** (v0.9.398 bug sınıfı).
- Bugünkü sınır: `CorePanel` yalnız `UPlotChart`/`UPlotConfigBuilder`/enum'ları alıyor, **hiçbir genel UI bileşenini almıyor** — `Button`/`Select`/`Table`/`Tooltip`/`Icon` sıfır import. `dataFrame.ts` yalnız `getDisplayProcessor`/`createTheme`/`ThresholdsMode`/`FieldType`. `corePanelContracts.test.ts:767` bu tekeli CI'da kırmızıya çeviriyor, `:150` `@grafana/schema` importunu yasaklıyor. **Bu sınır sağlam; genişletilmemeli.**

### Tree-shaking gerçekçiliği
`exports` map'te yalnız `.` ve `./unstable` var → **deep import kapalı**, cherry-pick mümkün değil. Barrel + shake çalışıyor (`UPlotChart` → 19 modül; `TimeSeries` → 431 modül, `00-feasibility.md §3.3`). Yani "sadece VizLegend alalım" demek, barrel'ı çekip shake'e güvenmek demektir.

### Karar önerisi — operatörün önerisiyle AYNI, gerekçeli

**Kendi uPlot sarmalayıcısını güçlendir. `@grafana/ui`'den YENİ hiçbir bileşen alma.**

| Aday | Karar | Gerekçe |
|---|---|---|
| `TooltipPlugin2` | ❌ **ALMA** | Bizim tooltip'te pin (`tooltipPin.ts`), satır tavanı (`capTooltipRows`), değer-sıralaması (`sortedTooltipRows`), XSS kaçışı (`v0.9.1371`, `escapeHTML`) ve **kendi Türkçe metnimiz** var. Grafana'nınki `hoverMode` + `onSelectRange` getirir, bizde ikisi de zaten var (`buildCursorOpts`). Net kazanç ≈ 0, tema riski gerçek. |
| `VizLegend` (`@public`) | ❌ **ALMA** | Tek cazip yanı `isSortable`/`onToggleSort`. Bizde lejant **iki uygulama** — doğru iş üçüncüsünü eklemek değil, ikisini `StatsLegend`'e indirmek (§7 Dilim 1). `VizLegend` emotion + `GrafanaTheme2` bağlar, `ui/` atom dilimizi ikiye böler. |
| Threshold editor | ❌ **YOK ZATEN** | `@grafana/ui`'de threshold **editörü** yok; `getThresholdsDrawHook` var ve karşılığı bizde `overlays.drawThresholds` (`lib/chart/overlays.ts`). Editör tarafı `dashboard/PanelEditor.tsx:541,594`'te zaten yaşıyor. |
| `GraphNG` / `TimeSeries` | ❌ **ALMA** | `graveyard/` + `@deprecated`; ürünün gösterdiği davranış (annotation şeridi, exemplar ◆, lejant istatistik modları) pakette **yok**. |
| `UPlotChart` + `UPlotConfigBuilder` (mevcut) | ✅ **TUT** | Zaten alındı, tek dosyada, CI kapılı. Sökmek §7'deki tüm dilimleri geciktirir. |
| `@grafana/data` display processor (mevcut) | ✅ **TUT** | Birim/ondalık/eşik çözümü. Elle yazmak = gelecekteki uyumsuzluk (design-system §7 kuralı). |

**Tek zorunlu iş:** `@grafana/ui` + `@grafana/data` caret'ini **tam pine indir** (`13.1.2`) veya `overrides` ile React-19 sürümlerini kilitle. Bu bir yeni bağımlılık değil, mevcut olanın **sözleşmeye bağlanması**.

---

## 4. Özellik listesi ve mevcut kodla mesafe

| Özellik | Durum | Kanıt / eksik |
|---|---|---|
| **Senkron crosshair (uPlot sync key)** | **KISMEN** | `cursorOpts.ts:59` yalnız X senkronlar (`scales:['x',null]` — Y senkronu v0.9.388'de bilerek kapatıldı). **46 `syncKey=` sitesi.** Ama grup adları sayfa başına elle kuruluyor (`podres:`, `infra:`, `podjmx:`, `clusters`, `service:*-ms`) ve `MultiLineChart.tsx:178` `-ms` son eki ekliyor → **aynı sayfada iki ad alanı**. Canvas yüzeyler ayrı otobüste (`cursorBus.ts`). |
| **Drag-zoom → ContextBar zamanı** | **VAR** | `usePageZoomRange.ts` → `useUrlRange`, `?range=custom:<ms>-<ms>`, `replace:true`; 10 sayfa. `hooks/useContextParams.ts:60` ContextBar'ın sahibi ama **zoom onunla değil `useUrlRange` ile konuşuyor** → iki yazıcı, tek URL. Explore bilinçli dışarıda. |
| **Legend tıkla-izole/gizle** | **VAR (iki kez)** | `lib/chart/legendVisibility.ts` (düz tık=isolate, Ctrl=toggle) → CP iç lejantı **ve** `chart/StatsLegend.tsx:37`. Kontrollü mod `hiddenNames` yalnız 1 tüketici (`QueryPanel.tsx:188`). |
| **Annotation'lar (rollout/problem, ayrı endpoint)** | **VAR** | `internal/api/annotation_routes.go:48` `/api/annotations` — deploy/rollout/alert_fired/alert_resolved/anomaly/event, 4 paralel kaynak, `annotationCap=500` + `truncated`. Şerit `AnnotationLane.tsx`. **Eksik:** şerit yalnız `Service.tsx`'te; Pod/Dashboard/Explore hâlâ kendi `regions=` mekanizmasını kuruyor (18 site). |
| **Eşik / referans çizgileri** | **VAR** | `overlays.ts:31` `ChartThreshold` + `drawThresholds`; 10 site. request/limit örneği: `clusters/TrendPanel.tsx:135,141` `limitThresholds(cpuLimitCores, cpuRequestCores)`. |
| **Birim formatlı eksen (OTLP unit metadata)** | **KISMEN** | `metric_points`/`metric_catalog`/`rollup_metrics_*` şemasında `unit LowCardinality(String)` **VAR** (`migrations/0003_rollup_metrics.sql:29`). FE haritası `lib/chart/metricUnit.ts:17-27` **yalnız 9 giriş**; bilinmeyen → `undefined` → ham sayı (dürüst ama eksik). `routeSeries.ts` ikinci yarım harita. |
| **Sıralı çok-serili tooltip** | **VAR** | `lib/chart/tooltipModel.ts` `sortedTooltipRows` + `capTooltipRows`; XSS kapısı `tooltipEscapeGate.test.ts`. |
| **Karşılaştırma modu (önceki dönem soluk çizgi)** | **KISMEN** | Kanal var: `corePanelEntry.tsx:70` `ghostItems` → `role:'muted'` + `dashed` + `" (önceki)"`. Tüketici **2**: `ServiceCharts.tsx:148-167` (`off\|24h\|7d\|prev`, localStorage'da) ve `explore/QueryPanel.tsx:178`. **Overview / Pod / Dashboard / Clusters'ta YOK.** `compare=prior` diye bir URL parametresi yok — seçim localStorage'da, yani **paylaşılan link karşılaştırmayı taşımıyor** (URL-as-source-of-truth ihlali). |
| **Chart'tan pivot** | **KISMEN** | bucket-tık → exemplar → trace: `CorePanel.tsx:218` `onBucketClick` (4 site) + `api.spanExemplar` (`ServiceCharts.tsx:449`). ◆ exemplar tık: `onExemplarClick` 3 site (Explore/PanelStack/QueryPanel). Heatmap hücre → `HeatmapCellExemplars.tsx:51`. **YOK:** span-türevli seriden trace LİSTESİNE (filtreli `/traces`) pivot — bugün yalnız TEK temsilci trace açılıyor. |
| **Tam ekran** | **VAR (yalnız hat A)** | `CorePanel.tsx:297,1119-1170` CSS overlay + `useEscLayer`. Hat B (TimeChart/TSP/OVC) **taşımıyor** → `/traces`, `/logs`, RuntimeCharts'ta tam ekran yok. |

---

## 5. Server-side downsample

### Bugün ne var (ölçüldü)

| Katman | Mekanizma | Kanıt |
|---|---|---|
| Metrik auto-step | `metricAutoStepPx(from,to,mdp)` — `rangeSec/mdp` → merdivene YUKARI yuvarla | `internal/chstore/metricresolve.go:196-214`; merdiven `:184` (20 basamak, 1s…86400s) |
| Metrik explicit-step tavanı | `clampExplicitStep` — bütçe `mdp` ya da `MetricPointBudget = 2000` | `metricresolve.go:750-777` (**v0.10.262**) |
| Metrik çözünürlük kelepçesi | `minMetricStepSec=10`, `maxMetricPoints=720` | `metricresolve.go:126-128` |
| Span-metrik step | `clampSpanMetricStep` — `mdp>0` → px-adaptif, yoksa sabit merdiven; her iki yolda bütçe tavanı (varsayılan **2000**) | `internal/chstore/spanmetric.go:944-988` |
| Span rollup planı | `PickRollup` — aile (dar/geniş) → kademe → step yuvarlama; `mdp` 0→1500, tavan 4000 | `internal/chstore/rollupselect.go:71-145` |
| Span rollup kademeleri | `narrow`: 10s/7g · 1m/14g · 5m/90g · 1h/396g; `wide`: 1m/5m/1h | `rollupselect.go:50-62` |
| Fast-path kademe | `pickNarrowRollupTier(step, from, now)` — step'i TAM BÖLEN en kaba kademe | `internal/chstore/rollup_fastpath.go:105-116` |
| Metrik rollup planı | `metricRollupPlan` — 1h/5m/1m kademeleri; cumulative ASLA, quantile YOK, `step<=0` → ham | `internal/chstore/metric_rollup_read.go:47-105` |
| Sparkline slot | `sparklineSlots` — `sparklineMaxSlots = 120`, 5dk katı | `internal/api/sparkline_slots.go:26-53` (**v0.10.262/269**) |
| Heatmap | sunucu tavanı 240 kova | `internal/chstore/heatmap.go:128` |
| Thanos | `stepForWindow` merdiveni, ≤480 nokta | `internal/thanos/client.go:1906-1928` |
| **LTTB** | **YALNIZ İSTEMCİDE** | `lib/perf/lttb.ts:88` `downsampleXY`; tüketiciler `Sparkline.tsx:174`, `viz/TimeSeriesPanel.tsx:245` (`MAX_POINTS=2000`), `LatencyScatter`. **Go tarafında `lttb` grep'i 0 sonuç.** |

**Yani "LTTB ile 2000 nokta üst sınırı" sunucuda YOK; sunucuda olan step-bucketing + 2000'lik bütçe tavanı.** İkisi aynı şey değil: bucketing kafesi kabalaştırır (tepe düzleşir), LTTB şekli korur.

### Açık delikler (öncelik sırası)

| # | Delik | Kanıt | Öneri |
|---|---|---|---|
| D1 | `/api/services/sparklines` + Endpoints/OperationsTable **sunucu-SABİT 120 kova**, px-kör | `internal/chstore/repo.go:1004` `SparklineBuckets = 120`; `sparkline_slots.go:26` | `maxSlots`'ı istekten al (bounded rung), FE `maxBarsForWidth` ile aynı bütçeyi konuşsun |
| D2 | Pod/infra CPU-Mem-Net **Thanos merdiveni**, panel genişliğini hiç görmüyor | `client.go:1906` | `deploy-trend`/`pods-trend` uçlarına `maxDataPoints` parametresi; `stepForWindow(from,to,mdp)` |
| D3 | `LogsExplorer` `windowSec/60` px-kör | `LogsExplorer.tsx:54` | `logsBucketSec` (zaten var) |
| D4 | `Dashboards.tsx:160` küçük resim `step: 60` sabit | — | küçük resim için sabit meşru; **belgelensin**, sessiz kalmasın |
| D5 | `CosreChart` `maxDataPoints: 300` sabit | `CosreChart.tsx:49` | ~560px kart için savunulabilir; kart genişliği değişirse kopar |
| D6 | Metrik yolu: `mdp` verilmezse `metricAutoStep` merdiveni → 30g penceresi 720 nokta | `metricresolve.go:197` + `:126-128` | `clampMetricStep` zaten kapatıyor; **auto yolda LTTB gerekmiyor** |
| D7 | **LTTB'nin gerçekten gerektiği tek yer**: quantile serileri (rollup'ta yok, ham okunur) ve `rateWindow` kayan pencere | `spanmetric.go:906-931` | Dilim 1 kapsamı DIŞI — ölçülmeden eklenmemeli |

**Hangi uçlara uygulanacak (Dilim 1):** `deploy-trend` + `pods-trend` (D2, pod sayfası ilk taşınacağı için **bloklayıcı**) → `services/sparklines` (D1) → `logs/timeseries` (D3). `spans/metric-batch` ve `metrics/query` **zaten bütçeli** — dokunulmaz.

---

## 6. Chart bileşeni API tasarımı (taslak — İMPLEMENTE EDİLMEYECEK)

Hedef: `CorePanelProps`'un 30 alanını **beş gruba** indirmek, dilim dilim. Yeni bir beşinci bileşen YOK — bu `CorePanelProps`'un yeniden gruplanmasıdır.

```ts
// components/chart/CorePanel.tsx — hedef şekil
export interface CorePanelProps {
  // ── kimlik ────────────────────────────────────────────────
  title: string;
  storageKey: string;                 // lejant katlanma + kolon genişlikleri
  height?: number;

  // ── veri (discriminated union KORUNUR) ───────────────────
  data: PanelData;                    // loading | error | empty{reason,hint} | ready{frames,partial}

  // ── seri sunumu ──────────────────────────────────────────
  series?: {
    roles?: SeriesRole[];             // seriesRole.ts — etiketten TAHMİN EDİLMEZ
    dashed?: boolean[];
    defaultHidden?: string[];
    viz?: PanelViz;                   // 'line'|'bars'|'area'|'stacked'|'stacked-bars'
    perSeriesViz?: PanelViz[];        // ⟵ YENİ (B5): TimeChart paritesi
    axis?: ('left' | 'right')[];      // ⟵ YENİ (B6): dual-y paritesi
  };

  // ── eksen / birim ────────────────────────────────────────
  unit?: string;                      // UCUM; metricUnit.ts tek kapı
  logScale?: boolean;
  xRange?: { from: number; to: number } | null;
  connectNulls?: number;              // ms eşiği — B2: ya tüketici bul ya SİL

  // ── overlay ──────────────────────────────────────────────
  overlays?: {
    thresholds?: ChartThreshold[];
    regions?: ChartTimeRegion[];      // annotation/problem penceresi
    exemplars?: (ChartExemplar[] | undefined)[];
    bands?: { above: number; below: number; fill?: string }[]; // B2 aynı hüküm
  };

  // ── etkileşim ────────────────────────────────────────────
  syncKey?: string;                   // uPlot.sync — ad alanı §7 D1.3'te tekleşir
  onZoom?: (fromSec: number, toSec: number) => void;
  onZoomReset?: () => void;
  onPointClick?: (p: { timeSec: number; seriesIdx: number; value: number | null }) => void; // ⟵ YENİ
  onBucketClick?: (fromNs: number, toNs: number) => void;
  onExemplarClick?: (traceId: string) => void;
  onRegionClick?: (r: ChartTimeRegion) => void;
  onCursorTime?: (timeSec: number | null) => void;
  onExpandClick?: () => void;

  // ── kabuk ────────────────────────────────────────────────
  legend?: { hidden?: boolean; hiddenNames?: Set<string>; focusedLabel?: string | null };
  menuExtra?: PanelMenuAction[];      // panelMenu.ts sözleşmesi — ReactNode DEĞİL
  note?: string | null;
  queryText?: string;
}
```

**Sözleşme kuralları (hepsi mevcut kodun dersleri, korunmalı):**
- Nesne/dizi prop'lar config bağımlılık dizisine **kimlikle giremez** → `overlaySig` deseni (`CorePanel.tsx:441-444`).
- Yeni prop **tüketicisiyle birlikte** iner. B2'nin dört ölü kanalı bu kuralın ihlali; taslak onları koruyor ama §7 Dilim 1 karar veriyor: tüketici yoksa **silinir**.
- Boş durum **sebep taşır** (`PanelData` `empty{reason,hint}` — `CorePanel.tsx:113-121`); `Spinner`/`Empty` `components/Spinner.tsx`'ten (design-system kanonik).
- Tema: token dışına renk yok; canvas fallback hex **token'ın gerçek değeriyle** eşleşecek.

---

## 7. Dilimleme, risk, dosya planı, test

### Dilim 0 — SÖZLEŞME (yarım gün, bloklayıcı, kod değil konfigürasyon)
`frontend/package.json:20-21` `^13.1.2` → `13.1.2` tam pin (ya da `overrides`). Gerekçe §3. Bu yapılmadan hiçbir dilim güvenli değil.

### Dilim 1 — Chart bileşeni + crosshair/zoom/legend/tooltip/eksen + server-side downsample; **pilot: `/pod`**

| Adım | Dosyalar | Risk |
|---|---|---|
| 1.1 Ölü prop kararı (`bands`,`connectNulls`,`headerExtra`,`logScaleToggle`) | `CorePanel.tsx:151,159,167,176` | Düşük — 0 tüketici |
| 1.2 Props gruplama (§6), eski düz prop'lar bir sürüm boyunca **kabul edilmez** (CLAUDE.md: şim yok) | `CorePanel.tsx`, `corePanelEntry.tsx`, 13 `MultiLineChart` tüketicisi | **Orta-yüksek** — blast radius 17 JSX |
| 1.3 `syncKey` ad alanı tekleşir (`-ms` son eki kalkar; grup adı tek yerden üretilir) | `MultiLineChart.tsx:178`, `lib/chart/cursorOpts.ts` | Orta — 46 site; **sessiz kayıp riski** (sync registry modül-düzeyi) |
| 1.4 Lejant tekleşmesi: `StatsLegend` kanonik, CP iç lejantı ona döner | `chart/StatsLegend.tsx`, `CorePanel.tsx` | Orta |
| 1.5 Birim haritası tekleşmesi (AS-4) | `lib/chart/metricUnit.ts`, `service/charts/routeSeries.ts` | Düşük — saf |
| 1.6 **Sunucu bütçesi:** `deploy-trend`/`pods-trend` `maxDataPoints` (D2) | `internal/thanos/client.go:1906`, `internal/api/thanos_handlers.go:427-470` | Orta — **cache key'e girer** (v0.5.187 kuralı: rung'a snap, sınırlı kardinalite) |
| 1.7 `/api/services/sparklines` `maxSlots` (D1) | `internal/api/sparkline_slots.go:26` | Düşük |
| 1.8 `/pod` pilotu: `PodResourceCharts`/`PodInlineResourceCharts` yeni API'ye | `pages/Pod.tsx`, `pages/service/PodResourceCharts.tsx`, `pages/clusters/MetricArea.tsx` | Orta |

### Dilim 2 — annotation + eşik çizgileri + karşılaştırma + pivot
- 2.1 `AnnotationLane`'i Pod/Dashboard/Explore'a yay; `regions=`'ın 18 elle-kurulan sitesini şeride devret (`ServiceAnnotationLane.tsx` emsali).
- 2.2 Eşikler config'ten: `dashboard/PanelEditor.tsx:541,594` editörü ↔ `clusters/TrendPanel.tsx:135` request/limit deseni tek kapıya.
- 2.3 Karşılaştırma: `ghostItems` kanalı Overview/Pod'a; seçim **URL'e** (`?compare=prior|24h|7d`) — bugün `localStorage` (`ServiceCharts.tsx:148-152`), yani link taşımıyor.
- 2.4 Pivot: `onPointClick` → filtreli `/traces` LİSTESİ (bugün yalnız tek exemplar).
- 2.5 Hat B'nin emekliliği (VolumeChart/LogsHistogram → CP) — **`perSeriesViz` + dual-y (B5/B6) olmadan yapılamaz**, o yüzden Dilim 1'den sonra.

### Risk listesi
1. **uPlot çift kopya — sessiz.** `package.json:40` `^1.6.32` caret vs `@grafana/ui` **exact `1.6.32`**. Drift olursa `uPlot.sync` registry'si modül düzeyinde ikiye bölünür; **hata yok, uyarı yok, imleç sadece geçmez**. `vite.config.ts` `id.includes('uplot')` substring kuralı iki gövdeyi aynı chunk'a koyar → bundle analizinde de görünmez. → `overrides: {"uplot":"1.6.32"}`.
2. **`framesToAligned` sameGrid** (B7) — kafes değişikliği getiren her step değişimi bu latent hatayı tetikleyebilir; Dilim 1.6 tam olarak step değiştiriyor.
3. **Cache key kirlenmesi** — yeni `maxDataPoints`/`maxSlots` parametreleri `s.serveCached` anahtarına **tüm girdileriyle** girmeli ve rung-kuantalı olmalı (`chartStep.quantizeWidth` emsali).
4. **`themeTick` rebuild'i kaybolmamalı** — canvas CSS var okuyamaz; opts cache'lenirse tema flip'inde renkler bayatlar (v0.8.531 bug'ı).
5. **`setData` görünürlük koruması** — isolate seçimi poll'de hayatta kalmalı; visibility yalnız rebuild'de resetlenir.
6. **Ad alanı değişimi (1.3) geri alınamaz şekilde sessiz** — bu adım kendi release'inde, kendi testiyle.

### Test planı

| Katman | Bugün | Dilim 1'de eklenecek |
|---|---|---|
| Saf çekirdek | `lib/chart/` **30 modül / 24 test** (`legendStats`, `legendVisibility`, `tooltipModel`, `tooltipPin`, `overlays`, `stacking`, `xRange`, `zoomHistory`, `axisSize`, `metricUnit`, `bucketWindow`…) | `metricUnit` birleşik harita tablosu; `syncKey` ad üreteci tablo testi |
| Kaynak-sözleşme kapıları | `components/chart/corePanelContracts.test.ts` — `@grafana/ui` tekeli `:767`, `@grafana/schema` yasağı `:150`, ham-veri kanalı `:217`, bar tabanı `:361` | ölü prop kaldırma kapısı; props gruplama kapısı |
| **jsdom + uPlot canvas sınırı** | **Çözülmüş:** `CorePanel.smoke.test.tsx` — `// @vitest-environment jsdom`, `vi.hoisted` ile `window.matchMedia` + `localStorage` shim'i (uPlot ve `@grafana/ui` logger'ı **modül yüklenirken** çağırıyor), `vi.mock('@grafana/ui')` **yalnız `UPlotChart`'ı** stub'lar; `UPlotConfigBuilder` **orijinal** kalır ve stub `config.getConfig()`'i gerçekten çağırır → `addScale/addAxis/addSeries/addBand` zinciri patlarsa test kızarır. Y-aralık fonksiyonu sahte `u` ile **çalıştırılır** (`:368`). | Aynı desen **hat B için yok** — `TimeChart`/`TimeSeriesPanel`/`OverviewChart`'ın hiç render testi yok. Dilim 2'de hat B emekli olacağı için **yeni test yazma, göç testini yaz**: aynı prop → CP çıktısı ile TC çıktısının seri/eksen sayısı eşitliği |
| Go tablo testi (downsample) | `rollupselect_test.go`, `metricstep_test.go`, `spanmetric_step_test.go`, `sparkline_slots_test.go`, `metric_rollup_read_test.go` | `stepForWindow(from,to,mdp)` tablo testi (Thanos, D2); `sparklineSlotWidth` maxSlots parametrik; **`route_pins_test.go` FE↔BE merdiven çivisi genişletilir** |
| Parite koşumu | `cmd/paritycheck` + `make parity` (`docs/charts/parity-report.md`) | Dilim 1.6 sonrası **yeniden koşulmalı** — step değişimi gauge paritesini bozmamalı |
| **Görsel regresyon** | **YOK.** `frontend/package.json`'da Storybook / Chromatic / Playwright **yok** (`grep` doğrulandı); 388 vitest dosyası var | **Storybook önerilmiyor** — yeni build zinciri + yeni bağımlılık ağacı, "ağır kütüphane yasak" ruhuna aykırı. Yerine: (a) `CorePanel.smoke.test.tsx` deseninin genişletilmesi — stub'lanan `getConfig()` çıktısı **snapshot**'lanır (seri/eksen/scale/hook anahtarları), yani "çizilen doğru mu" değil ama "config kaydı" pinlenir; (b) `alignedToCsv` (`lib/chart/exportCsv.ts`) üzerinden **veri snapshot'ı** — çizilen `AlignedData` tam hassasiyetle CSV'ye dökülüyor, tablo testine birebir uygun |

### Kapılar (her dilim, istisnasız)
```
cd frontend && npx tsc --noEmit && npm run lint && npm run test -- --run
go build ./... && go test ./... && make audit
```

---

## Kapanış

- **Mimari sağlam, borç ÇOKLUK.** İki çizim hattı (CP + `engine.ts`), iki lejant, iki tooltip HTML'i, iki formatter, iki birim haritası, iki `syncKey` ad alanı.
- **En acil kalem kod değil, bir caret:** `@grafana/ui` `^13.1.2` → npm `latest` **13.2.1** ve o sürüm **React ≥19** istiyor. Dilim 0.
- **`@grafana/ui`'den yeni bileşen alınmamalı.** `TooltipPlugin2`/`VizLegend` bizde zaten daha zengin karşılıklarına sahip; threshold editörü pakette yok. Sınır bugünkü hâliyle (2 dosya, CI kapılı) doğru çizilmiş.
- **Server-side LTTB Go'da yok** ve Dilim 1 için gerekmiyor; asıl açık, üç ucun **piksel bütçesini hiç görmemesi** (Thanos trendleri, sparkline grid'i, LogsExplorer) — pod sayfası pilotunun bloklayıcısı bu.

## Onay soruları

1. Dilim 0 (caret → tam pin + `overrides: {uplot: "1.6.32"}`) hemen, ayrı sürüm — kabul mü?
2. `@grafana/ui`'den YENİ bileşen alınmaması (Storybook de eklenmemesi) — kabul mü?
3. Dilim 1'de "server-side LTTB" yerine üç ucun piksel bütçesi (Thanos `maxDataPoints`, sparkline `maxSlots`, LogsExplorer `logsBucketSec`) — kabul mü?
4. Props gruplama (1.2) 17 JSX'e dokunuyor; bunu 1.8 pod pilotundan ÖNCE mi, pilotla birlikte mi?

---

## Durum (2026-09-03)

Dilim 0 GEMİDE (v0.10.283: `@grafana/*` + `uplot` tam pin, `overrides.uplot`, sözleşme testi). Dilim 1: 1.1 ölü prop'lar silindi (284, +285 test fix-forward) · 1.7 sparkline `?maxSlots=` (286) · 1.6 Thanos `?maxDataPoints=` + `stepForWindowMDP` (287) · 1.5 birim haritası tek sözlük (288) · 1.3 `msSyncKey` tek üretici, ek KALDIRILMADI (hat B yaşadıkça şart) (289) · 1.4 `PanelLegend.tsx` — kanonik lejant PanelLegend, StatsLegend hat B ile emekli (audit'ten gerekçeli sapma) (290) · 1.8 /pod bütçe (291). **1.2 props gruplama AÇIK** — Onay sorusu 4 (pilottan önce mi / birlikte mi) operatör kararı; brief "tek seferde büyük yeniden yazım yok". Canlı: sparkline 7g maxSlots 40→37 / 80→73 / 120→97 slot; pods-trend 6h mdp 120→73 / 240→181 / 480→361 nokta. B7 riski: `make parity` prod'da tekrarlanmalı.
