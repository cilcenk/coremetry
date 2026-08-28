# K8s entity katmanı — AŞAMA 2: tasarım (ONAYLANDI 2026-08-28) · AŞAMA 3 GEMİDE

**Tarih:** 2026-08-28 · **Girdi:** [entity-layer-discovery-2026-08-28.md](../audit/entity-layer-discovery-2026-08-28.md)
· **Durum:** operatör onayı 2026-08-28 ("Onay"). AŞAMA 3 sürümleri:
v0.10.127 şema + terfi kolonları + entity_seen MV (+ `migrations/0011`),
v0.10.128 Remote Cluster kimliği + cluster matcher, v0.10.130 syncer + span
geçişi + sorgu katmanı + pivot uçları, v0.10.131 UI (Settings → K8s entity
katmanı, Service → Infra "Pods (entity layer)", /pod şeridi). Bayrak
varsayılan KAPALI. Prod'da: `0011_entity_layer.sql` operatörde; Remote
Cluster kayıtlarına Thanos etiket adı/değeri + span cluster değeri girilir
(probe A2 sonucu), sonra Settings → K8s entity katmanı → Enable.
**Uygulamada tasarımdan sapmalar:** `GET /api/entities/{id}` yerine
`GET /api/entity?id=` (id `/` taşır); Thanos etiket varsayılanı boş
(matcher yok) — §1.1; ayar blobu `updatedAt` damgası (kendi reload
sinyali eski satırı okuyup değeri geri alıyordu); `system_settings`
INSERT'i version'ı açık yazar (v0.10.129, Replicated dedup).

> **Varsayımlar (probe gelmeden yazıldı, her biri konfigüre edilebilir):**
> V1 Thanos external label adı `cluster` (UI önerisi; kayıt varsayılanı BOŞ = matcher yok, bkz. §1.1), değeri Remote Cluster `Name` ile
> aynı (probe A2(b) aksini söylerse yalnız kayıt alanları değişir, model
> değişmez). V2 Prod span'leri `openshift.cluster.name`, `k8s.namespace.name`,
> `k8s.pod.name`, `k8s.node.name` taşır (n=1 kanıt; B5 filo ölçümü düşük
> çıkarsa span-türevli yol boş kalır, Thanos yolu etkilenmez). V3
> `k8s.pod.uid` yok; gelirse ömür ayrımında kullanılır, kimlik değişmez.
> V4 Prod dış Distributed CH'de `spans_local` kolon/MV/tablo değişiklikleri
> **operatör migration'ı** (`0011_entity_layer.sql`), boot DDL yalnız
> self-managed kurulumlarda.

## What

Thanos (KSM/node-exporter) ve span resource attribute'larından **zaman
geçerlilikli** bir varlık grafı kur: cluster > node > namespace > workload >
pod > container, artı ayrı **service** ekseni; her varlığı trace/metrik
verisine iki yönde bağla (pod → servisler/metrikler/trace'ler, servis →
pod'lar, span → pod/node). Kök kimlik Remote Cluster kaydı; eşlenemeyen
cluster değerleri sayaçlanır, düşürülmez. Özellik bayrağı arkasında.

---

## 1. Kimlik modeli

### 1.1 Cluster kökü — Remote Cluster kaydına üç alan + id

`internal/thanos/client.go` `ClusterConfig`'e (tam-blob JSON, geriye uyumlu):

| Alan | Anlam | Boşsa |
|---|---|---|
| `ID string` | opak, değişmez kimlik (`c-` + 8 hex); **oluşturmada üretilir** | mevcut kayıt için boot'ta `c-` + fnv64(Name)[:8] türetilir ve blob'a **bir kez** yazılır (SavePersisted, audit `settings.thanos.backfill_ids`) — Name sonradan değişse de ID sabit |
| `ThanosLabelName` | seriyi bu cluster'a bağlayan etiket adı | **boş = matcher YOK = cluster başına URL modeli** (uygulamada v0.10.128 kararı: V1'deki `cluster` varsayılanı per-cluster querier'ları — serilerde o etiket yoksa — boşaltırdı; tek querier operatörü alanı açıkça `cluster` yapar, UI önerir) |
| `ThanosLabelValue` | o etiketin değeri | `Name` |
| `SpanClusterValue` | span `cluster` kolonunda bu cluster'ın değeri | `Name` |

Geriye dönük doldurma = **türetilmiş varsayılanlar**, satır yeniden yazılmaz;
UI iki rozet: "Thanos'ta görülüyor" (`count(kube_node_info{<label>="<val>"}) > 0`,
10 s deadline, 60 s cache) ve mevcut "span'de görülüyor" (`nameKnown`).
`internal/thanos/promql.go`'daki **her** seçici `clusterMatcher(c)` =
`,<label>="<val>"` alır (tek querier'da cluster karışmasının kapısı; ayrı
URL modelinde zararsız — etiket yoksa `""` eşleşmez → **boş döner**, o
yüzden etiket adı boş bırakılabilir = matcher yok = eski davranış).

### 1.2 entity_id

`<type>:<cluster_id>/<doğal anahtar>` — insan-okur, önek tipler
(`TopologyNodeIdentity` emsali), cluster'lar arası çakışmaz:

| Tip | entity_id | İkincil |
|---|---|---|
| cluster | `cluster:<cid>` | — |
| node | `node:<cid>/<node>` | `system_uuid` (kube_node_info) |
| namespace | `ns:<cid>/<ns>` | display name (Project annotation, opsiyonel, Thanos'ta yok → boş) |
| workload | `wl:<cid>/<ns>/<kind>/<name>` (kind ∈ Deployment/StatefulSet/DaemonSet/Job/CronJob) | — |
| pod | `pod:<cid>/<ns>/<pod>` | **`uid`** varsa ömür ayrımı anahtarı |
| container | `ctr:<cid>/<ns>/<pod>/<container>` | image |
| service | `svc:<service_name>` (cluster'sız — bugünkü servis kimliği, E16) | — |

Kırmızı çizgi korunur: `entity_id` hiçbir ORDER BY/shard/önbellek anahtarına
girmez (doğal kolonlar girer); servis kimliği `service_name` kalır.

### 1.3 Zaman geçerliliği

Her varlık ve ilişki satırı bir **ömür**: `(entity_id, valid_from)` dedup
anahtarı, `valid_to` (0 = açık), `last_seen`. Kurallar:

- **Yeni ömür:** (uid varsa) uid değişti; (yoksa) aynı ad `last_seen`'den
  `podGap` (varsayılan 10 dk) sonra yeniden görüldü. StatefulSet sabit adı
  böylece uid'siz de çoğu restart'ta ayrışır (10 dk'dan kısa restart tek
  ömür kalır — sınır UI'da ilan edilir, `NameStable` emsali).
- **Kapanış:** Thanos o cluster'a **başarıyla** ulaştı ve pod listede yok →
  `valid_to = last_seen`; cluster ulaşılamıyorsa **hiçbir şey kapanmaz**
  (bilinmiyor), `staleAfter` (24 h) sonra satır `stale` işaretlenir, silinmez.
- **Sorguda "o an geçerli":** `valid_from <= t AND (valid_to = 0 OR valid_to >= t)`
  ReplacingMergeTree `FINAL` üzerinde; bir ad birden çok ömürde varsa
  trace zamanı hangisine düşüyorsa o (`t` = span/trace zamanı). Açık uçlu
  ömür + geç kapanış (≤ 1 sync aralığı) kabul edilen belirsizlik.
- **Eşlenemeyen cluster değeri:** span `cluster` kolonundaki değer hiçbir
  `SpanClusterValue`'ya eşlenmiyorsa (ya da Thanos etiketi bilinmeyen değer
  taşıyorsa) satır **yazılmaz**, `entity_sync_runs.unmapped` sayacına
  `değer → n` düşer; admin ekranı listeler ("Remote Cluster ekle").

---

## 2. Şema (`/clickhouse-schema` sözleşmesi; hepsi gün-bir
`highVolumeTables`/`defaultShardPolicy`/`tablesWithoutTraceID` kayıtlı)

### 2.1 `entities` — state, ReplacingMergeTree(version)

```sql
CREATE TABLE IF NOT EXISTS entities (
  entity_type  LowCardinality(String),     -- cluster|node|namespace|workload|pod|container|service
  cluster_id   LowCardinality(String),
  entity_id    String,
  valid_from   DateTime,                   -- ömür başlangıcı (dedup anahtarının parçası)
  valid_to     DateTime DEFAULT 0,         -- 0 = açık
  namespace    LowCardinality(String) DEFAULT '',
  name         String,
  uid          String DEFAULT '',
  parent_id    String DEFAULT '',          -- hızlı yukarı gezinme (relations'ın kopyası)
  label_keys   Array(LowCardinality(String)),
  label_values Array(String),
  source       LowCardinality(String),     -- thanos|span
  first_seen   DateTime, last_seen DateTime,
  stale        UInt8 DEFAULT 0,
  version      UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))
) ENGINE = ReplacingMergeTree(version)
ORDER BY (entity_type, cluster_id, entity_id, valid_from)
TTL last_seen + INTERVAL 180 DAY
```

Gerekçe: ORDER BY = dedup anahtarı, ilk kolon filtre öznesi (tip), ardından
cluster (her sorgu cluster taşır), `valid_from` ömrü ayırır. **PARTITION BY
YOK** (Kural P1: yeniden yazılan `last_seen` partition'da olsaydı FINAL
kopyaları temizleyemezdi). Shard anahtarı `cityHash64(entity_id)` — ORDER
BY'ın içinde (O5). Tam-satır replace: yazıcı tüm alanları taşır (E2).
Etiketler `Array` çifti (ev stili), hassas etiket taşınmaz (allow-list:
`app`, `app.kubernetes.io/*`, `version`, `tier`; geri kalanı yazılmaz).

### 2.2 `entity_relations`

```sql
CREATE TABLE IF NOT EXISTS entity_relations (
  rel_type   LowCardinality(String),   -- parent (cluster→node, cluster→ns, ns→wl, wl→pod, pod→ctr) | runs_on (pod→node) | runs (pod→service)
  cluster_id LowCardinality(String),
  parent_id  String, child_id String,
  valid_from DateTime, valid_to DateTime DEFAULT 0, last_seen DateTime,
  source     LowCardinality(String),
  version    UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))
) ENGINE = ReplacingMergeTree(version)
ORDER BY (rel_type, cluster_id, parent_id, child_id, valid_from)
TTL last_seen + INTERVAL 180 DAY
```

Ters yön (child → parent) `entities.parent_id` ile; `runs`/`runs_on` her
iki yönde okunur (`child_id` üzerinde skip index `bloom_filter(0.01)`
GRANULARITY 4 — nokta araması).

### 2.3 Terfi kolonları (spans) — `entity_seen`'in ve pod/node filtresinin ön koşulu

```sql
ALTER TABLE spans ADD COLUMN IF NOT EXISTS k8s_namespace LowCardinality(String)
  MATERIALIZED coalesce(nullIf(res_values[indexOf(res_keys,'k8s.namespace.name')],''),
                        nullIf(res_values[indexOf(res_keys,'kubernetes.namespace.name')],''), '');
ALTER TABLE spans ADD COLUMN IF NOT EXISTS k8s_pod  LowCardinality(String)
  MATERIALIZED coalesce(nullIf(res_values[indexOf(res_keys,'k8s.pod.name')],''), nullIf(host_name,''), '');
ALTER TABLE spans ADD COLUMN IF NOT EXISTS k8s_node LowCardinality(String)
  MATERIALIZED res_values[indexOf(res_keys,'k8s.node.name')];
ALTER TABLE spans ADD INDEX IF NOT EXISTS idx_k8s_pod  k8s_pod  TYPE set(0) GRANULARITY 4;
ALTER TABLE spans ADD INDEX IF NOT EXISTS idx_k8s_node k8s_node TYPE set(0) GRANULARITY 4;
```

MATERIALIZED (EXPLICIT değil): Distributed forward'ında düşer, ingest
kıramaz, eski pod bilmese de sunucu hesaplar ([feedback-distributed-column-safety]).
`promotedAttrs` kaydı (`promoted_attr.go`) + **veriyle kanıtlayan probe**
(kolon == dizi ifadesi, v0.9.621 dersi); probe geçmezse filtre dizi yoluna
düşer (yavaş, yanlış değil). `LowCardinality` ölçüsüz: pod adı prod'da ≤ 10k
distinct/gün beklenir — **ölçülmeden LC yapılmaz** (C3): `0011` başlığına
`uniq(k8s_pod)` kapısı yazılır; > 100k çıkarsa düz `String`.

### 2.4 `entity_seen_1m` / `entity_seen_5m` — MV

```sql
CREATE MATERIALIZED VIEW IF NOT EXISTS entity_seen_5m
ENGINE = AggregatingMergeTree()
PARTITION BY toDate(time_bucket)
ORDER BY (service_name, cluster, k8s_namespace, k8s_pod, time_bucket)
TTL toDate(time_bucket) + INTERVAL 30 DAY
AS SELECT
  toStartOfFiveMinute(time)             AS time_bucket,
  service_name, cluster, k8s_namespace, k8s_pod,
  anyLastSimpleState(k8s_node)          AS k8s_node,      -- pod→node (span yolu)
  countState()                          AS span_count_state,
  countStateIf(status_code = 'error')   AS error_count_state,
  sumState(duration)                    AS duration_sum_state,
  minState(time)                        AS first_seen_state,
  maxState(time)                        AS last_seen_state
FROM spans
WHERE k8s_pod != ''
GROUP BY time_bucket, service_name, cluster, k8s_namespace, k8s_pod
```

`entity_seen_1m` aynı şekil, `toStartOfMinute`, TTL 3 gün. **Filtre öznesi
`service_name` önce** ("bu servisi hangi pod'lar taşıyor" sorusu; ters yön
pod → servis için `cluster, k8s_pod` üzerinde ikinci okuma ORDER BY
önekini kullanmaz — pod sorguları `entity_relations.runs` ile cevaplanır,
MV yalnız zaman-dilimli sayım için). tDigest **yok** (satır başına ~1.5 KB,
2.5k pod × 288 kova/gün → yüzlerce MB/gün; yüzdelik gerekirse ham yol,
pod filtresi set-index ile sınırlı). Cluster boyutu `cluster` kolonundan —
prod'da o kolon `spans_local`'a inmemişse (probe E) `0011` onu da ekler.
`WHERE k8s_pod != ''` kapısı: k8s bağlamı taşımayan span'ler MV'ye hiç girmez.
**MV, MATERIALIZED kolonları okur** — yerel CH'de doğrulanacak ilk adım
(v0.5.361 sınıfı boot arızası riski: kolon ALTER'ı MV'den önce, `alters`
diliminde).

### 2.5 `entity_sync_runs` — kısmi başarı + sayaçlar

```sql
CREATE TABLE IF NOT EXISTS entity_sync_runs (
  cluster_id LowCardinality(String), started_at DateTime, finished_at DateTime,
  status LowCardinality(String),        -- ok|partial|failed|skipped|disabled
  entities_written UInt32, relations_written UInt32, closed UInt32,
  unmapped_keys Array(String), unmapped_counts Array(UInt32),
  thanos_ms UInt32, ch_ms UInt32, error String,
  version UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))
) ENGINE = ReplacingMergeTree(version)
PARTITION BY toYYYYMM(started_at)
ORDER BY (cluster_id, started_at)
TTL started_at + INTERVAL 30 DAY
```

`started_at` yeniden yazılmaz → P1 güvenli. `/admin/stats` + admin sayfası
buradan; span-türevli `unmapped` (bilinmeyen `cluster` değeri) aynı tabloya
`cluster_id='(unmapped)'` satırıyla.

### 2.6 Migration + bayrak

- Self-managed: boot DDL (`tables`/`alters`/`mvs` dilimleri, yerleşim testi).
- Prod dış Distributed: **`migrations/0011_entity_layer.sql`** (operatör; başlık:
  gerekçe + `system.clusters` doğrulama + `uniq(k8s_pod)` LC kapısı + "nasıl
  geri alınır") ve `0011_entity_layer_rollback.sql` (MV/tablo DROP, kolonlar
  `DROP INDEX → DROP COLUMN` sırası — Code 47).
- Bayrak `system_settings["entity_layer"]` = `{enabled, syncInterval:"60s",
  staleAfter:"24h", podGap:"10m", parallelClusters:4}`; `enabled=false` →
  syncer başlamaz, entity uçları `404 {disabled:true}`, UI sekmeleri gizli,
  MV/kolonlar **dokunulmaz** (varsa yazmaya devam eder — ingest maliyeti
  bayrakla değil migration'la geri alınır; bu bilinçli: bayrak "yüzey",
  migration "veri").

---

## 3. Doldurma ve senkronizasyon — `internal/entity/`

| Bileşen | Ne yapar |
|---|---|
| `Syncer` (worker rolü, `cache.NewLeaderHolder(lock, "entity-sync", LeaderTTL(interval))`, `SetOnAcquire` → ilk tick hemen) | her tick `thanos.Service.CurrentSettings()` okur (yeni cluster restart'sız gelir; kaldırılan cluster: sync atlanır, kayıtlar `stale`'e yaşlanır) |
| cluster başına `syncCluster(ctx, c)` — **bounded paralel** (`parallelClusters`, varsayılan 4), her cluster kendi 10 s deadline'ı | 6 PromQL: `kube_node_info`, `kube_node_status_capacity`(opsiyonel), `kube_pod_info`, `kube_pod_owner`, `kube_replicaset_owner`, `kube_pod_container_info` — hepsi `{<label>="<val>"<nsFilter>}` ile; **filtresiz seri taraması yok**; `maxSeriesParsed` bu yol için 50k (D14'e göre ayar) |
| `normalize.go` (saf) | etiket seti → entity/relation satırları; owner zinciri: `owner_kind` RS → `kube_replicaset_owner` → Deployment; STS/DS/Job doğrudan; Job → CronJob `kube_job_owner`; `node` label → `runs_on` |
| `diff.go` (saf) | önceki anlık görüntü (cluster başına bellekte) ile fark: yeni ömür / kapanış / `last_seen` tazeleme (her 5. tick toplu, yazım hacmini sınırlar) |
| `spanpass.go` | son 5 dk `entity_seen_5m` → `cluster` değeri → `SpanClusterValue` eşle → `runs` (pod→service) + Thanos'un tanımadığı pod'lara `source='span'` varlık; eşlenemeyen → `unmapped` |
| yazım | tek `INSERT` batch/cluster (async_insert korunur), tam-satır |

**Fan-out kararı:** cluster başına ayrı sorgu (D14 tek-sorgu ölçümü ne
olursa olsun): kısmi başarı, `NamespaceFilter`, cluster başına deadline ve
`entity_sync_runs` satırı bedavaya gelir; N=30 cluster × 6 sorgu × ~200 ms /
4 paralel ≈ 9 s/tick, 60 s aralığa sığar (ölçülecek). Tek dev sorgu yalnız
D14 "< 2 s ve < 50k seri" derse ve o zaman da cluster başına `label_replace`
ile ayrıştırılarak; varsayılan değil.

**Tazelik:** KSM 30 s scrape + 60 s sync → pod en geç ~90 s sonra varlık;
< 90 s yaşayan pod'lar Thanos'ta görünmeyebilir, span yolu (`entity_seen`)
onları da yakalar. Devir hızı yazım hacmini belirler (D13): 750 ömür/gün
için ~1 satır/dk — önemsiz.

**Çakışma:** yapı (var/yok, node ataması, owner, uid) → **Thanos kazanır**;
`runs` (pod→servis) ve etkinlik (`last_seen`) → **span kazanır**; Thanos
"yok" derken span geliyorsa ömür span durana kadar açık kalır (geç veri).

**Self-observability:** `EntityObservability()` (atomic sayaçlar: tick,
cluster ok/fail, entities/relations yazım, unmapped, son süre) →
`SystemStats`; her `syncCluster` `selfobs` span'i (`entity.sync`,
attr `cluster_id`, `status`), CH yazımları `tracedConn`'dan zaten span.

---

## 4. Sorgu yolları ve maliyet

| Soru | Yol | Tahmini maliyet |
|---|---|---|
| Pod'un span'leri / hataları / gecikmesi | `/api/traces?cluster=&filters=[{k8s.pod.name = X}]` → terfi kolon + `set(0)` index (granül eleme) + v0.10.126 yenilik dilimi; sayımlar `entity_seen_5m` (`cluster, k8s_pod` filtreli, pencere) | ham: yalnız pod'un granülleri (v0.9.623 emsali 10 M → 1.3 M satır); MV: ≤ 288 satır/gün |
| Pod'un metrikleri (CPU/bellek/restart) | mevcut `singlePodCPU/MemQuery` + restarts, cluster matcher'lı; entity → `(label, ns, pod)` çevirisi | 1-3 Thanos sorgusu, 10 s deadline, 60 s cache |
| Servisi taşıyan pod'lar + sağlık | `entity_seen_5m WHERE service_name=? AND time_bucket ∈ pencere GROUP BY cluster,k8s_namespace,k8s_pod` (ORDER BY öneki) → `entities`/`entity_relations` FINAL ile faz/node/workload; restart/faz Thanos anlık görüntüsünden (syncer bellek) | MV: pod × kova; entities: nokta okuma |
| Node üzerindeki servisler (etki alanı) | `entity_relations runs_on WHERE cluster_id=? AND parent_id='node:…'` (geçerli) → pod'lar → `runs` → servisler | ≤ node'daki pod sayısı, FINAL nokta okuma |
| Span → pod/node | trace detayındaki resource attr'lar → `pod:<cid>/<ns>/<pod>` (cluster: span `cluster` değeri → `SpanClusterValue`); node `entities.parent_id`/`runs_on` | sorgu yok / 1 nokta okuma |
| Trace listesinde pod/node filtresi | terfi kolon + set index; kolon `+ Column` faz-2 (sayfa id'leri, `idx_trace`) — **pencere taraması yok**. Doğrulama: `EXPLAIN indexes=1` çıktısında `Skip idx_k8s_pod Granules k/N`, k ≪ N | granül eleme |

**Cluster kuralı:** entity uçlarında `cluster` **zorunlu** (400) — `GET
/api/entities/clusters` hariç; traces'te pod filtresi cluster'sız verilirse
yanıt `clusterAmbiguous: [cid…]` taşır (aynı pod adı > 1 cluster'da), UI
cluster kolonu gösterir. Thanos'a giden her sorgu zaman aralığı + cluster
matcher taşır.

---

## 5. API ve UI

Yeni dosya `internal/api/entities.go` → `registerEntityRoutes(mux)`
(api.go tek satır; `/api-route` uygulanır), hepsi `serveCached` (anahtar:
tüm girdiler FNV) + `auth.RequireAnyRole(viewer…)`:

| Uç | Şekil |
|---|---|
| `GET /api/entities/clusters` | Remote Cluster listesi + son sync durumu (`entity_sync_runs`) |
| `GET /api/entities?cluster=&type=&namespace=&q=&at=&limit=` | sunucu-taraflı arama (picker kuralı), `at` = geçerlilik anı |
| `GET /api/entities/{id}?at=` | varlık + ebeveyn zinciri + çocuk sayıları + ömürler |
| `GET /api/entities/{id}/services?from&to` | pod/node/ns → servisler (`runs`/`runs_on` + `entity_seen_5m`) |
| `GET /api/entities/{id}/metrics?from&to` | Thanos CPU/bellek/restart (delegasyon) |
| `GET /api/services/{name}/pods?cluster=&from&to` | pod'lar + sağlık |
| `GET/PUT /api/settings/entities`, `PUT /api/settings/thanos` (yeni alanlar) | admin, audit `settings.entities.update` / `settings.thanos.update` |
| `GET /api/admin/entities/sync`, `POST /api/admin/entities/sync/run` | admin, audit `admin.entity_sync.run` |

`lib/types.ts` + `lib/api.ts` karşılıkları; hiçbir eager Combobox yok.

UI (mockup-first — [feedback-tables-over-cards]):
- **Cluster seçici** = Remote Cluster listesi (id/name), URL `?cluster=<id>`
  (bugünkü `?cluster=<Name>` ile geriye uyumlu: Name → id çevrilir).
- **Servis sayfası → Pods/Infra sekmesi:** entity-tabanlı tablo (`useDataTable`):
  pod · namespace · node · workload · faz · restart · span/err (pencere) ·
  ömür (valid_from) · "Traces →" (pod filtresi).
- **`/pod`:** üst şerit cluster › node › namespace › workload › pod;
  "Services on this pod", metrik paneli (mevcut PodDrawer parçaları).
- **Node görünümü** (yeni, `/clusters` node tablosundan pivot): node'daki
  pod'lar ve servisler (etki alanı).
- **Traces:** `+ Column` `k8s.pod.name`/`k8s.node.name` (faz-2), filtre çipi
  pod/node; hücreden `/pod`'a link.
- **Çoklu cluster / cross-cluster servis haritası: KAPSAM DIŞI** — servis
  ekseni cluster'sız; kenarlara cluster boyutu MV cerrahisi (G6 sınıfı).
- Admin → Entities: sync tablosu (cluster · durum · süre · yazım · unmapped).

---

## 6. Riskler ve geri alma

| Risk | Tahmin | Önlem |
|---|---|---|
| `entities` büyümesi | ömür = pod × devir; 2.5k pod, %30/gün → ~750/gün → 270k/yıl (+ container ×1.4) | TTL 180 g; önemsiz |
| `entity_seen_1m` | aktif (pod, servis) çifti ~3k/dk → 4.3 M satır/gün, ~60 B → ~260 MB/gün ham | TTL 3 g; 5m: ~0.9 M/gün, 30 g ≈ 27 M satır |
| Ingest CPU (3 MATERIALIZED kolon + 2 MV) | `cluster` kolonuyla aynı sınıf; ölçülmedi | ship öncesi lokal ölçüm: `system.query_log` INSERT süreleri öncesi/sonrası medyan; prod'da `0011` operatör penceresi |
| Thanos yükü | 6 sorgu × N cluster/dk, cluster matcher'lı | `maxSeriesParsed`, 10 s deadline, `parallelClusters` |
| Tek querier'da etiket adı yanlış | tüm cluster'lar boş | rozet + `entity_sync_runs.status=failed` + admin uyarısı; matcher boş bırakılabilir |
| Pod adı yeniden kullanımı (STS) | 10 dk altı restart tek ömür | `podGap` ayarı, UI ilanı; uid gelirse otomatik |
| Prod migration | dış CH'de kolon/MV eklemek operatör işi | `0011` + rollback dosyası; kolon yoksa filtre dizi yoluna düşer |
| Geri alma | bayrak → yüzey kapanır anında; veri → `0011_rollback` | iki katman ayrı |

**Kapatılabilirlik:** `entity_layer.enabled=false` (varsayılan) → mevcut
sayfalar bugünkü gibi; syncer yok, uçlar 404, UI sekmeleri gizli.

---

## 7. Uygulama sırası (AŞAMA 3 — onaydan sonra, her adım derlenir/ölçülür)

1. Şema: boot DDL + `0011`/rollback; MV'nin MATERIALIZED kolon okuduğu lokalde kanıt; `ddl_slice_placement_test`, `partition_dedup_test` yeşil.
2. Remote Cluster alanları + id geriye doldurma + rozetler; `clusterMatcher` tüm seçicilerde (tablo testi: her PromQL matcher taşır).
3. `internal/entity` syncer (leader, kısmi başarı, sahte Thanos testleri) + `entity_sync_runs` + self-obs.
4. Span-türevli geçiş (`spanpass`) + `entity_seen` okuyucuları + unmapped sayacı.
5. Metrik etiket normalizasyonu (`normalize.go` tablo testleri: STS/DS/Job, node'suz pod, aynı ad iki cluster).
6. Sorgu katmanı + uçlar (`registerEntityRoutes`), cache anahtarları, cluster zorunluluğu testi.
7. UI pivotları (mockup → onay → dilim dilim).

Tahmin: her adım ~yarım gün, 1 + 2 önce ölçülür. Doğrulama: `go build/vet`,
`go test -race ./...`, pivot sorguları `query_log` satır/bayt (tahmin: pod
filtresi ≤ %15 granül; `entity_seen` servis sorgusu ≤ pod×kova satır),
tüm cluster sync süresi ölçümü (`entity_sync_runs`).

## 8. Onay isteyen kararlar

1. Remote Cluster **id**: üretilen opak id + Name-hash geriye doldurma (önerim) — ya da Name'i id sayıp yeniden adlandırmayı yasaklamak.
2. Thanos etiket adı varsayılanı `cluster` (probe A2(b) sonrası kesinleşir); tek querier mi per-cluster URL mı — ikisini de destekler.
3. Terfi kolonları + iki MV **prod'da operatör migration'ı** (`0011`) — kabul mü?
4. `entity_seen` boyutuna `service_name` dahil (pivotun kendisi) — evet varsayıldı.
5. k8sattributes'ı prod'da açmak bu tasarımın **ön koşulu değil**; uid/deployment/container için ayrı karar.
6. Cross-cluster servis haritası kapsam dışı — kabul mü?

**Spec'i onaylıyor musun? Açık karar varsa (1-6) o ilkönce.**
