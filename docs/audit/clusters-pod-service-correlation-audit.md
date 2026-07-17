# Audit — /clusters pod'larını Coremetry servisleriyle ilişkilendirme (host_name)

**Tarih:** 2026-07-17 · **Durum:** ONAY BEKLİYOR — implementasyon yok
**Hedef:** (a) pod satırına "hangi servis" etiketi, (b) Servis→Cluster
pivotunu namespace'ten pod-seviyesine yükseltmek.

## 1. Zemin doğrulaması (canlı CH, son 30dk, 101 servis)

| Soru | Bulgu |
|---|---|
| host_name ↔ Thanos pod label aynı string mi? | **Lokalde EVET, birebir:** 99/101 serviste `host_name == res(k8s.pod.name)` karakteri karakterine (örnek: `aisapi-prod-2-r3816`). ⚠️ Lokal veri DEMO filosu (attr'ları demo üretir) — **gerçek prod SDK+collector zinciri için §5'teki probe SQL'i operatörce koşulmalı.** Normalize ihtiyacı çıkarsa (FQDN'li host.name: `pod.ns.svc.cluster.local`) ilk-nokta-öncesi kırpma fonksiyonu hazır tutulacak — bugün gerek YOK. |
| k8s.pod.name doluluk (ikincil anahtar olur mu?) | metric_points: 99/101, spans: 98/100. İkincil DOĞRULAMA anahtarı olarak uygun ama gerekmiyor: host_name (100/101) tek başına yeterli ve zaten ayrı kolonda (indexli erişim). k8s.pod.name yalnız probe SQL'inde tutarlılık kontrolü olarak kullanılacak. |
| **EK BULGU — namespace anahtarı metric tarafında YOK** | `k8s.namespace.name` spans'ta 98/100 dolu ama **metric_points'te 0/101** (ingest metrik yolunda res attr'ları aynen yazılıyor; SDK'ler namespace'i metric resource'una koymuyor). Sonuç: eşleştirme (cluster, namespace, host_name) üçlüsüyle DEĞİL, **(cluster, host_name) ikilisiyle** yapılmalı; namespace ayrıştırması gerekirse ServiceMetadata.namespace (v0.8.436 deriver, servis-seviyesi) devreye girer. Çakışma riski pratikte sıfıra yakın: pod adları replicaset hash'i taşır. |
| k8s.cluster.name metric_points'te | 99/101 dolu → cluster conjunct'ı güvenilir. |
| Dosya iddiaları | ✅ Hepsi: ServiceInstance.ID=host_name (service_instances.go, LIMIT 200 + max_execution_time=10 şekli); store.go:2386 "host.name = k8s pod name" yorumu; convert.go üç sinyalde de yalnız host.name'i ayrı kolona kaldırıyor (38/164/221); k8s.pod.name yalnız generic res_values okumalarında (deploys.go:355, endpoints_detail.go:411); pivot v0.8.579'da yalnız ?namespace=. |

## 2. Tasarım

### 2.1 chstore.PodServiceMap — tek bounded sorgu

```go
// PodServiceMap returns host_name → candidate service names for
// pods that emitted metrics in the window, optionally scoped to
// one cluster (res k8s.cluster.name). GetHosts precedent: whole-
// window metric_points scan, service prefix'siz — bu yüzden pencere
// ≤15dk tutulur (canlı pod eşleşmesi için yeter) + LIMIT 5000.
func (s *Store) PodServiceMap(ctx context.Context, cluster string, from, to time.Time) (map[string][]string, error)
```

```sql
SELECT host_name, groupUniqArray(3)(service_name) AS services
FROM metric_points
WHERE time >= ? AND time <= ? AND host_name != ''
  AND (? = '' OR res_values[indexOf(res_keys,'k8s.cluster.name')] = ?)
GROUP BY host_name
LIMIT 5000
SETTINGS max_execution_time = 10
```

- `groupUniqArray(3)`: tek pod'a birden çok servis yazan nadir durum
  (sidecar/agent) Go tarafında çözülür; 3 aday yeter.
- ORDER BY (service_name, metric, time) prefix'i cluster'a yardım
  etmez — kabul: GetHosts aynı şekli ≤6h pencerede LIMIT 2000 ile
  bütçe içinde koşuyor; burada pencere 15dk.
- Çözümleme (api katmanında): tek aday → o; birden çok aday →
  ServiceMetadata.namespace'i Thanos satırının namespace'iyle eşleşen
  aday; hâlâ belirsiz → eşleşmemiş say (yanlış etiket basmaktansa boş).

### 2.2 Handler zenginleştirmesi — cache stratejisi DEĞİŞMEZ

- `thanos.PodRow`'a `Service string \`json:"service,omitempty"\`` eklenir
  (frontend `ClusterPodRow.service?: string`).
- getClusterPods'un **serveCached fn'i İÇİNDE**: PodMetrics sonrası
  `PodServiceMap(ctx, cluster, now-15m, now)` + (yalnız çok-adaylı pod
  varsa) cached metadata okuması; satırlar etiketlenir. Key/TTL aynı
  (60s) — eşleşme verisi de aynı yaşam süresini paylaşır, ek cache
  katmanı yok. CH sorgusu Thanos çağrılarına ~ms ekler.
- getClusterPodDetail: drawer başlığına aynı etiketi tek-host
  varyantıyla ekler (`WHERE host_name = ?` + aynı bound'lar).

### 2.3 Pivot: namespace → pod-seviyesi (öneri: ?service= parametresi)

| Yaklaşım | Değerlendirme |
|---|---|
| **?service=<ad> (ÖNERİLEN)** | Pod listesi zaten 2.2 ile servis etiketli — /clusters `?service=` okur, satırları etiket üzerinden süzer. URL kısa, kalıcı (pod adları redeploy'da değişir, servis adı değişmez — kaynak-of-truth kalitesi), "pods →" linki `/clusters?cluster=X&service=svc&namespace=ns` üretir. ?namespace= aynen kalır (geriye uyum + eşleşmemiş pod'ları da görme yolu); ikisi bileşke süzer. |
| ?pods=a,b,c listesi | ServiceInstances'tan üretilebilir ama: URL şişer (200 instance cap'i), pod churn'üyle dakikada bayatlar, paylaşan link ertesi gün boş süzer. Reddedildi. |

Çift yönlü döngü: pod satırındaki servis etiketi `/service?name=`
linki olur (Hosts.tsx'in services hücresi emsali) — cluster→servis
yönü de kapanır.

### 2.4 Eşleşmeyen pod'lar (instrument edilmemiş / infra / sidecar)

- Servis hücresi: soluk `—` + title "Coremetry'ye telemetri
  göndermiyor (instrument edilmemiş servis ya da infra pod'u)".
  Sessiz boş DEĞİL (hücre var, kasıtlı boş olduğu okunur), ayrı
  "bilinmeyen iş yükü" rozeti de DEĞİL (100 infra pod'luk cluster'da
  rozet gürültüsü olur).
- `?service=` süzgeci aktifken eşleşmeyenler doğal olarak elenir;
  `?namespace=`-yalnız görünümde görünür kalırlar (operatör "bu
  namespace'te instrument edilmemiş ne var"ı da bu sayede görür —
  bilinçli özellik).
- Opsiyonel (ayrı karar): tablo üstüne "N/M pod instrumented" özet
  çipi — bu iterasyona ALINMADI, istenirse tek satır.

## 3. Kapsam dışı / dokunulmayan

- metric_points şeması (kısıt) — eşleştirme tamamen okuma-anı.
- ServiceInstances, mevcut /clusters davranışı, v0.8.579 pivot
  parametresi (?namespace= aynen çalışır).
- Namespace attr'ını metric resource'una ekletme (collector
  k8sattributes önerisi) — runbook notu olabilir, kod işi değil.

## 4. Dilimleme (onaya sunulan)

1. chstore.PodServiceMap + tablo-testi (sorgu şekli + çözümleme saf
   fonksiyonu) — ~30 dk
2. thanos_handlers zenginleştirme + PodRow.Service + tipler — ~30 dk
3. /clusters ?service= süzgeci + servis hücresi (link + "—") +
   pivot linkine &service= — ~30 dk

Toplam ~1.5 saat, üç ayrı tag.

## 5. Prod doğrulama SQL'i (operatör — implementasyondan bağımsız koşulabilir)

```sql
SELECT uniqExact(service_name)                                   AS total,
       uniqExactIf(service_name, host_name != '')                AS with_host,
       uniqExactIf(service_name, host_name != '' AND
         host_name = res_values[indexOf(res_keys,'k8s.pod.name')]) AS host_eq_pod
FROM metric_points WHERE time > now() - INTERVAL 1 HOUR;
-- host_eq_pod ≈ with_host ise eşleştirme anahtarı prod'da da geçerli.
-- Ek: birkaç host_name'i `oc get pods -A | grep <ad>` ile gözle doğrula.
```
