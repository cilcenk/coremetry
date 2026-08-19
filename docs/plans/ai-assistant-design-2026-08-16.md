# Coremetry AI Assistant — Audit + Tasarım (2026-08-16)

Hedef: Elastic AI Assistant (Observability) muadili yetenek seti —
(1) gömülü contextual insight kartları, (2) telemetri üzerinde
tool-calling'li chat (metin + veri + chart), (3) knowledge base,
(4) öğrenme döngüsü. Bu doküman üç paralel kod audit'inin (backend
LLM katmanı · MCP/tool/yetki · frontend AI yüzeyleri, HEAD
`b3565cf9` / v0.9.1114) sentezi ve fazlı uygulama önerisidir.
**Onaysız kod yazılmayacak.** Dosya:satır atıfları audit anındaki
HEAD'e göredir; uygulama öncesi doğrulanır.

---

## 1. Mevcut durum ve duplikasyon haritası

### 1.1 AI yüzey envanteri

**Bir-atımlık ✨ Explain uçları (21):** tamamı `POST`, tamamı
buffered JSON — **hiçbiri stream etmiyor**. Tamamı `api.go:1144-1178`
bandında inline kayıtlı (yalnız birkaçı ayrı handler dosyasında).

| Uç | Handler | Prompt | Wrapper | ai_calls.surface |
|---|---|---|---|---|
| `/api/copilot/explain-trace/{id}` | api.go:8908 | SystemPromptTrace(+WithCode) | copilotExplain(+Code) | explain-trace |
| `/api/copilot/explain-span/{traceId}` | api.go:8952 | SystemPromptSpan | copilotExplain | explain-span |
| `/api/copilot/explain-problem/{id}` | api.go:9057 | SystemPromptProblem | copilotExplain | explain-problem |
| `/api/copilot/explain-incident/{id}` | api.go:9183 | SystemPromptIncident | copilotExplain | explain-incident |
| `/api/copilot/explain-anomaly/{id}` | api.go:9242 | SystemPromptAnomaly | copilotExplain | explain-anomaly |
| `/api/copilot/explain-service` | api.go:9327 | SystemPromptServiceHealth | copilotExplain | explain-service |
| `/api/copilot/explain-charts` | copilot_service_charts.go:32 | SystemPromptServiceCharts | copilotExplain | explain-charts |
| `/api/copilot/explain-shift` | shift_page.go:123 | SystemPromptShiftSummary | copilotExplain | explain-shift |
| `/api/copilot/explain-alert-noise` | alert_noise_explain.go:44 | SystemPromptAlertNoise | copilotExplain | explain-alert-noise |
| `/api/copilot/explain-log-patterns` | log_patterns_explain.go:31 | SystemPromptLogPatterns | copilotExplain | explain-log-patterns |
| `/api/copilot/runbook/{id}` | api.go:9468 | SystemPromptRunbook | copilotExplain | runbook |
| `/api/copilot/compare-traces` | api.go:9561 | SystemPromptCompareTraces | copilotExplain | compare-traces |
| `/api/copilot/deploy-impact` | api.go:9620 | SystemPromptDeployImpact | copilotExplain | deploy-impact |
| `/api/copilot/explain-slo/{id}` | api.go:9783 | SystemPromptSLOBurn | copilotExplain | explain-slo |
| `/api/copilot/explain-slow-query` | api.go:9854 | SystemPromptSlowQuery | copilotExplain | explain-slow-query |
| `/api/copilot/explain-exception/{fp}` | copilot_exception.go:21 | SystemPromptException(+WithCode) | copilotExplain(+Code) | explain-exception |
| `/api/copilot/suggest-service-tags` | api.go:10087 | SystemPromptServiceTags | copilotExplainJSON | suggest-service-tags |
| `/api/copilot/nl-to-query` | copilot_nl_query.go:153 | SystemPromptNLToQuery | copilotExplainJSON+şema | nl-to-query |
| `/api/copilot/analyze-service` | copilot_aianalyze.go:174 | serviceAnalysisPrompt (LOCAL const!) | copilotExplainJSON+şema | analyze-service |
| `/api/admin/clickhouse/optimize-query` | copilot_ch_optimize.go:60 | SystemPromptCHQueryOptimize | copilotExplainJSON+şema | ch-optimize (özel durum, ai_observability.go:228) |
| `GET /api/copilot/config` | api.go:8815 | — | — | — |

**Rootcause anlatısı (copilot namespace dışında):**
`GET /api/{problems|anomalies}/{id}/rootcause/explain`
(rootcause.go:615/607) → `SystemPromptRCAVerdict` +
`rcaVerdictSchema(entities, rivals)` enum-kısıtı →
`copilotExplainJSONSurface`, surface=`rootcause-verdict`. 10dk cache,
anahtar `rootcause-explain:{kind}:{id}:{hypothesisVersion}`.
`SystemPromptRootCauseNarration` (copilot.go:1948) **ölü kod**
(v0.9.559'dan beri tüketicisiz).

**Chat (`POST /api/copilot/chat`, copilot_chat.go:94)** — SSE, 4
kademeli dispatch, sıra sabit:

| Kademe | Kod | Stream | surface |
|---|---|---|---|
| 1. Guided router (16 intent, prefetch + TEK tool-suz çağrı) | copilot_guided.go:882→1040 | ✅ delta | chat-guided |
| 2. AI-drawer chat (explain cevabına dayalı) | copilot_drawer.go:398→419 | ✅ | chat-drawer |
| 3. RAG doküman yolu | rag.go:312→371 | ✅ | rag-chat |
| 4. Serbest tool loop (5 tur × 19 tool) | copilot_chat.go:234/294 | ❌ (yalnız step/answer) | chat |

**Arka plan (HTTP'siz) tüketiciler:** ProblemExplainer
(anomaly/problem_explainer.go:212, surface=problem-auto-explain) +
ExceptionExplainer (exception_explainer.go:117,
exception-auto-explain). İkisi de `copilot.Explain`'i **doğrudan**
çağırıp CallMeta'yı elle kuruyor (wrapper sözleşmesinin 3. kopyası).

**MCP prompts (mcptools/prompts.go):** 7 sistem prompt'u
`prompts/get` ile dış istemciye verilir — LLM çağrısı YOK,
**ai_calls kaydı YOK** (kör nokta).

### 1.2 Provider katmanı (bugün)

- **Yapı:** `copilot.Service` (copilot.go:49-128, RWMutex'li).
  Provider enum: `anthropic | github | openai` (openai = tüm
  OpenAI-compat self-hosted yol: vLLM/KServe/Ollama).
- **Config kaynağı:** env `COREMETRY_AI_{PROVIDER,API_KEY,MODEL,BASE_URL}`
  → `system_settings["ai_copilot"]` overlay (DB kazanır) → 30s
  `StartConfigRefresh` çok-pod senkronu + PUT'ta `publishConfigReload("ai")`.
- **Timeout:** tek değer **180s**, `buildCopilotHTTPClient`
  (copilot.go:286-296) — Ollama 70B soğuk yükleme gerekçeli.
  **Konfigüre edilemez.** RAG'ın AYRI istemcisi 30s (rag.go:66).
- **Retry:** transport hatasında YOK. Semantik fallback'ler var:
  json_schema→json_object→serbest merdiveni (copilot.go:768-786,
  provider-başına verdict cache'li); stream→buffered tek düşüş
  (stream.go:457-500); kod-bloğu yarılama (copilot_code.go:158-175).
- **Kota kesici:** 429/kota hatası 1 saatlik pencere kurar
  (copilot.go:434-457); arka plan tüketiciler atlar, interaktif yol umursamaz.
- **max_tokens / temperature haritası (hard-coded):**

| Yol | max_tokens | temp |
|---|---|---|
| Explain openai-compat (copilot.go:702) | 4096 | 0.2 |
| Explain anthropic (copilot.go:891) | **1024** ⚠ | — |
| Explain github (copilot.go:969) | **1024** ⚠ | 0.2 |
| Chat tools her iki yol (chat.go:191/302) | 4096 | —/0.2 |
| Stream openai-compat (stream.go:434) | 4096 | 0.2 |
| Stream anthropic (stream.go:530) | **1024** ⚠ | — |

v0.8.138/393 bütçe terfisi anthropic/github/anthropic-stream'e hiç
uygulanmamış. Hiçbir değer operatör-ayarlı değil.

- **Thinking/reasoning boş-içerik zinciri** (v0.8.138→155→384
  olaylarının kalıcı dersi, `parseOpenAIChatResponse`,
  copilot.go:797-855): `stripThinking(content)` →
  `reasoning_content` → `reasoning` (vLLM ≥0.24) → `<think>` içi →
  hepsi boşsa ham şekil loglanır ve `finish_reason=length` ise
  operatöre eyleme dönük mesaj ("raise max_tokens or disable
  thinking / Qwen3 /no_think"). Aynı zincir stream.go:313 ve kısmen
  chat.go:355'te **tekrar** yazılmış.
- **Streaming:** `classifyStreamResponse` 4-verdict fallback
  merdiveni (stream/parse-buffered/fallback-cache/fallback-once).
  GitHub yolunun stream ikizi yok (sessiz buffered). Serbest tool
  loop bilinçli stream'siz.

### 1.3 Tool katmanı (bugün)

**`mcptools.ToolList` = tek kayıt defteri** (tools.go:99-107 gerekçe
yorumu; tüketiciler yalnız `main.go:1134` MCP server + `copilot_chat.go:198`
in-app). 19 tool, tamamı read-only; `Deps{Store, LogStore}` closure'ı
— **kimlik/yetki taşımıyor**.

Konvansiyonlar (tools_test.go pinli): `range_s` (sn, varsayılan 1800,
tavan 7g) — LLM'ler ns'de kötü; `clampLimit(req,def,max)` bantları;
her property'de description zorunlu; opsiyonel arg'lar additive;
env-siz tool seti bilinçli (filtre vaadi veremeyecek okuma property
taşımaz); dürüstlük zarfları tool ÇIKTISININ içinde (`truncated`,
`has_more`, `partial`, `degraded`, `computed:false`, `found:false`).
`get_trace` 200-span yapısal cap'i hata-önce-yavaş-sonra seçimiyle.
`render_chart` deseni: **model seçer, sunucu doğrular ve render eder**
— fence, modelin ham arg'larından değil doğrulanmış çıktıdan üretilir.

Transport: Streamable-HTTP (stateless, birincil, v0.9.14) + legacy
SSE (pod-lokal session → helm'de ClientIP affinity zorunluluğu;
streamable bunu bitirdi). 4MB gövde sınırı; SSE yolunda 2s
sendTimeout'lu **cevap-kaybı** riski var (streamable'da yok).

**Kaynak (resources) yan kapısı:** `coremetry://trace/{id}` tool'daki
200-span cap'i olmadan `GetTrace`'i sınırsız döndürür; resources/read
+ prompts/get rate-gate'ten de geçmez.

### 1.4 Guided router — 16 intent ve MCP eşleşmesi

| Intent | MCP karşılığı |
|---|---|
| problems / service_health / slow_traces / trace_by_id / deploy_impact | list_problems / get_service_health / search_traces / get_trace / get_deploy_diff — **birebir aynı store çağrıları, farklı format** |
| root_cause | get_problem_root_cause + kompozit |
| family_health / log_errors / my_exceptions | kısmi (aile filtresi, histogram, takım kapsamı MCP'de yok) |
| **span_by_id / my_services / my_problems / pod_health / shift_summary / db_health / messaging_health** | **YOK** — in-app zengin, MCP fakir; drift'in asıl yaşadığı yer |

`my_*` intent'leri codebase'in TEK kullanıcı-bazlı veri kapsamlaması
(session→UserID→team→servis listesi, copilot_guided.go:1375).

### 1.5 RAG / KB mevcut durumu

Uçtan uca gemide (v0.8.438+): `internal/rag/` (embed istemcisi:
OpenAI-compat `/v1/embeddings`, batch 64; chunker; PDF; URL/wiki
crawler sha256 diff'li + 30dk leader-tick) → `rag_chunks`
(ReplacingMergeTree(version) ORDER BY (doc_id, chunk_idx),
`embedding Array(Float32)`, ZSTD içerik) → `TopKRagChunks`
(cosineDistance brute-force, ~100k chunk bütçe notu) + **BM25 kelime
köprüsü** `TopKRagChunksByContent` (TR+EN stopword, whole-token,
"precision > recall, recall'u bge-m3 çözer") → `ragChatAnswer`
(stream'li, kaynak çipleri). Ayarlar `system_settings["rag_embedding"]`
+ Settings→Knowledge sekmesi. **Blokör:** air-gapped hedefte
`/v1/embeddings` (bge-m3) yok → `Ready()` false → semantik yol
kapalı, keyword yolu çalışıyor; ingest text-only devam ediyor
(`embedOrTextOnly`). Retrieval önceliği: semantik(floor) →
keyword(floor) → tool loop'a düşüş.

### 1.6 Anomali / correlator kanıt zinciri

```
dedektörler → gatherEvidenceInputs (tick başına 5 bounded okuma)
  → buildEvidenceBundle (CoFiring/Signals/Deploy/Neighbors/Confidence)
  → investigationPlan → gatherDeepEvidence (6 aile: exceptions, logs,
    saturation, runtime, operations, business; aile başına 1 okuma)
  → enrichDeployImpact + RankRootCausesFromEdges → Synthesize
  → RootCauseHypothesis (root_cause_hypotheses RMT)
```

LLM'e bugün giren: hipotez bloğu (HypothesisPromptBlockTR), fusion
kanıtı (renderEvidence), DeepEvidence prose'u (renderDeepEvidence) +
verdict yolunda **E/N kanıt kataloğu** (pozitif/negatif ID uzayları,
Found=false whitelist'e girmez) + shields (filterEvidenceIDs,
capRCAConfidence, checkRCAEntities — model kataloğun dışına çıkarsa
kırpılır ve rapora düşer) + yalnız 👍'lı geçmiş RCA'lar
(`ConfirmedRCASignatures`, prompt'un en sonunda).

**Hesaplanıp HİÇ anlatılmayan:** `/rootcause` fan-out'unun
Correlations (MV, v0.9.1062) · BlastRadius · BubbleUp · Exemplar ·
yapılandırılmış DeployImpact (yalnız tek cümlelik Reason giriyor) ·
DeepEvidence.Runtime (renderDeepEvidence'ta branşı YOK) ·
NeighborSignals (yalnız Confidence sayacına katılıyor). Ayrıca
renderDeepEvidence heap yüzdesi `s.Limit` sıfır-guard'sız
(investigation.go:394, +Inf/NaN şüphesi — uygulamada doğrulanacak).

### 1.7 Frontend envanteri

Üç mimari kat:
1. **Global chat** `CopilotChat` (FAB, backdrop'suz Drawer,
   480↔1100px) — SSE frame parser (`copilotChat`, api.ts:1490-1569;
   EventSource değil, POST + ReadableStream). Event sözleşmesi:
   `step{tool,args} · delta{text} · answer{text,exchangeId,sources[],suggestions[],links[]} · error · done`.
2. **AI Drawer** (`?ai=<kind>:<id>` codec'i, 9 kind; AIDrawer +
   CopilotExplain; "💬 Chat'te devam et" AIDrawer.tsx:236; "Yeniden
   sor" CopilotExplain.tsx:202) — explain gövdesi buffered, drawer-içi
   chat stream'li.
3. **Inline tek-atım paneller** (Shift/Logs/SlowQueries/NoisyRules/
   DeployHistory/Slos/Trace-compare/AIAnalysisPanel/RootCauseRibbon)
   — her biri kendi state makinesi.

Paylaşılan çekirdek: `useChatThread` (tek chat döngüsü) + `ChatBubble`
(tek balon renderer'ı: mdLite bold/code, 32-hex trace-id
linkleştirme, ```chart fence → `CosreChart`, ⚙ tool-adı çipleri,
📄 RAG kaynak çipleri, 🔗 sunucu-üretimi deep-link çipleri, ↳
öneriler, 👍/👎). **Tablo/başlık/liste render'ı YOK**; tool
SONUÇLARI görünmez (yalnız ad); üç yüzey (Shift/Logs/NoisyRules) ham
`pre-wrap` basıyor (literal `**`).

Chart: `CosreChart` spec-only desen (mesaj `{service,agg,rangeS}`
taşır, istemci fetch'ler — model asla veri noktası taşımaz). Genel
sistemde `CorePanel` **data-prop'lu** saf sunum bileşeni
(`PanelData` 4-durum union'ı) + lazy `corePanelEntry`
(`CorePanelWithFrames`) hazır; `corePanelMonopoly` testi UPlotChart'ı
CorePanel'e kilitler. `streamNDJSON` (lib/perf/streaming.ts) yazılmış
ama **sıfır tüketicili** hazır primitive.

Feedback: `POST /api/ai/feedback {exchangeId, verdict:±1}` — 3 çağrı
noktası (ChatBubble/RCAVerdictPanel/AIAnalysisPanel), optimistic +
rollback, `exchangeId` yoksa affordance HİÇ çizilmez
(canRateVerdict, vitest-pinli). Okuma tarafı /ai: RCAQualityPanel +
NegativeFeedbackPanel.

Contextual-card emsalleri (yakınlık sırasıyla):
`ServiceChartsExplainBody` (prose ⊥ typed signals ⊥ sunucu-link ⊥
dürüstlük altbilgisi + staleTime:Infinity disiplinleri) →
`RootCauseRibbon` (collapsed sıfır-fetch, expand fetch-once, ✨
ikinci opt-in) → `AIDrawer`+`Drawer` (Esc-katmanı, `?ai=`
paylaşılabilirlik) → Inbox `AISummaryLine` (worker-yazımı pasif satır
içi özet).

SSE state-bus (ayrı konu ama komşu): `useEventStream` tek-lider
(Web Locks + BroadcastChannel), 4 event türü, saf invalidation
haritası — AI ile ilgisi yok ama SSE altyapı deneyimi kanıtlı.

### 1.8 Duplikasyon haritası (konsolidasyon hedefleri)

| # | Duplikasyon | Kanıt | Maliyet |
|---|---|---|---|
| D1 | **8 bağımsız LLM istek gövdesi, 2 HTTP istemcisi** (copilot 180s + rag 30s); OpenAI-compat gövdesi 4, auth-header bloğu 3 kopya | copilot.go:687/882/956, chat.go:148/248, stream.go:417/516, rag.go:180 | Her provider düzeltmesi 3-8 yere |
| D2 | max_tokens 1024/4096 tutarsızlığı; hiçbir LLM parametresi ayar değil | §1.2 tablo | Reasoning kesilmesi + operatör müdahalesizliği |
| D3 | 6 wrapper varyantı; yalnız JSON varyantı ExchangeID taşıyor → **15 prose yüzeyi 👍/👎 alamıyor** | ai_observability.go:95-394 | Feedback döngüsü kör |
| D4 | 503 ön-kapısı 17 kopya, 3 mesaj şekli (3. regresyon v0.9.1101) | §1.1 | Her yeni yüzey regresyon adayı |
| D5 | Prompt sahipliği bölünmüş: 24 const copilot.go + 4 api local'i; dil-pinleme testi yarıyı görüyor | copilot_guided.go:860 vd. | Prompt disiplini kaçak |
| D6 | Guided = fiili 3. tool yüzeyi (5 intent birebir kopya, 7'si MCP'de yok) | §1.4 | Drift'in evi |
| D7 | Embedding + MCP prompt kullanımı ai_calls'a düşmüyor | rag.go, prompts.go | /ai eksik maliyet |
| D8 | BubbleUp/Blast/Correlations/DeployImpact(yapısal)/Runtime/NeighborSignals hiç anlatılmıyor | §1.6 | Davis hammaddesi masada |
| D9 | api.go 12107 satır / 378 route; AI'nın 32 route'u inline (desen 14 yerde kanıtlı, RAG tek çıkmış örnek) | api.go | Büyüme yasağı fiilen ihlalde |

### 1.9 Yetki gerçeği

- `mcp.go:27-33` + `api.go:196-198` "roller MCP'ye aynen uygulanır"
  → **mekanizma olarak YANLIŞ**: MCP route'ları RequireRole'süz
  (api.go:616-624), mcptools auth import etmiyor, store katmanı
  tamamen yetki-bilinçsiz. Somut sapma: `GET /api/anomalies/active`
  editor-gated; `list_anomalies` aynı satırları viewer'a veriyor.
- Kimlik ctx'te akıyor: `auth.FromContext` → `Claims{UserID,Email,Role}`;
  cmk_ token = `token:<id>` servis kimliği (rol var, kullanıcı yok).
  JWT yolu canlı-authz'lı (10s TTL, CH hatasında bilinçli fail-open).
- **`ToolCallGate` kancası doğru yerde** (tool çözümü sonrası,
  handler öncesi; hata JSON-RPC -32000 = model okuyabilir) ama bugün
  yalnız 60/dk rate sayıyor (mcp_gate.go:31-40, Role'ü hiç okumuyor).
  `resources/read` + `prompts/get` gate'ten GEÇMİYOR.
- Custom roller uygulama genelinde frontend-only (custom_roles.go:13-19)
  — AI'dan büyük, ayrı konu (Açık soru A6).

---

## 2. Hedef mimari

### 2.1 Paket ağacı ve sorumluluklar

```
internal/ai/                      ← şemsiye; big-bang taşıma YOK, faz faz dolar
  provider/
    client.go       Provider interface + doLLM(ctx, Request) (Response|Stream)
                    Request{Model, MaxTokens, Temperature, Stream, JSONLevel,
                            Messages, Tools []ToolSpec}
                    — TÜM yüzeyler (explain/chat/verdict/embed) buradan geçer
    openai.go       OpenAI-compat gövde+header üretici (vLLM api-key twin,
    anthropic.go    Gemini Raw-replay quirk'leri BURADA pinlenir)
    github.go
    salvage.go      stripThinking → reasoning_content → reasoning → think-içi
                    → finish_reason teşhisi — TEK kopya (bugün 3)
    stream.go       scanSSE + accum + classifyStreamResponse merdiveni (taşınır)
    embed.go        /v1/embeddings — rag'ın ayrı istemcisi buraya iner,
                    Recorder'a bağlanır (D7 kapanır)
    config.go       ai_copilot blob'u genişler: {maxTokens, temperature,
                    timeoutS, streamEnabled} — provider-bağımsız TEK kaynak
    prompts.go      24+4 prompt const'u TEK dosyada; dil-pinleme testi tam kapsar
    recorder.go     ai_calls Recorder (CallMeta tek kurucu; arka plan
                    tüketicilerin elle-kurma kopyası ölür)
  assemble/
    budget.go       Assemble(ctx, Budget, []Source) → []Message
                    Source{Tier, Render(maxLines) (text, truncated)}
    renderers.go    hipotez/deep/bubbleup/blast/corr/kb/geçmiş — saf
  kb/               internal/rag buraya evrilir (retriever+ingest+crawler);
                    davranış değişmez, embed istemcisi provider/embed'e
  insight/
    contract.go     InsightResponse{Prose, Signals []TypedSignal,
                    Links []TypedLink, Charts []ChartSpec, ExchangeID,
                    Truncated, Model} — prose ⊥ deterministik ayrımı TİPTE

internal/mcptools/  YERİNDE (tek registry; isim/import churn yok)
    + Tool.MinRole  ("" = viewer) + yeni tool'lar (Faz 3 listesi)

internal/api/
    ai_routes.go    registerAIRoutes(mux) — 32 route + requireCopilot
                    middleware'i; api.go yalnız çağırır (KÜÇÜLÜR)
    (handler dosyaları yaşamaya devam eder)

internal/copilot/   DONDURULUR; Faz 1-2 boyunca içi provider/'a boşalır,
                    en sonunda tip alias'larıyla emekli edilir
```

### 2.2 Veri akışı

```
                        ┌────────────────────────────────────────────┐
  UI (kart/chat) ──SSE──┤ /api/copilot/chat + /api/copilot/insight/* │
                        └───────┬────────────────────────────────────┘
                                │ auth.Claims (ctx) — HER katmanda taşınır
                    ┌───────────▼───────────┐
                    │ guided router (16+)   │  eşleşme yoksa
                    │ prefetch → tek çağrı  │──────────┐
                    │ (bundle'lar Faz 3'te  │          │
                    │  tool handler ÇAĞIRIR)│    ┌─────▼─────────────┐
                    └───────────┬───────────┘    │ serbest tool loop │
                                │                │ mcptools.ToolList │
                    ┌───────────▼───────────┐    │ (role-filtreli    │
                    │ internal/ai/assemble  │    │  spec listesi)    │
                    │ bütçe + öncelik:      │    │ + ToolCallGate    │
                    │ 1 anchor konu         │    │   (rol + rate)    │
                    │ 2 hipotez + verdict   │    └─────┬─────────────┘
                    │ 3 DeepEvidence        │          │ chstore
                    │ 4 BubbleUp/Blast/Corr │          │ (MV-first,
                    │ 5 KB chunk'ları       │          │  bounded)
                    │ 6 konuşma geçmişi     │          │
                    └───────────┬───────────┘          │
                    ┌───────────▼──────────────────────▼──┐
                    │ internal/ai/provider — TEK transport │──► ai_calls
                    │ stream-first · salvage · fallback    │   (embed dahil)
                    └───────────┬──────────────────────────┘
                                │ SSE: step/delta/answer(+chart, kaynak, link)
                        UI: ChatBubble / InsightCard (AYNI renderer çekirdeği)
                                │ 👍/👎 + yorum ──► ai_feedback
                                │      └► ConfirmedRCASignatures (mevcut)
                                │      └► KB terfi kuyruğu (küratörlü, Faz 5)
```

### 2.3 Çekirdek sözleşmeler (taslak — uygulama değil)

**SSE tel sözleşmesi (mevcut, korunur ve explain'e genişler):**
`step{tool,args}` → `delta{text}`* → `answer{text, exchangeId,
sources[], suggestions[], links[], charts[]}` → `done{ok}` |
`error{error}`. `answer` delta'ların YERİNE geçer (FE'de kanıtlı).
Yeni: `answer.charts[]` = doğrulanmış ChartSpec listesi (fence'in
tipli hâli; fence geriye-uyum için kalır).

**InsightCard sözleşmesi:** kart = `InsightResponse` tüketicisi.
Prose boş/halüsinasyon olsa bile Signals/Links deterministik render
edilir ve UI bunu söyler (ServiceChartsExplainBody:111-118 emsali).
Kart durumları: `collapsed(sıfır-fetch)` → `expanded(deterministik
sinyaller anında, prose stream'le dolar)` → `feedback`. Linkler
DAİMA sunucu-üretimi + pencere taşır (pivotHref disiplini).

**Assemble bütçesi:** girdi `Budget{MaxChars}` (modelden bağımsız,
konservatif; token≈chars/3 kabulü ayarda). Tier'lar sırayla eklenir;
taşan tier `Render(maxLines)`'la kırpılır ve `[kırpıldı: N/M]`
işareti prompt'a girer (dürüstlük zarfının prompt-içi hâli). Saf,
table-driven testli.

**Yetki sözleşmesi:** `gate(ctx, tool)` → `Claims.Role >= Tool.MinRole`
değilse JSON-RPC hatası ("bu tool <rol> gerektirir"). In-app spec
listesi role göre ÖNCEDEN filtrelenir (reddetmek yerine gizlemek —
model boşa tur yakmaz). resources/read + prompts/get aynı gate'e
bağlanır. cmk_ token rolü neyse o (kullanıcı-bazlı değil; bilinçli
sınır, runbook'a yazılır).

---

## 3. Karar tablosu

### K1 — Tool katmanı: MCP tek kaynak mı, ayrı registry mi?

| Seçenek | Artı | Eksi |
|---|---|---|
| **(a) `mcptools.ToolList` tek kaynak; guided-only yetenekler tool'laşır; MinRole + gate** ✅ | Zaten gemide, drift önleme 2 tüketicide kanıtlı; tek test yüzeyi; MCP dış istemcileri bedava zenginleşir; şema disiplin testleri hazır | JSON Schema seremonisi iç kullanım için hafif yük; MCP sürümleme dışa sızar |
| (b) Backend'de ayrı registry, MCP projeksiyon | Go-native iç tipler | Bugünün TERSİ: iki defter = geri gelen drift; 19 tool + test taşıma maliyeti; kazanım spekülatif |
| (c) Hibrit (guided bundle'ları "yerel tool" say) | Küçük modele verimli | 3. yüzeyi resmileştirir; MCP fakir kalır |

**Öneri (a).** "MCP'yi tek kaynak yapmanın maliyeti" sorusunun
cevabı: **sıfıra yakın, çünkü zaten tek kaynak** — asıl iş guided'ın
7 özel yeteneğini tool'a indirmek ve keşif tool'larını eklemek
(v0.9.1087-1092 "kimlik halüsinasyonunu öldür" dalgasının devamı:
bugün 5 tool `env` arg'ı kabul edip geçerli env listesi vermiyor,
`render_chart` operation kabul edip operation kataloğu yok — birebir
`list_metric_names`'in çözdüğü sınıf). Guided router KALIR
(küçük-model gerçeği: prefetch+narrate > tool-loop; 2B'de 5-tur loop
güvenilmez) ama Faz 3'te bundle'lar tool handler'larını çağırarak
beslenir → tek veri şekli, çift format ölür (D6).

### K2 — Knowledge base storage

| Seçenek | Artı | Eksi |
|---|---|---|
| **(a) CH `rag_chunks` (mevcut) + self-hosted bge-m3, BM25 fallback** ✅ | GEMİDE; single-binary korunur; cosine ~100k chunk'a kadar bütçede (kod yorumu), sonrası CH vector index (ANN) yolu açık; air-gapped uyumlu; ingest/crawler/UI hazır | Recall bge-m3'e bağlı (operatörde); ANN'siz büyük korpus taraması |
| (b) Elasticsearch | BM25 motor-seviyesi; dense_vector+kNN olgun | **Prod'da ölü doğar**: müşteri apikey'i `app-*` SALT-OKUNUR, indeks oluşturma YOK; invariant #2 ("ES yalnız log okuma") kırılır; ES'siz kurulumda KB yok |
| (c) Harici vector DB | Olgun ANN | Yeni bağımlılık = single-binary ihlali; air-gapped kurulum yükü |

**Öneri (a) — "sadece ClickHouse" kısıtı KB için de geçerli ve
istisna GEREKMİYOR**; kural kendiliğinden tutuyor çünkü ES seçeneği
prod yetki duruşuyla zaten imkânsız. Embedding kaynağı: mevcut
`rag_embedding` ayarı (OpenAI-compat `/v1/embeddings`; hedef bge-m3 @
vLLM/KServe — Settings zaten bge-m3'ü örnekliyor). LLM
provider'ından embedding almak (ör. Claude API) air-gapped hedefte
yok; embedding AYRI endpoint olarak kalır. Faz 5'te: text-only
chunk'lara embed backfill komutu + chunk sayısı eşiği aşarsa CH
vector-index değerlendirmesi (ayrı /clickhouse-schema kapısı) +
embed çağrılarına ai_calls kaydı (D7).

### K3 — Context assembly

| Seçenek | Artı | Eksi |
|---|---|---|
| **(a) Deterministik öncelik merdiveni + kademeli budama (saf paket)** ✅ | Table-driven test edilebilir; 2B-model gerçeğine uygun; maliyet öngörülebilir; mevcut renderer'lar yeniden kullanılır | Öncelik bakımı el ile |
| (b) LLM-özetlemeli rolling context | Uzun konuşma kalitesi (teorik) | Ek çağrı maliyeti; küçük modelde özet kalitesi riski; attribution bulanır |
| (c) Ham JSON'u modele bırak | Sıfır kod | Token israfı; guided router'ın varlık sebebi olan başarısızlık modu |

**Öneri (a).** Öncelik: anchor konu → hipotez+verdict kataloğu →
DeepEvidence (Checked iziyle) → korelasyon katmanı (BubbleUp top-N
boyut / BlastRadius / Correlations / yapısal DeployImpact — hepsi
**pre-aggregate özet, asla ham span**; MV-first invariant'ı prompt
beslemesinde de geçerli) → KB chunk'ları (topK, skor-floor'lu) →
konuşma geçmişi (chatMaxMessages=40 üst sınırı kalır; bütçe
daralınca en eskiden kırpılır). Her tier "≤N satır + `[kırpıldı]`
işareti" kuralıyla girer; drawer'ın 3000-rune klampı genel kurala
terfi eder. Özetleme stratejisi: sinyal-başına deterministik Türkçe
renderer (bugünkü HypothesisPromptBlockTR / renderDeepEvidence /
guided bundle formatları `assemble/renderers.go`'ya taşınır).

### K4 — Anomaly worker entegrasyonu (Davis hedefi)

| Seçenek | Artı | Eksi |
|---|---|---|
| **(a) E/N kanıt kataloğunu genişlet: BubbleUp/Blast/Corr/DeployImpact(yapısal)/Runtime/NeighborSignals katalog satırı olur; verdict şeması enum'lar** ✅ | Shields + attribution + feedback OLDUĞU GİBİ çalışır; halüsinasyon koruması bedava; tek anlatı | Prompt büyür → K3 bütçesine bağımlı |
| (b) Ayrı "Davis anlatısı" ucu | Verdict'e dokunmaz | Yeni yüzey + iki anlatının çelişme riski (v0.9.306 "iki çelişen liste" sınıfı) |
| (c) Agentic soruşturma (model tool'larla kanıt toplar) | Esnek | Küçük-model gerçeğine aykırı; maliyet öngörülemez; guided felsefesinin reddi |

**Öneri (a).** Somut genişleme: BubbleUp → "hatalarda ayrışan ilk 3
boyut (attr=değer, katkı%)" satırları E-uzayına; BlastRadius →
"etkilenen N servis, en kötü 3'ü" tek satır; Correlations → "aynı
pencerede kötüleşen komşular (p99Δ)"; DeployImpact → yapısal
önce/sonra RED (tek cümle Reason yerine); Runtime render branşı
eklenir + `s.Limit==0` guard'ı. NeighborSignals bireysel satır olur.
Verdict şemasının entity whitelist'i aynı mekanizmayla genişler.

### K5 — Feedback şeması ve öğrenme döngüsü

| Seçenek | Artı | Eksi |
|---|---|---|
| **(a) `ai_feedback` + `comment` + `surface`; ExchangeID her yüzeye (D3); KB'ye KÜRATÖRLÜ terfi** ✅ | Mevcut tablo/testler genişler; ConfirmedRCASignatures deseni kanıtlı; KB çöplenmez; no-redaction duruşuyla uyumlu ama insan süzgeci var | Terfi operatör disiplini ister |
| (b) Tüm konuşmalar otomatik KB'ye | Sıfır sürtünme | Gürültü + yanlış cevabın kalıcılaşması + KB kalite çöküşü |
| (c) Fine-tune | Teorik en iyi | Air-gapped'de pratik değil; kapsam dışı |

**Öneri (a).** Şema değişikliği (tek ALTER sınıfı, /clickhouse-schema
kapısından): `ai_feedback` RMT'ye `comment String DEFAULT ''`,
`surface LowCardinality(String) DEFAULT ''` kolonları (IF NOT
EXISTS, distributed-safe probe ile). Geri besleme yolları:
(1) 👍'lı RCA'lar → ConfirmedRCASignatures (MEVCUT, değişmez);
(2) 👎+yorum → /ai NegativeFeedbackPanel'e yorum kolonu (triage);
(3) 👍'lı chat/insight cevapları → "KB terfi kuyruğu": aday listesi
Settings→Knowledge'a düşer, operatör onaylarsa `rag_chunks`'a
`source='curated:<exchangeId>'` chunk'ı yazılır (crawler'la aynı
ingest yolu). Outage raporu: incident kapanışında tek buton →
EvidencePackage + timeline + verdict'ten postmortem TASLAĞI →
mevcut /incident postmortem editörüne düşer (editör var, v0.9.206) →
kaydedilirse KB'ye küratörlü giriş önerilir. Runbook önerisi:
`runbook` yüzeyinin girdisine çözümde kullanılan kanıt zinciri
eklenir; çıktı "runbook güncelleme önerisi" bloğu olarak runbooks
sayfasına iliştirilir (yeni şema yok; runbook execution kaydının
yanında).

### K6 — Streaming + provider disiplini (kural taahhütleri)

1. **Stream-first**: tek transport'ta varsayılan stream;
   `classifyStreamResponse` merdiveni korunur (stream → parse-buffered
   → fallback-cache → fallback-once). Explain uçlarına SSE varyantı
   `copilot_drawer.go` emit şekliyle gelir — FE parser'ı zaten var,
   `CopilotExplain` delta tüketecek şekilde güncellenir. GitHub
   yoluna stream ikizi yazılır ya da "buffered, bilinçli" olarak
   dokümante edilir (dış API, air-gapped hedefte yok — düşük öncelik).
2. **Parametreler ayara iner**: `ai_copilot` blob'u `{maxTokens,
   temperature, timeoutS, streamEnabled}` kazanır; AiTab'a alan
   gelir; kod hiçbir yerde sabit taşımaz (D2). Varsayılanlar: 4096 /
   0.2 / 180s. Anthropic/github 1024 tutarsızlığı Faz 0'da kapanır.
3. **Boş-içerik zinciri tek kopya** (`provider/salvage.go`) — üç
   yerde yaşayan zincir tek fonksiyona iner, v0.8.138/155/384
   vakaları golden testlerle pinlenir (Qwen3 reasoning_content, vLLM
   reasoning, `<think>` gövdesi, finish_reason=length teşhis mesajı).
4. **Self-hosted birinci sınıf**: openai-compat yol varsayılan test
   matrisi; vLLM api-key twin header + Gemini-compat Raw replay
   quirk'i transport golden testlerinde pinlenir; `skipTls` korunur.

### K7 — Yetki (tool katmanında, bypass'sız)

Uygulama noktaları (hepsi mevcut dikişlerde):
1. `mcp.Tool`'a `MinRole string` alanı (boş = viewer; okuma
   tool'larının çoğu viewer kalır, `list_anomalies` editor örneği
   REST ile eşitlenir — ya da REST viewer'a iner, ürün kararı).
2. `mcpToolGate` (mcp_gate.go): rate'ten ÖNCE rol kontrolü;
   `Claims.Role` zaten canlı-authz'lı geliyor. Hata metni modele
   okunur şekilde ("bu araç <rol> yetkisi gerektirir").
3. `copilot_chat.go:198`: spec listesi role göre filtrelenir
   (gizlemek > reddetmek).
4. `resources/read` + `prompts/get` dispatch'i gate'e bağlanır;
   `coremetry://trace/{id}` resource'una tool'daki 200-span cap'i
   uygulanır (yan kapı kapanır).
5. `mcp.go:27-33` + `api.go:196-198` yorumları GERÇEĞE çekilir.
6. cmk_ token = token rolü (kullanıcı-bazlı değil) — bilinçli sınır,
   `docs/runbooks/mcp-claude-code.md`'ye yazılır.
7. Custom-roles frontend-only gap'i AI kapsamı DIŞI — A6'da operatöre.

Yazma tool'u bu tasarımda YOK (mevcut duruş korunur); ileride
gerekirse ön-şartları zaten specli
(docs/audit/mcp-claude-code-production-audit.md:152-162: audit
source=mcp + default-off + operatör-onaylı akış).

---

## 4. Fazlı yol haritası

Her faz bağımsız değer üretir; her madde ~1 release (aggressive
cadence). Faz sınırında `make audit` + tam gate zorunlu.

### Faz 0 — Hijyen + feedback rayı (~4-5 release, ~1 gün)
En küçük çalışan dilim; hiçbir davranış değişmez, raylar döşenir.

| Dilim | Kapsam | Dokunulan |
|---|---|---|
| 0.1 | `registerAIRoutes(mux)` — 32 route çıkar; CH-optimize `/api/copilot/optimize-query`'ye taşınır (eski path 301 değil ALIAS kalır, admin gate aynen) → `aiSurfaceFromPath` özel durumu düşer | api/ai_routes.go (yeni), api.go (küçülür) |
| 0.2 | `requireCopilot(h)` middleware — 17 kopya 503 bloğu silinir, tek mesaj şekli | ai_routes.go |
| 0.3 | ExchangeID: `copilotExplain` inbound ExchangeID taşır; 6 wrapper → 2 (prose/JSON); **15 yüzeyde 👍/👎 açılır**; FE'de eksik feedback butonları (Shift/Logs/SlowQueries/DeployHistory/Slos/compare) bağlanır | ai_observability.go, ilgili FE panelleri |
| 0.4 | LLM parametreleri ayara: maxTokens/temperature/timeoutS `ai_copilot` blob + AiTab alanları; anthropic/github/anthropic-stream 4096 paritesi | copilot.go, AiTab.tsx |
| 0.5 | Ölü `SystemPromptRootCauseNarration` silinir; 3 raw-text FE yüzeyi `RenderedMarkdown`'a geçer | copilot.go, Shift/Logs/NoisyRules |

**Kabul:** /ai'da 21 yüzeyin hepsi feedback'li görünür; api.go satır
sayısı ölçülür şekilde düşer; mevcut tüm explain'ler davranış-birebir
(golden yanıt karşılaştırması elle, canlı).

### Faz 1 — Provider konsolidasyonu + streaming explain (~5-6 release)

| Dilim | Kapsam |
|---|---|
| 1.1 | `internal/ai/provider` iskeleti + openai-compat gövdesi; **canary: yalnız explain-charts** yeni transport'tan (kolay geri dönüş) |
| 1.2 | anthropic + github gövdeleri; salvage tek kopya; kota kesici taşınır; tüm explain'ler yeni transport'a |
| 1.3 | chat/tools + stream yolları taşınır; `copilot.go`/`chat.go`/`stream.go` gövde üreticileri boşalır |
| 1.4 | `provider/embed.go` — rag istemcisi buraya; embed çağrıları ai_calls'a (`surface='embedding'`) (D7) |
| 1.5 | Explain SSE varyantı: `POST /api/copilot/explain-*?stream=1` (drawer emit şekli); `CopilotExplain` delta tüketir |
| 1.6 | Prompt'lar `provider/prompts.go`'ya; dil-pinleme testi tam kapsama; 4 api-local prompt taşınır (D5) |

**Kabul:** `grep -c "chat/completions" internal/` = 1 dosya; tüm ✨
yüzeyleri token-token akıyor; ai_calls'da embedding satırları;
provider golden testleri yeşil.

### Faz 2 — Contextual insight kartları (~4-5 release)

| Dilim | Kapsam |
|---|---|
| 2.1 | `internal/ai/insight` sözleşmesi + `GET /api/insight/{kind}/{id}?stream=1` (SSE; Signals deterministik İLK frame — copilot namespace DIŞI: AI kapalıyken de tam cevap, requireCopilot pin'i istisnasız kalır; v0.9.1129 kararı) |
| 2.2 | FE `InsightCard` bileşeni: collapsed sıfır-fetch → expand'de sinyaller anında + prose stream; feedback + "Chat'te devam et" köprüsü; `?ai=` codec genişler |
| 2.3 | Yuva 1-2: exception satırı kartı + problem/alert satırı kartı (RootCauseRibbon ile birleşik görünüm — iki ayrı şerit DEĞİL, ribbon insight sözleşmesini tüketir) |
| 2.4 | Yuva 3-4: log paneli ("Desenleri anlat" karta evrilir) + slow-span (SlowQueries inline'ı karta evrilir) |

**Kabul:** dört yuvada ayrı sayfaya gitmeden açıkla+önerilen-aksiyon;
kart LLM'siz de (503) deterministik sinyalleri gösterir; ES-cost
disiplinleri (fetch-on-open, staleTime) uygulanmış.

### Faz 3 — Tool katmanı + yetki (~5-6 release)

| Dilim | Kapsam |
|---|---|
| 3.1 | `Tool.MinRole` + gate rol kontrolü + spec filtreleme + resources/prompts gate + trace-resource cap + yorum düzeltmeleri (K7) |
| 3.2 | Keşif tool'ları: `list_operations`, `list_environments`, `list_clusters`, `list_deploys`, `find_trace_by_span` |
| 3.3 | Büyük boşluklar: `get_topology` (servicegraph), `get_blast_radius`, `get_log_histogram` |
| 3.4 | Guided-only bundle'lar tool'laşır: `get_db_health`, `get_messaging_health`, `get_pod_health`, `list_problem_window_events`; guided router bunları ÇAĞIRIR (D6 kapanır) |
| 3.5 | (ürün kararı) `list_anomalies` MinRole=editor VEYA REST ucu viewer'a iner — sapma tablosu sıfırlanır |

**Kabul:** MCP tools/list role göre değişir; guided ve MCP aynı veri
şeklini döndürür; "env/operation kabul edip listesini vermeyen tool"
sayısı 0.

### Faz 4 — Chat derinleşmesi (~4-5 release)

| Dilim | Kapsam |
|---|---|
| 4.1 | Konuşma kalıcılığı: `saved_views(page='ai-chat')` blob (başlık + son 40 mesaj + subject); thread listesi FAB menüsünde. **GEMİDE — v0.9.1139 (2026-08-17), KALIYOR** (operatör 2026-08-19 teyidi). |
| 4.2 | ChatBubble `table` fence + başlık/liste (RenderedMarkdown alt-kümesi) |
| 4.3 | Tool sonucu görünürlüğü: ⚙ çipi tıklanınca "veriyi göster" açılır bloğu (JSON→tablo; truncated işaretleri görünür) |
| 4.4 | Chart spec genişlemesi: `operation/groupBy/from-to`; `CosreChart` → `corePanelEntry` (zoom/legend/cursor-sync bedava) |
| 4.5 | `assemble` chat geçmişi bütçesini devralır (K3) |

**Kabul:** sayfa yenilemede konuşma yaşar (4.1 gemide, v0.9.1139); model tablo
istediğinde tablo render olur; tool sonucu denetlenebilir.

### Faz 5 — KB + öğrenme döngüsü (~5-6 release)

| Dilim | Kapsam |
|---|---|
| 5.1 | `ai_feedback` comment+surface kolonları (/clickhouse-schema kapısı, distributed-safe); FE'de 👎 sonrası yorum kutusu; /ai negatif paneline yorum |
| 5.2 | KB terfi kuyruğu: 👍'lı cevap adayları → Knowledge sekmesinde onay listesi → `rag_chunks source='curated:*'` |
| 5.3 | bge-m3 backfill komutu (text-only chunk'ları toplu embed; endpoint gelince tek buton) + chunk-eşiği aşımında ANN değerlendirme raporu |
| 5.4 | Postmortem taslağı: incident kapanış → EvidencePackage+timeline+verdict → taslak → mevcut editör; kaydedilen postmortem'e "KB'ye ekle" önerisi |
| 5.5 | Runbook güncelleme önerisi: çözülmüş problemin kanıt zinciri `runbook` yüzeyine girdi; çıktı runbooks sayfasına iliştirilir |

**Kabul:** KB operatör onayıyla büyür; outage raporu tek tıkla
taslaklanır; embedding maliyeti /ai'da görünür (A5).

### Faz 6 — Davis anlatısı (~3-4 release)

| Dilim | Kapsam |
|---|---|
| 6.1 | Katalog genişlemesi: BubbleUp boyutları + BlastRadius + Correlations E-uzayına; verdict şeması enum genişler |
| 6.2 | Yapısal DeployImpact + Runtime render branşı + Limit=0 guard + NeighborSignals bireysel satır |
| 6.3 | Insight kartları verdict'i tüketir (kanıt-ID'li anlatı kartlarda); problem-auto-explain aynı katalogdan beslenir |

**Kabul:** "kök neden" anlatısı kanıt-ID'li ve shields'li; anlatıda
kataloğa girmemiş hiçbir iddia yaşayamaz (mevcut shield mekanizması).

---

## 5. Test planı

**Coverage hedefi:** yeni `internal/ai/*` saf paketlerinde **≥%80
satır** (assemble, salvage, gövde üreticileri, insight contract);
transport I/O sarmalayıcılarında hedef yok (golden testler davranışı
pinler). FE: yeni bileşenler vitest'li; mevcut 3901-test süiti gate.

| Paket/katman | Test şekli | Örnek vakalar |
|---|---|---|
| `provider` gövde üreticileri | **Golden request** (provider başına JSON snapshot, table-driven) | max_tokens/temp ayardan; stream bayrağı; json_schema/object/serbest üç kip; vLLM api-key twin header; Gemini Raw replay |
| `provider/salvage` | Table-driven | content-boş+reasoning_content dolu; `<think>` gövdeli; finish_reason=length teşhis metni; normal yol dokunulmaz |
| `provider/stream` | Kayıtlı SSE fixture'ları | openai/anthropic accum; kesik akış → fallback merdiveni 4 verdict'i; usage frame |
| `assemble` | Table-driven bütçe matrisi | tier taşması → `[kırpıldı N/M]`; boş tier atlanır; öncelik sırası; 3000-rune klamp paritesi |
| `insight/contract` | Şema testi | prose boş → Signals yine dolu; ExchangeID zorunlu |
| `mcptools` yetki | Table-driven gate matrisi | viewer×MinRole boş=geç; viewer×editor-tool=redd metni; cmk_ token rolü; resources/read gate'li; rate+rol sırası |
| Tool şemaları | MEVCUT desen sürer | yeni tool'lar: her property description'lı, additive, env-listesi tutarlılığı |
| `ai_routes` | mux-collision (mevcut `TestMuxRoutePatterns`) + `requireCopilot` 503 şekli tek test | eski CH-optimize alias'ı |
| ExchangeID ucu | Regresyon (v0.9.X başlıklı) | 15 yüzeyde exchangeId non-empty; feedback replace semantiği |
| FE `InsightCard` | vitest | 4 durum (collapsed/expanded/streaming/error); feedback gate (`canRateVerdict` deseni); chart fence kısmi-parse toleransı |
| FE `ChatBubble` table fence | vitest | kısmi fence stream'de metin, kapanınca tablo (chart fence emsali) |
| KB terfi | Go table-driven | curated chunk id şekli; onaysız yazım yok |
| Golden davranış (Faz 1 canary) | Elle, canlı | explain-charts eski/yeni transport yanıt karşılaştırması + /ai satırları |

Ek disiplinler: her bug-fix release regresyon testli (mevcut kural);
prompt dil-pinleme testi Faz 1.6'dan sonra 28 prompt'un tamamını
kapsar; `make audit`'e "handler'da çıplak `s.copilot.` çağrısı"
lint'i eklenir (wrapper bypass'ı yakalar — bugünkü CLAUDE.md
kuralının otomasyonu).

---

## 6. Riskler ve açık sorular

### Riskler

| Risk | Etki | Azaltım |
|---|---|---|
| Küçük model (2B-sınıf) tool-loop'ta güvenilmez | Chat kalitesi | Guided-first korunur; serbest loop fallback; K3 deterministik bütçe; Faz 3 sonrası guided=tool veri şekli |
| bge-m3 gecikir | KB recall BM25'te | Tasarım bge-m3'süz değer üretir (bugünkü davranış); 5.3 backfill hazır bekler |
| 8→1 transport regresyonu | Tüm AI yüzeyleri | Golden request testleri + Faz 1.1 canary (tek yüzey) + faz-içi geri dönüş kolay (eski kod Faz 1.3'e kadar yaşar) |
| SSE × LB/ingress | Kesik akış | Chat zaten prod'da akıyor (aynı yol); streamable-HTTP dersi mevcut; insight SSE aynı desen |
| KB'ye hassas veri | Gürültü/uygunsuz kalıcılaşma (sızıntı değil — no-redaction operatör duruşu) | Küratörlü terfi; `PromptLogOverride` maskeleme deseni KB önizlemede |
| api.go taahhüdü vs yeni uçlar | — | Tüm yeni AI route'ları ai_routes.go'da; mux-collision testi |
| Faz 3 rol sıkılaştırması mevcut MCP kullanıcılarını kırar | Dış istemci 403 | Varsayılan MinRole="" (viewer) — yalnız bugün REST'te editor olan uçlar sıkılaşır; runbook'ta duyurulur |
| ai_copilot blob genişlemesi eski pod'larla çakışır (rolling) | Config kaybı | Yeni alanlar omitempty + eski blob decode'u değişmez (Enabled *bool emsali) |

### Açık sorular (onayla birlikte yanıt bekliyor)

- **A1 — Konuşma kalıcılığı isteniyor mu?** İKİ KEZ yanıtlandı ve
  yanıtlar ters yönde — sırası önemli:
  1. **2026-08-17: EVET.** `saved_views(page='ai-chat')` blob'u
     (invariant #5'e uygun, yeni şema yok, son-40-mesaj sınırı).
     **Faz 4.1 v0.9.1139'da GEMİYE GİRDİ:**
     `internal/api/ai_conversations.go` (4 route: list/upsert/get/
     delete), FE `chatPersist.ts` + `useChatThread.ts`, FAB'da
     "Geçmiş" menüsü, 64KB shrink-to-fit.
  2. **2026-08-19: "kalıcı olmasına gerek yok bence"** — ama bu,
     özelliğin zaten canlı olduğu bilinmeden söylenmişti (bayat plan
     yüzünden yapılmamış bir iş gibi sunulmuştu). Durum netleşince
     operatör aynı gün **"Konuşma kalıcılığı olsun hadi"** dedi.
     **NİHAİ: EVET, özellik KALIYOR.** Kaldırma gündemden düştü;
     yeniden açmak yeni bir karar ister.

  ⚠️ **Bu bölümün kendisi 2026-08-19'a kadar (1) numaralı kararı
  YANSITMIYORDU** — plan "A1 onayına bağlı" derken özellik iki
  gündür canlıydı. Bayat plan yüzünden operatöre yanlışlıkla
  yapılmamış bir iş gibi sunuldu. Ders: bir fazın durumunu plandan
  değil `git log`dan / koddan doğrula (aynı ders
  [[project-perf-proposals-stale]] ve [[project-quality-bar-progress]]
  kayıtlarında da var).
- **A2 — Insight kartlarının otomatikliği:** hep fetch-on-click mi
  (önerim; LLM maliyet disiplini), yoksa P1 problem satırında
  otomatik açılsın mı? (Auto-explain worker'ları zaten pasif özet
  yazıyor — kart collapsed halde onu gösterir, önerim bu ikisinin
  birleşimi: pasif özet bedava görünür, LLM prose tıkla.)
- **A3 — MCP dış istemcilerine zenginleşme:** Faz 3 tool'ları MCP'ye
  de açılsın mı (tek kaynak gereği doğal sonuç), yoksa MCP kataloğu
  dondurulup yeni tool'lar yalnız in-app'e mi verilsin (tool'a
  `Internal bool` etiketi — mümkün ama iki katalog demek)?
- **A4 — Coverage eşiği:** ≥%80 (saf paketler) kabul mü?
- **A5 — /ai'da embedding maliyeti:** `ai.rates`'e embedding satırı
  (model başına 1M-token fiyatı) eklensin mi?
- **A6 — Custom-roles frontend-only gap'i** (uygulama geneli, AI'dan
  bağımsız): ayrı iş olarak kuyruğa alınsın mı?
- **A7 — `list_anomalies` sapması** (Faz 3.5): tool'u editor'e mi
  sıkalım, REST'i viewer'a mı gevşetelim? (Viewer'ın anomali GÖRMESİ
  invariant #7 ruhuna uygun; silence yazması zaten ayrı uç.)

---

*Kaynak: üç audit raporu (backend LLM · MCP/yetki · frontend AI),
2026-08-16, HEAD b3565cf9. Bu doküman onaylanana kadar kod yazılmaz;
onay + A1-A7 yanıtlarıyla Faz 0'dan başlanır.*
