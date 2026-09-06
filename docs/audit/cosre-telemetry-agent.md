# CoSRE Telemetry Chat Agent — Faz 1 AUDIT (2026-09-06)

Amaç: CoSRE sohbetini K8s varlıkları üzerinde gezinebilen ve trace arayabilen
gerçek bir tool-calling telemetri asistanına dönüştürmek. Bu belge YALNIZ
mevcut durumu ve boşlukları yazar; kod yok. Ölçüm tabanı v0.10.465 çalışma
ağacı; dosya:satır kanıtları o sürüme ait. Örneklerdeki adlar SENTETİK
(operatörün görevdeki namespace/servis/host örnekleri burada `shop`,
`shop-payment`, `apigateway.example.com` olarak yazıldı); gerçek müşteri
adları repoya girmez.

Kabul kriterleri (operatör): (1) namespace'teki servisler, (2) serbest metinden
varlık çözümü, (3) değerden attribute keşfi + trace araması, (4) route ile trace
araması, (5) bağlamı koruyan takip soruları, (6) özet + tablo + önceden
doldurulmuş Traces deep-link'i.

## 1. Mevcut durum haritası (dosya → sorumluluk)

| Dosya | Sorumluluk |
|---|---|
| `internal/api/ai_routes.go` | `POST /api/copilot/chat` (SSE), konuşma CRUD, geri bildirim, profil rotaları |
| `internal/api/copilot_chat.go` | Sohbet handler'ı; kademe sırası (guided → drawer → RAG → intent → serbest döngü, :263/:280/:290/:302/:462), serbest tool döngüsü, `ai_calls` kaydı |
| `internal/api/copilot_guided.go` | Kademe 1 deterministik router (`routeGuidedIntent` :894, 30 niyet), prefetch bundle'ları, tek anlatım çağrısı |
| `internal/api/find_entity.go` (v0.10.463), `family_traces.go` (465), `near_names.go`, `trace_nl_search.go`, `open_page.go` | Ad-şekilli mesaj → servis kartı/aday çipleri; aile trace listesi; bulanık ad; "içinde X geçen trace'ler"; "sayfasını aç" |
| `internal/api/copilot_intent.go` | Kademe 3.5 LLM niyet sınıflandırıcısı (katı JSON, beyaz liste `intentAllowed`) |
| `internal/api/copilot_followup.go` | Takip devralma (`applyFollowUpContext` :607), çipler, derin linkler (`guidedAnswerLinks`) |
| `internal/api/copilot_drawer.go`, `trace_explain_unified.go`, `explain_cache.go`, `ai_observability.go` | Explain çekirdeği: `buildTraceExplainInput` + `SystemPromptTrace` + `explainCacheKey` iki yüzeyde ORTAK; `copilotStreamSurface` LLM sarmalayıcısı (ai_calls) |
| `internal/api/ai_conversations.go` | Konuşma arşivi: `saved_views(page='ai-chat')` JSON blob (40 mesaj / 64 KB) |
| `internal/api/chat_mcp_bridge.go`, `chat_tool_budget.go`, `chat_deadline.go`, `mcp_gate.go` | Dış MCP köprüsü; tool sonucu 6000 rune tavanı; uçtan uca süre (180–900 s); rol kapısı `toolsForRole` |
| `internal/copilot/copilot.go`, `chat.go`, `stream.go`, `profiles.go`, `prompts.go` | Sağlayıcılar (anthropic/github/openai), `ChatWithTools`, `StreamText` + fallback, çoklu model profilleri, TÜM sistem prompt'ları |
| `internal/ai/provider/tools.go` | Tool calling tel kodlaması: OpenAI `tools`/`tool_calls`/`role:tool` (:340-379), Anthropic `tools` (:192) |
| `internal/mcptools/tools.go` (+ `search_traces.go`, `discovery.go`, `pivots.go`, `team_ownership.go`) | Tek tool kaydı `ToolList(Deps)` (~44 tool), hepsi salt-okunur, `MinRole: ""` |
| `internal/mcp/mcp.go`, `toolerr.go` | MCP protokolü, `runGate` (tools/call · resources/read · prompts/get), hata sınıfları, 20 s tool bütçesi |
| `internal/entity/*` (syncer, thanos_source, spanpass, normalize, settings) | K8s varlık katmanı (v0.10.127-143): Thanos/KSM + span → `entities`/`entity_relations`; ayar `entity_layer` (varsayılan KAPALI) |
| `internal/chstore/entity_schema.go`, `entity_queries.go`, `entity_store.go`; `migrations/0011_entity_layer.sql` | Varlık tabloları, sorgular (LIMIT 500, `max_execution_time=10`), cluster değer keşfi |
| `internal/thanos/client.go`, `promql.go`, `cluster_identity.go`, `cluster_detect.go` | Thanos istemcisi (15 s), cluster matcher enjeksiyonu, `InstantSamples` genel yardımcı, span-cluster eşlemesi |
| `internal/chstore/repo.go` (`TraceFilter`, `GetTraces`), `filterexpr.go`, `trace_identity_first.go`, `attr_index.go`, `promoted_attr.go`, `trace_facets.go` | Trace arama motoru, çip grameri, kimlik-önce arama, kvh bloom, terfi kolonlar, facet kaydı |
| `internal/api/api.go:4167` `parseTraceFilter`; `traces_extras.go`; `route_registry.go` | `/api/traces*` param ayrıştırma (liste/sayım/dışa aktarım tek kaynak); ek kolon ayrı istek; `registerRoutesExtra` defteri |
| `internal/auth/auth.go`, `live_authz.go`, `custom_roles.go`; `internal/ldap/*` | Rol kapısı (admin/editor/viewer; özel roller yalnız sayfa görünürlüğü); LDAP grup→rol, takım özniteliği |
| `frontend/src/components/CopilotChat.tsx`, `ai/AIDrawer.tsx`, `ai/useChatThread.ts`, `ai/ChatBubble.tsx`, `ai/chatMarkdown.ts`, `lib/openHref.ts` | Sohbet kabuğu (461'den beri Explain kabuğu), gönderme döngüsü, balon/kart render, blok ayrıştırıcı, `open` gezinmesi |
| `frontend/src/pages/Traces.tsx`, `lib/urlState.ts`, `lib/logsUrl.ts`, `pages/Service.tsx`, `pages/Pod.tsx`, `pages/Clusters.tsx`, `pages/EntityDetail.tsx`; `internal/api/link_window.go` | Deep-link sözleşmeleri (aşağıda §4), sunucu linklerine pencere eklenmesi |

## 2. Bulgular

### 2.1 Mevcut AI/chat katmanı

- **Uçtan uca:** backend `internal/api/copilot_chat.go` (SSE) → kademeler → `internal/copilot` (LLM) → `internal/mcptools` (tool'lar); frontend `CopilotChat.tsx` / `AIDrawer.tsx` → `useChatThread` → `ChatBubble`.
- **LLM istemcisi:** OpenAI-uyumlu `/chat/completions` (`internal/ai/provider/tools.go:254`); sağlayıcı `anthropic|github|openai`; env `COREMETRY_AI_PROVIDER|API_KEY|MODEL|BASE_URL` (`internal/config/config.go:786-797`), runtime ayarları `system_settings` (`copilot.go:1164/1297`, 30 s yenileme); ≤20 model profili, yüzey→profil eşlemesi (`profiles.go`), istek başına `context.profile`.
- **Tool/function calling: VAR, yerel.** İstek `tools:[{type:function…}]`, cevap `choices[0].message.tool_calls`, sonuç `role:"tool"` (`tools.go:311-379`). Serbest döngü: `chatMaxToolRounds=5` (`copilot_chat.go:58/:462`), tool başına 20 s (`mcp.ToolCallBudget`), sonuç 6000 rune tavanı, aynı ad+arg tekrar koruması (:566), 4. turda tool'suz kapanış, bağlam taşmasında bir kez küçült-yeniden dene. Geçmiş `ClampHistory(40 mesaj, 6000 rune)` (:148).
- **Streaming: KISMEN.** `delta` olayları yalnız anlatım kademelerinde (guided/drawer/RAG/intent/explain-in-chat, `copilotStreamSurface` → `StreamText`, fallback önbellekli). **Serbest tool döngüsü tamponlu** (`copilot_chat.go:44-48`) — tool-calling asistanı için akış sonradan eklenmeli (ara adımlar zaten `step` olaylarıyla akıyor).
- **Konuşma geçmişi:** uçuşta frontend state (her turda tam geçmiş POST edilir); kalıcı `saved_views(page='ai-chat')` JSON blob `{messages:[{role,text}], subject, updatedAt}` — tool çağrıları arşivlenmez; tavanlar 40 mesaj / 64 KB / 50 konuşma (`ai_conversations.go:59-80`); **özetleme YOK** (bilinçli ret, `internal/ai/assemble/history.go:5-8`). ClickHouse'ta ayrı `ai_conversations` tablosu YOK.
- **Explain ↔ chat paylaşımı:** `buildTraceExplainInput` + `SystemPromptTrace` + `explainCacheKey` iki yüzeyde aynı (v0.10.453/460); ortak LLM soyutlaması `copilotExplain*/copilotStreamSurface` (her çağrı bir `ai_calls` satırı). Çoğaltılmış tek dikiş: sohbetin kendi SSE yayıcısı (`deliverExplain` yerine).
- **Render sözleşmesi (`answer`):** `text` (mdLite: kalın/kod/liste/başlık/pipe tablo; ` ```chart ` çiti canlı uPlot), `links` → çip, `suggestions` → çip, `open` → SPA gezinme (`mergeOpenHref`), `sources`, `exchangeId` (👍/👎); `step`/`step-result` ayrı olaylar (şeffaflık paneli, `ChatStepDetail.href`). **Genel "aksiyon butonu" ve yapılandırılmış tablo payload'ı YOK** — tablo yalnız markdown; kabul kriteri 6 için bu yeterli (özet + markdown tablo + `links`).

### 2.2 Kubernetes varlık verisi — EN KRİTİK

| Kaynak | Bugün | Eksik |
|---|---|---|
| **Varlık katmanı (v0.10.127-143)** | `entities` (cluster/node/namespace/workload/pod/container/service; ORDER BY `(entity_type, cluster_id, entity_id, valid_from)`, TTL 180 g), `entity_relations` (`parent`, `runs_on`, `runs`; child bloom), `entity_sync_runs`, MV `entity_seen_1m/5m` (`service_name, cluster, k8s_namespace, k8s_pod`). Leader-only syncer 60 s (15 s–1 h), 4 paralel cluster. API: `GET /api/entities?cluster&type&namespace&q&at&limit`, `/api/entity?id`, `/api/entity/services`, `/api/services/{name}/pods` (15–30 s cache; LIMIT 500, `max_execution_time=10`). | **Ayar `entity_layer` varsayılan KAPALI** (`internal/entity/settings.go:59`); kapalıyken 404 `{"disabled":true}`. Prod: 0011 + bayrak operatörde (memory). Bu ÖN KOŞUL. |
| **Thanos / KSM** | Syncer 6 anlık sorgu/cluster: `kube_node_info`, `kube_pod_info`, `kube_pod_owner`, `kube_replicaset_owner`, `kube_job_owner`, `kube_pod_container_info` (`internal/entity/samples.go:22-29`); Clusters sayfası `kube_deployment_*` (`promql.go:400-428`); rollouts RS seti (`internal/rollout/ksm.go`). İstemci 15 s, ≤1000 seri, per-cluster matcher enjeksiyonu; handler cache 60 s; genel yardımcı `InstantSamples(ctx, cluster, expr)` (`cluster_identity.go:309`). | `kube_service_info` YOK. Namespace/workload düzeyinde önbelleklenmiş "katalog" sorgusu canlı yok — ama **syncer zaten 60 s'de CH'ye yazıyor**, canlı KSM sorgusuna gerek yok. |
| **Span resource attribute'ları** | `spans`: `service_name`, `deploy_env`, `host_name`, `cluster` (MATERIALIZED: `k8s.cluster.name` → `openshift.cluster.name` → `cluster`, res+attr; `repo.go:331-339`), **terfi MATERIALIZED kolonlar** `k8s_namespace` (`k8s.namespace.name`/`kubernetes.namespace.name`), `k8s_pod` (fallback `host_name`), `k8s_node` (`promoted_attr.go:100-102`); skip index `idx_k8s_pod`/`idx_k8s_node` `set(0)` (0011:104-105). DISTINCT cluster değerleri `EntitySeenClusterValues` (MV, LIMIT 200, 10 s; ham fallback 1 saat/15 s). | `k8s.deployment.name` terfi DEĞİL (workload adı KSM owner'dan ya da pod adı önekinden `podWorkloadName`); `k8s_namespace` üzerinde index YOK; res dizilerinde bloom YOK (kvh yalnız attr). |
| **`deployment.environment.name` = `prod-<cluster>`** | Böyle bir ayrıştırma **YOK**: `deploy_env` bağımsız kolon; cluster yalnız `k8s.cluster.name`/`openshift.cluster.name`/`cluster` attribute'larından türer. Span cluster değeri → Remote Cluster kaydı eşlemesi Thanos ayar blobunda (`SpanClusterValues`), admin API `POST /api/settings/thanos/assign-span-cluster` + otomatik etiket algılama (v0.10.139-141). | Env'den cluster türetimi gerekiyorsa `clusterDeriveExpr`'e 4. kol (`deploy_env` regex) + eşleme — operatör kararı; bugün yapılmıyor. |
| **Telemetri gönderen vs tüm workload'lar** | Bayrak AÇIKKEN ikisi de var: tüm workload'lar KSM'den (`normalize.go:173-200`, span ön koşulu yok), telemetrili olanlar `entity_seen_5m` + `runs` ilişkisi. | İkisini JOIN eden "telemetrisiz workload" sorgusu/uç/sayfa **YOK**; admin kapsama kartı span-örneklemli (200k satır), sıfır-span workload'u göremez. |

**Öneri (maliyet/tazelik):** yeni bir `k8s_entities` tablosu YAZMA — periyodik materialize tablo ZATEN `entities` + `entity_relations` (60 s, leader, TTL 180 g). Asistanın katalog tool'ları bunları okumalı (FINAL + LIMIT + `max_execution_time`), canlı Thanos sorgusu yalnız "şu an replica/pod sayısı" gibi anlık sorularda ve 60 s cache'li mevcut handler'lar üzerinden. Eksik olan üç sorgu: (a) namespace → workload → pod sayısı → `service.name` (relations + `entity_seen_5m`), (b) "telemetrisiz workload" anti-join, (c) namespace/workload bulanık çözüm (çok-cluster). Prod ön koşulu: 0011 uygulanmış + `entity_layer` açık.

### 2.3 Trace arama API'si

- **Sözleşme:** `GET /api/traces` (+`/count`, `/export.csv`, `/aggregate`, `/shapes`, `/{id}`); tek ayrıştırıcı `parseTraceFilter` (`api.go:4167-4258`). Param → `TraceFilter`: `service`, `search` (trace düzeyi HAVING), `traceId` (32-hex tam), `from/to` (**yoksa son 1 saat**), `hasError`, `rootOnly`, `services` (CSV, ≤8, hepsini içeren), `minMs/maxMs`, `attrKey/attrVal`, `env`, `cluster`, `sort` (`time|duration|…`), `order`, `limit` (50), `offset` (sayfa yok, cursor yok), `filters` (JSON `[{k,op,v}]`), `filterGroup` (AND/OR ağacı, `filters`'ı ezer), `extraAttrs`, `count` (`skip|approx|exact`). Aggregate: `groupBy/groupAttr` + `having`.
- **Çip grameri (`filterexpr.go`):** op = `=, !=, =~, !~, LIKE, NOT LIKE, IN, NOT IN, >, >=, <, <=, EXISTS, NOT EXISTS` (boş → `=`; başka op hata). Anahtar çözümü: `resource.X` → terfi kolon → wellKnown (`service.name`, `host.name`, `deployment.environment[.name]`) → `res_values[indexOf(res_keys,?)]`; `span.X`/çıplak → terfi → wellKnown (`http.route`→`http_route`, `http.method`, `http.status_code`, `cluster`, `duration_ms`…) → `attr_values[…]`. Dizi yolunda `=`/`IN` **kvh bloom** (`has(attr_kvh, cityHash64(k\x1Fv))`, v0.10.300) ile; `EXISTS` bilerek `has(attr_keys,…)`. Regex `^(?:…)$` çapalı `match`. `contains`/`prefix` op olarak YOK (LIKE `%v%` var; prefix için `LIKE` + `v%` FE'de yok → tool katmanında normalize edilmeli).
- **Kimlik-önce arama (v0.10.299-344):** tek jeton ≥8 karakter → terfi/facet anahtarlarında anahtar başına ayrı sorgu (indeksli önce, ilk isabet), `CandidateIDs` → `trace_id IN` (bloom); gömülü zaman damgası ±12 s pencere; facet kaydı `system_settings['trace_facets']` (≤16 facet), 0014 sihirbazı (`admin_attr_index.go`). Maliyet kapıları: liste ham 25 s, MV 12 s, extras 15 s, kimlik 5 s; cache 20 s.
- **Attribute KEŞFİ:** amaca uygun uç **YOK**. En yakınlar: `GET /api/attribute-keys` (200k satır ÖRNEKLEM, `filters` ile daraltılır, servis paramı yok, 60 s), `GET /api/attribute-values?key=&q=` (anahtar ZORUNLU; wellKnown tipli kolon, aksi 200k örneklem ya da `q` varken ILIKE taraması), `GET /api/services/{name}/attrs` (anahtar + ÖRNEK değerler, 5000 iç span, 10 s) — "bu değeri hangi anahtar taşıyor?" sorusunu (kabul 3) hiçbiri cevaplamıyor. MCP'de attribute tool'u YOK → **model anahtar uydurur**; en büyük risk burada.
- **Route defteri:** `route_registry.go` (v0.10.247): dosya kendi `init()`'inde `registerRoutesExtra("ad", (*Server).registerXxxRoutes)`; `api.go` büyümez, ad sırasıyla deterministik kayıt (`trace_facets_routes.go:18-22` örneği). Önerilen yeni dosyalar: `internal/api/attr_discovery_routes.go` (`registerAttrDiscoveryRoutes`: `/api/attributes/describe`, `/api/attributes/find-by-value`), `internal/api/entity_catalog_routes.go` (`registerEntityCatalogRoutes`: `/api/entity/catalog/namespaces|workloads|pods`, `/api/entity/resolve`), `internal/api/chat_context_routes.go` (`registerChatContextRoutes`: `/api/copilot/context`). Tool'ların kendisi HTTP değil `internal/mcptools/` (in-app + dış MCP tek kayıt).
- **Perf tuzağı (CHANNEL_CODE/FUNCTION_CODE):** `docs/audit/traces-attribute-columns.md`. Liste sorgusu değil, **2. faz `traceExtrasSQL`** (`repo.go:3171-3245`): sayfa satırları zaman-dışı sıralamada tüm pencereye dağıldığı için zaman sınırı 6 saate genişler; `spans` `ORDER BY (service_name, time)` servissiz sorguda yalnız `idx_trace` bloom'a kalır; dizi yolu terfi kolona göre 16× bayt. Mevcut önlemler: ayrı sınırlı istek (`traces_extras.go`: ≤200 id, ≤8 attr, ≤35 g, 20 s cache, 15 s), terfi kolon + `set(0)` index, kvh bloom. **Asistan aynı tuzağa düşer mi?** Süzgeç tarafında EVET (herhangi bir `filters` çipi MV hızlı yolunu kapatır → ham `GROUP BY trace_id`, 25 s tavan), kolon tarafında hayır (extras ayrı). Bugünkü MCP `search_traces`'ta süzgeç argümanı hiç yok; eklendiğinde zorunlu strateji: (1) `from/to` her çağrıda (varsayılan 30 dk, tavan 6 s; ötesi için önce `trace_stats`), (2) `limit ≤ 50`, (3) anahtar `describe_attributes`'tan gelmeli ve terfi/indeksli mi işaretli olmalı — indekssiz anahtarla ≥1 saat pencere REDDEDİLİR ya da daraltılır, (4) zaman-dışı sıralama + attr süzgeci birlikte YASAK (sayfa saçılması), (5) kimlik-şekilli tek jeton → kimlik-önce yol, (6) `search` (trace HAVING) ile çip (span düzeyi) farkı cevapta ilan edilir (INCIDENTS v0.10.341).

### 2.4 Frontend deep-link sözleşmesi

| Sayfa | Parametreler (kanıt) |
|---|---|
| `/traces` | `service`, `search`, `traceId`, `minMs`, `maxMs`, `hasError=true`, `rootOnly=true|false|auto`, `services` (CSV, hepsini içeren), `env`, `cluster` (yalnız okunur), `filters` (JSON FilterExpr[]), `filterGroup`, `sort`, `order`, `page`, `view=list|aggregate|shapes`, `groupBy`, `groupAttr`, `s_traces-agg`, `having`, `cols` (CSV ≤8; öncelik URL > sunucu tercihi > localStorage), `viz`, `range` (`Traces.tsx:249-402`, yazım `rebuildPreserving` :438-490 yabancı paramı korur) |
| `/trace` | `id`, `span`, `tab=trace|logs`, `xn=1`, `range`; global `ai`/`aisrc`/`aicode`/`chat` |
| `/logs` | `q` (KQL; `search` yalnız okunur), `service`, `cluster`, `severity` (sayı tabanı, 17=error), `traceId`, `spanId`, `hasTrace=1`, `filters` (`encodeFiltersParam`), `cols`, `range` (**tek pencere paramı; from/to okunmaz**), `env`, `panel=patterns|templates`, `breakdown`, `asc=1`, `doc`/`docsvc`; link üreticisi `logsHref` (`logsUrl.ts:258`) |
| `/service` | **`name`** kanonik (`service` yedek), `tab=overview|operations|details|logs|topology|infra|pods` (`traces` → /traces yönlendirir), `op`, `range`, `env`; alt sekme paramları (`jpod`, `icluster`, `lq/llvl`, `hops/eonly`, `ep`, `anomaly`). `src=metric` yalnız `/endpoints` ve `/endpoint`. |
| `/pod` | `cluster`, `namespace`, `pod`, `service`, `range`, `deploy` (yoksa pod adı öneki), `from`, `at`; `spans` (PodTracesTable) |
| `/clusters` | `cluster`, `ns` (`"<cluster>|<namespace>"` çekmece), `namespace`, `section=overview|pods|nodes`, `service`, `deployment`, `q`, `live=1`, `range`, `tw` |
| `/entity` | `id` (`pod:<cid>/<ns>/<pod>`, `wl:<cid>/<ns>/<kind>/<name>`…), `at` |
| Node / namespace sayfası | **YOK** — `/clusters?section=nodes` ve `ns=` çekmecesi |

Pencere: `range=<preset>` (`5m…30d`) ya da `range=custom:<fromMs>-<toMs>` (ms); sunucu linkleri `link_window.applyAll` ile yalnız 9 rotaya (`/service /services /traces /trace /logs /endpoints /databases /messaging /service-map`) pencere ekler — `/pod`, `/clusters`, `/entity` listede DEĞİL (asistan linki pencere taşımaz; eklenmeli). `env` URL > localStorage. Çip süzgeçleri `filters=` JSON'ı sunucudaki `chstore.FilterExpr` ile aynı şekil (`{"k","op","v"}`) — v0.10.465 `familyTracesHref` bunu zaten kullanıyor.

### 2.5 Yetki ve güvenlik

- **Veri kapsamı kısıtı YOK:** yalnız rol kapısı (`RequireRole`/`RequireAnyRole`, canlı 10 s cache, CH hatasında fail-open); özel roller sayfa görünürlüğü. Kullanıcı satırında cluster/namespace/env/servis alanı yok; `users.team` görüntü amaçlı (LDAP `TeamAttribute`). LDAP grup senkronu (`ldap_groups`) yalnız iki admin uçta okunuyor, veri kapısı değil; OIDC grup→rol **YOK** (varsayılan rol). `?env=` istemciden gelen bir süzgeçtir, kısıt değildir (Faz 4 env-ayrımı hâlâ açık).
- **Tool'lara kimlik:** `auth.FromContext(ctx)` sohbet ve tel yollarında handler'a ulaşıyor (`copilot_chat.go:220`, `mcp.go:1110`), ama `mcptools.Deps` kimlik taşımıyor ve `CallMeta`'da `Role` yok. Sunucu taraflı kapsam süzgeci için tek gereken: tool'ların ctx'ten kimliği okuyup bir `Scope{Clusters, Namespaces, Envs}` uygulaması — bugün böyle bir kaynak (kullanıcı→kapsam) YOK; ilk adım boş-kapsamlı bir arayüz + LDAP grup→kapsam eşlemesi (ayrı karar).
- **Salt-okunur:** 36+ tool'un tamamı okur (`tools.go:33-43` sözleşmesi); `runGate` tools/call·resources/read·prompts/get üçünde; in-app `toolsForRole` hem liste hem dispatch haritasını süzer. Guided rotalar tool katmanını atlar (`mcp_gate.go:89-93`) — bugün zararsız (hepsi viewer), kapsam süzgeci gelince guided bundle'lar da aynı süzgeçten geçmeli.
- **Maskeleme:** üründe redaction yok (operatör kararı); collector işi. Maskeli değeri çözmeye çalışan kod YOK; `internal/reqid` sabit-ofsetli düz kimliği dilimler (hash çözmez). Yeni tool'lar değer "ters çevirme" yapmaz; maskeli değer (`***`, hash) geldiğinde olduğu gibi gösterilir.
- **İz:** tel MCP çağrıları audit satırı (`mcp.tool.call`, arg önizlemesi ≤256 rune); **in-app yerel tool çağrıları audit satırı YAZMIYOR** (yalnız span + `ai_calls`) — dış MCP çağrıları yazıyor; asimetri kapatılmalı.

## 3. Boşluk listesi (hedef davranış için eksikler)

| # | Boşluk | Kabul | Bugün | Effort |
|---|---|---|---|---|
| G1 | `entity_layer` prod'da kapalı / 0011 uygulanmamış | 1,2,5 | Katman kodu hazır, bayrak operatörde | operatör |
| G2 | Namespace → workload → pod sayısı → `service.name` bileşik sorgusu + MCP tool'u | 1 | relations + `entity_seen_5m` ayrı ayrı var | ~2 saat |
| G3 | Serbest metin → {cluster, namespace, workload, service, pod} skorlu çözüm (`resolve_entity`) | 2 | Yalnız servis (v0.10.463/464 `NearNames`, `find_entity`) | ~yarım gün |
| G4 | `describe_attributes(scope, lookback)` — anahtar + terfi/indeks bilgisi + top değerler, servis kapsamlı | 3,4 | `/api/attribute-keys` örneklemli, servissiz; `/services/{name}/attrs` örnek değer | ~yarım gün |
| G5 | `find_attribute_by_value(scope, value)` — hangi anahtar bu değeri taşıyor (aday anahtar listesi × kvh probu) | 3 | YOK | ~2 saat |
| G6 | `search_traces` süzgeç/zaman/kapsam argümanları (`filters[]`, `from/to`, env/cluster/namespace) + zorunlu kapı (§2.3 strateji) | 3,4 | Yalnız service/search/errors/sort/range | ~yarım gün |
| G7 | `trace_stats` tool'u (aggregate ucu var, tool yok) | 3,4 | `/api/traces/aggregate` | ~2 saat |
| G8 | `build_link` — sunucu tarafı link üretici tool'u; `/pod`, `/clusters`, `/entity` için pencere uygulaması | 6 | `guidedAnswerLinks` + `link_window` (9 rota) | ~2 saat |
| G9 | Sunucu taraflı yapılandırılmış sohbet bağlamı (`set/get/clear_context`) — konuşma-kapsamlı state | 5 | Ekran bağlamı + önceki tur devralma + explain bağlamı; yapılandırılmış state YOK | ~yarım gün |
| G10 | Takip cümleleri → bağlam mutasyonu ("son 1 saate genişlet", "sadece hatalı", "bunun pod'ları", "aynı filtreyle loglar") deterministik | 5 | `applyFollowUpContext` servis/aralık devralır; pencere/durum/pivot mutasyonu YOK | ~yarım gün |
| G11 | Serbest döngüde streaming (delta) | — | Tamponlu | ~2 saat |
| G12 | In-app yerel tool çağrısı audit satırı | güvenlik | Yalnız dış MCP | ~1 saat |
| G13 | Kapsam süzgeci arayüzü (`Scope`) + tool/guided uygulaması; kullanıcı→kapsam kaynağı kararı (LDAP grup?) | güvenlik | YOK | ~yarım gün + karar |
| G14 | `k8s.deployment.name` terfi kolonu ya da KSM owner ile workload adı (span tarafı) | 1,5 | Pod adı öneki (FE) / KSM owner (syncer) | ~2 saat (+0011 ek) |
| G15 | Telemetrisiz workload anti-join sorgusu | 1 | YOK | ~1 saat |
| G16 | Eval seti: 6 kabul cümlesi + varyantları (Türkçe ekler, aday/çok-cluster) — router + tool-döngüsü altın vakaları | hepsi | `intent.json` 7 vaka | ~yarım gün |

## 4. Önerilen tool kataloğu (gerçek şemaya göre düzeltilmiş)

Discovery (hepsi `entities`/`entity_relations` FINAL + LIMIT + `max_execution_time=10`; bayrak kapalıysa dürüst `disabled` cevabı):
- `list_clusters()` → **VAR** (`list_clusters`); ek alan: son telemetri zamanı (`entity_seen_5m` max bucket), sync durumu (`entity_sync_runs`).
- `list_namespaces(cluster?, query?)` → YENİ; `q` = `NearNames` (çok-cluster: aynı ad birden çok cluster'da → satır başına cluster).
- `list_workloads(namespace, cluster?, kind?, query?)` → YENİ; kind/replica (KSM), pod sayısı (`parent` ilişkisi), `service.name` eşlemesi (`runs` + `entity_seen_5m`), `telemetry: true|false` (G15).
- `list_pods(workload|namespace, cluster?)` → YENİ; pod/node/phase/restart (KSM `kube_pod_info` alanları entities'te), son span zamanı (`entity_seen_5m`).
- `resolve_entity(text)` → YENİ; namespace/workload/service/pod adaylarını skorla (tam > önek > jeton > mesafe), cluster/namespace bağlamıyla; 1 aday = çözüldü, 2+ = sor (bugünkü `find_entity` çip deseni).

Telemetri:
- `describe_attributes(scope{service|namespace|cluster}, lookback)` → YENİ, **arama öncesi ZORUNLU** (sunucu: `search_traces` bilinmeyen anahtarı reddeder — "önce describe"); çıktı: anahtar, kapsam (span/resource), `promoted|indexed|array`, top ≤10 değer, örneklem notu.
- `find_attribute_by_value(scope, value, lookback)` → YENİ; aday anahtar sözlüğü (host: `server.address`, `http.host`, `net.peer.name`, `url.full`, `http.url`, `peer.hostname`; route: `http.route`, `url.path`, `http.target`) × kvh/terfi kolon probu; sonuç `{key, count, indexed}`.
- `search_traces(scope, filters[], time_range, status?, min_duration?, sort?, limit)` → **VAR, genişletilecek** (G6); `filters[].op ∈ eq|ne|in|contains|prefix|regex|exists` → sunucuda `=|!=|IN|LIKE|LIKE v%|=~|EXISTS`; `contains` yalnız terfi/indeksli anahtarda ya da ≤1 saat.
- `trace_stats(scope, group_by, filters, time_range)` → YENİ (aggregate ucu üzerinden).
- `get_trace(trace_id)` → **VAR**.
- `search_logs(trace_id | scope+filters, time_range)` → **VAR** (`search_logs`, `get_logs_for_trace`; ES federasyonu logstore üzerinden).
- `get_service_health(service, time_range)` → **VAR**.
- `build_link(page, scope, filters, time_range)` → YENİ; §2.4 sözleşmesini üretir (`filters=` JSON, `hasError`, `range=custom:ms-ms`, `name` vs `service` doğru param).

Bağlam:
- `set_context/get_context/clear_context(fields?)` → YENİ; state sunucuda konuşma kimliğine bağlı (`saved_views` blob'una `context` alanı ya da Redis `copilot:ctx:<conv>` TTL 24 s); alanlar: `cluster, namespace, workload, service, time_range, status, filters[]`. Tool'lar boş argümanı bağlamdan doldurur; her cevap "aktif bağlam" satırı taşır (FE D5 çipi).

Her tool: sunucu taraflı kapsam süzgeci (G13 arayüzü, bugün boş), zorunlu zaman sınırı (varsayılan 30 dk, tavan 6 s; ötesi `trace_stats`), satır limiti (≤50), `max_execution_time`, kısaltılmış sonuç özeti (6000 rune tavanı zaten var).

## 5. Faz planı

- **Faz 2 — entity katmanı + discovery tool'ları (ön koşul G1):** G2, G14/G15, G3 (`resolve_entity` = `find_entity`'nin namespace/workload/pod genişlemesi; guided çipleri aynı), `list_namespaces/list_workloads/list_pods` + `describe`; kabul 1-2 için guided rotalar (LLM'siz kart + tablo). ~2 gün.
- **Faz 3 — trace arama tool'ları + deep-link:** G4, G5, G6 (+ zorunlu kapı), G7, G8; kabul 3-4 için guided rota ("servisinin içinde <değer> olan trace'ler" bugünkü `trace_search`'ün attribute-farkındalı sürümü); deep-link round-trip testi (sunucu href → FE ayrıştırıcı aynı `TraceFilter`). ~2 gün.
- **Faz 4 — bağlam yönetimi + eval:** G9, G10 (takip cümleleri deterministik), G11 (akış), G12/G13 (audit + kapsam arayüzü), G16 evalset (`-tags evalset` kapıda). ~2 gün.

## 6. Riskler

- **Sorgu maliyeti:** attribute süzgeci MV yolunu kapatır; indekssiz anahtar + geniş pencere = ham tarama. Önlem: `describe_attributes` etiketleri + sunucu kapısı (indekssiz anahtarda ≤1 saat), `LIMIT 50`, `max_execution_time`, zaman-dışı sıralama+attr süzgeci yasağı, geniş pencere için önce `trace_stats`.
- **Yüksek kardinalite:** `url.full`/`http.target` değerleri; top-değer listesi ≤10 ve örneklemli (dürüst `sampled: true`); `attr-values` ILIKE taraması yalnız `q` ile ve ≤1 saat.
- **LLM'in attribute uydurması:** sunucu bilinmeyen anahtarı REDDEDER (describe zorunlu, katalog dışı anahtar → hata + yakın anahtar önerisi, `NearNames` deseni); guided rotalar 6 kabul cümlesini LLM'siz çözer (küçük yerel model dersi: prefetch+narrate > tool-loop).
- **Yanlış cluster'da arama:** span cluster değeri ↔ Remote Cluster eşlemesi atanmamışsa `resolve_entity` bunu ilan eder ("eşlenmemiş değer"); çok-cluster adaylar satır başına cluster ile listelenir; `set_context(cluster)` olmadan cluster'lar-arası liste tek cevapta birleştirilmez.
- **Bayrak kapalı prod:** entity tool'ları `disabled` der, uydurmaz; Faz 2 prod kabulü G1'e bağlı.
- **Kapsam kısıtı yok:** bugün her viewer her şeyi görür; tool'lar bunu değiştirmez ama G13 arayüzü olmadan yeni tool eklemek borcu büyütür — Faz 4'te arayüz şart, kaynak (LDAP grup→kapsam) operatör kararı.
- **Maskeleme:** collector'da; tool'lar değeri olduğu gibi taşır, çözmez.
- **Ad sızıntısı:** test/fixture/doküman yalnız sentetik ad (repo kuralı).
