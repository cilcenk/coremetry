# Runbook — Uzak cluster (Thanos) pod metrikleri

/clusters sayfasını yeni bir OpenShift cluster'ına bağlama adımları
(v0.8.575-579; tasarım: docs/audit/thanos-multicluster-metrics-audit.md).

## 1. Cluster tarafında (platform ekibi / cluster-admin)

```bash
# Salt-okunur SA + monitoring-view rolü + token:
oc -n <ns> create sa coremetry-metrics-reader
oc adm policy add-cluster-role-to-user cluster-monitoring-view \
  -z coremetry-metrics-reader -n <ns>
oc -n <ns> create token coremetry-metrics-reader --duration=8760h
# Thanos Querier route'u:
oc -n openshift-monitoring get route thanos-querier -o jsonpath='{.spec.host}'
```

Not: `create token` süreli token üretir — süre dolduğunda Settings'ten
yenilenmeli. Süresiz istenirse platform ekibinden SA token Secret'ı
(legacy) talep edilir.

## 2. Metrik mevcudiyeti probe'ları (varsaymadan doğrula — TEK LİSTE)

UI, veri gelmeyen HER ekseni kendiliğinden gizler ("—" ya da bölüm
hiç görünmez) — probe'lar yalnız "hangi eksenler görünür olacak"
sorusunu cevaplar. Hepsi bir arada:

```bash
TOK=<token>; HOST=https://<thanos-host>
probe() { curl -sk -H "Authorization: Bearer $TOK" \
  "$HOST/api/v1/query?query=$1" | head -c 200; echo; }

# ZORUNLU eksen — pod CPU/mem (cAdvisor):
probe 'count(container_cpu_usage_seconds_total)'
# Pod limit/request yüzdeleri + threshold çizgileri (kube-state-metrics):
probe 'count(kube_pod_container_resource_limits)'
probe 'count(kube_pod_container_resource_requests)'
# Node sekmesi (node-exporter; tenancy-port'ta boş dönerse ana route gerekir):
probe 'count(node_cpu_seconds_total)'
probe 'count(node_memory_MemAvailable_bytes)'
probe 'topk(1,node_memory_MemTotal_bytes)'      # instance label şekli (ip:port mu ad mı)
probe 'count(kube_node_info)'                   # node adı güzelleştirme join'i
# Network ekseni (Overview kartları/grafiği + Net in/out kolonları):
probe 'count(container_network_receive_bytes_total)'   # pod net
probe 'count(node_network_receive_bytes_total{device!="lo"})'  # node/cluster net
# (İleride) deployment rollup'ı — şimdilik plan iskeleti, kod yok:
probe 'count(kube_pod_owner{owner_kind="ReplicaSet"})'
```

Okuma: `"status":"success"` + sıfırdan büyük değer = eksen hazır.
Pod metrikleri ✓ ama node/net aileleri boşsa token büyük ihtimalle
tenancy portuna (9092) bağlı — çözüm ayrı kimlik değil, ana
thanos-querier route'u (platform ekibi sorusu).

## 3. Coremetry tarafında

1. **Settings → Remote clusters → + Add cluster** (admin):
   - **Name**: cluster'ın telemetriye yazdığı `k8s.cluster.name` /
     `openshift.cluster.name` değeri — alan gözlenen adları önerir;
     "telemetride görülmüyor" rozeti çıkıyorsa ad servis sayfalarıyla
     EŞLEŞMEYECEK demektir (cluster henüz telemetri göndermiyorsa
     normal, sonra kendiliğinden düzelir).
   - **URL**: adım 1'deki thanos-querier host'u (https://...).
   - **Token**: adım 1'deki token (bir daha gösterilmez; boş bırakılan
     düzenlemeler saklı token'ı korur).
   - **Namespace filter**: PromQL regex — kardinalite kalkanı; koca
     cluster'da uygulama namespace'lerinize daraltın (örn.
     `^(app-|payments-)`). Boş = tüm namespace'ler, top 500 pod.
2. **/clusters** sayfası: cluster seçici + pod tablosu ≤3sn dolmalı.
   Satır tıklaması dakikalık CPU/Memory trendini açar.
3. **Pivot**: çok-cluster'lı bir servisin sayfasındaki "Per-cluster
   breakdown" tablosunda eşleşen cluster satırında "pods →" belirir —
   tıklayınca /clusters o cluster + servisin namespace'iyle açılır.

## 4. Sorun giderme

| Belirti | Neden / çözüm |
|---|---|
| Cluster çipi "unreachable" | URL/route erişimi, token süresi (adım 2 curl'üyle ayır); self-signed cert'te "Skip TLS verify" |
| Tablo boş, hata yok | Namespace filtresi hiç pod'la eşleşmiyor VEYA metrik adları tenancy'de yok (adım 2) |
| CPU%/Mem% hep "—" | kube-state-metrics limits serisi yok — kozmetik, istenirse platforma sorulur |
| "pods →" görünmüyor | Settings'teki ad ≠ telemetrideki cluster adı (rozet uyarısına bak) |
| Servis sayfası pivot'u namespace'siz | Servisin metadata namespace'i türetilmemiş (SDK `service.namespace` / k8s resource attr eksik) |

Maliyet notu: cluster başına 4 sabit PromQL sorgusu / 60sn (sunucu
cache TTL'i); pod sayısından bağımsız. Thanos'a yazma yoktur.
