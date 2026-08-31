# Rollouts — event-tabanlı rollout boru hattı + sayfa dönüşümü (AUDIT)

**Tarih:** 2026-08-30 · **HEAD:** `54633955` (v0.10.189) · **Durum:** salt-okunur inceleme, hiçbir kaynak dosya değiştirilmedi; onay bekliyor
**Yöntem:** 10 soru kümesi × (okuyucu + şüpheci) — 425 künyeli iddia, 18'i çürütülüp düzeltildi (süreç ölçüsü; ham raporlar oturum scratchpad'inde) + bağımsız tamlık eleştirisi (12 künye yeniden okundu, hepsi tuttu); satırlar HEAD'e karşı doğrulandı
**Ortam:** lokal minikube (chc-0/chc-1 2-shard Distributed, CH 26.2); prod dış Distributed CH (2 shard × 2 replica, CH 24.8 — **operatör beyanı**, `dbstmt_test.go:8-9` paritesi) — **bu makineden prod erişimi YOK**; prod olguları operatör ekran görüntüleri (2026-08-30, n=1 pod + ikinci cluster'ın namespace'siz span'leri) + `docs/audit/*` keşiflerinden; prod `cluster_name` ayarı **varsayım** (0001/0011 başlıkları çelişkili)

---

## Kısa hüküm

1. **En büyük varsayım bugün değişti — lehimize.** Repodaki keşif dokümanları (26/28 Ağustos) "prod span'inde `k8s.deployment.name` / `k8s.pod.uid` / `k8s.container.name` / `k8s.replicaset.name` YOK" diyordu ve 10 okuyucunun 10'u buna dayanarak "span dedektörü prod'da ölü doğar" yazdı. Operatörün 2026-08-30 ekran görüntüsü aksini kanıtlıyor — **tek pod, tek an, tek cluster (n=1)**: prod'da EN AZ bir cluster'da k8sattributes (ya da eşdeğer bir işleyici) bu anahtarları basıyor; bir Deployment pod'unun span'i `k8s.cluster.name`, `k8s.namespace.name`, `k8s.pod.name`, `k8s.pod.uid`, `k8s.node.name`, `k8s.container.name`, `k8s.deployment.name`, `k8s.replicaset.name` (= deployment adı + pod-template-hash) ve `container.image.name` + `container.image.tag` taşıyor. **Faz 1'in tüm girdileri o cluster'da var.** ⚠ Aynı gün ikinci bir ekran seti **öteki cluster'ın** span'lerinin `k8s.namespace.name` bile taşımadığını gösterdi (v0.10.190 düzeltmesinin kök nedeni) — yani kapsama **cluster başına** ölçülmeden "prod'da var" denemez; §1 R1'in %95 kapısı cluster kırılımlı olmalı. Doğrulanamayanlar: `k8s.statefulset.name` / `k8s.daemonset.name` / cronjob / job (o türden pod gerekir) ve `deployment.environment.name` (görüntüde yok). `docs/audit/entity-layer-discovery-2026-08-28.md:108-110` ve `docs/audit/k8s-entity-layer-2026-08-26.md:19-24` artık **bayat** — bu audit'ten sonra işaretlenmeli.
2. **Şema tarafı hazır bir makineye oturuyor.** Resource attribute'ları `Map` değil paralel dizi (`res_keys/res_values`, `store.go:1033-1036`); terfi kolonu üretimi + doluluk probe'u + dış-Distributed atlaması + "kolonu okuyan MV'yi kapıla" deseni tamamı kodda (`promoted_attr.go:57-84, 183-206, 310-425`; `store.go:3246-3252, 4109-4114`). Beş yeni terfi kolonu (`k8s_deployment`, `k8s_statefulset`, `k8s_daemonset`, `k8s_replicaset`, `container_image`) **beş satırlık** bir ekleme; MV `entity_seen_1m`'in yanına aynı şablondan. CHANNEL_CODE dersi (boş kolon ORDER BY önekinde) burada tekrarlanamaz — **kolon prod'da gerçekten doluysa** (varsayım 1): MV `WHERE k8s_replicaset != ''` ile boş satır üretmez, probe doluluğu veriyle kanıtlar — ama kapı olarak `k8s_coverage.go:121-128` sayaçlarına `k8s.replicaset.name` + `container.image.name` eklenmeden MV prod'a alınmamalı.
3. **Verilmiş kararların dördü ev kuralıyla çatışıyor; dördü de küçük düzeltmeyle uyumlanıyor.** (a) `workload_rollouts` düşük hacimli **state** tablosudur → ev deseni shard etmez, `stateTableDDL` birleşik replikasyon grubuna otomatik yollar (`state_replication.go:71-93`); operatörün `cityHash64(cluster, namespace, workload, revision)` anahtarı O5'e uygun ama **gereksiz** (dedup zaten tek grupta). (b) `ReplacingMergeTree(updated_at)` → ev kuralı 38/38 tabloda `ReplacingMergeTree(version)` + `version UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))`; `updated_at DateTime64(3)` düz kursör kolonu olarak kalır. (c) `PROJECTION by_time` → repoda çalışan hiçbir DDL'de PROJECTION yok; boot eleyicisi `ADD PROJECTION`'ı eleyemez (her boot'ta yeniden gönderir, `ddl_skip_existing.go:203-205`), CH 24.8'de RMT+projection `deduplicate_merge_projection_mode` kapısına takılabilir → **v1'den çıkar**. (d) `started_at` "ilk tespitte donar" → `time.Now()` ile damgalanırsa lider devri (lease TTL = 3 dk, `leader.go:88-95`) ve `lockDegraded` (Redis yok → **her pod lider**, `cache.go:140-142`) penceresinde aynı rollout iki `started_at` ile iki satır olur; **deterministik** damga (aktif kümeye giren ilk kovanın başlangıcı) şart.
4. **SSE: hub çoğullamaya hazır, frontend değil.** Sunucu zaten `event: <kind>` + `data: {"kind","payload"}` zarfı yazıyor (`sse.go:73-76, 295`); Redis köprüsü çok-pod fan-out'u taşıyor (`sse.go:213-246`). Ama frontend hub'ı **payload'ı hiç okumuyor** (`eventStream.ts:114` — `e.data` atılıyor), kind listesi kapalı (`eventStream.ts:43`), abone tamponu 32 ve dolunca **sayaçsız** düşürüyor (`sse.go:186-191`), replay/`Last-Event-ID` yok. "subscribe+buffer → snapshot → upsert" tasarımı ancak hub payload taşırsa anlamlı (Plan B, 6 dokunuş); önerim v1'de **Plan A** (rollout = invalidation sinyali + debounce'lu snapshot refetch) — kayıp toleranslı, hub sözleşmesine dokunmuyor. "Her pod CH'yi tail'ler" kararının repoda emsali VAR (`logstream_broker.go:138-165`, ileri kursör + gap işareti) — ama tail'in yayını **yalnız yerel fan-out** olmalı (`Broker.Publish` köprüye de basar → N pod × N tail = N× teslim); `Broker`'a `PublishLocal` gerekir.
5. **"Deployment Report" bir deploy dedektörü değil; "Rollouts" yeniden adlandırma değil yeniden yazımdır.** Bugünkü sayfa operatörün elle girdiği bir zamandan sonra **açık Problem'i olan servisleri** süzüp önce/sonra RED + anomali + exception tabloları çiziyor (`deployment_report.go:127-252`); deploy hunilerinin hiçbirini okumuyor. Korunacak parça: `redComparisonWindow` + `redStatsFor` + `scoreHealth` (satır başına **health verdict**) ve problem/anomali/exception daraltması (satır çekmecesi). Atılacak: elle `since`, "Generate", fleet-wide çerçeve. Rename kapsamı ~16 dokunuş + 5 test kapısı (§4.3). Ayrıca "rollout" sözcüğü repoda ZATEN pod-churn heuristiği demek (`chstore.Rollout`, `/api/services/{name}/rollouts`, `/deploys`'ta `🔁 rollout` rozeti) — yeni yüzey üç mevcut deploy yüzeyiyle çakışıyor; birleştirme planı §4.
6. **KSM tarafı (Faz 5) bugünden yarı hazır, yarısı ölçülmemiş.** `kube_replicaset_owner` + `kube_pod_owner` zaten çekiliyor ve Pod→RS→Deployment join'i Go'da yazılı (`client.go:973-996`, `entity/normalize.go:98-115`); `kube_replicaset_spec_replicas` / `_status_ready_replicas` / `_created` **hiç sorgulanmıyor** ve prod'da açık oldukları **kanıtsız** (KSM allowlist ile kısılabilir). `InstantSamples` `time=` göndermiyor ve zaman damgasını atıyor (`cluster_identity.go:310-323`) → "ready < desired N dk" ya range sorgusu ya dayanıklı durum ister; yeni range seam'i için ev kararı `internal/promapi` (`promapi.go:19-26`: "new callers use it"). Kabul kapısı: §7'deki PromQL listesi operatörce koşulmadan Faz 5 başlamaz.
7. **Span-taraflı "N dk span üretmedi" sinyali `status` yazmamalı.** Bu ürün aynı hatayı üç kez yaptı ve üçünde operatör bildirdi (v0.8.405, v0.9.451, v0.9.588 — `deploys.go:582-589, 626-641`, `tick_continuity.go`). Cron/batch, consumer, sağlıklı canary, telemetri flıker'ı, gece diurnal çukuru: beşi de 1m kova + 2 kova kuralında yanlış `stalled` üretir. Sinyal yalnız KSM `ready<desired` ile AND'lenir ya da nötr "henüz trafik yok" rozeti olur; reconciler kendi kör noktasını (`sweepIsTrustworthy`, `evaluator.go:1104`) tanımalı.
8. **Lokal doğrulama bugün İMKÂNSIZ; demo üreteci önce genişlemeli.** Demo `k8s.replicaset.name` ve `container.image.*` yaymıyor, `service.version` sabit `"1.0.0"`, namespace tek değer, `workload_kind` hep Deployment (`cmd/demo/main.go:477-505`). "Yerelde çalıştı" cümlesi hiçbir kolu icra etmemiş olur ([[feedback-local-data-is-a-fixture]]). Faz 1'in ön koşulu: demo'ya RS hash'li replicaset adı + değişken image tag + ≥2 namespace + bir STS senaryosu.

---

## 0. Verilmiş kararlar ↔ kod: uyum tablosu

| Karar | Kodla durumu | Hüküm |
|---|---|---|
| Kimlik `(cluster, namespace, workload, revision, started_at)`, revision = RS adı | RS adı prod span'inde VAR (ekran görüntüsü); repoda bugün hiçbir kod yolu `k8s.replicaset.name` okumuyor (sıfır eşleşme); KSM tarafında RS adı ara değer olarak var (`entity/samples.go:74-77`) ama kalıcılaştırılmıyor | ✅ uyumlu; iki taraf da yeni kolon/alan ister |
| workload coalesce(deployment → statefulset → daemonset) | Prod'da deployment VAR; STS/DS anahtarları repoda hiç geçmiyor, prod'da kanıtsız; `looksStatefulSetName` sezgisi var (`pod_inventory.go:102-120`) | ✅ uyumlu; STS/DS için terfi kolonu + prod kanıtı |
| `started_at` ilk tespitte DONAR | Doğru ilke, ama damga `time.Now()` olursa çift-yazıcı pencerelerinde iki satır (§2.6) | ⚠ deterministik damga şart (§2.5) |
| Tek yazıcı: iki dedektör liderde, bellekte merge, tek upsert | Birebir emsal sevk edilmiş: `entity.Syncer` aynı tikte Thanos + span-türevi okuyup birleştiriyor (`syncer.go:198-199, 283`), lider kapısı `LeaderHolder` | ✅ kopyalanacak desen hazır |
| Kanonik cluster adı = registry değeri; collector statik `k8s.cluster.name`; KSM registry'den yazar | Registry `Name` **yeniden adlandırılabilir**, yalnız `ID` korunur (`cluster_identity.go:218-219`); span değeri → kayıt eşlemesi zaten var (`SpanClusterKeys`, `GroupSeenByCluster`, `spanpass.go:37-45`); chart'ta `resource` processor YOK ama prod span'i `k8s.cluster.name` taşıyor | ⚠ kolona `EffectiveID()` yaz, adı okuma anında çöz (önerim); değeri registry ile eşitleme planı §8 |
| `workload_revision_activity_1m` 1 dk AggregatingMergeTree MV | `entity_seen_1m` birebir şablon (`entity_schema.go:37-59`); combined form; `span_count(sum)` → `countState()`, `image(any)` → `anyLastSimpleState` | ✅ şablon var (`SETTINGS index_granularity = 8192`, `entity_schema.go:44`); `service_name` boyutu GÜN BİR eklenir (§1.3, MV tek atış); SimpleAggregateFunction istenirse TO-form |
| `workload_rollouts` RMT(`updated_at`), Nullable image, `detected_by` | Ev kuralı RMT(`version`) 38/38; Nullable ev kuralına aykırı (C4: sentinel `''`) ama MV'den bağımsız state tablosunda kabul edilebilir | ⚠ `version` kolonu; Nullable operatör kararı |
| Sharding `cityHash64(cluster, namespace, workload, revision)` | State tabloları shard EDİLMİYOR (0009 birleşik grup, `entities` emsali `0011:82-117`); `defaultShardPolicy`'de çok-argümanlı anahtar emsali yok (`cluster.go:374-462` hepsi tek argüman) | ⚠ shard etme (önerim); ısrar edilirse O5 sağlanıyor |
| `PROJECTION by_time` | Çalışan DDL'de sıfır emsal; boot eleyicisi eleyemez; CH 24.8 RMT kapısı | ⚠ v1'den çıkar |
| DDL = mevcut migration örüntüsü + ON CLUSTER | 0011 hem boot hem `migrations/` (ikili bakım sözleşmesi `0011:10-18`) | ✅ 0012 aynı şekilde |
| Aktif küme `HAVING sum(span_count) >= eşik`, histerezis ≥2 kova, overlap 30 dk | Karar veren her şey 5 dk kovada; `DwellBuckets: 3`, `lookback = 3`, `MinCount: 10` emsalleri | ✅; eşik önerileri §11 |
| `stalled` KSM'den; span sinyali zayıf | RS düzeyi ready/desired sorgulanmıyor; zaman serisi için range seam yok | ⚠ Faz 5 ön koşulları §7 |
| Pod'lar CH'yi tail'ler (cursor `updated_at`, watermark −3 s, FINAL+LIMIT) | Emsal `logstream_broker.go` (pod-yerel, ileri kursör, gap); tail sonucu yalnız yerel fan-out olmalı | ✅ + `PublishLocal` |
| SSE yeni bağlantı YOK, `event: rollout` çoğullama | Sunucu hazır; FE hub kind listesi kapalı + payload'sız | ⚠ karar: Seçenek T + Plan A (§3, §13.6) |
| `registerRolloutRoutes()`, api.go büyümez | 29 emsal; `deployment_report.go:21-23` zaten bu desende | ✅ |
| FE: subscribe+buffer → snapshot → upsert; sıralama `started_at`'e sabit | Payload'sız hub'da buffer anlamsız; `useDataTable` sıralamayı kullanıcıya açar (depo ilkesi) | ⚠ Plan A + `initialSort` (önerim) |
| Pivot linkleri `k8s.replicaset.name` filtresiyle | `resource.` önekli filtre terfi kolonuna otomatik düşer (`filterexpr.go:231-234`) — kolon eklenince indeksli | ✅ Faz 1'e bağlı |
| Eski rapor → agregat sekmesi | Eski rapor problem-tabanlı; agregat (sıklık/rollback/süre) `workload_rollouts` üstünde yeni sorgu | ⚠ yeniden yazım, §4 |
| Collector gereksinimleri | Chart extract listesi 6 anahtar (`otel-collector.yaml:35-40`), `resource` processor yok, `pod_association` yalnız `connection`; RBAC replicasets var (`otelcol-rbac.yaml:30`); prod çıktısı gereksinimin çoğunu zaten karşılıyor | ✅ chart + operatör dokümanı §10 |

---

## 1. `spans` şeması, terfi kolonları, MV

### Mevcut durum
- **Depolama:** `attr_keys/attr_values` + `res_keys/res_values` paralel diziler (`Array(LowCardinality(String))` / `Array(String) CODEC(ZSTD(3))`), `MergeTree`, `PARTITION BY toDate(time)`, `ORDER BY (service_name, time)` (`store.go:1033-1045`). Ingest her resource attribute'unu **koşulsuz, kırpmasız** diziye yazar (`convert.go:502-513`) → collector'ın bastığı yeni anahtar için `internal/otlp/` değişmez.
- **Terfi makinesi** (`promoted_attr.go`): `promotedAttr{col, keys, res, fallback}` (57-71); ifade `coalesce(nullIf(res_values[indexOf(res_keys,'k')],''),…,'')` (107-118); DDL `ADD COLUMN … LowCardinality(String) MATERIALIZED` + `ADD INDEX … TYPE set(0) GRANULARITY 4` (192-206; tip **sabit LC**, düz String gerekirse struct'a `typ` alanı); boot onarımı `repairPromotedAttrCols` (310-372, dış Distributed'da hiç ALTER göndermez 311-315); **doluluk probe'u** `probePromotedAttrs` (391-425: kolon, dizi aramasıyla aynı sonucu veriyor mu — `seen == 0` ise kaydetmez). Kararlı durumda boot başına DDL sıfır (`320-323`).
- **Bugünkü terfi kolonları:** `cluster` (6 yollu `clusterDeriveExpr`, `repo.go:307-315`; düz kolon 8.7 MiB/11 ms vs coalesce 137 MiB/81 ms `repo.go:328-331`), `k8s_namespace`, `k8s_pod` (fallback `host_name`, prod'da %98 eşit), `k8s_node`, `attr_channel_code`, `attr_function_code` (`promoted_attr.go:73-84`). Skip index'ler: `idx_trace` bloom_filter(0.01), `idx_name` set(0) (`store.go:1041-1042`), `idx_kind`/`idx_db_system`/`idx_status` set(0), `idx_http_status` minmax (`store.go:2693-2703`).
- **MV'yi kapılama deseni:** `hasK8sPodCol := k8sPodColExists || ensuredPromoted["k8s_pod"]` (`store.go:3251-3252`) → MV yalnız doğruysa `mvs`'e (`store.go:4109-4114`); gerekçe `store.go:3246-3250`: kolonsuz MV **her span INSERT'ini kod 47 ile düşürür**.
- **`spans` üstündeki MV'ler:** 16 sabit + 2 koşullu = 18 (`service_summary_5m` … `entity_seen_1m/5m`, hepsi combined `AggregatingMergeTree`; tek TO-form `span_links_reverse_mv` `store.go:3977`), prod'da + 0001/0002 rollup MV'leri. **Dakikalık k8s agregası zaten var:** `entity_seen_1m` `ORDER BY (service_name, cluster, k8s_namespace, k8s_pod, time_bucket)`, `WHERE k8s_pod != ''`, TTL 3 g (`entity_schema.go:32-59`). Deploy ailesinin MV'si `service_version_5m` (`store.go:3629-3651`, `minState(time)` gerekçesi 3623-3625 — `first_span_at` sorununun aynısı).
- **MATERIALIZED vs DEFAULT:** kural kodda: "DEFAULT columns are forwarded, only MATERIALIZED are erased" (`store.go:2992-2993`, CH PR #7377); INSERT adlandırılmış kolon listesi (`repo.go:80-123`) MATERIALIZED içermez → beş yeni kolon INSERT'i yapısal olarak kıramaz.
- **CHANNEL_CODE/FUNCTION_CODE dersi** (`0007_rollup_case_repair.sql:12-25`, `promoted_attr.go:9-40`): MV yalnız `'CHANNEL_CODE'` okudu, prod küçük harf yazıyordu → iki kolon sabit boş ve **ORDER BY öneki + bloom index'te** → birincil anahtar hiçbir şey elemiyor, filtreler sessizce boş. Kök neden: probe **varlığı** kanıtlıyordu, **doluluğu** değil. Onarım `DROP+ADD` (MODIFY eski part'ı onarmaz) + `MODIFY QUERY`. Üç kural: yazımı prod verisiyle kanıtla; probe doluluk kanıtlasın; boş boyut, olmayan boyuttan kötüdür.
- **Prod / demo gerçekliği:** prod (ekran görüntüsü 2026-08-30) tüm Faz 1 anahtarlarını taşıyor; demo `k8s.deployment.name` + `k8s.cluster.name` + `k8s.pod.name` yayıyor, `k8s.replicaset.name` ve `container.image.*` yaymıyor (`cmd/demo/main.go:477-505`, `486-489` yorumu "yerelde hiç çalışmayan dal"). `k8s.replicaset.name` repoda sıfır kod yolu.
- **Alternatif (kolonsuz):** revision `k8s_pod`'dan `podTemplateHash` (`deploys.go:662-684`) ile türetilebilir → prod'da `spans_local` ALTER'ı gerektirmez; bedeli hash ≠ RS adı, DeploymentConfig `<ad>-<no>-<rand>` biçiminde deploy numarası yakalanır. **Önerilmez** (operatör kimliği RS adı) ama Faz 1 gecikirse geçici köprü.

### Önerilen değişiklik
1. `promotedAttrs`'a altı satır (`promoted_attr.go:84` altına; altıncısı `container_image_tag`, bkz. madde 2):
   ```go
   {col: "k8s_deployment",  keys: []string{"k8s.deployment.name"},  res: true},
   {col: "k8s_statefulset", keys: []string{"k8s.statefulset.name"}, res: true},
   {col: "k8s_daemonset",   keys: []string{"k8s.daemonset.name"},   res: true},
   {col: "k8s_replicaset",  keys: []string{"k8s.replicaset.name"},  res: true},
   {col: "container_image", keys: []string{"container.image.name", "k8s.container.image.name"}, res: true},
   ```
   Üç ayrı workload kolonu (tek coalesce değil): `workload_kind` `multiIf` ile MV'de üretilir, her kolon **kendi doluluk probe'unu** alır; mevcut kolona anahtar eklemek DROP+ADD onarımı tetiklediği için (`promoted_attr.go:185`) ilk günden ayrı. `container_image` LC kararı C3 kuralı: `uniq(res_values[indexOf(res_keys,'container.image.name')])` 24 s'te < ~100k ise LC (`entity_layer_admin.go:110` `entityLayerLCGate` emsali); image adı registry yolu içerir ama distinct sayısı ≈ imaj sayısı → LC beklenir, ölçülsün. **İndeks tipi kararı:** `set(0)` granül başına distinct değer kümesi saklar — düşük kardinaliteli (granül başına az farklı değer) kolonlarda ucuz ve kesin; `bloom_filter(0.01)` granül başına sabit boyutlu, yüksek kardinalitede (trace_id gibi) tercih edilir. `k8s_replicaset` bir granülde (8192 span) tipik olarak 1-20 farklı değer taşır (aynı workload'ın pod'ları ardışık yazılır) → `set(0)` hem daha küçük hem `=`/`IN` için kesin; `container_image` benzer. `k8s_deployment/statefulset/daemonset` zaten düşük kardinalite → `set(0)`. Bloom yalnız `LIKE`/yüksek kardinalite senaryosunda üstün — burada gerekmiyor; `promotedAttrDDL` sabit `set(0)` (`promoted_attr.go:201-204`) **değiştirilmez**. Dokümanın soru başlığındaki "bloom filter" isteği bu gerekçeyle `set(0)` olarak cevaplanır.
2. **Image tag** — v0.10.193 kararı (bu audit'ten sapma, gerekçeli): `effectiveVersionExpr` (`deploys.go:87-104`) yedi anahtarlı `indexOf` zinciridir; MV'de kullanılsaydı INSERT başına dizi araması koşardı (pod_inventory.go'nun bilinçle ertelediği maliyet sınıfı). Bunun yerine **altıncı terfi kolonu** `container_image_tag` (`container.image.tag`, `k8s.container.image.tag`) — MV düz kolon okur; deploy ailesinin zinciri dokunulmadan kalır. `image` (ad) + `image_tag` iki kolon.
3. MV — `entity_seen_1m`'in **yanına**, aynı şablondan, combined form (TO-form `adaptDDL`'de `TO` hedefini `_local`'e çevirmez → her shard Distributed'a yazar, `cluster.go:1113-1120`):
   ```sql
   CREATE MATERIALIZED VIEW IF NOT EXISTS workload_revision_activity_1m
    ENGINE = AggregatingMergeTree
    PARTITION BY toDate(bucket)
    ORDER BY (cluster, k8s_namespace, workload, revision, service_name, bucket)
    TTL toDate(bucket) + INTERVAL 7 DAY
    SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1
    AS SELECT
      toStartOfInterval(time, INTERVAL 1 MINUTE)                       AS bucket,
      cluster, k8s_namespace,
      multiIf(k8s_deployment != '', k8s_deployment,
              k8s_statefulset != '', k8s_statefulset,
              k8s_daemonset != '', k8s_daemonset, '')                   AS workload,
      multiIf(k8s_deployment != '', 'Deployment',
              k8s_statefulset != '', 'StatefulSet',
              k8s_daemonset != '', 'DaemonSet', '')                     AS workload_kind,
      k8s_replicaset                                                   AS revision,
      service_name,
      anyLastSimpleState(container_image)                              AS image,
      anyLastSimpleState(container_image_tag)                         AS image_tag,
      countState()                                                     AS span_count,
      minState(time)                                                   AS first_seen,
      maxState(time)                                                   AS last_seen
    FROM spans
    WHERE k8s_replicaset != '' AND workload != ''
    GROUP BY bucket, cluster, k8s_namespace, workload, workload_kind, revision, service_name
   ```
   `service_name` boyutu **gün bir** (MV tek atış): workload → servis eşlemesi (health verdict, §4.1) buradan okunur; satır çarpanı workload başına servis sayısı (tipik 1-2). **Maliyet tahmini (ölçüm değil, akıl yürütme):** satır sayısı ≤ `entity_seen_1m`'in satır sayısı × (servis/pod oranı ≤ 2) / (pod/revision oranı ≥ 1) — pratikte `entity_seen_1m`'in altında; ingest yolunda 19. MV'nin marjinali `entity_seen_1m` ile aynı sınıf (aynı kaynak kolonlar, `WHERE` ile k8s bağlamsız span'ler dışarıda). Kesin sayı Faz 1 sonrası `system.parts` + `query_log` ile.
   Kapı: `hasRSCol := rsColExists || ensuredPromoted["k8s_replicaset"]` (+ deployment kolonu) — `store.go:3251` hizası; `mvs`'e koşullu ekleme + atlama logu (`store.go:4109-4114` kopyası). MV `IF NOT EXISTS` ile yaratılır → **SELECT/ORDER BY ilk sürümde doğru olmak zorunda** (kolon eklemek DROP+RECREATE, `SKILL.md:59,207`) — bu yüzden `service_name`, `image` (ad) ve `image_tag` kararları **bu audit'te kapanır** (§13'te açık bırakılmaz).
   Satır hacmi `entity_seen_1m`'in altında (revision pod adının öneki; N pod → 1 satır); `WHERE` k8s bağlamsız span'i hiç sokmuyor. Uzun vadede `entity_seen_1m` ile birleştirme (DROP+RECREATE, 3 gün veri) tek MV'ye indirir — v1 değil.
4. **Gün-bir üç kayıt** (`entity_schema.go:28-29` kuralı, v0.5.426): `tablesWithoutTraceID` (`cluster.go:301-302`), `defaultShardPolicy` (`cluster.go:431-432` — emsal `entity_seen_1m: cityHash64(service_name)`; öneri `cityHash64(cluster, k8s_namespace, workload)` — MV'de anahtar dekoratif, insert trigger shard-yerel yazar), `highVolumeTables` (`cluster.go:991-992`); kayıt testi `entity_schema_test.go:112-119` şablonu.
5. Kapsama kartı: `k8s_coverage.go:121-128`'e `rs` (`k8s.replicaset.name`) ve `img` (`container.image.name`) sayaçları; `clus` sayacı `k8s.cluster.name` ile `openshift.cluster.name`'i **ayırsın** (tasarım statik `k8s.cluster.name` istiyor, bugünkü OR ikisini birleştiriyor). Sorgu örneklemeli (`LIMIT %d BY service_name`) — oran ölçer, mutlak değil.
6. Demo üreteci: `k8s.replicaset.name` = `<deploy>-<hash>`, `container.image.name/tag` değişken (rollPodGeneration ile tag bump), ≥2 namespace, bir STS senaryosu (`cmd/demo/main.go:477-505`, `1323-1348`, `1834-1836`).

### Dokunulacak dosyalar
`internal/chstore/promoted_attr.go:73-84` (+`typ`/`idxType` alanı gerekirse `:196`) · `internal/chstore/rollout_schema.go` (YENİ; `entity_schema.go` şablonu, TTL sabitleri) · `internal/chstore/store.go:3251-3252, 4109-4114` (kapı + MV) · `internal/chstore/cluster.go:301, 431, 991` · `internal/chstore/k8s_coverage.go:121-128` · `internal/chstore/deploys.go` (`effectiveVersionExpr` referansı; 7-anahtarlı `has()` OR zinciri 6 yerde kopyalı — MV 7. kopya olmasın, sabit paylaşılsın) · `cmd/demo/main.go` · `migrations/0012_*` (§5).

### Risk / geri alma
- **R1 (0007 sınıfı):** kolon prod'da beklenenden boş → MV **boş kalır, yanlış olmaz**; kapı = kapsama sayaçları ≥ %95 olmadan MV prod'a alınmaz.
- **R2:** MV kaskadı ingest'i düşürür (0005 dersi, `0011:35-36`) → MV adımı **en son**, kolonlar her shard'da doğrulandıktan sonra; sihirbaz "Geri al" yalnız MV'yi düşürür.
- **R3:** app-managed / operator-managed sapması: 0011 yalnız `idx_k8s_pod/idx_k8s_node` ekliyor, Go döngüsü her terfi kolonuna indeks ekliyor → 0012'de beş indeksin beşi yazılsın.
- **R4:** `existingColumns` `x` ve `x_local`'i aynı anahtara indirger (`ddl_skip_existing.go:162-163`) → migration'da kolon `spans_local` + `spans` için **ayrı ayrı** yazılır (0011 ADIM 1-2).
- **Geri alma:** MV `DROP … SYNC` (ingest anında normale döner) → tablo → `DROP INDEX` → `DROP COLUMN` (ters sıra Code 47, `0011_rollback:8-10`); app tarafında `promotedAttrs` satırlarını kaldırmak + kapıyı `false`'a sabitlemek; ledger/down-migration yok (`SKILL.md §8`).

---

## 2. Lider seçimi, zamanlayıcı, reconciler

### Mevcut durum
- **Kilit:** `internal/cache/leader.go` `LeaderHolder` (kendi implementasyonu, redsync yok): `NewLeaderHolder(lock, key, ttl)` (104), `LeaderTTL(interval) = clamp(3×interval, 30 s, 10 dk)` → **1 dk aralık için TTL 3 dk**, refresh ttl/3 (88-95, 112); `IsLeader()` atomik (128-130); ctx bitince 2 s'lik taze ctx ile bırakma (160-169) ama `main.go`'da WaitGroup yok → release süreç çıkışıyla yarışır, kaçırılırsa devir TTL kadar (3 dk) sürer; edinim denemesi 10 s'de bir (150-155). `SetOnAcquire(fn)` **her edinimde kendi goroutine'inde** koşar (186-187) → periyodik tick ile örtüşebilir; refresh lease'i aşarsa demote (220-231). Redis: `SetNX` / token'lı Lua `PEXPIRE` / `DEL` (`redis.go:419-481`).
- **Dejenere kilit:** Redis yok/erişilemez → `noopLock.TryAcquire` ve `Refresh` **hep true** (`cache.go:140-142`) → `lockDegraded` (`main.go:574-579`) penceresinde **N pod N lider, hiç demote olmaz**; `SwitchableLock` Redis gelince sıcak takas (`main.go:567-568`). Sağlıklı boot'tan SONRA Redis düşerse: `StartRedisReprobe` yalnız `lockDegraded` iken kurulu (`main.go:1215-1223`) → herkes demote olur, **kimse lider olmaz** (tespit boşluğu).
- **Roller:** `parseRunMode` (`main.go:113-129`); arka plan döngüleri `if mode.worker` bloklarında (`main.go:631, 782, 980, 1018, 1105/1135`); entity syncer + thanos label check `main.go:1018-1047`. `StartExceptionTriageRefresh` **her rolde** koşar (`main.go:1162-1163`, hiçbir mode bloğunda değil).
- **Tick şablonları:** (a) main.go serbest fonksiyon (`runExceptionRefresher` 1481-1524, `runServiceTeamDeriver` 1612-1656); (b) paket içi `Start(ctx)` — evaluator `runIfLeader` + `tickDeadline` (`evaluator.go:172-209`), retention enforcer (`retention_enforce.go:171-200`; doc yorumu 165-170 bayat, kod uzun ömürlü holder); (c) **`entity.Syncer`** — `Run(ctx, isLeader)` aralığı her turda ayardan okur (`syncer.go:156-168`; ilk tick bir aralık gecikir 163-167 → `SetOnAcquire` şart), `inFlight` CAS (180-186), `SetLeaderCheck` API tetikli tick'i lidere kapatır (124-138), cluster başına 30 s alt-ctx + `ParallelClusters` semaforu (227-241, 281-284), **aynı tikte Thanos + span-türevi** (`syncer.go:198-199, 283`), hata veren cluster'da hiçbir ömür kapanmaz (16-19, 269-280). Koşu kaydı `entity_sync_runs` (`0011:119-136`; yalnız `RunOK`/`RunFailed` yazılıyor, `RunPartial`/`RunSkipped` tanımlı ama kullanılmıyor `syncer.go:71-74, 223, 272, 335`).
- **Lider olmayan pod'lara ulaşma:** Redis TTL anahtarı (evaluator heartbeat), `system_settings` blob + 30 s poll (`cluster_detect.go:164-265`; gerekçe "lider worker'ın belleği api pod'larından görünmez"), CH state tablosu + istek anında okuma, SSE (`notify.Notifier` tek yayıncı, `notify.go:456-460`, Redis köprüsü `main.go:608-609`).
- **CH tail emsali:** `logstream_broker.go` — filtre başına tek poll grubu, `cursor atomic.Int64` (75), refcount abone (97-131), sınırlı mailbox + `gap` (174-190), 12 s tick ctx (138-165), kursör **anahtarda değil** (49-53), `tailStep` saf + testli (136-137). `updated_at`+watermark'lı tail bugün yok.
- **Cluster eşlemesi hazır:** span ham değeri → kayıt id'si (`entity/identity.go:5-6`, `spanpass.go:34-45`, eşlenemeyen `(unmapped)` sayacına).

### Önerilen değişiklik
1. Yeni paket `internal/rollout/` (`entity`/`topology` emsali): `Reconciler{store, ksm KSMSource, clusters, leader, inFlight atomic.Bool, runs}`; kilit anahtarı **kendine ait** `coremetry:lock:rollout-reconciler` (entity kilidini paylaşma: entity bayrağı kapalıyken rollout koşmalı); `cache.LeaderTTL(interval)`.
2. `main.go` `if mode.worker` bloğuna tek satır, `main.go:1048` sonrası (`store`, `lockImpl` 568, `thanosSvc` 998, `entitySettings` 1012, `notifier` 583, `bus` 600 kapsamda).
3. Döngü: `SetOnAcquire(tick)` → `Start` → ticker; `Tick`: `inFlight` CAS (**zorunlu** — onAcquire ile örtüşme) → `IsLeader` → `tickDeadline` ctx → aktivite sorgusu (5 dk karar kovası, MV'den `countMerge/minMerge/maxMerge` — `EntitySeenRecent` `entity_store.go:195-221` kardeşi) → KSM (Faz 5) → **saf `Merge(prevState, act, ksm) → []Rollout`** (tablo-testli) → tek upsert → `rollout_reconcile_runs` kaydı. Aralık/kova ayardan (`entity.Resolved` deseni) → yeniden başlatma gerekmez.
4. **Durum makinesi** (saf): `in_progress` (revizyon aktif kümeye girdi, ≥2 ardışık 5 dk kova ≥ eşik) → `completed` (önceki revizyon(lar) aktif kümeden çıktı **ve** overlap ≤ 30 dk; tooltip iki aşama: `pods_ready_at` (KSM), `traffic_confirmed_at` (span)) / `rolled_back` (aktif kümeye giren revizyonun `first_seen`'i ≥ N saat eski **ya da** daha önce `completed` kaydı var) / `stalled` (yalnız KSM, Faz 5) / multi-revision steady state (overlap > 30 dk → `completed` yazılmaz, `note`); `sweepIsTrustworthy` benzeri süreklilik kapısı (`evaluator.go:1104`, `tick_continuity.go`): reconciler koşmadığı süre "sessizlik" sayılmaz.
5. **`started_at` deterministik:** aktif kümeye giren **ilk 5 dk kovanın başlangıcı**; `first_span_at = minMerge(first_seen)` ayrı; böylece çift yazıcı (lease crossover 3 dk / `lockDegraded`) aynı satırı üretir, RMT birleştirir.
6. Cluster damgası: span ham değeri → `GroupSeenByCluster` ile kayıt → kolona `EffectiveID()`; KSM ayağı zaten kayıt bazlı. Ad okuma anında `ClusterByID` (**yalnız etkin kaydı döndürür**, `cluster_identity.go:159-171` → pasif kayıt için ad çözümü ayrı yol).
7. Yayın: §3'teki tek-kanal kararı (Seçenek T — pod tail'i `internal/api/rollouts.go` içinde `Broker.PublishLocal`; reconciler HİÇ yayın yapmaz). Seçenek L seçilirse reconciler `notify.EventPublisher`'ı (`notify.go:178-180`) alır ve upsert sonrası `Publish(sse.KindRollout, row)` çağırır — iki yol aynı anda değil.

### Dokunulacak dosyalar
`internal/rollout/{reconciler,activity,merge,state}.go` + `_test.go` (YENİ) · `internal/chstore/rollouts.go` (aktivite sorgusu, upsert, tail okuması, runs) · `main.go` (~1048, 1 satır) · `internal/rollout/settings.go` (`system_settings["rollouts"]` blob: enabled, interval, bucket 1m/5m, threshold, hysteresis, overlap, stalledMin; `entity.NewSettingsService` emsali).

### Risk / geri alma
- **Çift yazıcı** (lease crossover ~3 dk, `lockDegraded` süresiz) → deterministik `started_at` + idempotent upsert; `lockDegraded`'da reconciler'ı durdurmak operatör kararı (bugün hiçbir worker durmuyor).
- **Tespit boşluğu** (boot sonrası Redis düşerse kimse lider değil) → koşu satırı + `/api/health` rozeti ("reconciler son koşu X dk önce"); `started_at` deterministik olduğu için kesinti sonrası aynı revizyon GEÇ değil DOĞRU damgayla açılır (MV geçmişi 7 gün).
- **Worker tick bütçesi** (evaluator/anomaly/topology/entity aynı pod) → `tickDeadline`, MV okuması, 5 dk karar kovası.
- Geri alma: `main.go` satırı + ayar `enabled=false` (tick ilk satırda no-op); tablo kalır.

---

## 3. SSE hub'ı ve `event: rollout`

### Mevcut durum
- **Broker** (`internal/sse/sse.go`, tek dosya): pod-içi `map[chan<- Event]`; `Publish` = `localFanout` + (varsa) Redis köprüsü (213-246, köprü publish 200 ms zaman aşımı 241-245); köprü `bus.SetBridge(cacheImpl)` `main.go:608` (Redis URL boşsa `NewNoop` → köprü ölü; `lockDegraded` re-probe'da `StartBridge` yeniden 1216-1220); kendi echo'sunu düşürür, gelen olayı **yeniden PUBLISH etmez** (167-174, 179-190). Zarf: `Event{Kind, Payload json.RawMessage}` (73-76), handler `event: <kind>\ndata: <TAM zarf>\n\n` (295) → FE'de satır `JSON.parse(e.data).payload`. Abone kanalı 32 tamponlu, dolunca **sayaçsız/logsuz düşürme** (186-191, 269); Redis tarafı 64 (`redis.go:236`). 15 s `: ping` (282). Abone-başına filtre/yetki yok (197, 182-193). `id:`/`retry:`/`Last-Event-ID` yok.
- **Uç:** `GET /api/events` koşullu `s.bus != nil` (`api.go:698-699`); `/api/logs/stream` de doğrudan api.go'da (830) — SSE uçları api.go'da mevcut norm; `TestMuxRoutePatterns` `/api/events`'i kapsamaz. Üçüncü akış: `POST /api/copilot/chat` `text/event-stream` (`copilot_chat.go:156`).
- **Üreticiler:** dört çağrı, hepsi `internal/notify` (`problem.open/resolve/acknowledge` 456-460, `runbook.complete` 988), `Notifier.Publish` pass-through (354-360), `EventPublisher` arayüzü (178-180). Go'da kind sabiti yok. **Sürüklenme:** FE `anomaly.open/clear`'a abone ama Go yayınlamıyor; Go `problem.acknowledge` + `runbook.complete` yayınlıyor, FE dinlemiyor.
- **Frontend:** `useEventStream` yalnız `AppShell.tsx:91`; Web Locks ile tek lider sekme, `BroadcastChannel` fan-out (`eventStream.ts:80-86, 103`), degrade yol sekme-başına (130-138, ilk açılışta catch-up yok 133); handler `const h = () => { applyInvalidations(qc, kind); bc.postMessage({ kind }); }` (**payload atılıyor**, 114); `EVENT_KINDS` kapalı liste (43); `eventInvalidations.ts` union (12-14) + switch (16-32, bilinmeyen kind `[]`) + catch-up (38-46); reconnect'te `catchupInvalidations` (106-112). `bc.onmessage` tipi `{kind}` (67). Vitest pini `toHaveLength(4)` problem kolunun ANAHTAR sayısı — yeni kind eklemek kırmaz (`eventInvalidations.test.ts:18`).
- **Bağlantı bütçesi:** `docs/audit/sse-http2-multitab-audit.md` — HTTP/1.1 origin başına ~6 bağlantı hipotezi (4-8), durum "ONAY BEKLİYOR" (3); kod tarafında EventSource çağrı yeri iki (`eventStream.ts:103, 131` aynı uç; `Logs.tsx:519`). "Yeni bağlantı açma" doğrulanabilir ölçüsü bu grep.
- **Yayın enjeksiyon noktaları:** `notify.EventPublisher` / `Notifier.Publish` / `Server.bus` (`api.go:180, 436`).

### Önerilen değişiklik
1. `internal/sse/sse.go`'ya kind sabitleri (`KindRollout = "rollout"` + mevcut dört) — sürüklenmeyi kapatan tek liste; FE `EVENT_KINDS` ile eşitliği pinleyen bir test (Go sabitlerini kaynak-tarayan vitest ya da tersi).
2. **Tek yayın kanalı kararı** (operatör):
   - **Seçenek T (tasarım):** her api pod'u `workload_rollouts`'u tail'ler (`logstream_broker` deseni: tek poll grubu, kursör `updated_at`, watermark `now64(3) − 3 s`, `FINAL … ORDER BY updated_at LIMIT 500`, gap işareti) ve **yalnız yerel** fan-out yapar → `Broker`'a `PublishLocal(kind, payload)` eklenir (köprüye basmaz; aksi hâlde N pod × N tail = N× teslim). Redis'siz de çalışır, tek yazıcıyı korur. Maliyet: pod × 1 sorgu / 3-5 s, küçük tabloda ihmal edilebilir.
   - **Seçenek L:** yalnız lider reconciler upsert sonrası `notifier.Publish("rollout", row)`; köprü diğer pod'lara taşır. En az kod; Redis yoksa lider dışı pod'lar olay görmez (poll'a düşer).
   - İkisi birden → çift teslim. **Önerim T** (tasarımla uyumlu, Redis'e bağımsız), payload = tek satırlık özet (kimlik + status + image/prev_image + started_at + updated_at).
3. **FE Plan A (önerim, v1):** `EVENT_KINDS`'e `'rollout'`, `eventInvalidations` `case 'rollout': [keys.rollouts.all]` + catch-up; sayfa `useQuery(keys.rollouts.list(f))` + `refetchInterval ≥ 10 s` (kayıp toleransı); rollout dalgasında N olay → N refetch → handler'da ≥500 ms debounce (bugün yok, yazılacak). "subscribe+buffer → snapshot" sırası gerekmez: refetch'in kendisi snapshot.
   **Plan B (operatör ısrar ederse):** payload taşıyan hub — `eventStream.ts:113-115` + degrade yol `134-137` handler'ları zarfı açar (`try/catch`, hata → eski invalidation yolu), `bc.postMessage({kind, payload})` + `bc.onmessage` tipi (67), modül düzeyi `onAppEvent(kind, cb)`, sayfada t0 subscribe → t1 snapshot → t2 drain (`updated_at` monotonluk: küçük/eşit yok sayılır) → t3 doğrudan upsert; kimlik `${cluster}|${ns}|${workload}|${revision}|${started_at}`; tavan `accumulatePage` (`logAccumulate.ts:26-29`, sessiz kayıp yasak). Risk: `eventStream.ts` uygulamanın **tek** canlılık kanalı.
4. Düşen olay sayacı: `sse.go:188-190` `default:` dalına atomik sayaç + `/api/health` alanı (bugün gözlemlenemez).

### Dokunulacak dosyalar
`internal/sse/sse.go` (sabitler, `PublishLocal`, drop sayacı) · `internal/api/rollouts.go` (tail goroutine'i `Server.bus.PublishLocal` ile; `StartRolloutTail(ctx)` her rolde değil YALNIZ `mode.api`'de — `StartExceptionTriageRefresh` emsalinin aksine, `main.go:1162` her rolde koşuyor) · `main.go` (`mode.api` bloğunda tek çağrı) · `frontend/src/lib/queries/{eventInvalidations,eventStream,keys}.ts` (+ Plan B'de `rolloutBuffer.ts`) · `eventInvalidations.test.ts` (yeni `it`, uzunluk pini kırılmaz).

### Risk / geri alma
- Redis'siz çok-replika: kilit dejenere (**tek yazıcı ihlali**, §2) — SSE'den ağır; snapshot+poll onarmaz.
- 32 tamponlu sessiz kayıp → poll fallback açık kalır; Plan B'de payload zarfı parse hatası tüm invalidation'ı öldürebilir → try/catch zorunlu.
- Payload'da cluster/namespace/image tüm oturumlara gider (abone filtresi yok) — tek-kiracı kabul.
- Geri alma: kind'ı iki listeden çıkar → sayfa poll'a düşer; tail goroutine'i tek çağrı.

---

## 4. Deployment Report bugün → "Rollouts"

### Mevcut durum
- **Uç:** `GET /api/deployment-report?since=<unix ns>&ownerTeam&sreTeam` (`deployment_report.go:21-23`, `api.go:929` tek satır — istenen desenin mevcut emsali); rol kapısı yok; `serveCached` 30 s, anahtar tüm girdiler (289-292); `since` zorunlu, gelecek → 400 (274-285); `?refresh=1` bypass (`cache.go:217-218`; `docs/DEPLOYMENT-REPORT-TESTING.md` buna dayanıyor).
- **Hesap:** deploy hunilerinin **hiçbiri** kullanılmıyor; `OpenProblemsSnapshot` → `filterOpenProblemsSince` (134-138) → `enrichProblemsForRead` (145) → takım daraltma (`servicesForTeam`/`intersectServices` 162-175; **nil = kısıtlama yok, boş dilim = hiçbiri** — `deployment_report_test.go:57,65`) → anomali (`ListAnomalyEvents` 186-197) + exception (`ListExceptionGroups{Services: svcOrder, Limit:500}` 207-218, `svcOrder` üst sınırsız IN listesi) → `redComparisonWindow` + iki `GetServicesAggFilteredIn` (223-230, limitsiz) → `REDStats` (113-119) → `scoreHealth` (244-252). Sözleşmede **cluster/namespace/workload/revision ekseni yok** (79-95; `types.ts:4219-4233`); servis↔workload tek yön `service_metadata.Deployment` (1:1 varsayımı).
- **Sayfa:** `pages/DeploymentReport.tsx` — elle "YYYY-MM-DD HH:mm:ss" → `?since=` + `?owner/?sre` (36-37, 70-84, 118-120), `useEffect`+`api.deploymentReport` (88-97, React Query DEĞİL, polling yok), 4× `useDataTable` `storageKey: deployment-report-*` (224-239), `CopilotExplain kind="problem"` (286), **gövdede i18n yok** (İngilizce sabit), `PageShell`+`Topbar` uyumlu. Backend testi 12 adet (`deployment_report_test.go`).
- **Gezinim:** `App.tsx:62` lazy import + `:169` rota; `Sidebar.tsx:82` (`nav.deploymentReport`, grup `navGroup.triage`); `CommandPalette.tsx:75` (**navKey'siz**; üstündeki 71-74 bloğu 67-70'in ölü kopyası — ⌘K Inbox/Incidents/Problems/Anomalies'i iki kez listeliyor, dedup yok 372-375); `i18n.ts:41` EN / `:177` TR; `chatContext.ts:45` listesinde `/deploys` var, bu sayfa yok. **Custom rol:** `pages.go:28-63` kayıt defterinde yok → Sidebar filtresi (`Sidebar.tsx:390-393`) + `AppShell.tsx:129-133` yönlendirmesi → custom-rol kullanıcısı için sayfa **ulaşılamaz**.
- **Kardeş yüzeyler:** `/deploys` (`Deploys.tsx`; `GetDeploysInWindow` ∪ pod-churn `GetServiceRollouts`; `pipeline` rozeti 151-153; sidebar'dan v0.9.509'da GİZLENDİ — `Sidebar.tsx:93-99` operatör: "deploy kısmı sağlıklı çalışmıyor… daha sonra belki Thanos ya da Argo'dan alırız"; ⌘K'da `CommandPalette.tsx:111`, `paletteReachability.test.ts:86` Ö17 testi `/deploys`'u palette'te ZORLUYOR); `POST /api/operator-events` (`docs/DEPLOY-EVENTS.md`); `GET /api/services/{name}/{deploys,deploy-history,rollouts}` (`api.go:764-770`), `GET /api/deploys/history` (773, `deploys_page.go`).
- **Deploy hunileri:** `mergeDeployEntries` `(service, version)` anahtarında **event kazanır** (`deploys.go:1074-1102`); üç huni `GetRecentDeploys` (1104-1118), `GetDeploysInWindow` (1120-1130), `GetServiceDeploys` (1135-1163 ASC). Tüketiciler: `fusion.go:110`, `behavior_scan.go:132`, `exception_context.go:396`. **🔴 Çatışma:** problem/anomali deploy zenginleştirmesi birleşimi kullanmıyor — `fetchDeploysByService` kendi `FROM spans` sorgusu (`problem_telemetry.go:66-107`); `deploys.go:1040` yorumu ("problem zenginleştirme bedavaya görür") yanlış → event (ve gelecekte rollout) satırları problem çipine ulaşmıyor.

### Önerilen değişiklik
1. **Korunacak:** `redComparisonWindow`/`redStatsFor`/`scoreHealth` → satır başına health verdict (`services []string` parametresi eklenir; servis çözülemezse **boş dilim**, `nil` DEĞİL — aksi hâlde filo döner); `filterOpenProblemsSince` + anomali/exception daraltması → satır çekmecesi (`since = started_at`, `svcOrder` üst sınırı konur). Workload→servis eşlemesi çoktan-çoğa: `service_metadata.Deployment` tersi + MV'de `service_name` boyutu (operatörün şemasında yoktu; §1.3'te gün bir eklendi — verdict buradan).
2. **Atılacak:** elle `since` + "Generate" + fleet-wide çerçeve; `/api/deployment-report` bir sürüm daha canlı (docs/scripts bağımlı), sonra emekli.
3. **Yeniden adlandırma kapsamı (tam):** `App.tsx:62,169` + sorgu dizesini KORUYAN yönlendirme (`TopologyRedirect` emsali `App.tsx:97-100`; statik `Navigate` `?since/owner/sre`'yi kaybeder) · `Sidebar.tsx:82` (grup kararı: triage mi services mi; v0.9.509 gizleme gerekçesi bu tasarımla kapanıyor) · `CommandPalette.tsx:75` (+`navKey: 'nav.rollouts'`; 71-74 kopyası silinir) · `i18n.ts:41,177` (`nav.rollouts`; yeni sayfa gövdesi i18n'li) · `lib/api.ts:2750-2751`, `lib/types.ts:4207-4232` · `internal/api/pages.go` (**zorunlu**, custom rol) · `chatContext.ts:45` · `paletteReachability.test.ts:49` `INTENTIONALLY_UNLISTED['/deployment-report'] = 'emekli rota, yönlendirme'` (rota keşfi regex'le otomatik, tek elle dokunuş bu) · `isPathAllowed.test.ts` (kardeş detay rotası yoksa gerekmez) · `resetLayoutAdoption.test.ts:92` `gap ≤ 72` (agregat sekmesi ayrı dosyada `useDataTable` alırsa `ResetLayoutButton` bağlar) · `storageKey`'ler (`rollouts-*`; kolon genişlikleri sıfırlanır, bilinçli) · `docs/DEPLOYMENT-REPORT-TESTING.md`, `docs/DEPLOY-EVENTS.md`, `scripts/seed-deployment-report-problems.sh`, `docs/superpowers/*deployment-analysis-report*` · `deployment_report_test.go` (12 test taşınır).
4. **Üç yüzey → bir:** `/rollouts` = Live (olay tablosu) + Aggregate (deploy sıklığı, rollback oranı, ortalama süre, en çok rollback alan workload'lar — `workload_rollouts` üstünde yeni `stats` ucu) + satır çekmecesi (eski rapor gövdesi). `/deploys` ve pod-churn `GetServiceRollouts` (`deploys.go:496-650`; MV'siz ham spans, filo ölçeğinde pahalı) Faz 4 sonrası emekliye — Ö17 palette testi + `GetServiceRollouts` tüketicileri (`deploys_page.go:117`) ile birlikte.
5. **Hunilere besleme:** `rolloutDeployEntries(ctx, service, from, to)` (`deployEventEntries` `deploys.go:1050-1070` emsali) → `Source:"rollout"`, `Version: image_tag`, `FirstSeenNs: coalesce(first_span_at, started_at)`; `mergeDeployEntries` iki-kaynaktan öncelikli n-kaynağa (**event > rollout > span çıkarımı** — operatör onayı); dedupe doğal (span çıkarımı da image tag'i önde okur). Ayrı kalem: `fetchDeploysByService`'i birleşime bağlamak (🔴 yukarıda) — P1 dağılımına etkisi ölçülmeden yapılmaz.

### Dokunulacak dosyalar
Yukarıdaki liste + `internal/api/deployment_report.go` → `rollouts.go` içine (verdict/drawer yardımcıları) · `internal/chstore/deploys.go` (`rolloutDeployEntries`, `mergeDeployEntries`) · `internal/chstore/problem_telemetry.go` (opsiyonel).

### Risk / geri alma
- Kayıtsız rota 404 değil **boş sayfa** (SPA catch-all); ⌘K girdisi kalıp rota silinirse "ölü hedef" (`paletteReachability:99`), gerekçesiz yönlendirme "yetim rota" (66-79), gerekçe var rota yok "bayatlama" (81-84) — üçü birlikte.
- `storageKey`/`?since` kaybı paylaşılan linkleri ve kolon düzenlerini sıfırlar → sorgu koruyan yönlendirme.
- Huni önceliği marker sayısını artırır; `mergeDeployEntries` limit kırpması `FirstSeenNs DESC` → eski span satırları düşebilir; geri alma `rolloutDeployEntries → nil` tek satır.

---

## 5. Migration / DDL yönetimi ve ON CLUSTER

### Mevcut durum
- **İki sahiplik yolu, 0011 ikisini birden kullanıyor:** boot `store.go` `tables`/`alters`/`mvs` dilimleri (CREATE `tables`'a, ALTER `alters`'a — `ddl_slice_placement_test.go:105-161` pinler) + `migrations/0011_entity_layer.sql` (başlık 10-18: "tanımlar store.go ile BİREBİR aynı tutuldu"; boot ASLA migration koşmaz 2-3; elle koşum da geçerli 6; sihirbaz `embed.go:23` gömme listesi — 0002/0004-0007 bilinçle dışarıda `embed.go:13-16`; 0009/0010 **yordamsal** sihirbaz, DDL'i çalışma anında üretir `state_repartition_admin.go:59-61`).
- **Sihirbaz mekaniği:** `SplitSQLStatements` (`rollup_admin.go:113-146`; satır bazlı `--` temizliği + `;` bölme — string literal/blok yorum farkındalığı YOK), `AdaptRollupDDL` = `uptrace_all` token'ının **kör `ReplaceAll`'u** (158-160; ZK yolunda/literalde geçerse sessizce değişir → token yalnız `ON CLUSTER`/`Distributed(` içinde), Apply ilk hatada durur (`entity_layer_admin.go:279-288`), Rollback durmaz (301-307), `context.WithoutCancel` + 5 dk (`admin_entity_layer.go:82`), preflight LC kapısı `entityLayerLCGate = 100_000` (`entity_layer_admin.go:110`), `Objects()` kapsama testi (`entity_layer_admin_test.go:62`), sözleşme testi: ≥12 ifade, token kalmamalı, **her ifade `ON CLUSTER`** taşımalı, sıra kolon→sarmalayıcı→index→tablo→mv→distributed (`entity_layer_admin_test.go:20-59`). 0001/0003/0008 testi 0011'i kapsamaz (`rollup_admin_test.go:108`) → 0012 iki listeden birine elle girer.
- **Boot tarafı Distributed:** `adaptDDL` (`cluster.go:1155-1157`), `shardKeyFor` (887-899: env override → `defaultShardPolicy` → `rand()`; `tablesWithoutTraceID` + env `trace_id` → `rand()` 891-892), üç kayıt kuralı; `existingColumns` `x`/`x_local` aynı anahtar (`ddl_skip_existing.go:162-163`); eleyici yalnız `CREATE … IF NOT EXISTS` ve `ADD COLUMN IF NOT EXISTS` tanır — **PROJECTION/MODIFY TTL/MODIFY COLUMN her boot'ta yeniden gönderilir** (`ddl_skip_existing.go:118-120, 203-205`; v0.9.607/608 sınıfı).
- **State tabloları:** üç kayıttan birinde adı geçmeyen tablo "state" sayılır ve `stateTableDDL` birleşik ZK yoluna (`/clickhouse/tables/state/<ad>`, `'{shard}-{replica}'`) gönderir (`state_replication.go:71-93, 127-132`) — "yeni state tablosu için yapılacak HİÇBİR ŞEY yok". Ev kuralı 38/38 `ReplacingMergeTree(version)` + `version UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))`; P1 taraması `partition_dedup_test.go` (istisna gerekçeli sicil 106-112); 0010 `problems`/`anomaly_events`'ten partition söktü.
- **Migration'da Distributed:** düz metin `ENGINE = Distributed(uptrace_all, currentDatabase(), <ad>_local, <anahtar>)` (0001 `rand()`, 0011 `cityHash64(service_name)` `0011:161,183`); boot şablonu `FROM spans` + çıplak motor, migration `FROM spans_local` + `ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/…','{replica}')` — **iki metin ayrı ayrı doğru tutulur**, kopyala-yapıştır yanlış.
- **PROJECTION:** çalışan DDL'de sıfır; yalnız `docs/architecture/scale-billions.md:158` önerisi ve `config_iox_test.go:189` anahtar kelime listesi.
- **TTL/purge:** state 180 g (`anomaly_verdicts`, `entities`), koşu kaydı 30 g; MV TTL DDL'de kademeli; operatör retention ayarı bu tablolara ulaşmaz (`retention.go:89-96`); `store.go`'da yaratılan her tablo `telemetryPurgeTables` / `configPreserveTables` / gerekçeli `purgeCoverageExempt` (`purge_coverage_test.go:23-50`); `service_seen` partition'sız+TTL'siz emsal (`store.go:4001-4008`).

### Önerilen değişiklik
- **(a) Yol:** `workload_rollouts` → `store.go` `tables` (state; üç kayda GİRMEZ) **ve** 0012'de `ReplicatedReplacingMergeTree('/clickhouse/tables/state/workload_rollouts', '{shard}-{replica}', version)` adımı (prod'da `cluster_name` boşsa boot ON CLUSTER basamaz). `workload_revision_activity_1m` → `store.go` `mvs` (kapılı) **ve** 0012 (spans'a dokunan her şey prod'da operatör migration'ı).
- **(b) 0012 şekli (0011 birebir):** ADIM 1 altı kolon (`container_image_tag` v0.10.193 kararı) `spans_local ON CLUSTER` → ADIM 2 aynı kolonlar Distributed sarmalayıcı → ADIM 3 doğrulama (`system.columns`, `clusterAllReplicas`) → ADIM 4 beş skip index → ADIM 5 `workload_rollouts` + `rollout_reconcile_runs` → ADIM 6 MV `_local` + Distributed sarmalayıcı. `uptrace_all` yalnız `ON CLUSTER`/`Distributed(`; başlıkta gerekçe + LC kapısı + "nasıl geri alınır". `embed.go:23`'e ekle; `internal/chstore/rollout_layer_admin.go` + `internal/api/admin_rollout_layer.go` (`entity_layer_admin` aynası: `Objects/Status/Preflight/Apply/Rollback`, host başına durum, audit) + `api.go` tek satır; sözleşme testi `entity_layer_admin_test.go:20-59` şablonu + `Objects()` kapsama testi.
- **(c) Motor/sürüm:** `ReplacingMergeTree(version)`; `updated_at DateTime64(3) DEFAULT now64(3)` düz kolon (tail kursörü); `started_at DateTime64(3)` ORDER BY'da.
- **(d) PARTITION:** `workload_rollouts` partition YOK (`entities`, `service_seen` emsali; retention partition düşürmüyor → kazanç sıfır, P1 riski sıfır); MV `PARTITION BY toDate(bucket)` + `ttl_only_drop_parts = 1`.
- **(e) PROJECTION:** v1'de yok; gerekirse ayrı migration adımı, `_local` üstünde, `deduplicate_merge_projection_mode` canlıda ölçüldükten sonra; **boot dilimine asla**.
- **(f) TTL:** `workload_rollouts` `TTL toDate(started_at) + INTERVAL 180 DAY` (operatör kararı: tarihçe "cevap kaybolur" sınıfıysa TTL'siz `service_seen` emsali); MV 7 g (1m, `spanmetrics_1m`/rollup 10s bandı).
- **(g) Purge sınıfı:** `telemetryPurgeTables` (span/KSM türevi; entity emsali) — ama `started_at` dondurulmuş tarihçe purge sonrası **geri gelmez** → operatör `configPreserveTables` isterse gerekçe satırı.
- **(h) Rollback dosyası:** MV → sarmalayıcı → `_local` → state → INDEX → COLUMN; her Replicated DROP `SYNC` (253 REPLICA_ALREADY_EXISTS); büyük `_local` için `max_table_size_to_drop = 0`; sihirbaz "Geri al" yalnız MV.

### Dokunulacak dosyalar
`migrations/0012_rollout_layer.sql` + `_rollback.sql` (YENİ; gemide bu ad) · `migrations/embed.go:23` · `internal/chstore/store.go` (`tables` + `mvs`) · `internal/chstore/rollout_schema.go` (YENİ) · `internal/chstore/cluster.go:301,431,991` (yalnız MV) · `internal/chstore/purge.go:24` · `internal/chstore/rollout_layer_admin.go` + `_test.go` (YENİ) · `internal/api/admin_rollout_layer.go` (YENİ) + `api.go` 1 satır · `internal/chstore/entity_schema_test.go` şablonlu kayıt testi.

### Risk / geri alma
- **MV kaskadı** (en yüksek) → MV en son, kolonlar doğrulandıktan sonra; sihirbaz geri al yalnız MV.
- **Kayıt unutma** (v0.5.426) → tek shard'ın dilimi, sessiz eksik sayım → üç satır aynı commit + kayıt testi; **ters kayıt** (`workload_rollouts` shard politikasına girerse `stateTableDDL` false → 0009'un kapattığı bölünme geri gelir).
- Boot eleyicisi tanımadığı ifadeyi her boot'ta gönderir → PROJECTION/MODIFY TTL boot'a konmaz.
- 0012 numarası varsayım (mevcut en yüksek 0011).

---

## 6. Thanos istemcisi — KSM için yeniden kullanım

### Mevcut durum
- `Settings{Clusters []ClusterConfig}` tek blob `system_settings["thanos_clusters"]`, 30 s refresh; `ClusterConfig`: `ID`, `Name`, `URL`, `ThanosLabelName/Value`, `SpanClusterValue(s)`, `ThanosLabelSource/DetectedAt`, `AuthType`, `Token`, `NamespaceFilter`, `InsecureSkipVerify`, `Enabled` (`client.go:45-82`). Auth yalnız bearer + blob token (`client.go:490-492`). HTTP: iki paylaşılan singleton, 15 s (`client.go:407-436`); gövde 8 MB (`:500`, aşımda **gürültülü** decode hatası 508-510); `maxSeriesParsed = 1000` **sessiz kırpma** (460, 512-513); `dedup`/`partial_response` gönderilmiyor; `internal/thanos`'ta semafor/rate limit yok — bounded paralellik yalnız entity syncer'da (`ParallelClusters` 4, ≤16).
- **Matcher enjeksiyonu:** `doQueryWith` yalnız cluster external label'ını her vektör seçicisine ekler (`client.go:478-484` → `cluster_matcher.go:97-200`; join/`by`/`group_left` güvenli 148-184). **NamespaceFilter enjekte EDİLMİYOR** — çağıran `nsMatcher` ekler (`syncer.go:284, 364`; `client.go:77-79` yorumu yanlış anlatıyor).
- **Seam'ler:** `InstantSamples(ctx, ClusterConfig, expr) ([]Sample, error)` (`cluster_identity.go:309`; `time=` yok, zaman damgası atılır 317-323); genel **range** seam'i dışa açık değil (`resourceTrendWith`/`rangeTrend` unexported `client.go:1401, 1868`). `ClusterByID` **yalnız etkin** kaydı döndürür (`cluster_identity.go:159-171`; `ThanosSource.Fetch` bulamazsa cluster'ı düşürür `thanos_source.go:36-39`).
- **Ev kararı — `internal/promapi` (v0.9.1150):** "DELIBERATELY NOT a refactor of internal/thanos… new callers use it" (`promapi.go:19-26`); `MaxSeriesParsed`/`MaxBodyBytes` (74, 79), `QuerySeries` instant+range tek tipte, `BuildURL/Do`; ikinci tüketici `internal/vmetrics`.
- **Hep-ya-da-hiç:** `DeploymentMetrics` `ksmOK` (v0.9.42, `client.go:1059-1105`): üç KSM sorgusundan biri hata verirse hiçbiri işlenmez (fake-zero yasağı).
- **KSM join emsali:** `workloadOf` Pod→RS→Deployment (`client.go:973-996`, RS hash soyma 987), `ResolveWorkload` owner zinciri STS/DS/Job→CronJob (`entity/normalize.go:98-115`), `entity/samples.go` `kube_pod_owner` **owner_kind süzgeçsiz** (25) + `kube_replicaset_owner` (26) + `kube_pod_container_info` `image` etiketi (28, 94 — etiket adı varsayımı sevk edilmiş).

### Önerilen değişiklik
1. KSM dedektörü `internal/rollout/` altında, `entity.ThanosSource` deseni (`Clusters()`/`Fetch(ctx, ref, queries)`); sorgular cluster başına ayrı + `nsMatcher` **çağıran ekler** (unutulursa namespace kalkanı yok — birinci sınıf regresyon riski; `thanos.NamespaceMatcher` ihraç edilsin, `syncer.go:364` kopyası kalksın).
2. Range/anlık sorgu için **promapi** üzerinden `rollout/promsource.go` (`promapi.QuerySeries` + `ClusterConfig` URL/Token/Insecure → `Request`); tek gerçek ihraç ihtiyacı `withClusterMatcher` (`cluster_matcher.go:97`). `thanos.Service`'e `RangeSamples`/`InstantSamplesN` eklemek promapi kararıyla çatışır — yapılmaz. `maxSeriesParsed` çağrı-başı parametre (rollout yolu ~50k RS serisi isteyebilir; global sabit değişmez).
3. Hep-ya-da-hiç devralınır: bir cluster'ın KSM setinden biri hata verirse o tik'te o cluster için satır **yazılmaz**, `stalled` üretilmez, koşu satırı `partial` (bu sefer gerçekten yazılır).
4. `stalled` için zaman: RS düzeyi `ready<desired` ya range sorgusu (`[10m]`) ya satırda dayanıklı `ksm_not_ready_since` (lider değişiminde bellek sıfırlanır) — ikincisi önerilir (tek anlık sorgu, durum tabloda).
5. **Sınırlı paralellik + tik bütçesi (rate limit yerine):** cluster'lar `entity.Syncer`'ın `ParallelClusters` semaforuyla aynı vida (varsayılan 4, ≤16; `syncer.go:227-241`), cluster başına 30 s alt-ctx, tik başına sorgu sayısı sabit (6 KSM sorgusu × cluster) → 1 dk tikte cluster başına 6 sorgu/dk; `maxSeriesParsed` çağrı-başı tavan `truncated` bayrağıyla (sessiz kırpma yok). Thanos'ta sunucu tarafı rate limit YOK (`internal/thanos` semaforsuz) — bütçe istemcide tutulur.

### Dokunulacak dosyalar
`internal/rollout/{ksm,promsource}.go` (YENİ) · `internal/thanos/cluster_matcher.go` (`WithClusterMatcher` ihracı) · `internal/thanos/promql.go` (`NamespaceMatcher` ihracı; RS sorgu şablonları) · `internal/entity/syncer.go:364` (kopya kaldırma, opsiyonel).

### Risk / geri alma
Ek yük cluster başına +4-6 anlık sorgu/dk (matcher + ns süzgeci ile); 1000 tavanı aşılırsa sessiz eksik revision → parametreli tavan + `truncated` ilanı; geri alma: KSM sorgu haritasını boşalt → yalnız span kolu, `detected_by='spans'`.

---

## 7. KSM seri mevcudiyeti — bilinen vs varsayılan + doğrulama

### Mevcut durum
Kodun sorduğu 18 KSM metriği arasında **var:** `kube_replicaset_owner` (`promql.go:401-404`, `entity/samples.go:26`), `kube_pod_owner` (`promql.go:395-399`, `samples.go:25`), `kube_pod_container_info` (yalnız entity, `samples.go:28`); **kullanılmıyor:** `kube_replicaset_spec_replicas`, `kube_replicaset_status_ready_replicas`, `kube_replicaset_created` (+ `kube_statefulset_*`, `kube_daemonset_*`, `kube_deployment_created`). Prod'a dair **her şey ölçülmemiş**: probe betiği yazıldı (`scripts/probe/entity-discovery-prod.sh`), çıktısı dokümana işlenmedi (`entity-layer-discovery-2026-08-28.md:11-16`, C8 tablosu boş). Varsayılanlar: RS ailesinin allowlist'te açık olduğu; `kube_pod_container_info.image` etiketi (sevk edilmiş varsayım); external label adı/değeri; RS seri sayısı < 1000.

### Doğrulama (operatör koşar, kod değişikliği yok)
1. `POST /api/settings/thanos/detect?cluster=<ref>` (apply'sız) → `<L>="<V>"` sabitle (`cluster_detect.go:61`).
2. `q()` yardımcısıyla (`entity-discovery-prod.sh:24-31`):
   ```promql
   count by (<L>) (kube_replicaset_owner)
   count by (<L>) (kube_replicaset_spec_replicas)
   count by (<L>) (kube_replicaset_status_ready_replicas)
   count by (<L>) (kube_replicaset_created)
   count by (<L>) (kube_pod_owner)
   count by (<L>) (kube_pod_container_info)
   count by (owner_kind) (kube_pod_owner{<L>="<V>"})
   count by (owner_kind) (kube_replicaset_owner{<L>="<V>"})
   topk(1, kube_replicaset_owner{<L>="<V>"})                 # etiket seti
   topk(1, kube_replicaset_spec_replicas{<L>="<V>"})
   topk(1, kube_pod_container_info{<L>="<V>"})               # image / image_spec / image_id?
   count(count_over_time(kube_replicaset_created{<L>="<V>"}[24h]))   # rollout/gün tavanı
   ```
   + `/api/v1/label/__name__/values?match[]={__name__=~"kube_replicaset_.*"}`.
3. **Kabul:** KSM dedektörü ancak `kube_replicaset_owner` **ve** (`_spec_replicas` veya `_status_ready_replicas`) cluster başına > 0 seri veriyorsa açılır; `count(kube_replicaset_owner{…}) > 1000` ise parametreli tavan zorunlu. Vermiyorsa `detected_by='spans'` tek kaynak, `stalled` **üretilemez**.

### Önerilen değişiklik
Probe betiğine (`scripts/probe/entity-discovery-prod.sh`) yukarıdaki 12 sorgu + label API çağrısı **C12 bloğu** olarak eklenir; çıktısı `docs/audit/entity-layer-discovery-2026-08-28.md` C8 tablosuna işlenir. Kod tarafında `internal/rollout/ksm.go` sorgu haritası bu blokla birebir aynı metrik adlarını kullanır (tek kaynak).

### Dokunulacak dosyalar
`scripts/probe/entity-discovery-prod.sh` (C12) · `docs/audit/entity-layer-discovery-2026-08-28.md` (C8 satırları) · Faz 5'te `internal/rollout/ksm.go`.

### Risk / geri alma
RS ailesi allowlist'te kapalıysa Faz 5 **başlamaz** (kod değil operatör kararı: KSM allowlist genişletmek). Geri alma: probe eklemesi salt-okunur, risk yok.

---

## 8. Cluster adı zinciri ve eşitlik doğrulaması

### Mevcut durum (dört halka)
Registry `Name` + opak `ID` (`EffectiveID() = "c-"+fnv64a(Name)[:8]`, `cluster_identity.go:37-52`; rename'de ID korunur 218-219) → Thanos `EffectiveThanosLabel()` (56-63; `doQueryWith` her seçiciye enjekte) → `SpanClusterKeys()` (`SpanClusterValue(s)`, yoksa `Name`, 76-95; teklik `checkClusterUniqueness` hem değer hem `(etiket,değer)` çifti 256-278) → span `cluster` kolonu 6 yollu coalesce `k8s.cluster.name → openshift.cluster.name → cluster` (`repo.go:307-315`). Collector: chart'ta `resource` processor **yok** (`otel-collector.yaml:16-44`); prod span'i `k8s.cluster.name` **taşıyor** (ekran görüntüsü) — değerinin registry `Name`/`SpanClusterValues` ile eşitliği buradan doğrulanamaz.

### Doğrulama planı (mevcut uçlar)
| Adım | Uç | Beklenen |
|---|---|---|
| 1 | `POST /api/settings/thanos/detect?cluster=<ref>` | `ambiguous=false`, label/value dolu |
| 2 | `GET /api/clusters/sources/probe?cluster=<ref>` | `ok:true`, `series>0` (`thanos_identity.go:60-77`) |
| 3 | `GET /api/settings/thanos/span-clusters` | her değer `ownerId` dolu, `unmapped == 0` (`thanos_identity.go:212-241`) |
| 4 | `POST /api/settings/thanos/assign-span-cluster {value, clusterId, backfill}` | eşleşmeyeni bağla (çakışma → sahip döner) |
| 5 | Nöbet: `LabelCheckTickPersist` 10 dk (`cluster_detect.go:208-221`, `main.go:1032,1039`) → `thanos_label_checks` blob'u → Settings rozeti; **etiketi algılanmamış kayıt atlanır** (274) |
| 6 | `count by (<L>) (kube_node_info)` ↔ Remote Cluster listesi farkı = token'ın görmediği cluster'lar |

**Rollout kabul kapısı:** her etkin kayıt için (1) `ambiguous=false`, (2) `probe.ok`, (3) `unmapped=0`. Üçü sağlanmadan span-kolu ve KSM-kolu satırları **birleşmez** (aynı rollout iki satır). Kolona `EffectiveID()` yazılırsa registry rename'i tarihçeyi koparmaz; `Name` yazılırsa (operatör kararı) rename = kopuk tarihçe, geri alınamaz.

### Önerilen değişiklik
**Collector ayağı ne yapar (uyuşmazlık dalı):** collector'ın bastığı `k8s.cluster.name` değeri registry `Name`'e eşit DEĞİLSE collector **değiştirilmez** (restart + wedge riski, filo çapında); değer `POST /api/settings/thanos/assign-span-cluster` ile kayda **atanır** (`SpanClusterValues`, v0.10.139 çoklu değer) — bu zaten ürünün tasarım yolu. Collector yalnız değeri hiç basmıyorsa (yalnız `openshift.cluster.name`) düzenlenir; o da `clusterDeriveExpr` ile bugün çalıştığı için acil değil. Yeni cluster kaydı açılırken sıra: registry kaydı → Detect label → probe → span değerlerini ata → rollout bayrağı.

### Dokunulacak dosyalar
Kod değişikliği yok (dört mevcut uç). Doküman: `docs/operator/rollouts-collector.md` §"cluster adı" (aşağıdaki §10 teslimatının parçası).

### Risk / geri alma
Yanlış atama teklik kuralına takılır (ikinci kayıt reddedilir, `checkClusterUniqueness`); düzeltme Settings'ten değeri kaldırmak, veri kaybı yok. `Name` yazılmış tarihsel satırlar rename'de kopar → §13.3 kararı `EffectiveID()`.

---

## 9. `registerXxxRoutes` referansları ve rota tablosu

### Mevcut durum
29 registrar; dördü doğrudan emsal: `deployment_report.go:21-23` (okuma, `serveCached` 30 s, kapısız), `anomaly_verdicts.go:28-30, 74-81` (`PUT` editor+, doğrula → store → invalidation → `s.audit` → `writeJSON`), `entities.go:37-48` (8 okuma ucu; `entity_id` `/` taşıdığı için `?id=` — Go mux çok-segmentli `{id}`'yi yalnız sonda kabul eder 40-41; bayrak kapısı `entityEnabled` 52-58; 24 h kelepçe **anahtardan önce** + `clamped:true` 669-680; limit kelepçesi 135-138), `exception_pods.go:27-34`. `api.go` kayıt blokları: okuma/drill-down 882-889, settings 1209-1213, Deployment Report 929. `serveCached` closure kapalı `ctx` (v0.8.319, `cache.go:209-216`); anahtar saf fonksiyon + `*_key_test.go` (`cache_key_test.go:5-20`, `entity_key_test.go:14-40`). `/api-route` SKILL.md kuralları kodla uyumlu, yalnız satır numaraları kaymış (`writeJSONError` 11734, `writeErr` 11747, `editorRoles` 564, spaHandler 1398, middleware 1418, `auth.go:304-307`). `collapseRoute`/`isVolatileSegment` (`api.go:11926-11971`): yol segmenti ancak tümü rakam ya da ≥8 hex ise `:id`'ye çökertilir — RS adı gibi bir id **span adı kardinalitesini patlatır**. `make audit` CHECK 1/5/5b/6/7.

### Önerilen rota tablosu (`internal/api/rollouts.go`, `api.go:889` altına tek satır)
| Rota | Parametreler | Kapı | Cache |
|---|---|---|---|
| `GET /api/rollouts` | `from,to` (snapshot), `cluster,namespace,workload,status,kind`, `limit ≤200` | yok (viewer görür) | `serveCached` 10 s, anahtar `fnvStr(cluster,ns,workload,status,kind)` + `cacheBucket(from,to)` + limit |
| `GET /api/rollout?id=` | `id` = opak digest (`cityHash64` ondalık ya da 16-hex — `collapseRoute` uyumlu) | yok | 30 s |
| `GET /api/rollouts/stats` | `from,to,cluster,namespace,env,topN ≤50` | yok | 60 s |
| `PUT /api/rollouts/{id}/verdict` (opsiyonel) | `{verdict, note ≤500}` | `auth.RequireAnyRole(editorRoles, …)` + `s.audit(r, "rollout.verdict", "rollout", id, …)` + `cacheInvalidatePrefix("rollouts:")` | — |

### Dokunulacak dosyalar
`internal/api/rollouts.go` (YENİ: registrar + 3 okuma ucu + opsiyonel verdict; tail goroutine'i) · `internal/api/rollout_keys.go` + `rollout_keys_test.go` · `internal/api/rollouts_test.go` · `internal/api/api.go:889` altına tek satır · `internal/api/pages.go` · `internal/chstore/rollouts.go` (+`_test.go`) · FE dört dokunuş (`lib/types.ts`, `lib/api.ts`, `lib/queries/rollouts.ts`, `lib/queries/index.ts` + `cancellation.test.ts` kaydı).

### Risk / geri alma
- 🔴 **Kayıt satırı unutulursa 404 değil HTTP 200 + boş sayfa** (SPA catch-all `api.go:1398`); otomatik kapısı YOK → `rollouts_test.go` registrar pini + `grep registerRolloutRoutes internal/api/api.go`.
- 🔴 **Degrade kip (dış Distributed prod):** kolonlar `repairPromotedAttrCols` tarafından atlanır → MV yok → `workload_rollouts` boş → `/api/rollouts` boş liste + `note: "MV kurulu değil (0012)"` döndürmeli; sayfa boş değil ilanlı (`Empty` gövdesi Admin → ClickHouse → 0012'ye link). Tail ucu da tablo yoksa 200 + `disabled:true` (`entities.go:52-58` emsali).
- 🟠 `{id}` yol segmenti: bileşik kimlik `/` taşırsa mux kabul etmez → `?id=`; opak digest (16-hex) seçilirse `collapseRoute` uyumlu.
- Geri alma: `rollouts.go` + anahtar dosyaları sil, `api.go` tek satır; CH tabloları kalır.

`rollout_keys.go` + `rollout_keys_test.go` (distinctness/stability/ayraç saldırısı); `rollouts_test.go` `registerRolloutRoutes(mux)`'un `api.go`'da olduğunu ve rota dizesinin api.go'da OLMADIĞINI pinler (`anomaly_verdicts_test.go:26-44`). Kapılar: `TestMuxRoutePatterns`, `make audit` CHECK 1/5/5b/6/7, `grep registerRolloutRoutes internal/api/api.go` (S1 — otomatik kapısı yok). `workload_revision_activity_1m` okuyan uç 24 h kelepçe + `clamped`; cluster süzgeci `SpanClusterKeys()` → `cluster IN (?)` (`entity_queries.go:373`). Yeni sayfa `pages.go:28-63` kayıt defterine girer.

---

## 10. Collector gereksinimleri — chart diff + operatör dokümanı

### Mevcut durum
Chart tek collector (`otel-collector.yaml`): zincir `[memory_limiter, (k8sattributes), batch]` üç pipeline'da (106-108, **sıra doğru**); `k8sattributes` bayrağa bağlı, varsayılan `false` (`values.yaml:484-485`), `auth_type: serviceAccount`, `passthrough: false`, extract **6 anahtar** (`k8s.namespace.name, k8s.deployment.name, k8s.pod.name, k8s.pod.uid, k8s.node.name, k8s.container.name` 35-40), `pod_association` yalnız `from: connection` (41-43), `filter`/`wait_for_metadata` yok, `replicas: 1` (118), `limit_mib: 512` (19) vs container limit 2Gi (`values.yaml:493`). `resource`/`resourcedetection` processor **yok**. RBAC: pods/namespaces + `apps/replicasets` get/list/watch (`otelcol-rbac.yaml:26-31`; nodes ve `batch/jobs` yok). Collector chart'ta varsayılan **açık** (`values.yaml:469`); Coremetry self-obs ve go-demo ona gidiyor (`deployment.yaml:133`, `go-demo.yaml:40`); düz OpenShift manifestleri collector'ı dışarıda varsayıyor (`examples/openshift/07-coremetry-service.yaml:3`). **Prod collector konfigi bu makineden görülemez**; ekran görüntüsü çıktının gereksinimin çoğunu karşıladığını gösteriyor (prod collector'ı operatör tarafından zaten genişletilmiş görünüyor).

### Diff (istenen ↔ chart)
| Alan | Chart | Prod (görüntü) |
|---|---|---|
| namespace / pod / uid / deployment / node / container | ✅ | ✅ |
| **replicaset** | ❌ | ✅ |
| **statefulset / daemonset / cronjob / job** | ❌ | ? (o türden pod gerekir) |
| **container.image.name / .tag** | ❌ (pending-actions §3 istemişti) | ✅ |
| resource → `k8s.cluster.name` | ❌ processor yok | ✅ (değer ↔ registry doğrulanmalı, §8) |
| resource → `deployment.environment.name` | ❌ | ❌ görünmedi |
| `pod_association` `k8s.pod.ip` → connection | ❌ (yalnız connection) | ? |
| k8sattributes → batch sırası | ✅ | ? |
| RBAC replicasets | ✅ | — |

### Önerilen değişiklik
Chart (varsayılan yine `false`): extract 6 → 13 (**yeni yedi anahtar:** `k8s.replicaset.name`, `k8s.statefulset.name`, `k8s.daemonset.name`, `k8s.cronjob.name`, `k8s.job.name`, `container.image.name`, `container.image.tag`); `pod_association` `[{from: resource_attribute, name: k8s.pod.ip}]` önce, `[{from: connection}]` sonra; bayrağa bağlı `resource/identity` (`k8s.cluster.name` = `values.otelCollector.clusterName` — Remote Cluster `Name`/`SpanClusterValue` ile aynı; `deployment.environment.name` = `values.otelCollector.environment`) k8sattributes'tan sonra, batch'ten önce; `cronjob` için `batch/jobs` RBAC (upstream owner zinciri — **açık soru**); `filter: node_from_env_var` ve `limit_mib`/limits uyumu ayrı maddeler. Operatör dokümanı `docs/operator/rollouts-collector.md`: aynı blok + gateway notu (collector uzaktaysa uygulama `OTEL_RESOURCE_ATTRIBUTES=k8s.pod.ip=$(POD_IP)` downward API; Coremetry'nin kendi pod'u `deployment.yaml:138-141` bilinçli eksik) + kabul testi = **K8s Coverage kartı** (yeni `rs`/`img` sayaçları, `clus` ayrımı) — dev cluster'da go-demo + self-obs üstünde uçtan uca doğrulanabilir, ekran görüntüsü şart değil. Bayat dokümanlar (`k8s-entity-layer-2026-08-26.md:19-24,45-56`, `entity-layer-discovery-2026-08-28.md:99-110,313`) "v0.10.92 öncesi / 2026-08-30'da EN AZ bir prod cluster'ında görüldü" notu alır.

**Teslimat — `docs/operator/rollouts-collector.md` çekirdeği (repo dışı collector'lara uygulanacak blok; chart'ta aynı blok bayrağa bağlı):**
```yaml
processors:
  memory_limiter: { check_interval: 1s, limit_mib: 512 }
  k8sattributes:
    auth_type: serviceAccount
    passthrough: false
    extract:
      metadata:
        - k8s.namespace.name
        - k8s.pod.name
        - k8s.pod.uid
        - k8s.deployment.name
        - k8s.replicaset.name      # rollout revision kimliği
        - k8s.statefulset.name
        - k8s.daemonset.name
        - k8s.cronjob.name         # batch/jobs RBAC gerektirebilir (açık soru)
        - k8s.job.name
        - k8s.node.name
        - k8s.container.name
        - container.image.name     # image diff
        - container.image.tag
    pod_association:
      - sources: [{ from: resource_attribute, name: k8s.pod.ip }]
      - sources: [{ from: connection }]
  resource/identity:
    attributes:
      - { key: k8s.cluster.name,            value: "<Remote Cluster kaydındaki Name ya da SpanClusterValue>", action: upsert }
      - { key: deployment.environment.name, value: "<env>", action: upsert }
  batch: {}
service:
  pipelines:
    traces:  { processors: [memory_limiter, k8sattributes, resource/identity, batch] }
    metrics: { processors: [memory_limiter, k8sattributes, resource/identity, batch] }
    logs:    { processors: [memory_limiter, k8sattributes, resource/identity, batch] }
```
RBAC (ClusterRole): `pods`, `namespaces` (get/list/watch) + `apps/replicasets` (mevcut, `otelcol-rbac.yaml:26-31`); `cronjob` için `batch/jobs` — upstream davranışı repodan doğrulanamıyor, operatör canlıda dener. Gateway topolojisi: uygulama pod'u `OTEL_RESOURCE_ATTRIBUTES=k8s.pod.ip=$(POD_IP)` (downward API). **Kabul testi:** her cluster için K8s Coverage kartı `ns/pod/deployment/rs/img ≥ %95` — 2026-08-30 ekranları bir cluster'ın `k8s.namespace.name` bile basmadığını gösterdi (v0.10.190); bu blok o cluster'a öncelikle uygulanmalı.

### Risk / geri alma
Kardinalite (`k8s.replicaset.name` her deploy'da yeni; LC kapısı §1) · collector restart → `maxUnavailable: 0` / wedge (`CLAUDE.md`, `docs/INCIDENTS.md:82-85`) → ayrı bakım penceresi · informer belleği (512 MiB soft limit) · prod dışarıda: blok uygulanmazsa span kolu sessizce boş → UI'da ilan · geri alma `k8sattributes.enabled=false` (RBAC aynı bayrağa bağlı), CH şeması etkilenmez.

---

## 11. Span-taraflı "stalled" zayıf sinyali — yanlış pozitif değerlendirmesi

### Mevcut durum
Kod tarihçesi: v0.8.405 "deploy yapılmadan deploy algıladı" (kova varlığı ≠ canlılık, `deploys.go:582-589`), v0.9.451 düşük hacimli pod 10+ dk sustu → RS-hash guard (`626-641`), v0.9.588 süpürme kendi kesintisini "kaynak sustu" okudu (`tick_continuity.go`). `service_silent` bu yüzden 3×5 dk **ve** baseline aktif-kova payı ≥ %90 (cron/batch dışarıda, `anomaly.go:823-836`). Senaryolar (demo sabitlerinden, prod'da benzerleri): cron/batch (`eod-batch` ~0,5 iz/dk gece → 1 dk kovada %60 boş), consumer/fanout, sağlıklı canary (`traffic_confirmed_at`'in varlık sebebi), telemetri flıker'ı, gece diurnal çukuru (0,28). **Hüküm:** sinyal `status` yazmaz; yalnız KSM `ready<desired` ile AND ya da nötr "henüz trafik yok"; "önceki revizyon istikrarlı trafikli" şartı; reconciler süreklilik kapısı.

### Önerilen değişiklik
**Eşik önerileri (kod gerekçeli):** MV kova 1 dk (`spanmetrics_1m`, `entity_seen_1m`) · karar kovası 5 dk (`bucketSeconds = 300`, `service_version_5m`) · aktiflik ≥10 span/5 dk (`MinCount: 10`, `anomaly_promotion.go:83`; düşük hacim: ≥1 span + 3 ardışık kova) · histerezis 5 dk kovada ≥2 (= operatörün kuralı; 1 dk kova seçilirse ≥5) · overlap 30 dk (`evidenceDeployLookback` sınıfı) · `stalled` KSM `ready<desired` **10 dk** (evaluator ailesinde sustain `ForSec=180`, pencere 10 dk — `evaluator.go:311-318`; 10 dk tavan, 3 dk taban operatör vidası) · span zayıf sinyali 15 dk + %90 şartı · tail ≥3-5 s, SSE/poll ≥10 s.

### Dokunulacak dosyalar
`internal/rollout/state.go` (saf durum makinesi + eşik sabitleri, ayardan okunur) · `internal/rollout/settings.go` (`system_settings["rollouts"]`: threshold, hysteresis, overlapMin, stalledMin, weakSignal on/off) · `internal/rollout/state_test.go`.

### Risk / geri alma
Yanlış `stalled` alarm üretir (en yüksek risk) → zayıf sinyalin `status` yazma yetkisi yok, tek bool vida ile tamamen kapatılabilir; eşikler ayardan → yeniden başlatma gerekmez; geri alma DDL istemez.

---

## 12. Faz planı ve test edilebilirlik

| Faz | Kapsam | Sürümler (tahmin) | Unit | Entegrasyon / kanıt |
|---|---|---|---|---|
| **Ön koşul** | Demo üreteci: RS hash'li `k8s.replicaset.name`, değişken `container.image.*`, ≥2 namespace, STS senaryosu; `k8s_coverage` sayaçları (`rs`, `img`, `clus` ayrımı); bayat doküman notları; operatör §7 PromQL + §8 üçlü kapı | 2 | golden test `internal/otlp` (yeni anahtarlar diziye giriyor) | K8s Coverage kartı lokalde `rs ≥ %95` |
| **Faz 1a DDL** | 5 terfi kolonu, `rollout_schema.go`, kapı, üç kayıt, `workload_rollouts` + `rollout_reconcile_runs` (`tables`), 0012 ADIM 1-5 + rollback + embed + `rollout_layer_admin` sihirbazı + admin uçları | 2-3 | `promoted_attr_test` (ifade/DDL), kayıt testi (`entity_schema_test` şablonu), `ddl_slice_placement`, `partition_dedup`, `purge_coverage`, 0012 sözleşme testi (≥N ifade, token yok, her ifade ON CLUSTER, sıra), `Objects()` kapsama | Lokal 2-shard: sihirbazdan 0012 ADIM 1-5 → host başına durum → kolonlar dolu (`probePromotedAttrs`) |
| **Faz 1b MV** | `workload_revision_activity_1m` (0012 ADIM 6 + boot `mvs` kapılı) — **kapsama kartı cluster başına ≥ %95 ve kolonlar her shard'da doğrulandıktan SONRA**, ayrı sürüm | 1 | MV DDL şablon testi, kayıt testi | Lokal: demo span'i → `SELECT … FROM workload_revision_activity_1m FINAL` satır; MV'nin MATERIALIZED kolonu okuduğu kanıtı (0011 ADIM 3 emsali); prod: operatör sihirbazdan |
| **Faz 2 span reconciler** | `internal/rollout` (aktivite okuma, saf `Merge`, durum makinesi, deterministik `started_at`, süreklilik kapısı), `chstore/rollouts.go` upsert, `main.go` 1 satır, ayar blobu | 2-3 | Tablo-testli `Merge`/state (giriş-aktif küme, rollback = eski RS geri, overlap>30 dk, histerezis, çift yazım idempotans, kesinti sonrası doğru damga) | Demo rollout senaryosu (tag bump + RS değişimi) → `workload_rollouts` satırı `in_progress → completed`; rollback senaryosu → `rolled_back`; koşu satırı |
| **Faz 3 API + tail + SSE** | `rollouts.go` (liste/stats/tekil), anahtar+testler, `PublishLocal` + tail (logstream_broker deseni), kind sabitleri, huni beslemesi (`rolloutDeployEntries`, öncelik onaylıysa) | 2 | key invariants, route pin, `tailStep`-tipi kursör/gap testleri, `TestMuxRoutePatterns`, `mergeDeployEntries` 3-kaynak testi | httptest SSE: upsert → `event: rollout` zarfı; iki pod simülasyonu (iki Broker) → tek teslim; `/deploys` marker'ında `Source:"rollout"` |
| **Faz 4 Rollouts FE** | Rename + sorgu koruyan redirect + i18n + palette dedupe + `pages.go` + testler; Live tablo (Plan A: invalidation + debounce + poll), Aggregate sekmesi (`stats`), satır çekmecesi (eski rapor gövdesi, `services` param), pivotlar (`resource.k8s.replicaset.name` filtresi → terfi kolonu, `cluster`+ns daraltmalı; `/entity?id=wl:`), health verdict | 3 | `rolloutRow.test.ts` (kimlik, monotonluk), `rolloutHref.test.ts`, `eventInvalidations` yeni `it`, `paletteReachability`, `isPathAllowed`, `resetLayoutAdoption`, tam vitest | Headless ekran görüntüsü (`shot-app.mjs`): liste/çekmece/agregat; SSE ile satır güncellemesi (DevTools `event: rollout` çerçevesi + tabloda değişim); eski `/deployment-report?since=` linkinin çalışması |
| **Faz 5 KSM + stalled** | `rollout/ksm.go` (RS sorguları, ns matcher), promapi tabanlı kaynak (+`WithClusterMatcher` ihracı), merge'e ikinci kaynak (`detected_by`, `ksm_started_at`, `pods_ready_at`, `ksm_not_ready_since`), `stalled`, hep-ya-da-hiç + `partial` koşu | 2-3 | Saf merge fixture'ları (span-only / ksm-only / ikisi; cluster eşleşmemesi → iki satır DEĞİL, unmapped ilanı), sorgu şablon testleri | Sahte Thanos (`scratchpad fake_thanos.py` emsali) ile `ready<desired` 10 dk → `stalled`; §7 kabul kapısı prod'da geçmeden bayrak kapalı |

**Toplam:** ~14-17 sürüm; her faz kendi sürümlerinde, her biri `/release` gate'leri + adversarial inceleme.

---

## 13. Onaya sunulan kararlar

1. `workload_rollouts` **shard edilmesin** (state deseni) — ısrar edilirse O5 sağlanıyor ama üç kayıt + tek-argümanlı emsal dışına çıkılır.
2. `ReplacingMergeTree(version)` + `updated_at` düz kursör; PARTITION yok; PROJECTION v1'de yok.
3. `cluster` kolonu = `EffectiveID()` (rename-güvenli) — tasarımdaki "registry değeri (Name)" yerine.
4. `started_at` = aktif kümeye giren ilk 5 dk kovanın başlangıcı (deterministik).
5. Karar kovası 5 dk (MV 1 dk); eşikler §11.
6. SSE: Seçenek T (pod tail'i, yerel fan-out, `PublishLocal`) + FE **Plan A** (invalidation + debounce + poll); Plan B yalnız ısrar hâlinde.
7. Span-taraflı zayıf sinyal `status` yazmaz.
8. MV'ye `service_name` boyutu **gün bir eklendi** (§1.3; MV tek atış olduğu için audit'te kapatıldı) — itiraz varsa ŞİMDİ.
9. `image` (ad, `container_image` terfi kolonu) + `image_tag` (`effectiveVersionExpr`) **iki kolon** (§1.3, kapatıldı); yalnız `container_image` LC/String kararı ölçümle (C3 kapısı).
10. Huni önceliği `event > rollout > span çıkarımı`; `fetchDeploysByService` birleşime bağlanması ayrı kalem.
11. Rota adı `/rollouts` + `/api/rollouts`; pod-churn `GetServiceRollouts` ve `/deploys` Faz 4 sonrası emekli (Ö17 palette testi ile birlikte).
12. `/deployment-report` redirect'i sorgu korur; `/api/deployment-report` bir sürüm daha yaşar.
13. Purge sınıfı: `telemetryPurgeTables` (varsayılan) mı `configPreserveTables` mi.
14. Nullable `image/prev_image` (ev kuralı sentinel) — kabul mü.
15. `PUT …/verdict` gerçekten isteniyor mu (anomaly emsalinde çağıransız uç kaldırılmıştı).

Kapsam dışı (bu audit'e girmedi): cross-cluster servis haritası; STS/DS için `controller-revision-hash` (span'de anahtar yok, KSM'de `kube_statefulset_status_current_revision` sorgulanmıyor) — Faz 5'te açık soru.

## 14. Sürüm izi (2026-08-30, onay sonrası)

| Faz | Sürüm | Not |
|---|---|---|
| 1a boot şeması | **v0.10.193** | 6 terfi kolonu (`typ` alanı, varsayılan LC), `workload_rollouts` + `rollout_reconcile_runs` (`host` ayırıcı), üç küme kaydı, purge sınıflandırması |
| 1a-2 0012 + sihirbaz | **v0.10.197** | `migrations/0012_rollout_layer.sql` (+rollback), `RolloutLayer*` (cluster başına kapsama ön kontrolü), `/api/admin/rollout-layer/*`, AdminClickhouse paneli |
| 1b MV | v0.10.198 | boot `mvs` kapısı `hasRolloutCols && hasK8sPodCol`; prod'da 0012 ADIM 6 sihirbazdan |
| 2 reconciler | v0.10.199 | `internal/rollout` gözlenmiş-giriş modeli (§14.1) + lider-kilitli tik + ayar blobu `system_settings["rollouts"]` |
| 3 API + SSE | v0.10.200 | `/api/rollouts*`, ayar uçları, pod-yerel CH tail (`PublishLocal`) |
| 4 Rollouts FE | v0.10.201 | `/rollouts` (Deployment Report emekli, sorgu koruyan redirect) |
| 4b çekmece | v0.10.203 | `/api/rollout/detail` (servisler MV'den, health verdict + önce/sonra RED ≤ 6 sa, deploy'dan beri sinyaller), `?rollout=` çekmecesi |
| 4b çekmece (kayıt) | v0.10.204 | 203'ün iki FE kayıt dosyası tag dışında kalmıştı — sağlama düzeltmesi |
| operatör bulguları | v0.10.205 / 206 | kolon `minWidth` tabanları (fit 48px'e eziyordu); `notePendingExit` — "sürüyor" satırı çıkış histerezisi beklediğini SÖYLER |
| STS/DS kapsamı | v0.10.211 | MV revizyonu `if(k8s_replicaset != '', k8s_replicaset, container_image_tag)`; WHERE `workload != '' AND revision != ''`; FE Traces pivotu türe göre süzer (`rolloutTracesFilters`) |
| 5 KSM dilim 1 | **v0.10.212** | `internal/rollout/ksm.go` — `KSMQueries` (§7 adlarıyla birebir) + saf `JoinKSM`/`applyKSM`; `pods_ready_at`, `ksm_not_ready_since` (dayanıklı sayaç), `stalled` YALNIZ KSM'den; kabul kapısı: owner VE (spec\|ready) yoksa nil → spans tek kaynak |
| giriş kanıt kapısı | **v0.10.213** | operatör prod bulgusu: bootstrap'ta seyrek/batch iş yükü sahte "tamamlandı" aldı → giriş artık kanıt ister |
| kapı düzeltmesi | **v0.10.215** | 213'ün çok-mercekli incelemesi (9 doğrulanmış bulgu) üç gerileme buldu: KSM vetosu span kanıtını EZİYORDU (rollout undo / blue-green yutuluyordu), tablo ayağı ZAMANSIZDI (sahte satır bir tik sonra geriye dönük yazılıyordu), 6 sa penceresi RS yaşını ölçtüğü için gecikmeli batch deploy'ları kesiyordu → kanıt önceliği (span > KSM), zaman sınırlı tablo ayağı, pencere 24 sa |
| 5 stalled kalanı | — | STS/DS readiness ikizleri + erken-tamamlama (KSM teyidiyle 30 dk → ~10 dk) açık |

Araya giren operatör bildirimleri (v0.10.194 CoSRE genel cevap, 195/196 K8s
kapsama örneklemi + başlıklar) fazları 194-198'den 197-201'e kaydırdı.
v0.10.202: scanRollout UInt64 düzeltmesi (canlı smoke buldu — tablo boşken
tüm gate'ler yeşildi). PROD v0.10.202'ye operatörce çekildi (2026-08-30),
sonra v0.10.212'ye (2026-08-31); 0012 sihirbazı + Enable prod'da yapıldı.

**§7 kabul kapısı hakkında (v0.10.212 sonrası dürüst durum):** PromQL
doğrulama listesi operatörce koşulmadı; onun yerine kapı KODA gömüldü —
`JoinKSM` owner VE (spec|ready) serisi yoksa `nil` döner (spans tek kaynak,
`stalled` üretilmez) ve owner > 5000 seri hata verir. Yani Faz 5 "ölçülmeden
başlamaz" kuralı, ölçümü ÇALIŞMA ZAMANINA taşıyarak korunuyor; §7'nin probe
bloğu hâlâ yazılmadı ve prod'da hangi RS metriklerinin açık olduğu
BELGELENMİŞ DEĞİL (koşu satırındaki `ksmMs` yalnız sorgunun koştuğunu söyler,
serilerin döndüğünü değil).

### 14.2 Modele sonradan giren kurallar (v0.10.206-213)

Aşağıdakiler §14.1'in modelini DEĞİŞTİRİR; oradaki anlatı tek başına
okunursa bugünkü davranışı yanlış tarif eder.

1. **`notePendingExit` (v0.10.206).** Açık satır, çıkış histerezisi dolarken
   beklediğini not olarak söyler. Geçici not — karar düşünce silinir; ilk
   revizyonda (önceki yok) yazılmaz. Operatör algısı düzeltmesi: "rollout
   bitti ama sürüyor yazıyor".
2. **Revizyon = RS adı DEĞİL, `RS ?? imaj tag'i` (v0.10.211).** StatefulSet/
   DaemonSet pod'unda ReplicaSet yoktur; MV revizyonu imaj tag'ine düşer
   ("deploy = yeni imaj", operatör kararı). Sınırlar: sabit tag'li (latest)
   STS rollout'u GÖRÜNMEZ; aynı tag'e rollback tek revizyon sayılır; kapsama
   kapısı hâlâ replicaset-bazlı (STS-ağırlıklı küme kapıyı açamayabilir).
3. **KSM ikinci kanıt (v0.10.212).** `stalled` yalnız KSM'den
   (`ready < spec` ≥ `StalledMin`); span sessizliği ASLA status yazmaz.
   `isOpen` artık `stalled`'ı da açık sayar. `noteStalled` karar
   SONRASI eklenir — completed'a giden satırda stalled notu kalmaz.
   Cluster başına hep-ya-da-hiç: sorgu hatası o cluster'ın KSM'ini düşürür
   ve koşuyu `partial` yapar; ailenin YOKLUĞU hata değildir.
4. **Giriş kanıt ister (v0.10.213, yapısı v0.10.215'te düzeltildi) —
   §14.1'in "gözlenmiş giriş" tanımına EK koşul.** Gözlenmiş yokluk + eşik
   geometrisi TEK BAŞINA yetmez, çünkü bir iş yükünün MV tarihindeki İLK
   gözlemi de aynı şekle sahiptir (prod vakası: haftalar önce yaratılmış
   RS'ler "tamamlandı" satırı aldı). Kanıtlar **sıralı değil ÖNCELİKLİ**:
   - **(1) Span tarafında gözlenmiş geçiş** — önceki revizyon girişte aktif
     (`prevAt`) ya da kardeş revizyon izi: pencere kovası, FirstSeen ufku,
     **entryStart'tan ÖNCE başlamış** tablo satırı. En güçlü kanıt; KSM
     damgasıyla EZİLEMEZ.
   - **(2) Span kanıtı yoksa** tek kanıt RS'in taze yaratılmış olmasıdır
     (`ksmFreshGrace`, 24 sa); o da yoksa satır üretilmez.

   **v0.10.213'ün üç hatası (inceleme, hepsi çalıştırılarak kanıtlandı):**
   (a) KSM tazelik kontrolü VETO olarak yazılmıştı ve (1)'i eziyordu —
   Kubernetes `rollout undo` MEVCUT RS'i yeniden ölçekler (`created`
   haftalar önce) ve blue/green'de RS trafikten saatler önce doğar, yani en
   kanıtlı geçişler yutuluyordu; üstelik eski satır "devraldı" notuyla
   kapanıp yeni revizyona satır yazılmıyordu (**yarım kayıt**: tabloda
   olmayan revizyona atıf). (b) Tablo ayağı zamansızdı: reddedilen giriş bir
   tik sonra, aynı pencerede yazılan gerçek deploy satırı sayesinde geriye
   dönük meşrulaşıyordu. (c) 6 sa penceresi trafiği değil RS YAŞINI ölçtüğü
   için sabah deploy edilip gece koşan batch'lerin gerçek deploy'ları kalıcı
   olarak kayboluyordu.

   **Kalan sınır (bilinçli):** cadence'i hem 6 g FirstSeen ufkundan hem 24 sa
   penceresinden uzun olan iş yükleri (aylık mutabakat) kanıtsız kalır ve
   satır almaz — span'lerden "ilk gözlem" ile "deploy" ayırt EDİLEMEZ.

   Genel kural: **bir olay iddiası, olayın kendisinden bağımsız bir kanıt
   ister** — ve kanıtlar arasında öncelik varsa zayıf olan güçlüyü VETO
   EDEMEZ. Yeni açılan bir veri kaynağı her zaman sahte-olay dalgası
   riskidir; onu kesmek için konan kapı da kendi yanlış-negatif sınıfını
   doğurur, ikisi ayrı ayrı ölçülmeli.

### 14.1 Faz 2 durum makinesi — inceleme sonrası model (v0.10.199)

Üç tur çok-mercekli inceleme (4 mercek + reddediciler, 2026-08-30) ilk üç
taslakta çalıştırılarak kanıtlanmış blocker'lar buldu: kayan pencere
kenarında sahte `rolled_back`; tek revizyona uydurma `completed`; koşu
başlangıcına dayanan "daha yeni mi" kararı (canary dalması); pencere kenarı
bir çukurun içine denk gelince köprülenen dalmanın giriş sayılması; satırı
olmayan yerleşik revizyonun ≥30 dk sessizliğinin (gece, ingest kesintisi)
sahte `completed` üretmesi; CH DateTime64 0'ın Go'ya 1970 dönmesi. Dördüncü
yazımın modeli (reconcile.go başlığı) §2.4'ün yerine geçer:

- **Satır = gözlenmiş GİRİŞ olayı.** Giriş için revizyonun öncesinde
  ≥ ExitHysteresis kova boyunca aktif OLMADIĞI pencere içinde gözlenmeli;
  pencere kenarına dayanan yokluk kanıt değildir. Pencere kenarında zaten
  aktif koşu satır açmaz. started_at mutlak kova — iki yazıcıda aynı.
- **Bilinen revizyonun dönüşü olay değildir:** 6 günlük (MV TTL − 1) ilk-görülme ufku
  (`RolloutFirstSeen`, MV'den ucuz GROUP BY) ya da tablo satırı revizyonu
  girişten ≥ EH kova önce gösteriyorsa ve yokluğunda başka revizyon aktif
  olmadıysa → satır varsa taşınır, yoksa hiçbir şey yazılmaz. Yokluğunda
  başkası aktif olduysa yeni giriş olayı (prev = o revizyon, "geri dönüş"
  notu). Hiç görülmemiş revizyon giriştir.
- **Giriş histerezisi 2 kova, ÇIKIŞ histerezisi 6 kova (30 dk, ayarlanır).**
  Kurulmuş koşuyu < 6 kovalık çukur kesmez.
- **prev_revision** girişten önceki EN YAKIN öteki-aktif kovadaki en yoğun
  revizyon; eşik-altı öncü kovalar (tavansız) koşunun başına dahil (kısmi
  ilk kova, uzun canary ısınması; yokluk öncünün öncesinden sayılır), öncü
  > H ve prev yoksa rampa. Koşu başı geriye/ileri kayarsa satır
  taşınır (started_at değişmez).
- **Durumlar (satırın kendi revizyonu R, önceki P):** `completed` R aktif,
  ötekiler ≥ EH kovadır yok; `rolled_back` R çekildi ve çekilişten sonra ilk
  aktif görülen öteki P (olay çekilen revizyonun satırında); `superseded`
  ilk aktif görülen öteki P değil (ya da aynı revizyonun daha yeni satırı
  var); `in_progress` aksi hâlde (kimse aktif değilse yalnız not). Çekiliş
  sonrası karar tek kovaya değil ileriye yürüyerek verilir. Terminal ve
  completed satırlar donuk. Bayat açık satırın bitişi pencere başı − 1 kova
  (uydurma damga yok).
- Karar kovası EPOCH hizalı; kova {1,5,10,15,30} dk; bağlı kelepçeler
  H·B ≥ 10 dk, EH·B ≥ 30 dk, EH ≥ H, lookback ≥ 4·EH·B (≤ 48 sa; sığmazsa EH
  düşer), overlap ≤ lookback/2.
- Reconciler: gerçek pencere başı Reconcile'a geçirilir; koşu satırı ayrık
  ctx (kapanışta kesilen tik `skipped`), tik süresi 5×aralık (≥ 2 dk),
  lider lease 3 dk (LeaderTTL(1 dk)), yazım öncesi liderlik yeniden doğrulanır, etkinlik
  tavanında kesilen iş yükü adıyla ilan edilir, önceki satırlar keyset
  sayfalı (300k tavanı HATA); tail kursörü keyset (`RolloutCursor`),
  DateTime64 bind'leri `toDateTime64(?,3,'UTC')`, 0 ↔ 1970 eşlemesi.
- **Varlık ≠ etkinlik (4. tur):** giriş/tamamlanma EŞİK üstü etkinlikle,
  "çekildi"/"yokluk" HERHANGİ bir span'in olmamasıyla karar verilir — eşik
  altına inen ama span üreten revizyon (canary çukuru) çekilmiş sayılmaz.
  **Devralma testi:** yoklukta "başkası aktifti" için ötekinin eşik üstü ve
  yokluk öncesi tabanın en az yarısı kadar trafik taşıması gerekir (düz %8
  canary, yerleşik revizyonun dalmasını rollout yapamaz). **Küme veri
  başlangıcı** (`Input.DataStart`, MV ufkundan): veri pencere içinde
  başlıyorsa (ilk etkinleştirme, yeni küme) başlangıçtan < EH kova sonra
  başlayan koşular satır açmaz. Etkinlik okuması kesikse (`Truncated`)
  etkinliği görünmeyen iş yükleri o tikte dokunulmaz. Önceki revizyonun
  çekilişi pencere dışında kaldıysa overlap üretilmez, üst-sınır notu düşer.
  `RolloutFirstSeen` ufuk tavanı (500k) aşılırsa HATA; ufuk MV TTL − 1 g.
  Beşinci tur: kalıcı satırla birebir aynı çıktı yazılmaz (tik başına
  gereksiz upsert/tail yok); bayat-satır kapatıcısı hâlâ span üreten
  (eşik altı) revizyonu çekilmiş saymaz; ortak sessizlik (aday da ≥ EH
  sustu, yokluktan önce birlikte aktifti) devralma değildir; aynı
  revizyonun taze kopyası bayat tablo kopyasını ezer; geçici notlar
  (zayıf sinyal / durağan durum / trafik yok) her tikte yeniden
  değerlendirilir.
  Altıncı tur: devralma testinde de ortak-sessizlik kapısı (ingest
  kesintisi + bir kova farklı dönüş olay değil); devralma tabanı tek kova
  değil, son aktif kovada biten EH kovalık dilimin en yüksek span'i
  (rampa-aşağı sonrası taban çökmesin; dilim gözlenmediyse dönüş kovası);
  ortak sessizlikten yalnız önceki revizyon dönerse karar ertelenir ve
  ≥ EH kova tek başına aktif kalınca rollback yazılır (bir lookback
  ertelenmez); bayat-satır kapatıcısı geçici notları temizler.
  Yedinci tur: ortak-sessizlik kapısı süreye dayalı (kardeş ≥ EH kova tek
  başına aktif kalana dek tutar; gözlenmemiş son-aktif kova "birlikte"
  sayılır); ertelenen aday aktif kalmazsa erteleme düşer ve kova yeniden
  değerlendirilir; gözlenmemiş taban için dönen koşunun kendi seviyesi;
  kesik okumada bayat-satır kapatıcısı da atlanır.
- Bilinen sınırlar: reconciler kesintisinde pencere kenarına 30 dk'dan
  yakın bir deploy kaçırılır (kenar yokluğu kanıt sayılmaz); canary
  dalması yerleşik düz kalırken canary'nin kendi dönüşü satır açabilir;
  farklı lookback/saatle koşan iki yazıcıda completed_at/traffic_confirmed_at
  ±1 kova oynayabilir (started_at oynamaz); devralma testi hacme bağlıdır —
  yokluk öncesi tabanın yarısından az trafik taşıyan halef geri dönüş
  satırını açmaz; açık satırın span_count'u kova başına bir kez güncellenir —
  kayıt yerine sessizlik bilinçli tercih.
