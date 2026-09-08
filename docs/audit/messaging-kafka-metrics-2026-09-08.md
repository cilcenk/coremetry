# Messaging sayfası — Kafka client metrikleri entegrasyonu (AUDIT)

Tarih: 2026-09-08 · Durum: **audit, kod yok, onay bekliyor** · Kapsam: `/messaging`
hibrit (a) client-side sağlık → VictoriaMetrics, (b) topic/operation RED + trace
pivotu → ClickHouse span'leri.

Doğrulama tabanı: repo `v0.10.548`; OTel Java instrumentation `main` (2026-09-08,
kafka-clients kütüphane README + `KafkaMetricsInstrumentationModule`); VM belge
sayfası (OTLP ingest). **Prod VM'e erişim yok** — §5'teki ad/label listesi
kütüphane kaynağından, prod'daki gerçek küme **doğrulanmadı** (§5.4 komutları
operatörde). "doğrulanmadı" etiketi her belirsiz iddiada duruyor.

---

## 0. Bir bakışta

| Soru | Cevap |
|---|---|
| Messaging bugün veriyi nereden alıyor | Yalnız ClickHouse: `messaging_summary_5m` + `messaging_caller_summary_5m` MV çifti; E2E ve Top-ops **ham `spans`/`span_links`** |
| Sayfada grafik var mı | **Yok** — v0.9.834'te operatör kararıyla KPI şeridi + 3 grafik kaldırıldı (`Messaging.tsx:178-181`) |
| VM'e sorgu katmanı var mı | Var ve olgun: `internal/vmetrics` (10.4k satır) + `internal/promapi`; seam `metricSourceFor(r)`; `query_range`, `label/<x>/values`, `labels` var; **`series` ve üretim instant query yok** |
| Endpoints `?src=metric` deseni taşınır mı | Kısmen: liste satırı **topic**, Kafka client metrikleri **servis/instance/client_id** tanecikli → tabloyu bütünüyle VM'e çevirmek anlamsız; hibrit katman doğru |
| Span tarafı messaging.* | Yalnız `messaging.system` tipli kolon (`msg_system`); destination MV'de coalesce; **`messaging.operation*` ve `messaging.kafka.consumer.group` hiçbir yerde okunmuyor**; OTLP golden test yok |
| Entity köprüsü | `host_name` iki tarafta da OTel `host.name` (span: `convert.go:69`; MV: `store.go:3917`; VM: `hosts_metric.go:131`) → pod düzeyi join anahtarı HAZIR; `service_name` label'ı `promLabel("service.name")`; `client_id` **yeni kavram** |
| En büyük doğruluk riski | `client_id` ≠ consumer group; `records_lag_max` = **o instance'ın** gördüğü partition max lag'i; broker lag'i yok |
| Öneri | **Alternatif B** — liste CH'de kalır, topic detayına "Kafka istemcileri (metrik)" bölümü + servis sayfasına "Kafka client" paneli; VM yoksa/serisi yoksa bölüm gizlenir, not ilan edilir |

---

## 1. Mevcut durum haritası

### 1.1 Frontend

| Parça | Yer | Not |
|---|---|---|
| Rota | `frontend/src/App.tsx:49` (lazy), `:174` `/messaging` | **Alt rota yok** — `/database`, `/endpoint` emsalleri var (`App.tsx:163-168`) |
| Sidebar / palette | `components/Sidebar.tsx:102`, `CommandPalette.tsx:80` | i18n `lib/i18n.ts:47,182` |
| Sayfa | `frontend/src/pages/Messaging.tsx` (232 satır) | overview fetch → `DepRow` → tavan şeridi → tablo |
| Codec | `pages/messaging/destinationParam.ts` (46) + test | `?destination=enc(system)\|enc(cluster)\|enc(dest)` |
| Tablo (paylaşımlı) | `components/DependenciesTable.tsx` (951) `kind='queue'\|'db'` | queue kolonları `:235-259` (Produce/min, Consume/min, P99 Δ, Üretim P95, İşleme P95); `useDataTable` storageKey `deps-queue` (`:289-290`) |
| Çekmece (paylaşımlı) | `features/dependencies/DetailDrawer.tsx` (701) | queue dalları `:159-165`, `:180-192`; tek `CorePanelMulti` = E2E gecikme (`:294-315`, `storageKey="msg-drawer-e2e"`) |
| Trace pivotu | `lib/pivotHref.ts:181` `messagingTracesHref` | `messaging.system =` + OR{`messaging.destination.name`, `messaging.destination`, `peer.service`}; `rootOnly:false` bilinçli (`:222-224`); kullanım `DependenciesTable.tsx:351`, `DetailDrawer.tsx:221,228,485` |
| State | `Messaging.tsx:37` `useUrlRange('1h')`; `?compare=prior` `:44-50`; `?destination=` `:67-97` (`replace:true`) | React Query `['messaging', from, to, compare]` `:56`, `staleTime 30 s`, **polling yok** |
| Durumlar | `TableSkeleton` `:182`, `Empty` hata `:183-188`, boş `:189-195`, tavan rozeti `:201-210` | Drawer `Spinner` `DetailDrawer.tsx:133` |
| API | `lib/api.ts:1410` `messaging`, `:1399` `msgTrends`, `:1451` `messagingDetail` | Tipler `lib/types.ts:692-748` (`MessagingInstance`, `MessagingOverview`), `:442-470` (`MessagingDetail`) |

**Endpoints/Databases ile ortak olan:** `DependenciesTable`, `DetailDrawer`, `depRowKey`
(`lib/depsTable.ts:71-77`), `trendsEnabled` (`:18-26`), `Sparkline`, `TrendDelta`,
`SavedViewsBar page="messaging"` (`Messaging.tsx:177`).

**Ortak OLMAYAN (Messaging'de yok):** detay tam sayfası (`pages/endpoints/`,
`pages/databases/detailSections.tsx` karşılığı yok), `MetricTile`
(`pages/endpoints/MetricTile.tsx:21-69`), `?src` anahtarı, `envApplies`
(`Messaging.tsx:159` vermiyor; `Databases.tsx:245`, `Endpoints.tsx:515` veriyor),
`useTablePrefs`.

**`?src=metric` deseni (Endpoints, v0.10.336/361/454) — taşınabilir parçalar:**
okuma `Endpoints.tsx:269-273`, yazma `:274-279` (`replace:true`), seçici `:566-573`,
istek dallanması `lib/api.ts:2672-2681` (`/api/endpoints/metric`), dürüstlük notu
`Endpoints.tsx:511, :973-979`, varsayılan-span sözleşme testi
`Endpoints.srcDefault.test.ts:11-22`.

**Gözlenen küçük borçlar (bu işle birlikte kapatılabilir):**
- `Messaging.tsx:38` ve `:44` iki ayrı `useSearchParams()` örneği.
- `MessagingDetail.series` (`types.ts:467`) hiç okunmuyor — ödenen, okunmayan alan.
- Trend (`DependenciesTable.tsx:365-402`) ve çekmece (`DetailDrawer.tsx:86-97`)
  fetch'leri React Query dışında, AbortSignal yok.
- Çekmecede **pod hücresi düz metin** (`DetailDrawer.tsx:649-651`), servis hücresi
  linkli (`:645-648`) — `podDetailPath` (`pages/service/podDetailPath.ts:13-33`) ile
  sarılmamış.

### 1.2 Backend

| Parça | Yer | Not |
|---|---|---|
| Rotalar | **`internal/api/api.go:769-772`** satır-içi (`GET /api/messaging`, `/trends`, `/detail`) | Ayrı `registerXxxRoutes` yok; handler gövdeleri `internal/api/api_databases.go:219,109,305` |
| Cache | `messaging:v2:<bucket>` / `:cmp:` 30 s (`api_databases.go:232-235`); `msg-trends:` (`:111`); `msg-detail:<sys>:<cluster>:<dest>:<assumed>:<bucket>` (`:329`) | Warm loop `api.go:1583` |
| Auth | Route düzeyinde yok, global middleware (`api.go:1407`); audit yok (salt-okunur) | |
| MV (topic) | `internal/chstore/store.go:3865-3892` `messaging_summary_5m` ORDER BY `(msg_system, cluster, destination, time_bucket)` | **kind yok** (bilinçli, `:3894`); `cluster` coalesce `server.address → messaging.kafka.bootstrap.servers → messaging.kafka.cluster.name → '(default)'`; `destination` coalesce `messaging.destination.name → messaging.destination → peer_service → 'unknown'` |
| MV (caller) | `store.go:3898-3928` `messaging_caller_summary_5m` ORDER BY `(msg_system, cluster, destination, service_name, host_name, kind, time_bucket)` | **`host_name` = OTel `host.name`** (`:3917`) — VM `host_name` label'ıyla aynı kaynak |
| Okumalar | `chstore/dependencies.go:1384` (`LIMIT 200`, mxt 15), `:1462-1477` (mxt 8), `:509-699` detay; `db_trends.go:275`; `messaging_e2e.go:203` (ham `span_links ⋈ spans`) | Top-ops **ham `spans`** (`dependencies.go:688-699`) |
| Rollup aileleri | `migrations/0001/0002/0003/0008` | **Hiçbirinde destination boyutu yok**; GENİŞ ailede `endpoint = if(http_route!='', http_route, name)` (`0002:62`) → messaging span'de **span adı**, topic değil |
| Giriş-span | `service_summary_5m` kind filtresiz (`store.go:3374-3390`) | consumer span'ler servis RED'ine giriyor; ilkeyi uygulayan yerler `slo.go:115`, `entity_pod_latency.go:38,101`, `endpoints.go:774-780` (`kind NOT IN ('client','producer')`) |
| FilterExpr | `chstore/filterexpr.go:79` `messaging.system → msg_system`; destination `wellKnown`'da yok → `attr_kvh` bloom / dizi yolu (`:265-285`) | |
| Kimlik-önce arama | `chstore/promoted_attr.go:85-122` — **messaging anahtarı yok**; operatör trace facet olarak ekleyebilir (`trace_facets.go:173-179`) | |

### 1.3 OTLP ingest — messaging.* ne korunuyor

`internal/otlp/convert.go:120` `convertSpan`:
- `messaging.system` → `chstore.Span.MsgSystem` (`convert.go:170,205`) → `spans.msg_system LowCardinality` (`store.go:1043`). **Tek tipli messaging kolonu**; skip index yok (`store.go:1051-1052` yalnız `idx_trace`, `idx_name`).
- `messaging.destination.name` / `messaging.destination` → yalnız `attr_keys/attr_values` dizisi; MV insert anında `indexOf` (`store.go:3879-3884`). Kolon yok.
- `messaging.operation`, `.operation.type`, `.operation.name` → **hiçbir yerde okunmuyor** (yalnız `acache.go:155` allowlist ve `ColumnManager.tsx:41` öneri).
- `messaging.kafka.consumer.group`, `messaging.consumer.group.name`, `.partition`, `.message.offset`, `.message.key` → **repoda sıfır referans** (dizide duruyor).
- `spanAttrAliases` (`otlp/semconv.go:47-56`) messaging taşımıyor; **`internal/otlp/` içinde messaging golden testi yok**, `testdata/` yok.
- Demo üreteci ESKİ semconv yazıyor: `messaging.destination`, `messaging.operation` (`cmd/demo/bank_extra.go:133`) — lokal veri yeni yazımı ölçmez ([[feedback-local-data-is-a-fixture]]). Demo **Kafka client metriği basmıyor** (yalnız `messaging.kafka.consumer.lag` gauge, `cmd/demo/main.go:1817`) → lokal fixture yok.

### 1.4 VictoriaMetrics sorgu katmanı

| Parça | Yer | Not |
|---|---|---|
| Paket | `internal/vmetrics/` (client 674, promql 1368, throughput 534, histogram 396, names 582, capacity 231, runtime_pods 266) | HTTP çekirdeği `internal/promapi/promapi.go` (15 s timeout `:173`, 8 MB gövde `:79`, **1000 seri parse tavanı** `:74`) |
| İmzalar | `QueryMetric(ctx, chstore.MetricQueryFilter)` `client.go:511`; `QueryMetricNoted` `:537`; `MetricLabelValues(ctx, metric, key, since)` `:591` (**`match[]` ile metrik kapsamlı**, 200 tavan); `MetricAttrKeys` `:631`; `ListMetricNames(ctx, service, pattern, limit, offset)` `:473`; `QueryPromQLRange(ctx, query, from, to, step, mdp)` `histogram.go:342`; `QueryMetricRate/CountRate` `throughput.go:178,214` | `MetricQueryFilter` `chstore/metricquery.go:12-34`: Name, Service, Filters, GroupBy, Aggregation, From/To, StepSeconds, MaxDataPoints, RateWindowSec |
| API'ler | `query_range` ✅; `label/<x>/values` ✅ (`client.go:487,609`); `labels` ✅ (`throughput.go:487`); `query` (instant) yalnız Test probe'u (`client.go:660`); **`series` ❌**; `metadata` bilinçli ❌ (`client.go:463`) | "son değer" her yerde `query_range` + son kova (`capacity.go:68`) |
| Thanos ile ortaklık | `promapi` **yalnız vmetrics** kullanıyor; `internal/thanos/client.go` bilinçli ayrı (`promapi.go:20-26`) — 7 kopya parça (envelope/series/15 s/8 MB/1000/firstN/insecure client) | thanos `firstN` (`client.go:599`) rune-güvenli değil (yan bulgu) |
| Konfig | `system_settings` anahtarı `victoria_metrics` (`chstore/vmetrics.go:12`); `LoadPersisted` `client.go:164`, `SavePersisted` `:211`, 30 s refresh `main.go:1141-1145`; rotalar `vmetrics_routes.go:28-30` (admin); FE `pages/settings/MetricsBackendTab.tsx`, `vmForm.ts` | Auth `none\|bearer`, basic **bilinçli yok** (`client.go:70`); tokenRef `env:/file:` (`:78`); tenant başlığı yok |
| Seam | `internal/api/metricsource.go:152-292` (18 metot), `metricSource()` `:643`, `metricSourceFor(r)` `:753`, `?metricsrc=vm\|ch` deneme `:686-742`; **VM→CH fallback yok** (`:62-67`) | Seam üstünden: Explore (`api.go:4857-5172`), `/api/metrics/promql` (`promql.go:68`), Overview RED (`service_metric_red.go:282`), Endpoints (`endpoints_metric.go:717`), hosts/infra (`hosts.go:26,47`, `infra_metric.go:32`), dashboards (`dashboards_data.go:175`), MCP (`mcp_deps.go:35`) |
| Label eşlemesi | `promLabel` `vmetrics/promql.go:341-361` (nokta→alt çizgi, `resource.`/`span.` öneki düşer); `serviceLabel()` = `service_name` (`:365`) | Servis kimlik adayları `chstore/job_service.go:63-69` → VM'de `k8s_deployment_name → k8s_container_name → job → service → name → service_name` (`throughput.go:288-303`) |
| Env / cluster | Env **VM'de ifade edilemez, bilinçli ret** `metricsource.go:617-623` → `envAmbiguous` (`service_metric_red.go:159-162,250`; `endpoints_metric.go:40,174`); cluster yalnız Thanos'ta matcher enjeksiyonu (`thanos/cluster_matcher.go`) | UI şeridi `pages/service/Overview.tsx:941-945` |
| Cache | Her anahtarda `src=<backend>` (`api.go:5003`, `service_metric_red.go:58`, `endpoints_metric.go:116`); VM'e özel `metricNameRuleTag()` `metricsource.go:147-151` | TTL 30-60 s |
| Ham PromQL | `GET /api/metrics/promql` (`api.go:841` → `promql.go:50`): 8192 uzunluk, mdp ≤ 4000, **allowlist yok**, VM'e olduğu gibi gider, viewer+ | Bütçe operatörün vmselect bayraklarında (`vmetrics/promql.go:486`) |

### 1.5 Entity eşleme

| VM label | Coremetry karşılığı | Köprü |
|---|---|---|
| `service_name` | `spans.service_name`; entity `svc:<ad>` (`entity/identity.go:21,55`; yalnız span-türevli `entity/spanpass.go:106-108`) | `promLabel("service.name")` `vmetrics/promql.go:365`; aday sırası `job_service.go:63-69` |
| `host_name` | pod adı — `k8s_pod = coalesce(k8s.pod.name, host_name)` (`migrations/0011_entity_layer.sql` ADIM 1); `PodServiceMap` (`chstore/podservice.go:34-48`, "canlıda %98 birebir"); `RuntimePodGroupBy` (`vmetrics/runtime_pods.go:31-52`) | hosts VM kolu `host.name` (`hosts_metric.go:131-132,256`) |
| `client_id` | **bulunamadı** — repodaki tüm `client_id` OAuth/SSO (`auth/oidc.go:40`, `SsoTab.tsx`) | yeni kavram |
| consumer group | ingest/UI'da yok; yalnız metrik şablonu `lib/metricTemplates.ts:179` | — |
| env | VM'de yok (§1.4) | — |
| cluster | Thanos'ta var, VM'de yok; Kafka `cluster` = bootstrap/cluster.name (MV) — **k8s cluster'ı DEĞİL** (`chstore/identity.go:315-331`, Ç9) | — |

Pivot sözleşmesi: `podDetailPath` (`pages/service/podDetailPath.ts:13-33`), `entityHref`
(`lib/entityHref.ts:27-42`), workload `/entity?id=wl:…` (`?workload=` diye param yok);
sayfa bağlamı `lib/pageContext.ts:142-151`. Tek-varlık skor şeridi emsalleri:
`REDStrip` (`EndpointDetail.tsx:304-308`), `PodKpiStrip` (`pages/pod/PodKpiStrip.tsx:25-45`),
`EntityDetail` Stat dizisi (`EntityDetail.tsx:204-210`). Kural: detay sayfasında tek-varlık
şeridi onaylı, **liste sayfasında sayfa-üstü KPI bloğu yasak**
(`docs/audit/database-entity-detail-2026-08-24.md:640`; v0.9.834 kararı da aynı yönde).

---

## 2. Metrik keşfi (§5)

### 2.1 Kaynak: OTel Java agent `kafka-clients-metrics` modülü

- Modül adı `kafka-clients-metrics` (`KafkaMetricsInstrumentationModule.java:20-25`; ek adlar
  `kafka-clients`, `kafka-clients-metrics-0.11`, `kafka`). Kapatma: standart
  `otel.instrumentation.kafka-clients-metrics.enabled=false`. Bankada hangi sürüm ve
  bayrak — **doğrulanmadı**. (`otel.instrumentation.kafka.metric-reporter.enabled` adlı
  bir özellik güncel kaynakta görülmedi — doğrulanmadı.)
- Mekanizma: Kafka'nın kendi JMX metrik raportörü OTel'e köprülenir; OTel adı
  `kafka.<consumer|producer>.<jmx-adı, tire→alt çizgi>`. Prometheus/VM tarafında nokta →
  alt çizgi: `kafka_consumer_connection_count` (operatörün Grafana'da gördüğü ad ile
  tutarlı). `_total` sonekli olanlar zaten JMX'ten `-total` sayaçları (VM
  `usePrometheusNaming` eklemiyor olsa da ad aynı).
- Kütüphane README'si **128 metrik** listeliyor (2026-09-08 `main`). Enstrüman tipleri:
  `_rate`/`_avg`/`_max`/`_count`/`_ratio` → **GAUGE** (Kafka'nın kendi pencere ortalaması;
  **`rate()` UYGULANMAZ**), `_total` → **COUNTER** (`rate()`/`increase()` doğru).
- Öznitelikler (VM'de label): `client_id` (her metrikte), `node_id` (broker bağlantı/istek
  metrikleri), `topic` (topic düzeyi), `partition` (yalnız lag/lead). Resource'tan
  `service_name`, `host_name` (VM varsayılan: tüm resource attr'ları label olur,
  `-opentelemetry.promoteAllResourceAttributes`; bankadaki yol collector remote-write mi
  VM OTLP mi — **doğrulanmadı**, operatörün label seti ikisiyle de uyumlu).
- README notu: Kafka aynı metriği birden çok tanecikte raporlar; köprü çift-sayımı
  önlemek için **yalnız en ince tanecikli** öznitelik setini kaydeder → `topic`'li
  metrikleri `sum by (service_name)` ile toplamak güvenli, `topic`'siz ve `topic`'li
  aynı ad yan yana gelmez.

### 2.2 Bu iş için anlamlı alt küme (README'den, adlar birebir)

| Soru | OTel adı → VM adı | Tip | Label | UI adı önerisi |
|---|---|---|---|---|
| Bağlantı sayısı | `kafka.consumer.connection_count` / `kafka.producer.connection_count` | gauge | client_id | "Açık bağlantı" |
| Bağlantı çalkantısı | `…connection_creation_rate`, `…connection_close_rate` (+ `_total`) | gauge / counter | client_id | "Bağlantı açma / kapama (sn⁻¹)" |
| Üretici hata | `kafka.producer.record_error_rate` / `record_error_total` | gauge / counter | client_id, **topic** | "Gönderim hatası (kayıt/sn)" |
| Üretici yeniden deneme | `kafka.producer.record_retry_rate` / `_total` | gauge / counter | client_id, topic | |
| Üretici gönderim | `kafka.producer.record_send_rate` / `_total`, `byte_rate` | gauge / counter | client_id, topic | "Gönderilen kayıt/sn" |
| Üretici gecikme | `kafka.producer.request_latency_avg` / `_max` | gauge (ms) | client_id, **node_id** | "Broker istek gecikmesi" |
| Üretici tampon | `buffer_available_bytes`, `buffer_exhausted_rate`, `record_queue_time_avg/max`, `requests_in_flight`, `waiting_threads` | gauge | client_id | tampon baskısı |
| Tüketici lag | `kafka.consumer.records_lag`, `records_lag_avg`, **`records_lag_max`**, `records_lead(_min)` | gauge (kayıt) | client_id, topic, **partition** | **"Bu istemcinin gördüğü lag (partition)"** |
| Tüketici tüketim | `kafka.consumer.records_consumed_rate` / `_total`, `bytes_consumed_rate`, `fetch_latency_avg/max`, `fetch_rate` | gauge / counter | client_id, topic (fetch_latency: client_id) | |
| Tüketici koordinatör | `commit_latency_avg/max`, `commit_rate`, `rebalance_rate_per_hour`, `failed_rebalance_total`, `last_poll_seconds_ago`, `assigned_partitions`, `heartbeat_rate` | gauge / counter | client_id | rebalance fırtınası, poll donması |
| Kimlik doğrulama | `failed_authentication_rate/_total` (iki tarafta) | gauge / counter | client_id | |

Kullanılmayacaklar: `io_ratio`, `io_wait_ratio`, `iotime_total`, `io_waittime_total`,
`bufferpool_wait_time_total` (**Deprecated**, README).

### 2.3 Sürüm bağımlılığı

- Bu ad ailesi OTel Java agent 1.x'ten beri var; 2.x'te aynı (README `main`). Kafka
  client sürümü metrik kümesini değiştirir (ör. `commit_sync_time_ns_total` yeni
  client'larda). Bankadaki agent + kafka-clients sürümü — **doğrulanmadı**.
- Semconv'un yeni "messaging client metrics" ailesi (`messaging.client.operation.duration`,
  `messaging.client.sent.messages`, `messaging.client.consumed.messages`,
  `messaging.process.duration`; hepsi **Development** kararlılığı, `messaging.consumer.group.name`
  koşullu) Java agent Kafka instrumentation'ında **bu README'de yok** → bankada var mı
  **doğrulanmadı**. Varsa lag dışındaki her şey için semconv adı tercih edilir; tasarım
  ad-adayı listesiyle ikisini de tanıyacak (§4 Faz 1).
- `service_name` label'ının varlığı resource promosyonuna bağlı (VM: varsayılan açık;
  collector `prometheusremotewrite`: `resource_to_telemetry_conversion` açık olmalı).
  Operatör label setini Grafana'da gördü → var kabul, ama hangi yol — doğrulanmadı.

### 2.4 Operatörün doğrulayacağı komutlar (prod VM, salt-okunur)

```bash
VM=https://<vmselect>/select/0/prometheus   # ya da tek-node kökü
S=$(date -u -v-1H +%s 2>/dev/null || date -u -d '-1 hour' +%s); E=$(date -u +%s)
# 1) kafka_* metrik adları
curl -s "$VM/api/v1/label/__name__/values?match[]=%7B__name__%3D~%22kafka_.%2A%22%7D&start=$S&end=$E" | jq -r '.data[]' | sort
# 2) label seti (hangi label'lar var: service_name? host_name? client_id? topic? partition? node_id?)
curl -s "$VM/api/v1/labels?match[]=%7B__name__%3D~%22kafka_.%2A%22%7D&start=$S&end=$E" | jq -r '.data[]'
# 3) örnek seri (label değerlerinin şekli)
curl -s "$VM/api/v1/series?match[]=%7B__name__%3D%22kafka_consumer_records_lag_max%22%7D&start=$S&end=$E&limit=10" | jq '.data[]'
# 4) kardinalite (client_id × topic × partition)
curl -s "$VM/api/v1/status/tsdb?match[]=%7B__name__%3D~%22kafka_.%2A%22%7D&topN=20" | jq '.data.seriesCountByMetricName'
# 5) semconv messaging.* ailesi var mı
curl -s "$VM/api/v1/label/__name__/values?match[]=%7B__name__%3D~%22messaging_.%2A%22%7D&start=$S&end=$E" | jq -r '.data[]'
```
Kodsuz ara doğrulama (bugün, prod Coremetry): **Explore → metrik adı kutusuna `kafka_`**
— `ListMetricNames` VM'den `label/__name__/values` çeker (`vmetrics/client.go:487`);
liste doluyorsa seam VM'i görüyor, boşsa adlar/erişim sorunu var.

---

## 3. Doğruluk uyarıları (UI adlandırmasına bağlayıcı)

1. **`client_id` ≠ consumer group.** `client.id` Kafka istemcisinin kendi adı (varsayılan
   `consumer-<group>-<n>` / `producer-<n>` desenini taşıyor olabilir — doğrulanmadı, **ad
   ayrıştırılıp grup diye gösterilmez**). Group ve partition bazlı gerçek lag broker
   tarafından (kafka_exporter / Burrow / broker JMX) gelir; bu entegrasyonun kapsamı DIŞI.
   UI etiketi: "İstemci" (client_id), "Tüketici grubu" YAZILMAZ.
2. **`records_lag_max` = o instance'a atanmış partition'lar üzerindeki max lag**, instance
   içinde örneklenmiş. Topic toplam lag'i değil; instance ölürse seri kaybolur (lag
   sıfırlanmış gibi görünmez, seri yok olur — `Empty`/"seri yok" ayrımı şart). UI: "Bu
   istemcinin gördüğü en yüksek lag (partition başına)". Toplam için `max by (topic)`
   değil `sum by (topic) (records_lag)` bile yanıltır (aynı partition'ı iki instance görmez
   ama rebalance anında çift sayılabilir) → "yaklaşık" damgası.
3. **Gauge'lara `rate()` uygulanmaz.** `_rate/_avg/_max` Kafka'nın kendi 30 s pencereli
   ortalaması; `_total` sayaçlarına `rate()`/`increase()`. `RateWindowSec`/`QueryMetricRate`
   yalnız `_total` için.
4. **Yalnız OTel Java agent'lı servislerde var.** Seri yoksa bölüm gizlenir ya da
   "Bu servis Kafka client metriği yaymıyor (OTel Java agent yok)" notu; sayfa span-türevli
   görünümde kalır. VM yapılandırılmamışsa aynı yol (seam `Configured()`).
5. **Env daraltması VM'de ifade edilemez** (`metricsource.go:617-623`) → bölüm
   `envAmbiguous` ilan eder; k8s cluster daraltması da uygulanmaz (`clusterIgnored`,
   `endpoints_metric.go:40` deseni).
6. **Topic ↔ destination eşleşmesi kesin değil.** MV `destination` coalesce'unun 3.
   halkası `peer_service`, 4. `'unknown'` — VM `topic` label'ı bunlarla eşleşmez;
   eşleşmeyen satırda client bölümü "topic etiketi eşleşmedi" der, sessizce boş kalmaz.
7. **`host_name` = pod varsayımı** (%98, `podservice.go:8-12`); VM tarafı `k8s.pod.name`
   yayıyorsa `RuntimePodGroupBy` sırası (`vmetrics/runtime_pods.go:31`) ile aynı çözüm.
8. **Kardinalite:** `client_id × topic × partition` — büyük bankada lag serisi binlerce
   olabilir; `promapi.MaxSeriesParsed = 1000` sert tavan → sorgular her zaman
   `sum/max by (...)` ile daraltılmış, partition düzeyi yalnız tek client seçilince.

---

## 4. Eksikler ve riskler

| # | Bulgu | Etki | Yer |
|---|---|---|---|
| E1 | Messaging rotaları `api.go` içinde satır-içi | Yeni uç aynı yere konursa kısıt ihlali | `api.go:769-772` |
| E2 | Detay tam sayfası yok, yalnız çekmece | Client sağlığı + lag tablosu + RED bir çekmeceye sığmaz; `/endpoint`, `/database` emsali var | `App.tsx:163-168` |
| E3 | v0.9.834 kararı: liste sayfasında grafik yok | Client metrikleri liste sayfasına değil detay yüzeyine konmalı; aksi operatör kararına ters | `Messaging.tsx:178-181` |
| E4 | `messaging.operation*` okunmuyor; MV'de `kind` yok (topic MV) | "operation seviyesi RED" bugün yalnız caller MV'nin `kind` boyutu (producer/consumer) + ham span Top-ops; publish/receive/process ayrımı yok | `store.go:3865-3928`, `convert.go:170` |
| E5 | Consumer group span attribute'u okunmuyor | Span tarafında da grup yok → lag'i gruba bağlayacak köprü YOK | `convert.go`, grep sıfır |
| E6 | OTLP messaging golden testi yok | Faz 4'te kolon/alias eklenirse regresyon kapısı yok | `internal/otlp/convert_test.go` |
| E7 | VM `series` API'si yok; instant query üretimde yok | Keşif/kardinalite ölçümü `label/values` + `query_range` ile; yeterli, `series` eklemek şart değil | `vmetrics/client.go` |
| E8 | 1000 seri parse tavanı, 15 s timeout, VM→CH fallback yok | Lag sorgusu daraltılmazsa kırpılır; zaman aşımı boş panel | `promapi.go:74,173` |
| E9 | Ham PromQL ucunda allowlist yok (viewer+) | Alternatif C'yi seçersek mevcut yüzeyin riski büyür | `promql.go:50` |
| E10 | Demo Kafka client metriği basmıyor; lokal VM yok | Test yalnız mock `promapi` + tablo testi; canlı doğrulama prod'da operatörde | `cmd/demo/main.go:1817` |
| E11 | Çekmece pod hücresi pivotsuz | topic → servis → pod pivotu yarım | `DetailDrawer.tsx:649-651` |
| E12 | `messaging.destination.name` kimlik-önce aramada yok | Traces kutusuna topic yazınca indeksli yol yok; facet olarak eklenebilir | `promoted_attr.go:85-122`, `trace_facets.go:173-179` |

---

## 5. Uygulama alternatifleri

### A — Endpoints deseni: `/messaging?src=metric` tablo kaynağı anahtarı
Liste satırları topic; VM'de topic düzeyi yalnız üretici gönderim/hata/byte ve tüketici
tüketim/lag var, **gecikme yüzdeliği ve tüketici hata oranı yok**. Tablo "metrik kipinde"
yarısı boş kolonlarla çizilir; Endpoints'te işe yarayan desen burada tanecik uyuşmazlığı
yüzünden yanıltır. **Artı:** hazır desen, ~1 gün. **Eksi:** yanlış soyutlama, grafik
istemeyen liste sayfasına yük. → **Önerilmez.**

### B — Hibrit katman: liste CH'de, detay yüzeylerine "Kafka istemcileri (metrik)" (ÖNERİLEN)
- **Liste** (`/messaging`) değişmez (topic RED, CH MV). Yalnız satırda küçük bir rozet:
  "istemci metriği var" (VM'den `label/values` ile topic listesi, 60 s cache) — isteğe
  bağlı, Faz 2b.
- **Topic detayı** (çekmece bugün; tam sayfa `/messaging/topic` önerisi, E2): mevcut
  producers/consumers tablosuna (service, pod) satırlarının yanına VM sütunları: üretici
  gönderim/sn, hata/sn, yeniden deneme/sn; tüketici tüketim/sn, **gördüğü max lag**;
  altında iki `CorePanelMulti`: "Gönderim hatası (kayıt/sn) — servis bazında",
  "Tüketici lag (partition max) — istemci bazında". Pod hücresi `podDetailPath` linki.
- **Servis sayfası** (`/service/<ad>` Overview ya da Infra sekmesi): "Kafka client"
  paneli — bağlantı sayısı, açma/kapama oranı, broker istek gecikmesi avg/max (node_id
  kırılımı), üretici tampon baskısı, tüketici rebalance/poll. Bu metrikler topic'e değil
  **istemciye** ait, doğal ev servis sayfasıdır.
- **Degrade:** seam `Configured()==false` ya da seri yoksa bölüm çizilmez; `note`
  alanı sebebi taşır (`service_metric_red.go:250` deseni). `envAmbiguous` şeridi.
- **Artı:** tanecikler doğru yerde; mevcut seam, cache, tokenRef, MV, pivot aynen; api.go
  büyümez; her faz bağımsız. **Eksi:** ~3-4 gün; iki yüzeye dokunur; mockup ister.

### C — Grafana ithali: ham PromQL panelleri
Operatörün Grafana sorguları `$svc/$pod/$client` değişkenleriyle dashboard paneli olarak
(`/api/metrics/promql`, `dashboards_data.go`). **Artı:** ~1 gün, sıfır yeni uç. **Eksi:**
entity bağı yok (topic → servis → pod pivotu yok), degrade mantığı yok, cache anahtarı tam
sorgu dizesi, allowlist'siz yüzeye yaslanır (E9), Grafana'nın kopyası. → Yalnız "hemen bir
şey görünsün" istenirse geçici köprü; B'nin yerine geçmez.

---

## 6. Önerilen plan — fazlar (her biri bağımsız merge, kendi `v0.10.X`)

### Faz 0 — Keşif (operatör, kod yok, ~30 dk)
§2.4 komutları; agent/kafka-clients sürümü; label seti; `messaging_*` ailesi var mı;
kardinalite (topN). Çıktı bu dokümana eklenir; Faz 1'in ad-adayı listesi buna göre pinlenir.

### Faz 1 — Backend katalog + uç (≈1 gün)
- `internal/vmetrics/kafka.go` (**saf**): metrik kataloğu `{otelName, promName, kind:
  gauge|counter, unit, labels, question}`; soru → `MetricQueryFilter` üreticileri
  (`topicProducerHealth(svcs, topic)`, `topicConsumerLag(svcs, topic)`, `clientHealth(svc)`);
  daraltma daima `service_name` (aday sırası `ServiceIdentityLabels`) + `topic`; gauge'a
  `rate` YASAK (tip kapısı); tablo testleri (ad adayları `plainNameCandidates` ile
  `kafka_…` üretiyor mu — pin).
- `internal/api/messaging_metric_routes.go`: `registerRoutesExtra("messaging-metric", …)`
  → `GET /api/messaging/clients?system=kafka&cluster=…&destination=…&from&to`
  (topic bağlamı: caller MV'den servis/pod listesi → VM sorguları; yanıt `{available,
  note, envAmbiguous, rows[], series{}}`) ve `GET /api/services/{name}/kafka-clients`
  (servis bağlamı). `serveCached` anahtarı `msg-clients:v1:src=…:sys:cluster:dest:bucket:mx`,
  30 s; viewer+; audit yok (salt-okunur). Seam `metricSourceFor(r)`; VM yoksa `available:false`.
- E1 için ayrı mini dilim (SOR): `api.go:769-772` üç satır `messaging_routes.go`'ya taşınır.
- Test: mock `promapi` ile handler tablo testi; cache anahtar izolasyonu; `available:false`
  yolu; 1000 seri tavanında `truncated` bayrağı.

### Faz 2 — Topic detayı FE (≈1 gün)
- `lib/types.ts` `MessagingClients`, `lib/api.ts` `messagingClients(...)`; React Query
  (`staleTime ≥ 30 s`, çekmece açılınca fetch — ES-cost disiplini).
- `DetailDrawer.tsx` queue dalı: callers tablosuna VM sütunları (`useDataTable` COLS
  genişler, storageKey aynı), pod hücresi `podDetailPath` (E11), altına iki
  `CorePanelMulti` (lazy `corePanelEntry`, `syncKey` mevcut `msSyncKey`), `Empty` + not.
- 2b (SOR): liste satırında "istemci metriği" rozeti.
- Mockup (ASCII) onaydan önce sunulur; v0.9.834 kararına uyum: liste sayfasına grafik yok.

### Faz 3 — Servis sayfası "Kafka client" paneli (≈yarım gün)
Overview ya da Infra sekmesi (SOR); `StatTile` şeridi (bağlantı, açma/kapama, gecikme
avg/max) + bir `CorePanelMulti`; yalnız `available` ise render; `?src` anahtarı YOK
(bu panel yalnız metrikten gelir, span karşılığı yok).

### Faz 4 — Span tarafı doğruluk (≈1-2 gün, `/clickhouse-schema` + `/otel-conventions` ÖNCE, SOR)
- `internal/otlp/`: messaging golden testi (yeni + eski semconv yazımları: `destination.name`
  / `destination`, `operation.type|name` / `operation`, `consumer.group.name` /
  `kafka.consumer.group`).
- `messaging.operation` → `operation` boyutu: MV'ye kolon eklemek DROP+RECREATE ister
  (rolling-deploy okuma penceresi) ya da `attr_kvh` filtre yolu ile "operation" kırılımı
  ham span'den yalnız çekmecede (Top-ops zaten ham). Karar operatörde.
- `messaging.destination.name` trace facet'i (E12) — ayarla, kod gerekmez.

### Faz 5 — Alarm/anomali (sonra)
Evaluator VM yolu var (`evaluator/runtime_vm.go`); "istemci lag artışı" kuralı
`AlertRule.Target`/metrik kuralı olarak; anomaly `kind=external` değil, metrik kuralı.

---

## 7. Açık sorular (Faz 1'den önce)

1. **Faz 0 sonucu:** `kafka_*` adları/label'ları, `messaging_*` var mı, kardinalite.
2. **Detay yüzeyi:** çekmece mi, `/messaging/topic` tam sayfası mı (E2)? Öneri: tam sayfa
   (RED şeridi + callers + client bölümü + E2E), çekmece kısa özet kalır.
3. **Servis paneli yeri:** Overview mı, Infra sekmesi mi?
4. **Liste rozeti (2b)** istenir mi? Liste sayfasına grafik konmayacak (v0.9.834).
5. **Lag adlandırması:** "Bu istemcinin gördüğü max lag" — onay; broker lag'i (kafka_exporter)
   ileride ayrı kaynak olarak eklenecek mi?
6. **`api.go:769-772` taşınsın mı** (E1, −3 satır, davranış değişmez)?
7. **Faz 4 kapsamı:** MV'ye `operation` boyutu (şema cerrahisi) mi, çekmece-içi ham kırılım mı?
8. **Env:** VM tarafında env label'ı hangi yazımla var (`deployment_environment` /
   `deployment_environment_name`)? Bilinirse Kafka sorgularında sabit tek label ile
   daraltma denenebilir (bugünkü bilinçli ret genel seam için; buraya özel istisna SOR).

---

## 8. Faz 4b — `messaging_summary_5m` operation boyutu (v0.10.563)

**Ne değişiyor:** MV'ye `operation` boyutu (okuma-anı coalesce
`messaging.operation.type → messaging.operation.name → messaging.operation`, boş =
SDK yaymıyor), ORDER BY `(msg_system, cluster, destination, operation, time_bucket)`.
Yeni okuma `MessagingOperationRED` → çekmecede "Operasyonlar · MV" tablosu. Mevcut
okuyucular (liste, trend) operation'sız GROUP BY ile aynı sonucu verir (state'ler birleşir).

**Boot geçişi (kodlanmış yol):** `db_name` emsali — `system.columns` probe, kolon yoksa
`dropCombinedMV` + yeniden CREATE. **Bu yol 90 günlük messaging 5-dk kovalarını siler**
(ileriye dönük yeniden dolar). Kolon zaten varsa boot HİÇ dokunmaz.

**Prod için önerilen: yerinde geçiş (geçmiş korunur), deploy'dan ÖNCE elle** —
`reference-ch-inplace-mv-column-add` yordamı, CH 24.8'de doğrulanmış (v0.8.52,
`trace_summary_5m`); küme kipinde replika başına inner tablo (`.inner_id.<uuid>`,
`clusterAllReplicas(system.tables)` ile bul):

```sql
-- 1) depo tablosunu bul (küme kipinde her replika; `_local` MV'nin inner'ı)
SELECT hostName(), database, name, uuid
FROM clusterAllReplicas('<cluster>', system.tables)
WHERE engine = 'MaterializedView' AND name IN ('messaging_summary_5m', 'messaging_summary_5m_local');

-- 2) depo tablosuna kolon + SIRALAMA ANAHTARI (ikisi birlikte; anahtara eklemek ŞART:
--    operation anahtarda olmazsa AggregatingMergeTree birleşmesi farklı operasyonları
--    tek satıra çökertir ve boyut sessizce yok olur). MODIFY ORDER BY yalnız SONA
--    ekleyebilir → yerinde geçen kurulumun anahtarı (…, time_bucket, operation) olur;
--    taze DDL'den (…, operation, time_bucket) ayrışır ama okuma sonuçları aynıdır
--    ((msg_system, cluster, destination) öneki korunur).
ALTER TABLE `.inner_id.<uuid>`
  ADD COLUMN IF NOT EXISTS operation String DEFAULT '' AFTER destination,
  MODIFY ORDER BY (msg_system, cluster, destination, time_bucket, operation);

-- 3) MV sorgusunu değiştir (store.go v0.10.563 SELECT metni birebir)
ALTER TABLE messaging_summary_5m MODIFY QUERY <yeni SELECT>;
```

Sonra deploy: boot probe kolonu görür → no-op, geçmiş kalır. Yerinde geçiş yapılmazsa
deploy DROP+RECREATE ile temiz başlar (dürüst log satırı). **Küme kipi düzeltmesi
(v0.10.563):** boot geçişi artık çıplak addaki Distributed sarmalayıcıyı da düşürüp
yeniden kurar — önceki `db_name` geçişi yalnız `_local`'i düşürüyordu, sarmalayıcı eski
kolon setiyle kalıyor ve çıplak addan `SELECT <yeni kolon>` prod dağıtık CH'de kod 47
veriyordu (lokalde görünmeyen sınıf). Rolling deploy'da kısa bir okuma-hatası penceresi
kabul edilmiş maliyettir; hızlı roll.

**Doğrulama:** `SELECT operation, countMerge(span_count_state) FROM messaging_summary_5m
WHERE time_bucket >= now() - INTERVAL 1 HOUR GROUP BY operation` — publish/receive/process
satırları; `''` satırı = SDK operation yaymıyor (demo eski semconv `messaging.operation`
yayıyor, coalesce 3. halkasıyla yakalanır).
