# Decision log — architectural calls

Referenced from CLAUDE.md. Newest entries append at the bottom.

- **v0.5.208** — Tempo external trace backend as a *fallback*,
  not a replacement. Coremetry samples at low rate, Tempo holds
  100%, fallback resolves the long-tail trace-by-id.
- **v0.5.210** — P1/P2/P3 priority score blended at READ time
  (no extra column). Persisted is wasteful; fresh recompute is
  cheap and lets fresh deploys/threshold ratios re-rank
  instantly.
- **v0.5.220** — Local monolithic Tempo + 30/100 collector split
  for POC. Operators replicate this layout in prod (sample to
  Coremetry, 100% to Tempo).
- **v0.5.226 / v0.5.235** — Faceted sidebar shipped THEN dropped
  at billion-doc scale because top-10 terms aren't useful when
  the operator's value is in the long tail. Replaced with
  click-from-row filter (still in place) + significant_text +
  Drain templates.
- **v0.5.241** — Log-pattern detector consumes `logstore.Store`,
  not raw `chstore`. Decoupled detector from CH-only path so
  ES-backed installs get coverage too. ES backend batches via
  `_msearch`.
- **v0.5.244** — Drain templater is sample-based on purpose.
  Three-layer log anomaly cover: curated regex (high-priority
  known failures) + significant_text (rare tokens, unsupervised)
  + Drain templates (full shape clustering, sample-based).
- **v0.5.246-247** — Topology op view + service view share the
  same NODE / COL / ROW constants + orphan handling so the
  operator's eye doesn't recalibrate when switching tabs.
- **v0.6.0** — `COREMETRY_MODE` env var lets the single
  binary run in four roles: `all` (default, monolithic POC),
  `ingest` (OTLP receivers + CH writers), `api` (HTTP API +
  SSE + Copilot), `worker` (evaluator + anomaly + topology
  agg + notifier; replicas=1 — leader-elected). Preserves the
  single-binary pitch (one image, one tag) while letting
  banks run 5×ingest + 2×api + 1×worker at billion-spans
  scale.
- **v0.6.2** — Helm chart `deployment.mode: monolithic |
  distributed` toggle. Monolithic = unchanged behaviour from
  v0.5.x (one Deployment, replicaCount applies). Distributed
  = three Deployments + four Services (`<release>` alias →
  api, plus `-ingest`/`-api`/`-worker`). HPA targets api in
  distributed mode; worker locked at 1 replica.
- **v0.6.3** — SSE Redis pub/sub bridge. Worker-pod-fired
  events (problem.open, anomaly.fire) ride a `coremetry-
  events` Redis channel so every api pod's local SSE
  subscribers receive them. PodID-stamped envelopes prevent
  loops; 200ms publish deadline so a wedged Redis doesn't
  stall the evaluator. Single-pod / Noop-cache installs are
  unchanged (no Redis activity).
- **v0.6.4-v0.6.7** — Model Context Protocol server. JSON-RPC
  2.0 over HTTP+SSE per spec 2024-11-05. Exposes tools (7
  telemetry surfaces in `internal/mcptools/`), resources
  (URI-addressed snapshots + templated per-id reads), and
  prompts (curated system+user message pairs that surface the
  in-app ✨ Explain workflows). Auth via existing JWT
  middleware — viewer/editor/admin roles carry into MCP. Runs
  on api+all modes only (worker/ingest pods don't take
  operator traffic).
- **v0.6.8** — AI-driven CH query optimizer on
  `/admin/clickhouse`. Operator pastes SQL, Copilot rewrites
  it against the MV catalogue + six-rule checklist (MV bypass
  / LIMIT / max_execution_time / time-bounded WHERE / GLOBAL
  IN / quantileTDigest defaults). Suggestion only — no auto-
  run; operator copies the optimized SQL to their CH client.
  Routes through `s.copilotExplain` so every call writes an
  ai_calls row for /ai attribution.

## v0.7 → v0.10.629 — karar kaydı (2026-05-29 → 2026-09-10)

Temaya göre gruplu, tema içinde sürüm sırasıyla. Git mesajı olan
kararlar sürüm etiketiyle anılır; yalnız operatör notundan bilinenler
"(kaynak: operatör notu, tarih)" ile işaretli. Müşteri/kurum adı,
alan adı, gerçek şema/tablo/kolon ve iş yükü adları bu dosyaya
girmez — jenerik ad kullanılır.

### Depolama / ClickHouse

- **v0.9.385-387 + `migrations/0001-0003`** — Rollup katmanı iki
  aile: DAR (service · kind · status, tdigest, 10s→1m→5m→1h) hızlı
  overview için; GENİŞ (endpoint · channel_code · function_code,
  20 kovalı eksponansiyel sayaç, 1m→5m→1h) breakdown için; metrik
  zinciri ayrı (0003). Okuma `PickRollup` karar tablosuyla step'i
  tam bölen en kaba kademeye iner; pencere başı tablonun en eski
  verisinden eskiyse ham yola düşer (kapsama dürüstlüğü), tablo
  yoksa sessizce ham yol. Reddedilen: DDL'in boot'ta otomatik
  koşması — ON CLUSTER DDL'i N pod yarıştırınca dağıtık kuyruk
  tıkanır (v0.9.613 vakası), sahibi operatör. `spanmetrics_*`
  ailesinin emekliliği v0.9.428'de RESMEN ertelendi (ölçüt: rollup
  prod'da ≥30 g + resolver okumaları eşit/hızlı + 0004 backfill).
  Kod: `internal/chstore/rollupselect.go`, `rollup_read.go`,
  `rollup_fastpath.go`.
- **v0.9.770 + 2026-08-09 A/B** — Rollup kurulumu UI sihirbazından
  (/admin/clickhouse → "Rollup katmanı": durum → ön kontrol →
  kur → 5 dk write_failed bekçisi → geri al); yine boot'ta ASLA
  koşmaz, tek tetik admin'in düğmesi. Aynı hafta ölçülen A/B
  (9 koşu, query_log medyanı, iki bağımsız tekrar): GENİŞ ailenin
  ORDER BY'ında `ts` sonda olduğu için servis+zaman okuması zaman
  budaması yapamıyor ve aile ≈1.02:1 sıkışıyor → **0002+0005 prod
  kurulumu ÖNERİLMEZ**; geniş aile ancak ORDER BY yeniden
  tasarımıyla (ayrı bilinçli karar) değer üretir. Kod:
  `frontend/src/pages/AdminClickhouse.tsx`,
  `migrations/0002_rollup_wide.sql` (kaynak: operatör notu,
  2026-08-09).
- **v0.9.1308 + `migrations/0009_state_unify.sql`** — State
  tabloları (problems, users, alert_rules, dashboards, …) tek
  replikasyon grubunda: ZK yolunda `{shard}` yok, yol koda gömülü
  değil kümeden okunuyor (`resolveStateReplicaPaths` — komşular
  hangi yolu tutuyorsa o), 37 state tablosu negatif türetimle
  bulunuyor (shard kayıtlarında adı geçmeyen = state). Sebep:
  `Replicated*` ama Distributed sarmalayıcısız tablolar failover'da
  bağlanılan host'a göre ayrışıyordu (prod: problems 633k / 4k iki
  shard'da). `ReplacingMergeTree(version)` + `FINAL` sözleşmesi
  göçü güvenli kıldı: iki shard'ın bağımsız ürettiği aynı
  deterministik id'ler `version` ile birleşti (prod 635.864,
  birleşim aritmetiği oturdu). Reddedilen: uygulamayı tek host'a
  pinlemek (yeni bölünmeyi durdurur, ayrışmayı onarmaz); ayrı
  1×N replica küme tanımı (ZK yolu yerinde değişemediği için
  iki kat iş). Kod: `internal/chstore/state_unify.go`.
- **v0.9.1139 · v0.10.247 · v0.10.561** — Kullanıcı durumu için
  yeni şema yok, `saved_views(page='<kind>')` tek tablo (invariant
  #5): sohbet kalıcılığı `ai-chat` blobu (1139), kişisel tablo
  tercihleri `page='table:<key>', id='pref:<uid>:<key>'` doğal
  upsert + `name=''` tombstone (247), 90 günlük sohbet arşivi
  süpürücüsü (561). Reddedilen: yüzey başına tablo. Kod:
  `internal/api/preferences_routes.go`.
- **v0.10.181** — Operatör kararı deposu `anomaly_verdicts`: düşük
  hacimli operatör state'i için şablon — `ReplacingMergeTree
  (version)`, ORDER BY event_id (olay başına SON karar), partition
  YOK, `FINAL` okuma, TTL 180 g, küme kipinde otomatik Replicated
  (`anomaly_silences` ile aynı sınıf). "Değil" artık yalnız
  susturma değil, kayıtlı karar; "Anomali" kararının deposu ilk
  kez var. Kod: `internal/chstore/anomaly_verdict.go`,
  `internal/api/anomaly_verdicts.go`.
- **v0.9.1150 → v0.10.601 (şablon)** — Her dış kaynak/ayar
  `system_settings` JSON blobu + `LoadPersisted` boot'ta + 30 s
  yenileme + maskeli `Snapshot` (Tempo şablonu,
  `internal/tempo/client.go`): `victoria_metrics` (v0.9.1150),
  `mcp_client_servers` (v0.10.87), `influx_sources` (v0.10.222),
  `oracle_sources` + `oracle_poll:<id>` watermark (v0.10.580/601).
  Sır sözleşmesi tek: blob'da düz token SAKLANIR, GET asla geri
  vermez (`hasToken`), boş girdi saklıyı korur, rotasyon = yenisini
  yapıştır; `env:`/`file:` referansı OPSİYONEL ve doluysa kazanır
  (`internal/secretref`, v0.10.271-273; Tempo/Thanos/VM'e yayıldı,
  ES/SMTP/AI/LDAP bilinçli kapsam dışı). Reddedilen: "yalnız secret
  referansı, düz token reddedilir" (v0.10.222-223 arasında yaşadı;
  operatör "saklanabilir, maskeli yaparsın", 2026-09-01). Kod:
  `internal/chstore/vmetrics.go`, `internal/secretref/`.

### Ingest / OTLP

- **v0.8.73** — Binary içi head+tail sampler ve Settings "Trace
  sampling" sekmesi tamamen söküldü (`internal/sampling/` yok);
  Coremetry ALDIĞI span'lerin %100'ünü saklar, örnekleme istenirse
  collector'da (v0.5.208/220 sample-to-Coremetry / 100%-to-Tempo
  ayrımı aynen). Ingest sıcak yolundan span başına bir karar ve bir
  ayar katmanı gitti; geriye dönük şim yok (eski `sampling:` bloğu
  sessizce yok sayılır). KALAN ve karıştırılmaması gereken:
  pipeline `KindSample` drop kuralı, self-obs SDK sampler'ı, okuma
  zamanı downsampling (heatmap/scatter). Kod:
  `internal/otlp/http.go` (addSpan → pipeline → store).
- **v0.9.797** — Dışlama mimarisi: ingest drop'un TEK sahibi
  `internal/pipeline` motoru (AcceptSpan/Log/Metric üçü OTLP
  hattına bağlı; kurallar `system_settings`, UI Settings →
  Pipeline). Span için OKUMA-zamanı route filtresi bilinçli YOK:
  dar rollup ve `operation_summary_5m` route bilmiyor, filtre
  entry-RED'i ham taramaya düşürür ve KPI fallback'i dışlanmamış
  sayı gösterirdi. Metrik için okuma-zamanı `metric_exclusions`
  var (kural seti cache anahtarına FNV özetiyle girer, kural yokken
  SQL bayt-bayt eski). Müşteri path desenleri repoya ASLA —
  operatör Settings'ten girer. Kod: `internal/pipeline/pipeline.go`,
  `internal/otlp/http.go` (kaynak: operatör notu, 2026-08-09).
- **v0.9.240 + 2026-07-25 direktifi** — Giriş-span ilkesi: bir
  servisin RED metrikleri yalnız `kind IN ('server','consumer')`
  giriş span'lerinden hesaplanır ("aynı Dynatrace gibi");
  client/producer/internal span'ler servisin YAPTIĞI çağrılardır,
  ölçüsü değil. Latency 240'ta, throughput+error operatör onayıyla
  çekildi (ölçülen etki: demo p50 60→807 ms, throughput 8× düşer,
  error rate 5× çıkar; prod latency SLI %99.99→%99.85, alarm
  fırtınası yok). Tuzak: `service_summary_5m`'de `kind` boyutu
  yok ve ORDER BY değişemez → çözüm boyut eklemek değil
  `entry_*_state` kolonları. Giriş span'i olmayan servis için "tüm
  span'lere düş + nedeni yaz" fallback'i şart. Kod:
  `frontend/src/lib/entrySpans.ts`, `internal/chstore/slo.go`
  (kaynak: operatör notu, 2026-07-25).
- **Sürekli (operatör kararı)** — PII/veri redaction özelliği YOK:
  attribute, log gövdesi, DB ifadesi, trace yükü hiçbir yerde
  maskelenmez/hash'lenmez/tokenize edilmez. Gerekçe: olay
  incelemesinde ham adli veri gerekir; erişim denetimi çevrede
  (roller, audit, depolama şifrelemesi) yaşar, okuma anında
  mutasyon değil. Reddedilen: field-level encryption, DLP,
  "configure off" toggle'lı redaction. Kod: `internal/otlp/
  convert.go` attribute'ları verbatim taşır (kaynak: operatör
  notu, "redact etme asla").

### Metrikler / VictoriaMetrics

- **v0.9.1150 (+1151)** — VictoriaMetrics OKUMA backend'i, Settings
  toggle'ıyla (env değil): açıkken katalog/picker,
  `/api/metrics/query` (Explore + dashboard + MCP), etiket
  değerleri VM'den; kapalıyken CH yolu bayt-bayt aynı. İki katı
  kural: **sessiz CH fallback YOK** (VM açık+erişilemez → 502;
  metrikte SAYILAR değişir ve ekran bunu söyleyemez — Tempo
  fallback'inden bu yüzden farklı) ve **400 ≠ 502** (çevrilemeyen
  sorgu 400 + desteklenen küme; transport/VM hatası 502 — sağlıklı
  VM'e "bozuk" dedirtmeyiz). Cache anahtarları `src=` backend
  işareti taşır (v0.5.187 sınıfının ayar-girdili hâli). 1151:
  `?metricsrc=vm|ch` deneme parametresi (Enabled şart değil, UI
  yazmaz). Span-türevi her şey + `/api/metrics/resolve` CH'de.
  Reddedilen: `internal/thanos`'u ortak çekirdeğe göçürmek —
  promapi kopya-desen, Thanos/clusters ayrı kalır. Kod:
  `internal/api/metricsource.go` (seam), `internal/vmetrics/`,
  `internal/promapi/`.
- **v0.9.1154-1160** — Çeviri dili MetricsQL (PromQL süperseti;
  operatör düzeltmesi 2026-08-17): rate/last 300 s pencere tabanı,
  `increase` ASLA taban almaz (v0.6.36 birim-ölçek sınıfı); `last`
  groupBy'da `max` ile çöker (CH argMax sınıfı); ad eşleyici OTel
  noktalı adı Prometheus yazımına PROBE ile değil aday
  alternation'ıyla eşler (varlık ≠ canlılık — bayat aile probe'u
  kandırır); OTLP histogramının taban serisi olmadığı için
  avg/rate/increase `_sum`/`_count` kollarıyla MetricsQL `or`
  kompozisyonu (operatör kararı, ilk kesimi değiştirdi: sol kol
  kazanır, alternation'ın çift sayma bedeli kalktı).
  `ValidatePromQL` asimetrik: CH parse eder, VM nil (üst kümeyi
  motorundan katı reddetmek "bozuk uç" gibi görünür). Kod:
  `internal/vmetrics/promql.go`, `names.go`, `histogram.go`.
- **2026-09-02 kararı → v0.10.292-294 (+365/366/374)** —
  **VictoriaMetrics TEK metrik deposu** (operatör: "VM'i ANA metrik
  deposu olarak konumlandırmak istiyorum", 2026-09-04; audit
  onaylı). OTLP metrik gövdesi HAM hâliyle VM'e forward edilir
  (`/opentelemetry/v1/metrics`; gzip aynen, JSON→protobuf; CH
  modelinden GERİ ÜRETİLMEZ çünkü CH modeli kayıplı), ayrı bayt
  bütçeli kuyruk, OTLP cevabını asla bekletmez, varsayılan KAPALI;
  `/api/metrics/compare` kıyas ucu; sabit-adlı okuyucular
  (hosts/infra, DB kapasite, JVM pod) `metricSource` seam'ine
  taşındı, `dql.go` bilinçli CH-çakılı. **AÇIK DURUM
  (2026-09-10):** ClickHouse `metric_points` yazımı HÂLÂ sürüyor —
  Dilim 5 (CH yazımını kapat, ölü `spanmetrics_*_5m` sil) ön koşulu
  prod çift yazım + compare sıfır fark, operatörde; VM OSS olduğu
  için 13 aylık saatlik rollup ufkunun VM karşılığı yok (ufuk VM
  retention'ına iner ya da CH rollup dondurulur — karar bekliyor).
  Ekip-hazırlık denetimi bunu "kural aspirasyonel, kod ÇİFT
  yazıyor" (B3) diye kaydetti. Thanos: remote_write tercih, olmazsa
  ayrı kaynak. 3b-2/3b-3 (db receiver keşfi + motor panelleri)
  PARK: "db receiver kullanmıyorum" (2026-09-05). Kod:
  `internal/otlp/forward.go`, `internal/vmetrics/write.go`,
  `internal/api/metrics_routes.go`,
  `docs/audit/vm-metrics-migration.md`.
- **v0.10.222-231 → v0.10.606** — InfluxDB 2.x dış hata serisi
  entegrasyonu eklendi (audit onayı 2026-09-01: kaynak yönetimi,
  1 dk kovalı poller → `metric_points` `ext:<sorgu>`, kind=external
  anomali, kanıt zinciri, Problem paneli, mevsimsel baseline) ve
  operatör "influxdb ile işimiz kalmadı" deyince Oracle Aşama 2
  gemiye çıktıktan SONRA tek sürümde söküldü (2026-09-10; şim yok,
  `influx_*` blobları okuyansız kaldı). Kalıcı olan kararlar: dış
  seri için AYRI tablo değil `metric_points` + `exemplars` (K1);
  istemci kütüphanesi değil net/http + annotated CSV (K2); seri
  kova bitiş zamanına yazılır, "operatörün Grafana'sıyla aynı
  sayı" ilkesi (v0.10.224). `ExternalScanner`/kind=external hattı
  KALDI — Oracle onun üstüne kurulu. Kod (kalan):
  `internal/anomaly/external.go`, `internal/secretref/`.

### Loglar / Elasticsearch

- **v0.7.14** — Collector logları Elasticsearch'e ÇİFT gönderir
  (Coremetry OTLP export'una ek); CH yazma deposu kalır, ES yalnız
  OKUMA backend'i (invariant #2), `COREMETRY_LOGS_BACKEND` ile
  seçilir. `logstore.Switchable` iki backend'i BİRLEŞTİRMEZ,
  aralarında SEÇER — prod ES'teyken CH `logs`'a yazılan satır hiç
  görünmez (Oracle satırlarının ayrı tabloya gitmesinin nedeni,
  aşağıda). Kod: `internal/logstore/switchable.go`, chart
  `otelCollector.elasticsearch.*`.
- **v0.8.8 · v0.8.139-140 · v0.8.270 (+2026-06-15 direktifi)** — ES
  maliyet disiplini: Live Patterns + Similar Traces silindi
  (`significant_text` 30 s poll'da, v0.8.3 CPU olayı; −1120
  satır); her ES okuma `serveCached` (singleflight + SWR);
  `track_total_hits` tavanı + soft timeout; canlı kuyruk tiki
  5→10 s; log-anomali `?window=` cache anahtarında sınırsız
  gezmesin diye {1m,5m,15m,30m} basamağına sabitlendi → uç başına
  en fazla 4 `_msearch`/TTL. Operatör: "Elastic üzerinde yoğun api
  istekleri olsun istemiyorum" — sayı VE sıklık; fetch yalnız
  expand/open'da, liste prefetch'i yok, staleTime ≥ sunucu TTL.
  Kod: `internal/api/cache.go`,
  `internal/api/log_patterns_explain.go` (kaynak: operatör notu,
  2026-06-15).
- **v0.10.566** — İstek kimliği (request_id) YALNIZ log gövde
  metninde yaşar; yapılandırılmış alan EKLENMEZ (operatör:
  ingest'i değiştirmek istemiyor; `LogRecord.Attributes` hiçbir
  backend'de dolmuyor). Dış log platformu linki: gövdede
  request_id varsa onunla, yoksa span attribute yolu (function_id
  + channel_code birlikte); kazanan span sırası seçili → ilk
  hatalı → root. ES bedeli (10B doc/gün) yüzünden okuma tavanlı.
  Kod: `internal/reqid/`, `internal/api/trace_link_identity.go`.
- **v0.10.580-603** — Kurumun Oracle hata tablosu → Problem hattı,
  Aşama 1-2. Sürücü **go-ora** (saf Go): Dockerfile `CGO_ENABLED=0`
  + alpine/musl, godror Instant Client ister ve musl için
  yayınlanmaz — "tek binary, tek imaj" kısıtı sürücüyü seçti.
  Şema/tablo/kolon adları ve tip süzgeci KODA GÖMÜLMEZ, ayardan
  (`Columns{alan: KOLON}`, `-` = alan tabloda yok; identifier
  regex'i tırnaksız SQL'e girer, değerler daima bind); zaman dilimi
  IANA (varsayılan Europe/Istanbul) + "kolon dilimli" anahtarı,
  `time/tzdata` gömülü (alpine'de zoneinfo yok). Tüketim modeli
  periyodik ingest (yalnız `COREMETRY_MODE=worker|all` lideri,
  `cache.LeaderHolder` 30 s kira), watermark `system_settings`'te
  kalıcı ve yalnız yazım başarılıysa ilerler; yeniden görülen satır
  aynı `row_id` → RMT'de tek satır. Satırlar CH `logs`'a DEĞİL
  kendi RMT tablosuna yazılır ve Trace Logs sekmesinde üçüncü dizi
  olarak (`/api/oracle/errors`, origin `oracle`) birleşir (audit
  B4). Bağlantı testi 200 + `{ok:false}`; dar pencere boşsa 24 s
  geniş ikinci deneme ("hata yok + satır yok" üç sorunu
  ayırt edemiyordu — Influx bunu prod'da öğretti). Aşama 3 (shadow
  Problem) açık. Kod: `internal/oracle/`,
  `internal/api/oracle_logs_routes.go`,
  `frontend/src/pages/settings/oracleForm.ts`,
  `docs/audit/oracle-error-log-2026-09-09.md`.

### CoSRE / AI / MCP

- **v0.8.374 + v0.8.397-398** — Copilot yerel küçük model
  (air-gapped kurum, 2B sınıfı; gemma4, 2026-07-21) için tasarlanır:
  veri sunucuda deterministik PREFETCH edilir, model yalnız ANLATIR
  (analyze-service deseni; guided chat = deterministik niyet
  router'ı + prefetch→anlatım); çok turlu tool döngüsü ve katı JSON
  şeması şüpheli. Her düzyazı yüzeyi Türkçe (`AnswerInTurkish`
  13 prompt'a; yapılandırılmış çıktı prompt'ları bilinçli
  dışarıda). Harici LLM API'si yok, dış ajan tüketicisi yok — MCP
  ToolList'in değeri in-app function-calling. Kod:
  `internal/copilot/`, `internal/api/copilot_guided.go` (kaynak:
  operatör notu, 2026-07-21).
- **v0.8.468 + Faz 1.6 → v0.9.1232** — AI çağrı disiplini: TÜM
  sistem prompt'ları `internal/copilot/prompts.go`'da (sicil +
  dil kapısı `prompt_language_test.go`; sicilde olmak doğru sınıfta
  olmak değil, satır-içi ekler de sicil dışı); her Explain
  `s.copilotExplain(r, …)` üzerinden ki `ai_calls` satırı düşsün ve
  `/ai` atfı dürüst kalsın — `s.copilot.Explain` doğrudan
  çağrılmaz. Erişilemez ve pahalı filo-geneli tek-atış
  SystemAnalysis (v0.8.75) silindi; tezi odaklı yüzeylerde yaşıyor
  (Problems Explain, RootCausePanel). Kod:
  `internal/api/ai_observability.go` (`copilotExplain`),
  `internal/copilot/prompts.go`.
- **v0.9.477 → v0.10.461/483** — TEK AI çekmecesi: inline Explain
  panelleri v0.9.477'de çekmeceye toplandı; operatör üç kez
  "Explain ile CoSRE iki ayrı drawer" deyince 461 kabuğu
  (genişlik 620, tek cevap kartı sınıfı), 483 bileşeni eşitledi:
  `AIDrawer` gövdesi `AIDrawerBody`'ye, `CopilotChat` `?ai=`
  öznesini kendisi okuyup aynı kabukta açıklama kipine geçer;
  AppShell yalnız CopilotChat mount eder, `AIDrawer` ince
  sarmalayıcı. Reddedilen: iki bileşen, iki başlık anatomisi. Kod:
  `frontend/src/components/CopilotChat.tsx`,
  `frontend/src/components/ai/AIDrawer.tsx`.
- **v0.10.172 + v0.10.194** — Serbest soru için kademe 3.5: router
  → çekmece → RAG eşleşmezse küçük modele TEK katı-JSON niyet
  sınıflandırma çağrısı (slotlar canlı listeye doğrulanır,
  adlandırılmış ama eşleşmeyen → none); eşleşirse mevcut
  prefetch→anlatım yolu. Kip `intentClassify` off | on |
  **on_no_loop (varsayılan)** — operatör "RAG'a gitmesine gerek
  yok"; 194'te `none` artık cevapsız değil: tool'suz TEK genel
  anlatım (`chat-general`, "telemetriyle eşleşmedi — genel
  bilgiyle" notu, KB adayı OLAMAZ). Reddedilen: serbest tool
  döngüsüne düşmek (2B modelde "schema soup"). Kod:
  `internal/api/copilot_intent.go`, `copilot_guided.go`,
  `internal/copilot/copilot.go` (IntentClassifyMode).
- **v0.9.1136** — MCP yetki: `MinRole` kayıt-defteri metadata'sı +
  tek `CallGate` (tools/call + resources/read + prompts/get; route
  tablosu yok); sıra rol → sonra rate (rol reddi bütçe tüketmez);
  tanınmayan MinRole FAIL-CLOSED; `cmk_` token kendi rolünü taşır,
  kişi-bazlı scoping yok. A7: REST↔MCP sapması sıfır olsun diye
  `GET /api/anomalies/active` editor kapısı viewer'a indi (okuma
  kapısı hiçbir şeyi korumuyordu, viewer'ın Cmd-K paletini
  bozuyordu). Yazma tool'u yok; gelirse MinRole ≥ editor + audit.
  Kod: `internal/api/mcp_gate.go`, `internal/mcp/`.
- **v0.10.86-89** — Dış MCP İSTEMCİSİ (Coremetry o güne dek yalnız
  sunucuydu): operatör izin listesi `system_settings`
  `mcp_client_servers` (8 sunucu tavanı, 200 tool katalog tavanı),
  tool başına allow/deny köprüde (deny kazanır, `*` önek; süzülen
  tool modele HİÇ görünmez), her çağrı audit `mcp.call`, `[dış:
  <sunucu>]` etiketi, önek `<sunucu>__<tool>` (çakışma politikayla
  değil biçimle imkânsız), tekrar muhafızı tüm tool'lara, deponun
  ilk giden-istemci span'leri. Bilinçli sınır: dış tool'lar YALNIZ
  serbest sohbet döngüsünde (guided/drawer/RAG'a sızmaz); MinRole
  viewer (rol merdiveni Coremetry verisinin). Kod:
  `internal/mcpclient/`, `internal/api/chat_mcp_bridge.go`.

### Anomali / Problems

- **v0.9.487 · v0.9.611 · v0.9.612** — P1 SEYREK kalmalı:
  paylaşılan patlama eşiği 10 servis (operatörün prod deneyimi;
  8→4 tahminleri yanlıştı); taze deploy tetikleyicisi önceliğe
  KARIŞMAZ (deploy sıklığı yüksek, her dağıtım penceresi P1
  üretiyordu) — deploy bilgisi ProblemDetail'de görünür, sıraya
  sokmaz; Inbox varsayılanı yalnız P1, exception-dışı türler
  inbox'ta HEP P3 (görünüm önceliği; evaluator/bildirim değişmez).
  P1 kapıları problemin KENDİ şiddetinden türer (2× eşik, 4+ saat
  açık); öncelik hâlâ okuma anında türetilir, yazılabilir alan yok
  (v0.5.210). Kod: `internal/chstore/problem.go` computePriority,
  `frontend/src/pages/Inbox.tsx`.
- **v0.9.1205** — P1'i HAK ETMİŞ exception grubu ele alınana dek
  (resolve/ignore) P1 KALIR; aynı sınıfın beşinci bildirimiydi
  (627→699→775→1189) — dört düzeltme "şiddet olgu, aciliyet
  zamanla düşer" ilkesini koruyup pencereyi oynatmıştı, operatör
  dördünde de aynı duvara çarptı. Patlama koşulsuz P1; yalnız
  kronik damlama basamaklara iner; P2/P3 eskisi gibi zamanla
  düşer. Kod: `internal/chstore/problem.go` (6 test direktife
  çevrildi).
- **v0.10.543** — `service_silent` dedektörü VARSAYILAN KAPALI
  (operatör: "Anomali service silent'lara ihtiyacım yok"); prod
  /incidents 100+ açık CRITICAL doluydu, span kesintisi bu filoda
  arıza değil. Kapalıyken açık kalanlar tikte çözülür. "Sinyal
  kaybı = critical" çıkarımı yapan yeni dedektör eklenmez
  (evaluator'ın "source silent" resolve gerekçesi ayrı, kalır).
  Kod: `internal/chstore/anomaly_sensitivity.go` (ServiceSilent).
- **v0.10.228 · 587 · 588 · 597** — Dış hat TEK tarayıcı:
  `ExternalScanner` `metric_points`'teki `ext:*` serilerini MEVCUT
  `evaluateAnomaly` çekirdeğine verir (MAD/dwell/z, aynı hassasiyet
  blobu), Problem `kind=external`, özne `ext:<kaynak>/<grup
  değerleri>`; kaynak başına ikinci tarayıcı YAZILMAZ — sözleşme
  "seriyi şu tabloya yaz, ben okurum" (Oracle audit B1). Tarama
  yalnız BAŞARILI poll sonrası (sıfır-padli sahte iyileşme yok).
  587 tik başına açılış tavanı (iki fazlı: önce değerlendir, sonra
  z'ye göre en güçlü N; refresh/resolve etkilenmez); 588 üç ardışık
  poll hatası = "kaynağa erişilemiyor" Problem'i (veri yokluğu ≠
  hata yokluğu); 597 aynı ilk boyutta ≥3 taze açılış tek küme
  Problem'i. Kod: `internal/anomaly/external.go`.
- **v0.10.592 + v0.10.605** — Poller-sahipli Problem'lerin yaşam
  döngüsü: evaluator'ın bayat süpürmesi (updated_at < 3×interval)
  `anomaly:ext-down:` / `anomaly:ext-cap:` kurallarını atlar — tek
  karar noktası `chstore.PollerOwnedRule` (önek sabitleri
  chstore'da; evaluator ve anomaly birbirini import etmez, ikisi
  chstore'u eder); seri Problem'leri BİLEREK süpürülür ("source
  silent" dürüst sinyal). 605: muafiyet yalnız YAŞAYAN kaynak için
  (`SetPollerSourceLive`) — kaynak silinince/kapatılınca Problem'i
  resolve edecek kimse kalmıyordu, Influx sökümü bunu ortaya
  çıkardı. Karar saf `staleSweepCandidates`, kablolama
  kaynak-pinli. Kod: `internal/chstore/problem.go:1439`,
  `internal/evaluator/evaluator.go`.

### Frontend

- **v0.7.55-65 + v0.8.306** — Her veri tablosu tek primitif:
  `useDataTable` (sort + resize, `storageKey` ile kalıcı genişlik,
  COLS `sortValue`/`numeric`) + `DataTable`; sunucu-sayfalı
  tablolar yalnız resize yarısını alır; >100 satır
  `content-visibility`. Elle sıralama yazan son tablolar 8.306'da
  göçtü. Kod: `frontend/src/components/ui/DataTable/DataTable.tsx`.
- **v0.8.253 (+v0.9.937)** — URL = seçimlerin tek kaynağı
  (filtre/çekmece/odak/sekme/aralık); sayfa `replace:true` ile
  yazar, URL→state içe aktarımı sig-guard'lı (bir range
  değişikliği yerel filtreleri siliyordu; tek-yönlü okuma tekrar
  eden bug sınıfı: 256/265/267). 937 v0.8.409'un "custom aralık
  asla kalıcı olmaz" kuralını TERSİNE çevirdi: kayıt sessionStorage
  (sekme ömrü) ve `?range=` olarak URL'e yazılır — asıl acı
  pencerenin SESSİZ olmasıydı. Reddedilen: localStorage kalıcılığı
  (haftalar sonra her sayfa geçmişte açılıyordu). Kod:
  `frontend/src/lib/logFilters.ts`,
  `frontend/src/lib/useUrlRange.ts`.
- **v0.8.268 / v0.8.285 (+2026-05 kararı)** — Tema yalnız token
  düzeyi CSS değişkeni: `redhat` paleti (PatternFly renkleri,
  OpenShift tarzı koyu nav) `#sidebar`'a kapsamlı token remap'iyle,
  sıfır bileşen değişikliği, yeni bağımlılık yok, ~1.5 KB CSS;
  285'te varsayılan tema. Grafikler `data-theme` değişince
  değişkenleri yeniden çözer. Reddedilen: Tailwind/shadcn göçü —
  30+ sayfayı yeniden stillemek kozmetik kazanç için haftalar,
  v1.0'a ertelendi. Kod: `frontend/src/styles/globals.css`
  (kaynak: operatör notu).
- **v0.8.428 · v0.8.509-510 · v0.9.67 · v0.9.1267** — Günlük triage
  yüzeyleri KLASİK kalır: Problems kart-feed'i (426) → sıralanabilir
  tablo geri (428; tam-sayfa detay kaldı); exception detayı
  drawer'ı (508) ve v426 düzeni → v0.8.425 klasik düzen (510);
  Operations Elastic-parity tablosu mockup onayına RAĞMEN canlıda
  reddedildi (67); Servis Overview "Tek-Bakış" `?newpage=1`
  bayrağıyla gerçek veride denendi, "eski hali daha iyiydi" (1267).
  Desen: operatör yoğun kullandığı listelerde yoğunluk/kas
  hafızasını estetiğe tercih ediyor; mockup onayı nihai değil.
  Kural: mockup-first + tek-commit'lik revert; 2026-07-30 "red
  geçmişimi overwrite edebiliriz" — hard kısıt değil, gerekçe hâlâ
  geçerli. Kod: `frontend/src/features/anomalies/ProblemDetail.tsx`
  (kaynak: operatör notları, 2026-07-10 → 2026-08-23).
- **v0.9.844 (+v0.10.283)** — Zaman serisi için TEK motor:
  `CorePanel` (`@grafana/ui` üstüne tek sarmalayıcı, altında uPlot);
  `chartsV2` bayrağı, MultiLineChart'ın kendi uPlot gövdesi ve
  DashboardViz SVG motoru söküldü (430 ekleme / 1992 silme).
  Chart.js vb. ağır kütüphane yok; @grafana/* + uplot tam pin
  (283). `'-ms'` senkron ad alanı kalıcı (saniye-eksenli motorlar
  yaşıyor). Reddedilen: eski yolu kaçış kapısı olarak tutmak (bir
  yıldır yalnız `?chartsV2=0`'da çalışıyordu). Kod:
  `frontend/src/components/chart/CorePanel.tsx`.
- **v0.9.1078** — Sayfa düzeyi yüzen/yapışkan şerit YOK: sticky
  kontrol çubuğu, sticky tablo başlığı ve `stickyBottom` Pager
  (v0.9.639/644/645'in denetim kararları) operatör isteğiyle geri
  alındı — görüş alanını yiyor, kaydırmada "oynuyor"; is-fit kaçışı
  yatay taşmayı `#content`'e sızdırıyordu. Kap içi `.is-scroll`
  (drawer) meşru; geniş tablo kendi `.table-wrap`'ında kaydırır.
  Kod: `frontend/src/components/Pager.tsx`, `globals.css` (kaynak:
  operatör notu, 2026-08-16).
- **v0.10.220 · 340 · 347 · 351 · 513** — Traces operatör UX
  kararları: zaman damgası EN SOLDA, sıra **Start time · Service ·
  Name · <attr> · Duration · Status · Spans** (Dynatrace'in sağ
  timestamp'i reddedildi; sabit sıra her kullanıcıda, sunucu yalnız
  EK kolonları saklar); Correlate ve alt Compare kalktı, dış link
  düğmeleri en sağda renkli; Tempo fallback ŞERİDİ kalktı ama
  PROVENANCE "source: Tempo fallback" ÇİPİ kalır (2026-09-07);
  şerit çizgisi p95 varsayılan, `?rt=` kompakt seçici,
  expand/shrink kalktı. Kod: `frontend/src/lib/traceColumns.ts`
  (test sırayı pinler),
  `frontend/src/components/traces/TraceHonesty.tsx`.
- **v0.10.255 + v0.10.257** — ContextBar sırası EnvPicker → kapsam
  (cluster/namespace/service/compare) → **TimeRangePicker sağ
  kenarda, son çocuk** (operatör: "eski yeri sağda daha iyiydi");
  sayfanın kendi kontrolüyle sunduğu boyut çubukta İKİNCİ KEZ
  çizilmez (`hidden` prop; devre dışı kutu bile değil) ve çubuğun
  URL→state effect'i yalnız uygulanan boyutlar için yazılır. Kod:
  `frontend/src/components/ContextBar/ContextBar.tsx`
  (`ContextBar.contract.test.tsx` sırayı pinler).

### Ops / Release

- **v0.9.0 (2026-07-17) → v0.10.1 (2026-08-25)** — Sürüm zinciri:
  her mantıksal birim KENDİ `vX.Y.Z` tag'i (operatör bug'ı derhal
  X+1, asla toplu; bisect tek küçük diff'e iner); v0.8 zinciri
  588'de, v0.9 zinciri 1388'de kapandı. 1.0 kesimi HAZIRDI
  (docs/RELEASE-1.0.md; canlı kapılar prod'da koşuldu, altısından
  beşi geçti) ama operatör "0.10.1 diye devam edelim, 1'e
  geçmeyelim" dedi; dosya silinmedi, ön-durum bloğu o gün yeniden
  ölçülür. Zincir değişince sürüm ÜRETEN makine (release skill'inin
  tag deseni) de çevrilir — aksi hâlde sessiz çatallanma
  (`version_chain_test.go` yönlü kapı). Kod:
  `.claude/skills/release/SKILL.md`,
  `internal/api/version_chain_test.go`.
- **2026-07-28 + 2026-08-25 (operatör kararları)** — Kabul edilen
  güvenlik duruşu, yeniden bulgu diye açılmaz: `/tempo/*` kimliksiz
  kalır; custom roller yalnız tarayıcıda uygulanır; admin config
  export'u saklı kimlik bilgilerini düz metin yazar; prod JWT
  imzalama anahtarı yer tutucu değerde ve ROTASYON REDDEDİLDİ
  (bedeli: tüm açık oturumlar düşer; `/admin/stats` şeridi bu
  yüzden kırmızı kalır). Gerekçe: tek-tenant, kurum ağı içi
  kurulum; operatör işletilebilirliği teorik maruziyete tercih
  ediyor. Kapsam TAM OLARAK bunlar — admin/editor/viewer rolleri
  sunucuda zorlanmaya devam eder, Settings GET asla secret geri
  vermez. (kaynak: operatör notları.)
- **2026-05-19 (operatör kararı)** — Multi-tenant YOK: her tabloya
  `tenant_id`, her sorguya WHERE, cache anahtarı patlaması ve ingest
  yönlendirmesi 3-5 sürümlük refactor; kurulum senaryosu
  (self-hosted, tek kurum, SaaS/MSP hedefi yok) karşılığını ödemiyor.
  "İleride lazım olur" diye kolon/WHERE eklenmez (YAGNI);
  tenant varsayan özellikler (per-tenant fiyat, signup, süper-admin
  çapraz görünüm) de kapsam dışı. (kaynak: operatör notu.)

### Repo / Ekip

- **v0.10.247 + v0.10.625** — `api.go` BÜYÜMEZ: yeni yüzey kendi
  `internal/api/<domain>.go` dosyasında `init() {
  registerRoutesExtra(name, fn) }` defteriyle kaydolur (çift ad
  panic; `buildMux` defteri ad sırasıyla deterministik kurar; Go
  1.22 çakışma testi defteri de kapsar; api.go 5 satır küçüldü).
  625: kural CLAUDE.md'de yazılıydı ama GLOBAL kapı yoktu →
  ratchet: taban `.claude/baselines/api_go_lines` (12113, yalnız
  aşağı iner), `api_go_size_test.go` büyümeyi VE indirilmemiş
  tabanı hata sayar, script + post-edit hook + CI adımı; tabanı
  yükseltmek CODEOWNERS onayı + `Api-Go-Baseline:` trailer'ı ister.
  Hedef: registerRoutes aileler hâlinde taşındıkça ~11000. Kod:
  `internal/api/route_registry.go`,
  `internal/api/api_go_size_test.go`,
  `scripts/guard-api-go-size.sh`.
- **v0.10.613 → v0.10.617-621** — Ekip geliştirmesi + GitHub org
  yayını hazırlık denetimi (`docs/audit/team-readiness-audit.md`):
  repo `cosretr` organizasyonuna taşındı; README/Chart/values/ghcr
  yolları org'a çekildi, Chart appVersion aylar sonra uygulama
  tag'iyle yeniden senkron (617). BİLİNÇLİ değişmeyen: go.mod
  modül yolu ve 622 dosyadaki import'lar (GitHub yönlendirir;
  kanonik yol ayrı karar). Maskeleme politikası koda girdi: gerçek
  prod IP/FQDN fixture'ları TEST-NET'e, LDAP fixture'ındaki gerçek
  kişi/birim adları, gerçek OpenShift iş yükü/küme adları (54
  dosya), kurumun Oracle şema/kolon adları ve Influx artıkları
  SENTETİK (618-621); kurum/müşteri adı, alan adı, gerçek
  şema/tablo adı repoya ve commit mesajına ASLA. Kalan: history
  rewrite (`git filter-repo`) ve kök manifestlerin repo dışına
  taşınması operatörde.
- **v0.10.623-624 · 626-628 · 629** — Katkı altyapısı ve kapılar:
  PR/issue şablonları, CODEOWNERS, .editorconfig, .gitattributes,
  CONTRIBUTING + SECURITY.md; `release.yml`'e `gate` job'ı
  (tsc+build, vet, `CGO_ENABLED=0` build, test, `make audit`) —
  tag push'u artık kırmızı main'den imaj basmaz (o gün üç kez
  basmıştı); ESLint gerçek kapı (0 hata); `go mod tidy -diff`;
  gofmt borcu tek mekanik sweep'te kapandı (86 dosya; dört
  toolchain aynı listeyi verdi — sapma değil borç) + CI gofmt
  kapısı; Trivy/npm audit HIGH'ları ve govulncheck pini
  (615/616/622). Reddedilen: go.mod'a `toolchain` satırı (operatör
  kararı; CI zaten 1.25.x). Onboarding: `docs/ENV.md` (89
  `COREMETRY_*`, yokken davranış + rol + secret bayrağı),
  `docs/local-dev.md`. Kod: `.github/workflows/ci.yml`,
  `.github/workflows/release.yml`, `.github/CODEOWNERS`.
