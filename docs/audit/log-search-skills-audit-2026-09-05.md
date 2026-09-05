# Log arama denetimi (dış skill'ler) — 2026-09-05

Operatör: "elastic/kibana log search için skill'leri bul … daha iyi bir log search ve gösterim
deneyimi" → kurulan resmi skill'ler (Elastic: esql, query-optimization, kibana-dashboards,
sre-triage; Grafana: loki) üç salt-okunur ajanla /logs hattına uygulandı. Önceki kararlar
(docs/audit/log-search.md, docs/plans/kibana-logs-parity-2026-08-21.md, v0.9.1215-1220,
v0.10.279-310) bulgu sayılmadı. Her bulgu dosya:satır kanıtlı; uygulamadan önce ±10 satır
bağlam okunur.

## A. ES sorgu maliyeti / doğruluk (elasticsearch-query-optimization + esql)

| # | Bulgu | Kanıt | Fix | Tahmin |
|---|---|---|---|---|
| A1 🔴 | **GEMİDE v0.10.412** — `CountPatterns` alt-sorgularında zaman yüklemi yok; pencere yalnız filter agg'de → token taraması tüm saklama süresinde (rollover/tarihsiz indekslerde daraltma kesmez) | `logstore/elasticsearch.go:2327-2336`, `es_indices.go:133,159` | gövdeye `bool.filter` range (baseFrom…curEnd) | ~20 dk |
| A2 🔴 | Agg tarafı `.keyword`'e çakılı; filtre iki mapping şeklini öğrendi, agg öğrenmedi → ECS/managed mapping'de severity yığını OTHER, servis kırılımı boş | `elasticsearch.go:1723,1864,2350`; teşhis `es_group_fields.go:20-26` | `resolveGroupAggFields`/field_caps üzerinden | ~yarım gün |
| A3 🟠 | `buildQuery` her şeyi `must`'a koyuyor (zaman, service, term, exists, severity) — filter context yok | `elasticsearch.go:2447,2745` | serbest metin dışını `bool.filter`'a; altın-gövde testi | ~1 saat |
| A4 🟠 | `/api/logs/patterns` 4 ardışık tam-`_source` PIT sayfası (2000 doküman) — yalnız `body` okunuyor | `patterns.go:83`, `elasticsearch.go:1428-1453` | `Filter.SourceFields` (es_rawsearch emsali) ya da ES\|QL `CATEGORIZE` (Platinum, fallback şart) | ~2 saat / ~yarım gün |
| A5 🟡 | **GEMİDE v0.10.413** — FieldStats zarfı `esSearchEnvelope`'u gömmüyor; bekçi testi yalnız `var raw struct` + tek dosya tarıyor | `elasticsearch.go:1123`, `es_envelope_test.go:135-143` | embed + bekçiyi `es_*.go` ve `raw\|decoded`'a genişlet | ~20 dk |
| A6 🟡 | Pencere ms hassasiyetli → `request_cache`/`serveCached` paylaşılmıyor | `api_logs.go:396`, `frontend/src/lib/utils.ts:21-24`; doğru emsal `es_trace_context.go:47` | handler'da `to`'yu 10 dk'ya truncate | ~1 saat |
| A7 🟡 | **GEMİDE v0.10.414** — `/logs/context` iki Search ardışık (yorum "parallel"), her yarı `track_total_hits:10000` + tam `_source` | `api_logs.go:771,869,876` | errgroup + CountMode=false | ~30 dk |

## B. Arama/gösterim deneyimi (Kibana Discover + Loki Drilldown)

| # | Bulgu | Kanıt | Fix | Tahmin |
|---|---|---|---|---|
| B1 | **GEMİDE v0.10.415** — Ana liste `degraded` bayrağını okumuyor → yavaş ES "No logs found" görünüyor (FieldsPanel/ContextModal aynı) | `Logs.tsx` grep 0; `api_logs.go:448-454,1049` | `:970` partial rozetinin ikizi, üç yüzey | ~30 dk |
| B2 | Live tail traceId/spanId'yi SSE URL'ine yazmıyor; "Filtered to trace" şeridi yalan | `Logs.tsx:544-553,788`; BE okuyor `api_logs.go:122` | iki `p.set` + pin | ~10 dk |
| B3 | Göreli pencere donuyor; sayfa-yerel ↻ yok; Search yeniden fetch atmıyor | `Logs.tsx:409`, `queries/logs.ts:31` | ↻ + `nowTick` + "veri: HH:mm:ss" | ~30 dk |
| B4 | Filter-for/out yalnız pod hücresinde; service/cluster/attr hücrelerinde yok | `LogTable.tsx:437,450,458-500` | `.lt-pivot` kalıbı | ~1-2 saat |
| B5 | Wrap/truncate anahtarı yok (`.td-full` hazır) | `globals.css:1277,1286`, `LogTable.tsx:411` | toolbar toggle + localStorage | ~20 dk |
| B6 | Desenler: exclude yok, "Ara" mevcut serbest metni eziyor | `Logs.tsx:191-194`, `LogPatternsPanel.tsx:146,163` | ⊕/⊖; ⊖ `NOT`; metin korunur | ~1-2 saat |
| B7 | Efektif sorgu görünmüyor/kopyalanmıyor | `compileSearch` 12 kullanım, 0 render | pill barı sonuna `<code>` + CopyButton | ~20 dk |
| B8 | Histogram kırılım serisinden filtreye geçiş yok | `LogsHistogram.tsx:343`, TimeChart legend tık 0 | `onSeriesPick` → `toggleFilter`; OTHER tıklanamaz | ~yarım gün |

## C. Problem → log triage hunisi (observability-sre-triage)

| # | Bulgu | Kanıt | Fix | Tahmin |
|---|---|---|---|---|
| C1 | Problem panelinde log kanıtı toplanıyor ama çizilmiyor (`templates?: unknown[]`) | `types.ts:914`, `RootCausePanel.tsx:282` | "Log kanıtı" Section + `logsHref(q: PatternSearchQuery)` | ~2 saat |
| C2 | Problem→Logs pivotu hata süzgeci taşımıyor (trace linki taşıyor) | `ProblemDetail.tsx:717,932`, `InboxTriageDrawer.tsx:150` | `severity: 17` + "tüm loglar" linki | ~10 dk |
| C3 | `familyLogs` kanıtı filo sıralı + ömür-boyu sayım (Service alanı geçilmiyor, TotalCount kümülatif) | `anomaly/investigation.go:152,408`, `log_templates.go:17` | `Service` geç + pencere sayımı (`GroupBySignature`) | ~yarım gün |
| C4 | Desenler örneklemesi en-yeni-2000; panel kapsanan alt pencereyi söylemiyor | `patterns.go:75`, `LogPatternsPanel.tsx:122` | `coveredFromNs/ToNs` + altbilgi | ~1 saat |
| C5 | "Hatalıyı ayıran alan" log tarafında yok (BubbleUp yalnız span) | `bubbleup.go:86`, `api_logs.go:914` | fieldstats `baseline=1` + lift rozeti | ~yarım gün |
| C6 | Desenler'de trend/yeni-mi yok (anomaly/log_patterns.go hesaplıyor) | `patterns.go:35-50`, `anomaly/log_patterns.go:193` | `Ratio/New` + Δ sütunu + YENİ rozeti | ~yarım gün |
| C7 | "✨ Desenleri anlat" filtre/pencereyi yok sayıyor (servissiz 24 sa, tavan 30 dk; `windowSec` atılıyor) | `log_patterns_explain.go:33-38`, `Logs.tsx:202` | service/cluster/env/search kanıta; `windowSec` başlıkta | ~yarım gün |
| C8 | Desenler paneli URL'de değil (localStorage) → Problem linki desene inemiyor | `Logs.tsx:189` | `?panel=patterns\|templates` → `logsUrl.ts` | ~1 saat |

## Zaten iyi olan
- `_msearch` toplu tik, `min_doc_count:1`, `track_total_hits:false` + soft timeout, PIT fallback; `es_rawsearch.go` dar `_source` emsali.
- Dürüstlük zarfı Kibana'yı aşıyor (partial, shardsFailed, envUnapplied, hasTraceUnapplied, capped, düşen satır sayacı); ES-maliyet disiplini yapısal; URL tek kaynak + `logsHref` pencereyi tip düzeyinde zorunlu kılıyor.
- Tek imza (`logstore.NormalizeSignature`), deep-link kapısı (`traceLogsLinkGate`), degrade sözleşmesi.

## Önerilen ilk dilim
Dakikalık: A1 (zaman yüklemi), A5, A7, B1 (degraded bandı), B2, B5, B7, C2. Sonra: A3 (filter context), A6, B3, C1 (log kanıtı paneli), C4, C8. Yarım gün sınıfı: A2, A4, B4, B6, B8, C3, C5, C6, C7.
