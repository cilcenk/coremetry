package api

import (
	"net/http"

	"github.com/cilcenk/coremetry/internal/auth"
)

// registerAIRoutes — v0.9.1117 (AI Assistant Faz 0.1,
// docs/plans/ai-assistant-design-2026-08-16.md).
//
// api.go BÜYÜMEYECEK kuralının AI ayağı: /api/ai/* (gözlemlenebilirlik),
// /api/settings/ai ve /api/copilot/* kayıtları buraya taşındı —
// registerRAGRoutes emsalinin (rag.go:44) devamı. Route desenleri ve
// gate'ler BİREBİR taşındı; davranış değişikliği yok (mux-collision
// testi TestMuxRoutePatterns korur).
//
// Bilinçli DIŞARIDA kalanlar:
//   - GET /api/shift — AI değil, vardiya VERİ ucu (copilot bloğunun
//     komşusuydu, api.go'da kaldı).
//   - POST /api/admin/clickhouse/optimize-query — ai_calls surface
//     etiketi ("ch-optimize", aiSurfaceFromPath özel durumu) path'e
//     bağlı; namespace taşıması Faz 0.3'ün wrapper işiyle birlikte
//     yapılacak ki /ai süreklilik kırılmasın.
//   - GET /api/{problems,anomalies}/{id}/rootcause/explain — kendi
//     domain bloklarında (problem/anomali) yaşıyorlar; LLM kullanan
//     ama domain'e ait uçlar.

// requireCopilot — LLM isteyen her ucun 503 ön-kapısı, TEK yerde
// (v0.9.1118, Faz 0.2). Bu blok daha önce 20 handler'a kopyalanmıştı
// ve v0.9.1071/1080/1101 üç ayrı kopya-regresyonuydu (kapısız yeni uç
// 500 dönüyor, FE "sunucu arızası" sanıyor — oysa AI'nın
// yapılandırılmamış olması arıza değil). Middleware ile sınıf ölür:
// yeni uç ai_routes.go'da sarılmadan kayıt OLAMAZ (ai_routes_test.go
// kaynak-pini). Mesaj şekli JSON — üç dolaşımdaki varyant teke indi;
// FE zaten status koduna bakıyor (aiErrorHint 503 = "yapılandırılmamış").
//
// Rol gate'iyle sıra: rol ÖNCE (sarımın DIŞINDA) — yetkisiz kullanıcı
// 403 alır, kurulumun AI yapılandırma durumunu 503'ten öğrenemez.
func (s *Server) requireCopilot(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.copilotReady() {
			http.Error(w, `{"error":"AI copilot not available (disabled or not configured)"}`, http.StatusServiceUnavailable)
			return
		}
		h(w, r)
	}
}

// copilotReady — "LLM çağrılabilir mi" sorusunun TEK yazılışı
// (v0.9.1129). Active() zaten nil-güvenli, ama nil kontrolünü de burada
// tutuyoruz ki çağıranlar iki koşulu hatırlamak zorunda kalmasın.
//
// İki meşru tüketici var ve ikisi FARKLI şey yapıyor:
//   - requireCopilot (yukarı): hazır değilse 503 — LLM'siz anlamsız uçlar;
//   - deliverInsight (insight.go): hazır değilse prose'u ATLAR ve
//     cevabı AIOff bayrağıyla döner — kartın deterministik yarısı
//     LLM'den bağımsız.
//
// Handler-içi üçüncü bir kapı YASAK; ai_routes_test.go
// (TestNoInlineCopilotGates) İKİ yazılışı da (`!s.copilot.Active()` ve
// `s.copilotReady()`) tarıyor — bir kapıyı yeni bir isim altında
// kopyalamak testi atlatmasın.
func (s *Server) copilotReady() bool {
	return s.copilot != nil && s.copilot.Active()
}

func (s *Server) registerAIRoutes(mux *http.ServeMux) {
	// ── AI gözlemlenebilirliği (/ai sayfası) — admin-gated okumalar ──
	mux.HandleFunc("GET /api/ai/calls", auth.RequireRole(auth.RoleAdmin, s.listAICalls))
	mux.HandleFunc("GET /api/ai/calls/{id}", auth.RequireRole(auth.RoleAdmin, s.getAICall))
	mux.HandleFunc("GET /api/ai/stats", auth.RequireRole(auth.RoleAdmin, s.aiStats))
	// v0.9.549 — guided router'ın yakalayamadığı sorular (serbest tool
	// döngüsüne düşenler). "Sıradaki intent ne olmalı" ölçüye bağlanıyor.
	mux.HandleFunc("GET /api/ai/router-gaps", auth.RequireRole(auth.RoleAdmin, s.aiRouterGaps))
	mux.HandleFunc("GET /api/ai/series", auth.RequireRole(auth.RoleAdmin, s.aiSeries))
	// v0.9.594 — RCA hakem motorunun KALİTESİ. Kardeşleri transport
	// sağlığını ölçüyor (kaç çağrı, kaç hata, kaç token); bu, cevabın
	// kendisine bakan tek uç.
	mux.HandleFunc("GET /api/ai/rca-quality", auth.RequireRole(auth.RoleAdmin, s.aiRCAQuality))
	mux.HandleFunc("GET /api/ai/rates", auth.RequireRole(auth.RoleAdmin, s.getAIRates))
	mux.HandleFunc("PUT /api/ai/rates", auth.RequireRole(auth.RoleAdmin, s.putAIRates))
	// v0.8.399 — thumbs up/down on AI answers. Any authenticated user
	// (NOT admin-gated like the reads above): whoever can chat can
	// rate the answer they got — mirrors POST /api/copilot/chat.
	mux.HandleFunc("POST /api/ai/feedback", s.postAIFeedback)
	// v0.9.423 (CoSRE fikir #6) — 👎 madenciliği: düşük puanlı cevaplar
	// prompt örnekleriyle; yeni guided-intent adayları veriden çıkar.
	mux.HandleFunc("GET /api/ai/feedback/negative", auth.RequireRole(auth.RoleAdmin, s.listNegativeAIFeedback))

	// ── AI ayarları (Settings→AI sekmesi) ──
	mux.HandleFunc("GET /api/settings/ai", auth.RequireRole(auth.RoleAdmin, s.getAISettings))
	mux.HandleFunc("PUT /api/settings/ai", auth.RequireRole(auth.RoleAdmin, s.putAISettings))

	// ── AI Copilot ──
	mux.HandleFunc("GET    /api/copilot/config", s.copilotConfig)
	// v0.6.53 — in-app agentic chatbot. SSE stream; any authenticated
	// user (the telemetry tools it calls are all read-only).
	mux.HandleFunc("POST   /api/copilot/chat", s.requireCopilot(s.copilotChat))
	// v0.8.75 — autonomous agentic root-cause analysis (same loop + tools,
	// kicked off on a subject service/problem rather than user-driven).
	mux.HandleFunc("POST   /api/copilot/analyze-service", s.requireCopilot(s.copilotAnalyzeService))
	mux.HandleFunc("POST   /api/copilot/explain-trace/{id}", s.requireCopilot(s.copilotExplainTrace))
	// v0.5.255 — natural-language → DSL filter converter. /explore
	// gets a "✦ Natural language" input that feeds this endpoint.
	mux.HandleFunc("POST   /api/copilot/nl-to-query", s.requireCopilot(s.copilotNLToQuery))
	mux.HandleFunc("POST   /api/copilot/explain-span/{traceId}", s.requireCopilot(s.copilotExplainSpan))
	mux.HandleFunc("POST   /api/copilot/explain-problem/{id}", s.requireCopilot(s.copilotExplainProblem))
	mux.HandleFunc("POST   /api/copilot/explain-incident/{id}", s.requireCopilot(s.copilotExplainIncident))
	mux.HandleFunc("POST   /api/copilot/explain-anomaly/{id}", s.requireCopilot(s.copilotExplainAnomaly))
	mux.HandleFunc("POST   /api/copilot/explain-service", s.requireCopilot(s.copilotExplainServiceHealth))
	// v0.9.1031 — ServiceCharts AI çekmecesi (onaylı mockup). explain-service
	// (AI triage) DURUYOR: guided chat'in service-health öznesi onu kullanır.
	// Bu yüzey grafiklere özgü ve daha geniş kanıt taşır; ai_calls'ta
	// "explain-charts" olarak AYRI görünür.
	mux.HandleFunc("POST   /api/copilot/explain-charts", s.requireCopilot(s.copilotExplainCharts))
	mux.HandleFunc("POST   /api/copilot/explain-shift", s.requireCopilot(s.explainShift))  // v0.9.1071 — /shift ✨
	mux.HandleFunc("POST   /api/copilot/explain-alert-noise", s.requireCopilot(s.explainAlertNoise)) // v0.9.1080 — F3.3 gürültü anlatıcısı ✨
	mux.HandleFunc("POST   /api/copilot/explain-log-patterns", s.requireCopilot(s.explainLogPatterns)) // v0.9.1100 — F3.5 desen anlatıcısı ✨
	mux.HandleFunc("POST   /api/copilot/runbook/{id}", s.requireCopilot(s.copilotRunbook))
	mux.HandleFunc("POST   /api/copilot/compare-traces", s.requireCopilot(s.copilotCompareTraces))
	mux.HandleFunc("POST   /api/copilot/deploy-impact", s.requireCopilot(s.copilotDeployImpact))
	mux.HandleFunc("POST   /api/copilot/explain-slo/{id}", s.requireCopilot(s.copilotExplainSLO))
	mux.HandleFunc("POST   /api/copilot/explain-slow-query", s.requireCopilot(s.copilotExplainSlowQuery))
	// v0.9.414 (operatör istegi) — exception grubu kök-sebep: örnek
	// trace + trace logları + deploy penceresi otomatik prefetch'lenir,
	// kanıt trace/span'leri deterministik döner (copilot_exception.go).
	mux.HandleFunc("POST   /api/copilot/explain-exception/{fp}", s.requireCopilot(s.copilotExplainException))
	mux.HandleFunc("POST   /api/copilot/suggest-service-tags", auth.RequireAnyRole(editorRoles, s.requireCopilot(s.copilotSuggestServiceTags)))

	// ── Insight kartları (Faz 2.1, v0.9.1129) ──
	//
	// requireCopilot İLE SARILMIYOR ve bu KASITLI — gerekçenin tamamı
	// insight.go'nun dosya başındaki "NEDEN /api/copilot/ DEĞİL"
	// bölümünde. Özeti: kart AI kapalıyken de tam değerli (sinyaller +
	// linkler deterministik), dolayısıyla 503 yanlış cevap olurdu;
	// namespace ayrı ki "/api/copilot/ altındaki her şey LLM ister"
	// kuralı istisnasız kalsın. TestInsightRouteNotCopilotGated bu
	// kararı pinler — sarmak testi kırmızı yakar.
	//
	// GET: okuma. Akan varyant `?stream=1` ile (explain'le aynı bayrak).
	// Rol kapısı yok: projelediği veri (problem/exception) viewer'a açık.
	mux.HandleFunc("GET    /api/insight/{kind}/{id}", s.getInsight)
}
