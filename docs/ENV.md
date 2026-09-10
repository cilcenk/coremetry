# Ortam değişkenleri — `COREMETRY_*` referansı

Coremetry'nin Go kodunun okuduğu **her** `COREMETRY_*` değişkeni. Kaynak:
`git grep -nP '\bCOREMETRY_[A-Z0-9_]+' -- '*.go' ':!*_test.go'` (2026-09-10,
89 benzersiz ad). Her satır koddan okunarak yazıldı; doğrulanamayan yer
"(koddan doğrulanamadı)" ile işaretli. Satır numaraları o günkü `main`'e
aittir — kayarsa dosya + fonksiyon adı yeter.

## Öncelik zinciri (env > config.yaml > yerleşik varsayılan)

`internal/config/config.go` `Load()` (satır 544+):

1. `cfg := defaults` — yerleşik değerler (`config.go:449-478`).
2. `config.yaml` varsa **üzerine** `yaml.Unmarshal` (dosya yoksa sessizce
   atlanır; `--config` bayrağı, varsayılan `config.yaml`).
3. Env override'ları (`config.go:555-818`) — ayarlı olan her env yaml'ı ezer.
4. Sıfır-değer geri doldurma (`token_ttl`, `ingestion.*`, `retention.*`,
   `background.*`, CH pool türetimi — `config.go:820-868`).

Dikkat edilecek üç davranış:

- **Bool env'ler yalnız AÇAR.** `COREMETRY_CH_SECURE`, `_OIDC_ENABLED`,
  `_DEMO_MODE`, `_ADMIN_RESET`, `_ES_INSECURE`, `_TRUSTED_HEADER_*` vb.
  yalnız `true`/`1` görünce `true` yazar; yaml'da `true` olan bir değeri env
  ile `false` yapamazsınız. İstisnalar: `COREMETRY_LOG_ANOMALY_ENABLED`
  (`false`/`0` kapatır), `COREMETRY_CH_PARALLEL_VIEWS` (`0/false/off/no`
  kapatır), `COREMETRY_OTLP_HTTP_ADDR` (`off`/`none`/`-` kapatır).
- **Sayısal env'ler geçersiz değerde sessizce yoksayılır** (`Atoi`/
  `ParseDuration` hatası → varsayılan kalır). İstisna: `COREMETRY_CH_MAX_*`,
  `_CH_MEM_FRACTION`, `_CH_PARALLEL_VIEWS`, `_SELF_OBS_SAMPLE_RATE` WARNING
  loglar.
- **Bazı alanlar boot'tan sonra `system_settings`'ten ezilir** (UI kazanır):
  AI/Copilot (`main.go:933-937`), Elasticsearch (`main.go:849-852`,
  Settings → Elasticsearch), retention (`retention.*` anahtarı). Env bu
  yüzeyler için yalnız **ilk boot varsayılanı**dır.

`COREMETRY_MODE` rolleri: `all` (varsayılan, tek pod) · `ingest` (OTLP alıcı
+ CH yazıcı) · `api` (UI + REST + SSE + MCP) · `worker` (evaluator, anomali,
topoloji, retention, bildirim; **tek replika**) · `agent` (runbook otomatik
adımları). "Rol" sütunu değişkeni **kullanan** rolü gösterir; `config.Load`
her rolde koşar, ama örneğin ingest knob'ları api pod'unda etkisizdir.

Chart eşlemesi: `charts/coremetry/templates/deployment.yaml:89-190` ve
`deployment-distributed.yaml:104-195` (Secret'tan: `jwt-secret`,
`clickhouse-password`, `initial-admin-password`, `oidc-client-secret`,
`es-password`, `es-api-key`). Compose eşlemesi: `docker-compose.yml`
`coremetry.environment`. Yerel `.env` şablonu: `.env.example`.

Sütunlar: **Okunduğu yer** · **Yokken / varsayılan** · **Etki** · **Rol** ·
**Gizli**.

---

## 1. Çekirdek / HTTP / rol

| Değişken | Okunduğu yer | Yokken / varsayılan | Etki | Rol | Gizli |
|---|---|---|---|---|---|
| `COREMETRY_MODE` | `main.go:117` `parseRunMode` | boş → `all` | `all\|ingest\|api\|worker\|agent`; başka değer → `log.Fatalf` (`main.go:130`). Chart `deployment.mode=distributed` her rolü ayrı Deployment yapar. | hepsi | hayır |
| `COREMETRY_HTTP_ADDR` | `config.go:632` | `:8088` | api rolünde Web UI + REST + SSE + OTLP/HTTP yedeği; api olmayan rollerde yalnız `/livez` + 503 sağlık dinleyicisi (`main.go:325` boot listener, `main.go:1414` "Health-only HTTP"). Port doluysa boot anında `Fatalf`. | hepsi | hayır |
| `COREMETRY_GRPC_ADDR` | `config.go:641` | `:4317` | OTLP/gRPC alıcı (`main.go:526`, yalnız ingest). `all` modda self-obs varsayılan hedefini de türetir (`main.go:301` `selfObsDefaultEndpoint`). | ingest | hayır |
| `COREMETRY_OTLP_HTTP_ADDR` | `config.go:647` → `resolveOTLPHTTPAddr` (`config.go:530`) | `:4318`; `off`/`none`/`-` → dinleyici kapalı; boşluk kırpılır | Ayrı, auth'suz OTLP/HTTP dinleyici (`POST /v1/{traces,logs,metrics}`, `main.go:542`). Kapalıyken OTLP/HTTP `:8088` üzerinden yine cevap verir. | ingest | hayır |
| `COREMETRY_PUBLIC_URL` | `config.go:638` | `""` → bildirimlerde derin link yok | `notifier.SetPublicURL` (`main.go:598`): Slack/Teams/e-posta/webhook gövdesine "Open in Coremetry" linki. Ör. `https://apm.example.com`. | worker (bildirim üretir), all | hayır |
| `COREMETRY_VERSION` | `main.go:189` (`init`); `selfobs.go:83,207` (yedek) | boş → ldflag `-X main.Version` > `/app/VERSION` > `"dev"` (`main.go:167-183`) | **Yalnız GÖSTERİLEN sürümü** değiştirir; `BuildVersion` (imaj kimliği) dokunulmaz. `/api/version` `version` + `build` + `overridden` (`api.go:11287`) döner — bayat env imajmış gibi davranamaz (olay v0.5.394, `docs/INCIDENTS.md`). | hepsi | hayır |

## 2. ClickHouse

| Değişken | Okunduğu yer | Yokken / varsayılan | Etki | Rol | Gizli |
|---|---|---|---|---|---|
| `COREMETRY_CH_ADDR` | `config.go:555` | `127.0.0.1:9000` (compose: `clickhouse:9000`) | Virgülle çoklu seed (`ch1:9440,ch2:9440`); sürücü yük dengeler + failover. | hepsi | hayır |
| `COREMETRY_CH_DATABASE` | `config.go:558` | `coremetry` | ON CLUSTER / DROP DATABASE hedefi. | hepsi | hayır |
| `COREMETRY_CH_USERNAME` | `config.go:561` | `default` | | hepsi | hayır |
| `COREMETRY_CH_PASSWORD` | `config.go:564` | `""` | Chart: Secret `clickhouse-password`; compose: `CLICKHOUSE_PASSWORD`. | hepsi | **evet** |
| `COREMETRY_CH_SECURE` | `config.go:567` | `false` | Native TLS (9440) (`store.go:452`). | hepsi | hayır |
| `COREMETRY_CH_INSECURE_SKIP_VERIFY` | `config.go:570` | `false` | Self-signed CA için sertifika doğrulamasını atlar (`store.go:453`). Prod'da CA bundle gelince kapat. | hepsi | hayır |
| `COREMETRY_CH_MAX_OPEN_CONNS` | `config.go:595` | `0` → türetilir: `5 sinyal × workers + 8` (`config.go:898-905`; 8 worker → 48) | Açık değer aynen uygulanır; fan-out'un altındaysa `[config] WARNING` (`config.go:861`, olay v0.8.205 "acquire conn timeout"). | hepsi (ingest'te kritik) | hayır |
| `COREMETRY_CH_DIAL_TIMEOUT` | `config.go:600` | `5s`; `ParseDuration` hatası → yoksayılır | | hepsi | hayır |
| `COREMETRY_CH_CLUSTER_NAME` | `config.go:580` | `""` → tek düğüm MergeTree | Dağıtık mod: DDL `ON CLUSTER`, `*_local` Replicated + Distributed sarmalayıcı (`config.go:305-320`). `system.clusters`'ta yoksa boot hatası, ad büyük/küçük harf duyarlı (`cluster.go:55-75`). Dış Distributed `spans` + boş ad → **boot HATASI** (`store.go:818-823`). `store.go:912` yorumundaki `COREMETRY_CH_CLUSTER` adı yanlış — kod yalnız `_CLUSTER_NAME` okur. | hepsi | hayır |
| `COREMETRY_CH_REPLICA_PATH` | `config.go:583` | `""` → `/clickhouse/tables` (`cluster.go:240`) | Keeper/ZK yol öneki; `{shard}/{replica}` eklenir. Paylaşımlı varsayılanda reset'in yetim-znode süpürmesi **atlanır** (`reset.go:153-158`) → ayrı önek ver (`/clickhouse/tables/coremetry`). | hepsi | hayır |
| `COREMETRY_CH_SHARD_KEY` | `config.go:586` | `""` → tablo başına politika (`cluster.go:474` `defaultShardPolicy`: `service_name` / `trace_id`) | Verilirse **tüm** Distributed tablolara uygulanır; `trace_id` kolonu olmayanlar `rand()` alır (`cluster.go:265`). | hepsi | hayır |
| `COREMETRY_CH_ALLOW_UNSET_CLUSTER` | `config.go:605` | `false` | Dış Distributed `spans` + boş küme adı durumunda boot hatası yerine **bozuk modda** (MV'ler boş, ham span okuması) devam (`store.go:818-823`, v0.8.213). | hepsi | hayır |
| `COREMETRY_CH_MAX_MEMORY_USAGE` | `config.go:614` `chBytesEnv` (`config.go:387`) | `0` → yerleşik 4 GB | Sorgu başına `max_memory_usage`. **Düz bayt tamsayı**; `12Gi`/`12G` REDDEDİLİR + WARNING. Boot'ta sunucu tavanı × `MEM_FRACTION` ile kelepçelenir, etkin değer loglanır (`store.go:615-645`). | hepsi | hayır |
| `COREMETRY_CH_MAX_BYTES_EXTERNAL_GROUP_BY` | `config.go:615` | `0` → 1 GB | Diske taşma eşiği (GROUP BY). | hepsi | hayır |
| `COREMETRY_CH_MAX_BYTES_EXTERNAL_SORT` | `config.go:616` | `0` → 1 GB | Diske taşma eşiği (sort). | hepsi | hayır |
| `COREMETRY_CH_MEM_FRACTION` | `config.go:619` `chFractionEnv` (`config.go:404`) | `0` → `0.6`; `[0.1, 0.9]`'a kelepçe (`query_memory.go`) | `max_server_memory_usage`'ın sorgu başına payı. `60`/`60%` REDDEDİLİR + WARNING; `0.6` yaz. | hepsi | hayır |
| `COREMETRY_CH_PARALLEL_VIEWS` | `config.go:622` (`LookupEnv`) | ayarsız → paralel **açık** (`1`) | `0/false/off/no` → spans INSERT'inde MV'ler sıralı itilir (`parallel_view_processing=0`, `store.go:620-621`). Başka değer → WARNING + açık kalır. Prod A/B ölçüm anahtarı (v0.10.511). | ingest | hayır |

## 3. Redis / cache

| Değişken | Okunduğu yer | Yokken / varsayılan | Etki | Rol | Gizli |
|---|---|---|---|---|---|
| `COREMETRY_REDIS_URL` | `config.go:741` | `""` → Noop cache + **always-leader** kilit (`main.go:552-560`) | L2 cache, lider kilidi, SSE çapraz-pod köprüsü, otomatik-tamamlama deposu (`main.go:490`). Ayarlı ama ulaşılamaz → always-leader + `[leader] WARNING` (`main.go:584`); çok replikada alert/bildirim/retention **çift** koşar. Chart bundled Redis'ten türetir; compose `redis://redis:6379/0`. | hepsi (worker için kritik) | kısmen (URL parola taşıyabilir) |
| `COREMETRY_ACACHE_LOWCARD_KEYS` | `main.go:143` `buildAcachePolicy` | boş → `acache.DefaultPolicy()` | Otomatik tamamlama: sıralı top-N değeri tutulan attribute anahtarları (CSV, ör. `http.route`). | ingest (`acStore.Start`, `main.go:497`) | hayır |
| `COREMETRY_ACACHE_HIGHCARD_KEYS` | `main.go:144` | boş → varsayılan politika | Yalnız HLL sayım tutulan anahtarlar (CSV, ör. `k8s.pod.name`). | ingest | hayır |

## 4. Ingest

| Değişken | Okunduğu yer | Yokken / varsayılan | Etki | Rol | Gizli |
|---|---|---|---|---|---|
| `COREMETRY_INGEST_BATCH_SIZE` | `config.go:664` | `10000` | CH INSERT batch boyutu. | ingest | hayır |
| `COREMETRY_INGEST_BUFFER_SIZE` | `config.go:654` | `500000` | Sinyal başına kanal kapasitesi; dolunca gRPC `ResourceExhausted` ("buffer full"). Sürekli doluyorsa darboğaz CH'dir. | ingest | hayır |
| `COREMETRY_INGEST_WORKERS` | `config.go:659` | `8` | Sinyal başına paralel flusher; CH pool türetimine girer (`5 × workers + 8`). | ingest | hayır |
| `COREMETRY_INGEST_FLUSH_INTERVAL` | `config.go:669` | `5s` (yerleşik, v0.10.240); repo `config.yaml` `2s` yazar | Batch dolmasa da flush aralığı. | ingest | hayır |
| `COREMETRY_INGEST_BYTE_BUDGET_MB` | `config.go:677` | `512` (5 tüketici × 512 ≈ 2.5 GB tavan); `0` = bayt kapağı kapalı | Tüketici başına bellek tavanı; aşan batch sayılarak düşürülür (OOMKill yerine). | ingest | hayır |
| `COREMETRY_EXEMPLARS_MAX_PER_SERIES_MIN` | `config.go:573` | `0` = sınırsız | Seri başına dakikada exemplar tavanı (`main.go:480` `SetExemplarCap`). | ingest | hayır |

## 5. Logs / Elasticsearch

ES yalnız **okuma** arka ucudur; yazma her zaman ClickHouse. Env değerleri
Settings → Elasticsearch'ün tohumu; UI'dan kaydedilen blob env'i ezer
(`main.go:849-852`).

| Değişken | Okunduğu yer | Yokken / varsayılan | Etki | Rol | Gizli |
|---|---|---|---|---|---|
| `COREMETRY_LOGS_BACKEND` | `config.go:744` | `""`/`clickhouse` | `elasticsearch` → `/api/logs` ES'ten okur. Başka değer → boot hatası (`main.go:1494`). ES boot'ta ulaşılamazsa CH logstore ile **bozuk** boot + tekrar deneme (`main.go:823-840`). | api, worker | hayır |
| `COREMETRY_ES_ADDRESSES` | `config.go:747` | `[]` | CSV: `http://es-0:9200,http://es-1:9200`. | api, worker | hayır |
| `COREMETRY_ES_USERNAME` | `config.go:751` | `""` | Basic auth (parola ile birlikte). API key varsa **yoksayılır** + log (`elasticsearch.go:447`). | api, worker | hayır |
| `COREMETRY_ES_PASSWORD` | `config.go:754` | `""` | | api, worker | **evet** |
| `COREMETRY_ES_API_KEY` | `config.go:757` | `""` | base64 `id:api_key` (`POST /_security/api_key` → `encoded`). Öncelikli. 401/403 boot'u durdurmaz, bozuk moda düşürür. | api, worker | **evet** |
| `COREMETRY_ES_INDEX` | `config.go:760` | `""` → `app-*` (`elasticsearch.go:261-264`) | Sorgulanan index deseni. | api, worker | hayır |
| `COREMETRY_ES_INDEX_TEMPLATE` | `config.go:763` | `""` = kapalı | Servis kapsamlı sorguyu tek index'e daraltır; `{service}`, `{namespace}` yer tutucuları (`config.go:124-131`), ör. `app-{service}.{namespace}`. | api, worker | hayır |
| `COREMETRY_ES_INSECURE` | `config.go:766` | `false` | TLS zincir doğrulamasını atlar. | api, worker | hayır |
| `COREMETRY_ES_ML_ENABLED` | `config.go:769` | `false` | Elastic ML anomaly job poller — salt okunur `/_ml/anomaly_detectors` (`main.go:671`). | worker | hayır |
| `COREMETRY_ES_ML_MIN_SCORE` | `config.go:772` | `0` → `75` (`elasticml/poller.go:28`) | Kayıt eşiği (Elastic "critical" bandı). | worker | hayır |
| `COREMETRY_ES_FIELD_TIMESTAMP` | `config.go:781` | `@timestamp` | Belge alan yolu. | api, worker | hayır |
| `COREMETRY_ES_FIELD_TRACE_ID` | `config.go:784` | `trace.id` (ek yedekler: `trace_id`, `traceId`, `TraceId`) | | api, worker | hayır |
| `COREMETRY_ES_FIELD_SPAN_ID` | `config.go:787` | `span.id` | | api, worker | hayır |
| `COREMETRY_ES_FIELD_SERVICE` | `config.go:790` | `service.name` | | api, worker | hayır |
| `COREMETRY_ES_FIELD_MESSAGE` | `config.go:793` | `message` | | api, worker | hayır |
| `COREMETRY_ES_FIELD_SEVERITY_TEXT` | `config.go:796` | `log.level` | | api, worker | hayır |
| `COREMETRY_ES_FIELD_SEVERITY_NUMBER` | `config.go:799` | `""` → alan sorgulanmaz | | api, worker | hayır |
| `COREMETRY_ES_FIELD_ENV` | `config.go:802` | `""` → `field_caps` ile kendiliğinden keşif (`es_env_field.go`) | `?env=` süzgecinin hedef alanı. | api, worker | hayır |
| `COREMETRY_LOGS_PATTERNS_SHARD_SIZE` | `elasticsearch.go:171` (her istekte okunur, restart gerekmez) | boş → çağrı noktasındaki varsayılan (`shardSizeFromEnv(def)`) | `significant_text` `shard_size`; milyar-belge kümede düşür (ör. `5000`). | api | hayır |
| `COREMETRY_LOGS_PATTERNS_ES_TIMEOUT` | `elasticsearch.go:185` | boş → çağrı noktasındaki varsayılan | ES yumuşak zaman aşımı (`"20s"` gibi); handler deadline'ından (15 s) küçük olmalı. | api | hayır |
| `COREMETRY_LOGS_TIMESERIES_ES_TIMEOUT` | `elasticsearch.go:199` | boş → çağrı noktasındaki varsayılan | Logs histogramının ES yumuşak zaman aşımı (patterns knob'undan ayrı, v0.8.3). | api | hayır |

## 6. Auth (JWT / bootstrap / OIDC / trusted header)

LDAP'ın env'i yoktur — Settings UI'dan canlı yapılandırılır.

| Değişken | Okunduğu yer | Yokken / varsayılan | Etki | Rol | Gizli |
|---|---|---|---|---|---|
| `COREMETRY_JWT_SECRET` | `config.go:682` | `""` → **her boot'ta rastgele 32 bayt** (`auth.go:118-122`) | HS256 imza anahtarı. Yokken restart'ta tüm oturumlar düşer; çok pod'da her pod farklı anahtar → auth flap (`values-minikube.yaml` notu). Zayıf/yer tutucu değer boot'u durdurmaz ama `[auth] ⚠ GÜVENLİK` + `/admin/stats` (`auth.go:123+`). Üret: `openssl rand -hex 32`. | api | **evet** |
| `COREMETRY_INITIAL_ADMIN` | `config.go:685` | `admin@coremetry.local` | `users` boşken tohumlanan admin e-postası (`main.go:711`, `seedInitialAdmin` `main.go:1830`). | hepsi (tohum her rolde koşar) | hayır |
| `COREMETRY_INITIAL_PASSWORD` | `config.go:688` | `admin` | İlk boot'ta bcrypt'lenir; sonraki boot'larda dokunulmaz (ADMIN_RESET / DEMO_MODE hariç). | hepsi | **evet** |
| `COREMETRY_ADMIN_RESET` | `config.go:734` | `false` | **DİKKAT:** `true` → admin parolası **her boot'ta** env'den yeniden yazılır (`main.go:1835-1873`), UI'dan değiştirilen parolayı ezer. Kilitli admin kurtarmak için bir boot aç, sonra kaldır (ya da GitOps'ta bilinçli bırak). | hepsi | hayır (tehlikeli) |
| `COREMETRY_DEMO_MODE` | `config.go:731` | `false` | `/api/auth/config` admin kimlik bilgisini **açık** döner + parola her boot reconcile (`main.go:1384-1389`, `1835`). **Asla prod.** | api | hayır (tehlikeli) |
| `COREMETRY_OIDC_ENABLED` | `config.go:691` | `false` | OIDC SSO (`main.go:800` `auth.NewOIDCService`); yerel parola girişi hep açık kalır. | api | hayır |
| `COREMETRY_OIDC_ISSUER_URL` | `config.go:694` | `""` | ör. `https://accounts.google.com`. | api | hayır |
| `COREMETRY_OIDC_CLIENT_ID` | `config.go:697` | `""` | | api | hayır |
| `COREMETRY_OIDC_CLIENT_SECRET` | `config.go:700` | `""` | Chart: Secret `oidc-client-secret`. | api | **evet** |
| `COREMETRY_OIDC_REDIRECT_URL` | `config.go:703` | `""` | `/api/auth/oidc/callback`'in dış URL'i. `scopes` / `display_name` (`SSO`) / `default_role` (`viewer`) / `allowed_domains` yalnız yaml (`config.go:820-830`). | api | hayır |
| `COREMETRY_TRUSTED_HEADER_ENABLED` | `config.go:710` | `false` | oauth2-proxy / IAP deseni (`main.go:730` `EnableTrustedHeader`). `TRUSTED_PROXIES` boşsa **boot reddedilir** (`auth.go:158-160`). | api | hayır |
| `COREMETRY_TRUSTED_HEADER_EMAIL` | `config.go:713` | `X-Auth-Request-Email` (`auth.go:171`) | Kimlik başlığı. | api | hayır |
| `COREMETRY_TRUSTED_HEADER_USER` | `config.go:716` | `X-Auth-Request-User` | | api | hayır |
| `COREMETRY_TRUSTED_HEADER_GROUPS` | `config.go:719` | `X-Auth-Request-Groups` | | api | hayır |
| `COREMETRY_TRUSTED_HEADER_AUTO_PROVISION` | `config.go:722` | `false` → eşleşmeyen e-posta 403 | `true` → ilk görüşte `DEFAULT_ROLE` ile kullanıcı açılır. | api | hayır |
| `COREMETRY_TRUSTED_HEADER_DEFAULT_ROLE` | `config.go:725` | `viewer` (`auth.go:179`); geçersiz rol → boot hatası | | api | hayır |
| `COREMETRY_TRUSTED_PROXIES` | `config.go:728` | `[]` | CSV CIDR (`203.0.113.0/24,10.0.0.0/8`); başlıklar yalnız bu kaynaklardan kabul edilir. Enabled iken zorunlu. | api | hayır |

## 7. AI / Copilot

Env = ilk boot varsayılanı; Settings → AI Copilot'ta kaydedilen değer
`system_settings`'ten ezer (`main.go:933-937`, `config.go:93-95`).

| Değişken | Okunduğu yer | Yokken / varsayılan | Etki | Rol | Gizli |
|---|---|---|---|---|---|
| `COREMETRY_AI_PROVIDER` | `config.go:805` | `""` → `anthropic` (`copilot.go:315`) | `anthropic` \| `github` \| `openai` (OpenAI-uyumlu; Ollama/vLLM/LM Studio). | api (explain, sohbet), worker (problem/exception explainer `main.go:1205-1210`) | hayır |
| `COREMETRY_AI_API_KEY` | `config.go:808` | `""` → özellik uykuda, UI düğmeleri gizli | Sağlayıcı anahtarı (`sk-ant-…`, `ghu_…`); openai sağlayıcıda opsiyonel. | api, worker | **evet** |
| `COREMETRY_AI_MODEL` | `config.go:811` | `""` → sağlayıcı varsayılanı (`copilot.go:615` `DefaultModels`) | | api, worker | hayır |
| `COREMETRY_AI_BASE_URL` | `config.go:814` | `""` | Yalnız `openai` sağlayıcı: `http://ollama:11434/v1` gibi. | api, worker | hayır |

## 8. Self-observability / K8s kimliği

Binary kendi trace + metriklerini `service.name = coremetry-<rol>`
(`coremetry-monolithic`, `-api`, `-ingest`, `-worker`) ile yayar.

| Değişken | Okunduğu yer | Yokken / varsayılan | Etki | Rol | Gizli |
|---|---|---|---|---|---|
| `COREMETRY_SELF_OBS_OTLP_ENDPOINT` | `selfobs.go:81,109`; `main.go:289-301` | `all` modda boş → `localhost:<grpc port>` (kendi alıcısı, v0.8.218); `api`/`worker`/`agent`'ta boş → SDK **kapalı**; saf `ingest`'te **zorla boş** (self-loop önlemi) | OTel SDK (trace + runtime metrik, 30 s export) hedefi, plaintext gRPC. Compose: `otel-collector:4317`. | all, api, worker, agent | hayır |
| `COREMETRY_SELF_OBS_SAMPLE_RATE` | `selfobs.go:266-277` | `0.1`; `[0,1]` dışı / parse hatası → `0.1` + log | `ParentBased(TraceIDRatioBased)` — frontend `traceparent` "sampled" ise takip edilir. | aynı | hayır |
| `COREMETRY_DEPLOY_ENV` | `selfobs.go:225` | `""` | Kendi telemetrisine `deployment.environment` resource attribute'u. | aynı | hayır |
| `COREMETRY_K8S_NAMESPACE` | `selfobs.go:239` | boş → attribute **hiç yazılmaz** | `k8s.namespace.name`; chart downward API (`deployment.yaml:142-157`). | aynı | hayır |
| `COREMETRY_K8S_POD_NAME` | `selfobs.go:240` | boş → yazılmaz | `k8s.pod.name` | aynı | hayır |
| `COREMETRY_K8S_POD_UID` | `selfobs.go:241` | boş → yazılmaz | `k8s.pod.uid` | aynı | hayır |
| `COREMETRY_K8S_NODE_NAME` | `selfobs.go:242` | boş → yazılmaz | `k8s.node.name` | aynı | hayır |

## 9. Worker arka plan işleri

| Değişken | Okunduğu yer | Yokken / varsayılan | Etki | Rol | Gizli |
|---|---|---|---|---|---|
| `COREMETRY_LOG_ANOMALY_ENABLED` | `config.go:739-740` → `resolveLogAnomalyEnabled` (`config.go:511`) | ayarsız/`true`/`1`/çöp → **açık** (yaml değeri korunur) | Yalnız `false`/`0` log-pattern recorder + Drain templater'ı kapatır (`main.go:900-912`) — periyodik ES/logstore sorgu trafiğinin kaynağı. Metrik anomali (CH) etkilenmez. | worker | hayır |

## 10. Admin / TEHLİKELİ

| Değişken | Okunduğu yer | Yokken / varsayılan | Etki | Rol | Gizli |
|---|---|---|---|---|---|
| `COREMETRY_CH_RESET_SCHEMA` | `main.go:264` | ayarsız → hiçbir şey | **DESTRUCTIVE.** `1`/`true` → boot'ta `DROP DATABASE <db> [ON CLUSTER] SYNC SETTINGS max_table_size_to_drop=0` (`reset.go:110-118`): spans, logs, metrikler, dashboard'lar, kullanıcılar, audit — **her şey**. Env yolu drop'tan sonra **boot'a devam eder** ve şemayı yeniden kurar (`main.go:355-369`); Deployment'ta unutulursa **her restart siler**. `--reset-schema` **bayrağı** ise drop + çıkış (Helm pre-install Job, `reset-schema-job.yaml`). Dağıtık kümede yetim znode süpürmesi için `CH_REPLICA_PATH` ayrı önek olmalı. | hepsi | — |
| `COREMETRY_ADMIN_RESET` | bkz. §6 | `false` | Her boot'ta admin parolasını env'den ezer. | hepsi | — |
| `COREMETRY_DEMO_MODE` | bkz. §6 | `false` | Admin kimlik bilgisini kimliksiz uçtan yayınlar. | api | — |

## 11. Perf / demo araçları (sunucu binary'si okumaz)

| Değişken | Okunduğu yer | Yokken / varsayılan | Etki | Rol | Gizli |
|---|---|---|---|---|---|
| `COREMETRY_PERF_EMAIL` | `cmd/perfcheck/main.go:67` | `admin@coremetry.local` | `make perfcheck` girişi (`-email` bayrağı ezer). | araç | hayır |
| `COREMETRY_PERF_PASSWORD` | `cmd/perfcheck/main.go:68` | `admin` | | araç | **evet** |
| `COREMETRY_URL` / `COREMETRY_EMAIL` / `COREMETRY_PASSWORD` | `scripts/demo-health.sh` (shell); `jboss-demo` profile-pusher (`COREMETRY_URL`) | `http://localhost:8088` / `admin@coremetry.local` / `admin` | `make demo-health` hedefi. Go kodu okumaz. | araç | parola: evet |

---

## Hızlı kontrol listesi — prod'a çıkmadan

- `COREMETRY_JWT_SECRET` ayarlı ve **tüm** pod'larda aynı.
- `COREMETRY_INITIAL_PASSWORD` `admin` değil; `COREMETRY_DEMO_MODE` ayarsız.
- `COREMETRY_CH_RESET_SCHEMA` ve `COREMETRY_ADMIN_RESET` Deployment'ta **yok**.
- Dağıtık CH: `COREMETRY_CH_CLUSTER_NAME` + ayrı `COREMETRY_CH_REPLICA_PATH`.
- Çok replika: `COREMETRY_REDIS_URL` ayarlı ve `/admin/stats` `lockDegraded=false`.
- `COREMETRY_CH_MAX_MEMORY_USAGE` düz bayt (boot logunda "per-query memory limits" satırını doğrula).
- `/api/version` `overridden=false` (bayat `COREMETRY_VERSION` yok).

İlgili: [docs/local-dev.md](local-dev.md) · [README §Configuration](../README.md#configuration) ·
[docs/clickhouse-cluster.md](clickhouse-cluster.md) · [docs/INCIDENTS.md](INCIDENTS.md) ·
[charts/coremetry/README.md](../charts/coremetry/README.md).
