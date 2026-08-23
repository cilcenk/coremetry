# Coremetry veri modeli vs Dynatrace entity modeli — denetim

**Tarih:** 2026-08-23 · **Taban:** `git HEAD = 6291bb53` (v0.9.1305) · **Tip:** salt-okuma denetim, kod değişikliği yok
**Dosya:** `/Users/cenk/Documents/gotrace/docs/audit/entity-model-audit-2026-08-23.md`

## Kapsam

Soru: *Coremetry'nin telemetri-türevli veri modeli, Dynatrace'in kalıcı entity (Smartscape) modeline karşı nerede duruyor; bir entity katmanı eklemek doğru karar mı?*

Kapsam içi: `internal/chstore/`, `internal/otlp/`, `internal/topology/`, `internal/correlator/`, `internal/anomaly/`, `internal/evaluator/`, `internal/rca/`, `internal/api/`, `internal/logstore/` (kimlik yüzeyi), `frontend/src/lib/*Href.ts`, `migrations/*.sql`, `charts/` (yalnız dağıtık kip kararları için).

Kapsam dışı — **okunmadı**: canlı ClickHouse kümesinde ölçüm (tek istisna: aşağıdaki DDL doğrulamaları statik, `system.query_log` okunmadı), prod ES, Thanos/`/clusters` hattının iç kimlik uzayı, Grafana panel hattı, demo generatörler, Playwright/UI davranışı.

## Yöntem

6 paralel ajan + 1 doğrulama turu:

| # | Ajan | Ne okudu | Taban HEAD |
|---|---|---|---|
| 1 | Kalıcı kimlik envanteri | `store.go` `migrate()` 45 tablo + 3 dosya-dışı DDL + 20 MV, `model.go`, tüm `*_metadata`/`*_groups` yazıcıları | `c294c1b4` (v0.9.1101) |
| 2 | Anahtar tutarlılığı | 22 okuma yüzeyi (API rota → SQL → MV), frontend `*Href.ts` ailesi | `c294c1b4` |
| 3 | Çağrı grafiği | `internal/topology/`, `chstore/topology.go`, `backtrace.go`, `service_map.go`, `external.go`, `api/topology.go`, `api/servicegraph.go` | `c294c1b4` |
| 4 | Korelasyon mimarisi | `internal/anomaly/` 19 dosya, `internal/evaluator/` 14 dosya, `internal/correlator/` 3, `internal/rca/` 3 | `c294c1b4` |
| 5 | Dynatrace referansı | `~/.claude/skills/dt-*` satıcı dokümanları + `go.opentelemetry.io/proto/otlp@v1.10.0` proto kaynağı + repo karşılaştırması | `c294c1b4` |
| 6 | Entity katmanı maliyeti | `/clickhouse-schema` skill + `cluster.go` üç registry + `ddl_defer.go` + sayım tabanları | `537dc167` (v0.9.1304) |
| 7 | Karşıt sav (adversaryal) | Aynı yüzeyler, **çürütme** görevi ile; 649 `service_name` referansı, 434 rota, 164 `serveCached` | `537dc167` |

**Bu dokümanı yazarken yapılan bağımsız doğrulama** (HEAD `6291bb53`): 10 tablo DDL satırı, `defaultShardPolicy` içerik sayımı, `runServiceTeamDeriver` canlılığı, `problems`/`anomaly_events` PARTITION cümlesi, `service_adjacency.go` filtresi, `entities` tablosunun yokluğu. Sonuçlar §Ek'te.

**Dürüstlük şerhi:** Ajan 1-4 `v0.9.1101`, ajan 6-7 `v0.9.1304` tabanlıdır — arada **204 sürüm** vardır. Aşağıda kritik satır numaraları HEAD'de yeniden doğrulanmıştır; doğrulanmayanlar `[v1101 tabanlı]` etiketi taşır.

---

## Yönetici özeti

1. **Coremetry'de kalıcı kimliği olan tek telemetri varlığı servistir ve o da envanter değil ek-açıklamadır** — `service_metadata` (`internal/chstore/store.go:1066`, doğrulandı) satırı ancak biri düzenlerse veya deriver türetirse doğar; diğer 11 kavramın hepsi okuma anında SQL `coalesce`/`multiIf` zincirleriyle üretilir ve hiçbir yerde saklanmaz.

2. **Aynı fiziksel şey üç yüzeyde üç farklı anahtar taşıyor ve bu ölçülmüş zarar üretiyor** — bir Postgres `/databases`'te 6 basamaklı coalesce ile (`store.go:3226-3243`), `/service-map` odakta 3 basamaklı ile (`chstore/topology.go:703-708`), `/service-map` globalde instance'sız (`chstore/service_map.go:460`) kimliklenir; sonuç çıkmaz link (`frontend/src/components/topology/nodeDetailHref.ts:75-82` `null` döner) ve sahte satır.

3. **En pahalı dört çakışmanın (Ç1/Ç3/Ç4/Ç5) kökü kimlik değil, MV ailesinde `deploy_env` ve `kind` boyutunun olmamasıdır** — `service_summary_5m ORDER BY (service_name, time_bucket)` (`store.go:3021`, doğrulandı); bir entity katmanı bunları **çözmez**, dolayısıyla entity katmanı bu dört kalem için yanlış alettir.

4. **Entity katmanının işe yarayan versiyonu, entity katmanı olmayan versiyonudur** — doğal anahtar her yerde birincil kalırsa (`service_name` bugün `defaultShardPolicy`'de **18** girdinin shard anahtarı, `internal/chstore/cluster.go`, doğrulandı) `entity_id` sıcak yolda dekoratif olur ve rename dayanıklılığı hedefini karşılamaz; anahtar olursa ingest'e çözümleme bağımlılığı girer ki bugün böyle bir arıza sınıfı yoktur.

5. **Öneri: entity tablosu ŞİMDİ hayır; bunun yerine (a) doğrulanmış P1 partition bug'ı, (b) `service_seen` MV'si ile yaşam döngüsü, (c) paylaşılan kimlik-ifadesi sabitleri + pin testleri, (d) `node_kind='service'` filtresinin gevşetilmesi** — dördü toplamda ~4-5 gün, entity katmanının vaat ettiği değerin büyük kısmını ikinci bir kaynak-hakikat yaratmadan verir.

---

## 1. Kalıcı kimlik envanteri

**Sayım tabanı:** `store.go` `migrate()` `tables` dilimi = 45 `CREATE TABLE IF NOT EXISTS` + dosya-dışı 3 (`ldap_groups.go:28`, `api_tokens.go:30`, `rag.go:55`) = **48 kalıcı tablo**; `store.go` içinde **20 MaterializedView** `[v1101 tabanlı]`; `migrations/0001–0008.sql` operatör-uygulamalı 4 rollup ailesi (boot'ta koşmaz).

### 1.1 Aday kavramlar

| Kavram | Birinci sınıf mı | Nerede yaşıyor | Kimlik kaynağı | Yaşam döngüsü |
|---|---|---|---|---|
| **service** | **Yarı** — tablosu var, registry değil | `spans.service_name` LC kolonu `store.go:970`; `service_metadata` `store.go:1066` (✅doğrulandı); `service_summary_5m ORDER BY (service_name,time_bucket)` `store.go:3021` (✅) | Doğal anahtar `service.name`; yoksa literal `"unknown"` `internal/otlp/convert.go:37` | **Yok.** `first_seen`/`last_seen`/`deleted` hiçbir yerde yok; varlık = "pencerede span var mı" |
| **endpoint / operation** | Hayır — kolon | `spans.http_route/name/op_group` `store.go:980,968,992`; `operation_summary_5m` `store.go:3048`; `spanmetrics_1m` `store.go:3124` | Bileşik doğal anahtar `(service_name, name\|http_route)`; şekil normalizasyonu **iki yerde**: ingest `templater.NormalizeOperation`→`op_group`, okuma `opSigWrap` `chstore/endpoints.go:123-154` | MV TTL 90g/30g dışında yok |
| **host** | Hayır | `spans.host_name` `store.go:971`, `logs.host_name` `store.go:1010`; envanter **tamamen okuma-anı** `chstore/hosts.go:97` `GROUP BY host_name` | `host.name` resource attr `otlp/convert.go:38` | Yok; `Up`/`LastSeen` türetilmiş `hosts.go:24-33`; `hosts.go:1-16` yorumu `host_summary_5m` MV'sini bilinçli erteliyor |
| **process / instance** | Hayır — **kolon bile değil** | `service.instance.id` `metric_points`'te kolon DEĞİL; yalnız `series_fingerprint` hash'ine girer `otlp/convert.go:233-252`, `otlp/fingerprint.go:51,79-90` | `instanceIdExpr` `chstore/deploys.go:432-446` | Yok |
| **pod / container** | Hayır | Yalnız `res_keys/res_values`; tek kalıcı iz `problems.pod` `store.go:1381` — pod'un değil bir *problemin* damgası | ⚠ bkz. Ç-B | Çıkarımsal: pod kümesi diff'i `deploys.go:546`, ReplicaSet hash `deploys.go:669,698` |
| **database instance** | Hayır — ama MV satır anahtarı olarak kanonikleşmiş | `db_summary_5m ORDER BY (db_system, instance, db_name, ts)` `store.go:3214-3216`; `db_caller_summary_5m` `store.go:3261` | **6 basamaklı coalesce** `store.go:3226-3243`: `peer_service → server.address → net.peer.name → db.host → db.name → service_name → 'unknown'` | MV TTL 90g |
| **queue / topic** | Hayır | `messaging_summary_5m ORDER BY (msg_system, cluster, destination, ts)` `store.go:3509-3512`; topolojide `node_kind='queue'` `store.go:1896` | `destination` 4 basamaklı coalesce `store.go:3518-3523`; `cluster` ayrı zincir `store.go:3517` | 90g (MV) / 14g (topoloji) |
| **external dependency** | Hayır | `topology_edges_5m.child_node` içinde `ext:<peer>` + `node_kind='external'` `store.go:1895-1896` (✅1891 doğrulandı) | **Kod sabiti katalog**, tablo değil: 44 substring deseni `chstore/external_catalogue.go:53-119`, first-match-wins `:125` | 14 gün `store.go:1946` |
| **cluster** | Hayır — **üç ayrı kavram aynı adı taşıyor** | (1) k8s: `spans.cluster` MATERIALIZED `store.go:993`; (2) CH Distributed: `cfg.ClusterName` `chstore/cluster.go:26`; (3) Kafka bootstrap `store.go:3517` | (1) `clusterDeriveExpr` 6 permütasyon `chstore/repo.go:307-317` | Yok; liste okuma-anı `GetServiceClusterMap` `repo.go:389` |
| **namespace** | Hayır — ama `service_metadata`'da kalıcı türetilmiş alan | `service_metadata.namespace` + `namespace_auto` `store.go:2403-2404`; okuma-anı ikizi `chstore/service_namespaces.go:23` | ⚠ bkz. Ç8 (iki ifade, ters öncelik) | `service_metadata.updated_at`'e bağlı |
| **deployment / version** | **`version` tek gerçek yaşam döngülü varlık** | `service_version_5m ORDER BY (service_name, version, ts)` + `minState(time) AS first_seen_state` `store.go:3362-3372` | `effectiveVersionExpr` 7 basamak `deploys.go:87-115` | **VAR** — `first_seen` MV'de `minState` ile; TTL 45 gün |
| **team / owner** | Hayır — **kendi tablosu yok** | `service_metadata.owner_team/sre_team` `store.go:1068-1074`; `users.team` `store.go:2269`; alias `system_settings["team_aliases"]` `chstore/settings.go:542` | Serbest string; `NormTeamName`/`CanonTeam` `settings.go:563-590` | Yok |

### 1.2 Kalıcı kimliği OLAN tablolar — ve hiçbiri altyapı varlığı değil

| Tablo | Anahtar | Yazan | TTL |
|---|---|---|---|
| `service_metadata` `store.go:1066` ✅ | `service` (doğal ad) | admin PUT **+** deriver `PopulateAllServiceMetadataFromSpans` (`main.go:647,1547,1559` ✅ CANLI) | **YOK** |
| `exception_groups` `store.go:1121` ✅ | `sha1(type\|service\|stack)[:16]` `chstore/exception_inbox.go:73-87` | exception indexer | YOK — `first_seen`/`last_seen`/`resolved_at` ile **tam yaşam döngüsü** |
| `anomaly_events` `store.go:1267` ✅ | `sha1(kind\|pattern\|service)[:16]` `chstore/anomaly_event.go:57` | detector | 30g |
| `problems` `store.go:1369` ✅ | evaluator üretir (`runtimeProblemID`, `capacityProblemID`, `fatalExcProblemID`, `sharedBurstProblemID`) | evaluator | YOK |
| `ldap_groups` `ldap_groups.go:28` | `group_uid` = **objectGUID** | LDAP sync | YOK — `deleted` tombstone |
| `saved_views` `store.go:1248` ✅ | `id`; `page` 13 farklı değer | kullanıcı | YOK |
| `service_contracts` `store.go:1686`, `events` `store.go:2085` ✅, `log_templates` `store.go:2058`, `system_settings` `store.go:1096`, `trace_snapshots` `store.go:1711`, `audit_log` `store.go:1228`, `monitors`/`incidents`/`runbooks`/`dashboards`/`alert_rules`/`slos`/`notification_channels`/`api_tokens`/`rag_chunks` | çoğu `id` | operatör / worker | çeşitli |

**Anahtar gözlem:** 20+ kalıcı-kimlikli tablonun hiçbiri altyapı varlığı değil — hepsi (a) operatör konfigürasyonu, (b) tespit çıktısı, (c) kullanıcı/kimlik. Repoda **bir varlığın adı değişince kimliğinin korunduğu tek yer** `ldap_groups.group_uid`'dir (`ldap_groups.go:22-24`) ve kimliği **biz üretmiyoruz** — dizin veriyor.

### 1.3 Ad değişirse ne olur

- **Servis yeniden adlandırıldı → bağ tamamen kopar.** `service_metadata` yetim kalır; 20 MV'nin `service_name` ORDER BY öneki eski satırları eski anahtarda tutar; `exception_groups.fingerprint` servis adını hash'lediği için **her açık exception grubu sıfırdan doğar** (`exception_inbox.go:76-78`) ve `state`/`assignee`/`notes` eski satırda kalır; `anomaly_events.id` ve `problems.id` aynı kopuşu yaşar.
- **Repo'da servis alias/rename mekanizması YOK** — tek alias mekanizması takım adları içindir ve tablo değil `system_settings` blob'udur (`settings.go:542`) `[v1101 tabanlı]`.
- **Pod yeniden yaratıldı → kopuş yok, çünkü bağ zaten yok.** Ama metrik seri kimliği **bilinçli olarak** kopar: `series_fingerprint` yazar kimliği zincirini içerir (`otlp/convert.go:240-252`, v0.9.724 gerekçesi: aksi hâlde çok-pod'lu kümülatif sayaçlar tek fingerprint'e çöküyordu).

### 1.4 Go tipleri

`internal/chstore/model.go`: `Span:6`, `Log:38`, `MetricPoint:55`, `ExemplarRow:106`, `SpanLinkRow:128`, `Profile:340`. **Hepsinde kimlik alanları düz `string`** (`model.go:13-15`). Repoda **tek bir newtype yok** — `type ServiceName string` mevcut değil; derleyici bir servis adını host adıyla karıştırmanı engellemez. `grep -rn "type Entity\|entityID\|entity_id" internal/` → **0** `[v1101 tabanlı]`.

Okuma projeksiyonları tip olarak var (`ServiceSummary:207`, `EndpointRow` `endpoints.go:29`, `DBInstance` `dependencies.go:20`, `HostRow` `hosts.go:24`, `Rollout` `deploys.go:447` …) ama **hiçbiri kimlik tipi değil, hepsi pencere-satırı projeksiyonu**.

---

## 2. Anahtar tutarlılığı

### 2.1 Yüzey → anahtar (22 yüzey, `[v1101 tabanlı]` satır numaraları)

| Yüzey | Servis anahtarı | Kaynak | env/cluster anahtara katılıyor mu | dosya:satır |
|---|---|---|---|---|
| `/services` listesi | `service_name` | `service_summary_5m` | **HAYIR** — env verilirse MV'den DÜŞÜLÜR, ham `spans`'e geçilir | `chstore/summary.go:489-498` |
| `/service` Overview | `service.name` **+ `kind IN [server,consumer]`** | ham `spans` | env EVET | `frontend/src/lib/entrySpans.ts:29-48` |
| `/service` Operations | `(service_name, name\|op_group)` | `operation_summary_5m` / `_group_` | env EVET (ham yola zorlar) | `store.go:3048-3066,3092-3110` |
| `/endpoints` | `(service_name, http_route)` | `spanmetrics_1m/10s` | **env MV'de İMKÂNSIZ** — `errEndpointsMVEnv` ile REDDEDİLİR | `chstore/endpoints.go:440-448,490-494` |
| `/databases` | **`(db_system, instance, db_name)`** üçlüsü | `db_summary_5m` | env EVET ama **MV'yi TERK EDER** | `store.go:3214-3248`, `dependencies.go:172-180` |
| `/service-map` GLOBAL | `db:<db_system>` (instance YOK), `ext:<peer_service>` | **200 trace ÖRNEKLEMİ**, ham `spans` | HAYIR | `chstore/service_map.go:460,473` |
| `/service-map` ODAK | `db:<db_system>@<infra_host>`, `queue:<sys>:<dest>` | `topology_edges_5m` | HAYIR — `parent_env`/`child_env` ORDER BY'da DEĞİL | `chstore/topology.go:703-708,742-743`; `store.go:1891-1944` |
| `/topology` (op BFS) | `service_name + "\|" + op` | `topology_op_edges_5m` | **HAYIR — `env` parametresi hiç OKUNMUYOR** | `internal/api/topology.go:48-72` |
| `/external` | `substring(child_node,5)` | `topology_edges_5m` + `service_summary_5m` NOT IN | hayır | `chstore/external.go:101-118` |
| `/messaging` | `(msg_system, cluster, destination)` | `messaging_summary_5m` | messaging cluster EVET (k8s değil) | `store.go:3509-3512` |
| `/traces` | `service_name` | `trace_summary` / ham `spans` | **env EVET + cluster EVET** — en eksiksiz yüzey | `chstore/repo.go:2038-2044,2262` |
| `/logs` | ES `fields.Service` + 4 basamak; **`-prod/-int/-uat/-prep` SOYULUR** | ES / CH `logs` | env EVET | `internal/logstore/env_suffix.go:29-41` |
| `/problems` | `problems.service` düz string | `problems` RMT | **env "SERVİS-KAPSAMLI"** — satır süzgeci DEĞİL | `chstore/env_members.go:147-160` |
| exception Inbox | `sha1(ex_type\|service_name\|stack)` | `exception_groups` | env: **fingerprint'te YOK** | `exception_inbox.go:73-87,1231-1236` |
| SLO availability | `service_name`, **TÜM span'ler** | `service_summary_5m` | HAYIR | `chstore/slo.go:135-152` |
| SLO latency | `service_name` **+ `kind IN (server,consumer)`** | ham `spans` | HAYIR | `chstore/slo.go:115,157-170` |
| Alarm değerlendiricisi | `service_name`, **TÜM span'ler** | `service_summary_5m` | **HAYIR — hiçbir env/cluster boyutu yok** | `chstore/evaluator_reads.go:219-249` |
| blast radius | `(service, caller_service)` | `service_callers_5m` | hayır | `store.go:1969-1994` (✅1969 doğrulandı) |

### 2.2 Çakışma listesi

| # | Çakışma | Somut örnek | Kanıt | Entity tablosu çözer mi |
|---|---|---|---|---|
| 🔴 **Ç1** | `/services` ile `/service` **farklı popülasyon** | Kodun kendi yorumu ölçmüş: "demo veride rps 1.6 → 0.2 (**8× DÜŞER**), error rate %2.03 → %9.79 (**5× ÇIKAR**)". Operatör listede %2 görüp geçer, alarm da %2'yi kullanır → giriş-span'lerde 5× kötü servis eşiği hiç geçmez | `frontend/src/pages/service/Overview.tsx:248-251`; `store.go:3033`; `summary.go:498` | **HAYIR** — kök neden MV'de `kind` boyutu yok |
| 🔴 **Ç2** | Aynı DB, **üç düğüm kimliği** | `peer.service`+`server.address`+`net.peer.name` boş, `db.host` dolu bir span'de: `/databases` `instance=orders-rds.internal`, topoloji düğümü çıplak `db:postgresql` → `splitDbNodeName` `@` bulamaz → **link HİÇ çizilmez**. Ayrıca 6. basamak `service_name` olduğu için `/databases`'te `postgresql / checkout-api / default` sahte satırı belirir | `store.go:3230-3240`; `chstore/topology.go:703-708`; `service_map.go:460`; `frontend/src/components/topology/nodeDetailHref.ts:75-82`; `dependencies.go:178` | **Hayır** — paylaşılan sabit yeter |
| 🔴 **Ç3** | Alarm env'siz, `/problems` servis-kapsamlı | `checkout-api` prod+uat'ta koşuyor; uat yük testi hata oranını %20 yapıyor; alarm TOPLAMDAN açılıyor, `?env=prod` "servis prod'da var mı? evet" deyip satırı GÖSTERİYOR; `/traces?env=prod`'da (gerçek süzgeç) hiç hatalı trace yok | `evaluator_reads.go:219-233`; `store.go:3034`; `env_members.go:130-160`; `repo.go:2040` | **HAYIR** — MV'de env yok |
| 🟠 **Ç4** | Exception fingerprint env taşımıyor | uat'ta 5.000 kez patlayan NPE + prod'da 3 kez patlayan aynı NPE = **tek satır, 5.003 occurrence** → uat gürültüsü kalıcı prod P1 üretebiliyor | `exception_inbox.go:73-87,1231` | **HAYIR** (hash girdisi eksik) |
| 🟠 **Ç5** | SLO'nun kendi içinde iki denominatör | availability MV'den kind süzgeçsiz, latency ham `spans`+kind; yorumda prod ölçümü: "241.919 entry span, all-span SLI %99,99, entry-span SLI %99,85" | `slo.go:107,135-152,157-170` | **HAYIR** |
| 🟠 **Ç6** | `/topology` `?env=` parametresini hiç okumuyor | Handler `root/root_op/depth/from/to` okur, `env` yok; cache key'de de yok. `/endpoints`'in dürüst reddinin (`errEndpointsMVEnv`) tam tersi | `internal/api/topology.go:48-72`; `endpoints.go:20` | **HAYIR** |
| 🟠 **Ç7** | Logs env ekini SOYUYOR, span yüzeyleri soymuyor | `mobile-overview-bff-prod` ve `-int` `/services`'te iki satır, Logs'ta tek arama terimi → her ikisinin logları iki servisin altında | `logstore/env_suffix.go:29-41`; `elasticsearch.go:2588` | **Kısmen** (alias baskısı gerçek) |
| 🟡 **Ç8** | İki namespace türetmesi, **ters öncelik** | `service_metadata.go:632-640`: `service.namespace` → `k8s.namespace.name`; `service_namespaces.go:32-38`: tersi. `service.namespace=payments` + `k8s.namespace.name=payments-prod` yayan servis iki yüzeyde iki namespace'e düşer | aynı | **Hayır** |
| 🟡 **Ç9** | `clusterExpr` pakette **iki anlam** | `dependencies.go:150` paket sabiti = MESSAGING cluster; `repo.go:352` *Store metodu = K8S cluster. Bugün doğru kullanılıyor ama `s.` düşerse Go **sessizce** paket sabitini bağlar → `/traces` cluster süzgeci Kafka bootstrap adresine bakar, derleme hatası yok | aynı | **Hayır** |
| 🟢 **Ç10** | `ext:` çift kaynak | `peer.service` vs `server.address` — aynı üçüncü taraf `payments-gateway` ve `payments.gw.internal:8443` diye iki satır | `chstore/topology.go:756-774`; `external.go:101` | **Hayır** |

**Mimari kök neden tek:** `service_summary_5m` / `operation_summary_5m` / `spanmetrics_1m` / `db_summary_5m` — MV ailesinin **hiçbiri `deploy_env` veya `cluster` boyutu taşımıyor** (`store.go:3021` ✅ doğrulandı). Env isteyen her yüzey ya MV'yi terk ediyor, ya sessizce yok sayıyor, ya servis-kümesi yaklaşımına düşüyor. **Üç kaçış stratejisi = üç farklı cevap.**

### 2.3 Frontend — anahtar SIZMIYOR

`frontend/src/lib/serviceHref.ts:63` tek `/service?name=` üreticisi, **56 çağrı noktası**, canlı kodda el-yapımı link **0** (`serviceHref.test.ts:319` kaynak taramasıyla pinliyor). `nodeDetailHref.ts:113` çeviremediğinde **`null` döner** — uydurma link çizmez. `databaseParam.ts:1-9` yorumu: *"IDENTITY IS A TRIPLE, not a label"*. Kardeşleri (`pivotHref`, `logsUrl`, `inboxUrl`, `dashboardHref`, `navHref`) hepsi test dosyalı. **Tek kusur:** `pages/ServiceMap.tsx:15` `serviceGraphToMap`'i import edip kullanmıyor (ölü import) `[v1101 tabanlı]`.

---

## 3. Çağrı grafiği — kalıcı mı, türetilmiş mi

### 3.1 Kısa cevap: kalıcı grafik YOK; 5 dakikalık kovaların yığını

Dört tablo — ve ⚠ **Ç-E: dördü de düz `ReplacingMergeTree` tablodur, ClickHouse MaterializedView'ı DEĞİL** (kod yorumları "MV" dese de); Go tarafındaki `INSERT … SELECT` doldurur.

| Tablo | DDL | ORDER BY | TTL |
|---|---|---|---|
| `topology_edges_5m` | `store.go:1891` ✅ | `(time_bucket, parent_service, child_node, node_kind, protocol)` | 14 gün `store.go:1945` |
| `service_callers_5m` | `store.go:1969` ✅ | `(time_bucket, service, caller_service, caller_host, caller_instance, client_address, user_agent)` | 14 gün |
| `topology_op_edges_5m` | `store.go:1996` | `(time_bucket, parent_service, parent_op, child_service, child_op)` | 14 gün |
| `topology_root_flows_5m` | `store.go:2014` | `(time_bucket, root_service, root_op)` | 14 gün |

Kanıt: `time_bucket` ORDER BY'ın **başında** → aynı A→B çifti her 5 dakikada **yeni satır**; hiçbir tabloda `first_seen`/`last_seen`/`edge_id` yok; okuma daima pencere-içi `sum()` (`chstore/topology.go:1266`, `repo.go:1729`, `service_adjacency.go:118-120`). Pencere dışına çıkan kova görünmez olur, **"kenar silindi" diye bir olay üretilmez**. Retention operatöre açık değil — `chstore/retention.go:85-102` `plans` listesinde bu dört tablo yok, 14 gün DDL'e gömülü.

### 3.2 Kim yazıyor, ne kadar maliyetle

Tek arka plan goroutine'i, `internal/topology/aggregator.go`, Redis lider kilidiyle (`:20,:30,:49`); `main.go:650` → `topology.New(store, 5*time.Minute, 1*time.Hour, lockImpl)`. İki tik: kapalı-kova (`:165-207`, 5 dk + 30 sn settle) 4 yazıcıyı da çağırır; canlı tik (`:116-163`, **60 sn**) açık kovayı yeniden yazar, `WriteRootFlowsBucket`'ı atlar (`:159-162`).

**Yazma amplifikasyonu:** aynı 5 dk penceresi için **~6 tam agregasyon koşusu**; her koşu tüm kova satırlarını yeni `version` ile yeniden INSERT eder (`topology.go:648,823,903,943,1024`, `backtrace.go:73`). Doğruluğu `FINAL` kurtarır, maliyeti merge katmanı öder.

**Iraksama bulgusu:** `topoJoinMemSettings()` (`chstore/topology.go:526-531`, bütçe `:516` = 1,5 GB) prod'daki 241 hatasından sonra "TEK kaynak" olsun diye yazıldı ve yorum uyarıyor (`:518-521`). Ama `WriteServiceCallersBucket` **kullanmıyor** — `chstore/backtrace.go:68-70` hâlâ elle `join_algorithm='grace_hash', max_bytes_in_join = 4000000000`, `grace_hash_join_initial_buckets` yok, eşik `heavyScanMemory = 8e9`'un (`query_memory.go:60`) **yarısı** — yorumun "tavana bu kadar yakın eşik hiç ateşlenemez" dediği hâl. Aynı tikten çağrılıyor (`aggregator.go:155,199`) `[v1101 tabanlı, doğrulanmadı]`.

### 3.3 Yön bilgisi nerede

**Kanonik olan KOLONLAR** (`parent_service`/`child_node`, `caller_service`/`service`). `span kind + parent_id` zinciri yalnız **yazma anında** okunur, okuma yolu asla zinciri tekrar yürümez.

| Yön kaynağı | Yer |
|---|---|
| `p.span_id = c.parent_id` JOIN → `parent_service`/`child_node` | `chstore/topology.go:632-641` |
| aynı JOIN, op düzeyinde | `topology.go:929-934` |
| aynı JOIN, ters isimlendirme (`c`=çağrılan, `p`=çağıran) | `backtrace.go:44-61` |
| **kind + attribute**, parent_id YOK → sentetik hedef | `topology.go:741-776` |
| **YÖN TERS ÇEVRİLİR** — kuyruk parent, consumer child | `topology.go:878-881` |

**İki bağımsız yön deposu ayrışabilir:** cross-service pass'i `topoNoiseExcludeSQL` ile cache-refresh trafiğini eler (`topology.go:642`), `service_callers_5m` **elemez** (`backtrace.go:62-64`). Op-edges pass'i `p.service_name != c.service_name` koşulunu **hiç koymaz** (`topology.go:935-937`) — servis-içi op→op kenarlarını da içerir.

**Tuzak:** `parent_service` her zaman bir SERVİS değil — consumer pass'i oraya `queue:<system>:<topic>` yazar ve `node_kind`'ı **`'service'`** damgalar (`topology.go:879-881`). Sonucu: `GetServiceAdjacency`/`…Weighted` `node_kind='service'` filtresi kullanıyor (`service_adjacency.go:62,122` ✅ doğrulandı) → **korelatörün grafiğine `queue:…` düğümleri "çağıran servis" olarak giriyor**. Aynı sınıf frontend'de yaşandı ve düzeltildi (`api/topology.go:1029`, v0.9.1029).

⚠ **DÜZELTME (v0.9.1327) — teşhis doğru, ÇARE yanlıştı.** Yukarıdaki gözlem
(`parent_service` her zaman servis değil; kuyruk düğümleri korelatörün
grafiğine "çağıran servis" olarak giriyor) doğrudur. Ama bundan "damga
yanlış" sonucu çıkarılamaz: `node_kind` **çocuğun** türünü taşır ve o
geçişte çocuk (`service_name`) gerçekten tüketici servistir. Yüklem
çocukları AŞIRI, parent'ları ise HİÇ süzüyordu. Çare yazıcıda değil okuma
tarafında: yüklem kalkar, iki uç da ham-ID önekinden tiplenir
(`TopologyNodeIdentity`, `chstore/identity.go`). Bu ayrım kodda zaten
yazılıydı — `servicegraph.go`'nun v0.9.1028/1029 şerhleri aynı şeyi
söylüyor.

### 3.4 Kim okuyor

**SQL noktaları:** 6 INSERT, 13 SELECT (+1 probe) `[v1101 tabanlı]`.

**Grafik tablolarından okuyan 12 HTTP rotası:** `/api/topology`, `/topology/ops`, `/topology/service`, `/topology/flows`, `/topology/drawio`, `/topology/service/drawio`, `/servicegraph`, `/services/graph`, `/services/{n}/backtrace`, `/services/{n}/blast-radius`, `/external`, `/external/host`.

**Grafik tablolarını ATLAYIP ham `spans`'tan türeten 5 rota + 2 panel:** `/service-map` (**200 trace**, `api.go:3382`), `/services/{n}/neighbors` (**50 trace**, `neighbors.go:38`), `/topology/flow`, `/topology/flow/drawio`, `/topology/edge/instances`; endpoint "kim çağırıyor" (**800 span**, `endpoints_callers.go:58`), endpoint "zaman nereye gidiyor" (**200 trace**, `endpoints_downstream.go:48`).

**Sonuç — aynı soruya iki cevap:** `/service-map` sayfası örneklenmiş grafiği çizerken (`pages/ServiceMap.tsx:90`), aynı sayfadaki odak paneli MV grafiğini çiziyor (`FocusedNeighborhood.tsx:157`). Bilinçli (v0.8.273) ama iki kaynağın kenar kümesi aynı olmak zorunda değil.

**AI hattı ham örneği görüyor:** `copilot_aianalyze.go:270` ve `api.go:8972` `ServiceNeighbors`'ı (50-trace örneği) kullanıyor — MV'yi değil.

**Ölü kod (canlı çağıran YOK):** `GetServiceTopologyEdges` (`chstore/topology.go:1425`), `GetTopologyEdges` (`:27`), `ServiceCallers` (`backtrace.go:212`).

### 3.5 Yürüme derinlikleri — dört farklı tanım

| Yürüyücü | Derinlik | Yer |
|---|---|---|
| Kök-neden propagation | **2 hop**, 0.5 sönümlü, yalnız `node_kind='service'` | `correlator/propagation.go:45`; `service_adjacency.go:122` |
| Incident komşuluğu | **1 hop** (`in ∪ out`) | `correlator/correlator.go:66-69`; `chstore/incident.go:396` |
| Topoloji UI (focus) | **3 hop** BFS, frontier tavanı 300 | `chstore/topology.go:1368-1385` |
| op-düzeyi BFS | **1-6 hop**, ≤50k kenar belleğe | `api/topology.go:60-68,117-138` |
| Blast radius | **1 hop upstream**, `LIMIT 25` | `chstore/blast_radius.go:93-105` |

**Aynı ürünün dört yerinde dört bağımlılık-derinliği tanımı var ve hiçbiri diğerinin sonucunu görmüyor.**

### 3.6 Kenarın kaybolması — zayıf halka

Kayboluş bir OLAY değil, yokluk. Üç kıyas mekanizması var, **hiçbiri MV yolunda "kayboldu" demiyor**: `?compare=prior` yalnız mevcut kenarlara prior işler (`api/topology.go:565-583`); `mergePriorGraphEdges` aynı (`api/servicegraph.go:436`); **tek gerçek kayıp tespiti** `annotateDiff` `RemovedNodes`/`RemovedEdges` üretir (`service_map.go:254-295`) ama **ham-örneklenmiş** yolda — yani "payment servisi fraud-check'i çağırmayı bıraktı" cevabı 200-trace örneğine dayanıyor. `internal/anomaly`, `internal/evaluator`, `internal/templater` altında `topology_edges_5m`'e **hiç referans yok** → topoloji değişimi için alarm/anomali dedektörü yok `[v1101 tabanlı]`.

---

## 4. Entity katmanı maliyeti

> Bu bölüm **fiyat listesidir**, tavsiye değil. Tavsiye §7'de ve §6'nın karşısında verilir.

### 4.1 Tablolar — DDL taslağı

Tek tablo, tip başına şema yok (`saved_views(page=…)` invariant'ının ikizi, `service_metadata`'nın motor kardeşi):

```sql
CREATE TABLE IF NOT EXISTS entities (
    kind          LowCardinality(String),        -- service | dependency | instance
    entity_key    String CODEC(ZSTD(3)),         -- OKUNABİLİR doğal anahtar, hash DEĞİL
    id_keys       Array(LowCardinality(String)), -- OTel EntityRef.id_keys ikizi
    display_name  String DEFAULT '' CODEC(ZSTD(3)),
    parent_key    String DEFAULT '' CODEC(ZSTD(3)),   -- entity_key'in SAF FONKSİYONU
    first_seen    DateTime64(9) CODEC(Delta, ZSTD(3)),
    last_seen     DateTime64(9) CODEC(Delta, ZSTD(3)),
    envs          Array(LowCardinality(String)),      -- KİMLİK DEĞİL, annotation
    clusters      Array(LowCardinality(String)),      -- KİMLİK DEĞİL, annotation
    attr_keys     Array(LowCardinality(String)),
    attr_values   Array(String) CODEC(ZSTD(3)),
    aliases       Array(String) DEFAULT [] CODEC(ZSTD(3)),
    aliases_auto  Array(String) DEFAULT [] CODEC(ZSTD(3)),
    version       UInt64 DEFAULT toUnixTimestamp64Nano(now64(9))
) ENGINE = ReplacingMergeTree(version)
ORDER BY (kind, entity_key)
-- PARTITION BY YOK — bilinçli (Kural P1; v0.9.1304 emsali)

ALTER TABLE entities MODIFY TTL
    toDateTime(last_seen) + INTERVAL 30 DAY DELETE WHERE kind = 'instance',
    toDateTime(last_seen) + INTERVAL 365 DAY DELETE WHERE kind IN ('service','dependency');
```

`/clickhouse-schema` uyumu: O3 ✅ (ORDER BY = dedup anahtarı, `envs`/`clusters` anahtara EKLENMEDİ) · **P1 ✅** (PARTITION yok — `problems`/`anomaly_events` bugün bu kuralı ihlal ediyor, bkz. §Ek) · E2 (tam satır replace) read-merge-write ile · C2/C3 ✅ (`kind` LC, `entity_key` düz `String`) · C4 ✅ (Nullable yok).

**İlişki tablosu (`entity_edges`) — AÇILMAZ.** Gerekçe üç katlı: (1) dinamik kenar zaten `topology_edges_5m` ailesinde; (2) statik kenarın (`runs_on`/`is_part_of`) **veri kaynağı yok** — OTLP resource'tan türeyen tek kompozisyon `instance→service` ve `dependency→cluster`, ikisi de `entity_key`'in saf fonksiyonu → `parent_key` kolonu yeter; (3) graf motoru tek-ikili kısıtının zıddı. Fiyatı yine de yazılı: k8s API/Thanos'tan ikinci besleyici ≈ **+2 hafta** ve kazara-merge sınıfını davet eder.

### 4.2 ID stratejisi — **KARAR NOKTASI**

| | **A. Okunabilir doğal anahtar** | **B. Opak hash** | **C. UUID + eşleme tablosu** |
|---|---|---|---|
| Biçim | `service:checkout`, `dependency:db/postgresql@orders-rds` | `sha1(kind\|demet)[:16]` | `uuid4()` + `entity_aliases` |
| Ad→id çözümü | **Saf fonksiyon** | Hash hesabı | **Her okuma yolunda lookup** (75 API giriş noktası) |
| Paylaşılan link | Çalışır (`URL = source of truth`) | Çalışmaz | Anlamsız |
| Olay anında CH'de elle sorgu | `WHERE entity_key LIKE …` | İmkânsız | İmkânsız |
| Yeniden adlandırma | Yeni entity doğar, **operatör NEDEN'i görür** | Yeni entity doğar, **hangi alan değişti görünmez** (DT'nin "dashboard'lar bir gecede boşaldı" sınıfı) | Kimlik korunur **ama** bunun için bir **merge kuralı** gerekir = DT'nin en pahalı sessiz hata sınıfı |
| Çok-pod ingest yarışı | Yok | Yok | **Var** |
| Emsal | `databaseParam.ts:1-9`; `saved_views.page` | — | `ldap_groups.group_uid` (`ldap_groups.go:22-24`) — ama orada kimliği **dış otorite** veriyor |

**Karar: A.** C'nin tek üstünlüğü (rename dayanıklılığı) ancak bir merge kuralıyla gerçekleşir ve o kural reddedilmesi gereken şeydir. Ucuz yarı-yol: `aliases` kolonu — operatör eski adı ekler, arama bulur, **kimlik değişmez**.

**Kanonikleştirme** — Ç2/Ç10'un asıl çözüldüğü yer: `entity_key` üreteci **tek Go sabiti** olmalı ve `dbInstanceExpr`'in (`store.go:3226-3243`) aynı basamaklarını kullanmalı; farklı bir zincir yazmak Ç2'yi **dördüncü kez** üretmek olur. `InstanceEntityKey`'in `instID`'si `otlp/convert.go:237-248` zinciriyle simetrik olmalı, aksi hâlde metrik serisi ile entity ayrı şeyleri "aynı pod" sanır.

**Aynı servisin iki env'i: Faz 1'de AYNI entity.** Gerekçe kanıtlı: 77 `GROUP BY … service_name` satırı (✅ doğrulandı) anında ikili anlama düşer ve cevap veremez, çünkü MV'de `deploy_env` yok (`store.go:3021` ✅). 75 API giriş noktası `?service=<ad>` alıyor; ad artık tekil belirlemezse ya sessiz yanlış ya kırılma olur.

### 4.3 Kim doldurur

| | Ingest yolu | Ayrı worker | **Mevcut topoloji aggregator'ı** *(öneri)* |
|---|---|---|---|
| Gecikme | ~saniye | 10 dakika | ~1 dk (canlı tik) / 5 dk |
| Lider kilidi | **Yok — N pod N× yazar** | Yeni Redis mutex | **Zaten var** (`aggregator.go:20,30,49`) |
| `first_seen` doğruluğu | **Bozulur** — ya sıcak yolda CH okuması ya restart'ta sıfırlanan bellek seti | ✅ | ✅ |
| Yeni worker/kilit | — | 3 yeni parça | **Yok** |

Entity pass'i **JOIN'siz** ve yalnız LC kolonlar okur (`service_name`, `deploy_env`, `cluster`, `time`). Kıyas: `deriveMetadataAllSQL` (`service_metadata.go:614-670`) **14 `indexOf()` dizi probu** yapıp 3 saatlik pencere tarıyor ve 10 dakikada bir koşuyor.

**Tahmin (ölçülmedi):** 1B span/gün = 3,47M span/kova; 4 kolon, LC dict + ZSTD → **kova başına ~30-45 MB**; mevcut cross-service pass'i kabaca 10-20× fazla bayt okuyor → entity pass'i tik maliyetine **%5'in altında** ekler. Yazma: 1000 servis + ~2000 bağımlılık + ~5000 instance → **≤2,3M satır INSERT/gün**, `spans`'in 1B'lik yazımının **%0,23'ü**.

**Yazma semantiği: RMT + read-merge-write, AggregatingMergeTree DEĞİL.** Sebep tek: operatör-düzenlenebilir alan (`display_name`, `aliases`) olduğu an `anyLast`'ın provenance kavramı yoktur ve deriver operatörün düzenlemesini bir sonraki tikte ezer. `service_metadata`'nın `*_auto` deseni (`store.go:1070-1073`, `mergeTeams` `service_metadata.go:307`) burada zorunlu.

**İki-boot sözleşmesi:** küme kipinde DDL ertelenir (`ddl_defer.go`); yazıcı ve okuyucu `hasEntitiesTable` probe'una bağlanmalı, probe false ise **bugünkü davranışa** düşmeli.

### 4.4 Mevcut sorgulara etki

**Faz 1-4'te HİÇBİRİ.** `entity_id` hiçbir zaman `service_name`'in yerine geçmez; `entities` bir *yan kayıt*tır.

| Etkilenen | Taban | Faz 1-4'te değişen |
|---|---|---|
| `GROUP BY … service_name` | **77** (✅ doğrulandı) | **0** |
| `service_name =/IN/LIKE` | 116 `[v1304]` | **0** |
| API giriş noktası (servis adı okuyan) | 75 `[v1304]` | **0** |
| `store.go` `ORDER BY (service_name` | 16 `[v1304]` | **0** |
| `store.go` MaterializedView | 22 `[v1304]` | **0** — MV yeniden kurulumu YOK |
| frontend `serviceHref()` | **56** | **0** |

Değişen tek şey **eklenen** okumalar: `GET /api/entities`; `/services` satırına `lastSeen`/`firstSeen` (JOIN değil, ikinci sorgu + map — `env_members.go:147-160` deseninin ikizi); `GetServiceEnvMap` (`env_members.go:38`) ve `GetServiceClusterMap` (`repo.go:402-410`) **gövdeleri** entity okumasına döner, imzaları değişmez → **Faz 1'in ölçülebilir perf kazancı**: iki ham `spans` `GROUP BY` (biri derive-ifadeli, `LIMIT 50000`, `max_execution_time=8` + spill) yerine ~1000 satırlık `FINAL`.

**Geriye uyumluluk sözleşmesi tek cümle:** *`kind='service'` satırının `entity_key`'i her zaman `'service:' || service_name`'dir; ad→key saf fonksiyondur, çözüm tablosu yoktur.* Çift yazım / view / kademeli geçiş **gerekmez**, çünkü geçiş yok — katman ekleniyor.

### 4.5 Frontend

**URL'ler `entity_id`'ye GEÇMEZ.** `/service?name=<service_name>` aynen kalır: 56 `serviceHref()` çağrısı + 0 el-yapımı link, `serviceHref.ts:1-37`'nin anlattığı olay (03:14'te blast-radius pill'i, pencere kaybı) tekrarlanmasın diye. Kimlik zenginleşirse tek dikiş yeri `serviceHref()`'in opsiyonel `entity` parametresi olur; 56 çağrının hiçbiri dokunulmaz.

**Faz 1'de görünen gerçek UI değişikliği (küçük ve değerli):** `/services` tablosuna `Son görülme` kolonu (`useDataTable` + `sortValue`) + susmuş servisler için `.badge .b-warn`; "yalnız canlı / susmuşları da göster" filtresi, varsayılan **canlı** = bugünkü davranış (regresyon yok).

### 4.6 Aşamalandırma ve efor

| Faz | İş | Kazanç | Efor | Risk |
|---|---|---|---|---|
| **0** | `entities` DDL + 3 registry kaydı + `hasEntitiesTable` probe | Şema yerinde, davranış değişmiyor | **0,5 gün** | 🟢 |
| **1** | Servis entity yazıcısı (aggregator 5. pass, tik SONUNDA) + `GET /api/entities` + `/services` `lastSeen` + `GetServiceEnvMap`/`GetServiceClusterMap` gövde değişimi | **Susmuş servis görünür**; 2 sıcak harita ham taramadan çıkar | **3 gün** | 🟡 |
| **2** | `dependency` entity; `entity_key` üreteci `dbInstanceExpr`'in **tek** kopyasına bağlanır | **Ç2/Ç10 kanonikleşir**; "yeni bağımlılık belirdi" mümkün | **2 gün** | 🟡 |
| **3** | `maintenance_windows` + `anomaly_silences` kapsamı prefix'ten `entity_key`'e | `checkout*` yarın doğacak servisi susturmuyor | **3 gün** | 🔴 davranış değişikliği |
| **4** | `instance` entity + pod türetmelerinin tek sabite indirilmesi | Aynı pod her sayfada aynı ad | **5 gün** | 🟡🔴 ⚠ bkz. Ç-B |
| **5** | *(ayrı karar)* env kimliğe girer → 6-8 MV yeniden kurulumu | Ç3/Ç4 gerçekten çözülür | **+2-3 hafta** | 🔴 |
| **6** | *(önerilmez)* `entity_edges` + statik kenar besleyicisi | DT `runs_on` pariteği | +2 hafta | 🔴 |

**Toplam:** Faz 0-1 ≈ **1 hafta** · Faz 0-2 ≈ 1,5 hafta · Faz 0-4 ≈ **3 hafta** (13,5 gün) · Faz 0-4 için ~10-13 ayrı `v0.9.X` sürümü.

**Ölçüm sözleşmesi (kapanış kapısı):** Faz 1'de aggregator tik süresinin öncesi/sonrası `system.query_log` **medyanı** (tek ad-hoc zamanlama yalan söyler — `feedback-perf-benchmark-discipline`), hedef +%5 altı; `GetServiceClusterMap`'in `read_bytes`'ı; Faz 4'te `instance` satır sayısı 7 gün sonra, **>500k ise TTL 30g→7g**.

---

## 5. Bu katman olmadan tavan

### 5.1 Korelasyon bugün nereye kadar gidiyor

**Dedektörler:** `internal/anomaly` 9 aile (metrik z-skor `verdict.go:34`, servis-sustu `anomaly.go:892`, kümeleme `clustering.go:131`, davranış/rejim `behavior_scan.go:108`, exception fırtınası `exception_storm.go:70`, log kalıbı `log_patterns.go:223`, yeni log şablonu `recorder.go:129`, trace-op hata `trace_ops.go:75`, op gecikme `op_latency.go:61`) + `internal/evaluator` 9 dedektör + 4 yaşam-döngüsü geçişi (`evaluator.go:432-595`).

**Skorlayıcı yapısal, zamansal değil:** `correlator/propagation.go:79` `rankRootCauses` → `score = 0.5 · share(S→D) · share(D→E)`, en fazla **2 hop**, hop başına 0.5 sönüm. Kenar ağırlığı **1 saatlik toplam hata sayısı**, anomali penceresiyle hizalı DEĞİL. Yorumun kendi itirafı (`propagation.go:16,35-39`): *"STRUCTURAL error-propagation, not temporal correlation"*.

**Hakem:** `correlator/hypothesis.go:247` `Synthesize` — tier'li saf skorlayıcı (deploy 0.80-0.95, propagation komşu ≤0.70, kendi-servis sinyali 0.30-0.60, komşu sinyali 0.28-0.52, eş-yanma 0.20 düz).

**Kritik boşluk:** `chstore/correlate.go:205` `GetCorrelatedChangesMV` (zaman örtüşmesi) ve `GetServiceBlastRadius` **yalnız API fan-out'unda** (`api/rootcause.go:184`, `api/rca_extras.go:56`) — **sentezleyici bunları hiç görmüyor**. Zamansal korelasyon sinyali sıralamayı **hiç etkilemiyor**.

**Incident birleştirme string'e dayanıyor:** `chstore/incident.go:364`, 30 dk pencere, 3 kural (zaten bağlı / aynı servis / 1-hop komşu). ⚠ **`service = ''` geçerli bir gruplama anahtarı** — self-health (`selfhealth.go:653`) ve ES watcher (`watcher_eval.go:616`) problemleri `Service: ""` üretiyor ve kural 2'nin `service = ?` yüklemi `''` ile eşleşiyor (`incident.go:381`) → **30 dk içindeki tüm filo-düzeyi problemler tek incident'a çöker**: "ingest durdu" + ilgisiz bir ES watcher alarmı aynı olay sayılır `[v1101 tabanlı]`.

### 5.2 Cevaplanamayan somut sorular

| # | Soru | Bugün nereye kadar | Eksik olan |
|---|---|---|---|
| **S1** | "Bu pod hangi servisin?" | `chstore/podservice.go:50` `PodServiceMap` tam olarak bunu yapıyor | **Yalnız `/clusters` handler'ında, 15 dakikalık pencerede** (`api/thanos_handlers.go:95`). `grep PodServiceMap internal/anomaly internal/evaluator internal/correlator` → **0**. 15 dk'dan eski pod için cevap yok |
| **S2** | "Bu DB instance'ını kimler kullanıyor?" — **en keskin boşluk** | `dependencies.go:312` `GetDatabaseDetail` çağıran servis+pod kırılımını **tam** veriyor | **Üç kat:** (a) korelasyon grafiği DB'yi hiç görmüyor — `service_adjacency.go:62,122` `node_kind='service'` (✅ doğrulandı) → bir DB asla `TopSuspect` olamaz; (b) DB kapasite problemi topolojik izole (`Problem.Service = "corebank.prod"`, `db_capacity.go:291`; `Neighbors()` boş döner) → Oracle dolduğunda o Oracle'ı kullanan 15 servisin problemleriyle **asla aynı incident'a girmez**; (c) paylaşılan patlama dedektörü ortak nedeni *"muhtemel ortak bağımlılık (DB/ağ/DNS)"* diye **tahmin** ediyor (`exception_storm.go:38`) — oysa `db_caller_summary_5m` kesişimle **adlandırabilir** |
| **S3** | "Bu host çökerse ne etkilenir?" | `hosts.go:97` host→servis; `GetServiceBlastRadius` servis→çağıranlar | **İkisi hiç birleştirilmiyor.** Hiçbir dedektör host okumuyor; `Problem` tipinde **host alanı yok** (`problem.go:77-131`). Ayrıca envanter metrik-türevli → yalnız span basan VM envanterde yok |
| **S4** | "Aynı servisin iki env'i arasındaki fark ne?" | `spans.deploy_env` var (`store.go:972`) | **Mimari olarak cevaplanamıyor** — hiçbir okuma MV'sinde env boyutu yok; bugünkü "env ayrımı" yalnızca **servis adı ayrımı** (`env_members.go:38,102`). Aynı `service.name` iki env'de koşuyorsa ayrım **yok**. Ara çözüm yok, MV şeması gerekiyor |
| **S5** | "Bu servis dün de var mıydı?" | Varlık = "son N saatte MV'de satırı var mı" (`summary.go:126,193`) | Yaşam döngüsü kaydı **yok**. `enoughHistory` (`anomaly.go:977`) yeni servisi sessizce atlıyor — sistem "bu yeni" bilgisini **kullanıyor ama hiçbir yerde ifade etmiyor**. "Dün 40 servis vardı bugün 38" sorulamıyor |
| **S6** | "Sessiz-ama-suçlu downstream neden görünmüyor?" | `fusion.go:148-173` komşu adaylarını **`in.openProblems` üzerinde** dönüyor | Bir downstream ancak KENDİ açık problemi varsa `Neighbours`'a giriyor; anomali anchor'ında (`rootcause_worker.go:377-387`) ham şüpheliler geçiyor. **İki anchor tipi farklı hassasiyette** |
| **S7** | "Kanıt zaten hesaplanmış ama hakem görmüyor" | `GetCorrelatedChangesMV` + `GetServiceBlastRadius` API fan-out'unda | Kalıcı hipotez skorlamasına (`Synthesize`) **girmiyorlar** |

### 5.3 Blast radius tavanı

`chstore/blast_radius.go:70` — `service_callers_5m FINAL`, `GROUP BY caller_service`, **`LIMIT 25`** (`:104`). Sınırlar: **transitif değil** (çağıranın çağıranı yok, zincir ucundaki kullanıcı-yüzeyi servisi görünmez) · **25 kesim** ve `TotalCallers = len(Callers)` yani **kesilmiş sayı, gerçek toplam değil** (`:132`) · **etki büyüklüğü yok** ("kaç kullanıcı" hesaplanmıyor) · **yalnız servis** (DB/queue/external blast radius öznesi olamaz).

**Bunlardan hangisi entity katmanını gerektiriyor:** S1 (kısmen — kalıcı pod kaydı), S3 (kısmen), S5 (**bir MV yeterli**, entity tablosu değil). **S2 entity gerektirmiyor** — iki `WHERE node_kind = 'service'` satırının gevşetilmesi yeter. **S4 entity katmanının çözemediği sorudur.**

---

## 6. Karşıt sav

> Bu bölüm bilerek düşmanca yazıldı ve iddiaların bir kısmı yazarı tarafından kendi aleyhine düzeltildi. Öneri (§7) bunun karşısında ayakta kalmak zorunda.

### 6.1 Ayakta kalan argümanlar

**K1 — `service_name` sadece anahtar değil, SHARD anahtarı.** `defaultShardPolicy`'de **18 girdi** `cityHash64(service_name)` (✅ doğrulandı; +2 girdi `cityHash64(parent_service)` = topoloji tabloları; toplam 74 girdi). Ve bu bedava değil: `cluster.go:117-129` `shardSkipSetting()` → `optimize_skip_unused_shards = 1` YALNIZ cluster modunda, *"çünkü Coremetry kendi yarattığı Distributed sarmalayıcının shard anahtarını SAHİPLENİR, dolayısıyla prune KANITLANABİLİR doğrudur"*. İki yol var, ikisi de kötü:
- **(a) `entity_id` shard anahtarı olur** → ingest yazma anında çözümleme yapmak zorunda. `otlp/convert.go:34-35` bugün adı ham okuyor, yoksa `"unknown"` basıyor. Çözümleyici ingest'e girdiği an **"kimlik servisi yavaşladı → span düştü"** diye bugün MEVCUT OLMAYAN bir arıza sınıfı doğar.
- **(b) shard anahtarı `service_name` kalır** → `entity_id` sıcak yolda **dekoratiftir**; prune hâlâ ada göre, MV ORDER BY hâlâ ada göre → entity katmanı **rename/alias hedefini zaten karşılamaz**, sadece bir ek-açıklama katmanı olur ki o `service_metadata`'dır.

**K2 — 10 çakışmanın 9'u entity OLMADAN çözülüyor, en pahalı 4'ünü entity çözmüyor.** §2.2 tablosunun son kolonu. En pahalı dört kalemin (Ç1/Ç3/Ç4/Ç5) kökü MV boyutları. *Bir mimari katmanı, çözmediği sorunlar için almak yanlıştır.*

**K3 — İkinci kaynak-hakikatin sicili.**

| Deneme | Sonuç | Neden |
|---|---|---|
| Terfi attribute kolonları (v0.9.198→621) | ❌ **11 gün sessiz boş** (2026-07-24 → 2026-08-04) | Türetme yazımı veriyle eşleşmiyordu; **probe kolonun VAR olduğunu kanıtlıyordu, DOLU olduğunu değil**; geri düşme yolu "yavaş ama doğru" olduğu için görünür hata yoktu |
| `db_stmt_hash` (v0.8.375) | ✅ Çalıştı | İçerik hash'i, **yaşam döngüsü yok**, sunucu-hesaplı MATERIALIZED, byte-parity sözleşmesi + pin vektörleri (`chstore/dbstmt.go:1-44`), **ham yedek yol** (`dbqueries.go:198-204,241-245`) |
| `ldap_groups.group_uid` | ✅ Çalıştı | Kimliği **biz üretmiyoruz** — dizin veriyor |
| `series_fingerprint` yazar kimliği | ⚠ İki turda oturdu (v0.8.328 → v0.9.724) | İlk sürüm çok-pod'lu servislerde çöküyordu; düzeltme ancak **süreklilik pinleyerek** yapılabildi |
| `service_metadata` deriver | ✅ Yaşıyor | Ama envanter değil ek-açıklama; v0.9.716'ya kadar **SELECT baytının %9,3'ü** |
| `problems` / `anomaly_events` | ❌ **BUGÜN kırık** | PARTITION dedup'u aşıyor (✅ doğrulandı, §Ek) |

**Sicilin okunuşu:** bu repoda yaşayan kimlikler ya **içerik hash'i** ya **dış otorite tarafından verilmiş**. Bizim **mint ettiğimiz** ve bir akışla senkron tutmamız gereken her durum ya sessizce ayrıştı ya ağır makine gerektirdi. Entity katmanı tam olarak üçüncü sınıftır.

**K4 — Operatörün yazılı kısıtlarıyla çelişki.** *"Stability over features"* (2026-07-07 daimi direktif, kuyruk sırası **bugs > perf > HA > features > polish**): entity katmanı 92 dosyada 649 `service_name` referansına (✅ doğrulandı) ve 434 rotanın kimlik semantiğine dokunan bir **mimari** kalem — ne bug, ne perf, ne HA. Ayrıca *"Don't add backwards-compat shims"* (CLAUDE.md) ile agresif varyantın uzun çift-anahtar dönemi çelişir (🟠); *sadeleştirme* yönü (v489-499) yeni birinci-sınıf kavramla çelişir (🟡); çok-kiracı olmadığı için (`feedback-no-multitenant`) entity katmanının klasik gerekçelerinden biri tanım gereği yok (🟡).

**K5 — Boot / keşif gecikmesi.** `ddl_defer.go:1-35`: küme kipinde + `spans` varsa boot DDL'i ertelenir ve *"probe'a bağlı özellikler ertelenen kolon uygulanana kadar KAPALI kalır… bir SONRAKİ boot'ta açılırlar"*. v0.9.625 bunun gerçekleşmiş hâli: *"operatör deploy'u çeker ve hiçbir şey değişmemiş görürdü"*. Bugün **9 probe kapısı** var (`hasClusterCol, hasDBStmtHashCol, hasIsMonotonicCol, hasLdapUsernameCol, hasOpGroupCol, hasProblemCmpCol, hasRCAVerdictBodyCol, hasSeriesFpCol, hasTopoClusterCol`) `[v1304 tabanlı]`. Farkla: diğer 9'u kapalıyken *özellik yok*, entity kapalıyken *tüm sayfaların kimlik çözümlemesi yok*.

**K6 — Bozulma biçimi bugün zarif.** Kimlik bulunamayınca `'unknown'` sentinel'ı (`otlp/convert.go:35`; `quantile_ordinal_test.go:303-305` test ile çiviliyor) → **satır düşmez, sayı kaybolmaz, yanlış varlığa da yazılmaz**. Entity katmanının varsayılan bozulma biçimi "çözümlenemedi" ve o dalın ne yapacağı (düşür / kuyruğa al / geçici entity yarat) her biri ayrı arıza sınıfı.

### 6.2 Karşıt savın kendi zayıf halkaları (yazarı işaretledi)

- **"JOIN pahalı" — ZAYIF.** Prod'daki 241 `spans × spans` join'iydi; her shard'da tam kopyası olan küçük Replicated boyut tablosuna join maliyeti ihmal edilebilir. Dürüst hâli: *164 `serveCached` yolunun (✅ doğrulandı) her birine bir "peki entity çözülemezse?" dalı ekleniyor.*
- **"Veri kaybı riski" — ZAYIF**, eğer tasarım eklemeli olursa. Ama o zaman kazanç da küçülür: rename'i çözmez, MV geçmişini birleştirmez.
- ⚠ **İç tutarsızlık (Ç-G):** karşıt savın tablosu "10 çakışmanın **9'u** entity olmadan çözülüyor, Ç7 kısmen" derken kapanış paragrafı **"0/10 çakışma entity gerektirmiyor"** yazıyor. **9/10 doğru sayıdır**; kapanıştaki "0/10" bir yazım hatasıdır ve savın gücünü değiştirmez.

### 6.3 Karşıt savın en ciddi karşı-kanıtı

`internal/logstore/env_suffix.go:29-41` **zaten bir alias mekanizmasıdır** ve prod-doğrulamalı bir operatör bug'ından doğdu (v0.9.545: BFF servislerinin Logs sekmesi tamamen boştu). İkizi frontend'de (`podWorkload.ts` `ENV_SUFFIXES`) ve dosyanın kendi yorumu şunu **kabul edilmiş sessiz arıza** olarak yazıyor: *"Ayrışırlarsa aynı servis pod yüzeyinde bulunup log yüzeyinde bulunamaz — sessiz ve teşhisi zor bir tutarsızlık."*

Yani: **filoda alias baskısı gerçek**, bugünkü cevap **elle yazılmış, iki dilde kopyalanmış bir string listesi**. Bu, "kimlik katmanına ihtiyaç yok" iddiasını doğrudan zayıflatır. Ama dürüst sonuç: ihtiyaç olan şey **alias çözümlemesi**dir ve alias ≠ entity katmanı.

---

## 7. ÖNERİ

### 7.1 Karar: entity tablosu ŞİMDİ HAYIR — "kimlik sözleşmesi" EVET

Gerekçe üç maddede:

1. **Değer/maliyet oranı ters.** Entity katmanının Faz 0-4'ü ~3 hafta ve 10-13 sürüm; buna karşılık §2.2'nin **en pahalı dört çakışmasını çözmüyor** (Ç1/Ç3/Ç4/Ç5) ve §5.2'nin en keskin boşluğunu (S2) çözmek için **iki SQL satırı** yetiyor (`service_adjacency.go:62,122`).
2. **K1 ikilemi kırılmıyor.** `service_name` `defaultShardPolicy`'de 18 girdinin shard anahtarı ve `optimize_skip_unused_shards=1` buna yaslanıyor. `entity_id` anahtar olursa ingest'e bugün olmayan bir arıza sınıfı girer; olmazsa katman dekoratiftir. Entity katmanı savunucusunun kendi hedefi (rename dayanıklılığı) **hiçbir dalda** karşılanmıyor.
3. **Sicil (K3) yüksek varyans gösteriyor** ve bugün `problems`/`anomaly_events`'te fiilen kırık bir RMT kimlik dedup'u var (✅ doğrulandı) — global yük taşıyan yeni bir RMT kimlik tablosu bu koşullarda kötü bir bahis.

### 7.2 Önerilen ilk dilim — 4 adım, ~4-5 gün, hepsi geri alınabilir

Sıra **operatörün kendi kuyruk kuralına** (bugs > perf > HA > features) göre:

| # | İş | Neyi kapatır | Efor | Dosya |
|---|---|---|---|---|
| **A1** (bug) | `problems` + `anomaly_events` PARTITION P1 ihlali — `PARTITION BY toDate(started_at)` + `ORDER BY id`, partition kolonu ORDER BY'da değil → `FINAL` partition sınırını aşamıyor, bayat satır kazanabiliyor | v0.9.1304'ün `root_cause_hypotheses` için düzelttiğinin aynısı; **doğrulanmış**, `partitionNotInOrderBy` haritasında bilinen istisna olarak duruyor | 0,5-1 gün + repartition emsali `rootcause_repartition.go` | `store.go:1267,1369`; `partition_dedup_test.go:43` |
| **A2** (feature, en ucuz) | `service_seen` MV — `minState(time) AS first_seen_state` + `maxState`, `service_version_5m` emsali (`store.go:3362-3372`) | **S5 tamamı**: "bu servis dün de var mıydı", "yeni servis", "kaybolan servis"; `/services`'te `Son görülme` kolonu. **Yeni yazma yolu, yeni worker, yeni kilit YOK** | 1-1,5 gün | yeni MV + `summary.go` + FE |
| **A3** (bug sınıfı) | Paylaşılan kimlik-ifadesi sabitleri + **sıra-pinli** tablo testleri: (i) dış/db düğüm anahtarı `dbInstanceExpr`'e bağlanır → Ç2+Ç10; (ii) namespace tek ifade → Ç8; (iii) `clusterExpr` gölgeleme adı ayrıştırılır → Ç9 | Ç2 (çıkmaz link + sahte satır), Ç8, Ç9, Ç10 | 1,5-2 gün | `dependencies.go:157`, `chstore/topology.go:703`, `service_map.go:460`, `service_namespaces.go:32`, `repo.go:352` · emsal test `quantile_ordinal_test.go:284-305` |
| **A4** (korelasyon) | `service_adjacency.go:62,122`'deki `node_kind = 'service'` filtresi gevşetilir; okuma tarafı kenarın İKİ ucunu da ham-ID önekinden tipler. ⚠ **DÜZELTME (v0.9.1327):** bu hücre önceden "+ `queue:` düğümünün `node_kind='service'` damgası düzeltilir (`chstore/topology.go:879-881`)" diyordu — **YANLIŞTI, uygulanmadı.** `node_kind` düğümün değil **çocuğun** türüdür; kuyruk→tüketici geçişinde çocuk gerçekten tüketici servistir, yani damga doğru. Kusur, yüklemin **parent'ı hiç kısıtlamaması**. Damgayı değiştirmek `buildServiceGraph`'ın tüketici servisi kuyruk düğümü çizmesine, yani v0.9.1028'in yeniden kırılmasına yol açardı — üstelik yazıcı değişikliği olduğu için 14 günlük karışık-damga penceresi de açardı. `TestQueueConsumerPassStampsChildKind` bu değişmezliği çiviliyor | **S2'nin (a) ve (c) katmanı**: paylaşılan bağımlılık **adlandırılabilir** hale gelir; ayrıca korelatörün grafiğine kuyruk düğümlerinin "servis" olarak sızması durur | 1-1,5 gün | aynı |

**A1-A4 toplam ≈ 4,5-6 gün**, dördü de ayrı `v0.9.X`, hiçbiri MV yeniden kurmuyor, hiçbiri URL kırmıyor, hiçbiri ikinci kaynak-hakikat yaratmıyor.

### 7.3 Ardından operatör kararı bekleyen iki büyük kalem

- **B1 — MV ailesine `deploy_env` boyutu** (entity-cost raporunun Faz 5'i): Ç1 hariç **Ç3+Ç4+Ç5+Ç6**'yı ve S4'ü tek kalemde çözer; 6-8 MV DROP+RECREATE veya inner `ALTER`+`MODIFY QUERY`, rolling deploy'da okuma-hatası penceresi, geçmiş veri `deploy_env=''` kalır. **+2-3 hafta, 🔴.** — *Bu, entity katmanından daha yüksek değerli ve daha net tanımlı bir mimari kalemdir; sıraya entity'den ÖNCE girmelidir.*
- **B2 — `service_metadata` + `aliases Array(String)`** (§6.3'ün karşı-kanıtına cevap): rename'in sahiplik/ekip/runbook yarısını kurtarır, `env_suffix.go`'nun elle listesini veriye taşır. **~1 gün, 🟢.** Dürüst sınır: MV geçmişini birleştirmez — ama entity tablosu da birleştirmez.

### 7.4 Karar kriterleri — hangi koşulda fikir DEĞİŞİR

Entity tablosu, aşağıdaki **beş koşulun hepsi** sağlandığında doğru karar olur:

1. **ÖNCE B1 (env boyutu) biter.** En pahalı 4 çakışmanın kökü orada; entity onların yerine geçemez, ancak sonrasına gelir.
2. **Eklemeli olur:** doğal anahtar her yerde birincil kalır; `entity_id` **hiçbir** ORDER BY'a, **hiçbir** shard anahtarına, **hiçbir** önbellek anahtarına girmez (aksi hâlde K1 ve v0.5.187 çapraz-zehirlenme sınıfı devreye girer).
3. **Tek kavramla başlar ve `db_stmt_hash` şeklindedir:** içerik-hash veya saf fonksiyon kimliği, parity/pin testli, probe'lu **ham yedek yol**. İlk aday **DB instance** (S2 en keskin boşluk, `db_caller_summary_5m` verisi hazır).
4. **"Çözümlenemedi" dalı bugünkü davranışın birebir aynısıdır** ve bir testle çivilenir (`'unknown'` sentinel disiplini, `quantile_ordinal_test.go:303-305` emsali). Entity çözümlemesi **hiçbir zaman satır düşürmez, hiçbir zaman yanlış varlığa yazmaz**.
5. **Yaşam döngüsü MV'den gelir, tabloda tutulmaz** (`minState` emsali) — `last_seen` için tik başına satır yeniden yazımı yok, §4.3'ün amplifikasyonu doğmaz.

**Ek tetikleyiciler (herhangi biri gerçekleşirse yeniden değerlendir):**
- Operatör bir servisi yeniden adlandırır ve triyaj geçmişi (exception grupları, açık problemler) kaybolur → alias/kimlik baskısı ölçülebilir zarara döner.
- Filo çok-env tek `service.name` desenine geçer (bugün env eki ile ayrılıyor: `-prod/-int/-uat/-prep`) → S4 acil olur ve B1 tek başına yetmez.
- DB/queue/external bir arızanın **öznesi** olması gerekir (bugün olamıyor, `Problem` tipinde alan yok) → `dependency` entity gerekçelenir.
- `instance` kardinalitesi ölçülür ve <500k/7gün çıkarsa Faz 4'ün riski düşer.

**Dürüst kapanış:** Beş koşulun sağlandığı şeyin adı, dürüst olmak gerekirse, "entity katmanı" değildir — doğal anahtarları koruyan, okuma tarafında kanonikleştiren, yaşam döngüsünü MV'den okuyan bir **kimlik sözleşmesidir**. §7.2'nin A1-A4 dilimi tam olarak o sözleşmenin ilk taksitidir.

---

## Ek: doğrulamalar, açık sorular, ölçülmemiş varsayımlar

### E1. Bu doküman yazılırken yapılan bağımsız doğrulamalar (HEAD `6291bb53`)

| Doğrulanan | Sonuç |
|---|---|
| `service_metadata` / `exception_groups` / `maintenance_windows` / `anomaly_events` / `problems` / `events` / `topology_edges_5m` / `service_callers_5m` DDL satırları | **1066 / 1121 / 1192 / 1267 / 1369 / 2085 / 1891 / 1969 — hepsi v0.9.1101'deki gibi, kaymamış** ✅ |
| `saved_views` DDL satırı | **1248** ✅ (⚠ Ç-D: entity-cost raporu "1244" yazmış — **yanlış**) |
| `service_summary_5m` MV + ORDER BY | `store.go:3018` / `:3021 ORDER BY (service_name, time_bucket)` ✅ — env boyutu **yok**, doğrulandı |
| `runServiceTeamDeriver` canlı mı | **CANLI** — `main.go:647` (`go runServiceTeamDeriver`), tanım `:1547`, çağrı `:1559` ✅ (⚠ Ç-C) |
| `entities` tablosu var mı | **YOK** — `grep "CREATE TABLE IF NOT EXISTS entities"` → 0 ✅ |
| `service_adjacency.go` filtresi | `:62` ve `:122` → `WHERE time_bucket >= ? AND node_kind = 'service'` ✅ |
| `defaultShardPolicy` sayımı | **74** toplam girdi; **18**'i `cityHash64(service_name)`; +2 `cityHash64(parent_service)`. `highVolumeTables` **30** girdi ✅ (⚠ Ç-H: karşıt sav "15 tablo" demiş — az sayım) |
| `GROUP BY … service_name` sayımı | **77** (testsiz) ✅ |
| `service_name` referansı | **649 satır / 92 dosya** (testsiz) ✅ |
| `serveCached(` çağrısı | **164** ✅ |
| `problems` DDL | `ENGINE = ReplacingMergeTree(version)` + **`PARTITION BY toDate(started_at)`** + `ORDER BY id` → **partition kolonu ORDER BY'da DEĞİL** ✅ |
| `anomaly_events` DDL | aynı şekil ✅ |
| `root_cause_hypotheses` | v0.9.1304'te PARTITION **kaldırıldı**; `partition_dedup_test.go` pinliyor ✅ |

### E2. İşaretlenen çelişkiler (sessizce çözülmedi)

| # | Çelişki | Durum |
|---|---|---|
| **Ç-A** | Ajan raporları iki farklı HEAD tabanlı: 1-4 → `c294c1b4` (v0.9.1101), 6-7 → `537dc167` (v0.9.1304), doküman → `6291bb53` (v0.9.1305). Arada 204 sürüm | Kritik satırlar yeniden doğrulandı (E1); doğrulanmayanlar `[v1101 tabanlı]` etiketli. **Faz 1'e başlamadan önce §3 ve §5'in satır numaraları HEAD'de yeniden doğrulanmalı** |
| **Ç-B** | Pod türetme zincirleri: kimlik + korelasyon raporları **"üç uyumsuz zincir"** diyor (`deploys.go:432-446` pod→instance→host, `evaluator/runtime_pods.go:22` pod→host→instance[:8], `metricrate.go:177` instance→pod→host); karşıt sav **"2 aile + 1 bilinçli süreklilik pini"** diyor ve her birinin gerekçesinin dosyada yazılı olduğunu gösteriyor (`runtime_pods.go:12-20` metrik tarafı, `metricrate.go:174-180` v0.9.724 fingerprint simetrisi) | **ÇÖZÜLMEDİ.** İkisi de iddiasını dosya:satır ile destekliyor. Sonuç: Faz 4'ün "tek sabite indir" hedefi **yanlış olabilir** — `metricrate.go` zincirini değiştirmek v0.9.724 sürekliliğini kırar. Doğru çözüm sabit değil, **yazılı politika + pin testi**. **Karar operatörde; kod okunmadan Faz 4 alınmamalı** |
| **Ç-C** | `MEMORY.md` "Metadata deriver KAPALI" vs `main.go:647` canlı goroutine | **Çözüldü:** MEMORY *kuyruk kaleminin* kapandığını söylüyor, mekanizmanın değil. Deriver **çalışıyor** — yani "türetilmiş kimlik katmanı denendi ve kapatıldı" argümanı geçersiz, tersine sınırlı bir türetme katmanı aylardır yaşıyor. Bu **entity lehine** bir kanıttır |
| **Ç-D** | `saved_views` satırı 1244 (entity-cost) vs 1248 (kimlik + karşıt sav) | **Çözüldü: 1248** (E1) |
| **Ç-E** | `topology_edges_5m` ailesi "MV" mi düz tablo mu | **Çözüldü:** düz `ReplacingMergeTree`, Go `INSERT … SELECT` ile doldurulur (`store.go:1891` `CREATE TABLE`, ✅ doğrulandı). Kod yorumları ve diğer raporlar "MV" derken yanıltıyor |
| **Ç-F** | Entity tablosunun dağıtık yerleşimi: entity-cost → `highVolumeTables` + `cityHash64(entity_key)` shard'lı; karşıt sav → shard'sız `ON CLUSTER` admin tablosu | **ÇÖZÜLMEDİ — tasarım çatalı.** Faz 0'ın ilk kararı budur ve `/clickhouse-schema` ile birlikte verilmelidir |
| **Ç-G** | Karşıt savın iç tutarsızlığı: tablo "9/10", kapanış "0/10" | **Çözüldü: 9/10** (Ç7 kısmen) |
| **Ç-H** | Shard anahtarı sayımı 15 (karşıt sav) vs **18** (doğrulandı) | **Çözüldü: 18** — argüman **güçleniyor**, zayıflamıyor |

### E3. Açık sorular — kod okunarak cevaplanmadı

1. **Shard'lanmamış state tabloları gerçekten bölünüyor mu?** Entity-cost raporunun R9'u: `adaptDDL` (`cluster.go:1101`) ZK yolunda `{shard}` makrosu var → `highVolumeTables`'ta olmayan tablo shard başına **bağımsız kopya**; sürücü çok-host fail-over ile bağlanıyor (`store.go:394-415`). Eğer doğruysa bugün `service_metadata`, `problems`, `exception_groups`, `saved_views` **shard'a göre farklı içerik gösteriyor**. Raporun yazarı bunu ❓ ile işaretledi ve *"bug olarak açmadan önce canlı kümede iki farklı shard'a bağlanarak `SELECT count()` ile doğrulanmalı"* dedi. **Bu doğrulama yapılmadı ve yapılmalıdır — doğruysa entity'den bağımsız bir 🔴 kalemdir.**
2. **Entity pass'inin gerçek maliyeti.** §4.3'teki "kova başına 30-45 MB, tik maliyetine %5 altı" bir **tahmindir**; `system.query_log` okunmadı.
3. **`instance` kardinalitesi.** 5 pod/servis × 3 restart/gün varsayımı ölçülmedi; gerçek pod churn'ü bilinmiyor.
4. **`WriteServiceCallersBucket`'ın `topoJoinMemSettings()` dışında kalması** (`backtrace.go:68-70`) bugün 241 üretiyor mu — `[v1101 tabanlı]`, HEAD'de doğrulanmadı, prod'da ölçülmedi.
5. **`/api/problems/buckets` orphan dead code** (`project-bug-hunt-2026-07-06` hafızası) ve §3.4'teki 3 ölü fonksiyon (`GetServiceTopologyEdges`, `GetTopologyEdges`, `ServiceCallers`) — silinmeleri bu denetimin kapsamı dışında, ayrı kalem.
6. **`ext:` eleme asimetrisi:** `/external` ve `/api/topology/service` enstrümante servise işaret eden `ext:` kenarlarını eliyor, `/api/servicegraph` ve `GetServiceGraphTopN` **elemiyor** (`repo.go:1734` tek koşul `parent_service != child_node`) — bilinçli mi, bug mu, sorulmadı.

### E4. Ölçülmemiş varsayımlar (bu dokümandaki her tahmin)

| Varsayım | Nerede | Durum |
|---|---|---|
| 1B span/gün → 3,47M span/5dk kova | §4.3 | Aritmetik; gerçek hacim ölçülmedi |
| Entity pass'i aggregator maliyetine %5 altı ekler | §4.3 | **Tahmin, ölçülmedi** |
| ≤2,3M satır INSERT/gün = `spans`'in %0,23'ü | §4.3 | 1000 servis / 2000 bağımlılık / 5000 instance varsayımıyla; **ölçülmedi** |
| A1-A4 eforu 4,5-6 gün | §7.2 | Tahmin |
| Faz 0-4 eforu 13,5 gün | §4.6 | Tahmin; prod dağıtık doğrulama turu **dahil değil** |
| B1 (env boyutu) eforu +2-3 hafta | §7.3 | Tahmin; 6-8 MV'nin hangileri olduğu tek tek çıkarılmadı |
| Ç1'in 8×/5× büyüklüğü | §2.2 | **Ölçülmüş ama demo veride** (`Overview.tsx:248-251`); prod'da ölçülmedi |
