# Dynatrace paritesi — Services · Problems · Predictive analytics (2026-09-12)

**Taban:** v0.10.695 · **Yöntem:** operatörün yüklediği Dynatrace skill'leri
(`dt-obs-services`, `dt-obs-problems`, `dt-obs-predictive-analytics`) yetenek
listesi olarak alındı; her madde koddan `dosya:satır` ile doğrulandı (iki
salt-okunur keşif). **Durum:** rapor; kod değişikliği yok. Önceki parite
denetimi `docs/plans/dynatrace-parity-2026-08-21.md` (EK B1 #1/#6 kapandı,
#3/#4/#5 açık).

Durum sözlüğü: **VAR** = Dynatrace'teki soru Coremetry'de aynı kalitede
cevaplanır · **KISMİ** = cevaplanır ama sınırlı · **YOK**.

---

## 1. Application Services

| Yetenek | Durum | Yüzey | Boşluk |
|---|---|---|---|
| RED (rate / error / p50-p95-p99), servis + operasyon | VAR | `service_summary_5m`, `operation_summary_5m`, `spanmetrics_1m`; `/api/services`, `/api/endpoints`, `POST /api/spans/metric-batch`; Services/Endpoints/Overview | — |
| Cluster / env yan yana kıyas | KISMİ | `/api/services/{name}/clusters` tablo (err/avg/p99) | MV'lerde cluster ve env boyutu yok → filtre ham span taramasına düşer (`servicesUseMV`, `errEndpointsMVEnv`); env altında sparkline kapalı; p50/p95 ve zaman serisi yok |
| SLA / SLO | VAR | `slo.go` availability+latency, burn-rate 2 pencere, budget forecast, autocreate; `/slos` | — |
| Sağlık skoru | KISMİ | `scoreHealth` 3 renk (critical problem / err>5 / err>1) | Sayısal 0-100 skor yok; latency ve SLO durumu skora girmiyor |
| Messaging (publish/receive/process/failure) | VAR | `messaging_summary_5m` operation boyutlu, e2e p95, Kafka istemci kataloğu, `/messaging` | Consumer-group **broker** lag yok (yalnız istemci JMX görüşü); Kafka dışı sistemlerde lag yok |
| Service mesh (Istio/Envoy overhead) | YOK | — | Hiç temel yok |
| Runtime metrikleri | KISMİ | `RuntimeCharts.tsx` JVM (heap/GC/threads), .NET, Go temel | Node.js (event loop), Python, PHP yok; Go goroutines, JVM class yok; runtime alarmları emekli (v0.9.1075) |
| Statik eşik / anomali / kıyas | VAR (endpoint eşiği KISMİ) | alert_rules p95/error_rate/rate, MAD-z + 14 g mevsimsel, `compare=prior`, CoSRE window_compare | Route/endpoint bazlı statik eşik yok (`RuleTarget` yalnız db_statement, kafka_client) |

## 2. Problems (Davis)

| Yetenek | Durum | Yüzey | Boşluk |
|---|---|---|---|
| Problem modeli | KISMİ | `problem.go` Kind service/db/external, P1-P3 + reason, RootCause özeti, blast radius, deploy | Kategori (AVAILABILITY/ERROR/SLOWDOWN/RESOURCE) alanı yok (rule_id önekinden türetilebilir); görüntü kimliği (P-1234) yok; etkilenen kullanıcı sayısı yok; host/node öznesi yok |
| Korelasyon / birleştirme | KISMİ | Anomali kümeleme (≥3 üye, kaynak servis), exception fırtınası, incident otomatik gruplama (30 dk / 1-hop), çapraz-sinyal füzyon | Dakikalara yayılan kaskadda erken açılanlar ayrı kalır (join-on-open yok); incident kök-neden çapası değil; nedensellik skorlu birleştirme yok |
| Kök neden | VAR | `topology_edges_5m` korelatör (2 hop, decay), deploy ≫ propagation ≫ co-firing füzyonu, DeepEvidence playbook, LLM verdict (auto v0.9.1281), etki (error share) | Temporal korelasyon çarpanı yok; host/process zincir adımı değil (yalnız node co-tenancy); kullanıcı etkisi yok |
| Yaşam döngüsü & trend | KISMİ | open→ack→resolved, snooze, `sweepStaleProblems`, benzer problem şeridi, `/shift`, gürültülü kural sayfası | MTTR/MTTA yok; problem zaman serisi/trend yok (`listProblemBuckets` yalnız sayım); "tekrarlayan" rozeti/filtresi yok |
| Çapraz alan | KISMİ | DB/external/K8s pod/rollout/log kanıtı/sentetik | Host/node özneli problem yok; external/db düğümü topoloji grafında değil |

## 3. Predictive analytics

| Yetenek | Durum | Yüzey | Boşluk |
|---|---|---|---|
| Forecast | KISMİ | SLO bütçe tükenme saati; DB kapasite ETA (lineer, R²≥0.6, ≤24 sa); CH disk ETA gün | Genel metrik forecast primitifi yok; mevsimsel/trend ayrışması yok; güven bandı yok; grafik üstünde çizilmiyor |
| Değişim / yenilik | KISMİ | MAD-z spike, log pattern new/spike, trace op new_error/error_spike, op p99 sıçraması, davranış motoru rejim kayması, `/api/correlate` "ne değişti" | Change-point (CUSUM/PELT) yok; step vs trend-onset ayrımı yok; şablon spike ertelenmiş |
| Eşik / adaptif / mevsimsel | KISMİ | statik kurallar + Watcher import; MAD-z openZ 3.5 / resolveZ 1.5; 14 g gün-sınıfı mevsimsel; hassasiyet ayarları | OTLP/infra metrikleri (JVM heap, CPU) için öğrenilmiş baseline yok — yalnız sabit eşik; kural editöründe "baseline mi eşik mi" seçimi yok |
| Sinyal karakterizasyonu | KISMİ (zayıf) | 7 g `MetricBaseline` → suggest threshold; SLO autocreate | Mevsimsellik/gürültü/trend profili yüzeyi yok; dedektör önerisi yok |
| Doygunluğa kaç gün | KISMİ | DB, CH disk | Host CPU/mem, pod/JVM heap, cluster, CH tablo büyümesi için projeksiyon yok |

---

## 4. Öncelikli boşluklar (birleşik sıralama)

Sıralama ölçütü: operatörün Dynatrace alışkanlığında en çok arayacağı şey ×
"korelasyon farklılaştırıcıdır" ilkesine hizmet (docs/DECISIONS.md).

| # | Boşluk | Uygulama taslağı | Boy | Korelasyon ilkesi |
|---|---|---|---|---|
| 1 | **Join-on-open birleştirme + incident düzeyi kök neden** — **GEMİDE** v0.10.698 (B: incident kök neden) + v0.10.699 (A: join-on-open, `clusterJoinWindow` 30 dk) | `detectAnomalyClusters` girdisine son N dk açık problemler; yeni açılış propagation-bağlantılı açık kümeye üye olsun; incident satırı üye hipotezlerin en yüksek güvenli TopSuspect'ini taşısın | M | ✔ doğrudan |
| 2 | **Temporal korelasyon çarpanı + 3 hop + dikey zincir** — **GEMİDE** v0.10.700 (dilim 1 gölge: faktör + gerekçe + ayar) + v0.10.701 (dilim 2: 3 hop, prompt/katalog zamansal satır, causal_chain node yönergesi) | `propagationMaxHops` vidası 2→3; 5 dk hata-serisi korelasyonunu (`ChangedService.Score`) propagation skoruna çarpan; `RankNodeCauses` adayı `causal_chain` adımı olarak verdict prompt'una | M | ✔ doğrudan |
| 3 | **Endpoint/route hedefli alert rule** — **GEMİDE** v0.10.705 (`http_route` hedefi, spanmetrics_1m ölçüsü, Endpoints ⚠ + detay düğmesi) | `RuleTarget`'a `http_route` türü; ölçü `spanmetrics_1m` (service, route) state'lerinden; Endpoints satırından "alarm kur" | S-M | kısmen |
| 4 | **OTLP/infra metrikleri için adaptif baseline** | `metricPolicies` desenine `jvm_heap_pct`, `gc_pause_ms`, `cpu_pct` (metricSource seam'i); mevcut dwell/seasonal kapıları aynen | M | ✔ (infra anomalisi hipoteze kanıt) |
| 5 | **Problem modeli: kategori + görüntü kimliği + etkilenen varlıklar** — **GEMİDE** v0.10.706 (kategori + `P-xxxxx`) + v0.10.707 (`/api/problems/{id}/affected`: çağıranlar ∪ hipotez pod'ları ∪ cluster'lar; çekmece + detay) | `rule_id` önek → `category` türetici (okuma anı, saf); `display_id` sıralı sayaç (boot-ALTER, iki-boot); `affectedEntities[]` = blast-radius callers ∪ AffectedPods ∪ cluster üyeleri | S-M | kısmen |
| 6 | **Genel forecast primitifi + "kaç gün" chip'i** | `capacityETA` + `diskETADays` → tek `forecast` paketi (lineer + haftalık mevsimsel ortalama, R² kapısı, ±band); Hosts/Clusters/AdminClickhouse'da chip; `self-*` ailesine host-disk/pod-heap ETA | M | ✘ |
| 7 | **MTTR/MTTA + problem zaman serisi** | `/api/problems/series` (`noisy_rules` medyan süre mantığı), Problems sayfasında trend şeridi | S | ✘ |
| 8 | **Cluster/env boyutlu RED rollup** | `service_env_summary_5m` (cluster, deploy_env) MV; `servicesUseMV` kapısı kalkar; ClusterBreakdown'a p50/p95 + seri; `/clickhouse-schema` iki-boot | L | ✘ |
| 9 | **Node.js / Python runtime kartları + Go goroutines** | `RuntimeCharts.tsx` FAMILIES'e nodejs (eventloop.delay/utilization, heap) ve python | S | ✘ |
| 10 | **Broker-side consumer-group lag** | VM'de `kafka_consumergroup_lag` ailesi `KafkaCatalog`'a `Side:"broker"`; topic çekmecesinde group lag; `kafka_group_lag` alert hedefi | M | ✘ |
| — | Sayısal sağlık skoru (0-100) | `scoreHealth` → error_rate + p99/baseline + SLO burn + açık problem ağırlıkları | M | ✘ |
| — | Service mesh | Istio `istio_requests_total` VM ailesi + sidecar span ayrımı | L | ✘ (temel yok; sıraya alınmadı) |

**Öneri:** 1 → 2 (ikisi de korelasyon çekirdeği, spec ister) · ardından 3 ve
5 (küçük, görünür) · 4 (adaptif baseline'ı infra'ya taşımak) · 6-7 (Davis
görünürlüğü). 8/9/10 operatör önceliğine göre.

## 5. Operatör UX bulgusu (2026-09-12, prod ekran görüntüsü)

Servis → **Operations** sekmesindeki TREND sütunu: satır başına üç mikro
sparkline (calls · errors · p99) 30 px yükseklikte, okunmuyor ("kullanışsız").
Seçenekler — Operations tablosu "klasik" yüzey, mockup-first + tek commit
geri alınabilir kuralı geçerli:

- **A.** TREND sütununu kaldır; satır başındaki 📈 zaten grafiği açıyor.
- **B.** Tek geniş sparkline: çağrı çubukları (hata payı kırmızı) + p99 çizgisi,
  ~160 px, hover'da değer. Dynatrace/Datadog "top requests" düzeni.
- **C.** Sütunu kapalı varsayılan yap (kolon yöneticisinde seçilebilir).

Karar operatörde; öneri **B** (mockup ile).

**Sonuç (2026-09-12):** B onaylandı ve gemide v0.10.697 (`TrendSpark`).

## 6. #1 uygulama notları (v0.10.698 + v0.10.699)

- **B — incident kök neden:** `chstore/incident_rootcause.go`
  (`IncidentProblemIDs` tek sorgu + `GetHypotheses("problem")` tek sorgu +
  saf `pickIncidentRootCause`: adı olan, eşik 0.05 üstü, max güven → max
  skor → ad). `Incident.rootCause` okuma-anı; liste kolonu + detay çipi.
  Yalnız hipotezler (küme SourceScore karıştırılmadı); warning-only
  incident dürüstçe "—".
- **A — join-on-open:** `recentOpenCandidates` (son 30 dk açılmış bireysel
  `anomaly:<svc>:<metrik>` satırları, bu tik çözülenler hariç) aday
  listesine katılır; küme yazıldıktan sonra `mergeIntoCluster` üyeleri
  `resolved` + "· merged into anomaly-cluster:<src>" ekiyle kapatır
  (kolon yok); küme `StartedAt` = en eski katılan üye. Bildirim: bireysel
  anomali resolve'u bugün de bildirmiyor, merge de sessiz; kümenin tek
  bildirimi kaynağa. Kayan-pencere simülasyon testi
  `clustering_join_test.go` (kaskad t0/t+2/t+4, 31 dk kenarı, determinizm).
- Ayrı kalem: merged üyenin ekibine küme haberi (519 ekip yönlendirmesi).

## 7. #2 uygulama notları (v0.10.700, dilim 1 / gölge)

- `correlator/temporal.go`: `ComputeTemporalFactor` — birinci farklar
  üzerinde Spearman, lag 0..2 kova (aday önde), onset sırası cezası
  (aday tetikleyiciden sonra sıçradıysa ×0.5), <6 dolu kova → nötr 0.5.
- `Synthesize`: Tier 2 (yapısal komşu, Kind=="") adayına `Structural /
  Temporal / TemporalReason` yazılır; `TemporalApply` ile
  `Score = Structural × (0.5 + 0.5·t)`. Reason metni gölgede AYNEN kalır
  (üç metin pini korunur), gerekçe ayrı alanda.
- Seri okuması `chstore.ServiceErrorRateSeries5m` (service_summary_5m,
  IN ≤12, LIMIT 300 BY, 10 s); işçi pencere [onset−60m, son tam kova),
  tavan 3 saat. İşçi iki anchor yolunda Synthesize ÖNCESİ `attachTemporal`.
- Anahtar `anomaly_sensitivity.temporalRanking` (`shadow` varsayılan /
  `on`); Settings → Anomaly seçici. Operatör prod'da gölgeyi izleyip açar.
- Ribbon: kalıcı hipotez adayları artık çiziliyor (`lib/rootCauseCandidates.ts`;
  bugüne dek yalnız canlı correlations çiziliyordu) — kind rozeti + ⏱ gerekçe.
- **Dilim 2 GEMİDE v0.10.701:** `propagationMaxHops` 2→3 (hop-3 = 0.25×pay
  çarpımı, `TestRank3HopDecay`); zamansal gerekçe üç prompt yüzeyinde aday
  satırına " · " ile eklenir (hipotez bloğu TR, hakem kanıt kataloğu, Explain
  düzyazı) — gerekçesiz satır bayt-özdeş; hakem sistem prompt'una NEDENSEL
  ZİNCİR paragrafı (node adayı ayrı adım, "leads by" nedene yakın, "rose
  after it" semptom). Anahtar hâlâ operatörde (`temporalRanking` shadow).

## 8. #3 uygulama notları (v0.10.705)

- `RuleTarget.Kind = http_route` (+ `Route`), kimlik (service, http.route);
  method MV'de yok, RPC ve imza kipi kapsam dışı. Metrik ailesi önekli:
  `http_route_p95_ms | p99_ms | error_rate | rate` (düz adlar servis kuralıyla
  çakışmasın). Doğrulama `ValidateRuleTarget` üçüncü dalı; şema yok
  (target_json).
- Ölçü `chstore.RouteWindowStats`: spanmetrics_1m, giriş-span yüklemi,
  1 dk grid'e hizalı [now−w, now) yalnız tam kovalar, tDigest idx 3/4;
  rate = calls / kapsanan sn. Env-agnostik (MV'de deploy_env yok) — modal
  satır env altında okunduysa söyler.
- Evaluator: özne servis, Kind=service (Kafka emsali); MinSamples =
  penceredeki çağrı; açıklama `/endpoint?service=&path=` yolunu taşır.
- FE: RouteAlertModal (Statement/Kafka klonu, karşılaştırıcı seçilebilir —
  hız düşüşü için `<`), Endpoints satırı ⚠ (editor/admin, yalnız HTTP
  sekmesi), EndpointDetail başlığında "⚠ Alarm oluştur" (imza kipi hariç),
  /alerts "HTTP ROUTE" rozeti + salt-okunur kapsam.

## 9. #5 uygulama notları (v0.10.706, dilim 1)

- `chstore.ProblemCategory(p)` saf: rule_id öneki + metric + kind + comparator
  → AVAILABILITY / ERROR / SLOWDOWN / RESOURCE / CUSTOM (18 üretici ailesi
  tablo testinde). `ProblemDisplayID(id)` = "P-" + FNV-32a base36 (türetilmiş,
  sıralı değil; operatör kararı); `GetProblemByDisplayID` son 90 g / 5000 id
  tarar, en yeni eşleşme kazanır. İkisi de `EnrichProblemsWithPriority`
  döngüsünde dolar; şema yok.
- `/api/problems/{id}` ve MCP `get_problem` "P-…" kabul eder; `/api/problems?
  category=`, `/api/inbox?cat=` çip süzgeçleri (okuma-anı, zenginleştirme
  sonrası; api.go süzgeçleri `problems_read_filters.go`'ya taşındı, api.go
  10 satır küçüldü).
- Inbox: exception/httperror → ERROR, anomali türüne göre ERROR/SLOWDOWN,
  incident → CUSTOM (`inboxDerivedCategory`). FE: "Kategori" çipi (`?cat=`),
  SOURCE altında rozet, çekmece başlığında kategori + tıkla-kopyala `P-…`,
  palete `P-3f9a2` yazınca problem açılır.
- **Dilim 2 GEMİDE v0.10.707:** `GET /api/problems/{id}/affected` (kendi
  dosyası, route defteri, 60 s cache, `P-…` kabul): pencere onset−1h..çözüm|
  şimdi (≤24 s); saf `buildAffectedEntities` = blast-radius çağıranlar (çağrı
  desc, ≤25, kendi problemi açık olan işaretli) ∪ hipotez `Deep.AffectedPods`
  ∪ `Clusters`, tekrarsız, özne dışarıda. FE `AffectedEntitiesList` çekmece
  (yalnız problem satırı) + detay Blast radius bölümünde; servissiz problem
  dürüst "—". #5 KAPANDI (küme üyeleri yapısal alan olarak ertelendi).
