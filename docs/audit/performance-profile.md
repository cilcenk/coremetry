# Audit — Coremetry performans profili (2026-09-02)

**Durum:** ölçüm + kod okuması tamamlandı; **prod ClickHouse sayıları BEKLİYOR** (bu makineden
erişim yok: prod CH host'ları çözülmüyor, prod kube-context yok). Prod satırları bu raporda
"⏳ prod" ile işaretli; paket [performance-profile-queries.sql](performance-profile-queries.sql)
prod'da olduğu gibi koşulup çıktısı bu dosyaya işlenecek. Kod değişikliği YOK (istek gereği);
öncelikli liste onaydan sonra ayrı oturumlarda uygulanır.

Yöntem: altı paralel okuyucu (ingest, cache, frontend sayfa açılışı, grafik nokta hacmi, şema,
yerel CH ölçümü) + SQL-imzası→Go-kaynağı eşleme taraması (667 sorgu yeri, 517 fonksiyon, 453
route kaydı) + her M/L bulguya üç açılı çürütme (kanıt / etki / doğruluk). Yerel ölçümler
**fixture**dır (minikube 2 shard × 1 replika, CH 26.2.4, demo yükü, CPU-kısıtlı); mutlak
süreler prod'a taşınmaz, SORGU ŞEKLİ (tarama/plan/gövde) taşınır ve bulgular yalnız
fixture'dan bağımsız olanlardır.

---

## 0. Yönetici özeti (5 madde)

1. **Ingest'in darboğazı satır hacmi değil MV kaskadı:** her `spans_local` flush'ı 19 MV'yi
   SIRAYLA tetikliyor (`parallel_view_processing=0`); yerelde flush p99 13-16 s, 43 async
   FlushError/24 s ve **kaskad ortasında timeout → async yeniden deneme = çift sayım** (ING-1).
   Prod spool olayı (2026-08-20) bu zincirin aynı ucunda: `insert_distributed_sync=0` +
   `rand()` (S7).
2. **Cache anahtarları ham nanosaniye taşıyor:** 14 uç `from/to` ya da RawQuery'yi anahtara
   koyuyor, SPA her poll'da `to=Date.now()` gönderiyor → cache fiilen MISS; `/api/services`
   ön-ısıtma anahtarı v0.9.345'ten beri handler anahtarıyla eşleşmiyor (ısıtılan slot
   erişilemez) (C1, C2). TTL=30 s ile cacheBucket=30 s çakışması SWR penceresini
   kullanılmaz kılıyor (C3).
3. **Sayfa açılışı 14-17 istek:** shell 6 + Topbar environments + SSE; Traces 9+1, Services 8;
   `/api/attribute-keys` aynı URL'yle iki kez; Inbox açık problem başına bir blast-radius
   isteği (200'e kadar); SSE olayları birleştirilmeden tek tek invalidation (F1-F4).
4. **Şema niyetle gerçek arasında açık:** yükseltilmiş kurulumlar `rand()` sarmalayıcıda
   kalıyor → 28 `optimize_skip_unused_shards=1` sitesi sessiz no-op (S1); `metric_points`
   ORDER BY `(service_name, metric, time)` okuma şeklinin (55/59 sorgu metric-filtreli)
   tersi (S3); `spanmetrics_1s`'in 6 saatlik satır TTL'i günlük partition içinde hiç
   ateşlenmiyor (S2); en sık koşan sorgu `system_settings FINAL` (30 s'lik 18+ yenileyici,
   yerelde 315k çağrı / 57k s toplam 7 günde).
5. **Grafik hacmi büyük ölçüde sınırlı** (adım merdivenleri, 1500 varsayılan maxDataPoints)
   — dört gerçek boşluk: `/api/services/sparklines` ham 5-dk MV satırı (7 g ≈ 100k JSON
   nesnesi / 50 satır), `groupBy`'lı `/api/metrics/query`'de sunucu top-N yok (50k satır
   tavanına kadar), açık adımda nokta bütçesi yok, heatmap örnekleme WHERE (I/O sınırsız)
   (CDV-1..4).

---

## 1. Ortam ve kısıtlar

| | Prod (hedef) | Yerel (ölçülen) |
|---|---|---|
| ClickHouse | dış Distributed küme, 2 shard × 2 replika, uygulama `coremetry-prod` ns'inde distributed modda | minikube `coremetry` kümesi 2 shard × 1 replika, CH 26.2.4.23, `max_threads auto(3)`, 12.5 GB |
| Erişim | ⏳ bu makineden yok (DNS/port kapalı, kube-context yok) | `kubectl exec chc-0 clickhouse-client` |
| Veri | 1B+ span/gün ölçeği | fixture: spans_local 18-38 M satır (host başına), 20 günlük partition |
| Self-obs | `coremetry-api/worker/ingest` (mod son-eki) | `coremetry-monolithic` |
| Kısıt | yalnız `system.*` + SELECT, `max_execution_time ≤ 30/120`, LIMIT | aynı paket |

Prod'da koşulacak paket: `sed 's/__CLUSTER__/<prod küme adı>/g' docs/audit/performance-profile-queries.sql | clickhouse-client -d coremetry --multiquery`.
Not: `query_log` prod'da açık mı (`log_queries`) doğrulanmadı; kapalıysa §2.1-2.3 uygulanamaz, elde yalnız
`system.events` kalır (bilinen boşluk, /perf-triage).

---

## 2. ClickHouse sorgu profili

### 2.1 Paket doğrulaması (yerel)
İlk paketin 22 ifadesinden 7'si hata verdi, 2'si sessizce yanlış sayı üretti; düzeltilmiş paket 22/22
temiz (Q1 9.2 s, Q2 62 s, Q3 17.8 s [1 gün], diğerleri ≤ 3 s). Düzeltmeler paketin başında
yazılı (iki geçişli `any(query)`, alias gölgesi, `AsyncInsertFlush`, GLOBAL self-join, re2, `marks_bytes`).

### 2.2 Yerel profil (fixture — şekil için, sayı için değil)
| Sıra | Ne | Kanıt (yerel, 7 g, iki node) | Kaynak (Go) |
|---|---|---|---|
| 1 | `SELECT value FROM system_settings FINAL WHERE key = ? LIMIT 1` | 314.8k çağrı, **57.268 s toplam**, avg 182 ms | `chstore/settings.go:20` `GetSetting` ← 18+ `StartConfigRefresh(…, 30 s)` (main.go) × ~24 anahtar × node |
| 2 | trace liste kök-ad agregatı (`anyIf(name, parent_id = '' …) AS root_name`) | bellek başına 130-147 MiB, 0.8-1.6 M satır okuma | `chstore/repo.go:2532 buildGetTracesListSQL` ← `GET /api/traces` (ham yol) |
| 3 | `deriveMetadataAllSQL` (3 saat ham spans, `has(res_keys…) OR …` zinciri) | p95 **15.7 s**, max 25.4 s, 500 çağrı | `chstore/service_metadata.go:626-673` ← 10 dk'lık servis-metadata işçisi |
| 4 | behavior 28 g `service_summary_5m` | p95 13.0 s | `anomaly/behavior.go:172` ← 2 dk anomali tiki |
| 5 | `discoverReceiverInstances` (`startsWith(metric,'oracledb.')`, 1 saat) | EXPLAIN: MinMax 63/3547 → PrimaryKey Keys `metric,time` ama **63/63 granül** (ikinci anahtar kolonu tek başına budamıyor) | `chstore/*receiver*` ← /databases |
| 6 | Q3c: 14.8k timeout (159), çoğu INSERT bacağı | yerel CH tıkanıklığı (DDL kuyruğu + CPU) | ingest |

**Endpoint → sorgu eşlemesi:** uygulama sorguları `log_comment`/`query_id` taşımıyor (0 kullanım);
eşleme metin imzasıyla. Tam tablo: MV adı → fonksiyon → route (667 site) ayrı üretildi; ayırt
edici imzalar: `max_execution_time = 180` yalnız 4 topoloji MV yazıcısı; `= 60` `RefreshExceptionGroups`
/ `GetFlowTopology`; `trace_id GLOBAL IN (` trace listesi; `anyIf(` + `trace_id IN (` extras;
`quantilesTDigestMerge` MV okuyucuları. Self-obs child span'leri yalnız `/v1/profiles` ve
`/api/auth/login` için var (Q13c) — endpoint→CH bağı prod'da da bu iki uçla sınırlı kalacak;
düzeltme = `log_comment` (aşağıda P-plan).

⏳ prod: Q1/Q2/Q3/Q3b/Q3c çıktısı bu bölüme; ağır 20 sorgu için Q4 EXPLAIN (`spans_local`) —
PrimaryKey `Keys:` listesinde `service_name` yoksa ve `Granules N/N` ise tam tarama işareti.

### 2.3 Tam-tarama adayları (koddan; prod EXPLAIN ile doğrulanacak)
158 ham-`spans` SELECT literal'inden **100'ü `service_name` yüklemi taşımıyor**, 89'u trace/span id de
taşımıyor — yalnız zaman + boyut (partition budaması + skip index'e güvenir): `heatmap.go:156`,
`exception.go:162`, `baseline.go:73`, `repo.go:389/468/556` (cluster/env picker'ları, günde ~600×),
`evaluator.go:1652/1675` (ham düşüş kolu, her tik), `k8s_coverage.go:120`, `pod_inventory.go:123`.
Yerel EXPLAIN: `time >= ? AND db_system != ''` → Keys `time`, Granules 4/4 (skip index düşürmüyor);
`kind='server' AND http_method != ''` → 5/5. LIMIT'siz ham okuma: topoloji MV yazıcıları (bilinçli,
met=180), `GetSystemStats` (met=8), `ComputeSLO*`, `GetMetricBaseline`, `GetInfraMetrics`,
`walkEndpointEdges`, evaluator ham kolu.

---

## 3. Şema ve index denetimi (koddan; ⏳ prod Q5-Q8 ile doğrulanacak)

| Tablo | Engine / ORDER BY / PARTITION / TTL | Index'ler | Not |
|---|---|---|---|
| `spans` | MergeTree `(service_name, time)` / `toDate(time)` / `retention.spans` (30 g) | `idx_trace` bloom(0.01) G4; `set(0)` G4: name, kind, db_system, status, attr_channel_code, attr_function_code, attr_function_id, k8s_*, container_image(_tag); `http_status` minmax | attribute'lar `Array(LowCardinality(String))` + `Array(String)` (Map değil); terfi kolonları MATERIALIZED |
| `logs` | `(service_name, severity_num, time)` / günlük / 30 g | `idx_body tokenbf_v1(32768,3,0)`, `idx_logs_trace` bloom | attr/res dizileri codec'siz (S6). tokenbf yalnız `hasToken` ile budar; `multiSearchAny*` budamaz (F1) |
| `metric_points` | `(service_name, metric, time)` / günlük / 7 g | **yok** | okuma şekli metric-filtreli (S3); 0006 prod'da LowCardinality'ye taşır |
| `exemplars` | `(series_fingerprint, timestamp)` / günlük / spans TTL | — | tek `Map` kolonu `filtered_attributes` |
| MV ailesi (20) | AggregatingMergeTree, `toDate(time_bucket)`, tDigest state, 90 g | — | `trace_summary_5m`/`trace_service_index_5m` 90 g × ham kardinalite (S5) |
| Rollup katmanları (13, operatör) | ReplicatedAMT + Distributed `rand()`, ZSTD(1) | — | `spanmetrics_1s` satır TTL 6 s günlük partition'da ateşlenmiyor (S2: 397 saat ölçüldü) |
| State (problems, anomaly_events, …) | ReplacingMergeTree(version), FINAL, partition YOK (P1) | — | `problems` TTL yok; `ListProblems` her çağrıda tüm tablo FINAL (F4) |
| Distributed | sarmalayıcı `Distributed(cluster, db, <t>_local, rand())` | — | `insert_distributed_sync=0` (spool), `optimize_skip_unused_shards` 28 sitede istenir ama `rand()` altında inert (S1); `distributed_group_by_no_merge` hiç kullanılmıyor; `GLOBAL IN/JOIN` disiplini temiz (30 + 13, GLOBAL'sız alt sorgu 0) |

`CREATE IF NOT EXISTS` canlı tabloyu bildirilen DDL'e yakınsamaz ve fark rapor edilmez (S8) —
prod şemasının koddakiyle eşleşip eşleşmediği ⏳ Q5/Q6/Q7 ile görülür.

---

## 4. Ingest sağlığı

**Kod (okundu):** receiver → bellek-içi consumer (10k satır / 2 s / 8 işçi / 500k öğe / 512 MiB
sinyal başına) → sinyal başına batch başına TEK INSERT (Distributed sarmalayıcı, RoundRobin havuz,
`async_insert=1`, `wait_for_async_insert=1`, 10 MiB / 1 s busy timeout). OTLP isteği başına 0 INSERT
(batch). Sunucu tarafında spans flush'ı **19 MV**'yi sırayla tetikler (metric_points 4, span_links 1 —
hedefi Distributed tablo → cross-shard spool dosyası, ING-7). `profiles`/`log_templates` consumer'ı
atlar (INSERT başına part). OTLP HTTP :8088'de zamanaşımı yok, gRPC'de eşzamanlı akış tavanı yok,
32 MiB gövde tam okunur (ING-4). `ObserveSpan` kabul edilen span başına global mutex (ING-8).

**Yerel ölçüm (fixture):** AsyncInsertFlush 24 s: metric_points 18.4k flush × 556 satır (p95 2.5 s),
spans 14.8k × 398 (p95 5 s), logs 9.5k × 2 satır, span_links_reverse 11.2k × 2, profiles 7.2k × 1,
problems 2.6k × 1 → **küçük flush'lar part churn'ü**: spans_local 157 aktif part / 21 partition (max
11/partition), metric_points 118 part (max 20/partition). MV inner tabloları günde 4.4-7.1k merge.
Replikasyon kuyruğu 40 kayıt (yerel DDL kuyruğu tıkanıklığı — ortam). 43 FlushError/24 s, hepsi
"Timeout exceeded … while pushing to views".

⏳ prod: Q9-Q12b (part sayısı/partition, flush sayısı × satır, merge backlog, `MaxPartCountForPartition`,
`ReplicasMaxQueueSize`).

---

## 5. API / frontend katmanı

**Self-obs (yerel, 7 g, `coremetry-monolithic`, kind=server):** `/api/problems` 191 çağrı p50 207 ms
**p95 19 s** p99 34.8 s (14 hata), `/api/inbox/count` p95 6.5 s, `/api/problems/count` p95 5.2 s,
`/v1/profiles` 9.1k çağrı p95 2.3 s, `/v1/traces` p95 196 ms. Mutlak süreler fixture (CH tıkalı);
**şekil** prod'a taşınır: problems yolu `ListProblems`/`CountProblems*` her çağrıda `problems FINAL`
tam tablo (F4). ⏳ prod Q13/Q13b.

**Sayfa açılışı (koddan sayıldı):** kabuk her sayfada 6 istek (auth/me, branding, health,
inbox/count, announcement, copilot/config) + Topbar `/api/environments` + 1 SSE (lider sekme) +
copilot açıksa problems/count. Traces ≈ 9 anlık + 1 ardışık (extras) → 16-17; Services 8 → 14-15;
Dashboard 2 + bundle'a girmeyen panel başına 1; Problems 5-6; Inbox 7 + açık problem başına 1
blast-radius (200'e kadar, F3). Çiftler: `/api/attribute-keys` aynı URL iki kez (F1); picker'lar
mount'tan 180 ms sonra tam top-200 katalog (F7); `/api/clusters`+`/api/namespaces` her aralık
değişiminde (F8). SSE geçersiz kılmaları olay başına, birleştirme yok; problem/inbox queryFn'leri
sinyali yok sayıyor (F4). Traces/Services/Exceptions ham `api.*` effect'leri (staleTime 0) → rota
yeniden girişinde tam refetch (F9). `staleTime 25 s < refetchInterval 30 s` beş hook'ta (F5).

**Sunucu cache:** L0 singleflight (pod) → L1 FIFO 1024 / min(TTL,5 s) (pod) → L2 Redis zarfı 3×TTL
+ SWR; 179 `serveCached` sitesi (30 s ×70, 60 s ×63, 15 s ×16, 5 dk ×10, 5 s ×6…). Adlı sıcak
uçların hepsi cache'li (`/api/health` kendi 5 s ping cache'i). Boşluklar: ısıtma anahtarı ↔ handler
anahtarı uyumsuz (C1), 14 uç ham ns anahtarı (C2), TTL=grid (C3), `refresh=1` anahtara giriyor
(C4), servis bundle deploy'ları yalnız Redis (Noop cache'te hep boş, C5), singleflight/L1 pod-yerel
(çok pod = N× soğuk hesap, C6), Redis kaybında devre kesici yok (C7), `?refresh=1` her role
sınırsız (C10), Redis `maxmemory=0/noeviction` (C11).

**uPlot nokta hacmi:** metrik yolu piksel-adaptif adım (`maxDataPoints` 1500 → 24 s'de 1440, 7 g'de
1008 nokta/seri); span batch/tek yol 144-336; resolver ≤720; heatmap ≤240×28; logs ≤240 (UI), 5000
sunucu tavanı; sparkline 120 slot. İstemci LTTB yalnız `TimeSeriesPanel` (2000), `Sparkline` çizgi,
`LatencyScatter` (2000, meşru ham). Boşluklar CDV-1..5.

---

## 6. Bulgular

Çürütme turu (her M/L bulguya 3 açı) oturum limitine takıldı: 108 ajandan 87'si koşamadı;
tamamlanan oylarda düşen bulgu yok. Aşağıdaki tablo okuma fazının bulgularıdır; **⭐ işaretliler**
bu raporun yazarı tarafından kaynakta/ölçümle ayrıca doğrulandı (dosya:satır atıfları). Etki
tahmini prod ölçeği (1B span/gün, 2×2) varsayımıyla; efor S/M/L.

| # | Bulgu | Kanıt | Etki | Çözüm önerisi | Efor |
|---|---|---|---|---|---|
| ING-1 ⭐ | MV kaskadı ortasında zaman aşımı → async flush yeniden denemesi span'leri ÇİFT yazar, MV oranları çift sayar (`async_insert_deduplicate=0`, consumer 3 deneme) | `internal/consumer/consumer.go:289-310` deneme döngüsü; yerelde 43 FlushError/24 s "while pushing to view" | **L** — 1B/gün'de her kaskad timeout'u sessiz veri hatası | Kaynak part'ı commit olduktan sonra "pushing to view" hatasında yeniden DENEME (idempotent değil); `parallel_view_processing=1`; MV sayısını düşür (19 → çekirdek); `async_insert_deduplicate=1` (Replicated tablolarda) | M |
| ING-2 | 19 MV `spans_local` flush'ında SIRALI tetikleniyor — gecikme ve timeout satır hacminden değil kaskaddan | `system.tables.dependencies_table` (yerel), `parallel_view_processing` 0 | L | `parallel_view_processing=1` + `materialized_views_ignore_errors` DEĞİL; MV'leri kademeli (1m'den 5m) kur | S |
| ING-3 | Part üretimi flush kadansına bağlı: 2 s consumer tiki + adaptif busy timeout (min 50 ms) → küçük part'lar, yüksek merge yükü (yerelde 305 part/saat/node, 41 satır/part) | `internal/config/config.go:635-662`, `repo.go:60-68`; Q9/Q11b yerel | M | consumer flush 2 s → 5 s + `async_insert_busy_timeout_min_ms` yükselt; hedef ≥ 5k satır/part | S |
| ING-4 | OTLP :8088 (API mux'unda) HTTP zaman aşımı yok; gRPC eşzamanlı akış tavanı yok; 32 MiB gövde tam okunur | `internal/api/api.go:571-580`, `internal/otlp/grpc.go:43-63` | M | `http.Server` Read/Write/Idle timeout, `MaxConcurrentStreams` | S |
| ING-5 | Takılı flush aşaması işçileri dakikalarca tutarken sabit 2 s retry-hint → yük tepesinde yeniden deneme fırtınası | `consumer.go:289-292`, `otlp/http.go:508-522` | M | Retry-After'ı kuyruk doluluğuna göre üstel | S |
| ING-7 | `span_links_reverse_mv` hedefi Distributed tablo → her span_links insert'i cross-shard spool dosyası üretir | `store.go` MV tanımı; `system.tables` yerel | S | MV hedefini `_local` yap (shard-yerel) | S |
| ING-8 | `ObserveSpan` kabul edilen span başına global mutex | `internal/otlp/*` (ölçülmedi) | M? | ölç (pprof) → shard'lı sayaç | S |
| S7 | Ingest Distributed spool'a bağlı (`insert_distributed_sync=0`, `rand()`) — 2026-08-20 prod olayının kök mekanizması | `cluster.go:260`, prod olayı runbook | **L** | uygulama shard'a doğrudan yazsın (`_local` hedefi + istemci tarafı shard seçimi) ya da `insert_distributed_sync=1` + batch ölçümü | L |
| C1 ⭐ | `/api/services` ön-ısıtma anahtarı handler anahtarıyla eşleşmiyor → ısıtılan slot erişilemez | `internal/api/api.go:1706` `warm("services-landing", landingKey…)` vs handler anahtarı (v0.9.345 sonrası) | M | tek anahtar fonksiyonu; `cache_key_test.go` emsali test | S |
| C2 ⭐ | 14 uç ham ns `from/to` ya da RawQuery ile anahtarlıyor; SPA her poll'da `to=Date.now()` → cache fiilen MISS | 7 `UnixNano()` anahtar yapıcı (grep), `?refresh=1` anahtara giriyor (C4) | **M-L** (çok pod'da N× CH) | `cacheBucket` basamağı (30 s grid) tüm anahtarlarda; `refresh=1` anahtardan düş | M |
| ~~C3~~ | ~~TTL (30 s) = cacheBucket grid (30 s) → SWR/STALE penceresi kayan sorgularda hiç kullanılmıyor~~ **ÇÜRÜTÜLDÜ (v0.10.256):** kayan pencerede anahtar `to` kovasıyla değişir; bir anahtar yalnız kendi 30 s kovası boyunca istenir, TTL kaç katı olursa olsun kovadan sonra o anahtarı kimse istemez → SWR bu sınıfta tanım gereği devreye girmez. TTL büyütmek yalnız SABİT pencereli (custom range) anahtarlara yarar; onlar zaten STALE görüyor | `cache.go:183-190` | — | reçete uygulanmaz; kayan pencerede kaldıraç kova boyu (C2) | — |
| C5 | Servis bundle deploy'ları yalnız Redis (L1 yok, eşzamanlı fallback yok) → Noop cache'te hep boş | `internal/api/*bundle*` | M | L1 + senkron fallback | S |
| C6 | singleflight ve L1 pod-yerel: soğuk anahtarda her pod CH'ye gider | `cache.go:24-30` | M (2+ pod) | Redis'te lider-hesap kilidi (SETNX) ya da anahtar başına tek hesap | M |
| C7 | Redis kaybında istek başına 500 ms'e kadar bekleme, devre kesici yok | `cache.go:205` | M | devre kesici (n hata → 30 s bypass) | S |
| C10/C11 | `?refresh=1` her role sınırsız; Redis `maxmemory=0/noeviction`, 24 s TTL'li anahtarlar | chart/values.yaml | S | admin'e kısıtla + rate limit; `allkeys-lru` | S |
| F1 ⭐ | `/api/attribute-keys` aynı URL ile iki kez (sayfa + ColumnManager) | `pages/Traces.tsx`, `components/ColumnManager.tsx:34` | S | tek `useQuery` anahtarı paylaş | S |
| F2 | `/api/traces` anahtarı ham query string; `to=Date.now()` → 20 s sunucu cache'i işe yaramıyor | `api.go getTraces` + `Traces.tsx:463` | M | istemcide `alignTraceWindow` zaten var → sunucu anahtarını kovaya oturt | S |
| F3 ⭐ | Inbox: açık problem satırı başına bir `/api/services/{svc}/blast-radius` (200'e kadar) | `features/anomalies/ProblemsSection.tsx:705/993` | M | toplu uç (`/api/blast-radius?services=…`) ya da görünürde/hover'da yükle | M |
| F4 ⭐ | SSE olayları birleştirilmeden tek tek invalidation; problem/inbox queryFn'leri sinyali yok sayıyor; `ListProblems` her çağrıda `problems FINAL` TÜM tablo (TTL yok) | `lib/queries/eventStream.ts:110`, `eventInvalidations.ts:17`; `chstore/problem.go:1228`; yerel Q13 `/api/problems` p95 19 s | **M-L** | 250 ms coalescing; `problems` için `started_at` sınırı + çözülmüşlere TTL (180 g) | M |
| F5-F9 | `staleTime 25 s < refetchInterval 30 s` (5 hook); bucket anahtarı `keys.problems.all` dışında; picker'lar mount'ta top-200 katalog; clusters/namespaces her aralıkta; ham `api.*` effect'leri rota girişinde tam refetch | `lib/queries/*` | S | staleTime ≥ interval; anahtar hiyerarşisi; picker'lar yazınca; RQ'ya taşı | S-M |
| CDV-1 | `/api/services/sparklines` ham 5-dk MV satırı (7 g ≈ 100k JSON nesnesi / 50 satır) | `api.go:802`, chstore sparklines | **L** (Services açılışı) | slot grid'e indir (120 slot) | M |
| CDV-2/3 | `/api/metrics/query` açık adımda nokta bütçesi yok; `groupBy`'da sunucu top-N yok (50k satır tavanına kadar) | `chstore/metricquery.go:64`, `api.go:4913` | M | maxDataPoints her yolda; `LIMIT N BY` top-N (rollup_read emsali) | M |
| CDV-4 | Heatmap örnekleme WHERE filtresi, `SAMPLE` değil → I/O sınırsız | `chstore/heatmap.go:121` | M | 6 s üstü `SAMPLE` ya da MV | L |
| CDV-5 | Metrik rollup katmanları otomatik adımda erişilmez (`StepSeconds>0` şartı) | `metric_rollup_read.go` plan | M | auto-step'i çözümledikten sonra plana ver | S |
| S1 ⭐ | Tablo başına shard politikası yalnız taze kurulumda; yükseltilmiş kurulum `rand()` → 28 `optimize_skip_unused_shards=1` sitesi no-op | `cluster.go:247-260, 310` | M | admin "shard policy" raporu + migration (yeni sarmalayıcı `cityHash64(service_name)`) | M |
| S2 ⭐ | `spanmetrics_1s` satır TTL 6 s, günlük partition → ölçülen 397 saat tutulmuş | `store.go:3445-3447` | M (disk + tarama) | saatlik PARTITION + `ttl_only_drop_parts=1` (0012 emsali) | S |
| S3 | `metric_points` ORDER BY `(service_name, metric, time)`; 55/59 okuma yalnız `metric` filtreli → ikinci anahtar tek başına budamıyor (EXPLAIN 63/63) | `metricquery.go`, receiver keşfi | **M-L** (/databases, Explore) | `metric` üzerine `set(0)`/bloom skip index (migration) ya da PROJECTION `(metric, time)` | M |
| S4 | env/cluster picker'ları günde ~600× ham spans tarıyor; katalog MV yok | `repo.go:389/468/556` | M | `service_seen`/`entity_seen_5m` üzerinden okuma ya da küçük katalog MV | M |
| S5 | `trace_summary_5m`/`trace_service_index_5m` 90 g × ham kardinalite vs spans 30 g | store.go MV TTL | M (disk) | TTL = spans retention + 1 g | S |
| S8 | `CREATE IF NOT EXISTS` canlı tabloyu bildirilen DDL'e yakınsamaz; fark rapor edilmez | store.go | M (prod sürprizi) | admin "şema drift" raporu (`system.tables` ↔ beklenen) | M |
| P-set ⭐ | En sık sorgu `system_settings FINAL` — 15+ yenileyici × ~24 anahtar × 30 s, yerelde 7 g'de 315k çağrı / 57k s | `main.go` `StartConfigRefresh(…, 30 s)` ×15, `settings.go:20` | M | tek toplu yenileyici (`SELECT key,value FROM system_settings FINAL` bir kez / 30 s) + versiyon damgasıyla dağıt | S |
| P-meta ⭐ | `deriveMetadataAllSQL` 3 saat ham spans `has(res_keys…) OR …` zinciri: yerel p95 15.7 s (en yavaş sorgu) | `service_metadata.go:626-673`, 10 dk işçi | M | `service_seen`/entity MV'den türet; ham tarama yalnız yeni servisler için | M |
| P-list ⭐ | Trace listesi ham yolu: süre/span/durum sıralaması + attribute filtresi → string durumlu tam pencere agregatı (**prod 241, 3.73 GiB**) | v0.10.235 ile iki aşamaya alındı (3.8× bellek) | L → düzeltildi | kalan: pencere daraltma (audit D5), `max_execution_time` 25 s tavanı uzun aralıklarda | M |
| P-comment | Sorgularda `log_comment`/`query_id` yok → prod'da endpoint→sorgu eşlemesi yalnız metin imzasıyla | 0 kullanım (grep) | M (denetlenebilirlik) | `telemetryReadConn` sarmalayıcısında `log_comment = route:<pattern>` | S |
| F1-log | Log-desen dedektörü `multiSearchAnyCaseInsensitive` kullanıyor; tokenbf yalnız `hasToken*` ile budar → 23 desen × 2 dk tam tarama (CH logstore) | `logstore/clickhouse.go:306-310, 424`; EXPLAIN 30/30 vs 0/33 | M (CH logstore'da) | `hasTokenCaseInsensitive` ön-filtre + `multiSearch` doğrulama | S |

## 7. Öncelikli plan — ilk 8 iş (etki/efor)

| # | İş | Etki | Efor | Değişecek dosyalar |
|---|---|---|---|---|
| 1 | **Ingest idempotans + MV kaskadı** (ING-1, ING-2, ING-3): "pushing to view" zaman aşımında yeniden deneme yok (ya da dedup), `parallel_view_processing=1`, flush 2 s→5 s, busy-timeout tabanı | L | M | `internal/consumer/consumer.go`, `internal/chstore/repo.go` (asyncInsertCtx), `internal/config/config.go`, `internal/chstore/store.go` (MV listesi/ayarları), testler `consumer_test.go` |
| 2 | **Cache anahtarı basamağı + ısıtma anahtarı** (C1, C2, C4 — **GEMİDE v0.10.256**; C3 çürütüldü, aşağıda) | M-L | M | `internal/api/api.go` (key builder'lar), `internal/api/cache.go`, `internal/api/cache_key_test.go` |
| 3 | **Ayar yenileyici tekilleştirme** (P-set) — **GEMİDE v0.10.259** (`chstore/settings_refresh.go`: tek tik, tek FINAL okuma, ctx anlık görüntüsü; ölçüm: 224 okuma/5 dk → 10) | M | S | `internal/chstore/settings.go` (`GetSettings`), `internal/tempo/client.go` (şablon), `main.go` kablolama, `internal/*/settings.go` tüketiciler |
| 4 | **Inbox/problems okuma yolu** — F3 toplu blast-radius + F4a SSE 250 ms birleştirme **GEMİDE v0.10.260**; F4b (`ListProblems` sınırı + TTL) ERTELENDİ: ORDER BY id tablosunda started_at sınırı IO kazandırmaz, kaldıraç TTL/partition = şema dilimi (/clickhouse-schema, 0016) | M-L | M | `internal/api/problems*.go` (yeni `blast_radius_routes.go`), `frontend/src/features/anomalies/ProblemsSection.tsx`, `frontend/src/lib/queries/eventStream.ts`, `internal/chstore/problem.go`, `store.go` (TTL) |
| 5 | **`metric_points` okuma şekli** (S3, CDV-5, F2-local): `metric` skip index (migration 0014) + rollup planına auto-step | M-L | M | `internal/chstore/store.go`, `migrations/0014_metric_points_metric_index.sql`, `internal/chstore/metric_rollup_read.go`, `metricquery.go` |
| 6 | **Grafik hacmi** (CDV-1, CDV-2, CDV-3): sparklines slot grid, açık adımda nokta bütçesi, `groupBy` top-N sunucuda | M | M | `internal/api/api.go` (sparklines/queryMetric handler'ları → kendi dosyalarına), `internal/chstore/metricquery.go`, `frontend/src/lib/queries/services.ts` |
| 7 | **Şema hijyeni** (S2, S5, S1-rapor, S8-rapor): `spanmetrics_1s` saatlik partition + `ttl_only_drop_parts`, trace MV TTL = spans+1 g, admin "shard policy / şema drift" raporu | M | M | `internal/chstore/store.go`, `migrations/0015_*.sql`, `internal/chstore/cluster.go`, `internal/api/admin_schema_drift.go` (yeni), `frontend/src/pages/system/*` |
| 8 | **Sorgu etiketleme** (P-comment) — **GEMİDE v0.10.254** (`chstore/query_tag.go`: route/worker/admin) | M (denetlenebilirlik) | S | `internal/chstore/traced_conn.go` (ctx'ten etiket), `internal/api/cache.go` (route → ctx), `.claude/skills/perf-triage/SKILL.md` (bilinen boşluk kapanır) |

Gemide (bu oturum, operatör bildirimi): **v0.10.235** — trace listesi ham yolunda iki aşama (P-list).
Sıra dışı ama ucuz: ING-4 (HTTP timeout'ları), ING-7 (`span_links_reverse_mv` hedefi), F1 (çift
attribute-keys), F5 (staleTime), F1-log (tokenbf) — birer küçük sürüm.

## 8. Doğrulama durumu ve prod adımları

- **Prod ölçümü ⏳:** paketi koş → §2.2 (top-20 toplam/p95, EXPLAIN'ler), §3 (Q5-Q8 şema/index/ayar), §4 (Q9-Q12b
  part/flush/merge), §5 (Q13/13b endpoint p95). Sonuçlar bu dosyaya işlendikten sonra §7 sırası
  yeniden bakılır (özellikle S3/CDV maddeleri prod okuma dağılımına göre yer değiştirebilir).
- **`log_queries` prod'da açık mı?** `SELECT count() FROM clusterAllReplicas('<küme>', system.query_log) WHERE event_time > now()-INTERVAL 1 HOUR`.
- Çürütme turu eksik kaldı (oturum limiti); ⭐ dışındaki bulgular "okuma iddiası" statüsünde — uygulanmadan
  önce ilgili dilimin kendi ölçümü (`/perf-triage` sözleşmesi) şart.
