# Dış skill denetimi — 2026-09-05

Operatör isteği: skills.sh'ten kurulan resmi skill'ler (ClickHouse ×3, Grafana
×3, Vercel ×2, Anthropic ×2) Coremetry'ye ne katar? Dört salt-okunur denetim
ajanı her skill'i yükleyip repoya uyguladı; repo sözleşmelerinde (CLAUDE.md,
`.claude/skills/*`, DECISIONS, INCIDENTS) zaten kabul edilmiş kararlar bulgu
sayılmadı. Her bulgu dosya:satır kanıtlı; uygulanmadan önce ±10 satır bağlam
okunur (`feedback-audit-verify-context`).

Sıralama ilkesi: yanlış sayı > eksik veri > maliyet/perf > erişilebilirlik >
cila. Tahminler: ~10 dk / ~30 dk / ~1 saat / ~2 saat / ~yarım gün.

## A. Yanlış sayı üreten bulgular (önce bunlar)

| # | Bulgu | Kanıt | Fix | Tahmin |
|---|---|---|---|---|
| A1 | **GEMİDE v0.10.376** — JVM GC penceresi ×2: `step=win` ile 2 nokta döner, ilk noktanın `increase`'i pencere DIŞI; `seriesWindowTotal` ikisini toplar | `internal/vmetrics/runtime_pods.go:69,126` | `lastValue` (capacity.go:57 kalıbı) | ~10 dk |
| A2 | Yığılmış alanda karşılaştırma hayaleti yığına katılıyor → pod throughput ~2× | `components/chart/corePanelEntry.tsx:130-139`, `lib/chart/stacking.ts:34` | `stackData`'ya `excludeIdx`; hayalet ham çizgi | ~2 saat |
| A3 | Yüzdelik dalında `vmrange` korumasız: exponential histogramda `le=""` tek kova → sessiz yanlış p95 | `internal/vmetrics/promql.go:741,1211` | sonuçta `le` yoksa `vmrange` / not | ~yarım gün |
| A4 | `attrInt` yalnız IntValue: string/double `http.status_code` → 0, 5xx sınıflandırması eksik | `internal/otlp/convert.go:554` | string/double dalları + test tip ekseni | ~1 saat |
| A5 | ~~`MetricPresentKeys` RTMetric, rate sorgusu Metric okuyor~~ — **bulgu değil**: iki ad aynı ailenin (`_count` ↔ taban) etiketlerini paylaşır, `labelNames` discovery adaylarıyla çözer | `internal/api/service_metric_red.go:157-177` | ikisi de `id.RTMetric` | ~10 dk |
| A6 | VM uç-damgası kaynakta değil 3 çağrı yerinde telafi ediliyor; diğer tüketicilerde x ekseni kayık (7g'de ~34 dk) | `internal/vmetrics/throughput.go:107`, `endpoints_metric.go:439`, `hosts_metric.go:314`, `capacity.go:205` | kaymayı `runRangeQuery`'de bir kez uygula, 3 telafiyi kaldır | ~yarım gün |
| A7 | Sparkline'da "trafik yok" ile "%0 hata" aynı: null yerine 0 | `pages/Services.tsx:756,871` | `(number\|null)[]`, null'da path kes | ~2 saat |
| A8 | Eşik çizgisi y-ölçeği dışındaysa sessizce yok (alarm önizlemesi, pod CPU limiti) | `lib/chart/overlays.ts:63`, `CorePanel.tsx:815` | eşiği `softMin/softMax`'a kat, sığmazsa kenar işareti | ~2 saat |
| A9 | Yığılmış alanda sıfır taban yok — **v0.9.811 sözleşmesiyle çelişir** (`CorePanel.smoke.test`: "area ve stacked dokunulmadan kalır"); operatör kararı olmadan uygulanmaz | `components/chart/CorePanel.tsx:634` | `(bars \|\| stacked) ? 0` | ~10 dk |
| A10 | Pasta/yığın `isAdditiveUnit`'e sormuyor ("p99'un payı %31") | `pages/explore/SummaryViz.tsx:56` | rail düğmelerini kapıla, top-6+diğer | ~2 saat |
| A11 | `http.target` ham hâliyle LowCardinality `http_route`'a (query string dahil) | `internal/otlp/convert.go:142`, `store.go:1031` | `NormalizePathTemplate`'ten geçir | ~1 saat |
| A12 | RED paneli `rateWindow=180` varsayılanı Settings tabanını (300 s) atlıyor; 120 s export'ta delikli rate | `internal/api/service_metric_red.go:274`, `promql.go:1088` | varsayılan 0 ya da `max(180, taban)` — **davranış değişikliği, sorulur** | ~10 dk |

## B. Eksik/sessiz düşen veri

| # | Bulgu | Kanıt | Fix | Tahmin |
|---|---|---|---|---|
| B1 | Summary quantile'ları sessizce düşüyor (yalnız avg) | `internal/otlp/convert.go:403-412` | quantile'ları attr'a yaz ya da sayaç | ~2 saat |
| B2 | Exponential histogram 4 dalda sessiz degrade, sayaç yok | `internal/otlp/exp_histogram.go:41-51` | sebep etiketli sayaç + /admin/stats | ~1 saat |
| B3 | Delta temporality VM forward'da bakılmıyor (VM rate kümülatif varsayar) | `internal/otlp/forward_filter.go`, `convert.go:345` | delta Sum sayacı + Settings uyarısı | ~2 saat |
| B4 | `usePrometheusNaming` yazımda doğrulanmıyor; yanlış bayrakta her VM paneli boş | `internal/vmetrics/write.go:41` | Test() probe + rozet | ~yarım gün |
| B5 | `anyValStr` bilinmeyen tipte sessiz `""` | `internal/otlp/convert.go:538` | rate-limited sayaç | ~30 dk |
| B6 | `async_insert_deduplicate` Distributed sarmalayıcıda etkisiz; MV kaskadı dedup dışı (v0.10.240 çift-span açık) | `internal/chstore/repo.go:85` | `insert_deduplication_token` + `deduplicate_blocks_in_dependent_materialized_views` | ~1 saat, kademeli roll |

## C. Maliyet / performans (ClickHouse + React)

| # | Bulgu | Kanıt | Fix | Tahmin |
|---|---|---|---|---|
| C1 | Sunucu `max_execution_time` (60/180 s) istemci `ReadTimeout` (30 s)'un 2-6× — istemci kopar, sunucu yakmaya devam eder | `store.go:654-659`, `topology.go:660,1133` | bütçeleri ≤25 s; uzun işler ayrı bağlantı | ~1 saat |
| C2 | spans/logs/metric_points `ttl_only_drop_parts` yok (rollup'larda var) | `store.go:1051,1073,1923` | MODIFY SETTING | ~30 dk |
| C3 | `metric_points` skip index yok; `service_name` opsiyonelken PK budamaz | `store.go:1921`, `metricrate.go:354` | `set(0)` index on `metric` | ~30 dk |
| C4 | `logs` env/pod filtresi dizi taraması; 0014 yalnız spans'e | `repo.go:4750-4764` | `res_kvh` + bloom logs_local'a | ~1 saat |
| C5 | `GetLogs` sınırsız `count()` her sayfada | `repo.go:5045` | traces'teki tavanlı count | ~30 dk |
| C6 | `parallel_view_processing=1` 19 MV'de tepe belleği çarpıyor (spool olayı) | `repo.go:90` | `parallel_view_processing_max_threads`, ölç | ~2 saat |
| C7 | `logs.attr_values/res_values` kodeksiz (spans ZSTD(3)) | `store.go:1063-1065` | MODIFY COLUMN CODEC | ~30 dk |
| C8 | Traces'te her tuş vuruşu 1370 satırlık gövde + memo'suz `AggregateTable` (200 satır) | `pages/Traces.tsx:318,1262,1636` | `memo` + `useDeferredValue` | ~3 saat |
| C9 | 112 effect-fetch'in 67'si yarış korumasız (216 `useQuery` varken) | ör. `AnomaliesPage.tsx:302`, Traces 7, PanelRenderer 7 | `useQuery`'ye taşı (ilk dilim) | ~yarım gün |
| C10 | `DataTableColgroup` fit memo'su 20 sitede hiç isabet etmiyor (satır içi dizi) | `DataTable.tsx:448-459` | sabit diziler / primitif dep | ~30 dk |
| C11 | **GEMİDE v0.10.377** — Endpoints satır anahtarı `rowKey\|i` → sıralamada tüm tbody remount | `pages/Endpoints.tsx:747` | `key={rowKey}` | ~10 dk |
| C12 | Traces 31 useState / 0 useCallback; `startResize` her pointermove'da yeniden | `Traces.tsx`, `DataTable.tsx:224-243` | reducer + useCallback; ref | ~yarım gün |

## D. Erişilebilirlik / tasarım

| # | Bulgu | Kanıt | Fix | Tahmin |
|---|---|---|---|---|
| D1 | `--text3` dark 2.90:1, light 3.24:1 (AA 4.5), 1250 kullanım | `styles/globals.css:38,205` | iki token değeri — **görsel değişiklik, sorulur** | ~10 dk |
| D2 | `Drawer` role/aria-modal yok, Tab hapsi yok (Modal'da var) | `components/ui/Drawer.tsx:103` | ortak `useFocusTrap` | ~yarım gün |
| D3 | 42 tıklanabilir `<tr>`'nin 37'si klavyeye kapalı; `getRowHref` 0 tüketici | `pages/Traces.tsx:1656`, `DataTable.tsx:81` | AnomaliesPage kalıbı / row-link | ~yarım gün |
| D4 | `CommandPalette` diyalog değil; ham renkler | `components/CommandPalette.tsx:553-583` | Modal kabuğu + listbox + token | ~2-3 saat |
| D5 | 18 sekme şeridi, 0 `role="tablist"` | `.tab-strip` siteleri | `TabStrip` atomu | ~3-4 saat |
| D6 | **GEMİDE v0.10.378** — `FlashBox` aria-live yok — ayar kaydı ekran okuyucuya ulaşmıyor | `pages/settings/shared.tsx:40` | `role=status/alert` | ~10 dk |
| D7 | Palet `--ok/--warn` hex'leriyle çakışıyor; renk körlüğünde ~3 küme; 8 seride çakışma ~%98 | `lib/chartFmt.ts:135-159` | Okabe-Ito/8 + panel-içi slot | ~yarım gün |
| D8 | LatencyHeatmap tema-kör, lejantsız, çok günlü eksende tarih yok, `--warn` fallback yanlış | `components/LatencyHeatmap.tsx:28-33,264,278,468` | tek-hue rampa + useThemeTick + lejant | ~yarım gün |
| D9 | Dashboard birim alanı serbest metin; karo/eksen iki sözlük | `components/dashboard/PanelEditor.tsx:386`, `PanelRenderer.tsx:1163` | `<select>` + normalizasyon | ~3 saat |

## Zaten iyi olan (dört ajan da övdü)
- MV disiplini (tDigest, fail-closed rollup seçici), Nullable sıfır, LC kararı ölçüme bağlı.
- OTLP kabul sözleşmesi HTTP/gRPC simetrik (PartialSuccess, 429+Retry-After, 32 MiB).
- Rota bölme + chunk stratejisi; `Modal` odak sözleşmesi; reduced-motion global.
- `@grafana/*` sınırı 2 dosya; panel dört-durum sözleşmesi; eksen/tooltip/lejant tek processor.

## Önerilen ilk dilim (~1 gün)
A1, A5, A9, C11, D6 (dakikalık, davranış değiştirmez) → A4, A11, C2, C3, C5, C7 (CH/OTel, birer saat) → A2, A8, A7 (grafik doğruluğu). Sorulacaklar: A12 (rate penceresi varsayılanı), D1 (kontrast token'ı), B6 (ingest yolu, kademeli roll).
