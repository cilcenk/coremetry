# Audit — /clusters: pod request bilgisi + node/cluster-seviyesi metrikler

**Tarih:** 2026-07-17 · **Durum:** ONAY BEKLİYOR — implementasyon yok
**Kapsam:** (1) pod utilizasyonuna request ekseni, (2) genişletilebilir
node sinyalleri. Node işi PROBE SONUCUNA KOŞULLU (aşağıda).

## 1. Mevcut durum — teyit

| İddia | Durum |
|---|---|
| CPUPct/MemPct limit'e göre hesaplanıyor; request sorgulanmıyor | ✅ client.go:429-434 (`a.cpuLim/a.memLim` → clampPct); promql.go'da yalnız `kube_pod_container_resource_limits` (satır 70) — `_requests` hiçbir yerde yok |
| Tek-sorgu-per-sinyal + topk(500) + nsMatcher deseni | ✅ promql.go — node sorguları da aynı şekle uyarlanacak (§4) |
| Bağlantı "oauth-proxy'd thanos-querier route" | ✅ client.go:46 (config yorumunda tipik desen olarak) — platform+UWM birleşimi olup olmadığı TEYİTSİZ, probe'a bağlı |
| Runbook cluster-monitoring-view SA'sı anlatıyor | ✅ docs/runbooks/thanos-clusters.md §1 — node metrikleri için ek kimlik GEREKMEYEBİLİR, probe karar verir |

## 2. ⚠️ Probe'lar BU KURULUMDA KOŞULAMADI — sende

`system_settings.thanos_clusters` bu kurulumda hiç yazılmamış (blob
yok) → configured URL+token olmadan probe imkânsız. §4/§8 probe
disiplini bozulmadı: hiçbir node sorgusu "çalışır" varsayılmıyor;
aşağıdaki set prod token'ınla koşulacak ve **dilim 3-4 sonuca koşullu.**

```bash
TOK=<token>; HOST=https://<thanos-host>
probe() { curl -sk -H "Authorization: Bearer $TOK" \
  "$HOST/api/v1/query?query=$1" | head -c 200; echo; }

# A) kube-state-metrics node ailesi (kapasite + sağlık):
probe 'count(kube_node_status_capacity)'
probe 'count(kube_node_status_condition{condition="Ready"})'
# B) node-exporter ham serileri:
probe 'count(node_cpu_seconds_total)'
probe 'count(node_memory_MemAvailable_bytes)'
# C) kube-prometheus kayıt kuralları (varsa B'den temiz):
probe 'count(instance:node_cpu_utilisation:rate5m)'
probe 'count(instance:node_memory_utilisation:ratio)'
# D) node-exporter 'instance' etiketi node ADI mı IP mi? (join şekli için)
probe 'topk(1,node_cpu_seconds_total)'   # metric.instance değerine bak
```

Karar tablosu:

| Sonuç | Yol |
|---|---|
| A ✓ ve (B veya C) ✓ | Standart yol — §4 tasarımı aynen; ek kimlik/route GEREKMEZ (cluster-monitoring-view platform metriklerinin tamamını okur) |
| A ✓, B/C ✗ | Kapasite+sağlık gösterilir, kullanım gösterilmez (parçalı sürüm) — muhtemel neden tenancy-port (9092, namespace-zorlamalı); çözüm ana route (9091) — platform ekibi sorusu |
| Hepsi ✗ ama pod metrikleri ✓ | Token tenancy portuna bağlı → node işi ana route + aynı rol ile mümkün; runbook'a route notu eklenir |
| D: instance = IP:port | node_exporter↔kube_node join'i `label_replace`/kayıt kuralı ister — C ✓ ise sorun yok, C ✗ ise B sorguları `instance` anahtarıyla kalır ve satır adı node adı yerine instance olur (kabul edilebilir, not düşülür) |

## 3. Pod request ekseni (probe'a KOŞULSUZ — mevcut best-effort sözleşmesi korur)

- `podRequestQuery(resource, nsFilter)` — podLimitQuery'nin birebir
  kardeşi (`kube_pod_container_resource_requests`); PodMetrics'in
  best-effort limit döngüsüne 2 sorgu daha eklenir (cluster başına
  4→6 sabit sorgu; hâlâ pod sayısından bağımsız). Seri yoksa alanlar
  0 kalır — kurulu "0 = bilinmiyor" sözleşmesi.
- PodRow'a `CPUPctOfReq`/`MemPctOfReq` (omitempty) eklenir; mevcut
  Limit-bazlı CPUPct/MemPct AYNEN KALIR (anlamları farklı: limit'e
  yakınlık = throttle/OOM riski; request'e oran = provisioning
  isabeti). İkisi yan yana yaşar.
- **UI önerisi:** liste tablosuna YENİ kolon eklemek yerine mevcut
  CPU%/Mem% hücrelerinin title'ı zenginleşir ("of limit 50% · of
  request 120%") + drawer'a tam kırılım satırı (usage / request /
  limit). Gerekçe: tablo 7 kolon, /clusters'ta kolon gizleme yok;
  "aşırı-provizyon avı" sıralaması istenirse sortable "%req" kolonu
  tek satırlık ekleme — ayrı karar olarak işaretli.

## 4. Node sinyalleri — genişletilebilir soyutlama (probe SONRASI)

promql.go'ya pod'lardan bağımsız node-scope builder ailesi:

```go
// nodeSignal — bir node-seviyesi sinyalin sorgusu + hangi label'ın
// node kimliği olduğu. Yeni sinyal (disk, network) = bu tabloya bir
// satır; parser/merge/endpoint değişmez.
type nodeSignal struct {
    ID       string // "cpuUsedCores", "memUsedBytes", "cpuCapacity", ...
    Query    string // probe'un doğruladığı varyant (§2 karar tablosu)
    KeyLabel string // "node" (kube_node_*) | "instance" (node_exporter)
}
```

- nsMatcher UYGULANMAZ (node namespace'siz); topk kalkanı kalır
  (topk(500) — node sayısı yüzlerle sınırlı, kalkan bedava).
  escapeLabelValue sabit-değer matcher'larda aynen.
- Aday sorgular (probe hangisini onaylarsa):
  - Kullanım: C varsa `instance:node_cpu_utilisation:rate5m` /
    `instance:node_memory_utilisation:ratio`; yoksa B ham:
    `sum by (instance) (rate(node_cpu_seconds_total{mode!="idle"}[5m]))`,
    `node_memory_MemTotal_bytes - node_memory_MemAvailable_bytes`.
  - Kapasite: `kube_node_status_capacity{resource="cpu"|"memory"}`.
  - Sağlık: `kube_node_status_condition{condition="Ready",status="true"}`.
- `NodeRow {Cluster, Node, CPUUsedCores, CPUCapacityCores, CPUPct,
  MemUsedBytes, MemCapacityBytes, MemPct, Ready bool}` — Pct'ler
  KAPASİTEYE oran (node'da limit kavramı yok).
- **Endpoint:** `GET /api/clusters/nodes?cluster=X` — pod uçlarından
  AYRI (namespace filtresi node key'ine girmez → digest'i sade:
  yalnız URL); serveCached, key `cluster-nodes:<ad>:<url-digest>`,
  TTL 60s; handler'da aynı 10s deadline. Fan-out yine istemcide.
- Testler: mevcut fakeQuerier ile nodeSignal merge + Ready çözümü +
  capacity-eksik best-effort; pod testlerine DOKUNULMAZ.

## 5. Frontend yerleşimi — öneri: tab-strip "Pods | Nodes" + sağlık çipi

| Seçenek | Değerlendirme |
|---|---|
| **Tab-strip (ÖNERİLEN):** /clusters'ta `.tab-strip` (evin tek tab anatomisi) — Pods / Nodes; `?tab=nodes` URL'de | Node tablosu kendi useDataTable'ıyla (storageKey 'clusternodes') tam sıralanabilir/resize; gelecekte disk/network kolonları buraya büyür; pod görünümü sıfır değişir |
| Cluster satırında inline özet | /clusters'ta "cluster satırı" yok (satırlar pod) — özet ancak üst çip şeridine sıkışır, node başına detay veremez |
| Ayrı sayfa /nodes | Menü kalabalığı; cluster bağlamından kopar — reddedildi |

Ek: tab'dan bağımsız, mevcut hata-çipi şeridine cluster başına
"N/M ready" sağlık çipi (Nodes verisi geldiyse; NotReady>0 → b-err).
"All clusters" görünümünde ilk bakışta hangi cluster'ın node derdi
olduğu görülür.

## 6. Kısıt teyitleri

- Pod sorgu/merge yolu, cache key'leri, mevcut 11 test DOKUNULMAZ
  (request sorguları yalnız best-effort döngüsüne eklenir; test
  dosyasına yeni case'ler gelir, mevcutlar değişmez).
- Node kodu probe onayı gelmeden YAZILMAZ (§2 karar tablosu boş
  kaldıkça dilim 3-4 bekler).

## 7. Dilimleme (onaya sunulan)

1. **(Sende)** §2 probe seti — sonuçları yapıştırman yeterli.
2. Pod request ekseni: podRequestQuery + PodRow alanları + tooltip/
   drawer kırılımı + testler — ~45 dk (probe'dan bağımsız).
3. nodeSignal soyutlaması + /api/clusters/nodes + testler — ~1 saat
   (probe sonrası, sorgu seti karar tablosundan).
4. Frontend: tab-strip + Nodes tablosu + "N/M ready" çipleri — ~1 saat.

Her dilim kendi tag'i; 2. dilim probe beklemeden çıkabilir.
