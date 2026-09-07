# CoSRE agent — oturum 1 dar audit (2026-09-08)

Kapsam: yalnız iki başlık + Adım 0 ölçümü. Geniş mimari audit zaten var:
[cosre-agent-v2.md](cosre-agent-v2.md) (2026-09-07). Buradaki dosya:satır
referansları v0.10.544 çalışma ağacına göre. Emin olunmayan her yer
**doğrulanmadı** işaretli.

---

## Adım 0 — Gemma 4 tool-calling doğrulaması

### 0.1 LLM istemci konfigürasyonu nerede, kaç yerde tekrar ediyor

| Ayar | Kaynak (tek doğruluk) | Tekrar / yansıma |
|---|---|---|
| Endpoint (`baseUrl`) | `system_settings["ai_copilot"]` blobu → `internal/copilot/copilot.go:1113-1151` (`persisted`), profil başına `internal/copilot/profiles.go:48-62` | Varsayılan `https://api.openai.com/v1` **4 yerde**: `copilot/provider_calls.go:103`, `copilot/stream.go:170`, `ai/provider/stream.go:398`, `ai/provider/tools.go:246` (+ `ai/provider/openai.go:26` const) |
| Model adı | aynı blob / profil; boot tohumu `internal/config/config.go:97-100, 805-816` (`COREMETRY_AI_PROVIDER/API_KEY/MODEL/BASE_URL`) | `ai/provider/openai.go:30` `defaultModel = "gpt-4o-mini"` |
| max_tokens | `copilot.go:408` `defaultMaxTokens = openAICompletionTokens` (`:897` = 4096); profil `MaxTokens` | **3 yerde 4096**: `copilot.go:897`, `ai/provider/openai.go:34` (`defaultMaxTokens`), `ai/provider/tools.go:117` (`resolvedMaxTokens` aynı const); ayrıca `copilot.go:851` `cap = 4096` (örnek kırpma — farklı anlam, aynı sayı) |
| temperature | `copilot.go` 0.2 varsayılan (`tuneTemperatureLocked`) | tek yer |
| timeout | `copilot.go:416` `defaultTimeout = 180s` (`clientTimeout`), profil `TimeoutS` | sohbet uçtan uca: `api/chat_deadline.go:55-78` (×3, 180–900 s); araç: `mcp/toolerr.go:203` `ToolCallBudget = 20s` (`ai/agent/tools/executor.go:73` aynı sabit); Influx Test bütçeleri ayrı |
| retry | akış: bağlantı/ilk-bayt hatasında **bir** buffered geri düşüş (`copilot/stream.go:28,132`; `ai/provider/stream.go:51-113` karar önbelleği); JSON kipi merdiveni 400/422/501'de iner (`copilot.go:989`); kota kesici 429 → 1 s (`copilot.go:734-766`); bağlam taşmasında bir kez küçültme (`api/chat_overflow.go`) | tek tanım, üç mekanizma |
| model-farkında yetenek | `internal/ai/modelcaps` (v0.10.534): aile, `Reasoning`, düşünme anahtarı (`chat_template_kwargs.enable_thinking` yalnız Qwen3) | **Gemma 4 ailesi tabloda yok** — `For("gemma-4-31b-it")` → `FamilyGemma`, `Reasoning=false`, anahtar yok. Adım 0 sonucu buraya girer. |

Hüküm: ayarlar merkezî (blob + profil), ama **varsayılan sabitler** (4096, base URL)
üç–dört dosyada kopya. Tek `defaults.go`'ya toplamak Faz 2'nin ilk küçük dilimi olabilir
(davranış değişmez).

### 0.2 Serving stack

- Depo izleri: vLLM ≥ 0.24 varsayımı (`content:null` + `reasoning` alanı yakalama,
  v0.8.384; `ai/provider/salvage.go`), birincil model `gemma4-26b-a4b-it`
  (`internal/anomaly/investigation.go:18`), memory'de "gemma4 lokal, air-gapped".
  Operatörün hedefi **google/gemma-4-31b-it** — depodaki addan farklı.
- Sunucu türü/sürümü ve Gemma 4 tool-call parser'ının varlığı: **doğrulanmadı**
  (bu ortamdan uca erişim yok; lokal küme askıda; laptop'ta model yok).
  vLLM'de kontrol: `vllm --version` ve sunucu bayrakları `--enable-auto-tool-choice
  --tool-call-parser <gemma?>`; parser yoksa `tool_calls` daima null döner ve çağrı
  `content`'ta metin kalır — Adım 0 betiğinin (a) testi tam bunu ayırt eder.

### 0.3 Betik

`scripts/dev/gemma4_toolcall_smoke.py` — bağımlılık `requests` (yoksa stdlib);
env `GEMMA_BASE_URL`, `GEMMA_MODEL`, `GEMMA_API_KEY`, `GEMMA_TIMEOUT`,
`GEMMA_MAX_TOKENS`, `GEMMA_LOW_MAX_TOKENS`, `GEMMA_INSECURE`. Testler a–f;
ham yanıtlar olduğu gibi basılır; sonda markdown tablo + KARAR satırı;
`--json-out` ile ham dosya.

Çalıştırma (prod'a erişimi olan makinede):
```
GEMMA_BASE_URL=http://<vllm>:8000/v1 GEMMA_MODEL=google/gemma-4-31b-it \
python3 scripts/dev/gemma4_toolcall_smoke.py --json-out /tmp/gemma4-smoke.json
```

### 0.4 Sonuç tablosu

Bu ortamda **çalıştırılamadı** (uç yok → çıkış 2; `requests` de yok, stdlib yolu var).
Tablo betiğin ürettiği biçimdedir; hücreler operatörün koşusuyla dolacak.

| test | amaç | sonuç | geçti | detay |
|---|---|---|---|---|
| a (en/tr) | tek argümanlı çağrı → `tool_calls` dolu mu | doğrulanmadı | – | structured / text_embedded / none |
| b (en/tr) | çok argümanlı + iç içe (`filters[]`, `range{}`) | doğrulanmadı | – | nested_ok, Unicode (İ/ş/ö) korunumu |
| c | aynı turda iki çağrı (paralel) | doğrulanmadı | – | n≥2 ise paralel |
| d | tool sonucu geri besleme (`tool` rolü) | doğrulanmadı | – | HTTP 200 + sonuç metne yansır |
| e1 | düşünme bloğu (`<\|channel>thought … <channel\|>` / `reasoning_content`) | doğrulanmadı | – | completion_tokens vs content_chars |
| e2 | düşük max_tokens=48 → boş içerik mi | doğrulanmadı | – | Qwen3 sınıfı |
| e3 | `chat_template_kwargs.enable_thinking=false` kabul | doğrulanmadı | – | |
| e4 | düşük bütçede tool çağrısı kesiliyor mu | doğrulanmadı | – | |

**KARAR: tool-calling'in yapılandırılmış gelip gelmediği doğrulanmadı.** Aşağıdaki
fallback katmanı Faz 2 kapsamına **baştan** giriyor; (a)/(c) "structured" dönerse
katman pasif kalır (sıfır maliyet), dönmezse tek yol odur.

### 0.5 Fallback katmanı önerisi (metin-gömülü tool çağrısı)

- **Yer:** `internal/ai/provider/toolcall_text.go` — `ParseTextToolCalls(content string,
  known []string) ([]ToolCall, rest string, ok bool)`; `ChatOpenAITools`/`ChatGitHubTools`
  yanıtı çözümlerken `tool_calls` boşsa ve `content` çağrı deseni taşıyorsa uygular
  (`ai/provider/tools.go` yanıt çözümleme noktası). Sohbet döngüsü (`api/copilot_chat.go`)
  ve Executor DEĞİŞMEZ — onlar zaten `ChatTurn.ToolCalls` okur.
- **Tanınan biçimler:** Gemma özel token'ları (`<|tool_call>`/`<start_function_call>`
  … doğrulanmadı — betik ham çıktıyı basar, gerçek biçim oradan alınır), Hermes
  `<tool_call>{json}</tool_call>`, ```` ```json {"name":…,"arguments":…} ````,
  ```` ```tool_code ```` pythonic `name(arg=…)`, çıplak JSON nesnesi/dizisi.
- **Kenar durumlar:** (1) düşünme bloğu + çağrı aynı içerikte → önce `StripThinking`
  (`salvage.go`), sonra ayrıştır; (2) `finish_reason=length` ile kesik JSON → çağrı
  YOK, "bütçe kesti" hatası (modele ToolErrorJSON ile değil, kullanıcıya deadline
  metniyle); (3) aynı içerikte birden çok çağrı → sırayla, paralel varsayma;
  (4) çağrı + düz metin karışık → metin `rest` olarak cevap adayı; (5) bilinmeyen
  ad → `unknown tool` sözleşmesi (Executor); (6) Unicode kaçışları (`ü`) →
  `json.Unmarshal` çözer, ham metin regex'i ASCII varsaymaz; (7) `arguments`
  dize değil nesne → ikisi de kabul; (8) tool sonucu rolü: sunucu `tool` rolünü
  reddederse (400) `user` rolünde `[tool_result name=…]{json}` şablonuna düş
  (Adım 0 (d) ölçer).
- **Gözlem:** `ai_calls.error_class`'a `tool_text_fallback` sınıfı değil, `ChatTurn`
  üzerinde `ToolCallsFromText bool` → `ai.chat.turn` span attr'ı; `/ai` sayfasında oran.
- **Test:** tablo testi her biçim + kenar durum; `body_singleton_test` gövde
  kapısı korunur (parser yanıt tarafında).

---

## 2.1 Sayfa bağlamı serialize edilebilir mi?

**Durum değişti (2026-09-07 akşamı gemide):** bu soru v0.10.538–540 ile büyük ölçüde
cevaplandı. Bugün:

- **Saf serileştirici VAR:** `frontend/src/lib/pageContext.ts` — `pageContext(pathname,
  search)` → `{page, path, env, cluster, namespace, service, workload, pod, operation,
  traceId, spanId, problemId, exceptionId, timeRange, activeFilters, search}`; tek rota
  tablosu `ROUTE_PAGES`, bağlamsız sayfa kümesi, üç `?filters=` kodeği (FilterExpr JSON /
  LogFilter tuple / skaler çipler) tek şekle iner. Rota kapsama testi iki yönlü
  (`lib/pageContext.test.ts`: App.tsx ⊆ tablo, tablo ⊆ App.tsx) — eksik rota artık
  sessiz bağlamsızlık değil, kırmızı test.
- **Her sohbet isteğine ekleniyor:** `CopilotChat.tsx` `useMemo(pageContext(loc))` →
  `useChatThread({page})` → `api.copilotChat(…, contextPage)` → gövde `context.page`
  (`lib/api.ts` copilotChat); sunucu `internal/ai/agent/context/page.go` `Sanitize` +
  `PreambleTR` (serbest tool döngüsü önsözü, `api/copilot_chat.go`). Pin (📌):
  `lib/pinnedContext.ts`, `context.pinnedPage`.
- **Nerede duruyor (kaynak):** global store YOK; URL kaynak-of-truth. Zaman `lib/useUrlRange.ts`
  (`?range=`, sessionStorage yedeği), env `lib/useUrlEnv.ts`, kapsam `lib/contextParams.ts`
  (+ `hooks/useContextParams.ts`, tek tüketici `pages/Traces.tsx`). Sayfa param'ları
  aşağıdaki matriste.

Sayfa × alan (U=URL, S=bileşen state, L=localStorage, P=sunucu tercihi, R=sunucu cevabı):

| Sayfa | cluster | namespace | service | workload | pod | trace_id | problem_id | time_range | active_filters | visible_columns |
|---|---|---|---|---|---|---|---|---|---|---|
| `/trace` | – | – | – | – | R | U `?id` | – | U | – | – |
| `/traces` | U | – | U | – | – | U `?traceId` | – | U | U `?filters`+skaler | U `?cols` > P > L |
| `/service` | – | – | U `?name` | – | R | – | – | U | U `?op` | L |
| `/problems`, `/inbox` | – | – | U | – | – | – | U `?problem` | U | U | L |
| `/logs` | U | – | U | – | – | U | – | U | U (LogFilter) | U > P > L |
| `/explore` | U | – | U | – | – | – | – | U | U `?q` | U > P > L |
| `/clusters` | U | U (+`?ns`) | U | U `?deployment` | S | – | – | U | U `?q` | L |
| `/pod` | U | U | U | U `?deploy` | U | – | – | U | – | L |
| `/entity` | R | R | R | R | R | – | – | U | – | – |
| `/rollouts` | U | U | – | U `?rollout` | – | – | – | U | U | L |
| `/dashboard` | U(değişken) | U(değişken) | U(değişken) | – | – | – | – | U | U serbest | – |

Kaynak: 2026-09-07 taraması ([cosre-agent-v2.md](cosre-agent-v2.md) §2.2).
**URL-dışı kalanlar:** `visible_columns` (`useDataTable` tercih/state) ve seçili chart
serisi (Explore `focusKey`/`hiddenKeys` state, `cm.legendVis:*`, `lib/chart/cursorBus.ts`).

**Kalan refactor (küçük, ~yarım gün):** `usePageContextPublisher()` — sayfa yalnız
URL-dışı iki alanı yayınlar (kolonlar, chart seçimi), serileştirici birleştirir;
`useContextParams`'ın `/traces` dışına yayılması opsiyonel (serileştirici zaten
URL'den okuyor). `/trace`'in `history.replaceState` ile yazdığı `?span=` `useLocation`
ile bayat kalabilir (`useAiSubject` bunu telafi ediyor) — **doğrulanmadı**, gözle bakılmalı.

## 2.2 Deployment ve Problem verisi tool olarak açılabilir mi?

### Deployment / rollout
| Soru | Veri | Sorgu yolu | Uç | Maliyet |
|---|---|---|---|---|
| Servisin deploy'ları | span `service.version` geçişleri (MV `deployMVCovers`) | `chstore/deploys.go:1104` `GetRecentDeploys`, `:1135` `GetServiceDeploys` | `api.go:778` `GET /api/services/{name}/deploys`, `:781` `deploy-history` | MV-first, ucuz |
| **Pencerede TÜM deploy'lar** | aynı | `chstore/deploys.go:1120` **`GetDeploysInWindow(from, to, limit)`** — **uç YOK, tool YOK** | – | MV okuma, pencere+limit sınırlı |
| K8s rollouts (ReplicaSet) | `workload_rollouts` (rollouts katmanı, bayrak; migration 0012) | `chstore/rollouts.go:348` `RolloutList(filter{cluster,namespace,status}, from, to, limit)` | `api/rollouts.go:36` `GET /api/rollouts` (namespace destekli), `:37` `GET /api/rollout` | limit 100/500, bayrak kapalıysa 404 `{"disabled":true}` |
| Operatör olayları | events tablosu (docs/DEPLOY-EVENTS.md) | `deploys.go:1050` `deployEventEntries` | `api.go:1109` `GET /api/operator-events` | ucuz |
| Problem ↔ rollout bağı | `workload_rollouts FINAL`, 125 dk pencere, ≤50 workload | `chstore/rollout_problem.go:49` `RolloutsForWorkloads` | DeepEvidence.Rollouts içinde | tek sorgu, `max_execution_time=10` |
| Mevcut tool | servis-kapsamlı | `mcptools/discovery.go:418` `list_deploys(service, range_s, limit)` | – | – |

"Şu pencerede bu namespace'te ne deploy edildi" → **bugün cevaplanamıyor**:
`list_deploys` servis ister, `GetDeploysInWindow` uçsuz, rollouts'un MCP yüzeyi yok.
Öneri: `list_deployments(cluster?, namespace?, service?, from, to, limit)` = üç kaynağın
birleşimi (inferred deploy + rollout + operator event, `source` etiketiyle) — yeni dosya
`internal/mcptools/list_deployments.go`; HTTP eşi `internal/api/changes_routes.go`
`registerChangesRoutes(mux)` → `GET /api/changes?cluster&namespace&service&from&to&limit`
(`route_registry.go` `init()` kaydı, `api.go` büyümez). Maliyet: 1 MV sorgusu + 1
rollout FINAL sorgusu + 1 events sorgusu, hepsi pencere+limit sınırlı, `serveCached` 30 s.

### Problem / correlator
| Soru | Veri | Sorgu yolu | Uç | Tool |
|---|---|---|---|---|
| Problem listesi | `problems` | `chstore/problem.go:1266` `ListProblems(ProblemFilter)` | `api.go` `GET /api/problems` (15 s cache) | `mcptools/tools.go:821` `list_problems` ✅ |
| Tekil problem | aynı | `problem.go:1759` `GetProblem` | `GET /api/problems/{id}` | **yok** (`get_problem`) |
| Kök neden + kanıt | `root_cause_hypotheses` (ReplacingMergeTree, FINAL, TTL 30 g) | `rootcause_hypothesis.go` `GetHypothesis`; `DeepEvidence` (`:46`, `Rollouts :64`) | `api.go:918` `GET /api/problems/{id}/rootcause` | `tools.go:642` `get_problem_root_cause` ✅ (hipotez + DeepEvidence tek gövde) |
| Kanıt alt-alanları | `deep_evidence` tek JSON kolonu | alt-sorgu yok | – | **yok** (`get_correlation_evidence`: Rollouts/TraceIDs/AffectedPods/LogSignatures/External seçici) |
| Benzer geçmiş | `problems` resolved | `problem.go:1358` `FindSimilarResolvedProblems(service, ruleID)` — **uç yok, tool yok** | – | **yok** |
| Pencere olayları | | | | `guided_parity.go:1157` `list_problem_window_events` ✅ |

Mevcut uçlar `list_problems` + `get_problem_root_cause` için yeterli; `get_problem` ve
`get_correlation_evidence` **mevcut store metotlarıyla** açılır (yeni SQL yok), önerilen
dosya `internal/mcptools/problem_evidence.go`; HTTP eşi zaten var (`/api/problems/{id}`,
`/rootcause`). `similar_problems` için `FindSimilarResolvedProblems` uç + tool
(`internal/api/problem_similar_routes.go`), anahtar (service, ruleID) → semptom anahtarı
(`rc_fail_mode`) ikinci dilim.

### RBAC — server-side uygulanabilir mi?
- Roller `internal/auth/auth.go:29-31`, `RequireRole :508`; özel roller **yalnız sayfa
  görünürlüğü** (`internal/auth/custom_roles.go:104`).
- LDAP grup senkronu rol + takım taşır, **cluster/namespace kapsamı taşımaz**
  (`internal/ldap/sync.go:189-648`).
- `envServices` istek parametresi, kullanıcı özniteliği değil; harita hatasında
  **filtresiz** (`internal/api/inbox.go:680`).
- Tool katmanı: `internal/api/mcp_gate.go:94` `toolsForRole`, `:108` `mcpCallGate` (rol +
  rate); tüm tool'lar `MinRole:""`. **Enjeksiyon noktası HAZIR:** `internal/ai/agent/tools`
  `Executor.WithScope(Scope)` — bugün `Unrestricted` (v0.10.536).
- **Hüküm:** server-side kapsam uygulanabilir (Scope.Constrain tool argümanını daraltır:
  `list_deployments`/`list_problems` için `cluster/namespace/service` kümesi), ama
  **kapsam KAYNAĞI yok** (G13: LDAP grup → cluster/namespace eşlemesi). Kaynak kararı
  gelmeden Scope boş kalır; prompt'a yazmak yeterli değil (audit §2.8) — **doğrulanmadı:**
  operatörün LDAP gruplarında namespace bilgisi var mı.

---

## Faz 2 (ilk dikey dilim) effort tahmini

| Dilim | İçerik | Tahmin |
|---|---|---|
| 2.0 Adım 0 koşusu | operatör prod'da betiği koşturur; sonuç modelcaps'e (Gemma 4 ailesi) işlenir | 1 saat (operatör) + 1 saat |
| 2.1 Fallback parser | `ai/provider/toolcall_text.go` + yanıt çözümlemeye bağlama + tablo testi; `ToolCallsFromText` span attr | yarım gün (biçimler Adım 0 ham çıktısından) |
| 2.2 `list_deployments` | tool + `/api/changes` + `changes_routes.go` + testler | yarım gün |
| 2.3 Varsayılan sabit birleştirme | 4096 / base URL tek `defaults.go` | 1 saat |
| 2.4 Eval harness | evalset şemasına `expectedTools`, 30 senaryo JSON, replay raporu | yarım gün |
| 2.5 Bağlam yayıncısı | `usePageContextPublisher` (kolon/seri) | yarım gün (isteğe bağlı) |
| **Toplam** | | **~2 gün** (Scope/G13 hariç) |

## Operatöre açık kararlar
1. **Adım 0'ı prod'da kim/ne zaman koşturur?** (uç + `vllm --version` + parser bayrağı) — sonuç fallback parser'ın kapsamını belirler.
2. **Model adı:** depoda `gemma4-26b-a4b-it` (investigation.go:18), hedef `google/gemma-4-31b-it` — prod profili hangisi? İkisi de mi?
3. **Kapsam kaynağı (G13):** LDAP grup → cluster/namespace eşlemesi nereden? (grup adı deseni mi, admin ayar tablosu mu, yok mu → Scope boş kalır)
4. **Rollouts / entity bayrakları prod'da açılacak mı?** Kapalıyken `list_deployments` yalnız inferred deploy + operator event döner; `resolve_entity` pod/workload'ı span'dan türetir.
5. **Deploy event kaynağı:** pipeline curl adımı (docs/DEPLOY-EVENTS.md) prod'da bağlı mı? Bağlı değilse D2 senaryosu yalnız rollouts/inferred ile geçer.
6. **Eval adları:** senaryolar sentetik; prod koşusu için gerçek ad eşlemesi repoya girmeden nerede tutulacak (operatör lokal dosyası)?
7. **Paralel tool call:** Adım 0 (c) "structured n≥2" dönmezse dizileri seri tutuyoruz — kabul mü, yoksa sunucuda paralel emülasyon mu?
