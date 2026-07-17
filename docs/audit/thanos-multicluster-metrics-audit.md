# Audit — Thanos Querier'dan çoklu-cluster pod/namespace metrikleri

**Tarih:** 2026-07-17 · **Durum:** ONAY BEKLİYOR — implementasyon yok
**Hedef:** N OpenShift cluster'ının Thanos Querier'ından pod/namespace
CPU+memory çekip /hosts benzeri bir yüzeyde göstermek. Salt okuma;
metric_points şemasına dokunulmuyor; backfill kapsam dışı.

## 1. Kök durum — teyit (paralel doğrulama, 6 okuyucu)

| İddia | Durum |
|---|---|
| serveCached L0 singleflight + L1 in-process + L2 Redis SWR; hosts.go/external.go ince handler deseni | ✅ cache.go:217; hosts key `"hosts:"+cacheBucket(from,to)` TTL **60s**, external TTL 30s; cacheBucket 30s grid (api.go:2515) |
| HostRow/HostServiceRow/HostTrendPoint/HostDetail şekilleri | ✅ hosts.go:24-58 — cpuPct (yüzde), memBytes, memPct (limit yoksa 0), lastSeen ns; trend dakika bucket'lı (unix saniye); pencere ≤6h clamp; kaynak yalnız metric_points |
| Zoom client deseni | ✅ notify.go:1106-1134 — paket-düzeyi tekil client (15s timeout) + `sync.Once` lazy **insecure ikizi**, seçim `zoomClientFor(skipVerify)`; Bearer per-request header; 401/403'te token invalidate + tek retry |
| system_settings emsalleri | ⚠️ **İki düzeltme:** external_catalogue.go ve dashboard_presets.go `internal/chstore`'da (api değil) ve ikisi de operatör-settings deseni DEĞİL (hardcoded slice; boot-seed version damgası). Gerçek şablonlar: **custom_roles** (tek JSON blob, boot LoadPersisted + 30s multi-pod poll, atomik tam-blob yazım, s.audit) ve **internal/tempo/client.go** — kanonik: `{enabled, baseUrl, authType, token, username, orgId, insecureSkipVerify}` blob'u, dar settingsStore interface'i, SavePersisted + canlı Configure() swap, `Snapshot()` token'ı maskeler (hasToken), boş input saklı token'ı korur, audit detayına secret girmez |
| api_tokens ayrı-tablo gerekçesi | ✅ hash lookup + tombstone revocation + satır-düzeyi kimlik — settings blob'unda yarışlı olurdu; bu ihtiyaçların hiçbiri cluster listesinde yok |
| metric_points res_keys/res_values yeni boyut taşır | ✅ store.go:1211 `Array(LowCardinality(String))/Array(String)`; okuma deseni hazır (`res_values[indexOf(res_keys,'k8s.cluster.name')]`, metricEnvExpr/clusterDeriveExpr emsalleri); ORDER BY'da cluster yok → sıcak filtre gerekirse spans'taki MATERIALIZED kolon terfisi (store.go:1489) kanıtlı yol — **backfill bu iterasyonda YOK, yalnız not** |
| Hosts frontend'i | ✅ Hosts.tsx 222 satır: useDataTable('hosts'), URL-first `?host=` drawer, trendler DrawerTrendRow (Sparkline/KPI yok); sidebar'da v0.8.490'dan beri GİZLİ ama route+API canlı |

## 2. Depolama şekli — ÖNERİ: system_settings, `thanos_clusters` key'i (tempo şablonu)

Şekil (tek JSON blob):
```json
{"clusters":[{"name":"prod-ist","url":"https://thanos-querier...","authType":"bearer",
  "token":"<secret>","insecureSkipVerify":false,"namespaceFilter":"^(app-|payments-)",
  "enabled":true}]}
```

| Yaklaşım | + | − |
|---|---|---|
| **system_settings blob (ÖNERİLEN)** | Invariant #6 birebir ("Settings live in system_settings"); tempo şablonu secret sözleşmesini hazır getirir (per-cluster `hasToken` maskesi, boş-input-korur merge, audit'e secret girmez); 30s poll multi-pod yayılımı bedava; N≤~20 cluster'da atomik tam-blob yazım dertsiz | Satır-düzeyi eşzamanlı düzenleme yarışı (iki admin aynı anda) — tam-blob last-write-wins; cluster sayısı yüzlere çıkarsa blob hantallaşır (gerçekçi değil) |
| Ayrı CH tablosu (api_tokens tarzı) | Satır kimliği, per-row revocation | Gerekçeleri burada yok (hash lookup yok, tombstone ihtiyacı yok); DDL+FINAL+migration maliyeti; invariant #5 ruhuna aykırı |

Paket yerleşimi: **`internal/thanos/`** — tempo'nun birebir simetriği
("Tempo trace fallback" nasıl `internal/tempo/` ise, Thanos metric
kaynağı da kendi paketi): `client.go` (settings + HTTP + PromQL),
handler'lar `internal/api/thanos_handlers.go` (tempo_handlers emsali).
Settings UI: Settings'e "Clusters" sekmesi — token alanı "stored"
göstergeli, asla geri echo yok.

## 3. HTTP client — Zoom deseninin birebir uyarlaması

- İki paket-düzeyi tekil client: verify (default) + `sync.Once` lazy
  insecure ikizi; seçim per-cluster `insecureSkipVerify` bayrağıyla
  (`thanosClientFor(skipVerify)`). Per-cluster client ÜRETİLMEZ —
  TLS varyansı tek bool olduğundan iki tekil yeter (Zoom kanıtı).
- Timeout 15s (Zoom değeri); ayrıca her sorguya `ctx` üzerinden 10s
  per-request deadline (sayfa bütçesi <3s'yi tek yavaş cluster'a
  kilitlememek için — bkz. §6 fan-out).
- Auth: per-request `Authorization: Bearer <token>` (cluster config'ten).
  **Teyit edilecek (varsayılmadı):** token'ın kaynağı platform
  standardına bağlı — tipik desen `cluster-monitoring-view`
  ClusterRole'lü bir ServiceAccount token'ı ve OAuth-proxy'li
  `thanos-querier` route'u; kimin token üreteceği ve token ömrü
  (bound SA token'ları expire olur!) platform ekibiyle netleşmeli.
  Uzun ömürlü secret verilmezse Settings'e "token yenileme" runbook
  notu düşülür.
- Yanıt zarfı: Prometheus `/api/v1/query` + `/api/v1/query_range`
  `{status, data:{resultType, result:[{metric{}, value|values}]}}` —
  dar bir decode struct'ı, `status!="success"` → hata.

## 4. PromQL tasarımı — cluster+metrik başına TEK sorgu

Liste görünümü (instant query, pod başına ayrı sorgu YOK):
```promql
# CPU (çekirdek): 
sum by (namespace, pod) (rate(container_cpu_usage_seconds_total{container!="",pod!=""}[5m]))
# Memory (byte):
sum by (namespace, pod) (container_memory_working_set_bytes{container!="",pod!=""})
# (opsiyonel, memPct/cpuPct için) limitler — kube-state-metrics gerektirir:
sum by (namespace, pod) (kube_pod_container_resource_limits{resource="memory"})
```
- Kardinalite sınırı: per-cluster `namespaceFilter` regex'i sorguya
  `namespace=~"..."` olarak girer (koca cluster'da 10k pod'u eve
  taşımamak için) + `topk(500, ...)` sarmalı — clampLimit ruhu.
- Trend (yalnız drawer, tek pod): `/api/v1/query_range`, `step=60`
  → HostTrendPoint'in dakika bucket'ıyla birebir hizalı.
- **Teyit edilecek (varsayılmadı):** `container_cpu_usage_seconds_total`
  ve `container_memory_working_set_bytes` cAdvisor/platform-monitoring
  metrikleridir; org'un stack'inde Thanos tenancy'sinin bunları
  sunduğu ve `kube_pod_container_resource_limits`'in mevcudiyeti şu
  tek komutla doğrulanır (canlı doğrulama §8'de):
  `GET /api/v1/query?query=count(container_cpu_usage_seconds_total)`.
  Limits yoksa cpuPct/memPct 0 kalır — HostRow zaten `memPct:0`
  sözleşmesini taşıyor, frontend "—" basar.

## 5. API yüzeyi — ince handler + serveCached

Yeni dosya `internal/api/thanos_handlers.go`:

| Rota | Cache key | TTL |
|---|---|---|
| `GET /api/clusters/pods?cluster=X&range=` | `cluster-pods:<cluster>:<cacheBucket>` | 60s (hosts konvansiyonu; tipik scrape 30s → bir scrape gecikmesi kabul) |
| `GET /api/clusters/pods/detail?cluster=&namespace=&pod=` | `cluster-pod-detail:<c>:<ns>:<pod>:<bucket>` | 60s |
| `GET/PUT /api/settings/thanos` | cache yok; PUT admin + `s.audit("settings.thanos.update", ...)`, secret detay dışı | — |

Key girdileri skaler (cluster adı + 30s'lik bucket) → hosts.go gibi
fnvDigest gerekmez; `namespaceFilter` settings'ten geldiği ve
değişimi nadir olduğu için key'e settings-blob versiyonu eklenir
(`:v<updatedAt>`), yoksa filtre değişince 60s bayat veri servis edilir.

## 6. N-cluster fan-out — ÖNERİ: frontend fan-out (cluster başına istek)

| Yaklaşım | + | − |
|---|---|---|
| **Frontend fan-out (ÖNERİLEN):** sayfa cluster başına `GET /api/clusters/pods?cluster=X` atar (React Query, paralel) | Her cluster KENDİ cache slot'unda (yavaş cluster diğerinin HIT'ini bozmaz); per-cluster loading/error/stale durumu React Query'den bedava — "kısmi sonuç + hata işareti" kendiliğinden; backend'de errgroup yok — v0.8.532 dersi (getServices errgroup prod'da geri alındı) tekrarlanmaz; SWR stale servisi cluster bazında çalışır | N istek (N≤10-20 — tarayıcı h2'de dert değil); "tüm cluster'lar" toplam KPI'ı client'ta toplanır |
| Backend errgroup fan-out (tek endpoint) | Tek istek; server-side birleşik sıralama | Tek cache key altında en yavaş cluster tüm yanıtı sürükler; kısmi sonuç için özel zarf (`{cluster, rows, error}`) + stale işaretleme el yapımı; errgroup'un prod geçmişi kötü |

Frontend fan-out'ta bile her cluster çağrısı server'da 10s deadline'lı
(§3) — asılı Thanos, singleflight slotunu süresiz tutamaz.

## 7. Frontend — yeni sayfa, aynı primitifler; HostRow YENİDEN KULLANILMAZ

Şekil farkı geri dönüşü olmayan: HostRow host-eksenli (`host, services[]`),
buradaki satır (cluster, namespace, pod) eksenli; CPU'su oran değil
çekirdek. Zorla HostRow'a bükmek iki yüzeyi de bozar. Öneri:

- `lib/types.ts`: `ClusterPodRow {cluster, namespace, pod, cpuCores,
  memBytes, cpuPct?, memPct?, lastSeen}` + `ClusterPodTrendPoint
  {bucket, cpuCores, memBytes}` (HostTrendPoint'le aynı bucket
  sözleşmesi: unix saniye, dakika).
- **Yeni sayfa `/clusters`** (pages/Clusters.tsx) — /hosts'A GÖMÜLMEZ:
  hosts sayfası (a) v0.8.490'dan beri sidebar'da gizli, (b) veri
  kaynağı semantik olarak farklı (uygulamanın OTel process metrikleri
  vs cluster'ın cAdvisor'ı) — karıştırmak "bu sayı neden iki yerde
  farklı" sorusunu doğurur. Sidebar'a tek satır "Clusters" girer.
- Sayfa anatomisi Hosts.tsx kopyası: Topbar + range, cluster filtresi
  (küçük sabit set ≤~10 → düz `<select>`, konvansiyon §3), useDataTable
  (`storageKey:'clusterpods'`, kolonlar: Cluster/Namespace/Pod/CPU/
  Memory/Mem %/Last seen, >60 warn >85 err renk eşiği aynen), satır >
  100 → contentVisibility, URL-first `?pod=` drawer (DrawerTrendRow
  ile CPU/Mem trend + namespace bağlamı). Cluster kolonu "hepsi"
  görünümünde görünür, tek cluster filtresinde gizlenebilir (v0.8.574
  ?cols= altyapısı hazır).
- Ayrı bileşen ama SIFIR yeni desen: tablo/drawer/badge/tema hepsi
  mevcut atomlardan.

## 7.5 APM ↔ Cluster eşleştirme ve pivot (operatör sorusu üzerine eklendi)

**Eşleşme anahtarı:** Settings'teki cluster `name` == spans'ın türetilmiş
`cluster` değeri (k8s.cluster.name / openshift.cluster.name coalesce'i,
repo.go:294 clusterDeriveExpr). String eşitliği — ekstra mapping tablosu
YOK. Yazım hatasını yapısal olarak önlemek için Settings → Clusters
"ekle" formu, telemetride GÖZLENEN cluster adlarını öneri listesi olarak
sunar (mevcut clusters enumeration'ı, repo.go:403 cache'li) ve listede
olmayan ada "telemetride görülmüyor — Thanos verisi servislerle
eşleşmeyecek" uyarı rozeti basar (kaydetmeyi engellemez: önce Thanos,
sonra telemetry gelen kurulum sırası meşru).

**Pivot zinciri (Databases drill-through'unun birebir analoğu):**

1. Service sayfasında per-cluster RED breakdown ZATEN var
   (`GET /api/services/{name}/clusters` + `ServiceClusterBreakdown`,
   Service.tsx:521). Değişiklik: breakdown satırındaki cluster adı,
   Settings'te eşleşen bir Thanos cluster'ı VARSA link olur →
   `/clusters?cluster=<ad>&namespace=<ns>` (URL kaynak-of-truth).
2. `namespace` derinliği bedava: servisin k8s namespace'i
   service_metadata'da zaten okunuyor (`k8s.namespace.name`,
   service_metadata.go:407) → pivot linki servis detayından namespace
   filtreli gider; /clusters o cluster'ın o namespace'indeki pod'ları
   açar. Pod-düzeyi eşleşme de mümkün (`k8s.pod.name` deploys.go:355'te
   okunuyor) — ilk iterasyonda namespace yeter, pod filtresi URL'de
   destekleyip linklemeyi sonraya bırakırız.
3. Eşleşme YOKSA (Settings'te o cluster tanımlı değil) ad düz metin
   kalır — ölü link üretilmez.

Hover davranışı: Databases gibi TIKLAMA pivotu öneriyoruz; hover'da
Thanos'a istek atan popover ES-cost disiplininin (fetch on open/expand
only) Thanos karşılığını ihlal eder. İstenirse tıklamayla açılan drawer
60s cache'li özet gösterebilir — o da /clusters sayfasının kendisi zaten.

## 8. Canlı doğrulama planı (implementasyon sonrası)

1. Settings → Clusters: 1 cluster + SA token gir; "stored" maskesi.
2. `curl -H "Authorization: Bearer $TOK" '<thanos>/api/v1/query?query=count(container_cpu_usage_seconds_total)'`
   → metrik mevcudiyeti (§4 teyidi) + auth yolu tek adımda.
3. /clusters: liste ≤3s dolmalı (X-Cache: MISS→HIT ardışık yenilemede);
   yanlış token'lı ikinci cluster eklenince o cluster'ın paneli hata
   çipi göstermeli, diğeri dolu kalmalı (kısmi sonuç).
4. Drawer trendi 15m/1h pencerede dakika bucket'larıyla çizilmeli.

## 9. Kapsam dışı (bilinçli) + ileri notlar

- **metric_points backfill / alerting-anomaly paritesi:** bu iterasyonda
  YOK. İleride istenirse: veri zaten `res_keys/res_values`'a OTLP'yle
  yazılabilir; sıcak cluster filtresi gerekirse spans'ın MATERIALIZED
  `cluster` kolonu + hasXCol probe terfisi (store.go:1489) kanıtlı yol.
- Thanos'a yazma yok; sorgu dışı hiçbir uç çağrılmıyor.
- Sidebar'daki gizli /hosts'un akıbeti değişmiyor.
- mTLS/custom-CA (skipVerify yerine CA sertifikası yükleme) — ihtiyaç
  çıkarsa tempo'daki gibi settings alanı olarak sonra.

## 10. Tahmin ve dilimleme (onaya sunulan)

1. `internal/thanos/` paket (settings+client+PromQL) + testler — ~1 saat
2. `thanos_handlers.go` + rotalar + audit — ~30 dk
3. Settings → Clusters sekmesi (gözlenen-cluster önerileri + eşleşme
   uyarısı dahil, §7.5) — ~1 saat
4. `/clusters` sayfası + types/api + sidebar — ~1 saat
5. Servis→cluster pivot linki (ServiceClusterBreakdown → /clusters,
   namespace'li, §7.5) — ~30 dk
6. Canlı doğrulama + runbook notu — ~30 dk

Her dilim kendi tag'iyle (`v0.8.57X`), toplam ~4-4.5 saat.
