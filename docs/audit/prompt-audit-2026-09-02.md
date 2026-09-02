# Prompt audit — Coremetry (2026-09-02)

Yöntem: `/claude-api prompt-audit` (Anthropic prompt-audit rehberi). Non-interaktif;
varsayımlar başta, bulgular güven sırasıyla, önerilen diff sonda. **Hiçbir dosya
değiştirilmedi** — diff önerdir, hunk hunk alınır.

## 0. Varsayımlar (kapsam + hedef model)

| Varsayım | Değer | Kanıt |
|---|---|---|
| Kapsam | Depo genelinde prompt yüzeyi: `internal/copilot/prompts.go` (26 sistem prompt'u, 1511 satır), kullanıcı-prompt kurucuları (`internal/api/copilot_guided.go`, `internal/anomaly/{problem_explainer,exception_context,rootcause_prompt}.go`, `internal/ai/insight/prompt.go`), araç açıklamaları (`internal/mcptools/tools.go`, 23 `Description:`; sohbet araçları aynı katalogdan `ChatDescription()`), MCP prompt'ları (`internal/mcptools/prompts.go`), istek kurucuları (`internal/ai/provider/{anthropic,openai,github}.go`, `internal/copilot/{copilot,chat,stream,provider_calls}.go`), skill/kural dosyaları (`CLAUDE.md`, 19 × `.claude/skills/*/SKILL.md`, `.claude/agents/coremetry-feature-shipper.md`), FE model tabloları (`lib/ai-rates.ts`, `settings/AiTab.tsx`) | envanter §1 |
| **Birincil sağlayıcı Anthropic DEĞİL** | Copilot üretimde **yerel küçük model** (OpenAI-uyumlu `openai` sağlayıcısı: Ollama/vLLM; prompt yorumları "qwen3.5-2b", "2B-sınıfı yerel model" der — `prompts.go:119, 1003, 1242`) | `internal/copilot/copilot.go:11-18`, `internal/ai/provider/openai.go`, `github.go` |
| Anthropic yolu var, ikincil | `internal/ai/provider/anthropic.go` — Messages API, varsayılan `claude-sonnet-4-6` | `anthropic.go:35`, `copilot/stream.go:188` |
| Hedef model (Copilot prompt metni) | **Yerel 2B-sınıfı model** — bu hedefte "eski Claude için yazılmış" kalıpların çoğu (büyük harf yasaklar, few-shot şekil, JSON-only disiplini) **yük taşıyan ölçülmüş düzeltmelerdir** (yorumlarda sürüm + gözlem var). Bu satırlar rehberin "mevcut modelde tekrar eden hata için yasak KALIR" kuralına girer; Anthropic yolu birincil olursa yeniden baz alınır (§3 flag) | `prompts.go:116-152, 375-380, 1240-1276` |
| Hedef model (Anthropic yolu, istek kurucusu) | Mevcut Claude nesli (Opus 5 / Sonnet 5 / Opus 4.7-4.8, Fable 5.1). Bulgular "bu modelde 400 döner / desteklenmez" ölçütüyle | rehber §1b, §4 |
| Hedef model (skill/kural dosyaları, MCP prompt'ları) | **Claude Fable 5.1** (Claude Code çalışma zamanı; MCP prompt/araçları dış Claude istemcileri tüketir) | harness |
| Anthropic-dışı işaretler (SDK'ya geçiş ÖNERİLMEZ) | `openai.go`, `github.go`, `copilot.go:46-48` üç sağlayıcı; `ai-rates.ts` gpt-*/llama/qwen satırları | — |

## 1. Envanter

| Yüzey | Nerede | Boyut | Not |
|---|---|---|---|
| Sistem prompt'ları | `internal/copilot/prompts.go` | 26 sabit; 13 İngilizce gövde + Türkçe çıktı, 13 Türkçe-native | tek dosya, dil kapısı testli (`prompt_language_test.go`) |
| Sistem-prompt ekleri | `AnswerInTurkish`, `systemCodeAddendum`, `DataNotInstruction`, `systemChatRoundCap` | — | çağrı yerinde birleştirilir |
| Kullanıcı-prompt kurucuları | `copilot_guided.go` (8 "UYDURMA" satırı), `problem_explainer.go:230`, `exception_context.go:521`, `rootcause_prompt.go` (HypothesisPromptBlockTR), `insight/prompt.go:100,130` | — | kanıt paketi + kural satırları |
| Araç açıklamaları | `mcptools/tools.go` 23 tanım (78–1191 byte, medyan 176) + `ShortDescription` (60–400 byte kapısı, `short_desc_test.go:39-44`) | — | sohbet döngüsü `ChatDescription()` |
| MCP prompt'ları | `mcptools/prompts.go` — `explain_trace/problem/…` → `copilot.SystemPrompt*` aynısı | — | dış Claude istemcileri |
| İstek kurucuları | `provider/anthropic.go` (buffered), `provider/stream.go`, `provider/openai.go` (response_format json_object/json_schema), `copilot/provider_calls.go` (JSON basamağı düşürme), `salvage.go` (boş cevap/`<think>` kurtarma) | — | §3 H1-H3 |
| Örnekler | `systemProblem` 1 few-shot, `systemServiceAnalysis` 1 few-shot, `systemNLToQuery` 3 örnek | — | küçük model "şekli görmeli" gerekçeli |
| Skill/kural | `CLAUDE.md` 208 satır; 19 SKILL.md (82–449 satır, toplam ~4.8k); ajan tanımı 225 satır | — | §3 S1-S5 |
| FE model sabitleri | `lib/ai-rates.ts:35-41`, `settings/AiTab.tsx:177` | — | §3 H4 |
| Token muhasebesi | `ai_calls` tablosu + `/ai` sayfası **VAR** | — | Grup 4 ön koşulu sağlanmış |

## 2. Provenance

`prompts.go` 12 commit (v0.9.1128 taşıma → v0.10.194); her prompt başındaki yorum
gerekçeyi ve sürümü taşıyor (v0.5.144 span, v0.8.374 Türkçe, v0.9.556 anti-uydurma,
v0.9.842 ve v0.9.1045 "kısalık emri derinliği çöpe atıyordu" düzeltmeleri, v0.10.13
self-meta marka halüsinasyonu, v0.10.48 enjeksiyon çerçevesi). Yani "kimse gerekçesini
hatırlamıyor" sınıfı satır **yok**; kalan soru her satırın hedef modelde hâlâ gerekip
gerekmediği. Anthropic istek kurucusu FAZ 1.2'de taşınmış "bayt-bayt aynı" koddur —
model nesli ilerlerken istek şekli güncellenmemiş (H1-H3).

## 3. Bulgular (güven sırasıyla)

Sayım: Grup 1 (prompt metni) 6 · Grup 2 (skill) 5 · Grup 3 (araç) 2 · Grup 4 (istek/mimari) 5 · flag 4.

**En yüksek etkili üç bulgu:** (H1) Anthropic yolunda `temperature` gövdeye basılıyor — Opus 4.7+/Opus 5/Sonnet 5/Fable'da **400**, yani operatör modeli değiştirdiği an Anthropic yolu tamamen kırılır; (H2) Anthropic yolu JSON basamağını reddedip prompt-JSON + salvage'a düşüyor — structured outputs (`output_config.format`) varken; (P1) sekiz kardeş prompt'ta "3-5 bullets / Be terse" sayısal tavanı duruyor — deponun kendisi bu sınıfı iki kez (v0.9.842, v0.9.1045) "kanıtı çöpe atan kısalık emri" diye düzeltmişti, kalan sekizi düzeltilmedi.

| # | Konum | Kanıt | Kalıp | Neden eskimiş | Güven | Aksiyon |
|---|---|---|---|---|---|---|
| H1 | `internal/ai/provider/anthropic.go:71-73` (+ `copilot/copilot.go:395` `defaultTemperature = 0.2`, `chat.go:126`) | `if req.Temperature != nil { body["temperature"] = … }` | 1b / Grup 4 API fosili | Örnekleme parametreleri Opus 4.7/4.8/5, Sonnet 5, Fable 5/5.1'de **kaldırıldı — 400**. Varsayılan `claude-sonnet-4-6` kabul ediyor; ayar sayfasından yeni bir model yazıldığında bütün yol düşer. 0.2 literal'i openai/github yolunun mirası (`copilot.go:391`) | **Yüksek** | rewrite — Anthropic gövdesinde `temperature` gönderme (diff D1) |
| H2 | `anthropic.go:24-28, 48-50` + `copilot/provider_calls.go:106-112` | "JSON basamağı yok … Anthropic'in yazılışı tool-forcing" → `JSONLevel != JSONPlain` hata; Service basamağı `jsonNone`'a düşürüyor | 1b "JSON zorlama yığını" → API özelliği | Structured outputs (`output_config.format` json_schema) Messages API'de var; prompt'taki "NO preamble, NO fences" + salvage zinciri bu yolda gereksiz. Not: prefill KULLANILMIYOR (iyi — 4.6+'da 400 dönerdi) | **Yüksek** (özellik var) / uygulama şekli **doğrula** | replace-with-API-feature (diff D2; canlı dokümanla alan şeklini doğrula) |
| H3 | `anthropic.go:63-70`, `ParseAnthropic:105-133` | `thinking` yok, `output_config.effort` yok, `stop_reason` okunmuyor | Grup 4 yeniden-bazlama (`add`) | Sonnet 4.6'da thinking ancak `{type:"adaptive"}` ile açılır (Opus 5'te varsayılan açık); Fable/Opus 5'te `stop_reason:"refusal"` HTTP 200 + boş metin döner → bugünkü kod bunu "boş panel" olarak yutar (`:125-127` boş metin hata sayılmıyor). Ayrıca `cache_control` yok: sistem prompt'ları sabit, kullanıcı bloğu değişken — cache'lenebilir | **Orta** | add (diff D3: adaptive thinking + effort + refusal → açık hata) |
| H4 | `frontend/src/lib/ai-rates.ts:35-41`; `anthropic.go:35`, `copilot/stream.go:188`, `settings/AiTab.tsx:177` | `claude-3-5-sonnet-20241022`, `claude-3-5-haiku-20241022`, `claude-3-opus-20240229` satırları; `claude-opus-4-7` $15/$75 (güncel $5/$25); Opus 4.8/5, Sonnet 5, Fable yok; varsayılan model üç yerde ayrı literal | 1d fosil (emekli model adları) + Grup 2 uçucu özgül + üç kopya | Emekli kimlikler; fiyatlar 2025-12 anlık görüntüsü (`ai-rates.ts:20-24`). Üç kopya birlikte kaymıyor | **Yüksek** (kimlikler) / **Orta** (fiyat: 2026-06-24 önbelleğinden) | rewrite (diff D4) + tek sabit (diff D5) |
| S1 | `.claude/skills/release/SKILL.md:108`, `.claude/agents/coremetry-feature-shipper.md:65` | `Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>` | Grup 2 "pinli model adı sessizce eskir" | `CLAUDE.md:94` jenerik `Claude <noreply@anthropic.com>` der; çalışma zamanı harness'i güncel imzayı kendisi verir. Skill metni yanlış nesli sabitliyor | **Yüksek** | rewrite (diff D6) |
| P1 | `prompts.go:107-114` (span), `:271-277` (incident), `:286-292` (anomaly), `:309-321` (service health), `:338` (runbook 5-8), `:409-424` (compare), `:444-456` (deploy impact), `:478-497` (SLO), `:574-593` (slow query) | "Answer in 3-6 short bullets: (1)…(4)", "Respond in 3-5 short bullets", "Be terse — pager call" | 1f çıktı-şekillendirme (sayısal tavan + numaralı koreografi) | Depo-içi kanıt: aynı tavan `systemTraceBody` (v0.9.842) ve `systemExceptionBody` (v0.9.1045) için "kanıtın parasını ödeyip derinliği çöpe atıyordu" diye kaldırıldı; sekiz kardeş prompt aynı emri taşıyor. Hedef küçük modelde de ölçülen zarar bu. Bölüm şeması ("(1)…(4)") kalır — çıktı biçimi duyarlı; SAYI gider | **Orta** | rewrite (diff D7: tavanı "kanıtın desteklediği kadar" ile değiştir; bir prompt tam, diğerleri tek satır) |
| P2 | `prompts.go:1294`, `:1316` | "madde işaretleri kullan, 8 maddeyi geçme" | 1f sayısal tavan | Aynı sınıf; sohbet cevabında 8 tavanı uzun kanıt listelerini keser | **Orta** | rewrite (diff D7) |
| P3 | `prompts.go:717-755` | "Available MVs: service_summary_5m, operation_summary_5m, topology_edges_5m, topology_root_flows_5m, db_summary_5m, db_caller_summary_5m"; "spans / logs / metric_points are ordered by (service_name, time)" | Grup 2 uçucu özgül (kod kaydı, doğrulama tarihi yok) | Katalog eksik: `trace_summary_5m`, `trace_service_index_5m`, `spanmetrics_{1s,10s,1m}`, `rollup_spans_*`, `rollup_metrics_*`, `db_statement_summary_5m`, `service_callers_5m`, `topology_op_edges_5m`, `entity_seen_5m`, `workload_revision_activity_1m` (perf SQL-eşleme raporu 2026-09-02); `metric_points` ORDER BY `(service_name, metric, time)` | **Orta** | rewrite (diff D8) |
| T1 | `internal/mcptools/tools.go:585` (get_root_cause_hypothesis) | "CRITICAL: never cite a signal whose checked entry says found=false as a cause" | Grup 3 açıklama içinde MUST/CRITICAL yönlendirme | Sözleşme cümlesi doğru ve kalmalı; "CRITICAL:" vurgusu Claude istemcisinde aşırı tetikler. Anlamı düz sözleşme olarak yaz | **Orta** | rewrite (diff D9) |
| S2 | `CLAUDE.md:203`, `.claude/skills/api-route/SKILL.md:3,8,17,291` | "api.go 11.8k satır", "11.848 satır / 352 route", "438 route + 131 dosya", "352 route'unun 179'u" | Grup 2 uçucu özgül; birbiriyle tutarsız (352 / 438 / bugün 351 kayıt satırı, 453 registration) | Sayılar her sürümde kayar; kural "api.go büyümez" sayıya bağlı değil | **Orta** | rewrite (diff D10) |
| S3 | `CLAUDE.md:80-84` | "Bu satır v0.5.394→v0.9.339 arasında YANLIŞTI: env override kaldırılmıştı ama doküman onu zincirin başında gösteriyordu" | 1d göç-göreli ifade (önceki prompt sürümüne diff) | Mevcut kural tek başına yeter; olay `docs/INCIDENTS.md`'ye aittir | **Orta** | rewrite (diff D11) |
| S4 | `.claude/skills/bugfix/SKILL.md:25-49` | "Re-read past-mistake ledger (MANDATORY — every invocation) … open and read in full: MEMORY.md, EVERY feedback-*.md" | Grup 2 ritüel; v0.6.37 operatör direktifi (gerekçeli → içerik kalır) ama mekanik eskidi | MEMORY.md artık her oturumda otomatik yüklü; "tüm feedback dosyalarını tam oku" ~45 dosya okuması; skill zaten "en alakalı 1-3 dersi seç" der | **Orta** (operatör direktifi — reddedilebilir) | rewrite (diff D12: index bağlamda, yalnız ilgili dosyalar açılır) |
| S5 | `.claude/skills/perf-triage/SKILL.md` "ÖRNEK VAKA" (~90 satır) | v0.9.621-623 CHANNEL_CODE olayı anlatısı | Grup 2 tarih anlatısı; kontrol listesi zaten kuralı taşıyor | Skill her tetiklemede 399 satır; vaka `docs/INCIDENTS.md`/`/perf-triage` referans dosyasına taşınabilir. Ev tarzı olay hikâyesini değerli sayıyor → düşük | Düşük | flag (move önerisi; diff yok) |
| F1 | `prompts.go` genel: 16 × "UYDURMA", "ASLA", "MUTLAK KURAL" (`:369`), "YALNIZ JSON" | Baskı dili yoğunluğu | 1a | Hedef yerel 2B modelde bu satırların her biri gözlenmiş bir hataya (v0.9.556, v0.10.13) bağlı → **kalır**. Anthropic yolu birincil olursa yeniden bazla: Claude'da aynı emirler aşırı-tetikleme/kaçınma üretir | Düşük | flag |
| F2 | `prompts.go:150-152` ↔ `problem_explainer.go:31`, `copilot_guided.go` (8 satır) | Anti-uydurma kuralı sistem + kullanıcı prompt'unda tekrar | 1c tekrar | Kopyalar birbiriyle çelişmiyor; rehber: çalışan yedeklilik cruft değil | Düşük | flag |
| F3 | `prompts.go:855` "Öneriler en fazla 3 tane" | Sayısal tavan, JSON çıktısı | 1f | Şema-duyarlı çıktı: tavan JSON şemasına (`maxItems: 3`) taşınabilir, prompt'tan düşer | Düşük | flag (move → şema) |
| F4 | `prompts.go:981` "En fazla iki KISA paragraf … Başlık/madde KULLANMA — arayüz başlıkları kendi basıyor" | Sayısal tavan + biçim yasağı | 1f / 1d anti-format | Biçim yasağının gerekçesi gerçek (çekmece çift başlık) → kalır; "iki paragraf" tavanı niteliğe çevrilebilir | Düşük | flag |

Temiz bulunanlar (kayıt): prefill yok; `stop_sequences` yok; `budget_tokens` yok;
`anthropic-version: 2023-06-01` geçerli; `DataNotInstruction` enjeksiyon çerçevesi
bağlam/politika (kalır); `systemNLToQuery` üç örneği biçim-duyarlı ve etiketli (kalır);
araç açıklamaları genel olarak sözleşme odaklı ve yeterince uzun (medyan 176 byte;
kısa varyantlar için ayrı kapı var) — Grup 3 "az açıklanmış" bulgusu yok; `ai_calls`
muhasebesi mevcut (Grup 4 ön koşulu).

## 4. Önerilen diff (yalnız Yüksek/Orta; hunk başına bir bulgu) — **UYGULANDI v0.10.253** (2026-09-02)

Uygulama notları: D1 tam (buffered + streaming + tools gövdeleri; testler tersine çevrildi) · D2 JSONSchema seviyesi `output_config.format` (json_schema) + 400'de düz çağrıya düşüş ve uç-başına işaret; JSONObject seviyesi bu API'de yok → düz çağrı · D3 `Request.Effort` → `output_config.effort` + `stop_reason=refusal` açık hata; adaptive thinking GÖNDERİLMİYOR (5 ailesinde varsayılan, 4.6'da maliyet kararı operatörün) · D4 · D5 (`DefaultAnthropicModel` + `/api/settings/ai defaultModel` + AiTab) · D6 · D7 (9 prompt + 2 sohbet) · D8 · D9 · D10 (+ route_registry notu) · D11 · D12. F1-F4 bayrak olarak kaldı.

### D1 — H1: Anthropic gövdesinde örnekleme parametresi gönderme
```diff
--- a/internal/ai/provider/anthropic.go
+++ b/internal/ai/provider/anthropic.go
@@
-	if req.Temperature != nil {
-		body["temperature"] = *req.Temperature
-	}
+	// temperature/top_p/top_k Opus 4.7+, Opus 5, Sonnet 5 ve Fable 5/5.1'de
+	// KALDIRILDI (400). Değer openai-compat yolunun mirasıydı (copilot.go
+	// defaultTemperature); bu yolda hiç gönderilmez — model seçimi 400'e
+	// dönüşmesin. Determinizm istenirse effort/thinking ile (D3).
```
İkinci yarılar: `provider_calls_temp_test.go` / `anthropic_test.go` içinde "temperature gövdeye basılır" iddiası varsa Anthropic dalı için tersine çevrilir; `copilot.go:391-395` yorumundaki "openai/github literal'i" notu Anthropic'i kapsam dışı bırakacak biçimde güncellenir.

### D2 — H2: JSON basamağını structured outputs ile karşıla (şekli canlı dokümanla doğrula)
```diff
--- a/internal/ai/provider/anthropic.go
+++ b/internal/ai/provider/anthropic.go
@@
-	if req.JSONLevel != JSONPlain {
-		return Response{}, fmt.Errorf("provider: anthropic yolunda JSONLevel=%d desteklenmiyor …", req.JSONLevel)
-	}
@@
 	body := map[string]any{
 		"model":      model,
 		"max_tokens": maxTokens,
 		"system":     req.System,
 		"messages":   []map[string]any{{"role": "user", "content": req.User}},
 	}
+	// Structured outputs: şema varsa json_schema, yalnız "JSON olsun" ise
+	// serbest nesne şeması. Prompt'taki "NO preamble / no fences" satırları
+	// bu yolda gereksizleşir (yerel model için kalır). Alan adlarını
+	// yayınlamadan önce Messages API structured-outputs dokümanıyla doğrula.
+	switch {
+	case req.JSONLevel == JSONSchema && len(req.JSONSchema) > 0:
+		body["output_config"] = map[string]any{"format": map[string]any{
+			"type": "json_schema", "schema": req.JSONSchema}}
+	case req.JSONLevel >= JSONObject:
+		body["output_config"] = map[string]any{"format": map[string]any{
+			"type": "json_schema", "schema": map[string]any{"type": "object"}}}
+	}
```
```diff
--- a/internal/copilot/provider_calls.go
+++ b/internal/copilot/provider_calls.go
@@ jsonSchemaBlocked / jsonModeBlocked
-	// anthropic: basamak her zaman düşürülür (response_format yok)
+	// anthropic: output_config.format ile karşılanır — düşürme yalnız
+	// sunucudan 400 (unsupported) dönerse (jsonModeVerdictStatus mevcut).
```
İkinci yarılar: `jsonmode_test.go` Anthropic dalı beklentisi; `salvage` zinciri Anthropic'te yalnız boş-cevap için kalır.

### D3 — H3: adaptive thinking + effort + refusal
```diff
--- a/internal/ai/provider/anthropic.go
+++ b/internal/ai/provider/anthropic.go
@@ body
+	// 4.6+ ailesi: adaptive thinking (Sonnet 4.6'da açık istenmeli; Opus 5'te
+	// varsayılan). Derinlik prompt'la değil effort ile: tek-atış anlatım/
+	// sınıflandırma yüzeyleri "low", RCA hakemi "high".
+	body["thinking"] = map[string]any{"type": "adaptive"}
+	if req.Effort != "" { // Request'e yeni alan; Service yüzey başına seçer
+		oc, _ := body["output_config"].(map[string]any)
+		if oc == nil { oc = map[string]any{} }
+		oc["effort"] = req.Effort
+		body["output_config"] = oc
+	}
@@ ParseAnthropic
 	var parsed struct {
 		Content []struct{ Type string `json:"type"`; Text string `json:"text"` } `json:"content"`
+		StopReason string `json:"stop_reason"`
 		Usage struct{ … } `json:"usage"`
 	}
@@
+	if parsed.StopReason == "refusal" {
+		// Fable/Opus 5: HTTP 200 + boş metin. Boş panel değil, açık hata.
+		return Response{InputTokens: …, OutputTokens: …}, errors.New("anthropic: model isteği reddetti (stop_reason=refusal)")
+	}
```
Not: `thinking` bloğu `ParseAnthropic:121` zaten atlanıyor. `cache_control` (sistem prompt'una `{"type":"ephemeral"}`) ayrı, ölçülerek eklenir — sistem metinleri ≥ minimum önbellek eşiğini geçiyor mu bilinmiyor.

### D4 — H4: fiyat tablosu (emekli kimlikler çıkar, güncel nesil girer; 2026-06-24 önbelleği)
```diff
--- a/frontend/src/lib/ai-rates.ts
+++ b/frontend/src/lib/ai-rates.ts
@@
-  'claude-opus-4-7':           { inputPer1M: 15.00, outputPer1M: 75.00 },
-  'claude-opus-4-6':           { inputPer1M: 15.00, outputPer1M: 75.00 },
-  'claude-sonnet-4-6':         { inputPer1M: 3.00,  outputPer1M: 15.00 },
-  'claude-haiku-4-5':          { inputPer1M: 0.80,  outputPer1M: 4.00 },
-  'claude-3-5-sonnet-20241022': { inputPer1M: 3.00,  outputPer1M: 15.00 },
-  'claude-3-5-haiku-20241022':  { inputPer1M: 0.80,  outputPer1M: 4.00 },
-  'claude-3-opus-20240229':     { inputPer1M: 15.00, outputPer1M: 75.00 },
+  // Anthropic — 2026-06-24 fiyat önbelleği; emekli 3.x kimlikleri çıkarıldı
+  'claude-fable-5-1':  { inputPer1M: 10.00, outputPer1M: 50.00 },
+  'claude-fable-5':    { inputPer1M: 10.00, outputPer1M: 50.00 },
+  'claude-opus-5':     { inputPer1M: 5.00,  outputPer1M: 25.00 },
+  'claude-opus-4-8':   { inputPer1M: 5.00,  outputPer1M: 25.00 },
+  'claude-opus-4-7':   { inputPer1M: 5.00,  outputPer1M: 25.00 },
+  'claude-opus-4-6':   { inputPer1M: 5.00,  outputPer1M: 25.00 },
+  'claude-sonnet-5':   { inputPer1M: 2.00,  outputPer1M: 10.00 },
+  'claude-sonnet-4-6': { inputPer1M: 3.00,  outputPer1M: 15.00 },
+  'claude-haiku-4-5':  { inputPer1M: 1.00,  outputPer1M: 5.00 },
```
`ai-rates.ts:20` "verified 2025-12" notu tarihle güncellenir; `ai-rates.test.ts` varsa anahtar listesi.

### D5 — H4: varsayılan model tek sabit
```diff
--- a/internal/ai/provider/anthropic.go
+++ b/internal/ai/provider/anthropic.go
-	defaultAnthropicModel = "claude-sonnet-4-6"
+	// DefaultAnthropicModel — TEK kaynak; stream.go ve /api/settings/ai
+	// varsayılan-etiketi buradan okur (üç kopya v0.9.1120 sınıfı kayma).
+	DefaultAnthropicModel = "claude-sonnet-4-6"
--- a/internal/copilot/stream.go
@@
-		model = "claude-sonnet-4-6"
+		model = aiprov.DefaultAnthropicModel
```
FE `AiTab.tsx:177` etiketi `/api/settings/ai` cevabındaki `defaults.anthropicModel` alanından okur (ayrı küçük hunk). Varsayılanı `claude-opus-5`'e yükseltmek **ürün kararı** — burada önerilmiyor, yalnız tek-kaynak.

### D6 — S1: imza pinleri
```diff
--- a/.claude/skills/release/SKILL.md
-Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
+Co-Authored-By: Claude <noreply@anthropic.com>
--- a/.claude/agents/coremetry-feature-shipper.md
-Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
+Co-Authored-By: Claude <noreply@anthropic.com>
```
(`CLAUDE.md:94` ile aynı; çalışma zamanı harness'i model-özel imzayı kendisi dikte ediyorsa o kazanır.)

### D7 — P1/P2: sayısal tavanlar → kanıt-ölçekli nitelik (bir prompt tam, kalanı tek satır)
```diff
--- a/internal/copilot/prompts.go
+++ b/internal/copilot/prompts.go
@@ systemSpan
-Answer in 3-6 short bullets: (1) one-line description of what this
-span is doing, (2) where the time goes (self vs. waiting on
-children — call it out by service + name), (3) any error chain
-visible in the context, (4) one or two concrete next-step
-suggestions for an oncall.
-
-Be terse and direct — operator is reading this on a pager call.
-No preamble, no headers — just the bullets.
+Answer in short bullets — as many as the evidence supports, no more:
+what this span does; where the time goes (self vs. waiting on
+children — by service + name); any error chain visible in the
+context; the concrete next step for an oncall.
+
+The operator is reading this on a pager call: quote exact values,
+skip filler, no preamble, no headers.
@@ systemIncident:271 / systemAnomaly:286 / systemServiceHealth:309 / systemCompareTraces:409 / systemDeployImpact:444 / systemSLOBurn:478 / systemSlowQuery:574
-… in 3-5 bullets:            (ve "3-4", "3-5 short bullets")
+… in short bullets — as many as the evidence supports:
@@ systemRunbook:338
-Produce 5-8 numbered steps, each one a concrete action:
+Produce a numbered step list — as many steps as the past instances
+and the metric justify — each one a concrete action:
@@ systemGuidedChat:1294 / systemDrawerChat:1316
-- Kısa ve taranabilir yaz: madde işaretleri kullan, 8 maddeyi geçme.
+- Kısa ve taranabilir yaz: madde işaretleri kullan; yalnız sorunun
+  gerektirdiği kadar madde.
```
Doğrulama: `prompt_language_test.go` / `trace_prompt_test.go` bu satırlara pinliyse beklentiler birlikte değişir. Rehber: yeniden-bazlama ölçülmeli — küçük modelde tavan kalkınca uzama olursa nitelik ifadesini sıkılaştır, sayıyı geri koyma.

### D8 — P3: CH optimize prompt'unun MV kataloğu
```diff
--- a/internal/copilot/prompts.go
+++ b/internal/copilot/prompts.go
@@ systemCHQueryOptimize
-       • db_caller_summary_5m (DB callers grouped)
+       • db_caller_summary_5m (DB callers grouped)
+       • db_statement_summary_5m (per-statement DB summary)
+       • trace_summary_5m / trace_service_index_5m (trace list + service index)
+       • spanmetrics_1s / _10s / _1m (RED per route, tiered)
+       • rollup_spans_narrow_{10s,1m,5m,1h}, rollup_spans_wide_* (long-window RED)
+       • rollup_metrics_{1m,5m,1h}, rollup_metrics_route_* (metric_points rollups)
+       • service_callers_5m, topology_op_edges_5m (callers / op edges)
+       • entity_seen_5m, workload_revision_activity_1m (K8s entity + rollouts)
@@
-     metric_points are ordered by (service_name, time) — every
+     spans/logs are ordered by (service_name, time), metric_points by
+     (service_name, metric, time) — every
```
Kalıcı çözüm (flag): kataloğu koddan (`chstore` tablo dilimi) üretip prompt'a çağrı yerinde enjekte etmek — uçucu özgül kodda yaşar.

### D9 — T1: araç açıklamasında vurgu → sözleşme
```diff
--- a/internal/mcptools/tools.go
+++ b/internal/mcptools/tools.go
@@ get_root_cause_hypothesis
-… and `business` breaks the failure down by channel/function code. CRITICAL: never cite a signal whose checked entry says found=false as a cause — it was looked at and nothing was there. If every entry is found=false, say the evidence is insufficient instead of naming a cause.
+… and `business` breaks the failure down by channel/function code. A `checked` entry with found=false means that family was inspected and nothing was found; it is not evidence for a cause. When every entry is found=false the honest answer is "evidence insufficient".
```

### D10 — S2: uçucu sayılar
```diff
--- a/CLAUDE.md
-… (`/api-route`). api.go 11.8k satır; oraya route eklemek bu dosyayı büyütmenin tek yolu.
+… (`/api-route`). api.go zaten çok büyük; oraya route eklemek onu büyütmenin tek yolu.
--- a/.claude/skills/api-route/SKILL.md
-description: … so api.go (11.8k lines, 352 routes) grows by exactly one line. …
+description: … so api.go grows by exactly one line. …
-`internal/api/api.go` **11.848 satır / 352 route**. Yeni yüzey oraya
+`internal/api/api.go` on binin üstünde satır ve yüzlerce route taşıyor. Yeni yüzey oraya
```
(`:17` "v0.9.1293'te 438 route + 131 dosya" tarih damgalı ölçüm notu olarak kalabilir; `:291` "352 route'unun 179'u" → "route'ların yaklaşık yarısı".)

### D11 — S3: göç-göreli cümle
```diff
--- a/CLAUDE.md
@@ Versioning
-… `/api/version` ikisini de döndürür + `overridden` bayrağı — bayat bir env artık imajmış gibi davranamaz (v0.5.394 olayı). Bu satır v0.5.394→v0.9.339 arasında YANLIŞTI: env override kaldırılmıştı ama doküman onu zincirin başında gösteriyordu.
+… `/api/version` ikisini de döndürür + `overridden` bayrağı — bayat bir env imajmış gibi davranamaz (olay: v0.5.394, docs/INCIDENTS.md).
```

### D12 — S4: bugfix adım 0 mekaniği
```diff
--- a/.claude/skills/bugfix/SKILL.md
@@ ### 0.
-Before any investigation, open and read in full:
-1. `…/memory/MEMORY.md` — the index of feedback memories.
-2. Every `feedback-*.md` file referenced in that index. …
+Before any investigation: the memory index (MEMORY.md) is already in
+context every session — scan its feedback entries and OPEN the 1-3
+`feedback-*.md` files that plausibly apply to this report, in full.
+(Reading all of them each time was the v0.6.37 mechanic; the index now
+loads automatically and the point of the step is picking the right
+lessons, not re-reading every lesson.)
```
İçerik (v0.6.37 direktifi, "1-3 dersi yaz" çıktısı) aynen kalır; yalnız tarama mekaniği değişir. Operatör direktifi olduğu için reddedilebilir.

## 5. Doğrulama planı (rehber Adım 7)

- **D1-D3 (Anthropic yolu):** `anthropic_test.go`/`body_singleton_test.go` gövde iddiaları güncellenir; canlı doğrulama = Ayarlar → AI → Anthropic + `claude-opus-5` ile bir Explain: 400 yok, `refusal` yolu "boş panel" yerine hata mesajı.
- **D7 (tavanlar):** yerel modelde her prompt için önce/sonra 3'er örnek (aynı kanıt paketi): uzunluk ve kanıt-atıf sayısı; uzama olur da atıf artmazsa nitelik cümlesini sıkılaştır (sayıyı geri koyma).
- **D8:** prompt'taki katalog `chstore` tablo listesiyle karşılaştırılır (`grep -o "_5m\|_1m\|_1h" internal/chstore/store.go | sort -u`).
- **Bant dışı bağımlılıklar:** `prompt_language_test.go`, `trace_prompt_test.go`, `prompt_problem_test.go`, `prompt_antifabrication_test.go` prompt METNİNE pinli — D7/D8'de birlikte güncellenir; `tracesRowLink`-tipi kaynak-tarama testleri skill dosyalarını okumuyor.
- **Yeniden denetim:** Anthropic yolu birincil olursa F1 (baskı dili) ve F2 (tekrar) orta güvene çıkar; o gün bu rapor yeniden koşulur.
