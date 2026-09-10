# CoSRE v2 — Birleşik Agent Çekirdeği — FAZ 1 AUDIT (2026-09-07)

**Durum:** salt-okunur denetim, kod yok, operatör onayı bekliyor.
**Ölçüm tabanı:** çalışma ağacı v0.10.532. Dört paralel salt-okunur tarama
(yüzeyler/LLM runtime · sayfa bağlamı · sinyal kataloğu/RBAC/maliyet ·
render/insight/bilgi tabanı/feedback) + önceki denetimler:
[copilot-chat-2026-08-25.md](copilot-chat-2026-08-25.md) (dört kademe, sayı
güvencesi, injection), [cosre-telemetry-agent.md](cosre-telemetry-agent.md)
(entity + trace tool'ları, v0.10.466-492) ve
[ai-assistant-skills-audit-2026-09-05.md](ai-assistant-skills-audit-2026-09-05.md)
(prompt sahipliği, evalset, gözlemlenebilirlik). Onlarda kapanan bulgular burada
tekrar edilmez, yalnız referans verilir.

Kabul kriterlerine karşı **bugünkü** durum (özet; ayrıntı §2-§5):

| # | Hedef davranış | Bugün | Hangi faz kapatır |
|---|---|---|---|
| 1 | Trace sayfasında "bunu açıkla" → trace sorulmaz | ✅ büyük ölçüde: `context.trace` (`/trace?id=`) sohbete gidiyor, Explain zaten özne-kilitli | Faz 3 (sayfa bağlamı protokolü bunu her sayfaya genelleştirir) |
| 2 | Servis sayfasında "dün bu saatte de böyle miydi?" → karşılaştırmalı chart | ⚠ servis + aralık gidiyor; **"dün"ü çözen tool yok**, chart yalnız `render_chart` (now-çapalı, tek pencere) | Faz 3 (render sözleşmesi `chart{timerange, compare}`) + Faz 4 (`query_metric` compare) |
| 3 | "X'te son 1 saatte error rate neden arttı?" → trace+log+metrik+rollout, hipotez, kanıt | ⚠ guided `family_health`/RCA yolu var; **rollout tool'u yok, namespace-pencere deploy sorgusu yok**, kanıt linkleri kısmi | Faz 4 |
| 4 | Problem detayında sormadan özet insight | ⚠ `problems.ai_summary` (arka plan) + RCA ribbon + auto verdict var; **etkilenen servis / ilk anomali / yakın rollout / benzer geçmiş** tek kartta yok, "benzer geçmiş" hiç açılmamış | Faz 5 |
| 5 | "runbook'ta ne yazıyor?" → bilgi tabanı + kaynak | ❌ runbook tablosu var ama **okuma tool'u yok**; RAG sohbet kademesi var, tool değil; kaynak çipi yalnız RAG cevabında | Faz 5 (lexical yeter, embedding şart değil) |
| 6 | "Filtreyi uygula" düğmesi mevcut sayfayı günceller | ❌ cevapta durum değiştiren düğme yok; `open:` alanı `navigate()` yapıyor; üç uyumsuz `?filters=` kodeği | Faz 3 |
| 7 | 👍/👎 + serbest metin kayıt | ✅ `ai_feedback` (+comment) ve paylaşılan `AIFeedbackButtons`; ⚠ üç elle yazılmış kopya, sohbette yorum kutusu yok | Faz 2 (atom birleştirme) + Faz 5 (öğrenme döngüsü) |

---

## 1. Mevcut durum haritası (dosya → sorumluluk)

### 1.1 Üç yüzey — aslında "iki kabuk + bir çekirdek + arka plan tüketicileri"

Görevdeki "üç bağımsız yüzey" varsayımı bugün yarı doğru: **AIDrawer → CopilotChat
birleşmesi v0.10.461/483'te bitti**. `components/ai/AIDrawer.tsx` 11 satırlık bir
yönlendirme; `CopilotChat.tsx:100-104` `?ai=<kind>:<id>` öznesini okuyup aynı
kabukta `AIDrawerBody` çiziyor (pin: `drawerParity.test.ts`, `singleDrawer.test.tsx`).
"AI Copilot" adlı üçüncü bir sohbet sayfası yok; `/ai` admin AI-gözlemlenebilirlik
panosu (`pages/AIObservability.tsx`, 813 satır).

| Katman | Dosya | Sorumluluk | Satır |
|---|---|---|---|
| **Rota kaydı** | `internal/api/ai_routes.go:69` `registerAIRoutes` | 20+ explain rotası, sohbet, feedback, budget, evalset; tek 503 kapısı `requireCopilot` (`:39-49`, yapısal test `ai_routes_test.go`) | 170 |
| **Sohbet handler'ı** | `internal/api/copilot_chat.go:129` | 5 kademe: guided → drawer → RAG → intent (LLM sınıflandırıcı) → serbest tool döngüsü; SSE `emit` + heartbeat + adım kimlikleri; bütçeler | 828 |
| **Guided kademesi** | `internal/api/copilot_guided.go` | ~20 niyetin deterministik prefetch + tek anlatım çağrısı | 3556 |
| **Drawer kademesi** | `internal/api/copilot_drawer.go` | Explain bağlamına dayalı takip | 562 |
| **RAG kademesi** | `internal/api/rag.go:380` | doküman anlatımı, delta'lar atılır | 511 |
| **Intent kademesi** | `internal/api/copilot_intent.go` | strict-JSON sınıflandırıcı, slot doğrulama | 491 |
| **Ekran bağlamı** | `internal/api/chat_screen_context.go` (v0.10.32) | `{service, operation, rangeS, toMs, env, trace}` → deterministik önsöz + çip | — |
| **Çalışma seti** | `internal/api/chat_context.go` (v0.10.478) | Redis `copilot:ctx:<user>:<conv>` 24 s, `_` köprüsü 10 dk; `get/set/clear_context` tool'ları | — |
| **Konuşma kalıcılığı** | `internal/api/ai_conversations.go` (v0.9.1139) | `saved_views(page='ai-chat')` blob; 40 mesaj / 64 KB | — |
| **Explain sarmalayıcıları** | `internal/api/ai_observability.go:96-502` | 7 sarmalayıcı (buffered/stream × path-surface/explicit × plain/json/masked); `make audit` CHECK 4 doğrudan `s.copilot.Explain`i yasaklar | ~300 |
| **Explain teslimi** | `internal/api/copilot_explain_stream.go:171` `deliverExplain` | `?stream=1` → SSE (`delta/answer/done`), buffered → JSON; Redis cache 1 s (`explain_cache.go`) | — |
| **Tool kataloğu** | `internal/mcptools/tools.go:248` `ToolList` | **47 tool**, tek kaynak; MCP sunucusu + in-app döngü aynı listeyi kullanır | — |
| **Tool kapısı/bütçesi** | `internal/api/mcp_gate.go`, `chat_tool_budget.go`, `chat_mcp_bridge.go` | rol filtresi (bugün no-op: 47 tool'un hepsi `MinRole:""`), 6000 rune sonuç tavanı, dış MCP köprüsü + audit | — |
| **LLM istemcisi** | `internal/copilot/copilot.go` + `internal/ai/provider/*` | anthropic / github / openai-uyumlu; JSON kipi merdiveni; thinking salvage; stream fallback | — |
| **Prompt'lar** | `internal/copilot/prompts.go` (1634 satır) | 35+ sistem prompt'u; `promptOwnership` kapısı api paketinde prompt tanımını yasaklar | — |
| **Model profilleri** | `internal/copilot/profiles.go` | ≤20 profil, yüzey→profil eşlemesi, çözüm sırası `WithProfile > surfaceProfiles > default` | 541 |
| **Öz-gözlem** | `internal/api/ai_span.go`, `chat_span.go`, `internal/selfobs` | `ai.chat` → `ai.chat.turn` / `ai.tool` / `ai.explain` (+ `gen_ai.*`); `ai_calls` tablosu | — |
| **FE sohbet kabuğu** | `components/CopilotChat.tsx` (581), `ai/useChatThread.ts` (285), `ai/ChatBubble.tsx` (619), `ai/chatMarkdown.ts` (263) | tek AppShell mount'u; SSE okuma; blok ayrıştırma; chart fence; link çipleri | ~1750 |
| **FE Explain** | `components/CopilotExplain.tsx` (573), `ai/ExplainBody.tsx`, `components/Markdown.tsx` | özne-kilitli explain; ikinci markdown yazımı | — |
| **FE insight** | `components/ai/InsightCard.tsx` | satır-altı kart; ilk SSE çerçevesinde deterministik sinyaller, sonra anlatı | — |
| **FE AIAnalysisPanel** | `components/AIAnalysisPanel.tsx` (336) | düz POST, akış yok, iptal yok, kendi hata eşlemesi | — |

### 1.2 LLM'e giden **tüm** giriş noktaları
- Etkileşimli: `ai_routes.go:99-138` altındaki 20 explain rotası + sohbet; alan-sahipli
  `GET /api/{problems,anomalies}/{id}/rootcause/explain`, `POST /api/admin/clickhouse/optimize-query`.
- Arka plan: `internal/anomaly/problem_explainer.go` (`problem-auto-explain`, 30 s tik,
  yalnız critical+open, batch 16), `exception_explainer.go` (`MinOccurrences:500`),
  `internal/api/rca_auto_verdict.go` (`rootcause-auto`, 30 dk anchor dedup).
- Diğer: `copilot_aianalyze.go` (servis analizi), `insight.go` (kart açılışında),
  `namespace_guided.go`, `ai_settings_profiles.go:206` (probe), `ai_evalset.go`.
- `ai_calls.surface` değerleri: `chat, chat-guided, chat-drawer, chat-intent, chat-intent-none,
  chat-general, rag-chat, explain-*, problem-auto-explain, exception-auto-explain,
  rootcause-verdict, rootcause-auto, settings-probe, ch-optimize, evalset*, embedding`.

---

## 2. Sekiz başlık için bulgular

### 2.1 Üç yüzeyin envanteri, çoğaltma, model konfigürasyonu

**Çoğaltılmamış (yeniden çözme):** SSE *okuma* tek yazım (`lib/sse.ts:36`, 3 tüketici);
boş-cevap salvage tek yazım (`internal/ai/provider/salvage.go`, `salvage_singleton_test`);
prompt sahipliği tek dosya; explain attribution `explainCallCtx` (`ai_observability.go:131`).

**Hâlâ çoğaltılmış:**
1. **SSE yazımı (sunucu) iki gövde, 11 dosyadan emit.** `copilot_chat.go:176` (mutex +
   heartbeat + adım kimliği) vs `copilot_explain_stream.go:118` (tembel başlık, heartbeat
   yok). Çerçeve şekli aynı ama tipe değil yoruma + teste bağlı.
2. **Yedi attribution sarmalayıcısı** (`ai_observability.go:96/116/173/210/253/270/502`)
   — aynı karar matrisi elle 7 kez; `ExchangeID` taşıma yorumu 3 kez kopya (v0.9.593
   regresyon sınıfı).
3. **FE iptal/hata dört yazım:** `CopilotExplain.tsx` (2 AbortController), `useChatThread.ts`,
   `InsightCard.tsx`; `AIAnalysisPanel.tsx` iptalsiz. Abort-vs-hata sınıflandırıcısı
   (`ai/chatAbort.ts`) yalnız sohbette.
4. **Hata → Türkçe metin iki sözlük:** FE `lib/aiErrors.ts` (3 regex) vs BE
   `chat_deadline.go:84`, `chat_overflow.go`, `observe_meta.go ClassifyAIErrorText`.
5. **Markdown üç yazım:** `chatMarkdown.ts`+`ChatBubble.mdLite` (tablo + chart fence),
   `components/Markdown.tsx` (`anatomy`, kod), `AIAnalysisPanel` (markdown yok).
6. **İptal semantiği yüzeye göre:** sohbet = uçtan uca deadline (`chat_deadline.go`: ×3,
   180-900 s) + tool bütçesi 20 s + Stop + kısmi metin korunur; Explain = yalnız istemci
   abort; AIAnalysisPanel = hiçbiri.

Büyüklük: beş sohbet kademesi **~5.600 Go satırı**, her biri kendi prefetch → prompt →
narrate → emit → record dizisiyle.

**Model konfigürasyonu — tek yerden mi?** Evet ve hayır:
- Tek blob: `system_settings["ai_copilot"]` (`copilot.go:1113-1151`): provider/model/
  baseUrl/maxTokens/temperature/timeoutS + **profiller** (`profiles.go`, ≤20, yüzey→profil).
  Boot env `COREMETRY_AI_*` yalnız tohum. 30 s refresh.
- **Model-farkında tablo YOK.** `maxTokens` global-ya-da-profil skaler; JSON kipi ve
  streaming yeteneği çalışma zamanında öğrenilip `(provider,baseURL,model)` başına
  cache'leniyor (`stream.go:102`, `copilot.go:989`); `tuneTemperature()`'ın `include`
  boolu var ama her zaman `true` (`copilot.go:504-512`). `enable_thinking` /
  `reasoning_effort` / `/no_think` / model başına completion bütçesi **hiçbir yerde yok**.
- **Qwen3 thinking olayı bugün nasıl karşılanıyor:** `openAICompletionTokens = 4096`
  (gerekçe `copilot.go:892-895`, `provider/openai.go:28-33`: 1024'te düşünce ortasında
  `finish_reason=length`, boş içerik) + `SalvageAnswer` zinciri (content →
  `reasoning_content` → `reasoning` → `<think>` içi; her salvaj işaretlenir, cache'e
  yazılmaz, `explain_cache.go:41-44`). Yani semptom yamanmış, **sebep (model-farkında
  bütçe) merkezî değil**. Öneri: `internal/ai/llm/capabilities.go` — model adı desenine
  göre `{thinking: off|native|tag, completionBudget, temperatureAllowed, jsonMode,
  streams}`; profilde ezilebilir; runtime tek noktadan uygular.

**Öneri (görevdeki AgentRuntime):** §4'te paket yapısı. Kısa cümle: `copilot.Service`
transport/politika olarak kalır; **turn**'ü sahiplenen tek tip yok — beş kademe ve 42
explain çağrı noktası her seferinde turn'ü elle kuruyor. Runtime bu "turn"ü
sahiplenir; üç ince adaptör (chat / explain / insight) yalnız girdi şeklini çevirir.

### 2.2 Sayfa bağlamı protokolü

**Bugün nerede duruyor:** global store **yok** (yalnız QueryClient/Router/Confirm/Auth
sağlayıcıları, `main.tsx`, `AuthProvider.tsx:14`). URL kaynak-of-truth:
- Zaman: `lib/useUrlRange.ts:128` (`?range=`, sessionStorage `cm.lastRange` yedek);
  kodek `lib/urlState.ts:9,47` (`preset` ya da `custom:<fromMs>-<toMs>`).
- Env: `lib/useUrlEnv.ts` (`?env=` + localStorage `coremetry-env`).
- Kapsam: `lib/contextParams.ts` (saf) + `hooks/useContextParams.ts:60`
  (`cluster/namespace/service/compare` + `sig` + `windowNs`) — **tek tüketici**
  `pages/Traces.tsx:238`; diğer sayfalar `useUrlRange`+`useUrlEnv`+ham `useSearchParams`.
- Sohbet bugün ne gönderiyor (`lib/api.ts:2044-2119`):
  `{ tzOffsetMin, tz, service, operation, explain, subject, rangeS, trace, env, toMs,
  profile, conversation }`. **Yok:** `page, cluster, namespace, workload, pod, problem_id,
  active_filters, visible_columns, selected_chart_series`.
- Servis çözümü `lib/chatContext.ts serviceFromRoute` = **elle tutulan rota listesi**
  (`/traces,/endpoints,/logs,/inbox,/metrics,/explore,/clusters,/profiling,/endpoint` +
  `/service` `?name`, `/pod` `?service`, `/service-map` `?focus`); eksik rota = sessizce
  bağlamsız sohbet (dosyanın kendi yorumu bu sınıfı iki sürüm ıskaladığını yazıyor).
- Sohbet paneli **AppShell'de tek mount** (`AppShell.tsx:189`), rota değişince
  remount olmaz, `useLocation()` ile yeniden hesaplar (`CopilotChat.tsx:130-155`).
- Ters yön (sohbet → sayfa) var: `answer.open` → `mergeOpenHref` (`lib/openHref.ts:21`,
  yalnız `/traces` için sayfa-sahipli param silme) → `navigate(replace:true)`;
  `coremetry:ai-evidence` / `ai-focus` CustomEvent'leri şelale/istisna vurgusu.

**Sayfa × alan matrisi** (U=URL, S=bileşen state, L=localStorage/session, P=sunucu
tercihi `useTablePrefs`, R=sunucu cevabı, –=anlamsız):

| Sayfa | cluster | namespace | service | workload | pod | trace_id | problem_id | time_range | active_filters | visible_columns | chart series |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `/trace` | – | – | – | – | R | **U** `?id` | – | U | – | – | U `?span` |
| `/traces` | U | – | U | – | – | U `?traceId` | – | U (+S zoom) | **U** `?filters` FilterExpr[] + skalerler | U `?cols` > P > L | S + L legend |
| `/service` | – | – | U `?name` | – | R | – | – | U | U `?op`,`?tab` | L | S + L |
| `/problems` | – | – | U | – | – | – | **U** `?problem` (+`?exc`) | U | U `?tab,?owner,?sre,?minOcc` | L | – |
| `/logs` | U | – | U | – | – | U `?traceId`,`?spanId` | – | U | **U** `?filters` **LogFilter** kodeği + `?q,?severity` | U > P > L | – |
| `/explore` | U | – | U | – | – | – | – | U (özel) | U `?q` → `fl/fg/by` | U > P > L | **S** `focusKey/hiddenKeys` |
| `/clusters` | U | U (+`?ns` çekmece) | U | U `?deployment` | S | – | – | U (+`?tw`) | U `?q,?section` | L×4 | S |
| `/pod` | U | U | U | U `?deploy` | **U** `?pod` | – | – | U | – | L | S |
| `/entity` | R | R | R | R | R | – | – | U | – | – | S |
| `/rollouts` | U | U | – | U `?rollout` | – | – | – | U | U `?status,?tab` | L | – |
| `/inbox` | – | – | U | – | – | – | **U** `?problem` | U | U (8 param) | L | – |
| `/endpoints`,`/endpoint` | U | – | U | – | – | – | – | U | U `?entry,?src,?compare…` | U > P > L | S |
| `/databases`,`/statement` | – | – | U/R | – | – | – | – | U | U `?dbsys,?dbname,?stmt` | L | S |
| `/dashboard` | U(değişken) | U(değişken) | U(değişken) | – | – | – | – | U | U serbest `?<var>=` | – | S + L |
| `/service-map` | – | – | U `?focus` | – | – | – | – | U | U `?hops,?eonly,?node` | – | – |

Çapraz: `env` her sayfada U+L; `time_range` `?range=` yoksa sessionStorage.

**Üç bulgu:**
1. **11 alanın 9'u URL'den saf `(pathname, search)` fonksiyonuyla türetilebilir.**
   Yalnız `visible_columns` (`useDataTable` state/tercih) ve `selected_chart_series`
   (Explore `useState`, `cm.legendVis:*`, cursorBus) URL dışı — bunlar bir "sayfa
   sağlayıcısı" ister (§4 `usePageContextPublisher`).
2. **`?filters=` üç uyumsuz kodek:** `FilterExpr[]` JSON (Traces/Explore,
   `lib/urlState.ts:71`), `LogFilter[]` kompakt tuple (Logs, `lib/logFilters.ts:101`),
   ad-hoc skalerler. "Filtreyi uygula" aksiyonu sayfa başına kodek adaptörü ister
   (yeni kodek değil: mevcut `rebuildPreserving` + `writeLogsParams` + `writeScopeParams`).
3. `/problems` ve `/inbox` `?problem=` çekmece; `/problems/:id` rotası yok — `problem_id`
   çekmece parametresinden okunur.

**Sayfa değişince bağlam güncellemesi ve pin:** bugün her turn'de ekrandan yeniden okunuyor
(doğru varsayılan; `chat_screen_context.go` yorumu "bağlam bir varsayılan, kelepçe değil").
**Pin önerisi: EVET, ama sohbet-yerel ve görünür.** Gerekçe: operatör sorunu Problem
sayfasında açıp kanıt için /traces'a geçtiğinde sohbet öznesi kaybolmamalı; aksi hâlde
"aynı problemden bahsediyoruz" varsayımı model tahminine kalır (küçük modelde uydurma
yüzeyi). Mekanik: FE `pinnedContext` (sohbet thread'inde, `saved_views` blobuna yazılır) +
çip "📌 <servis> · <aralık>"; pin varken ekran bağlamı **yine gönderilir** ama
`pinned:true` işaretlenir; sunucu önsözde ikisini ayrı satır yazar ("Sabitlenmiş bağlam …
/ Şu an açık sayfa …"). Redis `copilot:ctx` (24 s) zaten konuşma-ölçekli çalışma seti —
pin oraya değil thread'e yazılır (kullanıcılar arası sızıntı dersi v0.10.487).

### 2.3 Sinyal kapsamı (tool kataloğu §3'te)

- **Trace:** [cosre-telemetry-agent.md](cosre-telemetry-agent.md) §2.3/§4 kapsıyor;
  `search_traces` (filters/namespace/cluster/env/min-max + kapı), `trace_stats`,
  `describe_attributes`, `find_attribute_by_value`, `get_trace`, pivot tool'ları.
- **Log:** `logstore.Store` arkasında CH ve ES (`logstore.go:387-493`); **serbest metin
  arama zaten tool** (`search_logs`, `tools.go:996`: query grammar + trace_id + severity,
  limit 50-500); trace_id join `get_logs_for_trace`; histogram tool var. **Yok:** desen/
  şablon tool'u (`/api/logs/patterns`, `/templates`, `CountPatterns` — "hangi şekil yeni
  başladı" en ucuz sinyal), bağlam (±N) tool'u. ES maliyeti: `_msearch` toplu, 15-60 s
  cache, staleTime ≥ sunucu TTL disiplini (`api.ts:789`).
- **Metrik:** `metricSource` seam'i (CH|VM, `metricsource.go`), VM düşerse 502 (fallback
  yok). `query_metric` tool'u parametrik (name/service/aggregation/group_by/range/step).
  `/api/metrics/promql` (`api.go:841`) var ama **tool'u yok**; CH tarafı PromQL alt
  kümesi (`ValidatePromQL`), VM tarafı doğrulamasız MetricsQL — agent hangi lehçeyi
  üretebileceğini bilemez.
  **Serbest MetricsQL vs parametrik — karar:** parametrik çekirdek + **kapılı ifade
  tool'u**. Gerekçe: (a) küçük model (gemma4) MetricsQL'i güvenilir üretemez, her hata
  bir tur yakar; (b) serbest ifade kardinalite kalkanlarını (`topk`, `maxSeriesParsed`,
  rollup seçicileri) atlar; (c) parametrik tool rollup seçicilerini (`metricRollupPlan`,
  `PickRollup`) bedava kullanır. Esneklik boşluğunu `query_metric_expr` kapatır:
  yalnız allowlist'li fonksiyon/aggregation grameri (`rate|increase|sum|avg|max|min|
  quantile by (label…)`), metrik adı katalogdan doğrulanır, step/pencere sunucuda
  kelepçelenir, seri tavanı 20, sonuç her zaman "hangi ifade koştu" ile döner. Ham
  passthrough yalnız admin ve tool dışı.
  **RED rollup aileleri:** DAR (10s→1h, tDigest p50/95/99, dims service/kind/status) →
  **yüzdelik, SLO, "yavaşladı mı"**; GENİŞ (1m→1h, 20 kovalı sabit histogram ±%15-30,
  + endpoint/channel/function) → **yalnız kırılım sıralaması** ("hangi endpoint/kanal").
  Agent kuralı: soru yüzdelik istiyorsa DAR, kırılım istiyorsa GENİŞ ve cevapta "≈"
  işareti; ikisi de yoksa `spanmetrics_1m`/MV. Bu, `/clickhouse-schema` §6'daki seçici
  sözleşmesinin aynısı — tool açıklamasına yazılır, modele bırakılmaz.
- **Cluster metrikleri:** Thanos yalnız **15 sabit handler** (pod/node/namespace/deploy/
  network/JMX/HAProxy), `clampThanosWindow` (30 g, v0.10.531), `maxSeriesParsed=1000`,
  8 MB gövde, `promQuote`, cluster matcher enjeksiyonu. **Raw PromQL passthrough yok** ve
  önerilmez. Güvenle açılabilecek: mevcut handler'ların parametrik tool aynası
  (`cluster_metric{kind: pod_cpu|pod_mem|node|namespace|network|deploy_trend, cluster,
  namespace?, workload?, range}`) — kalkanlar handler'da, tool ek risk taşımaz.
- **Problem/anomali:** `list_problems` (25-200), `list_anomalies`, `get_problem_root_cause`
  (hipotez + DeepEvidence), `list_problem_window_events` var. **Yok:** `get_problem`
  (tekil, çekmece paritesi), `get_correlation_evidence` (DeepEvidence alt alanları:
  Rollouts/TraceIDs/AffectedPods/LogSignatures/External — bugün tek JSON kolonu,
  alt-sorgulanmıyor), **`similar_problems`** (`FindSimilarResolvedProblems`
  `problem.go:1358` var, uç ve tool yok; anahtar yalnız (service, ruleID)).
- **Deployment/Rollout:** `list_deploys` servis-kapsamlı; **`GetDeploysInWindow`
  (`deploys.go:1120`) uçsuz/tool'suz**; K8s rollouts katmanı (ReplicaSet, `/api/rollouts`
  namespace destekli, Problem↔Rollout bağı `rollout_problem.go:49`) **sıfır MCP yüzeyi**.
  "Şu pencerede bu namespace'te ne deploy edildi" bugün cevaplanamıyor → Faz 4'ün ilk
  tool'u `list_changes(cluster?, namespace?, service?, from, to)` = deploy events +
  inferred deploys + rollouts birleşik, kaynak etiketiyle.
- **dış kaynak (Influx dönemi) hata sayacı:** genel dış-metrik yolu (`metric_points` `ext:<sorgu>`, kind=external
  problem). **trace_id pivotu VAR:** `influx/enrich.go:102` SORGU 2 satırlarından
  `traceid/trace_id/trace.id` ayıklar (≤50, geçersiz sayılır), CH span + ES log'a fan-out,
  `exemplars` tablosuna yazar; sonuç `DeepEvidence.External`. Tool yok; `query_metric`
  ile seri okunur. v0.10.532 oran serisi de aynı yoldan.
- **Entity katmanı:** `list_namespaces/list_workloads/list_pods/resolve_entity` var
  (v0.10.468-471) ama bayrak varsayılan **kapalı** (404 `{"disabled":true}`); servis→
  workload yönü (`EntitySeenForService`) tool'suz; agent için **yetenek probu** yok
  (`get_capabilities`: entity/rollouts/VM/Thanos/RAG açık mı).

### 2.4 Cevap render sözleşmesi

**Bugün:** sohbet cevabı markdown + sunucu-üretimli bloklar: ```` ```chart ```` fence
(`cosreChartSpec.ts:15-31`, yalnız `render_chart` tool argümanından, model-yazımı
fence'ler serbest döngüde ayıklanır `chat_chart_origin.go`), `turn.links` çipleri,
`answer.open` (otomatik navigasyon), tool adım çipleri `↗ Üründe aç`, inline 32-hex
trace linkleri. Chart **zaten sohbet çekmecesinde** çiziliyor (`CosreChart` → lazy
`CorePanelMulti`/uPlot, 300 nokta, ≤8 seri, birim `AGG_UNIT`'ten zorla).
- Tablo: `.cm-md-table` (sıralama/yeniden boyutlama yok), `DataTable` bilinçli değil.
- **Aksiyon bloğu yok.** Sayfa state'ini değiştiren tek şey `navigate()`.
- Akış: `parseChatBlocks(text, streaming)` yarım satırı tutar (`chatMarkdown.ts:113-160`);
  yani blok-düzeyi ayrıştırma akış sırasında zaten var. Serbest tool döngüsü **akmaz**
  (buffered, 15 s heartbeat). Insight `ChartSpec[]` üretiliyor ama çizilmiyor
  (`InsightCard.tsx:56-64`: CosreChart now-çapalı, kart penceresi tarihsel).

**Öneri — blok protokolü:** SSE `block` olayı `{id, type, seq, final, payload}`:

| type | payload | kaynak | akar mı |
|---|---|---|---|
| `text` | markdown parça | model | delta ile |
| `table` | `{columns[], rows[], sort?, href?}` | **yalnız tool sonucu** | bütün |
| `chart` | `{series[], from, to, unit, compare?: {shift: 86400}}` | tool sonucu (`render_chart`/`query_metric`) | bütün |
| `trace_list` | `{traces[], deep_link}` | `search_traces`/`family_traces` | bütün |
| `link` | `{href, label, kind}` | sunucu link kurucusu | bütün |
| `action` | `{kind: apply_filter|set_range|open_drawer|navigate, params, label}` | **yalnız tool sonucundan türetilir, model metninden ASLA** | bütün |
| `evidence` | `{claims[], sources[]}` (trace/log/metric/rollout/doc kaynak çipleri) | RCA/hipotez | bütün |

Kurallar: model yalnız `text` üretir; yapısal bloklar deterministik (v0.10.47 chart
origin kararının genellemesi). Akışta metin delta'ları blok arasına serpilir; yapısal
blok tool bittiği anda gelir (bugünkü `step-result` çerçevesinin tipli hâli). Eski
`answer.text` markdown'ı bir sürüm boyunca birlikte yayımlanır (FE bayrak).
FE: `BlockRenderer` → `text`=`ChatBubble` markdown, `table`= ≤20 satır `.cm-md-table`,
>20 satır `DataTable` (`storageKey:'ai-<blockId>'`), `chart`=`CosreChart` +
`from/to` zorunlu (now-çapası kalkar → insight kartı da çizebilir), `action`=`Button`
atomu. **Güvenlik:** aksiyonlar salt sayfa-state; `applyPageAction(action)` FE
dispatcher'ı sayfa kodeğine göre yazar (Traces `FilterExpr`, Logs `LogFilter`,
`?range=`, `?problem=`); sunucu hiçbir zaman yazmaz; `navigate` yalnız kök-göreli.

### 2.5 Gömülü insight'lar

Bugün dört tetik ve dört depo yan yana (§B tablosu): `problems.ai_summary` (arka plan,
critical+open, 30 s tik, batch 16) · `exception_groups.ai_summary` (≥500 occurrence) ·
`root_cause_hypotheses` (synthesizer, deterministik, LLM yok) + `rca_verdicts` (auto,
30 dk dedup) · InsightCard (kart açılışında, cache yok) · Explain (Redis 1 s, `?refresh=1`).
`ai.budget` blobu **yalnız gözlem**; hiçbir istek engellenmez; fren = 429 sonrası 1 s
devre kesici + işçi batch tavanları.

**Operatör önerisi ("Problem oluşunca bir kez üret, CH'ye yaz, sayfada oku, yenile ile
tekrar") doğrulandı — kısmen zaten böyle**, iki düzeltmeyle:
1. Kabul kriteri 4'ün dört kalemi (**etkilenen servisler, ilk anomali zamanı, yakın
   rollout, benzer geçmiş problem**) **LLM'siz** hesaplanabilir: hipotez `Nodes/Path`,
   `problem.StartedAt` + `anomaly_events` ilk kova, `DeepEvidence.Rollouts` (v0.10.242-244),
   `FindSimilarResolvedProblems`. Öneri: **"insight başlığı" deterministik**, synthesizer
   tikinde `root_cause_hypotheses.deep_evidence` JSON'una eklenir (DDL yok), sayfa
   `/api/problems/{id}/rootcause` ile okur (zaten 60 s cache). LLM anlatısı isteğe bağlı
   ve mevcut `problem_explainer` yolundan (yalnız critical) — her sayfa açılışında çağrı
   yok.
2. "Yenile" = mevcut `?refresh=1` + explain cache; arka plan tik dokunmaz.
Alternatif (açılışta üret + cache): InsightCard deseni; maliyet `serveCached` +
anchor-dedup ile sınırlı ama P2/P3 problemlerde gereksiz LLM harcar — **önerilmez**,
yalnız "Yenile" için.

### 2.6 Bilgi tabanı

- **Var:** `runbooks` + `runbook_executions` (birinci sınıf tablolar, `store.go:2258`;
  sayfalar `Runbooks/Runbook/RunbookExecution.tsx`; problem köprüsü
  `ProblemRunbookPanel.tsx`), `service_metadata` (owner/sre team, runbook_url, oncall_url,
  chat_channel, repository, custom_links), `team_contacts`/`team_aliases` blobları,
  `incidents` + `POST /api/copilot/draft-postmortem` → RAG'e postmortem,
  `rag_chunks` (brute-force cosine, ANN yok, ~100k chunk tavanı; **BM25 lexical fallback
  `TopKRagChunksByContent` var**, embedding yoksa devrede), URL/wiki crawler (30 dk).
- **Yok:** runbook okuma tool'u (MCP `suggest_runbook` prompt'u modelin uydurmasını
  ister), RAG tool'u (yalnız anlatım kademesi), "benzer geçmiş problem" arama ucu.
- **Depolama kararı:** **embedding'e gitme.** Ölçek: runbook onlarca, doküman ≤200,
  chunk on binler altı; bge-m3 iki aydır operatör blokörü ([project-rag-state]). Lexical
  (`TopKRagChunksByContent`) + **yapısal arama** (runbook adı/servis eşlemesi,
  `service_metadata.runbook_url`) kabul kriteri 5'i karşılar; embedding geldiğinde aynı
  `search_knowledge` tool'u arkada geçer, sözleşme değişmez.
- **Kaynak gösterimi:** `evidence.sources[]` zorunlu: `{kind: runbook|doc|url, id,
  title, section, chunkIdx, href}`; RAG chunk'ının başlık satırı `section` olarak
  yakalanır (chunker'a ek). Cevapta kaynaksız iddia → shield sayacına ek sınıf.

### 2.7 Hafıza, feedback, öğrenme

- **Konuşma state'i:** görevdeki "v1 kararı: `conversation_state`" **kodda yok**; v1 kararı
  (operatör A1, 2026-08-16) `saved_views(page='ai-chat')` blobu (`ai_conversations.go`,
  40 mesaj/64 KB, TTL yok → sınırsız birikir) + Redis `copilot:ctx` 24 s çalışma seti.
  Explain yüzeyleri ikisine de katılmıyor.
- **Feedback:** `ai_feedback(exchange_id, surface, verdict, comment, user_email)` 90 g TTL,
  `POST /api/ai/feedback`, paylaşılan `AIFeedbackButtons` (11 yüzey) + **üç elle yazılmış
  kopya** (`ChatBubble.rateTurn`, `RCAVerdictPanel`, `AIAnalysisPanel`) → sohbette yorum
  kutusu yok. Kabul kriteri 7 için: kopyaları atoma indir, arşivden gelen turn'lere
  `exchangeId` taşı (bugün bilinçli yok, `chatPersist.ts:40-47`).
- **Anomali feedback şeması ile ortak mı?** `anomaly_verdicts(event_id, fingerprint,
  verdict, note)` olay-anahtarlı, `ai_feedback` exchange-anahtarlı — **tablolar ayrı
  kalır**, ortak olan *okuma modeli* (haftalık rapor). `rca_verdicts` zaten `ai_feedback`
  ile `exchange_id` üzerinden bedava join.
- **Feedback ile ne yapılacak — NET:** **fine-tuning YOK.** (1) 👎 + yorum → evalset adayı
  (`GET /api/ai/evalset/export` var; hedef: aday → `internal/copilot/evalset/*.json`
  yarı-otomatik, CI dışı replay `-tags evalset`); (2) haftalık rapor `/ai` sayfasında:
  surface × `prompt_version` × tool başına 👎 oranı, `error_class`, tool başarısızlık/
  tekrar oranı (`ai_calls` + `ai.tool` span'ları); (3) mevcut RCA LEARN önseli
  (`ConfirmedRCASignatures`, yalnız 👍) aynen kalır. Beklenti: **model öğrenmez, prompt/
  tool/rota değişir ve evalset kırmızıya döner** — kazanç ölçülebilir olur.

### 2.8 Güvenlik, maliyet, gözlemlenebilirlik

- **RBAC / veri kapsamı:** roller `admin/editor/viewer` + özel roller **yalnız sayfa
  görünürlüğü** (`custom_roles.go:104`); LDAP grup senkronu rol + takım taşır,
  **cluster/namespace kapsamı yok** (`ldap/sync.go`); `envServices` istek parametresi,
  kullanıcı özniteliği değil ve harita hatasında **filtresiz** düşer (`inbox.go:680`);
  47 tool'un hepsi `MinRole:""` → `toolsForRole` no-op; guided router tool kapısını
  atlar. **Öneri:** `auth.Scope{Clusters, Namespaces, Services}` — kaynak LDAP grup →
  kapsam eşlemesi (admin ayarı; G13 kararı hâlâ operatörde), `Deps` üzerinden **her
  tool'un filtresine sunucuda enjekte** (search_traces/search_logs/query_metric/
  list_problems/entity/rollouts), prompt'a yazılmaz; kapsam dışı istek boş küme değil
  "kapsam dışı" hatası (sessiz daralma yok). Guided rotalar da aynı `Deps` kapsamını
  okur. Explicit test: iki kullanıcı, iki kapsam, aynı soru → farklı küme.
- **Maskeleme:** binary içinde **yok** (operatör tercihi, [feedback-no-redaction];
  `internal/pipeline` yalnız drop/enrich/sample). Collector'da maskelenen alan
  Coremetry'ye hiç gelmez → agent onu göremez; "çıkarım yoluyla unmask" riski
  telemetride değil **kod-bağlamı (repo) ve RAG dokümanlarında** — kod `ai_calls`'ta
  maskeli (v0.9.4xx kararı); RAG chunk'ları için aynı `prompt_sample` maskesi eklenmeli.
  Doğrulama: `prompt_injection_test` + shield'e "maskeli alan adı geçen cevap" sınıfı
  eklenmez (yanlış pozitif); bunun yerine ai_calls örneği üzerinde periyodik grep raporu.
- **Prompt injection:** savunma tek cümle `DataNotInstruction` (5 kademe, kimlik
  pinli) + intent satırı + fabrication shield (sayaç, cevabı değiştirmez). Yeterli değil
  ama küçük modelde girdi temizleyici de yalan söyler. **Öneri:** (1) tool sonuçları
  runtime'da `<data source="…">` sınırlayıcıyla ve "data" rolüyle sarılır (tek yazım);
  (2) `action` blokları yalnız tool sonucundan türetilir — modelin "şu filtreyi uygula"
  metni aksiyon üretmez (yürütme kanalı kapanır, en etkili önlem); (3) kontrol
  karakterleri/ANSI/`<think>` taklidi ayıklanır; (4) evalset'e injection vakaları.
- **Maliyet:** turn bütçeleri var (5 tur, tool sonucu 6000 rune, geçmiş 40/6000 rune,
  deadline ×3 180-900 s, explain cache 1 s, 429 → 1 s devre kesici). **Kullanıcı başına
  kota yok**, `ai.budget` uygulanmıyor. Öneri: runtime girişinde `budget.Reserve(user,
  surface)` — günlük token tavanı kullanıcı başına (`ai_budget` blobuna alan), aşımda
  429 + Türkçe metin + `/ai` sayfasında görünür; arka plan işçileri ayrı havuz.
  Cache: blok protokolü tool sonuçlarını `serveCached` anahtarıyla (hash-all-inputs)
  yeniden kullanır; aynı turn'de tekrar çağrı muhafızı zaten var (`markRepeatedCall`).
- **Self-observability:** `ai.chat → ai.chat.turn / ai.tool / ai.explain` span'ları +
  `gen_ai.*` var (`ai_span.go`, `chat_span.go`), `COREMETRY_SELF_OBS_OTLP_ENDPOINT`
  opt-in, `mode=all`'da varsayılan. **Kör noktalar:** HTTP handler katmanı, VM istemcisi,
  Thanos istemcisi, logstore (ES) — yavaş tool'un hangi arka uca gittiği görünmüyor.
  Runtime tool yürütücüsü tek yerden `ai.tool` span'ı + alt istemci span'ları (VM/Thanos/
  ES'e `traced` sarmalayıcı, CH'deki `traced_conn.go` emsali) → `/ai` sayfasında tool
  gecikme p95 + hata oranı.

---

## 3. Tool kataloğu (mevcut / eksik / effort)

Gecikme: lokal/prod ölçümlerinden kaba; K = kardinalite riski (D düşük, O orta, Y yüksek).
RBAC bugün tüm tool'larda viewer; "kapsam" = §2.8 Scope enjeksiyonu gerekir.

| Tool | Sinyal | Kaynak | Mevcut | Gecikme | K | RBAC | Effort |
|---|---|---|---|---|---|---|---|
| `search_traces` | trace | CH spans/MV | ✅ v0.10.473 | 0.3-6 s (kelepçe) | O | viewer+kapsam | — |
| `trace_stats` | trace | CH MV/raw | ✅ | 0.5-3 s | O | viewer+kapsam | — |
| `get_trace` | trace | CH | ✅ | <1 s | D | viewer | — |
| `describe_attributes` / `find_attribute_by_value` | trace | CH örneklem | ✅ | ≤3 s | O | viewer | — |
| `get_logs_for_trace` | log | ES/CH | ✅ | 0.2-2 s | D | viewer | — |
| `search_logs` (serbest metin) | log | ES query_string / CH | ✅ | 0.5-5 s (ES) | O | viewer+kapsam | — |
| `get_log_histogram` | log | ES/CH | ✅ | <2 s | D | viewer | — |
| **`log_patterns`** | log | `/api/logs/patterns` (örneklem) + `templates` | ❌ | ≤2 s | D | viewer | S (uç var) |
| `query_metric` | metrik | `metricSource` (VM/CH) + rollup seçici | ✅ | <1 s | O | viewer+kapsam | — |
| **`query_metric` + `compare`** | metrik | aynı + `/api/metrics/compare` | ❌ (uç var) | <1 s | O | viewer | S |
| **`query_metric_expr`** (kapılı gramer) | metrik | VM MetricsQL / CH alt küme | ❌ | <2 s | **Y** (tavan 20 seri) | viewer+kapsam | M |
| `get_service_health` / `get_operation_health` | RED | DAR/GENİŞ rollup | ✅ | <1 s | D | viewer | — |
| `list_metric_names` | metrik | katalog | ✅ | <1 s | D | viewer | — |
| **`cluster_metric`** | k8s | Thanos sabit handler'lar | ❌ (15 uç var) | 1-15 s | O (topk/1000) | viewer+kapsam | M |
| `get_pod_health` | k8s | Thanos | ✅ | 1-5 s | D | viewer | — |
| `list_namespaces` / `list_workloads` / `list_pods` / `resolve_entity` | entity | CH entities (bayrak) | ✅ (bayrak kapalı) | <1 s | D | viewer+kapsam | — |
| **`workloads_for_service`** | entity | `EntitySeenForService` | ❌ (uç var) | <1 s | D | viewer | S |
| `list_problems` / `list_anomalies` | problem | CH | ✅ | <1 s | D | viewer+kapsam | — |
| `get_problem_root_cause` | RCA | `root_cause_hypotheses` | ✅ | <1 s | D | viewer | — |
| **`get_problem`** | problem | `GetProblem` | ❌ | <1 s | D | viewer | S |
| **`get_correlation_evidence`** | RCA | DeepEvidence alt alanları | ❌ | <1 s | D | viewer | S |
| **`similar_problems`** | geçmiş | `FindSimilarResolvedProblems` (+ symptom anahtarı) | ❌ | <1 s | D | viewer | S/M |
| `list_deploys` (servis) | deploy | span version MV | ✅ | <1 s | D | viewer | — |
| **`list_changes`** (ns/pencere) | deploy+rollout+event | `GetDeploysInWindow` + `RolloutList` + operator-events | ❌ | <1 s | D | viewer+kapsam | M |
| **`get_rollout`** | rollout | `RolloutByID` + `RolloutServices` | ❌ | <1 s | D | viewer | S |
| `get_deploy_diff` / `get_correlated_changes` | deploy | RED önce/sonra | ✅ | <2 s | D | viewer | — |
| **`external_series`** (Influx/<measurement>) | dış metrik | `metric_points ext:*` + `exemplars` pivot | ❌ (`query_metric` ile kısmen) | <1 s | O | viewer | S |
| **`get_runbook`** / `runbook_for_service` | bilgi | `runbooks` + `service_metadata` | ❌ | <1 s | D | viewer | S |
| **`search_knowledge`** | bilgi | `rag_chunks` lexical (→ embedding opsiyonel) | ❌ (kademe var) | <1 s | D | viewer | S/M |
| `list_teams` / `get_team_services` | katalog | `service_metadata` + aliases | ✅ | <1 s | D | viewer | — |
| `render_chart` | render | span metrik batch | ✅ | <1 s | D | viewer | (Faz 3'te `chart` bloğuna) |
| `build_link` / `deep_link` | render | FE rota sözleşmesi | ✅ | 0 | D | viewer | — |
| **`get_capabilities`** | meta | bayraklar (entity/rollouts/VM/Thanos/RAG/model) | ❌ | 0 | D | viewer | S |
| `get/set/clear_context` | hafıza | Redis | ✅ | 0 | D | viewer | (Faz 3'te pin) |

Effort: S ≤ 2 s, M ≤ yarım gün. Katalogdaki 47 tool'un adı/şeması değişmez.

---

## 4. Birleşik `AgentRuntime` — paket yapısı ve göç planı

`internal/ai/` zaten var (`provider`, `assemble`, `insight`); yeni kök açmak yerine
altına yerleşir. `internal/api/api.go`'ya **dokunulmaz**; rotalar
`internal/api/agent_routes.go` `registerAgentRoutes(mux)` (+ `route_registry.go`
`init()` kaydı) ile gelir.

```
internal/ai/agent/
  runtime.go        AgentRuntime.Run(ctx, Turn) → BlockStream
                    — deadline (chat_deadline), bütçe (budget.Reserve), span (ai.agent →
                      ai.tool/ai.llm), ai_calls kaydı, shield, exchange id: TEK yerde
  turn.go           Turn{Surface, Principal(+Scope), PageContext, Pinned, Messages,
                    Subject, Profile, Mode: chat|explain|insight}
  router.go         kademe seçimi (guided/drawer/RAG/intent/loop) — mevcut sıra,
                    ilk kez TESTLE pinli (bugün yalnız fiziksel sıra, copilot-chat §0)
  context/          PageContext tipi + Assembler: ekran bağlamı önsözü
                    (chat_screen_context.go buraya), Redis çalışma seti
                    (chat_context.go), pin; kullanıcı izolasyon testi (v0.10.487)
  tools/            Registry = mcptools.ToolList + dış MCP köprüsü (chat_mcp_bridge);
                    Scope enjeksiyonu; bütçe (chat_tool_budget); tekrar muhafızı;
                    audit; <data> sarmalama; ai.tool span'ı — tool YÜRÜTME tek yol
  blocks/           Block tipleri (text/table/chart/trace_list/link/action/evidence),
                    SSE Emitter (sseEmitter + chat emit → tek gövde, heartbeat, mutex,
                    step id), eski answer/delta çerçeveleriyle paralel yayın
  llm/              copilot.Service üstünde ince cephe: profil çözümü, model
                    yetenek tablosu (capabilities.go: thinking/completionBudget/
                    temperature/json/stream), tek attribution kurucu (7 sarmalayıcı → 1)
  adapters/
    chat.go         chatRequest → Turn; kademeleri runtime.Router'a taşır
    explain.go      explain-<kind> rotaları → Turn{Mode: explain}; deliverExplain →
                    blocks.Emitter; cache anahtarı aynen
    insight.go      /api/insight/* ve problem_explainer → Turn{Mode: insight}
internal/api/agent_routes.go   POST /api/agent/turn (SSE, blok protokolü),
                               GET /api/agent/capabilities; eski rotalar adaptörlere
                               delege eder (URL değişmez)
frontend/src/lib/pageContext.ts       pageContext(pathname, search) saf serileştirici
                                      (contextParams.readScopeParams + rota tablosu;
                                      chatContext.serviceFromRoute buraya katlanır) +
                                      rota kapsama testi (App.tsx rota listesi × tablo)
frontend/src/lib/pageContextBus.ts    usePageContextPublisher(): sayfa yalnız URL-dışı
                                      alanları yayınlar (visibleColumns, chartSelection)
frontend/src/components/ai/blocks/    BlockRenderer + TextBlock/TableBlock/ChartBlock/
                                      TraceListBlock/ActionBlock/EvidenceBlock
frontend/src/lib/pageActions.ts       applyPageAction(action): sayfa kodeği adaptörleri
                                      (FilterExpr / LogFilter / range / drawer)
```

**Göç ilkesi — strangler, davranış sabit:** eski handler'lar kalır ve adaptöre delege
eder; `chat_golden_test.go` + SSE çerçeve parite testleri + evalset replay her dilimde
koşar; yüzey başına bayrak `ai_copilot.agentV2{chat,explain,insight}` (system_settings,
varsayılan kapalı) — kapalıyken eski kod yolu birebir.

---

## 5. Faz planı

### Faz 2 — AgentRuntime + tool registry + üç yüzeyin göçü (davranış değişmeden)

**Durum (2026-09-07):** 2.1 GEMİDE v0.10.533 (aiCall tek attribution kurucu, 7 → 1);
2.1b v0.10.534 (`internal/ai/modelcaps` + profil `thinking`, `Request.ExtraBody`);
2.2 v0.10.535 (`internal/ai/agent/blocks.Emitter`, sohbet/explain/insight tek SSE
yazımı); 2.3 v0.10.536 (`internal/ai/agent/tools.Executor`, Scope kısıtsız);
2.6 v0.10.537 (👍/👎 tek atom, AIAnalysisPanel iptal). **Düzeltme:** madde 4'ün
öncülü bayat — kademe sırası v0.10.30'dan beri kaynak-pinli
(`chat_tier_order_test.go`); Router/Turn kabuğu davranış getirmeyeceği için
Faz 3'e katlandı (PageContext ile birlikte). Madde 5 (adaptörler/bayrak): ortak
katmanlar (aiCall/Emitter/Executor) üç yüzeyi zaten besliyor; ayrı adaptör
dosyası ve `agentV2` bayrağı Faz 3'te blok protokolüyle gelir.
1. `llm/`: tek attribution kurucu (7 → 1), model yetenek tablosu + profil ezmesi
   (Qwen3 thinking/`enable_thinking`, completion bütçesi model-farkında).
2. `blocks/`: tek SSE emitter; eski `delta/answer/done` çerçeveleri aynen (parite testi).
3. `tools/`: ToolList + köprü + bütçe + audit + span tek yürütücü; Scope arayüzü boş
   (kısıtsız) — G13 kararı gelince doldurulur.
4. `runtime.go` + `router.go`: kademe sırası ilk kez testle pinli.
5. Adaptörler: sohbet → explain → insight; her biri kendi bayrağıyla, golden/evalset
   yeşil; `AIAnalysisPanel` iptal + Türkçe hata tek sözlükten.
6. FE: üç elle yazılmış thumbs → `AIFeedbackButtons`; hata metni tek kaynak.
Çıkış ölçütü: `ai_calls`/span'larda surface dağılımı değişmez; evalset 6+13 vaka yeşil;
`copilot_chat.go` ≤ 300 satır, `ai_observability.go` sarmalayıcıları silinmiş.
Tahmin: ~2 gün (5 sürüm).

### Faz 3 — Sayfa bağlamı protokolü + render sözleşmesi
1. `pageContext.ts` (saf) + rota kapsama testi + `usePageContextPublisher` (kolon/seri).
2. Sunucu `PageContext` tipi; önsöz genişler (page/cluster/namespace/problem_id/filters);
   pin (thread'de, çip). Kabul 1 tüm sayfalarda.
3. Blok protokolü: `block` olayı + `BlockRenderer`; `render_chart` → `chart{from,to}`
   (now-çapası kalkar, insight kartı çizer); `table` DataTable eşiği; `evidence` çipleri.
4. `action` bloğu + `applyPageAction` (Traces/Logs/Explore/Problems kodek adaptörleri).
   Kabul 6.
5. `chart.compare` (dün aynı saat) — `query_metric` compare ile. Kabul 2.
Tahmin: ~2 gün.

### Faz 4 — Sinyal genişletme
1. `list_changes` (deploy+rollout+event, ns/pencere) + `get_rollout` — kabul 3'ün kısa yolu.
2. `get_problem`, `get_correlation_evidence`, `similar_problems` (symptom anahtarı:
   rule + servis + hipotez `rc_fail_mode`).
3. `log_patterns`, `workloads_for_service`, `cluster_metric`, `external_series`,
   `get_capabilities`.
4. `query_metric_expr` (kapılı gramer) — ölçümle: 100 prod sorusu üzerinde parametrik
   yetmeyen oran %10'un altındaysa ertelenir.
5. Guided "neden arttı" rotası: trace + log desen + metrik + `list_changes` prefetch →
   hipotez sıralaması → `evidence` bloğu (küçük model için prefetch+narrate). Kabul 3.
Tahmin: ~2 gün.

### Faz 5 — Gömülü insight'lar + bilgi tabanı + feedback
1. Deterministik insight başlığı (`deep_evidence` içine; DDL yok) + Problem sayfasında
   kart; anlatı yalnız critical (mevcut explainer). Kabul 4.
2. `get_runbook` / `search_knowledge` (lexical) + `sources[]` zorunlu + RAG chunk maskesi.
   Kabul 5.
3. Feedback döngüsü: sohbette yorum, arşiv turn'lerine exchangeId, haftalık `/ai`
   raporu (surface × prompt_version × tool), 👎 → evalset aday akışı. Kabul 7.
4. Kullanıcı başına token tavanı (`budget.Reserve`), self-obs kör noktaları (VM/Thanos/
   ES istemci span'ları).
Tahmin: ~2 gün.

---

## 6. Riskler ve geri alma

| Risk | Etki | Önlem / geri alma |
|---|---|---|
| Küçük lokal model (gemma4) çok-turlu tool döngüsünü sürükleyemez | kabul 3 boş cevap | Guided prefetch+narrate rotaları runtime içinde kalır (`router.go`); tool döngüsü son çare; evalset her fazda |
| 5.600 satırlık kademelerin göçünde sessiz davranış kayması | yanlış sayı, yanlış kapsam | golden + SSE parite + evalset; yüzey başına bayrak; eski yol bir sürüm daha kalır — bayrağı kapat = geri al, DDL yok |
| Blok protokolü FE'de büyük patlama | çekmece boş/kırık | `block` olayları eski `answer.text` ile **paralel** yayımlanır; FE bayrağı kapalıyken eski render |
| Bağlam sızıntısı (pin / Redis) | başka kullanıcının servisi | v0.10.487 izolasyon testleri Turn/Assembler'a taşınır; pin thread'e yazılır, Redis'e değil |
| `action` bloğu ile injection | telemetriden gelen "filtreyi sil" | aksiyon yalnız tool sonucundan; model metni aksiyon üretemez (tasarım gereği kapalı kanal) |
| Scope kararı (G13) gecikir | agent kapsamsız kalır | Scope arayüzü boş-kısıtsız çıkar; karar gelince tek dosya; prompt'a bağımlılık yok |
| Yeni tool'larla maliyet artışı | ES/VM yükü | turn bütçesi, `serveCached` anahtarları, kullanıcı tavanı; `list_changes`/`log_patterns` MV/örneklem tabanlı |
| Rollouts/entity bayrakları prod'da kapalı | tool'lar `disabled` | `get_capabilities` + guided rotalar bayrağı görüp ucuz yola düşer; prod bayrak kararı operatörde |
| `saved_views(page='ai-chat')` sınırsız birikim | tablo şişmesi | Faz 5'te süpürücü (90 g, `created_at`), DDL yok |

**Geri alma:** her faz yüzey bayrağı + eski handler; şema değişikliği yalnız Faz 5'te
`ai_budget` blob alanı (system_settings) ve `deep_evidence` JSON içi — hiçbiri DDL
değil. Bayrak kapatınca v0.10.532 davranışı birebir.

---

## Ek — görevdeki varsayımların düzeltmeleri
- "Üç bağımsız yüzey": AIDrawer→CopilotChat birleşti (v0.10.461/483); üçüncü öğe
  arka plan/insight tüketicileri + `AIAnalysisPanel`.
- "`conversation_state` v1 kararı": kodda `saved_views(page='ai-chat')` + Redis ctx.
- "Log yalnız trace_id ile mi": serbest metin `search_logs` zaten var.
- "Sayfa bağlamı yok": kısmen var (v0.10.32 ekran bağlamı, 6 alan); protokol genişletilir.
- "Maskeleme agent'ta da kalmalı": binary'de maskeleme yok (operatör tercihi); agent
  yalnız depolananı görür; risk kod/RAG tarafında.
- "<measurement>": genel dış-metrik yolu; trace_id pivotu enrich'te var.
