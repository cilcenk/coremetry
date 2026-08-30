# K8s entity katmanı — AŞAMA 1: keşif ve fizibilite

> **⚠ BAYAT (2026-08-30, rollouts audit):** buradaki "prod span'inde uid/deployment/container/replicaset YOK" tespiti EN AZ bir prod cluster'ında artık geçerli değil — k8sattributes (ya da eşdeğeri) açık, `k8s.replicaset.name` ve `container.image.*` dahil tam set görüldü (n=1 pod). Öte yandan İKİNCİ bir cluster `k8s.namespace.name` bile basmıyor (v0.10.190). Kapsama CLUSTER BAŞINA ölçülür: Admin → System → K8s Coverage (v0.10.192 sayaçları).

**Tarih:** 2026-08-28 · **Kapsam:** cluster > node > namespace > workload > pod >
container hiyerarşisi + service ekseni, trace/metrik verisine bağlanma
(Dynatrace host → service pivotu muadili). **Kod DEĞİŞTİRİLMEDİ, şema
AÇILMADI.** Önceki iş: [k8s-entity-layer-2026-08-26.md](k8s-entity-layer-2026-08-26.md)
(prod tek-span kanıtı + fazlı plan), [clusters-pod-service-correlation-audit.md](clusters-pod-service-correlation-audit.md),
[thanos-multicluster-metrics-audit.md](thanos-multicluster-metrics-audit.md),
[openshift-cluster-attr-audit.md](openshift-cluster-attr-audit.md).

> ⚠ **Kanıt sınırı — okumadan geçme.** Bu makineden prod'a erişim YOK
> (`kubectl config get-contexts` → yalnız `minikube`; Thanos yalnız prod'da).
> Prod'a bağlı her madde (A2 karşılaştırma, B5 filo doluluğu, C8-C11 Thanos
> serileri, D12-D14 ölçek) **`scripts/probe/entity-discovery-prod.sh`**
> ile operatör tarafından koşulur; tablolardaki boş hücreler o çıktıyla
> dolar. SQL kısmı lokal CH'de prova edildi (sözdizimi + şekil), PromQL
> kısmı lokalde koşamaz. Lokal veri **demo fixture'ıdır**
> (`cmd/demo/main.go:477-500` k8s.* anahtarlarını elle basar) — kanıt
> olarak KULLANILMADI, yalnız "sorgu çalışıyor" provası.

---

## A. Cluster kimlik zinciri — tasarımı belirleyen bulgu

### A1. Remote Cluster kaydı

`internal/thanos/client.go:41-58` — **yedi alan, id YOK, external label YOK**:

```go
type ClusterConfig struct {
	Name               string `json:"name"`      // join anahtarı — id bu
	URL                string `json:"url"`       // cluster'ın KENDİ Thanos Querier'i
	AuthType           string `json:"authType"`  // "" | none | bearer
	Token              string `json:"token"`     // asla echo edilmez (HasToken)
	NamespaceFilter    string `json:"namespaceFilter"` // namespace=~"…" her sorguya
	InsecureSkipVerify bool
	Enabled            bool
}
```

| Soru | Cevap | Kanıt |
|---|---|---|
| Nerede saklanıyor | `system_settings["thanos_clusters"]`, tek JSON blob (`Settings{Clusters []ClusterConfig}`), boot `LoadPersisted` + 30 s çok-pod poll + `SavePersisted` canlı swap | `internal/chstore/thanos.go:13`, `client.go:218/239/262` |
| Sorgu katmanı nasıl okuyor | `ClusterByName(name)` — yalnız `Enabled` kayıtlar; **her handler `?cluster=<Name>` alır, o kaydın URL'ine gider** | `client.go:320-332`, `internal/api/thanos_handlers.go:20-23` |
| Kayıt kimliği | `Name` (PUT'ta tekillik zorlanır) — sayısal/opak id yok | `thanos_handlers.go:744-752` |
| UI | Settings → Clusters: "Cluster name (join key)" Combobox'ı son 24 h span'lerde GÖRÜLEN cluster adlarıyla beslenir, girilen ad görülmemişse uyarır | `frontend/src/pages/settings/ClustersTab.tsx:138-156` |

### A2. Üç değer yan yana — kodun söylediği

| | Değer | Kaynak |
|---|---|---|
| (a) Remote Cluster id/name | `ClusterConfig.Name` | yukarıda |
| (b) Thanos serisindeki cluster etiketi | **KOD HİÇBİR CLUSTER ETİKETİ OKUMUYOR.** Satırlara cluster adı Go'da konfigden damgalanır (`row.Cluster = c.Name`); PromQL'lerde `cluster=`, `cluster_id`, `prometheus` **0 kez** geçer | `internal/thanos/client.go:570, 677, 591`; `promql.go` grep: 0 |
| (c) Span'deki cluster değeri | `cluster` MATERIALIZED kolonu = 6-yol coalesce: `res.k8s.cluster.name → res.openshift.cluster.name → res.cluster → attr.(aynı üçü)`; prod'da dolan basamak **`openshift.cluster.name`** | `internal/chstore/repo.go:307-315`, `store.go:1036`; prod: 08-26 denetimi §0 |

**Çıkarım (kritik):** Bugünkü model **"cluster başına ayrı Thanos URL'i"**
varsayar (`docs/runbooks/thanos-clusters.md:14-15, 68`: her cluster'ın
`thanos-querier` route'u). Operatörün tarif ettiği **TEK Thanos Querier**
modelinde N Remote Cluster kaydı aynı URL'i taşır ve **her sorgu bütün
cluster'ların serilerini döndürür** (`topk(500)` ile kırpılmış): pod tablosu,
node tablosu, deployment join'i cluster'ları birbirine karıştırır. Şu anki
prod'da bunun görünmemesi ya `NamespaceFilter`'ın cluster'ları tesadüfen
ayırmasından ya da hâlâ per-cluster route kullanılmasındandır — **probe A2(b)
bunu netleştirir** (`count by (cluster) (kube_pod_info)` → hangi etiket kaç
değer). Tasarım şartı: Remote Cluster kaydına **external label adı + değeri**
eklenir ve `internal/thanos/promql.go`'daki her seçici bu eşleyiciyi taşır.

**Karşılaştırma tablosu — operatör doldurur (`probe A2`, ≥3-4 cluster):**

| Remote Cluster `Name` (a) | Thanos etiket adı / değeri (b) | Span `cluster` değeri (c) | Aynı mı? |
|---|---|---|---|
| … | … | … | … |

Lokal fixture (kanıt değil): `cluster` = `k8s.cluster.name`, 5 değer
(`prod-eu-west`…); `openshift.cluster.name` lokalde 0 span → prod dalı
lokalde SINANAMAZ ([root-span-cluster-tag-audit.md](root-span-cluster-tag-audit.md)).

### A3. Eşleme alanları — gerekiyor

(a)≠(b) kesin (b yok), (a)=(c) UI uyarısıyla yumuşak zorlanıyor. Öneri
(tasarım): `ClusterConfig`'e `ThanosLabelName` (varsayılan `cluster`),
`ThanosLabelValue` (boş = `Name`), `SpanClusterValue` (boş = `Name`) — geriye
dönük doldurma: boot'ta boş alanlar `Name`'den türetilir, hiçbir kayıt
yeniden yazılmaz; UI'da "Thanos'ta görülüyor / span'de görülüyor" rozetleri
(`count(kube_node_info{<label>="<value>"}) > 0` ve mevcut `nameKnown`).

---

## B. Span tarafı

### B4. Collector — `k8sattributes`

| Soru | Cevap | Kanıt |
|---|---|---|
| Var mı | Chart'ta **v0.10.92'den beri OPT-IN, varsayılan `false`**; compose konfiglerinde hiç yok | `charts/coremetry/values.yaml:478-485`, `templates/otel-collector.yaml:22-42` |
| Ne çıkarır | `k8s.namespace.name, k8s.deployment.name, k8s.pod.name, k8s.pod.uid, k8s.node.name, k8s.container.name`; **`k8s.cluster.name` çıkaramaz** (processor'ın verebildiği bir alan değil), replicaset adı ve `container.image.tag` listede yok | aynı |
| Pod eşleme | tek kural `from: connection` (OTLP bağlantısının kaynak IP'si) | aynı |
| RBAC | ClusterRole `pods,namespaces` + `apps/replicasets` get/list/watch, yalnız bayrak açıkken render | `templates/otelcol-rbac.yaml` |
| Prod'da | **KAPALI.** Prod span'inin 22 resource attribute'u **elle yazılmış downward API env listesinden** geliyor (k8sattributes bu kadar seçici bir küme üretmez) | 08-26 denetimi §0; memory (v0.10.93 notu) |
| Diğer processor'lar | `resourcedetection` yok; `resource` processor yalnız compose'da (`telemetry.processed_by`); hiçbir processor attribute düşürmez/yeniden adlandırmaz | agent taraması, `internal/otlp/convert.go:502-513` (`attrsToArrays` koşulsuz, cap yok) |
| Açma riski | collector restart ister; pod bounce'ta collector wedge'i bilinen risk | `internal/chstore/k8s_coverage.go` başlığı, [feedback-otelcol-restart-after-rollout] |

### B5. Doluluk — filo geneli ÖLÇÜLMEDİ, araç hazır

Prod kanıtı bugün **tek span**'in resource seti (08-26 §0, n=1 —
namespace ilk taslakta yanlış "YOK" yazılmıştı, operatör düzeltti):

| VAR (prod, n=1) | YOK (prod, n=1) |
|---|---|
| `openshift.cluster.name`, `k8s.namespace.name`, `k8s.pod.name`, `k8s.pod.ip`, `k8s.node.name`, `host.name` (= pod adı), `container.image.name/.tag`, `service.instance.id` | **`k8s.pod.uid`**, `k8s.deployment.name`, `k8s.container.name`, `k8s.replicaset.name`, `k8s.cluster.name` |

Filo geneli ölçüm için iki yol, ikisi de operatörde: (1) **System → K8s
Coverage** (`/api/k8s/coverage`, v0.10.36, admin) — servis × yedi anahtar
var/yok tablosu, ekran görüntüsü yeter; (2) `probe B5` — servis başına
pod/uid/ns/node/cluster yüzdeleri + tam/kısmi/hiç özeti. "Boş olanlar hangi
servisler ve neden" sorusunun cevabı: **k8sattributes kapalı olduğundan
doluluk tamamen üreticinin kendi env listesine bağlı** — listeyi almayan her
iş yükü (özellikle Coremetry'nin kendi self-telemetry'si, v0.10.91 downward
API'ye kadar) boş kalır.

Lokal fixture (yalnız SQL provası): 99/100 servis pod/ns/cluster %100,
uid %0, node %0 — demo üreteci böyle basıyor.

### B6. Span'de cluster'ı ayıran alan

`openshift.cluster.name` (prod). `deployment.environment` prod'da tek
değer ("prod'ta herkes prod env", operatör 2026-07-18) — cluster ayırıcı
DEĞİL. Engel yok; ama iki not: (i) prod dış Distributed CH'de chstore'un
`cluster` MATERIALIZED kolonu `spans_local`'a **inmemiş olabilir**
(v0.8.185/186 sınıfı; `hasClusterCol` boot probe'u) — inmediyse her cluster
okuması 6-yol dizi türetimiyle koşar (v0.9.692 ölçümü: kolon 137 → 8.7 MiB,
81 → 11 ms); `probe E` bunu sorar. (ii) "ocpma çıkıyor, ocpmb çıkmıyor"
(v0.9.138) bazı cluster'ların span'lerinde tanımlayıcının eksik/uyumsuz
olduğunu gösterdi — A2(c) tablosu bunu cluster cluster kanıtlar.

### B7. `k8s.pod.uid`

**Prod'da YOK.** İki kaynaktan gelebilir: k8sattributes (kapalı) ya da
downward API'ye `metadata.uid` eklenmesi (08-26 §5 FAZ 3, operatör tarafı,
opsiyonel). **Operatör kararı 2026-08-26: kimlik `(namespace, pod_name)`,
uid beklenmiyor** (`internal/chstore/pod_inventory.go:11-24`). Görev
metnindeki "uid mevcutsa birincil anahtar odur" ile uyumlu: mevcut değil →
ikincil anahtar `(cluster_id, namespace, pod_name)` + **zaman geçerliliği**
(StatefulSet ad sabitliği ömür ayrımını uid'siz de kısmen çözer: aynı ad,
`first_seen` boşluğu > eşik → yeni ömür).

**Sonuç B:** k8sattributes kapalı ama prod span'leri **pod/namespace/node/
cluster'ı zaten taşıyor**; entity katmanı bunun üzerine kurulabilir.
k8sattributes'ın getireceği artı: uid, deployment/container adı ve kendini
beyan etmeyen bileşenler. Tasarım "k8sattributes açılınca daha iyi, kapalıyken
de çalışır" olmalı; açılması **ayrı operatör kararı**.

---

## C. Metrik tarafı (Thanos)

### C8. Seriler — kodun bugün kullandığı ve doğrulanacaklar

| Seri | Kodda | Okunan etiketler | Prod doğrulaması |
|---|---|---|---|
| `kube_pod_info` | **YOK** (yalnız plan dokümanlarında boşluk olarak) | — | probe C8 (`node`, `pod_ip`, `uid`, `created_by_kind/name`, `host_network`) |
| `kube_pod_owner` | var — `{owner_kind="ReplicaSet",pod!="",namespace="…"}` | `pod`, `owner_name` | probe C8/C9 (`owner_kind` dağılımı: RS/STS/DS/Job/Node) |
| `kube_replicaset_owner` | var — `{owner_kind="Deployment",namespace="…"}` | `replicaset`, `owner_name` | probe |
| `kube_pod_container_status_restarts_total` | var — `sum by (namespace,pod)` | `namespace`, `pod` | probe (`container`, `uid`) |
| `kube_node_info` | var — instance↔node köprüsü | `internal_ip`, `node` | probe (`kernel_version`, `provider_id`, `os_image`, `system_uuid`) |
| `kube_namespace_labels` | **YOK** | — | probe |
| diğer kullanılanlar | `kube_pod_status_phase`, `…_last_terminated_reason`, `kube_pod_container_resource_{limits,requests}`, `kube_node_role`, `kube_node_status_capacity`, `kube_deployment_{spec_replicas,status_replicas_ready,status_condition}`, cAdvisor `container_*`, node-exporter `node_*`, `ALERTS*`, haproxy, JMX | `internal/thanos/promql.go:87-411` | — |

Kaynak: `internal/thanos/promql.go:177, 212-216, 237-241, 376-386`; join
Go'da (`client.go:886-894`). Seçici tavanları: `topk(500)`,
`maxSeriesParsed=1000`, 8 MB gövde, 15 s HTTP / 10 s handler deadline,
`dedup`/`partial_response` parametresi **gönderilmiyor**.

### C9. Owner zinciri ve node ataması

- **Pod → ReplicaSet → Deployment:** kodda var (Go join), namespace
  kapsamlı. Yedek: pod adı sonek kırpma (`stripPodSuffixes`,
  `deploymentFromPodName`, iki farklı sertlik).
- **StatefulSet / DaemonSet / Job:** `kube_pod_owner.owner_kind` doğrudan
  (tek hop); kod bugün yalnız RS dalını okuyor → tasarımda eklenir
  (CronJob → Job → Pod iki hop: `kube_job_owner`).
- **Pod → Node:** Thanos'tan **YOK** (`kube_pod_info.node` okunmuyor);
  bugün tek pod→node bağı **span-türevli** (`k8s.node.name`, RUNS_ON kenarı
  v0.10.93, `internal/chstore/topology.go:671-728`). `kube_pod_info` ile
  kapanır — probe C9 son satırı node'suz pod sayısını verir.
- Eksik kalan alan: **container** (`kube_pod_container_info` var ama
  okunmuyor; span'de `k8s.container.name` prod'da yok) → container
  varlığı yalnız Thanos'tan.

### C10. Etiket ↔ attribute eşleme tablosu

| Varlık alanı | Prometheus/KSM etiketi | Span resource attr (prod'da) | Not |
|---|---|---|---|
| cluster | **(yok — external label, probe A2)** | `openshift.cluster.name` (6-yol coalesce) | eşleme Remote Cluster kaydı üzerinden |
| namespace | `namespace` | `k8s.namespace.name` (VAR); kanonik zincir `service.namespace → k8s.namespace.name → kubernetes.namespace.name/_name` (`identity.go:249-286`) | `metric_points`'te **0/101** (07-17 ölçümü) |
| pod | `pod` | `k8s.pod.name` (VAR) = `host.name` = `host_name` kolonu (%98 ölçüldü; **konvansiyon, sözleşme değil**) | |
| pod uid | `uid` | `k8s.pod.uid` (**YOK**) | |
| node | `node` (`kube_pod_info`) | `k8s.node.name` (VAR) | |
| container | `container` | `k8s.container.name` (**YOK**) | yalnız Thanos |
| workload | `owner_kind/owner_name` (kube_pod_owner + kube_replicaset_owner) | `k8s.deployment.name` (**YOK**) — türetim: pod adından kırpma | |
| service | — | `service.name` = `service_name` kolonu | metrik tarafında servis yok; köprü `(cluster, host_name)` (`podservice.go`) |
| image/version | `kube_pod_container_info.image` | `container.image.tag` (VAR) | deploy ailesi bunu kullanıyor |

### C11. Erişim

Token = Settings'e yapıştırılan **ServiceAccount token'ı**, `thanos_clusters`
blob'unda saklanır; env/Secret/mounted SA **yok** (`client.go:417-420`; grep
`THANOS` → 0). Kapsam = o token'ın Thanos'ta gördüğü; tek querier modelinde
tüm cluster'lar (`cluster-monitoring-view` beklenir). Kısmi görünürlük
ölçümü: `probe C11` (`count by (cluster) (kube_node_info)` ↔ Remote Cluster
listesi farkı).

---

## D. Ölçek — prod probe (D12-D14)

Bu makineden ölçülemez. Probe çıktısı: cluster/namespace/pod sayıları
(`count by (cluster)`), 24 h pod devri (`count_over_time(kube_pod_info[24h])`
− anlık, `kube_pod_created > time()-86400`), fan-out süresi + seri sayısı
(`timed q 'kube_pod_info'` üç varyant). 08-26 denetiminin **modelleme
varsayımı** (40 node · 2.500 pod · 3.500 container · 300 iş yükü) ölçüm
değildir; tablo büyüme tahmini D13'e bağlanacak.

Fan-out bugün **istemcide** cluster başına istek (thanos-multicluster
denetimi §6 kararı: yavaş cluster diğerinin cache'ini bozmasın). Tek querier
modelinde bir sorgu tüm cluster'ları getirir → senkronizasyon **cluster
başına `{<label>="<değer>"}` seçicili ayrı sorgu** olarak kalmalı (kısmi
başarı + atlanan cluster kaydı doğal olarak çıkar).

---

## E. Mevcut yapı

### E15. Şema kalıbı (özet; tam sözleşme `/clickhouse-schema`)

- Ham telemetri `MergeTree`, ORDER BY `(kimlik…, time)`; state
  `ReplacingMergeTree(version)` + `FINAL`, ORDER BY = dedup anahtarı, PARTITION
  BY yalnız gerekiyorsa (Kural P1: partition kolonu yeniden yazılıyorsa FINAL
  kopyayı temizleyemez); MV `AggregatingMergeTree`, `PARTITION BY
  toDate(time_bucket)`, `ORDER BY (filtre öznesi…, time_bucket)`, TTL gün.
- **Dağıtık gün-bir kuralı:** yeni tablo `highVolumeTables` +
  `defaultShardPolicy` + `tablesWithoutTraceID`'ye aynı commit'te
  (`internal/chstore/cluster.go:921, 428, 300`); `adaptDDL` yalnız dört motoru
  Replicated'a çevirir. Prod dış Distributed'da chstore ALTER'ları
  `spans_local`'a **inmez** → `spans`'a MATERIALIZED kolon eklemek
  **operatör migration'ı** (`migrations/0005_attr_promotion.sql` emsali).
- Resource attribute okuyan MV **tek**: `service_version_5m`
  (`store.go:3527-3547`, 7 anahtarlı `has()` kapısı + `multiIf`).
  **`pod_seen` MV bilinçli ERTELENDİ** (`pod_inventory.go:41-54`): namespace/pod
  promoted kolon değil, MV her span'de `indexOf` koşardı. → `entity_seen` MV
  ancak **promoted kolon** (v0.9.621-623 `attr_channel_code` + `set(0)` emsali:
  10 M satırda 3.9 GiB → 261 MiB) ya da `has(res_keys,'k8s.pod.name')` kapısı +
  ölçümle gelir.
- TTL: ham 30 g (operatör ayarı), `topology_edges_5m` 14 g,
  `service_version_5m` 45 g, `service_seen` TTL'siz (ilk-görülme yalanı olmasın).
- Migrations: `NNNN_topic.sql`, operatör uygular, boot ASLA koşmaz, `ON
  CLUSTER uptrace_all` token'ı, ledger yok → idempotent + ileri-onarılabilir.

### E16. Servis kimliği

**Yalnız `service_name`** (`otlp/convert.go:67`; `service.namespace` kolona
alınmaz). 18 tablonun shard anahtarı `cityHash64(service_name)`; MV'lerde
cluster/namespace/env boyutu **yok** → aynı `service.name` iki cluster'da
**BİRLEŞİR**. Ayıran yalnız üç ham-tarama yolu: `GetServiceClusterBreakdown`
(`repo.go:666-707`), `GetServiceClusterMap` (`:389-432`, 60 s cache),
`/api/services?cluster=` (MV'yi kapatır, `api.go:1904-1908`). `service_metadata`
namespace/deployment **tek-değerli mod** — çok-cluster serviste biri kazanır.
Kırmızı çizgi (entity-model memory): doğal anahtar birincil kalır, `entity_id`
hiçbir ORDER BY/shard/önbellek anahtarına girmez.

### E17. Sayfalar → veri

| Sayfa | Kaynak |
|---|---|
| `/clusters` | %100 Thanos, istemci fan-out; tek CH zenginleştirme `PodServiceMap` (`metric_points`, `(cluster, host_name)`, 15 dk, LIMIT 5000) |
| `/services` | `service_summary_5m` (MV); `?cluster=`/`?env=` → ham `spans` |
| `/service-map` (topology yönlendirir) | sampled traces (`GetServiceMap`) + `topology_edges_5m` (k8s cluster boyutu **yok**; RUNS_ON kenarı `node:<k8s.node.name>` yazılıyor, yüzeyi v0.10.96'da geri alındı) |
| `/pod`, Service → Infra | Thanos + `podWorkloadName` yedek eşleme |
| System → K8s Coverage, `/api/k8s/pods` | `spans` örneklemeli (300 k), `(namespaceExpr(), k8s.pod.name)` |

### E18. Sorgu katmanı ve kalıplar

- Rota: kendi dosyasında `registerXxxRoutes(mux)`, `api.go` tek satır
  (şablon `internal/api/admin_trace_backfill.go`, `vmetrics_routes.go`);
  `s.serveCached(...)` anahtarında TÜM girdiler FNV (`endpointKeyDigest`
  deseni); `auth.RequireRole/RequireAnyRole`; `s.audit(r, action, kind, id, details)`.
- Ayar/bayrak: `system_settings` JSON blob + `LoadPersisted` boot +
  `SavePersisted` canlı swap (`internal/vmetrics/client.go:61-178` — backend
  anahtarı emsali; `internal/tempo/client.go` kanonik).
- Lider: `cache.NewLeaderHolder(lock, key, cache.LeaderTTL(interval))` +
  `Start(ctx)` + tick başında `IsLeader()`; worker başına ayrı anahtar;
  `SetOnAcquire` ile devir anında ilk tick (`internal/cache/leader.go`,
  kullanım `internal/topology/aggregator.go:45-69,138`). `COREMETRY_MODE`
  (`all|ingest|api|worker|agent`, `main.go:112-129`) worker'ları `worker`
  rolüne bağlar.
- Self-observability: `atomic.Int64` + `XxxObservability()` anlık görüntüsü
  → `SystemStats` (`/admin/stats`), CH sorguları `tracedConn` ile zaten span;
  OTel `selfobs.Meter()` mevcut ama tek kullanıcı (ldap sync).
- Traces "+ Column": liste DAR, attribute'lar **faz-2** ile sayfanın ≤50 id'si
  ve gerçek min/max zamanıyla (`idx_trace` bloom) — pencere taraması yok
  (`repo.go:2786-2791`). Tam-pencere taraması **filtre** yolundaydı (P2);
  v0.10.126 yenilik dilimi 2.1 M → 0.6 M satır. Pod/node **filtresi** için
  doğru yol promoted kolon + `set(0)` (v0.9.621-623 emsali), dizi `indexOf` değil.

---

## Engeller — bu tasarımın önünde (sıralı)

| # | Engel | Etkisi | Kim çözer |
|---|---|---|---|
| 1 | **Thanos'ta cluster ayrımı yok** — kod hiçbir cluster etiketi okumuyor; tek querier'da sorgular cluster'ları karıştırır | Entity kimliğinin birinci bileşeni üretilemez | tasarım (label eşleme + her seçiciye matcher) + operatör probe A2(b) |
| 2 | Remote Cluster kaydında **id yok**, `Name` = join anahtarı; (b)/(c) eşleme alanı yok | Kök kimlik ad değişiminde kırılır; (a)≠(c) sessiz boşluk | tasarım A3; geriye dönük doldurma |
| 3 | **k8sattributes prod'da kapalı**; prod attribute seti elle downward API → **uid, deployment, container YOK** | uid birincil anahtar olamaz; workload/container yalnız Thanos'tan | operatör kararı (açma) — tasarım kapalıyken de çalışır |
| 4 | Prod dış Distributed CH: chstore ALTER/MV `spans_local`'a inmez; `cluster` kolonunun prod'da var olup olmadığı belirsiz | Promoted kolon + `entity_seen` MV **operatör migration'ı** ister; kolon yoksa dizi türetimi 66× byte | probe E + `migrations/00NN` |
| 5 | `metric_points`'te namespace 0/101 | pod↔servis köprüsü `(cluster, host_name)`; namespace catalog'dan | tasarım (kabul) |
| 6 | Filo doluluğu **ölçülmedi** (n=1) | doluluk düşük çıkarsa entity_seen boş kalır | operatör: K8s Coverage ekranı / probe B5 |
| 7 | `host.name == pod adı` sözleşme değil | pod↔servis köprüsü kırılgan | tasarım: `k8s.pod.name` önce, `host_name` yedek |
| 8 | Prod erişimi bu makineden yok | tüm prod kanıtları operatör koşumu | operatör: `scripts/probe/entity-discovery-prod.sh` |
| 9 | k8sattributes açılışı collector restart + wedge riski | ayrı bakım penceresi | operatör |

**Fizibilite hükmü:** uygulanabilir — **koşullu**. Prod span'leri
cluster/namespace/pod/node'u bugün taşıyor, Thanos owner zinciri kodda var;
eksik parçalar (cluster etiketi eşlemesi, `kube_pod_info` okuma, promoted
kolonlar) tasarım işi. Tasarımı belirleyen tek bilinmez **A2(b)**: probe
çıktısı gelmeden AŞAMA 2'de external label modeli **varsayım olarak** yazılır
(varsayılan etiket adı `cluster`, değer = `Name`) ve alan konfigüre edilebilir
tutulur.

## Ek — prod probe paketi

`scripts/probe/entity-discovery-prod.sh` — salt-okunur (SELECT + Thanos GET),
`THANOS_URL/THANOS_TOKEN/CH/WIN` ile; bölümler A2, B5-B7, B(metrik), C8, C9,
C11, D12-D14, E (prod `spans_local` kolonları). SQL kısmı lokal CH'de koştu.
