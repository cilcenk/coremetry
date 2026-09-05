# CoSRE (AI asistan) denetimi ve planı — 2026-09-05

Operatör: "AI asistan skill'lerini yükledikten sonra iyileşme alanlarını çıkar, plan sun, sonra
implemente edelim." Kurulan skill'ler: `ai-prompt-engineering-safety-review`, `agentic-eval`,
`go-mcp-server-generator` (GitHub), `observability-llm-obs` (Elastic), `prompt-engineering-patterns`,
`llm-evaluation` (wshobson). Üç salt-okunur ajan; önceki kararlar (docs/audit/prompt-audit-2026-09-02.md,
DECISIONS v0.6.8 copilotExplain→ai_calls, "prefetch+narrate", redaksiyon yok) bulgu sayılmadı.
Her bulgu dosya:satır kanıtlı; uygulamadan önce ±10 satır bağlam okunur.

## P. Prompt güvenliği ve küçük-model desenleri

| # | Bulgu | Kanıt | Fix | Tahmin |
|---|---|---|---|---|
| P1 | **GEMİDE v0.10.403** — Üç explain prompt'unda kanıt sınırı yok; `systemRunbook` somut dashboard/komut adı istiyor, `systemIncident` "blast radius" tahmini istiyor | `internal/copilot/prompts.go:266,281,342` (kardeşi `:75` "Use ONLY facts…") | tek satır kanıt sınırı + "kanıt yetersiz" izni; `prompt_antifabrication_test` 2→5 prompt | ~1 saat |
| P2 | **GEMİDE v0.10.404** — Ham telemetri kod çitinin içine giriyor; içindeki ``` çiti erken kapatıp talimat düzlemine çıkar (`DataNotInstruction` bu yüzeylerde yok) | `anomaly/exception_context.go:535`, `explain_trace_input.go:250,311`, `api.go:8956` | prompt kopyasında ``` → ˋˋˋ kaçışı (redaksiyon değil, biçim); çit-kırma testi | ~1 saat |
| P3 | **GEMİDE v0.10.399** — İki katı-JSON yüzeyi yalnız `TrimPrefix("```json")` ile ayıklıyor; kardeşleri `salvageJSONObject` | `api/copilot_nl_query.go:159`, `copilot_ch_optimize.go:65` vs `rca_verdict.go:268` | tek `salvageJSONObject`, beş yüzey; "İşte JSON: {…}" tablo testi | ~30 dk |
| P4 | **GEMİDE v0.10.405** — Problem kanıtında sayı birimsiz, metrik adı basılmıyor; few-shot 0.14/0.05 derken üretim error_rate yüzde | `api/copilot_guided.go:2743`, `prompts.go:190`, `anomaly/anomaly.go:115` | `metrik + birim` satırı, few-shot yüzde ölçeğine; tablo testi | ~1 saat |
| P5 | **GEMİDE v0.10.406** — Niyet sınıflandırıcısında hiç örnek yok (17 niyet + 5 slot) | `prompts.go:1462-1490` | 3 örnek (servisli, servissiz, `none`); varlık kapısı | ~30 dk |
| P6 | **GEMİDE v0.10.398** — Anlatım bloğunda talimat son konumda değil; en son gelen şey telemetri | `api/copilot_drawer.go:362` (doktrin `prompts.go:888`) | blok sonuna çapa satırı; tablo testinde son satır pini | ~10 dk |
| P7 | **GEMİDE v0.10.402** — Türkçe üründe İngilizce `explain` örnekleri (katı-JSON, dil yalnız örnekten) | `prompts.go:678,681,684` | üç örneğin `explain` değeri Türkçe | ~10 dk |

## E. Değerlendirme döngüsü

| # | Bulgu | Kanıt | Fix | Tahmin |
|---|---|---|---|---|
| E1 | Model ÇIKTISI hiç ölçülmüyor; replay eval koşumu yok (golden testler yalnız metin) | `anomaly/prompt_golden_test.go:14-19` | `internal/copilot/evalset/*.json` 20-30 donmuş vaka + `go test -tags evalset` (yerel model yanı başında); skor: verdict eşleşmesi, kanıt-ID atıf oranı, `rca.ScanUnknownEntities` uydurma sayımı | ~yarım gün |
| E2 | `ai_calls` prompt sürümü/profil taşımıyor → prompt değişikliği ölçülemez | `chstore/ai_calls.go:16-35`, `copilot/copilot.go:225` | `prompt_version` (prompts.go FNV) + `profile_id` kolonu; stats group-by | ~2-3 saat |
| E3 | **GEMİDE v0.10.400** — Model başına kalite/gecikme/hata yok — çok-model seçici ölçülemiyor | `ai_calls.go:202-208` | aynı GROUP BY'a `countIf(error)`, `avg/p95(duration_ms)`, 👍 oranı | ~1 saat |
| E4 | Güven kalibre edilmiyor (`0.5*breadth+0.5*TopScore` elle) | `correlator/hypothesis.go:536`, `rca_verdict_store.go:275` | 3 güven kovası × 👍/👎 reliability satırı | ~2 saat |
| E5 | 👎 etikete dönüşüyor ama regresyon kümesine dönüşmüyor | `chstore/ai_feedback.go` `ListNegativeFeedbackCalls` yalnız panelde | "vakaya çevir" + `GET /api/ai/evalset/export` → E1 fixture | ~2 saat |
| E6 | Uydurma oranı sayılmıyor — kalkan var, sayaç yok (20 prose yüzeyinde kalkan da yok) | `anomaly/narrative_shield.go:29`, `problem_explainer.go:196` | `shieldNarrative` `unknown []string` → `ai_calls.shield_hits` | ~3 saat |
| E7 | Altın küme metne pinli, davranışa değil | `prompt_antifabrication_test.go:63`, `prompt_language_test.go:135` | E1 koşumunda prompt başına davranış vakası | E1 ile |
| E8 | Maliyet/gecikme bütçesi yok; `SamplePromptCap 4 KiB` replay sadakatini kırpıyor | `pages/AIObservability.tsx:133`, `api/ai_observability.go:23`, `ai_calls.go:43` | yüzey p95 + `ai.budget` eşiği → rozet | ~1-2 saat |

## O. LLM gözlemlenebilirliği

| # | Bulgu | Kanıt | Fix | Tahmin |
|---|---|---|---|---|
| O1 | **GEMİDE v0.10.397** — Sohbet satırlarının `duration_ms`'i 0 → /ai gecikme KPI'ları düşük | `copilot/chat.go:90` (ikizi `copilot.go:795` yazıyor) | `t0` + `RecordUsage` süre parametresi | ~30 dk |
| O2 | Ajan döngüsünün araç zinciri hiçbir yere düşmüyor (yalnız SSE); `ai.explain` span'i 4 sarmalayıcıda | `api/copilot_chat.go:597-614`, `ai_observability.go:97-233` | `ai.chat` / `ai.chat.turn` / `ai.tool` span'leri (mcpclient deseni) | ~yarım gün |
| O3 | Hata sınıfı ve refusal boyut değil (`status` + serbest metin) | `chstore/store.go:1687`, `ai/provider/anthropic.go:139` | `error_class LowCardinality` (+ `prompt_hash` aynı ALTER; dağıtık-güvenli, iki-boot) | ~yarım gün |
| O4 | TTFT ölçülmüyor; akış→buffered geri düşüşü sayılmıyor | `copilot/stream.go:28,110` | ilk delta damgası → `ttft_ms`, `stream_fallback` | ~yarım gün |
| O5 | **GEMİDE v0.10.400** — Model başına gecikme/hata oranı yok (UI: Provider/Calls/tok/cost) | `ai_calls.go:292`, `AIObservability.tsx:190` | GROUP BY'a `avg/p95`, `countIf(error)` + iki kolon (E3 ile aynı) | ~20 dk |

## M. MCP sunucu/istemci

| # | Bulgu | Kanıt | Fix | Tahmin |
|---|---|---|---|---|
| M1 | Gelen MCP araç çağrıları gözlemsiz (span/audit/ai_calls yok); giden çağrılar hem audit'li hem span'li | `mcp/mcp.go:1078`, `api/mcp_gate.go:35` | `mcp.tool` span + `source=mcp` audit | ~yarım gün |
| M2 | **GEMİDE v0.10.401** — Tel üzerindeki `tools/call`'un süre tavanı yok (uygulama içi 20 sn) | `mcp.go:1078` vs `copilot_chat.go:703` | `WithTimeout` + `ToolErrTimeout` eşlemesi | ~30 dk |
| M3 | **GEMİDE v0.10.407** — Limit'li 10 tool sessiz kırpıyor (`has_more` yok) → model "tam liste" sanıyor | `mcptools/tools.go:566,850` + 8 tool | `limit+1` oku, `has_more`+`limit` döndür | ~yarım gün |
| M4 | `notifications/cancelled` yok sayılıyor; ağır CH okuması sonuna kadar koşar | `mcp.go:944` | request-id → cancelFunc | ~yarım gün |
| M5 | **GEMİDE v0.10.408** — `deploy_impact` modelden epoch ms istiyor (skill anti-pattern'i) | `mcptools/prompts.go:213,230` | `deploy_time_iso8601` + retention dışı ret; ms geriye-uyum | ~1 saat |

## Zaten iyi olan
- Enjeksiyon çerçevesi tek sabit + beş kademede kapılı; sınıflandırıcı slotları canlı kataloğa doğruluyor; kırpma modele söyleniyor.
- Deterministik kalkanlar (LLM self-critique yerine sunucu doğrulaması); 👍 → `ConfirmedRCASignatures`; 20 yüzeyin hepsi `ai_calls`'a tek satır.
- MCP şema kalitesi: 36 tool, 98/98 property açıklamalı, `rangeWindow` zorlaması testli, 5 sınıflık hata sözleşmesi telde ve sohbette aynı.

## PLAN (onaylandı 2026-09-05; Faz 0 GEMİDE v0.10.397-402; Faz 1 GEMİDE v0.10.403-408)

| Faz | Kapsam | Toplam | Ne zaman görünür |
|---|---|---|---|
| **0 — dakikalık, davranış değişmez** | O1 sohbet süresi · P6 çapa satırı · P7 Türkçe örnek · P3 tek JSON ayıklayıcı · O5/E3 model başına gecikme+hata · M2 MCP süre tavanı | ~2.5 saat | /ai gecikme doğru; model karşılaştırması; bozuk parse azalır |
| **1 — doğruluk ve güvenlik** | P1 kanıt sınırı · P2 çit kaçışı · P4 birimli kanıt · P5 niyet örnekleri · M3 `has_more` · M5 ISO zaman | ~5 saat | uydurma dashboard/komut biter; injection yüzeyi kapanır; MCP "hepsi bu" yalanı biter |
| **2 — ölçüm altyapısı** | E2+O3 `ai_calls` kolonları (prompt_version, profile_id, error_class; tek ALTER, iki-boot) · O4 TTFT+fallback · E4 güven kalibrasyonu · E6 kalkan sayacı · E8 bütçe rozeti | ~1.5 gün | prompt değişikliği ölçülebilir; refusal/timeout ayrışır; güven güvenilirliği görünür |
| **3 — eval döngüsü** | E1 evalset + `go test -tags evalset` replay (yerel model) · E5 👎 → evalset export · E7 davranış kapıları | ~1.5 gün | prompt regresyonu CI dışı ama tekrarlanabilir; feedback kapanır |
| **4 — gözlemlenebilirlik derinliği** | O2 `ai.chat/turn/tool` span'leri · M1 gelen MCP span+audit · M4 iptal | ~1.5 gün | "sohbet neden 40 sn sürdü" ve "hangi dış ajan bütçeyi yaktı" cevaplanır |

Sıra: 0 → 1 → 2 → 3 → 4; her madde kendi vX. Toplam ~5 gün.

**Onay soruları:** (1) Faz 2'nin `ai_calls` ALTER'ı prod'da iki-boot sözleşmesiyle uygulanacak — uygun mu? (2) Evalset vakaları prod 👎 kayıtlarından mı türesin (E5 önce), yoksa elle 20 vaka mı? (3) `has_more` alanı MCP araç çıktısına EKLENİR (mevcut alanlar değişmez) — dış ajan tüketicisi varsa haber ver. (4) Faz sırası bu mu?
