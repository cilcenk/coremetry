# Performance pitfalls — historical incidents

Full narratives behind the one-line pitfall rules in CLAUDE.md.
Each entry references the incident-shaped fix. Avoid re-living them.

- **`timeRangeToNs(range)` in JSX / IIFE on every render** —
  re-evaluates `now()`, breaks `useEffect` dep equality, infinite
  refetch (v0.5.184). Always `useMemo([range])` or call inside
  `useEffect` body where deps are explicit.
- **Cache key = `len(set)`** — cross-set poisoning where two
  different sets sharing the same cardinality return each
  other's data (v0.5.187). Stable digest required.
- **`table-layout: fixed` + `white-space: nowrap` + small fixed
  width** — silently clips text. Use `min-width` + `max-width`
  + `ellipsis` + `title` attribute for tooltip.
- **ES `query_string` with `case_insensitive: true`** — rejected
  by ES 8.x as an unknown field (v0.5.231). Don't add it back.
  Standard analyzer already case-folds.
- **Per-pattern `_search` over N curated patterns** — N
  round-trips. Use `_msearch` for a single coordinator fan-out
  (v0.5.241).
- **`significant_text` without `background_filter`** — ES
  defaults the background to the whole index = catastrophic on
  billion-doc indices. Always cap baseline window AND wrap in
  `sampler` to bound per-shard scoring (v0.5.243).
- **Drain-style templating against raw logs at billion-row
  scale** — sample-based puller (1000/5min) > full scan. Sample
  bias on rare templates is fine because the curated detector
  + significant_text panel pick those up (v0.5.244 architecture
  note).
- **Polling without `document.hidden`** — burns mobile/laptop
  battery + idle API traffic. See PublicStatus.tsx pattern.
  Always:
  ```js
  setInterval(() => { if (!document.hidden) fetchOnce(); }, 30_000);
  ```
- **Unit-mixing in SQL/time templates (`toDate(time) + INTERVAL %s`
  with `%s` ∈ {"30 DAY", "1 HOUR"})** — `toDate(time) + INTERVAL 1
  HOUR` = midnight + 1h = 01:00 of the SAME day, not "1 hour from
  the row's time". v0.6.36: retention.spans = "1h" via admin UI
  silently TTL'd every span after 01:00 — operator saw inconsistent
  /traces counts because merges ran intermittently. Rule: ANY
  template that accepts a value+unit (Nh/Nd, ms/s, MB/GB) MUST
  have a table-driven test exercising **every** unit at ship time.
  For sub-day TTLs use `<col> + INTERVAL N HOUR` (row-level); for
  day TTLs use `toDate(<col>) + INTERVAL N DAY` (partition-aligned).
  Never let `toDate()` wrap a sub-day calculation. See
  [internal/chstore/retention_test.go](../internal/chstore/retention_test.go)
  for the canonical example.
- **Combined-MV DROP at billion-row scale** — `DROP TABLE <mv>`
  trips CH's `max_table_size_to_drop` guard (default 50 GB) once
  the hidden `.inner_id.<uuid>` storage is large, and a per-query
  `SETTINGS max_table_size_to_drop=0` on the MV does NOT reach the
  inner drop (verified on 24.8). Drop the inner DIRECTLY with the
  override, then the empty MV (`dropCombinedMV`, v0.8.190). Every
  boot migration that recreates an MV routes through it.
- **Reservoir `quantilesState` for `duration_q_state` at scale** —
  the 8192-sample reservoir is ~64 KiB/row; merging a wide window
  blows the per-query memory limit (code 241) + the timeout (code
  159). The summary MVs use `quantilesTDigestState` (~4.3 KiB/row,
  parallel-safe, ≤2% error, v0.8.194) — the concrete form of the
  "quantile() past ~1M rows → TDigest" rule. Never put reservoir
  `quantiles` in an MV state column.
- **MV aggregate-type change = rolling-deploy read-error window** —
  an atomic MV state-type swap at boot means OLD pods read the NEW
  MV with the old finalizer (`quantilesMerge` on a TDigest column →
  code 43) until they roll. `maxUnavailable:0` keeps the rollout
  graceful but lengthens the window; finish the rollout fast (no
  `dev`-tagged stragglers), or use a dual-column transition to
  avoid it (v0.8.194).
- **Hardcoded database name in SQL** — `FROM coremetry.spans`
  breaks on any install whose CH database isn't literally
  `coremetry` (e.g. `coremetry_prod`) → "Database coremetry does
  not exist" (code 6, v0.8.195). The chstore conn defaults to
  `cfg.Database`, so reference telemetry tables UNQUALIFIED.
- **`toDateTime64('<RFC3339>', 9, 'UTC')` rejects the trailing
  `Z`** — `time.RFC3339Nano` emits `…Z`; CH's DateTime64 string
  parser errors at the Z (code 6, "Cannot parse string … as
  DateTime64", parsed-just-`…714`). Format UTC with a space and NO
  tz designator (`chDateTime64Arg`, v0.8.197). Any `toDateTime64(?)`
  bind arg MUST be tz-less.
- **Collector wedge after a coremetry rollout** — the OTel
  collector's gRPC exporter resolves to "zero addresses" when the
  headless ingest Service's ready endpoints drain during a roll
  (default `maxUnavailable: 25%` + slow boot) and stays wedged
  (503s every app's telemetry — only coremetry's own self-obs keeps
  landing) until the collector is restarted. Fix: explicit
  `maxUnavailable: 0` on the Deployment so endpoints never hit zero
  (v0.8.193). Pre-fix installs: restart the collector once.
- **Service filter + long window: stage 2 merged the whole window**
  (v0.10.265, operator-reported on prod 7d + service + hasError, time
  sort: every "Next" 40+ s and a "shortened to …" badge). The MV list
  path had two service shapes and both were linear in the window:
  post-agg filters (hasError/rootOnly/min/max) ran stage 2 with
  `trace_id GLOBAL IN (index GROUP BY trace_id)` — the set prunes
  nothing when the service touches most traces (gateway: 409k of 7d
  traces locally), so trace_summary_5m was merged end to end (1.7-2.0M
  rows, 12 s cap, two halvings); the plain shape ran `GROUP BY
  trace_id ORDER BY maxMerge(last_seen)` over the whole service index
  (800k rows, 10-15 s) and ignored `order=asc`. Fix: a recency slice
  on `trace_service_index_5m` in primary-key order (service_name,
  time_bucket, trace_id) with `optimize_read_in_order` and no GROUP
  BY — the same contract as the no-service `traceRecencySlice` —
  feeding the already-bounded stage 2 (`trace_id IN (ids)` + floor at
  the cut for desc; asc keeps the window floor because its ids sit at
  the window start). Lesson: a `trace_id IN (subquery)` bound is only a
  bound if the subquery is small; when the entity is a hub (gateway,
  root service) it is the whole window in disguise — bound by TIME
  (PK-ordered slice), never by set membership. Regression pin:
  `trace_service_slice_test.go`.
- **ClickHouse integer promotion bites the scanner** (v0.10.269):
  `toUInt64(x) - ?` with a UInt64 bind is promoted by ClickHouse to
  Int64 (unsigned minus unsigned is signed), and `intDiv(Int64,
  UInt64)` stays Int64 — scanning it into a Go `uint64` fails at
  runtime with "converting Int64 to *uint64 is unsupported" while
  every unit test stays green (no CH in tests). Scan arithmetic
  results into `int64` and clamp, or cast explicitly in SQL
  (`toUInt64(intDiv(...))`). Same class as the tz-less DateTime64
  bind: the type is decided by the server, not by the Go side.
- **Errors + attribute filter + service: whole-service GROUP BY, halved
  window, "No traces found"** (v0.10.307, operator-reported on prod: 6h
  with Errors + `name = POST` returned rows, 3h with the same filters
  returned nothing although every row sat inside the 3h window; "it has
  happened before"). An attribute filter forces the raw path; with
  another span predicate present, `hasError` is not span-local (the
  trace must have an error span AND a span matching the filter, possibly
  different spans), so Errors moved into HAVING and stage 1 grouped
  every trace of the service in the window — `idx_status` pruned
  nothing. On prod that blew the 25 s stage-1 budget,
  `narrowOnExhaustion` halved the window and the page came back empty
  with a `narrowedFromNs` the UI never showed. Fix: error-first
  candidate narrowing (`trace_error_first.go`) — a cheap
  `status_code = 'error'` scan in primary-key order (service_name,
  time) returns the service's error trace ids (cap 5000, `rankedWithin`
  set when hit); every later stage carries `trace_id IN (ids)` through
  `TraceFilter.CandidateIDs`, so GROUP BY sees at most the candidates
  and an empty answer is exact ("no error traces") rather than "budget
  ran out". `TracesEmpty` now says "No traces in the shortened window"
  when `narrowedFromNs` is set. Local measurement (api-gateway 3h,
  fixture where every granule carries errors and the window is two
  index granules): stage 1/2 unchanged at 65k rows, the extra pass
  costs ~0.7 s — the fixture cannot show the prune; the prod shape (a
  handful of error traces in a hub service) is where the `idx_trace`
  bloom and the bounded GROUP BY pay, and a candidate set in the
  thousands saturates the bloom (only the GROUP BY bound remains).
  Lesson: when a filter cannot be expressed on the primary key, find
  the cheapest predicate that IS on it and let that drive the candidate
  set; and an empty page under a narrowed window is a budget statement,
  never a fact — say so in the UI. Regression pins:
  `trace_error_first_test.go` (eligibility, SQL shape, order-of-
  operations source pin). v0.10.308 fixed the pin itself: a substring
  assertion on `name = ?` matched `service_name = ?`, and the 307 gate
  chain read a missing test-output file as "no FAIL" — negative greps
  are not gates without positive evidence (`[ -s f ] && grep -q '^ok'`).
- **Traces strip empty under a non-entry-span filter** (v0.10.323,
  operator-reported on prod: `service.name = X AND db.statement ~ …`
  listed rows while the volume strip said "No traces in view to
  bucket"). Since v0.10.268 the strip counts entry spans (`kind IN
  (server, consumer)`) and ANDs the operator's filters onto the SAME
  span; the table applies them trace-wide (any span of the trace).
  `db.statement` never lives on an entry span, so the strip's predicate
  was unsatisfiable. Fix: `stripScope` — when a filter targets a key
  that does not live on entry spans (db., messaging., custom
  attributes) or a free-text search is present, the strip counts
  MATCHING SPANS instead (kind restriction dropped, unit "spans",
  header labels and hint say so; the median is then the span's own
  latency — exactly the question a db.statement filter asks). Entry-span
  keys (service/http/url/status/cluster/channel_code…) keep the trace
  count. Lesson: two surfaces sharing one filter must share its
  SEMANTICS (span-local vs trace-wide), or the cheaper one must say what
  it counts. Pin: `volumeSeries.test.ts` (stripScope table).
- **Waterfall rows vanish past the first screen on 400+ span traces**
  (v0.10.324, operator-reported on prod with a 1065-span trace: ~40 rows
  then blank). v0.10.278 virtualised the waterfall with
  `useWindowVirtualizer`, i.e. it listened to WINDOW scroll — but the
  app never scrolls the window: `#content` (flex:1 + overflow:auto) and,
  on the trace page, `.tc-wf` are the scroll containers. No scroll event
  ever reached the virtualiser, `scrollOffset` stayed 0, only the first
  viewport + 20 overscan rows were ever rendered while the spacer kept
  the full height. jsdom cannot see this (no layout), which is why the
  1000-row virtual test stayed green. Fix: `lib/scrollParent.ts`
  (nearest overflow auto/scroll ancestor + offset within it as
  `scrollMargin`) and a dual-mode `useVirtualizer` — element mode when a
  scroll parent exists, react-virtual's own window composition
  (`observeWindow*` + `windowScroll`) otherwise. Lesson: a virtualiser is
  a contract with ONE scroll container; pin the container choice in
  source (`traceWaterfallVirtual.test.tsx`), because the DOM test cannot.

### v0.10.338 — Çekmecede tam kimlik "…" ile kırpıldı (genel `tbody td` kuralı)

Operator-reported: rollout çekmecesinde revizyon ve imaj (repo:tag) satırları
sonundan kırpılıyordu; aynı workload birden çok cluster'a çıktığı hâlde küme
adı görünmüyordu. Kırpmanın sebebi bileşen değil, `globals.css`'teki genel
`tbody td { white-space: nowrap; max-width: 320px; text-overflow: ellipsis }`
kuralı: hücreye verilen `wordBreak: break-all` nowrap'i kaldırmadığı için
hiçbir etkisi yoktu. Ders: "kırpma" pitfall'ının üçüncü şekli — sınıfsız bir
`<td>` her çekmecede 320 px'e kilitli. Okunması gereken tam metin taşıyan
hücre `.td-full` alır (sarar, kırpmaz) ve yanına kopyalama düğmesi konur;
`RolloutDrawer.pins.test.ts` hücreleri ve CSS kuralını pinler.

### v0.10.339 — Terfi kolonu "var ama bu değeri taşımıyor" (prod, replika farkı)

Operator-reported: /traces'te `channel_code = 060203` çipi eklenince liste
boş; aynı pencerede aynı span'ler çipsiz listede CHANNEL_CODE=060203 ile
görünüyor. Explain (v0.10.326-329): filtre `attr_channel_code = ?` TERFİ
kolonuna derlenmiş, sayım 23 ms'de 0. Boot probe'u (v0.9.621: 50k örnek,
kolon == dizi) geçmişti; yani kolonu doğrulayan örnek ile sorgunun gittiği
replika/part farklı — dağıtık prod'da DROP+ADD onarımının bir replikada
takılı kalması (v0.9.623 "Cannot apply mutation" sınıfı) bu tabloyu verir.
Aynı sebep "6 saat geliyor, 30 dakika gelmiyor" şikâyetini de açıklar:
hangi replikanın cevapladığı isteğe göre değişir.

Ders: **var ≠ dolu ≠ HER REPLİKADA dolu.** Boot'taki tek-örnek doğrulama
yetmez; doğruluk sorgu anında ölçülmeli. Ürün artık boş sonuçta kolon/dizi
sayımını host (replika) başına karşılaştırır, dizi fazlaysa aynı isteği
dizi yoluyla yeniden koşar (doğru ama yavaş), haritayı askıya alır ve
hangi host'un yalan söylediğini zarfa+loga yazar (`[traces] PROMOTED
COLUMN MISMATCH`). Onarım hedefi o host: `system.mutations` takılı mı,
`spans_local` kolon ifadesi iki yazımı da okuyor mu.

### v0.10.341 — Arama + attribute çipi: aynı span tuzağı (v0.10.258'in çip hâli)

Operator-reported: "channel_code bir örnekti; servis + operasyon seçildikten
sonra HERHANGİ bir attribute eklendiğinde aynı sorun." Arama HAVING'de
TRACE düzeyi (`countIf(arama) > 0`), çip WHERE'de SPAN düzeyi: liste ancak
aynı span hem aranan adı hem attribute'u taşıyorsa doluyor. Tablo ise
attribute kolonunu trace'in HERHANGİ bir span'inden gösteriyor
(`traceExtrasProjection` anyIf) — operatör "işte değer orada" diye
bakıyordu, filtre başka span'e soruyordu. v0.10.339'un replika teşhisi
bu vakada tetiklenmez (kolon da dizi de 0: span düzeyinde gerçekten yok).

v0.10.258 aynı tuzağı Errors için çözmüştü (WHERE→HAVING). Şimdi çipler
de arama varken HAVING'e taşınır (`filtersTraceLevel` /
`traceLevelFilterHaving`: çip başına `countIf(<yüklem>) > 0`, OR kökü tek
countIf); arama yokken WHERE'de kalır (terfi kolonu / kvh indeksi budar).
Teşhis sayımı da trace düzeyi. Ders: **bir yüzeyde gösterilen değer hangi
düzeydense filtre de o düzeyde sorulmalı**; aksi hâlde ekran "var" der,
sorgu "yok" der ve ikisi de haklıdır.
v0.10.349: Aggregated sekmesi aynı şekle sahipti (çip WHERE, arama iç
HAVING) — `aggregateChipHaving` ile aynı taşıma; arama yokken SQL bayt-bayt aynı.

### v0.10.355 — MV saklama temizliği hiç çalışmamış: "inner adı backtick'li gelir" yalanı

Self-telemetri yakaladı: coremetry-worker'da `clickhouse.exec` ERROR span'i,
`ALTER TABLE .inner_id.<uuid> ON CLUSTER … DROP PARTITION 2026-08-16` →
code 62 "Syntax error at position 13". İki sözleşme kırığı aynı satırda:
`mvDropPartitionSQL`'in yorumu "inner adı backtick'li gelir" diyordu, testi
de backtick'li girdiyle geçiyordu; ama çağıran `mvInnerTablesCluster` ÇIPLAK
`.inner_id.<uuid>` döndürüyor (exemplar TTL yolu kendi backtick'ini basar).
Ayrıca `mvOldPartitions` partition DEĞERİNİ (`2026-08-16`) okuyup tırnaksız
gömüyordu — Date anahtarında geçersiz. Sonuç: v0.5.320'den beri MV
partition'ları enforcer'la hiç düşmedi, yalnız merge-tabanlı satır TTL'iyle
küçüldü; hata saatte bir loga yazıldı, kimse okumadı.
Ders: builder testi girdinin GERÇEK üreticisiyle beslenmeli — "isim sözleşme
ilan eder, kimse zorlamaz" (feedback-names-assert-contracts). Düzeltme:
builder adı kendisi backtick'ler, `partition_id` + `DROP PARTITION ID '…'`.

### v0.10.357 — Traces yatay kaydırma: sığdırma sanal tabloya hiç ulaşmadı

Operator-reported ("bir türlü düzeltemedik"): /traces tablosu yatayda
kayıyor. `fitColumnWidths` (v0.9.1030) ve "muhafaza kaldırıldı" (v0.9.1334)
düzeltmeleri `DataTableColgroup`'ta kabı `closest('.table-wrap')` ile
arıyordu; sanal tablonun (`VirtualTable`, Traces) kabı `.vt-scroll` →
closest null → ölçüm yok → sığdırma sanal tabloda HİÇ devreye girmedi.
Saf çekirdek testi yeşildi, çağrıldığı yer pinli değildi — v0.9.1334'ün
kendi dersinin (tested-but-unreachable) ikinci örneği, üstelik aynı satırda.
Düzeltme: `closest('.table-wrap, .vt-scroll')` + kaynak pini
(DataTable.contract.test.tsx). Kural: bir düzeltme "kap" seçiyorsa
her kap türü için ulaşılabilirlik pini yaz.

### v0.10.362 — Endpoints metrik kipi servissizde süre aşımı: sırala-sonra-ayrıntı

Operator-reported (prod, varsayılan metrik 361'den bir saat sonra): /endpoints
servissiz + 30 dk + compare → "çok yavaş… Failed to load endpoints". Metrik kipi
ALTI aralık sorgusunu tüm servislerin tüm route'ları için 60 adımla koşuyordu
(compare ile 12): VM'de binlerce seri × 60 nokta × histogram_quantile → süre
aşımı. Operatörün önerdiği sayfalama çözmezdi: sıralama için her route yine
hesaplanmalı. Çare iki aşama — (1) tek kaba sorgu (çağrı, 12 adım) tüm
route'ları sıralar, (2) hata/avg/p50/p95/p99/sparkline yalnız ilk N route için
(`http.route =~ r1|…|rN`; ≤300). Diğer sıralamalar bu havuz içinde; zarf
`pool`/`poolCapped`. Ders: "her seri için her şey" sorgusu N ile değil
seri sayısıyla ölçeklenir; listeleme = ucuz sıralama + pahalı ayrıntı yalnız
görünen küme için (error-first / identity-first aday deseninin aynısı).

### v0.10.364 — Exceptions listesi P1 göstermiyordu: kural vardı, satır taşımıyordu

Operator-reported (prod): 4 dakikada 5.8K ve 56 dakikada 125.6K olaylık iki
exception grubu "P1 olmamış". Kural (`exceptionPriorityAt`: ≥1000 olay ve
≥100/dk = patlama → P1) ikisini de P1 sayıyordu; hesap yalnız Triage
Inbox'a katlanırken (`exceptionToInbox`) yapılıyordu. `GET /api/exceptions`
`chstore.ExceptionGroup` satırlarını öncelik alanı olmadan döndürüyor,
Exceptions sayfası da hiç çizmiyordu — operatör "P1 olmadı" gördü, aslında
"P1 hiç gösterilmiyor"du. Çare: satıra `priority`/`priorityReason`
(liste ucu doldurur, saf hesap, CH okuması yok) + STATE yanında rozet
(tooltip = gerekçe). `PriorityBadge` Inbox'tan `ui/`'a taşındı. Aynı sürüm:
tablonun sabit px bütçesi (150/150/160/240) yüksek zoom'da sığmıyordu →
124/124/120/208. Ders: "kural yeşil" ≠ "kural görünür" — bir yüzey kuralı
göstermiyorsa operatör için kural yoktur; liste ucunun kuralı çağırdığını
kaynak-pin testi çiviler (`TestListExceptionGroupsAttachesPriority`).

### v0.10.367 — Metrik dışlama kuralları VM okuma yolunda uygulanmıyordu

Operator-reported (prod, VM-birincil): Service Overview metrik grafiklerinde
checkLiveness / checkReadiness / checkStartup probe route'ları görünüyor;
"kapatabilir miyiz". Mekanizma v0.9.797'den beri var (Settings → Pipeline →
Dışlama: metrik `*`, http.route deseni) ama YALNIZ ClickHouse okuyucusu
uyguluyordu (`applyMetricExclusionWhere`); VM adaptörü (`vmMetricSource`)
filtreyi olduğu gibi iletiyor, önbellek anahtarı ise kuralların digest'ini
taşıyordu — kural CH'de gizliyor, VM'de bırakıyordu. Çare: adaptörün beş
sorgu metodu `excluded(f)` ile `http.route !~ ".*(?:desen).*"` ekler
(kural ankorsuz RE2, PromQL tam ankorlar → sarmalama). Ders: dikişin iki
ucu aynı okuma-politikasını taşımalı; digest anahtarda olup filtre sorguda
yoksa "uygulanıyor" yanılsaması. Kaynak-pin: `v.svc.Query*(ctx, f` çıplak
geçiş yasak. Bilinen boşluk: `dropAtIngest` VM forward'ında uygulanmıyor
(ham OTLP gövdesi aynen gider) — okuma filtresi görünümü kapatır.

### v0.10.370 — Overview → Explore kapısı `_count` sayacını `avg` ile açıyordu

Operator-reported (prod, VM): Throughput · metrik grafiği artan trend
gösterirken üstüne basınca açılan Explore grafiği dümdüz, eksen "4.63 days".
Kapı (`metricsHref`) iki paneli de throughput'un `_count` adıyla ve /metrics
sayfasının varsayılan `avg`'ıyla açıyordu: kümülatif sayacın ORTALAMASI —
saniye birimli ad yüzünden gün eksenli düz çizgi. Response time paneli de
aynı `_count` adını taşıyordu (rtMetric değil). v0.9.1274 dersinin kapı
hâli: bir metrik adı SORUYA göre çözülür — Throughput = `_count` + rate,
Response time = `rtMetric` + avg. Çare: `metricsHref({metric, agg, by})`,
panel başına ayrı kapı; `/metrics?agg=` zaten taşınıyordu. Ders: bir
"doorway" grafiğin GERÇEK sorgusunu taşımalı (ad + aggregation + kırılım);
ad tek başına yarım sözleşmedir. Pin: Throughput kapısı `agg: 'rate'`.
