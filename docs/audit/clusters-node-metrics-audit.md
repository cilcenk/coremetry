# Audit — /clusters: node CPU/memory/sayı (dar kapsam)

**Tarih:** 2026-07-17 · **Durum:** ONAY BEKLİYOR — implementasyon yok
**Kapsam:** node CPU, memory, sayı. Kapasite kolonu / health / disk /
network YOK. Bu doküman clusters-requests-nodes-audit.md'nin node
bölümünü (§4-5) DARALTARAK geçersiz kılar; pod-request dilimi oradan
v0.8.580 olarak çıktı.

## 1. Doğrulama durumu — probe'lar yine SENDE

`system_settings.thanos_clusters` hâlâ boş (yeniden kontrol edildi) →
kayıtlı URL+token yok, probe buradan koşulamaz. Hiçbir node sorgusu
"çalışır" varsayılmıyor; §5 probe seti sonuç dönene kadar dilim 2-4
bekler. İki doğrulama sorusu ve tasarımın iki sonuca da dayanıklılığı:

**(1) node-exporter erişimi:** `node_cpu_seconds_total` /
`node_memory_*` platform-monitoring (cluster-monitoring) kapsamıdır.
Mevcut token `cluster-monitoring-view` rolündeyse ana thanos-querier
route'unda (9091) erişilebilir OLMALI ama teyitsiz; token tenancy
portuna (9092, namespace-zorlamalı) bağlıysa node serileri BOŞ döner
— o durumda çözüm ayrı kimlik değil, ana route (platform ekibi
sorusu). Probe A/B ayırır.

**(2) instance ↔ node adı:** node-exporter serileri `instance`
label'ı taşır (tipik `<ip>:9100`), kube-state-metrics `node` label'ı
(gerçek node adı). DAR KAPSAM bu join'i ZORUNLU olmaktan çıkarıyor:
CPU/mem/sayının tamamı node-exporter ailesinden geliyor, `instance`
tek başına satır anahtarı. `kube_node_info` (labels: node,
internal_ip) yalnız GÖRÜNEN ADI güzelleştirmek için best-effort join
— erişilemezse satır adı `10.0.1.5:9100` kalır (kabul; probe D
gerçek label şeklini gösterecek, hostname dönüyorsa join'e hiç gerek
kalmaz).

## 2. Sorgular — pod deseninin node-scope aynası (promql.go)

```promql
# ZORUNLU (3 sorgu):
nodeCPUQuery:      sum by (instance) (rate(node_cpu_seconds_total{mode!="idle"}[5m]))
nodeMemTotalQuery: sum by (instance) (node_memory_MemTotal_bytes)
nodeMemAvailQuery: sum by (instance) (node_memory_MemAvailable_bytes)
# BEST-EFFORT (2 sorgu):
nodeCPUCountQuery: count by (instance) (node_cpu_seconds_total{mode="idle"})  # çekirdek sayısı (CPU% paydası)
nodeInfoQuery:     kube_node_info                                             # instance→node adı güzelleştirme
```

- MemBytes(used) = Total − Avail; MemPct = used/Total (payda AYNI
  zorunlu aileden — kube-state-metrics bağımlılığı yok). CPUPct =
  usedCores/coreCount, yalnız best-effort sayım dönerse; dönmezse 0
  (HostRow.MemPct "0 = bilinmiyor" sözleşmesi).
- **topk kararı:** KALIR — `topk(500, ...)` node ölçeğinde neredeyse
  hiç tetiklenmez (yüzlerle node) ama bedava, deseni tekdüze tutar ve
  yanlış-etiketli bir tenancy'de (instance'a container serisi sızmış)
  8MB gövde kalkanından önce davranır. `nsMatcher` UYGULANMAZ
  (node namespace'siz) — nodeInfoQuery hariç hepsi sabit-string,
  escapeLabelValue'ya girdi yok.
- Değişken maliyet yok: cluster başına sabit 5 sorgu, node
  sayısından bağımsız (pod tarafındaki 6 sorgunun kardeşi).

## 3. NodeMetrics + NodeRow + endpoint — PodMetrics'in birebir aynası

```go
type NodeRow struct {
    Cluster  string  `json:"cluster"`
    Node     string  `json:"node"`     // kube_node_info eşleşirse adı, yoksa instance
    CPUCores float64 `json:"cpuCores"` // kullanılan çekirdek
    MemBytes float64 `json:"memBytes"` // kullanılan (Total-Avail)
    CPUPct   float64 `json:"cpuPct,omitempty"` // çekirdek sayısı best-effort döndüyse
    MemPct   float64 `json:"memPct,omitempty"`
}
func (s *Service) NodeMetrics(ctx context.Context, c ClusterConfig) ([]NodeRow, error)
```

- Composite key yalnız `instance` (pod'daki ns+pod ikilisinin tekli
  hali); acc struct + best-effort continue döngüsü + "yalnız-info"
  gürültü satırı eleme (cpu==0 && memTotal==0 → skip) — PodMetrics
  satır satır aynı akış. `kube_node_info` eşleşmesi: internal_ip ==
  instance'ın port'suz hali → Node = info.node.
- **Endpoint:** `GET /api/clusters/nodes?cluster=X` —
  getClusterPods'a paralel: aynı 10s deadline, serveCached, key
  `cluster-nodes:<ad>:<digest>`; digest'e namespaceFilter GİRMEZ
  (node sorgularını etkilemiyor) → sade `fnv(URL)`. TTL 60s.
  Node SAYISI ayrı sorgu/alan DEĞİL — `count` alanı yanıt zarfında
  `len(rows)` olarak gelir (pods zarfıyla simetrik), frontend satır
  sayısından da türetebilir.
- Testler: mevcut fakeQuerier'la NodeMetrics merge + core-count'suz
  pct=0 + info-join + 401 yüzeylemesi; pod testleri DOKUNULMAZ.

## 4. Frontend yerleşimi

| Seçenek | Artı | Eksi |
|---|---|---|
| **Tab-strip "Pods \| Nodes" (ÖNERİLEN)** — `?tab=nodes`, evin `.tab-strip` anatomisi | Node tablosu kendi useDataTable'ıyla (storageKey 'clusternodes') sortable/resizable; sayı tab etiketinde bedava ("Nodes (12)" — satır sayısından, ek sorgu YOK); ileride kolon eklemeye (disk/network — kapsam dışı ama kapı açık) tek yer | Bir tık uzakta |
| Cluster özet şeridi (pod tablosu üstünde inline) | Sıfır tık | /clusters'ta "cluster satırı" yok (satırlar pod) — özet ancak çip olur, node BAŞINA veri gösteremez; sayı+toplamla sınırlı kalır, sıralama/resize kaybedilir; dar kapsam bile 4 kolon istiyor |

Öneri: **tab**. Ek olarak "All clusters" görünümünde hata-çipi
şeridine dokunulmaz (node fetch'i yalnız Nodes tab'ı açıkken —
fetch-on-open disiplini; pod akışına sıfır ek istek).

## 5. Probe seti (kayıtlı cluster oluşunca — runbook'a adım olarak da girecek)

```bash
TOK=<token>; HOST=<settings'teki URL>
probe() { curl -sk -H "Authorization: Bearer $TOK" \
  "$HOST/api/v1/query?query=$1" | head -c 200; echo; }
probe 'count(node_cpu_seconds_total)'            # A: node-exporter erişimi
probe 'count(node_memory_MemAvailable_bytes)'    # A2
probe 'topk(1,node_memory_MemTotal_bytes)'       # D: instance label şekli (ip:port mu hostname mi)
probe 'count(kube_node_info)'                    # E: ad-güzelleştirme join'i mümkün mü
```

| Sonuç | Yol |
|---|---|
| A,A2 ✓ | §2 aynen; D hostname ise info-join gereksiz, E ✗ ise satır adı instance kalır |
| A ✗ (boş/403), pod metrikleri ✓ | Token tenancy portunda → ana route (9091) platform ekibinden istenir; kod değişmez, URL değişir |

## 6. Dilimleme (onaya sunulan)

1. **(Sende)** §5 probe'ları — cluster'ı Settings'e girip 4 satırı koş.
2. promql node sorguları + NodeMetrics + NodeRow + testler — ~45 dk
3. /api/clusters/nodes + tipler/client — ~20 dk
4. Nodes tab'ı (tab-strip + useDataTable + sayı etiketi) — ~45 dk

2-4 probe SONRASI; her dilim kendi tag'i.
