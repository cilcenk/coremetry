# K8s altyapı entity katmanı — durum denetimi ve aşamalı plan

**Tarih:** 2026-08-26 · **Yöntem:** kod okuması + canlı ölçüm + üç düşmanca sınama
**Kod DEĞİŞTİRİLMEDİ.**

> ⚠ **LOKAL ÖLÇÜMLER FIXTURE.** Lokaldeki her `k8s.*` değeri demo
> üretecinden geliyor (`cmd/demo/main.go:475-500` elle basıyor; tek gerçek
> pod 885 sahte `k8s.pod.name` yayıyor). Prod gerçeği aşağıdaki tabloda
> ve operatörün prod ekranından alındı.

---

## 0. Prod yer gerçeği

Prod span'inde 22 resource attribute var. Belirleyici olanlar:

| VAR | YOK |
|---|---|
| `k8s.node.name` | **`k8s.pod.uid`** |
| `k8s.pod.name` · `k8s.pod.ip` | **`k8s.namespace.name`** |
| `openshift.cluster.name` | `k8s.deployment.name` |
| `host.name` (= pod adı) | `k8s.container.name` |
| `container.image.name` / `.tag` | `k8s.replicaset.name` |

**`host.name` == `k8s.pod.name`** — node bilgisi değil, pod adının tekrarı.
Node ayrıca `k8s.node.name`'de duruyor.

**Kaynak elle yazılmış downward API env listesi.** k8sattributes bu
alanları HEP BİRLİKTE ekler; bu kadar seçici bir küme çıkmaz. Depoda da
hiçbir konfigde yok.

---

## 1. Collector durumu — ölçüldü

`k8sattributes` **hiçbir yerde yok**: ne chart'ta, ne compose konfiglerinde,
ne canlı ConfigMap'te.

```
chart:     processors: [memory_limiter, batch]
distributed: [memory_limiter, resource, probabilistic_sampler, batch]
```

- **ClusterRole/RoleBinding YOK** — chart'ta tek bir RBAC nesnesi bile yok
- otelcol Deployment'ı **`serviceAccountName` set etmiyor** (default SA)
- `examples/openshift/01-rbac.yaml` açıkça: *"No additional roles are bound
  — Coremetry does not call the k8s API"*
- ✅ İyi haber: imaj **`otel/opentelemetry-collector-contrib:0.111.0`** —
  processor binary'de **zaten var**. İmaj değişikliği gerekmiyor,
  salt konfig + RBAC işi.

---

## 2. ⚠ ASIL BLOKÖR uid DEĞİL — terfi zinciri

Düşmanca sınamanın en değerli bulgusu. `internal/otlp/convert.go`:

```go
:262  service.name yoksa → "unknown"
:268  host.name    yoksa → ""
```

**kubeletstats ikisini de yaymaz.** Yani mükemmel kurulmuş bir kubeletstats
bile `service_name='unknown'`, `host_name=''` satırları üretir — ve depodaki
**her** pod-anahtarlı okuma yolu onları eler:

| yol | süzgeç |
|---|---|
| `infra_metrics.go:131` | `WHERE service_name = ?` |
| `service_instances.go:63-66` | `WHERE service_name = ? … AND host_name != ''` |
| `podservice.go:37` | `AND host_name != ''` |

**Sonuç: `k8s.pod.uid` join'i çözülse bile veri hiçbir sayfaya ulaşmaz.**
Bu, uid sorusundan daha derin ve daha erken çözülmesi gereken bir engel.

---

## 3. ⚠ İKİ AYRI HAT — ve dikey eksen ZATEN üretimde

| | HAT A | HAT B |
|---|---|---|
| kaynak | OTLP → spans / metric_points | `internal/thanos`, canlı PromQL |
| taşıdığı | servis telemetrisi + kısmi k8s attr | **(cluster, namespace, pod) envanteri, faz, restart, son-sonlanma sebebi, limitler, node** |
| CH'ye yazıyor mu | evet | **hayır** |
| ne zamandır | — | **v0.8.575'ten beri üretimde** |

**Pod ↔ servis köprüsü HAT B'de VAR ve %98 ölçülmüş.**

Bunun anlamı: *"pod entity'si için önce collector lazım"* önermesi **çürük**.
Dikey eksenin verisi zaten üretimde, yalnız **varlık olarak modellenmemiş**.

`k8sattributes` hâlâ gerekli — ama **uid/node ve kendini beyan etmeyen
bileşenler** için. Prod'da bağlamı olmayan tek şey Coremetry'nin **kendi
self-telemetry'si**; k8sattributes'ın çözeceği sınıf tam orası.

---

## 4. Kardinalite

Lokal (fixture): 79.362 distinct seri. **Ama %85'i tek metrikten:**

```
oracledb.top_sql.elapsed  → 67.566 seri / 78.667 nokta  (0,86 seri/nokta!)
```

Sebep ölçüldü: `executions` attr'ı **43.832 distinct değer** taşıyor — yani
**monoton artan bir sayaç değeri etiket olarak** yayılıyor. Ders kitabı
kardinalite bombası. Kaynak: `cmd/demo/oracle_metrics.go:529`. Demo kodu,
ama aynı desen gerçek bir receiver'da tekrarlanabilir.

**Bomba hariç gerçek seri sayısı: 11.956.**

Modelleme (40 node · 2.500 pod · 3.500 container · 300 iş-yükü):
kubeletstats ~pod 15 + container 11 + node 15 seri → **~80K ek seri**.
Entity tablosu: pod yeniden yaratma hızına bağlı; uid anahtarlıysa her
restart yeni satır.

**Risk sırası:** ① etiket olarak sayaç değeri (yukarıdaki sınıf) →
② pod uid (her restartta yeni) → ③ container id → ④ ephemeral Job/CronJob pod'ları.

---

## 5. Aşamalı plan

### FAZ 0 — "K8s Bağlam Kapsama Kartı" ← **İLK DİLİM**

Yeni `internal/api/k8scoverage.go` + `registerK8sCoverageRoutes(mux)`,
`api.go`'ya tek satır. Tek **örneklemeli** CH sorgusu (`attributeKeysSQL`
kalıbı), `GROUP BY service_name`. Yüzey: servis × (namespace/deployment/
pod/uid/node) var-yok tablosu.

- **Tek başına merge edilebilir:** EVET — hiçbir şeyi beklemiyor
- **Şema/MV/kolon değişikliği:** yok · **collector'a dokunmuyor**
- **Efor:** S · **Geri alınabilir:** tek commit revert
- **Değer:** bugün cevapsız olan *"filonun hangi kısmı k8s bağlamı yayıyor"*
  ölçülür — ve **Faz 3'ün kabul testi** olur (öncesi/sonrası aynı tablo)

> **Neden bu ilk:** Faz 3 prod'da pod bounce istiyor ve collector wedge
> riski taşıyor. O riski ölçülmemiş gerekçeyle almak yanlış sıra.

### FAZ 1 — `pod_seen` MV
`service_seen` (`store.go:3905`) birebir emsali: `minState`/`maxState`.
HAT A'nın pod yaşam döngüsü. **Faz 0'ı beklemiyor**, uid'i de beklemiyor
(pod adı + namespace ile). Efor: S–M.

### FAZ 2 — Terfi zinciri düzeltmesi *(§2'nin blokörü)*
`service_name='unknown'` / `host_name=''` satırlarının okuma yollarında
elenmemesi. **Bu olmadan kubeletstats'ın hiçbir faydası görünmez.**
Efor: M. Faz 4'ün ÖN KOŞULU.

### FAZ 3 — Downward API'ye `pod.uid` + `namespace` ← **operatör tarafı**
```yaml
- name: K8S_POD_UID
  valueFrom: { fieldRef: { fieldPath: metadata.uid } }
- name: K8S_NAMESPACE
  valueFrom: { fieldRef: { fieldPath: metadata.namespace } }
```
Collector'a **dokunmuyor** (wedge riski yok), uygulama deployment'ında tek
satır, geri alınabilir. Kalıcı kimliğin önkoşulu.

### FAZ 4 — k8sattributes + RBAC
ClusterRole (`pods`, `namespaces`, `replicasets`, `nodes` üzerinde
`get/list/watch`) + SA + processor. Deployment/replicaset/container adlarını
ve **kendini beyan etmeyen bileşenlerin** bağlamını getirir.
⚠ Collector restart'ı gerektiriyor.

### FAZ 5 — kubeletstats / k8s_cluster receiver
Faz 2 **ve** Faz 3'ü bekliyor. Pod seviyesi kısa saklama, workload seviyesi
uzun saklama iki ayrı TTL ile.

### FAZ 6 — Entity tabloları + dikey korelasyon
HAT B'yi CH'ye yazmak ve pod→node kenarlarını correlator'a vermek.

---

## 6. Bu denetimin sınırleri

- Prod'da **tek bir span'in** resource seti görüldü; filo geneli kapsama
  ölçülmedi → **Faz 0 tam olarak bunu ölçmek için var**
- HAT B'nin CH'ye yazma maliyeti modellenmedi
- kubeletstats seri sayıları OTel varsayılan `metric_groups` setinden
  türetildi, prod'da ölçülmedi
