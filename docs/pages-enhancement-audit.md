# Endpoints / Database / Messaging — Dynatrace-Style Enhancement Audit (Stage 1)

2026-07-07. Üç paralel keşif; her bulgu file:line'a dayanıyor. Kod değişikliği
YOK — Stage 2 bu dokümanın onayıyla başlar. ⚠ Operatör brief'i Endpoints
bölümünün ortasında kesildi; Database + Messaging hedef bölümleri hiç
ulaşmadı. O sayfaların hedefleri Dynatrace standardından TÜRETİLDİ ve
**[INFERRED]** işaretli — onayda düzeltilebilir.

## Yönetici özeti

Üç sayfanın da iskeleti sağlam (useDataTable, MV'ler, drawer'lar, sparkline
primitifleri) ve bazı "hedef" yetenekler zaten var (Endpoints'te
`compare=prior` delta rozetleri; Messaging'de publisher/consumer kırılımı;
DB'de instance-bazlı RED trendleri). Gerçek açıklar dört sınıfta toplanıyor:

1. **MV-first ihlalleri:** Endpoints tablosu ve SlowQueries kataloğu RAW
   spans okuyor (billion-span ölçeğinde tek bounded-scan'ler). Endpoints
   için hazır çözüm var: `spanmetrics_1m` MV'si `http_route` boyutunu ZATEN
   taşıyor (store.go:1994-1998).
2. **Kimlik eksikleri:** normalize SQL statement'ın kalıcı kimliği yok
   (regex her sorguda yeniden; trend/karşılaştırma/SLO bağlanamaz);
   endpoint detayı route-scoped değil servis-scoped.
3. **Pivot kopuklukları:** v0.8.328-335 pivot altyapısı (exemplar,
   span_links, linked-traces) yalnız /trace'e bağlı — üç sayfanın hiçbiri
   kullanmıyor. Messaging için producer↔consumer span-link korelasyonu
   hazır bekleyen en değerli varlık.
4. **Karşılaştırma tekilliği:** `compare=prior` yalnız Endpoints'te;
   DB/Messaging'de dönem karşılaştırması yok.

## 1. ENDPOINTS — mevcut durum + boşluklar

Mevcut: ENDPOINT_COLS (Endpoints.tsx:50-63) Calls/Errors/Err%/Status
(2xx-5xx pill)/Avg/P99/Trend sparkline/Traces + gizli Impact; client sort
(server yalnız calls DESC top-N → global sort DEĞİL); `compare=prior`
delta'ları ÇALIŞIYOR (api.go:2248-2273, ikinci pencere taraması);
sparkline-click → 3'lü RED modal + EventMarkers; DependencyStrip servis
seviyesinde. Backend RAW spans CTE (endpoints.go:125-277), pencere p99 =
max(bucket p99) — gerçek pencere quantile'ı değil. URL'de yalnız
range/search/cluster/service (compare/limit/detail DEĞİL). Polling yok.

| Hedef | Durum | En yakın taş |
|---|---|---|
| Sıralanabilir tablo + failure% + trend sparkline | EXISTS | — |
| req/min kolonu | MISSING | QuerySpanMetric `per_min` (spanmetric.go:366) |
| p50/p90/p99 | PARTIAL (yalnız avg+p99) | `spanmetrics_1m` quantile'ları **.5/.95/.99 sabit — p90 YOK** (aşağıda soru) |
| Önceki pencere delta rozetleri | **EXISTS** (`compare=prior` + TrendDelta) | — |
| Detayda latency histogramı | MISSING | GetLatencyHeatmap + ServiceLatencyHeatmap.tsx (route filtresi eklenerek) |
| Hata kırılımı: status | EXISTS (pill) · exception-tipi | MISSING | exceptionGroups (servis-scoped; **http.route filtresi yok**) |
| Top failing traces pivot'u | MISSING | /api/traces?hasError + FindExemplar(Rollup) |
| Detayda split-by (attr) | MISSING | /api/spans/facets (`http.route` wellKnown) + bubbleup + Explore splitBy makinesi |

## 2. DATABASE — mevcut durum + boşluklar

Mevcut: /databases (DependenciesTable: System/Instance/DbName/RED/Trend/
Top-callers; receiver satırları additive), DetailDrawer (aggregate RED +
(service,pod) caller impact tablosu + top-80-char statements raw-scan),
SlowQueries kataloğu (normalize regex read-time, raw spans, totalMs sıralı),
DBQueriesPanel + DbCard (servis-scoped), OraclePanel (WaitClasses stacked
bar, RowLockWaits, TopSQL, tablespace), PG/MySQL TopSQL receiver panelleri.
MV'ler: db_summary_5m + db_caller_summary_5m (statement boyutu YOK).

| Hedef | Durum | En yakın taş |
|---|---|---|
| Toplam-süre sıralı statement listesi | EXISTS (raw-spans, bounded) | dbqueries.go |
| Statement-seviyesi latency/hata TRENDİ | MISSING | DBTrend (instance-bazlı) + MultiLineChart |
| Statement DETAY görünümü (kalıcı sayfa) [INFERRED] | MISSING | row-expand var; kimlik yok |
| Statement başına CALLER kırılımı [INFERRED] | MISSING | db_caller_summary_5m'de statement boyutu yok |
| Statement→gerçek exemplar trace [INFERRED] | PARTIAL (lossy LIKE deep-link) | metric exemplar fingerprint deseni |
| Dönem karşılaştırması [INFERRED] | MISSING | Endpoints compare=prior deseni |
| Kalıcı statement fingerprint | MISSING | otlp.SeriesFingerprint deseni (ingest-time xxhash) |
| Cross-engine wait/lock modeli [INFERRED] | PARTIAL (Oracle tam, MySQL kısmi, PG yok) | receiver panelleri |

## 3. MESSAGING — mevcut durum + boşluklar

Mevcut: tek tablo (DependenciesTable kind=queue: System/Cluster/Destination/
RED/Trend[DB-trends'ten ödünç rps]/Top callers), DetailDrawer
Publishers/Consumers/Other rol kırılımı ((service,pod,kind) RED) + TopOps.
MV'ler messaging_summary_5m + messaging_caller_summary_5m (p50/p95 state'te
VAR, yalnız p99 yüzeye çıkıyor). `mq_consume_p99_ms` alarmı span-süresi
PROXY'si — gerçek consumer-group offset lag'inin ingest/read yolu YOK
(yalnız dashboard preset'inde isim olarak geçiyor). Topic detayı
route'lanabilir değil (URL'siz inline drawer). span_links producer↔consumer
korelasyonu bu sayfaya HİÇ bağlı değil.

| Hedef | Durum | En yakın taş |
|---|---|---|
| Produce vs consume rate kolonları [INFERRED] | PARTIAL (kind boyutu MV'de var, overview'da collapse) | GetMessaging'e GROUP BY kind |
| İşleme p50/p99 | PARTIAL (p50 tek satırlık read değişikliği) | MV state hazır |
| Gerçek consumer-lag trendi [INFERRED] | MISSING | metric_points + semconv mapping (broker/collector emisyonu ŞART) |
| Topic detayı: producer↔consumer + lag sparkline | PARTIAL (rol kırılımı var; seri yok) | Sparkline/TrendCell |
| Uçtan uca produce→consume gecikmesi [INFERRED] | MISSING | **span_links** (v0.8.329-335) — link timestamp delta'sı; en hazır varlık |
| Exemplar/linked-trace pivot'ları | MISSING (bu yüzeyde) | /api/exemplars + /api/traces/{id}/links hazır |
| Dönem karşılaştırması [INFERRED] | MISSING | compare=prior deseni |
| Route'lanabilir topic görünümü (?destination=) | MISSING | URL-first house kuralı |

## Fazlı implementasyon planı (onay sonrası; her dilim kendi v0.8.X'i)

**Faz E — Endpoints (önerilen ilk: MV-first + en görünür):**
- E1 (~yarım gün): tabloyu `spanmetrics_1m`/QuerySpanMetric'e taşı — req/min
  + p50/p95/p99 kolonları, gerçek pencere quantile'ları, server-side global
  sort (v0.8.318 deseni), sparkline'lar korunur; raw-spans CTE emekli.
  Dosyalar: chstore/endpoints.go (MV yolu), api.go handler, Endpoints.tsx
  kolonlar, types/api.
- E2 (~yarım gün): route-scoped detay drawer'ı — latency heatmap
  (spanHeatmap + route filtresi), status+exception kırılımı (exception
  tablolarına http.route/span attrs filtresi: exception_inbox'a route
  boyutu EKLEMEDEN raw-span occurrences filtrelemesi), top failing traces
  (?hasError ranked + exemplar), URL ?endpoint= state.
- E3 (~2 saat): detayda split-by — facets+bubbleup wiring (Explore makinesi).
- E4 (~1 saat): compare/limit/detail URL'e; polling 30s (document.hidden).

**Faz M — Messaging (en yüksek değer/maliyet oranı):**
- M1 (~2 saat): produce/consume rate kolonları + p50 projeksiyonu +
  ?destination= route'lu detay + compare=prior portu.
- M2 (~yarım gün): span_links tabanlı uçtan-uca gecikme + topic detayında
  linked-trace/exemplar pivot'ları + lag-proxy sparkline (consume p99 serisi).
- M3 (~yarım gün, **ops bağımlı**): gerçek consumer-group lag — semconv
  metric mapping + lag sparkline + offset-lag alarmı (süre-proxy'sinden ayrı).
  ÖN KOŞUL: broker/collector'ın lag metriği emit etmesi (aşağıda soru).

**Faz D — Database (en derin iş; kimlik önce):**
- D1 (~yarım gün): **ingest-time statement fingerprint** — normalize (mevcut
  regex'in Go portu) + xxhash → spans'e `db_stmt_hash` MATERIALIZED/explicit
  kolonu (⚠ distributed-safety: hasXCol probe + koşullu INSERT — v0.8.185/186
  sınıfı) + `db_statement_summary_5m` MV'si (system/instance/service/
  stmt_hash/sample stmt/time_bucket + RED state'leri). SlowQueries kataloğu
  MV'ye taşınır.
- D2 (~yarım gün): statement detay görünümü — trend (yeni MV), statement
  başına caller kırılımı (MV'de service boyutu var), gerçek exemplar pivot
  (stmt_hash → spans PK-yakın arama), compare=prior.
- D3 (~2 saat): cross-engine wait/lock şeridi — Oracle WaitClasses + MySQL
  row-lock + PG lock metriklerinin ortak modeli, /databases detayına.

## Onay öncesi AÇIK SORULAR (bloklayan)

1. **Kesik brief:** Endpoints'in son maddesi + Database/Messaging hedef
   bölümleri gelmedi. [INFERRED] hedefler kabul mü? Kalan metni yapıştırırsan
   planı düzeltirim.
2. **p90 vs p95:** MV quantile state'leri .5/.95/.99'da sabit. p90 şart mı?
   Şartsa `quantilesTDigestState` argüman değişikliği = MV drop+recreate +
   bucket reset (kısa okuma boşluğu; combined-MV drop prosedürü). Önerim:
   **p95 ile devam** (mevcut, sıfır maliyet), p90 istersen D1 tarzı ayrı
   migration dilimi.
3. **DB fingerprint yeri:** ingest-time kolon (önerilen; distributed-gated)
   mı, read-time-only MV (normalize INSERT'te ama spans'e kolon eklemeden —
   daha az invaziv, exemplar pivot'u zayıflar) mı?
4. **Gerçek Kafka lag'i:** ortamında broker/collector consumer-group lag
   metriği emit ediyor mu (kafka exporter / JMX)? Etmiyorsa M3 ertelenir,
   span-link tabanlı uçtan-uca gecikme (M2) ana lag göstergesi olur.
5. **Sıra onayı:** önerilen E1→E2→M1→M2→D1→D2→(E3/E4/M3/D3). Stability
   direktifi gereği her dilim küçük + ölçülü.

**Stage 1 burada bitiyor — onayını bekliyorum.**
