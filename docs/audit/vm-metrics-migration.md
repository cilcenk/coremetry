# VictoriaMetrics tek metrik deposu — geçiş auditi

**Tarih:** 2026-09-02 · **Repo:** `/Users/cenk/Documents/gotrace` · **Taban sürüm:** v0.10.273
**Karar (operatör):** VM TEK metrik deposu. OTLP metrik ingest'i VM'e (`/opentelemetry/v1/metrics`) yönlendirilir; ClickHouse'a raw OTLP metrik yazımı (`metric_points`) KALDIRILIR. CH'de yalnız span/log + span-derived rollup'lar (HAT A) kalır.
**Yöntem:** salt okuma (Explore ajanı + ana oturum grep doğrulaması). Her iddia `dosya:satır` ile. Dış bilgi (VM sürüm davranışı) **"doğrulanmalı"** işaretli.
**Sayım tabanı:** "ifade" değil **nesne** — `CREATE TABLE` / `CREATE MATERIALIZED VIEW` başına bir.
**Durum:** ONAY BEKLİYOR — kod yok.

## 0. Özet

- Repo'da bugün **VM'e yazan hiçbir kod yok**; `internal/vmetrics` yalnız okur (`ConvertMetrics` CH'ye yazar, doğrulandı: `internal/otlp/convert.go:255`, `main.go` metrics consumer).
- Seam üzerinden (`internal/api/metricsource.go`) **5 uç ailesi** VM'e hazır; seam **dışında** `metric_points` okuyan **41 chstore dosyası** var — geçişin gerçek ağırlığı "okuyanlar VM'e" dilimi (≈20 sabit-adlı okuyucu ailesi: hosts/infra/JVM/databases/pod↔servis/DQL/cardinality/anomaly-external/exemplar).
- Üç 🔴 risk: **kardinalite** (CH satır-içi dizi → VM seri boyutu; pod × instance × tüm attr'lar), **exemplar kaybı** (VM saklamıyorsa `/api/exemplars` ölür → CH'de kalsın), **13 aylık rollup ufkunun VM karşılığı** (downsampling Enterprise mi? cevapsız → Dilim 2 öncesi karar).
- Çift yazım **ham OTLP gövdesinden** forward edilmeli (CH modeli kayıplı: scope, DP flags, exp-histogram explicit'e materyalize); ayrı kuyruk, asla senkron.
- Karşılaştırma için hazır seam (`?metricsrc=vm|ch`) + `cmd/paritycheck` MOD B var; eksik olan nokta-nokta kıyas ucu → `internal/api/metrics_routes.go` (registry, api.go'ya satır yok).
- VM Settings **liste olmasın** (tek endpoint kararı kodda yazılı); iki alan eklensin: `WriteURL` (vminsert ≠ vmselect) ve `WriteEnabled` (varsayılan false). Token/secret: v0.10.273 tokenRef zaten yerinde.

---

## 1. Mevcut OTLP metrik yazım yolu ve ona bağlı HER ŞEY

### 1.1 Yazım yolu (receiver → ClickHouse)

| Adım | Kod |
|---|---|
| OTLP/HTTP `POST /v1/metrics` | `internal/otlp/http.go:251` (route), `:387` (`handleMetrics`) |
| OTLP/gRPC `MetricsService/Export` | `internal/otlp/grpc.go:128` (kayıt), `:223` (`Export`) |
| Dönüştürme | `internal/otlp/convert.go:255` `ConvertMetrics` → `:311` `convertMetric` → `:422` `appendExemplars` |
| Seri kimliği | `internal/otlp/fingerprint.go:51` `SeriesFingerprint(name, dpAttrs, service, instance)` |
| Kuyruk | `internal/otlp/http.go:198` `addMetric` → `main.go:444` `consumer.NewSized("metrics", …, store.InsertMetrics)` |
| CH yazımı | `internal/chstore/repo.go:250` `InsertMetrics` (async_insert; `series_fingerprint`/`is_monotonic` kolonları boot-probe'lu) |
| Exemplar yan-akışı | `main.go:447` `exemplarConsumer` → `internal/chstore/exemplar_otlp.go:64` `InsertExemplars` |

Beş OTLP tipinin beşi de işleniyor (Gauge / Sum / Histogram / ExponentialHistogram / Summary — `convert.go:311-410`); `switch` **`default` dalsız** (`/otlp-converter` §1 "Metrik: 5/5 tip"). Exponential histogram, `internal/otlp/exp_histogram.go` ile **explicit bounds'a materyalize** ediliyor — yani CH'de native exponential kova yapısı yok, sentetik `bucket_bounds`/`bucket_counts` var.

**Kardinalite açısından kritik iki satır:**
- `convert.go:283` — `resK, resV = attrsToArrays(rm.Resource.Attributes)` → **TÜM resource attribute'ları verbatim** her metrik satırına kopyalanıyor.
- `convert.go:316` — `attrK, attrV := attrsToArrays(attrs)` → **TÜM datapoint attribute'ları verbatim**.

CH'de bunlar `Array(LowCardinality(String))` anahtar + `Array(String)` değer olarak **satırda** durur; **seri sayısını çarpmaz**. VM'de her biri **ayrı label** olur ve **seri sayısını çarpar** — §2.3'ün tüm konusu bu.

### 1.2 `metric_points` şeması

`internal/chstore/store.go:1883-1917`:

```
metric LC, instrument LC, description String ZSTD(1), unit LC,
service_name LC, host_name LC,
time DateTime64(9) Delta+ZSTD(3), start_time DateTime64(9),
value Float64 Gorilla, count UInt64, sum_value Float64 Gorilla,
min_value Float64, max_value Float64,
attr_keys Array(LC) / attr_values Array(String) ZSTD(1),
res_keys  Array(LC) / res_values  Array(String) ZSTD(1),
bucket_bounds Array(Float64), bucket_counts Array(UInt64),   -- v0.5.358
temporality LC                                               -- v0.6.56
ENGINE MergeTree · PARTITION BY toDate(time) · ORDER BY (service_name, metric, time)
```

Sonradan eklenen iki EXPLICIT kolon, dağıtık-güvenli blokla: `series_fingerprint` (`store.go:3072-3090`) ve `is_monotonic` (`store.go:3104-3115`). İkisi de dış Distributed + `cluster_name` unset'te **atlanır** ve okuma bozulmadan degrade olur — §4'ün "prod kısıtları" başlığında tekrar geleceğiz.

LC anahtarları: `metric`, `instrument`, `unit`, `service_name`, `host_name`, `temporality`, `attr_keys[]`, `res_keys[]`. `migrations/0006_metric_points_lc.sql:1-14` üç serbest kolonu (description/attr_values/res_values) opsiyonel olarak LC'ye indiriyor (operatör-uygulamalı).

Env, kolonda DEĞİL `res_keys`'te: `internal/chstore/filterexpr.go:402` `metricEnvExpr` = `coalesce(res_values[indexOf(res_keys,'deployment.environment.name')], res_values[indexOf(res_keys,'deployment.environment')])`. **Bu ifade VM'de karşılıksız** — `metricsource.go` `EnvFilterExpr` yorumu bunu zaten ilan ediyor (`/clickhouse-schema` §7 "VM: ifade EDİLEMİYOR → envAmbiguous").

### 1.3 `metric_points`'ten türeyen CH nesneleri

**4 MV (hepsi `store.go`, combined):**

| MV | Satır | Kim okuyor |
|---|---|---|
| `spanmetrics_calls_5m` | `store.go:3691` | **okuyucusuz** — `/clickhouse-schema` §11.2: "kalıcı 0 satır, bozuk değil belgeli" |
| `spanmetrics_hist_5m` | `store.go:3733` | aynı |
| `spanmetrics_duration_5m` | `store.go:3765` | aynı |
| `metric_catalog` | `store.go:3966` | **CANLI** — katalog/picker, `/databases` tazelik kapısı |

**2 rollup ailesi (`migrations/`), her biri 6 tablo + 3 MV:**

| Aile | Dosya | Kademe | Kaynak | TTL |
|---|---|---|---|---|
| **METRİK (0003)** | `migrations/0003_rollup_metrics.sql:23,44,48` | 1m→5m→1h | `FROM metric_points_local` (`:60`) | 14g / 90g / 13 ay |
| **ROUTE (0008)** | `migrations/0008_rollup_metrics_route.sql:82,103,107` | 1m→5m→1h | `FROM metric_points_local` (`:135`) | 14g / 90g / 13 ay |

Okuma tarafı: `internal/chstore/metric_rollup_read.go:48-50` (kademe tablosu) + `:64` `metricRollupPlan`; `internal/chstore/metric_rollup_route_read.go:110` `metricRollupRoutePlan`. Devreye giriş noktası `internal/chstore/metricquery.go:232`. İkisi de **fail-open** — uygun değilse sessizce ham `metric_points`'e düşer (`/clickhouse-schema` §6 "Ortak sözleşme").

**Ek tablo:** `exemplars` (`store.go:1935`) — `series_fingerprint` üzerinden `metric_points` ile aynı kimliği taşır; TTL `retention.spans`'tan.

**Backfill/mutation dosyaları:** `migrations/0004_backfill.sql` (0003 kaskadını geçmişten doldurma), `migrations/0006_metric_points_lc.sql`, `migrations/0003_rollup_metrics_rollback.sql`.

### 1.4 Job'lar / worker'lar

| Job | Kod | metric_points bağı |
|---|---|---|
| Metrik consumer (ingest rolü) | `main.go:444`, `:462` | Yazıcı |
| Exemplar consumer | `main.go:447` | Yazıcı |
| Retention TTL uygulama | `internal/chstore/retention.go:104` (`retention.metrics` → `metric_points.time`) | ALTER MODIFY TTL |
| Retention enforcer (saatlik DROP PARTITION) | `internal/chstore/retention_enforce.go:48`; başlatma `main.go:789` | Partition düşürür |
| Purge | `internal/chstore/purge.go:41` | Tablo listesinde |
| Ayar yenileyici (VM dahil) | `main.go:1141`, `main.go:1175` | — |
| Influx poller (worker lideri) | `main.go:1093`, `internal/influx/poller.go:3` | **Yazıcı** (`ext:*` gauge) |
| Anomaly / evaluator | `main.go:640`, `:648` | Okuyucu (§5) |

**`metric_catalog` için ayrı bir worker YOK** — MV, INSERT anında dolar (`store.go:3966`). Yani "metric_catalog worker'ı" fiilen **metrik INSERT'inin kendisidir**; yazım kesilince katalog **ileriye doğru donar**.

**Rollup için de Go worker'ı YOK** — 0003/0008 MV'leri CH içinde çalışır. Yazım kesilince kaskad girdi almaz.

### 1.5 Bileşen → bugün nereden okuyor → VM karşılığı

> Kısaltmalar: **Seam** = `internal/api/metricsource.go` üzerinden (CH|VM seçilebilir) · **CH-çakılı** = seam dışı, doğrudan `s.store` · **HAT A** = span türevli, metrik değil.

| # | Bileşen / yüzey | Bugün nereden | Kanıt | VM'de karşılığı |
|---|---|---|---|---|
| 1 | `/api/metrics/names` (katalog + `MetricNamePicker`) | **Seam** | `api.go:855`, `api.go:4820` | ✅ `ListMetricNames` (`vmetrics/client.go:454`) — ama VM `description/unit/instrument` DÖNDÜRMEZ |
| 2 | `/api/metrics/query` (Explore, dashboard `metric` paneli, MQE) | **Seam** | `api.go:857`, `:4904` | ✅ `QueryMetric` + `QueryMetricNoted` |
| 3 | `/api/metrics/histogram` (heatmap, p50/p95/p99) | **Seam** | `api.go:866`, `:5030` | ✅ `vmetrics/histogram.go` — `histogram_quantile` + `_bucket` |
| 4 | `/api/metrics/promql` | **Seam** | `api.go:859` | ✅ verbatim proxy (MetricsQL üst kümesi) |
| 5 | `/api/metrics/labels`, `/api/metrics/attr-keys` | **Seam** | `api.go:872`, `:876` | ✅ aday alternation'lı keşif (`vmetrics/names.go`) |
| 6 | `/api/metrics` (HAM nokta listesi) | **CH-çakılı** | `api.go:856` → `:4877` `s.store.GetMetricPoints` | ❌ **YOK** — VM-only kurulumda BOŞ (proje memory'si "AÇIK BOŞLUK 2") |
| 7 | `/api/metrics/resolve` (Explore + ServiceCharts BİRİNCİL yolu) | **CH-çakılı** | `api.go:865` → `internal/api/metricresolve.go:57` `s.store.ResolveMetricQuery` | ❌ **YOK** — rollup-kademe sözleşmesi VM'de ifade edilemiyor |
| 8 | Servis Overview throughput + "Response time" | **Seam** (v0.9.1268) | `internal/api/service_metric_throughput.go:1-40`; FE `frontend/src/pages/service/Overview.tsx:522` (`rtMetric \|\| metric`) | ✅ (`MetricExists`/`MetricInstrument`/`QueryMetricRate`/`LatencyMetricName`) |
| 9 | MCP `query_metric`, `list_metric_names` | **Seam** (Deps.Metrics) | `internal/mcptools/tools.go:200-201`, `:297`, `:1071-1119` | ✅ — ama `metricSource()`, param GÖRMEZ (bilinçli istisna) |
| 10 | Diğer ~30 MCP tool'u (`guided_parity.go:613` pod envanteri dahil) | **CH-çakılı** | `internal/mcptools/guided_parity.go:613-626` | ❌ ham `metric_points` taraması |
| 11 | `/hosts` (host + pod envanteri) | **CH-çakılı** | `internal/api/hosts.go:16-17` → `chstore/hosts.go:97,163` | ❌ sabit-adlı okuyucu |
| 12 | Infra panelleri (`GetInfraMetrics`) | **CH-çakılı** | `chstore/infra_metrics.go:94` | ❌ |
| 13 | JVM heap / GC pod panelleri | **CH-çakılı** | `chstore/runtime_pods.go:50,106,157` | ❌ (alarm tarafı VM'e döndü — satır 21) |
| 14 | `/databases` receiver blend + DB kapasite/horizon/topsql | **CH-çakılı** | `chstore/dependencies.go:1035,1195,1555`; `db_capacity.go:75-280`; `db_horizon.go`, `db_topsql.go`; `oracle.go` (13 atıf), `postgres.go` (7), `mysql.go` (5), `redis.go` (4) | ❌ **en büyük tek blok** |
| 15 | Pod ↔ servis köprüsü | **CH-çakılı** | `chstore/podservice.go:50`; `internal/api/thanos_handlers.go:137` | ❌ |
| 16 | `ServiceInstances` (servis örnekleri) | **CH-çakılı** | `api.go:2693` → `chstore/service_instances.go:39` | ❌ |
| 17 | `/admin/cardinality` | **CH-çakılı** | `chstore/cardinality.go:74` | ❌ (VM'in kendi `/api/v1/status/tsdb`'si var — **doğrulanmalı**) |
| 18 | Metrik export aralığı probu (`clampStepToExport`) | **CH-çakılı** | `chstore/metric_export_interval.go:205,217` | ❌ VM'de probe YOK; yerine 300s pencere tabanı (`vmetrics` `promLookbehindFloorSec`) |
| 19 | DQL değerlendirici | **CH-çakılı, bilinçli** | `internal/dql/dql.go:475` (`FROM metric_points`) | ❌ — seam yorumu bunu "backend farklı cevaplayamaz" sınıfında sayıyor |
| 20 | Alarm evaluator — DB kapasite | **CH-çakılı** | `internal/evaluator/db_capacity.go:51` | ❌ |
| 21 | Alarm evaluator — JVM GC | **VM-ONLY** (v0.9.1213) | `internal/evaluator/runtime_vm.go:1-27`, `:109,113`; kapı `runtime_pods.go:162` | ✅ zaten VM'den |
| 22 | Anomaly — dış (Influx) seri | **CH-çakılı** | `internal/anomaly/external.go:6,98,132` | ❌ (§5) |
| 23 | Anomaly — mevsimsel baseline | **CH-çakılı** | `chstore/external_seasonal.go:68` | ❌ |
| 24 | Self-health (spool alarmı) | **CH-çakılı** | `internal/evaluator/selfhealth.go:6` | ❌ |
| 25 | Exemplar pivot (`/api/exemplars*`) | **CH-çakılı** | `internal/api/pivot.go:51-52` → `chstore/exemplar_otlp.go:121,141,209` | ❌ **VM exemplar saklamıyor — doğrulanmalı** |
| 26 | Influx ingest durumu | **CH-çakılı** | `chstore/influx_status.go:46` | ❌ |
| 27 | `/clusters` (pod/node/ns/deployment/rollout) | **Thanos** | `internal/thanos/client.go:627,632,662,…` | §3 |
| 28 | Servisler / operasyonlar / topoloji / trace / exception | **HAT A** (span MV'leri) | `/clickhouse-schema` §7 | Değişmez — CH'de kalır |
| 29 | Explore `?source=metrics` | FE, #6+#7'ye bağlı | `frontend/src/pages/explore/useExploreQueries.ts` | ❌ |
| 30 | Metrics sayfası kaynak rozeti | Zarfın `source` alanı | `frontend/src/pages/Metrics.tsx:271` | ✅ |
| 31 | Deneme modu `?metricsrc=vm\|ch` | Seam param'ı | `internal/api/metricsource.go:708`; FE `frontend/src/lib/metricSource.ts:32` | ✅ — kıyas için hazır seam |

**Sayı:** seam üzerinden **5 uç ailesi**; seam DIŞINDA `metric_points` okuyan **41 chstore dosyası** (`grep -l metric_points internal/chstore/*.go`, testler hariç). Bu, geçişin gerçek ağırlığı: "okuyan yerler VM'e" dilimi 5 uç değil, **~20 sabit-adlı okuyucu ailesi** demek.

---

## 2. VictoriaMetrics'e OTLP metrik ingest'i

### 2.1 Endpoint ve tip desteği — **hepsi doğrulanmalı**

Repo'da bugün **VM'e yazan hiçbir kod yok** (doğrulandı: `grep -rn "opentelemetry/v1/metrics"` → 0 eşleşme; `internal/vmetrics` yalnız okur, `client.go:1-40` başlığı "read backend" diyor). Aşağıdakiler dış bilgi:

| Konu | Beklenen | Not |
|---|---|---|
| Endpoint | `POST <vm>/opentelemetry/v1/metrics`, protobuf + gzip | vmsingle ve vmagent'ta; **sürüm eşiği doğrulanmalı** |
| Gauge / Sum | Doğrudan seri | Cumulative Sum → VM sayaç semantiği (`rate`/`increase` reset-korumalı) |
| Histogram (explicit) | `_bucket{le=…}` + `_sum` + `_count` | `vmetrics/histogram.go` zaten **bu şekli bekliyor** — yazım/okuma inşaen uyumlu |
| Exponential histogram | VM'in kendi `vmrange` kovalarına çevrilebilir | ⚠ `vmrange` **`le` DEĞİL**; `internal/vmetrics` proje memory'si: "`le` taşımıyor" hatası tam bu vaka. **Doğrulanmalı + test edilmeli** |
| Summary | quantile serilerine düzleştirme | **Doğrulanmalı** |
| **Exemplar** | **VM exemplar SAKLAMAZ (beklentim)** | **Doğrulanmalı.** Doğruysa satır #25 (`/api/exemplars`) VM'de **kaybolur** — geçişin en somut fonksiyon kaybı |
| Delta temporality | VM cumulative bekler; delta→cumulative dönüşümü collector'da (`cumulativetodelta`/tersinin) ya da VM tarafında | **Doğrulanmalı.** `metric_points.temporality` (`store.go` v0.6.56 kolonu) VM'de **karşılıksız** |

### 2.2 Resource attribute → label

VM'in OTLP alıcısı resource attribute'larını label'a düşürür ve isimleri sanitize eder (`.` → `_`). İki bayrak:

- `-opentelemetry.usePrometheusNaming` — birim eki (`_seconds`, `_bytes`, `_total`) ve Prometheus adlandırması. **Sürüm eşiği doğrulanmalı.**
- Bu bayrağın açık/kapalı olması, `internal/vmetrics/names.go`'nun **aday alternation** kuralıyla doğrudan etkileşir: proje memory'si ("AD EŞLEYİCİ") ek tablosunu `_seconds/_milliseconds/_bytes/_ratio/_total` + üç bileşik olarak sabitlemiş. **Karar: bayrağı AÇIK koşun** — aksi hâlde okuma tarafındaki adaylar üretilen adla buluşmaz ve prod'da yaşanan "her VM paneli boştu" tekrar eder.

Kritik üç attribute:

| OTLP | VM label (beklenen) | Bugün CH'de |
|---|---|---|
| `service.name` | `service_name` | `metric_points.service_name` kolonu |
| `k8s.pod.name` | `k8s_pod_name` | `res_values[indexOf(res_keys,'k8s.pod.name')]` |
| `deployment.environment(.name)` | `deployment_environment` **ya da** `deployment_environment_name` — **iki AYRI label** | tek `metricEnvExpr` coalesce'i (`filterexpr.go:402`) |

Son satır tek yönlü bir **kayıp**: `metricsource.go` `EnvFilterExpr` VM için zaten `false` dönüyor ve sonucu `envAmbiguous` diye ilan ediyor. Metrik tek depoya inince env daraltması **her metrik yüzeyinde** kaybolur. **Öneri:** collector'da bir `transform` ile tek yazımı (`deployment.environment.name`) kanonikleştirin; o zaman VM tarafında `EnvFilterExpr` **implement edilebilir** hâle gelir ve bu, geçişin bedava kazanımıdır.

### 2.3 Kardinalite — geçişin en büyük teknik riski

CH'de attribute'lar **satır içinde dizi**; VM'de **seri boyutu**. Bugün taşınanlar:

- `convert.go:283` — **tüm** resource attr'ları (k8s.* ailesi, `service.instance.id`, `host.*`, `container.*`, `os.*`, `process.*` …)
- `convert.go:316` — **tüm** datapoint attr'ları

`/clickhouse-schema` §6'daki ölçü ile: 1000 servis × ~15 endpoint × 10 channel × 30 function GENİŞ span ailesinde ~150k (kötü 500k) aktif seri üretiyor. Raw OTLP metriklerde çarpan **pod** (`k8s.pod.name`) ve `service.instance.id` — 0003 tasarım notunun `host_name`'i **bilinçli dışarıda bıraktığı** gerekçe tam bu (`migrations/0003_rollup_metrics.sql:17-18`: "pod başına seri patlaması"). VM'de bu koruma **yok**: her pod ayrı seri.

**Öneri — üç katmanlı label disiplini (collector'da, VM'e girmeden ÖNCE):**

1. **DROP:** `process.pid`, `process.runtime.description`, `container.id`, `k8s.pod.uid`, `service.instance.id` (fingerprint'in yarısı ama VM'de yalnız kardinalite) — `service.instance.id` düşerse `convert.go:265-280`'deki yazar-kimliği zinciri karşılıksız kalır, **kabul edilen bedel**, çünkü VM sayaç reset'ini kendisi çözüyor (`histogram.go` "ZAMAN ekseni VM'in işi").
2. **KEEP:** `service.name`, `k8s.namespace.name`, `k8s.deployment.name`, `k8s.pod.name`, `deployment.environment.name`, `k8s.cluster.name`.
3. **RELABEL:** `deployment.environment` → `deployment.environment.name` (§2.2).

Ayrıca vmagent/vmsingle tarafında `-maxLabelsPerTimeseries` ve stream aggregation ile **operatör tarafında ikinci bir kapı** kurulmalı — **doğrulanmalı**.

**Ölçüm kapısı (dilim 1'in çıktısı olmalı):** bugünkü `/admin/cardinality` (`chstore/cardinality.go:74`) raw `metric_points` üzerinden distinct sayıyor. Çift yazım döneminde aynı pencerede VM `/api/v1/status/tsdb` ile karşılaştırın; **oran 10×'i geçiyorsa label disiplini yetersizdir.**

### 2.4 Retention / downsampling eşlemesi

Bugünkü CH tarafı:

| Katman | Süre | Kanıt |
|---|---|---|
| Ham `metric_points` | **7 gün** (varsayılan) | `internal/config/config.go:410,447` (`MetricsDays: 7`); TTL `store.go:1915`, override `chstore/retention.go:104`, DROP PARTITION `retention_enforce.go:48` |
| `rollup_metrics_1m` | 14 gün | `migrations/0003_rollup_metrics.sql:41` |
| `rollup_metrics_5m` | 90 gün | 0003 (5m bloğu) |
| `rollup_metrics_1h` | **13 ay** | 0003 (1h bloğu) |
| `rollup_metrics_route_*` | aynı 14g/90g/13ay | `migrations/0008_rollup_metrics_route.sql` |
| `exemplars` | `retention.spans` (30g vars.) | `store.go:1958` |

VM karşılığı (**bayrak adları doğrulanmalı**):

```
-retentionPeriod=13months
-downsampling.period=7d:1m,30d:5m,90d:1h     # 13 ay ham tutmak kabul edilemez
```

⚠ **Downsampling VM Enterprise özelliğidir — doğrulanmalı.** Open-source vmsingle'da yoksa, 13 aylık ufuk ya (a) tek `-retentionPeriod` + kabul edilen disk, ya (b) vmagent **stream aggregation** ile 1m/5m/1h serilerini ayrı ad altına yazma (0003'ün kaskadının VM'deki karşılığı) olur. **Bu, geçişin kapatılmamış en büyük tasarım sorusudur** — dilim 2'ye girmeden cevaplanmalı.

⚠ **Retention semantik farkı:** CH'de retention **tablo başına**; VM'de **kurulum başına**. Bugün operatör `retention.metrics`'i Settings'ten ayrı ayarlıyor (`chstore/retention.go:60`). VM'e geçince bu ayar **anlamsızlaşır** — Settings'ten kaldırılmalı ya da "VM'de yönetiliyor" diye ilan edilmeli, yoksa çalışmayan bir vida bırakılmış olur.

---

## 3. Thanos cluster metrikleri — VM'e toplanabilir mi?

### 3.1 Bugünkü yol

`internal/thanos/client.go` **yalnız anlık sorgu** kullanıyor: `/api/v1/query` (29 çağrı) — ana oturum doğruladı: `query_range` 10 çağrı da var (trendler için); ajan raporundaki "yok" iddiası düzeltildi. Trendlerin çoğu PromQL'in kendi `[5m]` pencereleriyle çözülüyor.

Okunan seriler (`internal/thanos/promql.go`):
- cAdvisor: `container_cpu_usage_seconds_total` (`:72`), `container_memory_working_set_bytes` (`:80`)
- kube-state-metrics: `kube_pod_container_resource_limits` (`:89`), `..._requests` (`:100`), `kube_pod_container_status_restarts_total` (`:218`), `..._last_terminated_reason` (`:233`), `kube_node_info` (`:177`), `kube_node_role` (`:238`), `kube_node_status_capacity` (`:258`)
- node-exporter: `node_cpu_seconds_total` (`:149`), `node_memory_MemTotal_bytes` (`:158`), `node_memory_MemAvailable_bytes` (`:163`)

Ayrıca cluster kimliği: `ThanosLabelName/Value` enjeksiyonu (`thanos/client.go:49-56`) — TEK querier'ın arkasında N cluster varken seriyi ayıran external label.

### 3.2 Toplanabilirlik

| Yol | Değerlendirme |
|---|---|
| **vmagent → Prometheus `remote_write`** | ✅ **Tercih edilen.** OpenShift platform monitoring'inde `remoteWrite` zaten desteklenen bir konfigürasyon; her cluster'ın Prometheus'u doğrudan merkezi VM'e yazar. Cluster ayrımı `externalLabels` ile — bugünkü `ThanosLabelName/Value` ile **birebir aynı kavram** |
| **vmagent scrape** | ⚠ Merkezi VM'in her cluster'ın cAdvisor/KSM/node-exporter uçlarına ağ erişimi gerekir — banka topolojisinde genelde YOK |
| **Thanos Querier → federate** | ❌ Thanos Querier `/federate` **sunmaz** (Prometheus sunar) — **doğrulanmalı**. Sunsa bile federation instant snapshot'tır, kesintide delik açar |
| **Thanos Sidecar/Store → vmagent** | ⚠ mümkün ama Thanos'un object-store'unu ikinci kez okumak demek |

### 3.3 Toplanamıyorsa

Thanos **ayrı kaynak olarak kalır** ve bu kabul edilebilir: `/clusters` yüzeyi `metric_points`'e **hiç** bakmıyor (istisna: `internal/api/thanos_handlers.go:137` — pod adı köprüsü için `metric_points`'ten tek okuma; **bu satır dilim 2'de VM'e taşınmalı**, aksi hâlde pod↔servis eşleşmesi sessizce boşalır).

VM'e taşınırsa değişenler:
- `internal/thanos/client.go` **refactor EDİLMEZ** (promapi başlığının bilinçli kararı: `internal/promapi/promapi.go:20-27`). Yerine `vmetrics.Service` üstünde cluster label'lı sorgular.
- Cluster kimliği `ThanosLabelName/Value` → VM'de düz label matcher'ı; `ReconcileClusterSettings`'in teklik kuralı (`thanos/client.go:60-66`) korunur.
- **Kazanç:** tek okuma yolu, tek token, tek TLS ayarı; `/clusters` trendleri `query_range`'e geçebilir.
- **Kayıp:** cluster'a özel `NamespaceFilter` + per-cluster TLS-verify (iki-singleton kalıbı, `thanos/client.go:14-18`) tek endpoint modelinde yeniden ifade edilmeli.

---

## 4. Geçiş planı

### Aşama 0 — ölçüm (kod değişikliği yok)
`/admin/cardinality` + VM `/api/v1/status/tsdb` ile **aynı pencerede** seri sayısı; `metric_points` distinct `metric` sayısı; hangi metriklerin `bucket_counts` taşıdığı. Geri alma: yok (salt okuma).

### Aşama 1 — ÇİFT YAZIM (feature flag)
`Ingester.addMetric` (`internal/otlp/http.go:198`) noktasında **ikinci bir sink**: `ConvertMetrics`'in ürettiği `[]*chstore.MetricPoint` DEĞİL, **ham `ExportMetricsServiceRequest`** VM'e forward edilir.

> **Neden ham istek:** `convert.go` OTLP'yi CH şemasına **kayıplı** düzleştiriyor (`/otlp-converter` §1: `ScopeMetrics.scope` KAYIP, DP `flags` KAYIP, exp-histogram explicit'e materyalize). CH modelinden geri OTLP üretmek bu kayıpları **VM'e de taşırdı**. Forward, receiver'ın protobuf gövdesinden yapılmalı.

- Bayrak: `system_settings["victoria_metrics"].writeEnabled` (blob zaten var: `vmetrics.Settings`, `internal/vmetrics/client.go:64`).
- **Geri alma:** bayrağı kapat. CH yazımı hiç durmadı → sıfır veri kaybı.
- Risk: ingest yolunda ikinci ağ çağrısı. **Zorunlu:** kendi `consumer.NewSized` kuyruğu (`main.go:444` kalıbı), asla senkron değil — aksi hâlde yavaş VM `/v1/metrics` p99'unu taşır ve collector wedge'ler (CLAUDE.md "maxUnavailable: 0" olayı sınıfı).

### Aşama 2 — SORGU BAZINDA DOĞRULAMA
Mevcut iki araç:
- **Seam:** `?metricsrc=vm|ch` (`internal/api/metricsource.go:708`) — aynı sorguyu iki backend'e yollamanın **hazır** yolu. Cache anahtarı `src=<Name()>` taşıdığı için gövdeler karışmaz.
- **paritycheck:** `cmd/paritycheck/main.go:1-27` — MOD A (API ↔ ham CH), MOD B (`--prom-url` ile Prometheus). **MOD B doğrudan VM'e çevrilebilir** (VM Prometheus-uyumlu). Sapma sınıfları zaten tanımlı: BEKLENEN / KABUL (≤1e-9) / HATA.

**Eksik olan ve dilim 1'de yazılması gereken:** bir **kıyas ucu** — `GET /api/metrics/compare?metric=…&from=…&to=…&step=…` iki kaynağı aynı istekte çözüp nokta-nokta fark döndürür. Bugün böyle bir uç **yok** (doğrulandı).
Yer: **`internal/api/metrics_routes.go`** + `route_registry.go` `init()` kaydı (`internal/api/route_registry.go:29-38`) → **api.go'ya satır EKLENMEZ**.

**Kabul kriteri (Aşama 3'e geçiş kapısı):** her metrik ailesi için MOD A/B'de **0 HATA**; nokta sayısı ve kafes hizası (`ts % step == 0`) birebir; rate ve histogram-quantile ayrı raporlanır.

### Aşama 3 — CH METRİK YAZIMINI KAPAT
`writeEnabled` → tek yazıcı VM. `metricConsumer` (`main.go:444`) devre dışı; `exemplarConsumer` **ayrı karar** (§2.1: VM exemplar saklamıyorsa CH'de KALMALI, aksi hâlde `/api/exemplars` ölür).

**Ön koşul — okuyanlar taşınmış olmalı.** §1.5'in ❌ satırları (6,7,10-20,22-26) taşınmadan yazım kapatılırsa o yüzeyler **7 gün sonra sessizce boşalır** (retention). Bu, v0.9.1268'in dersinin ta kendisi: *"Deliberately scoped to CH" ve "pinned to the wrong store" aynı koddur; hangisi olduğuna etrafındaki deployment karar verir* (`internal/api/metricsource.go`, v0.9.1268 bloğu).

**Geri alma:** bayrağı geri aç. `metric_points` tablosu duruyor, MV'ler duruyor; kesinti penceresi kadar delik kalır, kalıcı kayıp yok.

### Aşama 4 — TABLOLARI TTL İLE BOŞALT
`retention.metrics` → 1 gün; `EnforceRetention` (`retention_enforce.go:48`) saatlik DROP PARTITION ile boşaltır. 0003/0008 kaskadları girdi almadığı için kendi TTL'lerinde erir (14g/90g/13ay).
**Geri alma:** bu aşama **geri alınamaz** (veri silinir). Bu yüzden Aşama 3 ile arasına **en az bir tam retention penceresi (7 gün) + bir 1h-rollup penceresi** konmalı.

### Aşama 5 — KODU KALDIR
Sıra: (1) `metricSource` interface'inden `chMetricSource` → (2) `chstore` metrik okuyucuları → (3) `ConvertMetrics`'in CH dalı → (4) `metric_points` + 4 MV DDL'i → (5) `migrations/0003`, `0008` için **ileri-migration** (drop).

### 4.1 Prod dış Distributed CH kısıtları

`store.go:3073` ve `:3105`: dış Distributed `metric_points` + `cluster_name` unset olduğunda `series_fingerprint` ve `is_monotonic` ALTER'ları **atlanıyor**. Yani prod'da bu iki kolon **olmayabilir**; exemplar pivot'u metric+service fallback'ine, `rate()` monoton-koruması KAPALI hâle düşüyor. **Geçiş açısından iyi haber:** VM'e giden yol bu kolonlara hiç dokunmuyor — forward ham OTLP'den. **Kötü haber:** çift yazım döneminde "CH yanlış, VM doğru" farkları bu kolonların yokluğundan gelebilir ve paritycheck bunu "HATA" diye raporlar. **Aşama 2'de `cluster_name` durumu rapora YAZILMALI.**

### 4.2 Boot DDL'in `metric_points`'i yaratmaya devam etmesi

`store.go:1883` `tables` diliminde, koşulsuz. Sonuç: kod kaldırılana dek **her boot boş tabloyu yeniden yaratır** — zararsız ama yanıltıcı ("tablo var → veri var" sanısı). `planDDL` eleyicisi (`/clickhouse-schema` §8, v0.9.1302) mevcut nesneyi eler, yani maliyet sıfıra yakın.

⚠ **Asıl tuzak `mvs` dilimi:** `/clickhouse-schema` §8 — "`mvs` dilimine eleyici **bilinçli uygulanmıyor**; ardından ada göre drop+recreate yükseltme mantığı koşuyor". Yani `metric_catalog` ve üç `spanmetrics_*_5m` MV'si **her boot'ta yeniden değerlendirilir**. Kaldırma sırasında MV'yi önce, tabloyu sonra düşürün; combined MV için `dropCombinedMV` + `max_table_size_to_drop=0` (CLAUDE.md pitfall).

⚠ **Ledger yok:** `/clickhouse-schema` §8 — "LEDGER YOK, DOWN-MIGRATION YOK". 0003/0008'in geri alınması `0003_..._rollback` ya da **yeni ileri-migration**. Yeni dosya `NNNN_drop_metric_pipeline.sql` yazılırken zorunlu başlık (gerekçe + şema doğrulama + "bu nasıl geri alınır") uygulanır.

---

## 5. Anomaly worker ve `external_metrics` (Influx) tasarımı

### 5.1 Bugünkü durum

| Bileşen | Kaynak | Kanıt |
|---|---|---|
| Influx poller | Flux → `BuildMetricsRequest` → **`otlp.ConvertMetrics`** → `InsertMetrics` | `internal/influx/poller.go:5-12` |
| Metrik adı | `ext:<sorgu adı>` | `poller.go` `MetricPrefix = "ext:"` |
| Kova semantiği | 1-dk kova, `_time` = kova BİTİŞİ, watermark ile bir kez | `poller.go:13-22` |
| Dış anomali | `metric_points`'ten `ext:*` gauge okur, mevcut MAD/dwell/kritik-z çekirdeğine verir | `internal/anomaly/external.go:6,98,132` |
| Pencere | gözlenmiş ilk↔son kova, ≤240 dk | `external.go:39-46` |
| Mevsimsel baseline | `retention`-kapılı | `chstore/external_seasonal.go:68`; `external.go:188` "Kapı: metric_points ufku" |

**Poller'ın OTLP dönüştürücüsünden geçmesi bilinçli** (`poller.go:8-12`): attribute yönlendirme + `series_fingerprint` tek kaynaktan, `metric_catalog` bedava. Bu tasarım **VM'e taşınırken korunabilir** — sadece sink değişir.

### 5.2 Operatör önerisi: Influx serilerini VM'e remote-write ile yaz

**Değerlendirme: EVET, ama poller kalmalı.**

| | Kazanç | Kayıp |
|---|---|---|
| **Tek yol** | `ext:*` serileri de VM'de → tek katalog, tek retention, tek Explore | — |
| **1-dk kova + watermark** | Korunur — poller aynı `SplitBuckets` mantığıyla VM'e yazar (`/opentelemetry/v1/metrics` ya da `/api/v1/import/prometheus`) | ⚠ VM **idempotent değil**: aynı kova iki kez yazılırsa iki örnek olur. CH'de de birikmiyordu ama okuma `avg` yapıyordu; VM'de `last_over_time` seçilirse sorun yok, `sum` seçilirse **çift sayım**. Watermark **daha da kritik** hâle gelir |
| **`kind=external` anomali** | Karar çekirdeği (MAD/dwell/z) **saf ve kaynak-bağımsız** — `external.go` yalnız `QueryMetric` çağırıyor (`:98` dar arayüz). Bu arayüz `metricSource`'a bağlanabilir | ⚠ Bugün `s.store.QueryMetric` (`:132`) — **seam DIŞI**. Tek satırlık değişiklik değil: anomaly paketi `api` paketini import edemez (mcptools'un aynı kısıtı, `mcptools/tools.go:196-199`). Çözüm: **`MetricSource` arayüzünü `anomaly` paketine de bildir**, `main.go` enjekte etsin |
| **Mevsimsel baseline** | — | ⚠ `external_seasonal.go` **retention-kapılı**: CH'de 7 günlük ufuk. VM'de 13 aylık ufuk varsa baseline **iyileşir**. Ama `ExternalSeasonal` ham SQL — VM'de `avg_over_time` + `offset` ile yeniden yazılmalı, **mekanik değil** |
| **Enrichment / exemplar** | — | ❌ `internal/influx/enrich.go` ikinci Flux sorgusuyla kanıt topluyor ve exemplar'a bindiriyor. VM exemplar saklamıyorsa (**doğrulanmalı**) bu **CH'de kalmalı** — `exemplars` tablosu §4 Aşama 3'te korunur |

**Öneri (net):**
1. Influx poller'ı **koru**, sink'ini `MetricSink` arayüzü üzerinden değiştirilebilir yap (`poller.go` `MetricSink` zaten dar bir arayüz — tek metot). VM sink'i yaz.
2. `anomaly.externalSource`'u `QueryMetric` arayüzü üzerinden **seam'e bağla** (`main.go`'da enjeksiyon).
3. **`external_metrics` ayrı bir CH tablosu DEĞİL** — bugün de değil, `metric_points` içinde `ext:` prefix'iyle yaşıyor. Yani "CH'de mi kalacak" sorusunun cevabı: **hayır, VM'e gider**, ama enrichment/exemplar CH'de kalır.
4. **Kaybolan:** `metric_catalog`'un `ext:*` serilerini otomatik kaydetmesi (`poller.go:11` "bedava kazanım") — VM'de karşılığı VM'in kendi `__name__` kataloğu, kabul edilebilir.

---

## 6. Settings'te VM'in "Remote Source" olarak tanımı

### 6.1 Bugünkü VM ayar yüzeyi (zaten var)

- Blob anahtarı **`victoria_metrics`**; Redis reload topic'i **`victoria-metrics`** (tire — **aynı DEĞİL**).
- Rotalar: `internal/api/vmetrics_routes.go:27-31` — GET/PUT/POST-test, **üçü de admin**.
- `Settings` struct: `internal/vmetrics/client.go:64-101` — `Enabled`, `BaseURL`, `AuthType(none|bearer)`, `Token`, **`TokenRef`** (`:78`, v0.10.273), `InsecureSkipVerify`, `RateWindowFloorS`, `AllowUnfilteredPercentiles`.
- `Snapshot`: `client.go:102-120` — token yerine **`HasToken`**; `TokenRef` görünür, `TokenResolved`/`TokenError` rozet.
- `secretref` sözleşmesi: `internal/secretref/secretref.go` — `env:NAME` | `file:/path`; çözüm `Configure`'da (`client.go:224-231`), sıcak yolda IO yok.
- İki predikat: `Configured()` (`client.go:332`, Enabled+URL) vs `Available()` (`client.go:357`, yalnız URL).

### 6.2 Thanos kalıbına geçiş — gerekli mi?

Thanos kalıbı = **LİSTE** (`thanos.ClusterConfig`: `ID` opak `"c-"+8hex`, `Name`, `URL`, `ThanosLabelName/Value`, `SpanClusterValues`, per-cluster TLS — `internal/thanos/client.go:41-70`).

**Değerlendirme: HAYIR — VM tek endpoint kalmalı.** Gerekçe kodda yazılı: `vmetrics/client.go:61-63` — *"One endpoint per install: vmselect already fans out across a cluster, so federation is VM's job, not ours."*

**Ancak** VM **yazma** hedefi olunca iki alan eklenmeli:

| Alan | Neden |
|---|---|
| `WriteURL` (boşsa `BaseURL`) | vmselect (okuma) ile vminsert (yazma) prod'da **ayrı adreslerdir**; tek URL varsayımı cluster kurulumda kırılır |
| `WriteEnabled bool` | Aşama 1/3'ün feature flag'i. **Varsayılan `false`** = korunan durum (`AllowUnfilteredPercentiles`'ın aynı gerekçesi, `client.go:95-101`: "fresh install, missing blob, partially-written blob → hepsi güvenli tarafa") |

Token/secret: **değişiklik gerekmez.** `TokenRef` sözleşmesi (v0.10.271-273) zaten yerinde ve yazma yolu **aynı `Settings`'i** kullanacak. Yalnızca `Test()` probu (`client.go:635`) yazma URL'ini de denemeli — bugün yalnız okuma ucunu deniyor.

**Audit:** PUT zaten `s.audit(...)` yazıyor (`internal/api/vmetrics_handlers.go`). `writeEnabled` değişimi **ayrı audit satırı** hak eder: bu, kurulumdaki metrik yazım yönünü değiştiren tek vida.

---

## 7. Dilimleme, risk, dosya planı, test

### 7.1 Dilim 1 (S/M) — VM Remote Source + çift yazım + kıyas ucu

| Dosya | Değişiklik | Boy |
|---|---|---|
| `internal/vmetrics/client.go` | `Settings`+`Snapshot`'a `WriteURL`, `WriteEnabled`; `Test()` yazma probu | S |
| `internal/vmetrics/write.go` **(YENİ)** | `WriteOTLP(ctx, body []byte) error` — protobuf+gzip POST `/opentelemetry/v1/metrics`; `promapi` **kullanılmaz** (o okuma/JSON zarfı için) | M |
| `internal/otlp/http.go`, `grpc.go` | `Ingester`a `MetricForward` alanı; `handleMetrics`/`Export` ham gövdeyi forward kuyruğuna verir | S |
| `main.go` | forward consumer (`consumer.NewSized("vm-metrics", …)`) + `ing.SetMetricForward(...)` | S |
| `internal/api/metrics_routes.go` **(YENİ)** | `registerMetricsRoutes(mux)` + `init(){ registerRoutesExtra("metrics", (*Server).registerMetricsRoutes) }` → **api.go'ya satır EKLENMEZ** | M |
| `internal/api/metrics_compare.go` **(YENİ)** | `GET /api/metrics/compare` — iki kaynak, nokta-nokta fark, sapma sınıfı | M |
| `frontend/src/pages/settings/…` | Write URL + write toggle alanları (`Field.tsx` atomu) | S |
| `frontend/src/lib/types.ts` + `lib/api.ts` + `lib/queries/` | 4 dokunuş (`/api-route` §7) | S |

### 7.2 Dilim 2 (L) — okuyanlar VM'e, CH yazımı kapanır

Alt-dilimler (her biri kendi release'i):
- **2a:** `/api/metrics` (ham nokta) + `/api/metrics/resolve` seam'e (§1.5 #6, #7). ⚠ `resolve` en zor: rollup-kademe sözleşmesinin VM karşılığı yok.
- **2b:** hosts / infra / JVM pod panelleri (#11-13)
- **2c:** `/databases` ailesi (#14) — **en büyük tek blok**, ~40 SQL
- **2d:** pod↔servis + ServiceInstances + thanos_handlers.go:137 (#15, #16)
- **2e:** anomaly external + seasonal + Influx sink (#22, #23, §5)
- **2f:** DQL (#19), cardinality (#17), export-interval (#18)
- **2g:** `writeEnabled` tek yön; `retention.metrics` → 1 gün

### 7.3 Dilim 3 (M) — eski tablo/kod kaldırma

`metric_points` + 4 MV DDL'i, 0003/0008 için drop-migration, `chstore` metrik okuyucularının silinmesi, `chMetricSource`'un kaldırılması, `metricSource` seam'inin **tek implementasyona** inmesi (seam'i tamamen kaldırmayın — `?metricsrc` kaçış kapısı ve compile-zamanı imza kapısı değerini korur).

### 7.4 Risk kaydı

| # | Risk | Şiddet | Azaltma |
|---|---|---|---|
| R1 | **Kardinalite patlaması** (pod × instance × tüm attr'lar) | 🔴 | §2.3 üç katmanlı label disiplini; Aşama 0 ölçümü kapı |
| R2 | **Exemplar kaybı** (VM saklamıyorsa) | 🔴 | `exemplars` tablosunu CH'de BIRAK; pivot ucu CH'de kalır |
| R3 | **13 aylık rollup ufkunun VM karşılığı yok** (downsampling Enterprise?) | 🔴 | Dilim 2 öncesi cevaplanmalı; alternatif stream aggregation |
| R4 | Env daraltması VM'de ifade edilemiyor | 🟡 | Collector'da tek yazımı kanonikleştir → `EnvFilterExpr` implement edilebilir |
| R5 | Ingest yolunda ikinci ağ çağrısı p99'u taşır | 🟡 | Ayrı kuyruk + byte budget; asla senkron |
| R6 | Delta temporality karşılıksız | 🟡 | Collector'da cumulative'e sabitle; paritycheck'te ayrı sınıf |
| R7 | 7 gün sonra sessiz boşalma (okuyan taşınmadan yazım kapanırsa) | 🔴 | Dilim 2 tamamlanmadan 2g koşulmaz; `metric_exists` kapılarının **veriyle** probe etmesi (`/clickhouse-schema` §9: "kolon VAR ≠ kolon DOLU") |
| R8 | Sıcak cache eski gövdeyi servis eder | 🟡 | `metricNameRuleTag` basamağını artır (`metricsource.go:148`); yeni `wr=` damgası |
| R9 | Boot DDL'i boş tabloyu yeniden yaratır | 🟢 | Dilim 3'e kadar kabul; `mvs` diliminde eleyici yok — drop sırası MV→tablo |
| R10 | Exp-histogram `vmrange` vs `le` uyuşmazlığı | 🟡 | Golden test (aşağı) + `histogram.go`'nun mevcut "le taşımıyor" hatası teşhisi veriyor |

### 7.5 Test planı

**`go test -race` geçmeli. Yeni test dosyaları:**

1. **`internal/vmetrics/write_test.go`** — `httptest.NewServer` ile **tablo testi**:
   | Vaka | Beklenen |
   |---|---|
   | 200 | nil hata |
   | 204 | nil hata (VM'in normal cevabı — **doğrulanmalı**) |
   | 400 + gövde | hata metni **verbatim** korunur (`ErrUnsupported` sınıfı değil) |
   | 401 | `errUpstream` sınıfı, mesajda "401" |
   | 429 / 503 | retry-able işaretlenir |
   | bağlantı reddi | `errUpstream` |
   | gzip başlığı | `Content-Encoding: gzip` + `Content-Type: application/x-protobuf` gönderiliyor |
   | TokenRef çözülmüş | `Authorization: Bearer <çözülmüş>` |
   | ctx iptali | çağrı iptal olur, goroutine sızmaz |

2. **`internal/otlp/forward_test.go`** — forward'ın **ham gövdeyi** ilettiği (CH modelinden geri üretilmediği): aynı payload'ın byte-özdeşliği. `/otlp-converter` §2.6'nın golden-test yükümlülüğü buraya da uzanır.

3. **`internal/api/metrics_compare_test.go`** — saf karşılaştırma çekirdeği (nokta sayısı, kafes hizası, mutlak/oransal fark, null konumu) **Server'sız** tablo testi; sapma sınıflandırması `cmd/paritycheck`'in üç sınıfıyla **aynı eşiklerde** (1e-9).

4. **`internal/api/route_registry_test.go`** — mevcut `TestMuxRoutePatterns` yeni rotayı otomatik kapsar; ek olarak `registerMetricsRoutes`'ın **defterde** olduğu (api.go'da değil) taranır.

5. **Mevcut kapıların bakımı:** `internal/api/metricsource_test.go:59-98` `metricStoreMethods` listesi — dilim 2'de seam'e giren her yeni metot **bu listeye eklenmeli**, yoksa kapı yeni yüzeyi görmez. ⚠ "bayat öncül" sınıfı: `TestUntranslatableQueryIs400Not502`'nin reddedilen örneği desteklenir hâle gelirse test sessizce anlamsızlaşır.

6. **Doğrulama scripti:** `cmd/paritycheck`'in MOD B'si `--prom-url` ile VM'e yönlendirilir; `-report docs/charts/parity-vm-report.md`. CI'da MOD A korunur (CH hâlâ yazarken), dilim 2g'de **MOD B'ye çevrilir**.

---

## Açık sorular (operatöre)

1. **Downsampling:** VM Enterprise mi, open-source mu? 13 aylık `rollup_metrics_1h` ufkunun VM karşılığı bu cevaba bağlı. **Dilim 2 öncesi.**
2. **Exemplar:** VM'de saklanmıyorsa `/api/exemplars` + `exemplars` tablosu CH'de kalsın mı (önerim: EVET), yoksa özellik emekliye mi ayrılsın?
3. **`spanmetrics_calls/hist/duration_5m`** — `/clickhouse-schema` §11.2'nin açık sorusu: okuyucusuz, kalıcı 0 satır. **Bu geçiş onları silmek için doğal fırsat.**
4. **`/api/metrics/resolve`:** rollup-kademe sözleşmesi VM'de ifade edilemiyor. VM-only'de bu uç ne yapsın — 400 mü (dürüst), yoksa düz `query_range`'e mi düşsün (kademe garantisi olmadan)?
5. **`retention.metrics` Settings vidası** VM'e geçince anlamsızlaşıyor. Kaldırılsın mı, "VM'de yönetiliyor" diye mi ilan edilsin?
6. Dilim 1 (VM Remote Source alanları + ham OTLP çift yazım + kıyas ucu) onaylanıyor mu? Kod bu onaydan sonra başlar.

---

## Durum (2026-09-03)

Dilim 1 GEMİDE: 1a `vmetrics/write.go` WriteOTLP + `writeUrl`/`writeEnabled` (varsayılan kapalı) + Test yazma probu + form (v0.10.292) · 1b `otlp/forward.go` ham ileti + `vm-metrics` consumer + /admin/stats sayaçları (293) · 1c `/api/metrics/compare` `metrics_routes.go` defter kaydı (294). Canlı doğrulama operatörde (lokalde VM yok; prod'da kurulu): Settings → Metrics backend → Yazma URL'i + "VM'e de yaz", Test, sonra `curl /api/metrics/compare?name=…`. Dilim 2 öncesi "Açık sorular" 1-6 cevaplanmalı.
