# Audit — InfluxDB 2.x dış metrik kaynağı (TFAIL anomalisi → kanıtlı Problem)

**Tarih:** 2026-09-01 · **Durum:** ONAYLANDI 2026-09-01 ("İnflux onay veriyorum" — K1-K6 dahil) · D1 v0.10.222 gemide, D2-D5 sırada
**Hedef:** `GGFailTraceBckt/TFAIL/ADET` serisini (OPERATIONCODE × ERRORCODE)
30 sn'de bir çekip mevcut medyan+MAD dedektörüyle anomali tespit etmek;
anomali açılınca Influx'tan TRACEID + INSTANCEID listesini alıp CH span'leri
ve ES loglarını toplayarak kanıtlı bir Problem açmak, sürerken güncellemek,
düzelince kapatmak.
**Kapsam dışı:** KANALKOD/FUNCTIONCODE gruplaması (v1 konfigürasyonla
kapalı), mevsimsel baseline (Faz 2), Influx'a yazma.

Sayım tabanı bu belgede: "dosya" = değişen kaynak dosyası, "yer" = kod
içindeki dal/switch noktası.

---

## 0. Özet — spec'ten sapan altı karar (onay gerekir)

> **Revizyon v0.10.224 (operatör geri bildirimi, 2026-09-01):** K3 → seri
> Grafana'nın gerçek sorgusuyla hizalandı (`aggregateWindow(every: 1m, fn:
> sum)`, nokta zamanı = kova bitişi `_time`, kısmi kova atlanır, watermark
> ile bir kez yazılır; `_time`'sız düz `sum()` poll-anı gauge'u olarak
> kalır). K5 → "yalnız referans" KALDIRILDI: düz token blob'da saklanır,
> GET maskeler (hasToken), boş girdi korur; `env:`/`file:` referansı
> seçenek (doluysa kazanır).


| # | Spec | Audit önerisi | Neden |
|---|---|---|---|
| **K1** | Yeni `external_metrics` tablosu (migration, 30g TTL) | **Yeni tablo YOK.** Seri → `metric_points`, TRACEID satırları → `exemplars` | `/clickhouse-schema` §1 karar ağacı: *"ham metrik → DUR, mevcut tabloya"*. Dış Distributed prod'da yeni tablo = `_local`+Distributed çifti + 0013 sihirbazı + iki-boot sözleşmesi; `metric_points` prod'da zaten var. Bedava kazanımlar: `metric_catalog` MV'ye otomatik kayıt → Explore/dashboard/alert-rule'da görünür, retention `retention.metrics`'ten. |
| **K2** | `influxdb-client-go/v2` | **net/http + annotated-CSV çözücü** (tempo/thanos/vmetrics ile aynı desen), `QueryAPI` arayüzü arkasında | go.mod'da hiçbir dış metrik istemcisi yok; hepsi `net/http` + dar decode. client-go ~6 transitif bağımlılık getirir (air-gapped imaj). Operatör client-go'da ısrar ederse aynı arayüz arkasına konur — mimari değişmez. |
| **K3** | SORGU 1: `range(start:-2m)` her 30 sn + `sum()` | Seri **gauge** olarak yazılır ("son 2 dk'daki hata sayısı"), 1-dk kova = `avg` | Pencere (2 dk) > aralık (30 sn) → her olay ~4 poll'da sayılır. `sum` delta sayaç gibi okunursa 4× şişer; ya Flux `aggregateWindow` ile hizalanır ya gauge semantiği kabul edilir. Gauge, geç yazılan Influx satırlarına da dayanıklı — ÖNERİLEN. |
| **K4** | "external_metrics baseline anahtarı (source, metric, OP, ERR)" | Anahtar = `(service_name=<kaynak adı>, metric=<sorgu adı>, attrs{operation, error.code})` | `metric_points` ORDER BY `(service_name, metric, time)` önekiyle sınırlı okuma; `groupBy` konfigürasyonu hangi tag'lerin seri boyutu olacağını söyler (v1: OP+ERR), kalan tag'ler (INSTANCEID, FUNCTIONCODE, KANALKOD) **seriye değil exemplar'a** biner (kardinalite kapısı). |
| **K5** | "Token düz metin saklanmayacak; secret referansı" | `tokenRef: "env:COREMETRY_INFLUX_TOKEN_<AD>"` \| `"file:/var/run/secrets/…"` — blob yalnız referansı taşır, çözüm kullanım anında | Repo'da **hiç** emsal yok: tempo/thanos/ES/VM token'ları system_settings JSON'unda düz metin (`tempo/client.go:40-43`, `es_config_persist.go:28-44`), `crypto/aes` sıfır kullanım. Bu YENİ bir sözleşme; Helm `extraEnv` (values.yaml:541) + `existingSecret` ile beslenir. |
| **K6** | Problem'e "external metric anomaly + trace listesi + pod'lar + log imzaları" | Problem satırı DEĞİŞMEZ; kanıt `root_cause_hypotheses` (`anchor_kind=problem`) `DeepEvidence`'a **4 yeni alan**; özne `Kind="external"`, `Service="<kaynak>/<op>/<err>"` | Problem tablosunda kanıt kolonu yok, olmamalı (invariant #4 tam-satır replace). `DeepEvidence` zaten JSON blob, 30g TTL, FINAL okuma, RootCausePanel okuyor. **Tuzak:** RootCauseSynthesizer aynı anchor'ı yeniden yazıp (E2 tam-satır) kanıtı silebilir → `kind=external` atlanır, test pinler. |

---

## 1. Kök durum — teyit

| İddia | Durum |
|---|---|
| Thanos remote-source kalıbı: typed Settings blob + `settingsStore` dar arayüz + `LoadPersisted` boot + `StartConfigRefresh` 30 s + `SavePersisted` + `Configure` takas + maskeli `Snapshot` | ✅ `internal/thanos/client.go:1-20` (başlık bunu tempo şablonu diye ilan ediyor), `:247` settingsStore, `:254` LoadPersisted, `:286` StartConfigRefresh, `:309` SavePersisted, `:325` Configure, `:335` Snapshot |
| Handler: PUT → `SavePersisted(context.WithoutCancel)` → `s.audit` | ✅ `internal/api/thanos_handlers.go:732-809`; audit anahtarı `settings.thanos.update` |
| ⚠ Thanos GET/PUT rotaları **api.go içinde** (`api.go:1224-1225`) | ❌ ev kuralına aykırı emsal — Influx rotaları `influx_routes.go`'ya (`vmetrics_routes.go:28-30` doğru emsal: GET/PUT/**POST test**) |
| Anomaly dedektörü YALNIZ `service_summary_5m` okur | ✅ `anomaly.go:1072`, `:1222` (`fetchAllBuckets`/`fetchAllSeasonal`); `metric_points` okuyan **tek** dedektör yok (op_latency/trace_ops ham `spans`) |
| Saf çekirdek store'suz ve bucket-boyutundan bağımsız | ✅ `verdict.go:34 evaluateAnomaly(metric, buckets, seasonal, rates, seasonalMinSamples, hasOpen, cfg)`, `anomaly.go:340 evalWindow`, `:372 anomalyAction`, `:270 decideAnomaly`, `:220 effectiveMAD`; sabitler `minSamples=12` (:55), `resolveZ=1.5` (:41), `DwellBuckets: 3` (`anomaly_sensitivity.go:314`), P1-only kapısı (:300-311) |
| Metrik-adına bağlı switch noktaları | ⚠ **6 yer**: `metricDirections` (:150), `policyFor` (:164), `flatMADFloor` (:187), `displayMetric` (:1378), `unitOf` (:1389), `anomalyComparator` — dış metrik için hepsine giriş şart (ikinci-yarı sözleşmesi) |
| Problem açma/tazeleme/kapama | ✅ `applyOutcome` (:706): `ruleID="anomaly:"+service+":"+metric`, `openSnap.ByKey(ruleID, service)` (`problem.go:1442`), `UpsertProblem`; "open" = tazele + yön yeniden yaz, "resolve" = son kova bandın içinde → hızlı kapat |
| ⚠ "none" kararında Problem **tazelenmez** (`:709-711`) | Bayat süpürücü `evaluator.go:1108` `updated_at < now-3×interval` (`problem.go:1354-1356`) — dış dedektör "none" tiklerinde de `updated_at`'i **dokunmalı**, yoksa yükselmiş ama dwell'i kaçıran problem "stale" diye kapanır (memory: *seyrek dedektör vs yaşam döngüsü*) |
| Problem öznesi türü | ✅ `problem.go:91 ProblemKindService`, `:94 ProblemKindDB`, `:102 ProblemSubjectKind`; `problems.kind` kolonu `store.go:2541` (v0.9.1338) |
| ⚠ Env-kapsamlı inbox filtresi `service IN (…) OR kind='db'` | `problem_subject_lane.go:66-85` — üçüncü kind eklenince **kaçış genişletilmezse dış problemler env görünümünde KAYBOLUR** (memory: *boş küme kaybolur, sıfır olmaz*). `CountProblemsBySubject` (:116) haritayı iki kind ile başlatıyor |
| Kind'a dallanan dosyalar | 6 dosya / 30 yer: `notify.go:703`, `evaluator/db_capacity.go`, `chstore/env_members.go`, `problem.go`, `problem_subject_lane.go`, `api/inbox.go`; FE `SubjectLink.tsx:41-53` (`subjectIsLinkable`, db rozeti) |
| Kanıt yapısı | ✅ `rootcause_hypothesis.go:46 DeepEvidence{Checked, Exceptions, Templates, Heap, GCPause, Runtime, SlowOps, Business, CodeMeaning}`, `:58 RootCauseHypothesis{AnchorKind, AnchorID, …, Deep, ExemplarTraceID}`; tablo RMT(version) `ORDER BY (anchor_kind, anchor_id)`, partition YOK (Kural P1), 30 g TTL (`store.go:1442`) |
| Synthesizer anchor seçimi "yüksek-şiddetli açık Problem" | ✅ `rootcause_worker.go:71` — kind filtresi yok → dış problemi de alır (K6 tuzağı) |
| Kanıt UI | ✅ `components/RootCausePanel.tsx:192` (exemplar), `:220` (`deep.checked`); `features/anomalies/ProblemDetail.tsx:745` "Root cause analysis" Sect, `:647 ExceptionPodsPanel` (pod dağılımı emsali, `types.ts:6426 ExceptionPodRow`) |
| Leader | ✅ `cache/leader.go:104 NewLeaderHolder(lock,key,ttl)`, `:86 LeaderTTL(interval)=clamp(3×,30s,10m)`, `:70 SetOnAcquire`, `:128 IsLeader`; worker kapısı `main.go` `if mode.worker` (anomaly `:642`, entity syncer `:1022-1030` = **birebir şablon**: holder + OnAcquire ilk tik + `Run(ctx, IsLeader)`) |
| ES çoklu trace_id | ✅ `logstore.Filter.TraceIDs []string` (`logstore.go:64-69`, v0.5.271); ES `terms` × 5 alan yazımı (`elasticsearch.go:2484-2508`), CH `trace_id IN (?…)` (`clickhouse.go:326`) — **iki backend de hazır**, kod değişikliği YOK |
| CH span'leri trace listesiyle | ✅ `spans.idx_trace bloom_filter(0.01)` (`store.go:1041`); `WHERE trace_id IN (…)` emsali `aggregate.go:87`, `neighbors.go:85` (zaman sınırlı + LIMIT) |
| OTLP dönüştürücü seam'i | ✅ `otlp/convert.go:255 ConvertMetrics(*ExportMetricsServiceRequest) ([]*MetricPoint, []*ExemplarRow)`; `repo.go:226 InsertMetrics`; `exemplar_otlp.go:64 InsertExemplars`; `fingerprint.go:51 SeriesFingerprint(metric, dpAttrs, service, instanceID)` |
| Seri okuma seam'i | ✅ `metricquery.go:12 MetricQueryFilter{Name, Service, Filters, GroupBy, Aggregation, From, To, StepSeconds}` + `:144 QueryMetric` → gruplu seri; ⚠ boş kovaların 0-doldurulup doldurulmadığı **doğrulanacak** (yoksa dedektör tarafında pad — `padTrailingSilence` emsali) |
| Exemplar pivot UI | ✅ `api/pivot.go:51-52` `GET /api/exemplars`, `/by-series` → seri → trace pivotu hazır |
| `exemplars` tablosu | `store.go:1935` app-owned `tables` diliminde; ⚠ **dış Distributed prod'da varlığı doğrulanacak** (yoksa exemplar yazımı bayrakla atlanır, kanıt listesi yine DeepEvidence'ta) |
| Retention | ⚠ `retention.metrics` varsayılan **7 g** (`config.go:447`); mevsimsel baseline `seasonalDays=14` (`anomaly.go:72`) → Faz 2 için prod'da `retention.metrics ≥ 14` gerekir; ardışık-pencere baseline (Faz 1) 4 saatle yaşar |
| Secret referans emsali | ❌ yok (grep `env:`/`file:`/`crypto/aes` → 0) — K5 yeni sözleşme |
| Log normalize/hash yardımcısı | ❌ Go'da yok; Drain sample-tabanlı ES/CH tarafında; `stackparse` yalnız exception. Saf `internal/logstore/signature.go` yazılır |
| Sayaç kaydı | `selfobs.Meter().Int64Counter` (`ldap/sync.go:140` emsal); `/admin/stats` = `sysstats.go:430` |

---

## 2. Soru 1 — Thanos kalıbı Influx için yeniden kullanılabilir mi?

**Evet, birebir.** `internal/influx/` = `internal/thanos/`'un simetriği:

```
internal/influx/
  settings.go   Settings{Sources []SourceConfig} · SourceConfig{ID, Name, URL, Org,
                TokenRef, IntervalSec, InsecureSkipVerify, Enabled, Queries []QueryConfig}
                QueryConfig{Name, Flux, EnrichFlux, AttrMap map[string]string,
                GroupBy []string, Thresholds{CriticalZ, Dwell, MinAbsDelta, MinMAD}}
                Snapshot (tokenRef görünür — secret DEĞİL, referans; "resolved" rozeti)
                LoadPersisted / StartConfigRefresh(30s) / SavePersisted / Configure
  client.go     QueryAPI interface{ Query(ctx, flux) ([]Row, error) } — net/http
                POST {url}/api/v2/query?org=… · Content-Type application/vnd.flux ·
                Accept application/csv · annotated CSV çözücü (#datatype/#group/#default,
                çoklu tablo, `error` satırı) · verify + lazy insecure ikiz (Zoom deseni)
  csv.go        saf çözücü (golden test)
  template.go   {{from}}/{{to}}/{{op}}/{{err}} doldurma — Flux string-literal kaçışı
                (`\` ve `"`) + değer regex kapısı ^[A-Za-z0-9_.:\-]{1,64}$
  secret.go     ResolveTokenRef("env:NAME" | "file:/path") — çözüm kullanım anında
  poller.go     Worker: leader-gated tik → SORGU 1 → metric_points; open dış problemler
                için enrich → exemplars + DeepEvidence (§4)
```

Farklar (Thanos'a göre):

| Thanos | Influx |
|---|---|
| Salt-okuma federasyon (CH'ye yazmaz) | **CH'ye yazar** (metric_points + exemplars) — `entity` syncer emsali (Thanos → 0011 tabloları) |
| Auth: bearer/none, token blob'da | `tokenRef` (K5) |
| Cluster listesi; teklik kuralı (`ReconcileClusterSettings`) | Kaynak listesi; teklik = `Name` (service_name olur) |
| Etiket doğrulama arka planı | Test-connection + durum uç noktası |

`system_settings` anahtarı: `influx_sources`. `chstore/influx.go` =
`chstore/thanos.go:15-20`'nin kopyası (`GetInfluxSettingsRaw/PutInfluxSettingsRaw`).
Her rol blob'u hidrate eder (api pod Settings'i servis eder), yalnız
worker lideri poll'lar (§5).

---

## 3. Soru 2 — Anomaly worker'ın kaynakları ve minimum değişiklik

**Bugün:** `Detector.scan` (`anomaly.go:472`) → `ListActiveServiceNames` →
metrik başına **iki toplu MV okuması** (ardışık 5-dk + mevsimsel aynı-dilim)
→ servis başına `evaluateAnomaly` (saf) → `applyOutcome`. Seri kimliği
`(service, metric)`; her şey `service_summary_5m`'e çakılı.

**Minimum değişiklik = saf çekirdeği yeni bir tarayıcıya vermek, `scan`'e
dokunmamak:**

```
internal/anomaly/external.go            (YENİ, ~200 satır)
  type ExternalSeries struct{ Source, Query, Subject string; Labels map[string]string;
                              Buckets []float64; LatestHasData bool }
  type ExternalScanner struct{ store *chstore.Store; notifier *notify.Notifier }
  func (x *ExternalScanner) Scan(ctx, series []ExternalSeries, th Thresholds)
       → her seri için: evaluateAnomaly("ext:"+Query, Buckets, nil, nil, 0, hasOpen, cfgFrom(th))
       → applyOutcomeExternal(...)  (applyOutcome'un kopyası DEĞİL — ortak parça
         `openOrRefresh/resolve` yardımcılarına çıkarılır, HAT A çağrısı aynen kalır)
       → "none" kararında TouchProblem(updated_at) (§1 bayat-süpürücü bulgusu)
```

| Nokta | Karar | Gerekçe |
|---|---|---|
| Kova boyutu | **1 dk** (HAT A 5 dk) | Çekirdek kova-boyutundan bağımsız (`[]float64`). 30 sn poll + 1-dk kova: ilk karar için `minSamples+dwell = 15` kova = **15 dk** (5-dk kovada 75 dk olurdu). |
| Baseline | Ardışık pencere **4 saat** (240 kova), mevsimsel `nil` | `seasonalMinSamples` altında ardışık baseline'a düşüş bekleniyor — **uygulamada `verdict.go` üzerinde doğrulanacak**, yoksa dış yol ardışık baseline'ı doğrudan verir. Faz 2: `retention.metrics ≥ 14` sonrası aynı-dilim. |
| Kova değeri | `avg(value)` (gauge, K3) | `sum` 4× şişirir. |
| Boş kova | **0'a pad** — baseline penceresinde en az bir kez görülmüş seriler için | Influx `sum()` boş grupta satır ÜRETMEZ; pad yoksa hata kesilen seri "resolve" göremez. `QueryMetric` doldurmuyorsa dedektörde pad. |
| Politika | Yön `up`, birim `count`, `flatMADFloor` tabanı `max(1, 0.05×median)` | 6 switch yerine `ext:` öneki ile giriş; eşikler `Thresholds` (kaynak başına, varsayılan: `CriticalZ`+`Dwell` global `anomaly_sensitivity`'den, `MinAbsDelta=5`, `MinMAD=1`). Hacim kapısı (`rates`) dış metrikte **yok**. |
| Özne | `Kind="external"`, `Service="<kaynak>/<op>/<err>"`, `Metric="ext:<sorgu>"`, `RuleID="anomaly:ext:<kaynak>:<sorgu>:<op>:<err>"`, `Comparator=">"` | `ByKey(ruleID, service)` ile idempotent tazeleme. `problems.service` LC: sözlük yalnız **anomalileşen** çiftlerle büyür (ölçülmeden LC — C3 — ama sınır doğal). |
| Flapping | HAT A ile aynı: `allOpen` (dwell kova aynı yönde) açar, son kova bandın içinde (`resolveZ=1.5`) kapatır | Aynen isteniyor. |
| Tarayıcının sahibi | **Influx worker tiki** (30 sn), `anomaly.Detector` DEĞİL | Poll → yaz → oku → karar → enrich tek liderde; 2-dk dedektör tiki kararı geciktirirdi. Çağrı yeri test-pinli (memory: *test edilmiş ama ulaşılamaz*). |

Seri okuma: tik başına **tek** `QueryMetric{Name:"ext:<sorgu>", Service:<kaynak>,
GroupBy:["operation","error.code"], Aggregation:"avg", StepSeconds:60, From:now-4h}`
— ORDER BY öneki `(service_name, metric, time)` ile sınırlı, ≤ 240×N satır.

**Değişen dosyalar:** `anomaly/anomaly.go` (6 yer + ortak yardımcı çıkarma),
`anomaly/external.go` (yeni), `chstore/problem.go` (`ProblemKindExternal`,
`TouchProblem`), `chstore/problem_subject_lane.go` (`OR kind='external'` kaçışı
+ sayım haritası), `api/inbox.go` (lane), `notify/notify.go:703` (md yoksa
db-değil dalı — dış için de "md yok" doğru), FE `SubjectLink.tsx` (rozet,
linksiz), `ProblemsSection.tsx`/`streams.tsx` (kind geçişi zaten var).

---

## 4. Soru 3 — Problem / correlator kanıt yapısı

Correlator (`internal/correlator`) servis topolojisi üzerinde çalışır; dış
öznenin topolojisi yok → **correlator'a dokunulmaz.** Kanıt, hesaplanmış-state
tablosu `root_cause_hypotheses`'e (`anchor_kind="problem"`, `anchor_id=Problem.ID`)
`DeepEvidence` genişletmesiyle yazılır:

```go
// chstore/rootcause_hypothesis.go — DeepEvidence'a 4 alan
External      *ExternalMetricEvidence `json:"external,omitempty"`
TraceIDs      []string                `json:"traceIds,omitempty"`      // ≤50, en yeni önce
AffectedPods  []PodHit                `json:"affectedPods,omitempty"`  // {Pod, Count, LastSeenNs}
LogSignatures []LogSignature          `json:"logSignatures,omitempty"` // {Hash, Template, Count, Severity, Sample, TraceCount}

type ExternalMetricEvidence struct{ Source, Query string; Labels map[string]string;
  Current, Median, MAD, Z float64; WindowFromNs, WindowToNs int64; SpanSummary []TraceSpanSummary }
```

Enrichment akışı (`influx/enrich.go`, açık dış problem başına, tik başına
en çok **20** anchor, açılışta hemen + sürerken **5 dk**'da bir, pencere
`[startedAt−2m, now]`):

1. **SORGU 2** (`EnrichFlux`, `{{op}}/{{err}}/{{from}}/{{to}}`) → ≤50 satır
   `(_time, TRACEID, INSTANCEID, FUNCTIONCODE, KANALKOD)`. TRACEID
   küçük-harf 32 hex doğrulanır (span/ES sözleşmesi), geçmeyen düşer + sayaç.
2. **exemplars** ← her satır bir `ExemplarRow{Fingerprint: otlp.SeriesFingerprint(...),
   Metric, Service, Time:_time, Value:1, TraceID, FilteredAttrs{k8s.pod.name,
   FUNCTION_CODE, CHANNEL_CODE}}` → Explore'da seri→trace pivotu bedava
   (`/api/exemplars/by-series`).
3. **CH span'leri** — `WHERE time BETWEEN from−5m AND to+5m AND trace_id IN (…)
   LIMIT 5000 SETTINGS max_execution_time=5` (bloom `idx_trace`) → trace başına
   özet: kök servis, hata span'i, en yavaş op; `ExemplarTraceID` = ilk hatalı
   trace (RootCausePanel:192 zaten çizer). **CH'de trace_id ARANMAZ** — liste
   Influx'tan gelir, CH yalnız `IN` ile okur.
4. **ES logları** — `logstore.Search(Filter{TraceIDs, From, To, SeverityMin:13
   (WARN), Limit:500})` → `logstore.NormalizeSignature(msg)`: UUID / ≥16-hex /
   ISO-zaman / IP / ≥2 haneli sayı → `<x>` yer tutucu, boşluk sıkıştırma;
   `xxhash64` → grupla, ilk N=15 imza (count desc). **Örnek mesaj verbatim**
   saklanır — bu gruplama anahtarıdır, redaksiyon değil (feedback-no-redaction).
5. **Pod'lar** — INSTANCEID sayımları (`k8s.pod.name`), span res-attr'larıyla
   çapraz doğrulanır; ⚠ `Problem.Pod` tek string, listeye **değil** kanıta yazılır.
6. `UpsertHypothesis` — mevcut satır FINAL okunur, trace listesi birleşim
   (≤50 en yeni), sayımlar toplanır; `Confidence` = mevcut kanıt türü sayısı.

**Tuzak — E2:** `RootCauseSynthesizer` (`rootcause_worker.go:71`) yüksek-şiddetli
açık problemleri anchor alır ve tam-satır yazar → dış kanıtı **siler**.
Çözüm: synthesizer `Kind=external` anchor'ları atlar; tablo-testi pinler.

**UI:** `ProblemDetail.tsx` `kind==='external'` dalı — servis-tabanlı
bölümler (Top offenders, occurrences) çizilmez; yerine "External evidence"
Sect: metrik şeridi (`CorePanel`/uPlot, `QueryMetric` serisi), trace tablosu
(`useDataTable`, `/traces/{id}` linki, exemplar ✓), pod tablosu
(`ExceptionPodsPanel` deseni, `/pods/…` pivotu bayrağa bağlı), log imzaları
tablosu (`useDataTable`, örnek + count + severity rozeti). Hepsi mevcut
atomlarla; yeni primitive yok (`/frontend-design-system` önce aranır).

---

## 5. Soru 4 — Leader election hook noktası

`main.go:1022-1030` entity syncer bloğu **birebir şablon**:

```go
if mode.worker {
    influxLeader := cache.NewLeaderHolder(lockImpl, "influx-poller", cache.LeaderTTL(30*time.Second)) // TTL 90 s, refresh 30 s
    w := influx.NewWorker(influxSvc, store, logsStore, ext /* anomaly.ExternalScanner */, notifier)
    influxLeader.SetOnAcquire(func() { w.Tick(ctx) })   // failover'da ilk tiki ticker'a bırakma (v0.9.730 dersi)
    influxLeader.Start(ctx)
    go w.Run(ctx, influxLeader.IsLeader)                // tik başında `if !isLeader() { return }`
}
```

- Settings blob'u (`influxSvc.LoadPersisted` + `StartConfigRefresh 30s`) **her
  rolde** (api pod Settings'i servis eder, worker poll'lar).
- Tek holder poll+değerlendirme+enrich'i kapsar (aynı tikte ardışık). Enrich
  ES/CH maliyeti 20 anchor/tik ile sınırlı.
- Redis'siz tek pod: noop kilit — mevcut işçilerle (anomaly, evaluator) aynı yol, ayrı davranış yok.
- Kaynak başına aralık farklıysa `Run` en küçük aralıkla tikler, kaynak
  başına `nextDue` tutar (stateless: liderlik el değiştirince yalnız bir tik
  erken/geç).

---

## 6. Soru 5 — ES federasyonunda çoklu trace_id (terms)

**Hazır, değişiklik yok.** `Filter.TraceIDs []string` v0.5.271'den beri:
ES tarafı `bool.should[terms{<alan>: ids}]` × {yapılandırılmış alan, `trace.id`,
`TraceId`, `trace_id`, `traceId`} + `minimum_should_match:1`
(`elasticsearch.go:2484-2508`), CH tarafı `trace_id IN (?,…)` (`clickhouse.go:326`).
50 id ≪ `index.max_terms_count` (65 536). Gövde-içi eşleşme (`traceTermsAny`'nin
`match`/`multi_match` dalları) çoklu yolda **yok** — bilinçli; trace alanı
yapılandırılmamış kurulumda log kanıtı boş döner ve DeepEvidence bunu ilan
eder (`LogSignatures` boş + `Checked` satırında "ES trace alanı yok").
`SeverityMin:13` (WARN) + `[from,to]` + `Limit:500` maliyeti sınırlar; ES-cost
disiplini: yalnız enrich anında, liste prefetch yok.

---

## 7. Veri modeli — Influx → Coremetry eşlemesi

| Influx | Coremetry | Not |
|---|---|---|
| bucket + measurement + field (`ADET`) | `metric_points.metric = "ext:<sorgu adı>"` (örn. `ext:tfail_adet`), `instrument="gauge"`, `unit="{failures}/2m"`, `temporality=""` | K3 |
| kaynak (Settings `Name`) | `service_name` (LC, 1 değer) + `res_keys{service.name, coremetry.source="influx", influx.bucket, influx.measurement}` | ORDER BY öneki |
| `OPERATIONCODE`, `ERRORCODE` (groupBy) | `attr_keys/attr_values{operation, error.code}` | seri boyutu |
| `INSTANCEID`, `FUNCTIONCODE`, `KANALKOD` | **seriye girmez**; exemplar `filtered_attributes{k8s.pod.name, FUNCTION_CODE, CHANNEL_CODE}` + kanıt | kardinalite; KANALKOD'u açmak = `groupBy`'a eklemek, kod değişmez |
| `TRACEID` | `exemplars.trace_id` (küçük-harf 32 hex) | span/ES join anahtarı |
| `_time` (SORGU 1: poll anı) | `time` = poll anı (Influx `_time` değil — `sum()` zamanı kaybeder) | |

Yazım yolu: poller `ExportMetricsServiceRequest` proto kurar →
`otlp.ConvertMetrics` → `InsertMetrics` (async_insert korunur). Böylece attr
yönlendirme + fingerprint **tek kaynaktan** (invariant #1'in ruhu: CH'deki her
satır dönüştürücüden geçti). `/otlp-converter` skill'i uygulamada okunur.

Hacim: ≤ (OP×ERR gözlenen çift) satır / 30 sn — yüzlerce/dk; SORGU 1'e
`limit(n: 5000)` + poller tarafında grup tavanı (aşım = sayaç + log, satır
düşer).

---

## 8. Riskler

| # | Risk | Etki | Önlem |
|---|---|---|---|
| R1 | Flux enjeksiyonu (`{{op}}`/`{{err}}` string-literal içine) | Influx'ta keyfi sorgu | Kaçış + regex kapısı (§2 template.go); geçmeyen değer enrich edilmez + log. Alternatif `params` nesnesi hedef Influx sürümünde doğrulanırsa tercih. |
| R2 | 2-dk pencere / 30-sn aralık çakışması | Sayım 4× şişer, z-score baseline'la tutarlı olsa da UI "hata sayısı" yalan söyler | K3 gauge + birim etiketi `{failures}/2m`; operatör Flux'ı `aggregateWindow(every:30s)` ile hizalarsa `instrument=sum/delta`'ya geçiş konfigürasyonla |
| R3 | Boş küme kaybolur (Influx `sum()` boş grupta satır yok) | Kesilen hata hiç "resolve" olmaz | Baseline penceresinde görülen seriler 0'a pad (§3) — kayan-pencere simülasyon testi (memory v0.10.199) |
| R4 | Synthesizer tam-satır yazıp kanıtı siler (E2) | Kanıt sessizce kaybolur | `kind=external` atla + test |
| R5 | Env-kapsamlı inbox `service IN (…)` dış özneyi eler | Problem inbox'ta görünmez | `OR kind='external'` kaçışı (db emsali) + sayım haritası + test |
| R6 | Bayat süpürücü "none" tiklerinde kapatır | Yükselmiş ama dwell dışı problem "stale" kapanır | `TouchProblem` her tikte; Influx erişilemezse süpürücünün kapatması **doğru** ("kaynak sustu") ve gerekçe eki bunu söyler |
| R7 | `retention.metrics` 7 g < mevsimsel 14 g | Faz 2 baseline imkânsız | Faz 1 ardışık; Faz 2 kapısı = prod ayarı |
| R8 | `exemplars` tablosu dış Distributed prod'da yok olabilir | Exemplar yazımı hata | Boot probe'u (mevcut `tableIsExternalDistributed` deseni) + atla + sayaç |
| R9 | Token referansı çözülemiyor (env yok / dosya yok) | Poll sessizce 401 | Test-connection "resolved ✗", durum ucu son hata, Settings rozeti |
| R10 | Yeni bağımlılık (client-go) | air-gapped imaj, supply chain | K2 net/http; go.mod değişmez |
| R11 | Saat kayması Influx ↔ CH/ES | Span/log penceresi kaçırır | ±5 dk slack (§4 adım 3) |
| R12 | Trace ID büyük harf / 16 hex | Join boş | Normalize + doğrula + düşen sayısı kanıtta ("12/50 id geçersiz") |
| R13 | `problems.service` LC sözlüğü | Anomalileşen çift sayısıyla büyür; on binler olası değil | Sicile ölçüm notu; 10k+ görülürse `String`'e geçiş |

---

## 9. Alternatifler

| Alternatif | + | − | Karar |
|---|---|---|---|
| **A. metric_points + exemplars (ÖNERİLEN)** | Sıfır şema, sıfır migration, prod'da hazır; catalog/Explore/alert-rule bedava; retention mevcut | Attr-dizisi okuması (`indexOf`) — hacimde önemsiz; birim/temporality disiplini (HAT B) | ✅ |
| B. `external_metrics` tablosu (spec) | Şema temiz (`Map(String,String)`), Explore'dan izole | ORDER BY'a Map giremez → materialized anahtar kolonları; `_local`+Distributed + 0013 sihirbazı + iki-boot; `metric_catalog`/Explore/alert entegrasyonu ayrı iş; §1 karar ağacına aykırı | ❌ |
| C. OTel Collector ile çekme (Telegraf/receiver) | Coremetry'ye kod girmez | Collector-contrib'de Flux-poll receiver yok; enrichment (SORGU 2) yine Coremetry'de olmalı; operatör iki yerde ayar | ❌ |
| D. Değerlendirmeyi `anomaly.Detector` tikine eklemek | Tek dedektör | 2-dk tik → tespit gecikmesi; `scan` HAT-A'ya çakılı, iki okuma yolu bir döngüde | ❌ (saf çekirdek paylaşılır, tarayıcı ayrı) |
| E. Trace listesini Problem satırında JSON kolon | Tek yerde | invariant #4 (tam-satır replace), her okuma yolu değişir | ❌ |
| F. Token'ı AES ile blob'da şifrele | Tüm remote kaynaklara uygulanabilir | Anahtar yönetimi (nerede duracak? = aynı sorun), repo'da emsal yok, K5'in ihtiyacını karşılamaz | ❌ (ileride ayrı spec) |

---

## 10. Dosya bazlı değişiklik planı — dilimler

Her dilim kendi `v0.10.X` sürümü (216'dan başlar), her biri `go test -race`
+ `vitest` yeşil. Skill sırası: `/api-route` (D1), `/frontend-conventions` +
`/frontend-design-system` (D1, D5), `/tdd` (D2-D4), `/otlp-converter` (D2),
`/clickhouse-schema` (tamamlandı — bu belge).

### D1 — v0.10.216 · Kaynak yönetimi (poller yok)
| Dosya | Değişiklik |
|---|---|
| `internal/influx/settings.go` (yeni) | Settings/Snapshot/Load/Save/Refresh/Configure (thanos kopyası) |
| `internal/influx/client.go`, `csv.go`, `template.go`, `secret.go` (yeni) | QueryAPI + CSV çözücü + şablon + tokenRef |
| `internal/chstore/influx.go` (yeni) | `Get/PutInfluxSettingsRaw` (`influx_sources`) |
| `internal/api/influx_routes.go` (yeni) | `registerInfluxRoutes`: GET/PUT `/api/settings/influx` (admin, audit `settings.influx.update`), POST `/api/settings/influx/test` (admin; formdaki değerlerle SORGU 1'i `limit(n:20)` ile koşar, kaydetmez), GET `/api/influx/status` (her rol, salt-okur) |
| `internal/api/api.go` | **tek satır** `s.registerInfluxRoutes(mux)` |
| `main.go` | `influxSvc := influx.New()` + LoadPersisted + StartConfigRefresh (her rol) |
| `frontend/src/lib/types.ts`, `lib/api.ts` | `InfluxSourceSnapshot/Input/TestResult/Status` + 4 istemci metodu |
| `frontend/src/pages/settings/InfluxTab.tsx` (yeni), `tabIndex.ts`, `settingsTabIndex.test.ts` | Sekme `influx` "Influx kaynakları" (`clusters`'tan sonra; "Remote Sources" grubu **yok**, sekmeler düz); ClustersTab liste + MetricsBackendTab Test deseni; `Field`/`Button`/`Badge` atomları; tokenRef alanı + "resolved" rozeti |
| Testler | `csv_test.go` (golden: çoklu tablo, error satırı, boş), `template_test.go` (kaçış + regex kapısı), `secret_test.go` (env/file/geçersiz), `settings_test.go` (maskeleme, boş PUT referansı korur), `influx_routes_test.go` (httptest: auth/audit), `vitest` sekme indeksi |

### D2 — v0.10.217 · Poller → metric_points
| Dosya | Değişiklik |
|---|---|
| `internal/influx/poller.go` (yeni) | Worker.Run/Tick; SORGU 1 → proto → `otlp.ConvertMetrics` → `InsertMetrics`; grup tavanı; sayaçlar (`influx_polls_total`, `influx_points_total`, `influx_errors_total`, `influx_rows_dropped_total`) |
| `main.go` | worker kapısı + LeaderHolder (§5) |
| `internal/chstore/sysstats.go` | `/admin/stats` Influx satırı |
| Testler | `poller_test.go` (mock QueryAPI: satır→MetricPoint eşlemesi tablo-testi; K3 gauge; tavan; hata sayacı), `otlp` golden (dış kaynaklı istek dönüştürücüden aynen geçer) |

### D3 — v0.10.218 · Dış anomali + Problem kind
| Dosya | Değişiklik |
|---|---|
| `internal/anomaly/external.go` (yeni), `anomaly.go` (6 yer + `openOrRefresh/resolve` ortak yardımcı) | §3 |
| `internal/chstore/problem.go`, `problem_subject_lane.go`, `api/inbox.go`, `notify/notify.go` | `ProblemKindExternal`, `TouchProblem`, lane kaçışı, sayım |
| `internal/influx/poller.go` | tik: `QueryMetric` → pad → `ExternalScanner.Scan` |
| FE `SubjectLink.tsx`, `ProblemsSection.tsx` | rozet "external", linksiz |
| Testler | `external_test.go` (tablo: open/refresh/resolve/none-touch; kayan-pencere simülasyonu R3; dwell; MinAbsDelta), `problem_subject_lane_test.go` (üç kind), çağrı-yeri pin testi (poller Scan'i çağırıyor) |

### D4 — v0.10.219 · Enrichment
| Dosya | Değişiklik |
|---|---|
| `internal/influx/enrich.go` (yeni) | SORGU 2 → exemplars + span özeti + ES imzaları + pod'lar → `UpsertHypothesis` (birleşim) |
| `internal/logstore/signature.go` (yeni) | `NormalizeSignature` + `SignatureHash` (saf) |
| `internal/chstore/rootcause_hypothesis.go` | DeepEvidence 4 alan (JSON, şema değişmez) |
| `internal/chstore/spans_by_trace.go` (yeni) | `SpanSummariesForTraces(ctx, ids, from, to)` — bloom + LIMIT + max_execution_time |
| `internal/anomaly/rootcause_worker.go` | `kind=external` atla |
| Testler | `signature_test.go` (tablo: UUID/hex/zaman/IP/sayı/boşluk; Türkçe mesaj), `enrich_test.go` (mock QueryAPI + CH mock + `logstore` fake: birleşim, ≤50, geçersiz id sayımı, ES alanı yoksa ilan), synthesizer atlama pini |

### D5 — v0.10.220 · Problem detay kanıt UI
| Dosya | Değişiklik |
|---|---|
| `frontend/src/features/anomalies/ProblemDetail.tsx` + `ExternalEvidencePanel.tsx` (yeni) | §4 UI; `useDataTable` × 3; `CorePanel` şerit; URL-first (`?tab=`) |
| `lib/types.ts` | `DeepEvidence` alanları |
| Testler | vitest: kind dallanması, boş kanıt durumu, tablo storageKey'leri |

### D6 — sonra (kuyruk)
Mevsimsel baseline (retention kapısı), KANALKOD `groupBy` UI anahtarı,
tokenRef sözleşmesini tempo/thanos/VM'ye yayma (ayrı spec).

**Efor:** D1 ~1 gün · D2 ½ · D3 1 · D4 1 · D5 ½ → ~4 iş günü, 5 sürüm.

### Gerçekleşen sürümler (2026-09-01 → 02) — plandan sapmalar

Plandaki numaralar (216-220) /traces dilimlerine gitti; dilimler bir
sonraki boş numaradan devam etti.

| Dilim | Sürüm | Sapma / not |
|---|---|---|
| D1 | v0.10.222 | Plan aynen. tokenRef `env:`/`file:` + düz token reddi |
| D1' | v0.10.224 | **Operatör:** düz token SAKLANIR, GET maskeler (`hasToken`); "yalnız referans" sözleşmesi kaldırıldı. Seri Grafana `aggregateWindow(1m, sum)` ile aynı: kova bitişi `_time`, kısmi kova atlanır, (kaynak, sorgu) watermark |
| D2 | v0.10.223 | Plan aynen (`influx_status.go` SAF telemetri dosyası, conn_strategy allowlist) |
| D3 | v0.10.228 | Pencere sabit 4 saat DEĞİL: kaynağın gözlenmiş ilk/son kovası (≤240 dk) — sabit pencere genç kaynağı sıfırla doldurup medyanı 0 yapıyordu. Tarayıcı yalnız BAŞARILI poll sonrası koşar (Influx düşükken sıfır-padli sahte iyileşme yok); "none" kararında touch. `evaluateAnomaly`'ye seasonalMinSamples 0 geçilemez (boş mevsimsel kazanır). Lane kaçışı iki eşitlik (IN-liste kapısı) |
| D4 | v0.10.229 | Plan aynen + `{{op}}`/`{{err}}` = adında operation/error geçen ilk tag; her tag kendi adıyla da değişken. Sayı imzası regex'i sondaki sınırsız ("1500ms" → "<x>ms") |
| D5 | v0.10.230 | Plan aynen; hipotez `/rootcause` zarfından. ⚠ Lokalde Influx yok — panel canlı dış problemle GÖRÜLMEDİ |
| D6 | v0.10.231 | Mevsimsel kapı: ufuk < 3 gün → okuma yok; ufuk < gün sayısı → gün sayısı ufka iner (eldeki geçmişle mevsimsel). KANALKOD checkbox. tokenRef yayma AYRI spec (açılmadı) |

**Operatörde kalan:** prod'da kaynak + SORGU 1/2 tanımı (Influx sekmesi),
`retention.metrics ≥ 14` (mevsimsel tam güç), ilk canlı problemde D5
panelinin görsel kontrolü.

---

## 11. Onay soruları

1. **K1** — `external_metrics` yerine `metric_points` + `exemplars`? (öneri: evet)
2. **K2** — client-go yerine net/http + CSV? (öneri: evet; client-go'ya dönüş arayüz arkasında ücretsiz)
3. **K3** — SORGU 1 gauge semantiği mi, Flux'ı `aggregateWindow` ile hizalamak mı? (öneri: gauge, birim `{failures}/2m`)
4. **K5** — `tokenRef` `env:`/`file:` sözleşmesi; prod'da secret'ı Helm `extraEnv`+`existingSecret` ile kim besleyecek?
5. **K6** — özne `"<kaynak>/<op>/<err>"` + `kind=external`, inbox'ta linksiz rozet — kabul?
6. §3 — 1-dk kova + 4 saatlik ardışık baseline; mevsimsel Faz 2 (`retention.metrics ≥ 14` sonrası)?
7. Settings sekmesi adı `Influx kaynakları`, `Remote clusters`'tan sonra (grup yok)?
8. Prod teyitleri (uygulamadan önce operatörde): `exemplars` tablosu dış Distributed'da var mı; Influx sürümü (`params` desteği); ES `fields.TraceID` yapılandırılmış mı.
