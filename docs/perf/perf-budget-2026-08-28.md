# "Dynatrace kadar hızlı" → ölçülebilir bütçe (keşif + taban ölçüm + öneri)

Tarih: 2026-08-28 · Sürüm: v0.10.111 · Onay: operatör 2026-08-28 ("onay
önerine veriyorum") — AŞAMA 2 önerisi kabul sayılır, AŞAMA 3 otomasyon bu
dokümanın son bölümündeki tasarıma göre `cmd/perfcheck` olarak gelir.

## 0. Ölçüm ortamı ve geçerlilik sınırı

| | Değer | Kaynak |
|---|---|---|
| Uygulama | v0.10.111-dirty, minikube pod, `kubectl port-forward` (8090→8088) | `/api/version` |
| ClickHouse | 26.2.4, 2 düğüm `chc-0/chc-1`, Distributed + `*_local` | `system.query_log` açık (7977 satır/10 dk) |
| `spans_local` (chc-0) | 16.307.004 satır · 975 MiB · 60 part · 101 servis · 2026-08-12→27 | `system.parts` |
| Küme 24h / 6h / 1h | 1.477.291 / 425.451 / 63.061 span · 143.432 trace (24h) | `spans` |
| `channel_code` (1h) | 60.819/63.171 span'da var, **küçük harf**, terfi kolonu DOLU | `countIf(attr_channel_code != '')` |
| Arka plan gürültüsü | demo üretici + evaluator; `problems FINAL` 105 koşu/25 dk p50 612 ms; `GetMessagingDetail` 12 koşu p50 4-5 s (bir sekme/warmer) | `query_log` |

**Prod'a erişim yok.** Mutlak süreler laptop-VM'inin süreleri: aynı 13k satırlık
`topology_edges_5m` okuması CH'de p50 600 ms sürüyor — gerçek donanımda
10-20 ms olur. Taşınabilir olan **read_rows / read_bytes / granül sayıları
ve oranlar**; süreler yalnız aynı ortamda öncesi/sonrası kıyası için.
Soğuk = `?refresh=1` (`X-Cache: BYPASS`), sıcak = `HIT-L1`; her nokta 3 koşu,
tablo **medyan (min–max)**. Ölçüm betiği: scratchpad `measure.sh` (curl
`time_starttransfer`).

## 1. Ekran → istek → ClickHouse haritası

| Ekran | Yükte atılan istekler | CH okuması | Cache |
|---|---|---|---|
| **Traces listesi** (`pages/Traces.tsx`) | `GET /api/traces` (:451) · `POST /api/spans/metric-batch` (:590) · `GET /api/traces?traceIds=…&extraAttrs=…` (liste döndükten sonra, :518) · `GET /api/views` · service/operation picker (mount'ta eager) · `attribute-keys` yalnız "+Kolon" açılınca | MV yolu `trace_summary_5m` iki aşama (`repo.go:2926`, dilim 5000 id) · filtre/env/cluster/arama → HAM `spans` `GROUP BY trace_id` (`repo.go:2503`, **tam pencere**) · extras `spans WHERE trace_id IN(50)` + gerçek min/max ±5 dk (`traces_extras.go:214`) | 20s / 30s / 20s |
| **Trace detay** (`pages/Trace.tsx`) | `GET /api/traces/{id}` · `/links` (lazy) · Logs sekmesi açılınca `GET /api/logs` | Tempo önce (3 s), sonra `trace_summary_5m` yoklama + `spans WHERE trace_id=? AND time…` (`idx_trace` bloom G4) | 30s |
| ” span seçimi (`SpanDetail.tsx`) | her seçimde **4 istek** (profiles, hotspots, logs, serviceOperations 24h) — debounce/iptal yok | — | — |
| **Servis haritası** (`/service-map`) | `GET /api/service-map` (30 s poll) · `GET /api/servicegraph?focus…` | `/service-map`: **HAM `spans`** örnekli (≤500 trace id, `service_map.go:302`) — MV-bypass · `/servicegraph`: `topology_edges_5m FINAL` LIMIT 20000, hop başına **ardışık** sorgu (1..3) (`topology.go:1483-1500`) | 30s |
| **Problems / Exceptions** (`/problems`) | `GET /api/exception-groups` (poll yok) · `/api/problems` · `/buckets` · `/evaluator` · sidebar `count` ×2 | `exception_groups FINAL` ×2 (sınır yok, 3000 satır) · `problems FINAL … max_execution_time=8` | 5s / 15s |
| **Metrik dashboard** (`/dashboard?id=`) | **tek** `POST /api/dashboards/data` (≤50 panel) · bundle dışı paneller tek tek | `spanmetrics_*` / `operation_summary_5m` / ham `metric_points` (rollup YOK) | **cache YOK** (api.ts:2251 yorumu bayat) |

Frontend RUM yok (yalnız DEV `usePerfStats`); CH sorgularında `log_comment`
/`query_id` yok → `query_log` ↔ uç bağı yalnız metinle.

## 2. Taban ölçüm (lokal, medyan of 3)

### 2.1 Sunucu tarafı — TTFB

| # | Uç · senaryo | Soğuk | Sıcak (HIT-L1) | Gövde | CH (initial query) |
|---|---|---|---|---|---|
| 1 | `GET /api/services` | **0.30 s** (0.28–0.44; ilk MISS 2.02 s) | 8–20 ms | 13 KB | — |
| 2 | `GET /api/problems` | **0.59 s** (0.57–0.76; bir MISS 2.90 s) | 20–132 ms | 66 KB | `problems FINAL` 6.55k satır/1.8 MiB, p50 612 ms |
| 3 | `GET /api/traces` MV, count=skip, **1h / 6h / 24h** (from/to açık) | **0.47 / 0.44 / 0.58 s** (0.23–0.86; bir 3.1 s aykırı) | 160–280 ms | 10 KB | aşama-1 13.7k satır/357 KiB (1530 id) + aşama-2 5–28k satır/2–2.7 MiB → 51; **pencereden bağımsız** |
| 4 | `GET /api/traces` + terfi filtre `channel_code='010101'` (%96 eşleşir), 1h | **0.37 s** (0.23–0.43) | 82 ms | 10 KB | `attr_channel_code = '010101'` 127k satır/8.9 MiB → 51 (2.500:1) |
| 5 | `GET /api/traces` + terfi filtre eşleşmeyen (`function_code='zzz'`), 1h / 24h | **0.08 / 0.07 s** | — | 29 B | **0 satır** — `idx_attr_function_code set(0)` granülü eliyor |
| 6 | `GET /api/traces` HAM tam pencere, **24h** (bkz. §2.3) | **6.2 s** (4.2–10.3) | — | 10 KB | **1.57 M satır / 107 MiB → 51 (30.800:1)**; EXPLAIN: MinMax 2105→105 granül, PrimaryKey `Keys:` BOŞ, 105/105 |
| 7 | `GET /api/traces?traceIds=50&extraAttrs=channel_code,function_code` (kolonlar) | **0.70 s** (0.59–1.05) | — | 4 KB | EXPLAIN (1h): MinMax 19/2105 granül, `idx_trace` 8/19 → **sayfadaki 50 trace'e sınırlı** |
| 8 | ” dizi anahtarları (`http.user_agent,net.peer.name`) | **0.37 s** (0.27–1.48) | — | 4 KB | aynı sınır, 4 dizi dekompresyonu |
| 9 | `GET /api/traces/{id}` (3 id) | **0.32 s** (0.25–1.15) | 15–95 ms | 8–13 KB | `idx_trace` |
| 10 | `GET /api/servicegraph?focus=api-gateway&hops=2&top=500` 1h | **1.67 s** (1.38–2.58) | 170–190 ms | 32 KB | `topology_edges_5m FINAL` 13.3k satır/468 KiB × 2 hop ardışık (p50 ~600 ms/sorgu lokalde) |
| 11 | `GET /api/service-map?since=1h` | **1.16 s** (1.00–1.55) | 150–180 ms | 18 KB | HAM `spans` örnekli (MV-bypass) |
| 12 | `POST /api/dashboards/data` preset-dependencies, 12 spanMetric, 1h | **2.20 s** (2.07–2.35) | cache yok | **1.71 MB** | 12 paralel `operation_summary_5m`/`spanmetrics` |
| 13 | `POST /api/spans/metric-batch` (traces grafikleri) 1h / 24h | **0.10 / 0.19 s** | — | 7–9 KB | `operation_summary_5m` 278k satır/10–42 MiB |
| 14 | `GET /api/exception-groups` | **0.39 s** (0.26–0.47) | 86 ms | 19 KB | `exception_groups FINAL` ×2 |

Yan bulgu: 3 no'lu noktada `HIT-L1` 160–280 ms — süreç-içi cache isabeti için
yüksek (`/api/services` HIT-L1 8–20 ms); serializasyon/port-forward payı
ölçülmeli (AŞAMA 3 harness `size`+`ttfb` ayrımıyla yakalar).

### 2.2 Bilinen yavaşlıklar — doğrulama

**(a) Attribute kolonları → tüm aralık taranıyor mu? HAYIR (kolon), EVET (filtre).**
Kolon açmak liste sorgusunu YENİDEN KOŞTURMAZ (`Traces.tsx:471-475`); extras
çağrısı sayfadaki 50 id + satırların gerçek min/max ±5 dk ile sınırlı
(`repo.go:2671-2705`), EXPLAIN 2105 granülden 19'a, bloom ile 8'e iniyor
(#7). Terfi kolonu (`attr_channel_code`) `anyIf` ile okunuyor; terfisiz
anahtar 4 dizi dekompresyonuna düşüyor (#8; traces-evidence §6: 4.3× bayt).
**Filtre** ise MV yolunu diskalifiye edip (`tracesMVEligible`,
`repo.go:2140`) HAM `GROUP BY trace_id`'ye düşürüyor: terfi kolonu + skip
index varsa ucuz (#4, #5); terfisiz anahtar ya da (bkz. §2.3) düşen filtre
tüm pencereyi tarıyor (#6).

**(b) Servis haritası — düğüm sayısına göre.** Sunucu: `hops` başına ardışık
`FINAL` okuması (1.67 s soğuk, 2 hop), `compare=prior` ikiye katlar.
İstemci: yerleşim `TopologyFlowGraph.tsx:194-250` — BFS her seviyede tüm
kenarları tarıyor, barycenter **sort comparator'ın içinde** `edges.filter`
(O(E·n log n)). SSR benchmark (`TopologyFlowGraph.perf.test.tsx`,
react-dom/server, boya hariç, 3 koşu medyanı):

| düğüm / kenar | 50/148 | 100/496 | 300/2.994 | 500/9.980 | **500/19.960 (sunucu tavanı)** |
|---|---|---|---|---|---|
| yerleşim+SVG | 5.1 ms | 13.4 ms | 189 ms | 940 ms | **1.906 ms** |

Sunucu tavanı (500 düğüm/20k kenar) tam dolduğunda **istemci ana iş
parçacığı 1.9 s**, ve bu 30 sn'lik her poll'da yük kimliği değişince
yeniden koşuyor → tavanda darboğaz **istemci**; küçük haritada sunucu.

**(c) Çoklu sekme SSE / HTTP-2.** Lokalde ingress yok (port-forward) →
**ölçülemedi**. Kod: `/api/events` origin başına **1** bağlantı (Web Locks
lider + BroadcastChannel, `eventStream.ts:49-131`), logs tail sekme başına
1 ve `document.hidden`'da kapanıyor; sunucuda abone tavanı yok; 15 sn
heartbeat HAProxy 30 sn'yi geçmez. h2 durumu prod'da yalnız operatör iş
istasyonundan `curl --http2 -sS -o /dev/null -w '%{http_version}'
https://<host>/api/health` ile anlaşılır — `docs/audit/sse-http2-multitab-audit.md`
§2 karar ağacı geçerli; route wildcard-cert'te olduğu için h2 pazarlığı
yapılmıyor olabilir (§1 ek bulgu). Bu nokta bütçeye **girmez** (ölçülemeyen
eşik olmaz); harness "SSE açıkken 6 paralel XHR" senaryosunu h1 tavanını
yakalamak için ölçer.

### 2.3 Yan bulgu — geçersiz operatör filtreyi SESSİZCE düşürüyor

`filters=[{"k":"http.user_agent","op":"contains",…}]` → `contains`
`allowedOps`'ta yok; `FilterExpr` diziye girer (MV diskalifiye,
`len(f.Filters)>0`) ama SQL'e yüklem üretmez → **filtresiz** ham tam-pencere
taraması (#6) ve **filtrelenmemiş 51 satır** döner. Hem doğruluk hem
maliyet; `parseFilters` op'u parse anında reddetmeli (400). → `/kuyruk`
bug kalemi (bu dokümanın kapsamı dışı, ölçümü burada).

### 2.4 Bugün ne ölçebiliyoruz (self-observability)

- `otelhttp` sunucu span'ı her `/api/*` için var (`api.go:1393`), ad =
  `METHOD + collapseRoute` (`GET /api/traces`, `GET /api/traces/:id`);
  Coremetry kendi OTLP alıcısına yazıyor (servis
  `coremetry-monolithic|api|ingest|worker`, %10 örnekleme). **Uç başına
  p95 SERİSİ bugün sorgulanabilir:**
  `SELECT time_bucket, countMerge(calls_state),
  quantilesTDigestMerge(0.95)(duration_q_state)[1]/1e6 FROM spanmetrics_1m
  WHERE service_name LIKE 'coremetry%' AND name='GET /api/traces' …` —
  lokalde doğrulandı (dakikalık kovalar, p95 2.6 s / 1.9 s / 0.57 s).
- Eksikler: `http.server.duration` metriğinde `http.route` yok (yalnız
  span'da); `X-Cache` span attribute'u değil (soğuk/sıcak ayrımı yok);
  CH sorgularında `log_comment` yok; frontend RUM/TTFI yok.

### 2.5 Darboğaz: sunucu mu istemci mi (veriyle)

| Ekran | Karar | Kanıt |
|---|---|---|
| Traces listesi | **Sunucu (CH)** — yalnız terfisiz filtre / düşen filtre senaryosunda | #3 pencereden bağımsız 0.5 s; #6 1.57 M satır 6.2 s |
| Trace detay | Sunucu soğuk 0.3 s, sıcak 15 ms — bütçede; istemci N+1 (4 istek/seçim) ölçülmedi (tarayıcı ister) | #9 |
| Servis haritası | Küçük harita: sunucu (1.2–1.7 s, ardışık hop + MV-bypass); **tavanda: istemci (1.9 s yerleşim)** | #10, #11, §2.2(b) |
| Problems/Exceptions | Sunucu — `FINAL` CPU'ya bağlı, lokalde 0.6 s; cache 15 s koruyor | #2, #14 |
| Dashboard | Sunucu 2.2 s **ve** 1.7 MB gövde (istemci parse/çizim ölçülmedi) | #12 |

## 3. AŞAMA 2 — Bütçe önerisi (≤6 nokta, p95, lokal fixture)

Koşul: 24h ≈ 1.5 M span · 100 servis · demo yükü; soğuk = `refresh=1`;
5 koşu, karar **medyan**, p95 bilgi. Eşikler §2 ölçümünden türetildi
(ulaşılabilir = bugünkü medyanın ≈1.5×'i, gürültü payıyla; arzu = darboğaz
giderilince beklenen). Ad = self-telemetry span adı + senaryo eki.

Taban = **perfcheck düzeltilmiş koşusu** (2026-08-28 01:4x, 5 soğuk koşu,
her koşu 61 sn kaydırılmış pencere, 24h 1.46 M span / 100 servis,
`perf/out/baseline-2026-08-28.json`); §2'deki curl sayıları ilk keşfin
sayılarıdır ve iki tuzak taşıyordu (aynı SQL tekrarı → 0 satır; `range=`
yok sayılıp varsayılan pencere). Ulaşılabilir eşik = taban medyanının
≈1.5×'i (gürültü payı); karar medyan üzerinden, p95 bilgi.

| # | Nokta (span adı · senaryo) | Taban p50 (p95) | Ulaşılabilir (p50 eşiği) | Arzu | Darboğaz → gereken değişiklik |
|---|---|---|---|---|---|
| P1 | `GET /api/traces` · `mv-24h` (count=skip, filtresiz) | 334 ms (1.1 s) | **≤ 600 ms** | ≤ 300 ms | CH iki-aşama; lokal CH taban gecikmesi. Arzu için aşama-1 dilimini `trace_service_index_5m`'e bağlı küçültme |
| P2 | `GET /api/traces` · `raw-attr-filter-24h` (terfisiz attribute filtresi, geniş eşleşme `user_agent.original LIKE %`) | 4.19 s (5.6 s) | **≤ 6.5 s** | ≤ 1 s | Ham `GROUP BY` tam pencere (dizi yolu 1.54 M satır/273 MiB). Arzu: ham yola **yenilik dilimi** (önce son N id, gerekirse genişlet) ve operatörün sıcak anahtarları terfi + `set(0)` index (channel/function_code emsali: 0 satır) + §2.3 op reddi |
|    | ↳ **v0.10.126 yenilik dilimi GEMİDE** (`trace_raw_probe.go`): zaman-DESC listede önce 1h(+1h lookback) → 6h → tam pencere; K=offset+limit+1 satırın hepsi floor üstünde başlamışsa sayfa aynı. Ölçüm (lokal, query_log): liste sorgusu **2.146 M satır / 487 MiB / 1.4–3.8 s → 603 k satır / 137 MiB / 0.26–1.1 s** (satır 3.6× az); sayfa içeriği önce/sonra bire bir aynı (id/span/süre/başlangıç, 5 varyant). Bedel: hiç eşleşmeyen filtrede 3 tarama (2h+7h+tam) ≈ +%63 satır (+0.8 s). perfcheck p50 4.19 s → 0.31 s. Arzu eşiği (≤1 s) tutuldu; terfi+skip index kalemi ayrı. |
| P3 | `GET /api/traces` · `extras-50-2cols` | 377 ms (0.7 s) | **≤ 600 ms** | ≤ 300 ms | `idx_trace` bloom G4 (8/19 granül) — zaten sayfaya sınırlı; arzu için `trace_summary_5m`'e terfi kolon boyutu (spec ister) |
| P4 | `GET /api/servicegraph` · `focus-2hop-top500` + istemci yerleşim `500/20k` | 2.30 s (2.7 s) / 1.9 s | **≤ 3.5 s / ≤ 4 s** | ≤ 0.5 s / ≤ 0.3 s | Sunucu: hop sorguları ardışık + `FINAL` → tek 2-hop sorgu ya da paralel hop; istemci: barycenter comparator'ında `edges.filter` → komşuluk indeksi (O(E+n log n)) |
|    | ↳ **v0.10.133 istemci yarısı GEMİDE** (`lib/topoBfsLayout.ts`, çıktı referansla birebir pinli): 500/20k **1 716 → 336 ms**, 500/10k 895 → 199 ms, 300/3k 187 → 83 ms (SSR probe; kalan 20k kenarın SVG dizesi). Probe eşiği 4 s → 1.5 s. **Sunucu yarısı ERTELENDİ — ölçüm gerekçesi:** aynı gün boş VM'de 2-hop isteği 260–490 ms; hop sorguları 50–190 ms, her biri pencerenin tamamını (17.6k satır, `FINAL`) okuyor — frontier `IN` süzgeci ORDER BY (time_bucket, …) yüzünden budamıyor. Bütçe içinde; tek-sorgu/paralel hop değişikliği prod-ölçek `query_log` kanıtı (satır/pencere) gelince. |
| P5 | `POST /api/dashboards/data` · `preset-dependencies-1h` | 1.89 s (2.8 s) / 1.62 MB | **≤ 3 s, gövde ≤ 2 MB** | ≤ 1 s, gövde ≤ 500 KB | Cache yok + panel başına tam seri; `maxDataPoints`/downsample + serveCached (anahtar: gövde hash) |
|    | ↳ **v0.10.146 cache dilimi GEMİDE; gövde AÇIK.** Ölçüm düzeltmesi: perfcheck probe'u `step=60` gönderiyordu, FE 1h/600 px panelde **15** gönderir → gerçek gövde **5.0 MB** (666 seri × ~240 nokta × 50 B/nokta; peer/caller panelleri 95-96 seri, 0.6-0.8 MB/panel), soğuk 1.3-2.0 s, sıcak = soğuk (cache yok). Probe step 15'e çekildi, tavan 6 MB (arzu 500 KB duruyor). Gemide: `cachedJSON` (serveCached'in HTTP'siz çekirdeği, tek yerde; `computeBody` sf içinde marshal → HEAD'deki veri yarışı + `null`-cache gizli hataları kapandı), bundle her paneli `dashPanelKey` (tüm girdiler + 30 s kova) ile 30 s SWR'lar, hata slot'u cache dışı, boş sonuç `series: []`, grid edit modunda override geçmez (canlı önizleme). **Top-N kırpması REDDEDİLDİ** (inceleme doğruladı): dashboard `foldTopN` "others" çizgisini TAM kuyruktan toplar; top-50 girdiyle kuyruk kütlesi sessizce kaybolur — tekil uç/fallback zaten böyle (HEAD). **Gövde kazancının doğru yolu = kuyruk ön-toplamları** (QuerySpanMetricTopN top-N + zaman-başı othersSum/othersCount, birim-bağımsız; FE kesin katlar): 95 seri → 8+others ≈ **10×** küçülme, tekil uç da düzelir — SPEC. Sonra: nokta kodlaması (50 B/nokta). |
|    | ↳ **v0.10.146 ölçüm sonucu (2026-08-29).** Cache DOĞRULANDI: aynı 30 s kovasında ardışık çağrılar MISS 3.8 s → HIT 0.3 s (sakin), 6.8 s → 1.5 s (yüklü; HIT süresinin tamamı 5 MB'ın port-forward transferi). Soğuk kıyas GEÇERSİZ: "önce" kampanyası (6 bundle = 72 sorgu × 100k satır / 2 dk) 3-CPU'luk lokal CH'yi doyurdu (clickhouse-go select cpuwait p50 07:30'da 8 ms → 07:34'te 553 ms), deploy + perfcheck + tekrar ölçümler kümeyi geri getirmedi; uygulamanın anomali dedektörü yavaşlayan CH'yi problem yapıp RCA sorguları tetikledi (kendi kendini besleyen fırtına). query_log: aynı 12 sorgunun CPU süresi 145/146'da eşit (p50 60 vs 77 ms), fark tamamen CPU beklemesi; tekil uç üzerinden cachedJSON yolunun app payı ≈ 0.4 s (CH 3.5 s / uç 3.9 s, 0.5 MB dahil). Redis 0.7 ms ort. Kural: before/after arasında sakinlik teyidi (cpuwait_p50 < 20 ms) — [memory: feedback-perf-benchmark-discipline]. |
|    | ↳ **v0.10.147 kuyruk ön-toplamları GEMİDE.** Sunucu top-N (bundle 16, tekil uç 50) + kırpılan kuyruğun zaman-başı ham sum/count'u (`tail`); FE `foldTopN` kendi kuyruğuna ekler → "others" çizgisi ve notu TAM seriyle katlamaya eşit (eşdeğerlik testi, sonlu olmayan nokta dahil — tel kuralı: 0 sayılır). Sunucu birim bilmez. Explore dokunulmadı. Ölçüm (2026-08-29, tek soğuk çağrı, FE step 15): gövde **5.2 MB → 1.31 MB (−75%)**, 666 → 122 seri; gruplu panellerde 16 seri + 241 noktalık tail, totalSeries 95/96/84; stat panelleri değişmedi. Süre yine deploy-sonrası fırtınada (cpuwait p50 ~1 s) — kıyas yapılmadı; HIT 1.5 s = 1.3 MB port-forward transferi. Tavan 6 MB → 2 MB (arzu 500 KB: kalan kol nokta kodlaması ~50 B/nokta → ~25 B, ayrı karar). |
| P6 | `GET /api/problems` · `cold` | 1.54 s (2.3 s) | **≤ 2.5 s** | ≤ 200 ms | `problems FINAL` 6.5k satır — CPU/FINAL, lokal VM'de 0.6–1.5 s oynak; arzu: `OpenProblemsSnapshot` emsali (v0.9.691) ya da `FINAL`'sız argMax okuma |
|    | ↳ **v0.10.144 ÖLÇÜLDÜ — KAPANDI, kod değişikliği YOK.** Reçete (`FINAL`'sız argMax / `LIMIT 1 BY id`) geri çekildi. Kanıt (2026-08-29, minikube chc-0, `problems` 7 034 satır / 21 part / 740 KiB): aynı `FINAL` sorgusu `clickhouse-client`'tan **13–100 ms**; `SELECT … FROM (SELECT * FROM problems ORDER BY version DESC LIMIT 1 BY id)` **94–197 ms (daha YAVAŞ)**; `FINAL`'sız kirli tarama 26–87 ms. Uygulamanın aynı şekildeki sorguları `query_log`'da 1.0–3.0 s, ama `OSCPUWaitMicroseconds` **1.4–4.4 s** / `OSCPUVirtualTime` **60–290 ms** — süre %90+ CPU KUYRUĞU (chc-0 2.0 çekirdekte: ingest+MV+merge+evaluator), FINAL birleştirmesi değil; `count() FROM problems FINAL` bile 1.5 s. Aynı gün perfcheck 3 koşu p50 775 ms / p95 1.2 s (`perf/out` dışı, scratch). Sonuç: arzu eşiği (≤200 ms) prod CPU sorusu — prod `query_log` (SELECT-only): `SELECT round(quantile(0.5)(query_duration_ms)) p50, round(quantile(0.5)(ProfileEvents['OSCPUWaitMicroseconds']/1000)) cpuwait_p50, count() FROM system.query_log WHERE event_date >= today()-1 AND type='QueryFinish' AND query LIKE 'SELECT id, rule_id%FROM problems FINAL%'` — cpuwait_p50 ≪ p50 ise o zaman yeniden aç. |

Bütçeye GİRMEYENLER (ölçülemedi): TTFI/istemci çizim (tarayıcı gerekir —
Playwright yalnız operatör isterse), SSE/h2 (ingress yok), trace-detay N+1.

## 4. AŞAMA 3 — Otomasyon tasarımı (onaylı, uygulama sırada)

- **`cmd/perfcheck`** (Go, yeni bağımlılık YOK): `scripts/perf/budget.json`
  okur (nokta adı, istek, koşu sayısı, eşikler), login olur, her noktayı
  **ardışık** K soğuk (`refresh=1`) + K sıcak koşar, `httptrace` ile TTFB
  ölçer, `X-Cache` doğrular (soğuk koşuda BYPASS değilse nokta geçersiz),
  medyan/p95/max hesaplar, **JSON** yazar (`-out`), `-compare önceki.json`
  ile fark yüzdesi basar, eşik aşımında `exit 1` + anlamlı mesaj
  (`P2 raw-attr-filter-24h: p50 6.2s > ulaşılabilir 3.0s (önceki 5.9s,
  +5%)`). Saf çekirdek `internal/perfcheck/` (istatistik, eşik, kıyas,
  veri-seti sapması) `go test -race` ile tablo-testli.
- **Veri-seti parmak izi**: `/api/spans/metric?agg=count` 24h toplamı +
  `/api/version` JSON'a girer; önceki koşuya göre hacim %20'den çok
  sapmışsa kıyas **uyarı** olur (fail değil) — farklı veriyle kıyas yalan söyler.
- **İstemci noktası**: `TopologyFlowGraph.perf.test.tsx` (vitest, SSR)
  kalıcı; eşik 500/20k ≤ 4 s (CI makinesi payı ×2), medyan of 3.
- **CI önerisi — GECELİK**, PR başına DEĞİL: nokta canlı yığın + sabit
  fixture ister (minikube/compose + `cmd/demo` sabit rps ≥ 2 saat ısınma);
  PR'da yalnız saf testler + vitest yerleşim eşiği koşar. Gürültü: 5 koşu,
  karar medyan, tolerans %25 (hem bütçe aşımı hem önceki koşuya göre +%25
  → fail; yalnız biri → warn), ilk koşu ısınma sayılıp atılır, noktalar
  ardışık (eşzamanlı yük yok), taban = son 3 gecenin medyanı.
- **Prod ile ilişki**: nokta adı = `spanmetrics_1m.name` (span adı);
  `scripts/perf/prod-compare.sql` aynı adla prod p95'i çeker → aynı tabloda.
- Ortam hazırlığı: `docs/perf/perf-budget-2026-08-28.md` §0 + `make
  perfcheck` (port-forward, login, budget.json, JSON çıktı `perf/out/`).
  Prod kümesine/verisine **yazma yok**; prod yalnız `SELECT` ile okunur.

### 4.1 Gemideki dosyalar (v0.10.116)

| Parça | Yol |
|---|---|
| Koşucu | `cmd/perfcheck/main.go` — `go run ./cmd/perfcheck -budget scripts/perf/budget.json [-compare önceki.json] [-out …]` |
| Saf karar çekirdeği | `internal/perfcheck/` (`Percentile`, `Evaluate`, `ValidateCold`, `DatasetDriftPct`, `Tally`) + tablo testleri |
| Bütçe | `scripts/perf/budget.json` — 6 nokta, eşikler §3'ten |
| Prod kıyası | `scripts/perf/prod-compare.sql` — aynı nokta adlarıyla `spanmetrics_1m` p50/p95 |
| İstemci noktası | `frontend/src/components/TopologyFlowGraph.perf.test.tsx` (vitest, eşik 500/20k ≤ 4 s) |
| Make | `make perfcheck [PREV=perf/out/x.json]`; çıktı `perf/out/<zaman>.json` (gitignore) |

**Ölçüm tuzağı (v0.10.117'de kapatıldı):** ilk taban koşusunda aynı SQL
metni art arda koşunca ikinci koşu `query_log`'da **0 satır / 16 ms**
okudu (CH `use_query_cache=0`, app kullanıcısı `default`, mekanizma
teşhis edilmedi) — `refresh=1` yalnız Coremetry cache'ini atlıyor.
perfcheck her soğuk koşuda pencereyi 61 sn geriye kaydırır (SQL metni
farklı, veri yoğunluğu aynı); `measure.sh` ile elle ölçerken `from/to`'yu
her koşuda değiştir, yoksa "soğuk" p50 yalan söyler.

**Oynaklık uyarısı (üç ardışık taban koşusu, 2026-08-28):** aynı fixture,
aynı harness, 10 dk arayla — P6 `problems` 1.54 s → 113 ms, P5 bundle 1.89
s → 520 ms, P4 2.30 s → 799 ms, P2 4.19 s → 3.01 s. Lokal VM'de ±3×
sallanma normal (evaluator/merge/demo yükü). Bu yüzden karar ÇİFT koşullu
(bütçe aşımı VE önceki koşuya göre gerileme), tolerans %25, taban = son 3
gecenin medyanı — tek koşuya bakıp eşik değiştirme. Son koşu
(`perf/out/baseline-2026-08-28.json`): 6/6 geçti.

Ortam hazırlığı (tekrar edilebilir taban): minikube `chc-0/1` + demo
üretici en az 2 saat koşmuş (24h penceresi dolu değilse `datasetDrift`
uyarısı kıyası düşürür), `kubectl port-forward -n coremetry svc/coremetry
8090:8088`, `admin@coremetry.local/admin` (COREMETRY_PERF_EMAIL/PASSWORD
ile ezilir). Noktalar ardışık koşar; aynı anda başka yük (loadtest,
vitest) koşturma. İlk koşu taban olur; sonrakiler `PREV=` ile kıyaslanır.
