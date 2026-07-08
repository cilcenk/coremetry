# Coremetry AI — Bits AI / Davis CoPilot Bar Audit (Stage 1)

2026-07-08. Operatör brief'i: "Coremetry AI'ı geliştir". Her bulgu file:line'a
dayanıyor; kod değişikliği YOK — Stage 2 onayla başlar. ⚠ TASARIM HEDEFİ:
prod copilot modeli **qwen3.5-2b** (vLLM 0.24, KServe gateway, air-gapped,
~30s route timeout, Türkçe zorunlu v0.8.374). Küçük model degrade yolu değil
BİRİNCİL hedef: çok-turlu tool döngüsü ve serbest strict-JSON şüpheli;
tercih edilen şekil = **sunucu deterministik toplar, model anlatır**
(analyze-service deseni, copilot_aianalyze.go).

## Yönetici özeti

1. Envanter geniş: 13 Türkçe prose ✨Explain yüzeyi + chat (11 tool'lu agentik
   SSE) + 2 Türkçe-native JSON analiz + 3 strict-JSON yardımcı + arka planda
   problem-auto-explain. Hepsi tek sarmalayıcıdan (`s.copilotExplain`,
   ai_observability.go:95) ai_calls'a düşüyor → /ai sayfası çalışıyor.
2. **En değerli varlık LLM'siz:** RootCauseSynthesizer worker'ı saf
   correlator.Synthesize ile hipotez üretip persist ediyor
   (rootcause_worker.go:13-42) — ribbon + on-demand narration hazır. Kuzey
   yıldızı ("anomaly→root-cause otomatik birleştirme") %70 kurulmuş; eksik
   olan tek şey bu hipotezin problem AISummary'sine ve chat'e AKMASI.
3. **2B kırılganlıkları:** chat tool-loop'u (5 tur × 11 şema,
   copilot_chat.go:37) küçük modelde büyük risk; chat max_tokens=1500
   (chat.go:185,294) reasoning modelde düşünme fazında tükenir (Explain 4096,
   copilot.go:394); 13 prose prompt İngilizce talimat + Türkçe cevap karışımı.
4. Bar'a göre net eksikler: token streaming yok (cevap bütün gövde,
   copilot_chat.go:26-29), feedback/thumbs yok, konuşma hafızası ephemeral,
   MCP/chat tool'ları env-körü (v0.8.383-387 picker'ı yalnız UI'a girdi),
   NL alarm-kuralı ve NL→panel yok (frontier-model işi, aşağıda demote).

## 1. ENVANTER

| Yüzey | Yer | Model ihtiyacı | Türkçe | 2B uyum |
|---|---|---|---|---|
| 13 prose ✨Explain: trace/span/problem/incident/anomaly/service-health/runbook/compare-traces/deploy-impact/slo-burn/slow-query/rootcause-narration (+servis-tags JSON) | rotalar api.go:827-849; prompt'lar copilot.go:809-1412 dip | tek-atış, uzun ctx yok | `AnswerInTurkish` ×13 (copilot.go:819; prompt_language_test.go) — talimatlar İngilizce | İYİ (tek-atış); prompt dili riski §3 |
| copilotChat SSE agentik loop | copilot_chat.go:55-188; UI CopilotChat.tsx (AppShell'de global) | tool-calling + 5 tur + 40 msg (:36-38), tool başına 20s (:195), max_tokens **1500** (chat.go:185,294) | sistem prompt'a ekli (copilot_chat.go:46) | **ZAYIF** — 2B tool seçimi güvenilmez; 1500 token reasoning'de yetmez |
| MCP tool seti (7 çekirdek + 4 pivot v0.8.333) | mcptools/tools.go:82-98, pivots.go | JSON şema okuma | n/a | şemalar basit ama 11 şema ctx yükü; **env parametresi YOK** (tools.go:302 env'siz `GetServicesFiltered`; env'li varyant repo.go:814 kullanılmıyor) |
| analyze-service ("AI ile analiz et") | copilot_aianalyze.go:133; prompt :28-65 | tek-atış strict-JSON + few-shot; sunucu toplar (:198-242), halüsinasyon post-check (:356), Redis 5dk cache (:150) | Türkçe-NATIVE prompt | **EN İYİ desen** — 2B için referans şekil |
| analyze (sistem geneli verdict) | copilot_analyze.go:14-59 (v0.8.75) | tek-atış strict-JSON, fleet özeti | Türkçe-native | İYİ (JSON şema orta boy; few-shot yok) |
| NL→query (/explore) | copilot_nl_query.go:60-148; prompt copilot.go:1228 (İngilizce) | strict-JSON; sunucu op/preset doğrular (:115-145), raw fallback | çıktı JSON; `explain` alanı dilsiz | ORTA — doğrulama sağlam, kalite değişken |
| CH query optimize (admin) | copilot_ch_optimize.go; rota api.go:416 | strict-JSON {optimized, explanation} | dilsiz | ZAYIF — SQL yeniden yazımı 2B'de güvenilmez; düşük trafik, dokunma |
| problem-auto-explain (arka plan) | anomaly/problem_explainer.go:14-43; 30s tik, critical-only, batch 16, evidence bundle (:117-131) | tek-atış prose | Explain prompt'ları üzerinden | İYİ — prefetch-narrate zaten |
| RootCause hipotez worker + ribbon | rootcause_worker.go:13-42 (LLM'siz, batch 64); persist rootcause_hypothesis.go:63; ribbon :304; narration rotası api.go:620,694 + prompt copilot.go:1379 | narration tek-atış | narration Türkçe | MÜKEMMEL — deterministik çekirdek modelden bağımsız |
| AI gözlemlenebilirlik | ai_calls → /ai (AIObservability.tsx:85-137; App.tsx:133); RecordUsage chat.go:111-143 | — | — | feedback alanı YOK |
| Sağlayıcı katmanı | anthropic/github/openai-compat (copilot.go:40-43); 180s client (:226); thought_signature raw replay v0.8.373 (chat.go:42-55,248-265); vLLM reasoning/api-key v0.8.384 (chat.go:319-355,408-414) | — | — | vLLM uyumu güncel |

Not: `systemException` prompt'u yalnız MCP prompts'ta (mcptools/prompts.go:158) — HTTP yüzeyi yok.

## 2. GAP — Davis CoPilot / Bits AI barına göre

| Yetenek | Durum | Not + 2B gerçekçiliği |
|---|---|---|
| Proaktif olay anlatımı | PARTIAL — critical problem'lara AISummary (problem_explainer.go); incident/anomaly için yok, problem evrildikçe güncellenmez | tek-atış narrate = 2B-uygun; genişletme ucuz |
| Chat'te kılavuzlu çok-adımlı inceleme | EXISTS ama serbest tool-loop | **2B'de temelden kırılgan** — sunucu-prefetch "hazır akışlar"a dönüştürülmeli (§3) |
| Konuşma hafızası / takip soruları | EPHEMERAL — frontend state, reload'da kaybolur (copilot_chat.go:30-34) | persist etmek modelden bağımsız; düşük öncelik |
| Token streaming | YOK — SSE yalnız `step`+`answer` (bütün gövde) (copilot_chat.go:26-29); Explain'ler düz POST | vLLM stream:true destekler; yavaş 2B'de algılanan hızın ANA kaldıracı + 30s gateway timeout sigortası |
| Feedback döngüsü (👍/👎 → /ai) | YOK — ai_calls'ta alan yok, UI'da buton yok | modelden bağımsız; küçük model kalite takibi için kritik |
| Otomatik kök-neden birleştirme | GÜÇLÜ çekirdek (worker+ribbon+narration) ama AISummary/chat'e akmıyor | LLM'siz çekirdek = 2B ortamının en büyük avantajı; birleştirme saf plumbing |
| Tool'ların env/cluster farkındalığı | YOK — global env picker (v0.8.383-387) yalnız UI; tools.go env-körü | deterministik parametre işi, model etkisi yok |
| NL→dashboard/panel | YOK | **frontier-model işi** — panel şeması JSON'u 2B'de güvenilmez; DEMOTE (uzak vadede opsiyonel bulut modeli arkasına) |
| NL alarm-kuralı yazımı | YOK (alert_tuning.go deterministik) | **frontier-model işi**; DEMOTE — yanlış alarm kuralı = operasyonel risk |

## 3. KÜÇÜK MODEL STRATEJİSİ (qwen3.5-2b birincil hedef)

2B'de kırılanlar: (a) çok-turlu tool seçimi — 11 şema + 5 tur; vLLM tool
parser'ı emit etse bile yanlış tool/arg olasılığı yüksek; (b) serbest
strict-JSON — mevcut savunmalar iyi (fence-strip + brace-extract
copilot_aianalyze.go:319-336, raw fallback copilot_nl_query.go:100) ama
kalite few-shot'suz düşer; (c) chat'in 1500 token bütçesi reasoning
fazında biter (Explain'de aynı ders 4096'ya çıkarılmıştı, copilot.go:390-394);
(d) İngilizce talimat + Türkçe cevap kod-değiştirme yükü.

Degrade/ana yol — **analyze-service deseninin genelleştirilmesi**:
1. Niyet yönlendirme: önce deterministik (regex/anahtar kelime: servis adı,
   "neden yavaş", "hata", "problem"), çözülemezse tek küçük enum-JSON
   sınıflandırma çağrısı (2B'nin yapabildiği en güvenli JSON: tek alan, kapalı küme).
2. Sunucu ilgili bağlamı KENDİSİ toplar: buildServiceContext
   (copilot_aianalyze.go:198), ListProblems, persist edilmiş RootCause
   hipotezi (GetHypothesis), evidence bundle (problem_explainer Phase 7).
3. Model TEK atışta Türkçe anlatır (few-shot'lu, Türkçe-native prompt).
4. Tool-loop tamamen kalkmıyor: provider'a göre kapı — frontier model
   (test ortamındaki Gemini) yapılandırıldığında serbest loop açık kalır;
   Settings'e "kılavuzlu mod" anahtarı (system_settings, invariant #6).

## 4. FAZLI PLAN (onay sonrası; her dilim kendi v0.8.X'i; sıralama = operatör değeri × kuzey yıldızı × 2B gerçekçiliği)

- **A1 (~yarım gün) — Kök-neden × AISummary füzyonu:** problem_explainer
  prompt'una persist edilmiş hipotezi enjekte et (TopSuspect/skor/aday yolu +
  RecentDeploy, GetHypothesis zaten var) ve çıktıyı analyze-service tarzı
  Türkçe-native yapılandırılmış habere çevir. "Anomaly→root-cause otomatik"
  vaadinin görünür hali; tek-atış = 2B-güvenli. Dosyalar:
  problem_explainer.go, copilot.go (prompt), Problems UI'da mevcut alan.
- **A2 (~saat) — Chat token bütçesi düzeltmesi:** chat max_tokens 1500→4096
  (chat.go:185,294) — Explain'in v0.8.384 dersinin chat'e uygulanmamış hali;
  bugfix sayılabilir, hemen çıkar.
- **A3 (~1 gün) — Chat "kılavuzlu mod" (sunucu-prefetch):** §3'teki akış;
  deterministik router + 3-4 hazır toplama (servis sağlığı / problem özeti /
  kök-neden / log hatası) + tek narrate. Tool-loop provider-kapılı kalır.
  Dosyalar: copilot_chat.go, system_settings anahtarı, CopilotChat.tsx
  (akış çipleri mevcut `step` UI'ını yeniden kullanır).
- **A4 (~yarım-1 gün) — Token streaming:** openai-compat yolda stream:true →
  SSE `delta` event'leri; Anthropic yolu sonra. 30s gateway timeout'una da
  sigorta (Q2'ye bağlı). Dosyalar: chat.go/copilot.go, copilot_chat.go,
  CopilotChat.tsx.
- **A5 (~yarım gün) — Feedback döngüsü:** ai_calls'a `feedback` kolonu +
  PATCH endpoint + chat/Explain'de 👍/👎 + /ai'da kırılım. Küçük model kalite
  regresyonlarını yakalamanın tek yolu.
- **A6 (~yarım gün) — Türkçe-native prompt geçişi + golden set:** 13 prose
  prompt'u Türkçe-native'e çevir; 5-6 sabit girdiyle önce/sonra karşılaştırma
  (Q3 onayına bağlı; prompt_language_test.go güncellenir).
- **A7 (~yarım gün) — Tool'lara env/cluster:** list_services /
  get_service_health / query_metric'e `env`/`cluster` argümanı
  (GetServicesFilteredIn'e geçiş, repo.go:814) + chat sistem prompt'una not.
- **Sonrası:** incident'lara proaktif anlatım (A1 deseni), konuşma persist.
  **DEMOTE (frontier gerektirir, 2B'de yapma):** NL→panel, NL alarm kuralı.

## 5. ONAY ÖNCESİ AÇIK SORULAR (bloklayan)

1. **vLLM gateway tool-calling + streaming:** prod uçta `tools` parser'ı açık
   mı ve `stream:true` gateway'den geçiyor mu (yoksa buffer'lıyor mu)? Cevap
   A3'ün şekli (tool yolu tamamen kapalı mı) ve A4'ün önceliğini belirler.
2. **30s route timeout:** banka ops tarafında sabit mi? Sabitse A4 (stream)
   öne çekilir ve 5-turlu her akış zaten imkânsız → A3 zorunlu hale gelir.
3. **Prompt dili:** 13 İngilizce prose prompt'un Türkçe-native yeniden
   yazımına onay var mı (A6)? Riski: mevcut Gemini test çıktıları değişir.
4. **Feedback kapsamı:** 👍/👎 yalnız chat'te mi, tüm ✨Explain yüzeylerinde mi
   (ai_calls satırıyla eşleme her yüzeyde mümkün)?

**Stage 1 burada bitiyor — onayını bekliyorum.**
