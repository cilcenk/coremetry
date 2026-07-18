# OpenShift cluster/namespace attribute audit — 2026-07-18

Operatör talebi: "mapping'ler k8s.namespace.name'e göre; openshift.cluster.name
ya da OpenShift-spesifik daha doğru olurdu, yedekli yapı daha iyi" (filo ağırlıkla
OpenShift). Bu doküman mevcut durumu haritalar; standing directive gereği
implementasyon onay kapılı (B1 hariç — net doğruluk bug'ı, talebin kendisi).

## Zemin — ZATEN OpenShift-farkındalı ve yedekli olanlar

| Yüzey | Zincir | Yer |
|---|---|---|
| Span cluster kimliği (merkez) | `k8s.cluster.name → openshift.cluster.name → cluster` (önce res, sonra attr; 6 permütasyon) | `clusterDeriveExpr` repo.go:294 |
| Spans materialized `cluster` kolonu | Aynı ifade insert-time hesaplanır; eski partlar canlı derive'a düşer | store.go:554 + `clusterColExpr` repo.go:314 |
| CH logs cluster | Aynı üçlü coalesce | `chLogsClusterExpr` logstore/clickhouse.go:117 |
| ES logs cluster | `openshift.labels.cluster → openshift.cluster.name → kubernetes.cluster.name → k8s.cluster.name → …` (7 yol) | elasticsearch.go:2473 |
| ES fields paneli | `openshift.*` prefix'i tanınır | elasticsearch.go:2630 |

Sonuç: servis→cluster eşlemesi (`GetServiceClusterMap`, `serviceClusters`,
problems zenginleştirme, Clusters çipleri) OpenShift'te BUGÜN doğru çalışır.

## Bulgular

### B1 — pod↔servis eşleşmesinin cluster filtresi zinciri BYPASS ediyor (BUG)
`podServiceMapSQL` (podservice.go:28) yalnız
`res_values['k8s.cluster.name']` okur — openshift/cluster yedeği YOK, attr
yedeği YOK, metric_points'te materialized kolon da yok. Yalnız
`openshift.cluster.name` basan bir OpenShift cluster'ında filtre hiçbir satır
eşleştirmez → Clusters pod tablosunun Service kolonu ve Service→Infra
korelasyonu sessizce boş kalır. **Fix: aynı 6-yollu coalesce inline** (bu
dokümanla birlikte v0.9.52'de uygulandı; maliyet: 15dk pencere + LIMIT 5000
taramasında satır başına ≤6 indexOf — kabul edilebilir, ölçüldü).

### B2 — namespace/deployment deriver'ına OpenShift yedeği (ONAY + KANIT KAPILI)
Deriver zinciri: namespace = `service.namespace → k8s.namespace.name`
(res→attr), deployment = `k8s.deployment.name`. OpenShift DE Kubernetes'tir —
k8sattributes processor'lı standart collector bu anahtarları basar; yani
OpenShift'te bu eşleme YANLIŞ DEĞİL. Risk: OTel-dışı/eski agent'lar
(fluentd-tarzı `kubernetes.namespace_name`, `openshift.labels.*`) span
resource'una farklı anahtar yazıyorsa deriver boş kalır. ES log zinciri bu
varyantları zaten tanıyor (emsal) — ama SPAN tarafında prod kanıtı yok.
**Önce prod'da doğrula** (aşağıdaki sorgu), kanıt gelirse zincire
`kubernetes.namespace_name` / `openshift.project.name` eklenir:

```sql
SELECT k, count() FROM (
  SELECT arrayJoin(res_keys) AS k FROM spans
  WHERE time >= now() - INTERVAL 1 HOUR
) WHERE k ILIKE '%namespace%' OR k ILIKE '%openshift%' OR k ILIKE '%project%'
GROUP BY k ORDER BY count() DESC LIMIT 20
```

### B3 — Thanos/KSM tarafı (BİLGİ, değişiklik gerekmez)
Clusters + Service→Infra sekmesinin PromQL'leri Prometheus **label**'larıyla
çalışır (`namespace`, `pod`, `deployment`) — bunlar kube-state-metrics/cadvisor
label'larıdır ve OpenShift'te birebir aynıdır. Resource attribute dünyasından
bağımsız; OpenShift-spesifik değişiklik gerekmez.

## Karar

- B1: v0.9.52'de düzeltildi (operatör talebinin kendisi).
- B2: operatör yukarıdaki sorguyu prod CH'de koşup çıktıyı paylaşınca
  zincir genişletilir (spekülatif anahtar eklemiyoruz — dürüstlük).
- B3: kapalı.
