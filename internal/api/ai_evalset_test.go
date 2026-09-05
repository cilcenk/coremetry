package api

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.423 — E5: vaka üretimi saf ve tablolu; yetim satır düşer, kırpma
// bayrağı dürüst, surface haritası fikstür doğrulamasıyla uyumlu.
func TestEvalsetCaseFrom(t *testing.T) {
	fb := chstore.NegativeFeedbackCall{ExchangeID: "x1", UserEmail: "op@example.test", Comment: "dashboard uydurdu"}
	call := &chstore.AICallEvalRow{Surface: "explain-problem", Provider: "openai", Model: "qwen3:8b",
		CreatedAt: time.Unix(1_700_000_000, 0), PromptChars: 120, ResponseChars: 40, Prompt: strings.Repeat("p", 120), Response: strings.Repeat("r", 40),
		PromptVersion: "abcd", ProfileID: "local"}
	c, ok := evalsetCaseFrom(fb, call)
	if !ok || c.ID != "fb-x1" || c.Surface != "Problem" || c.Why != "dashboard uydurdu" || c.Provenance.Truncated {
		t.Fatalf("vaka: ok=%v %+v", ok, c)
	}
	if _, ok := evalSystemPrompt(c.Surface); !ok {
		t.Fatalf("üretilen surface %q fikstür haritasında çözülmüyor", c.Surface)
	}
	call.PromptChars = 9000 // örnek 4 KiB'de kırpıldı
	if c, _ := evalsetCaseFrom(fb, call); !c.Provenance.Truncated {
		t.Fatal("kırpık prompt truncated=true olmalı")
	}
	if _, ok := evalsetCaseFrom(fb, nil); ok {
		t.Fatal("çağrı kaydı yoksa vaka üretilmez")
	}
	if _, ok := evalsetCaseFrom(fb, &chstore.AICallEvalRow{Surface: "chat", Response: "  "}); ok {
		t.Fatal("boş cevaplı yetim satır düşmeli")
	}
	if c, _ := evalsetCaseFrom(chstore.NegativeFeedbackCall{ExchangeID: "x2"}, &chstore.AICallEvalRow{Surface: "weird", Response: "r"}); c.Why == "" || c.Surface != "Unknown" {
		t.Fatalf("yorumsuz 👎 varsayılan why, bilinmeyen etiket Unknown: %+v", c)
	}
}

// Her bilinen etiket fikstür haritasında çözülen bir surface'e gider.
func TestEvalSurfaceLabelsResolve(t *testing.T) {
	for _, label := range []string{"explain-trace", "explain-span", "explain-problem", "problem-auto-explain", "explain-exception",
		"explain-incident", "explain-anomaly", "explain-service", "runbook", "compare-traces", "deploy-impact", "explain-slo",
		"nl-to-query", "ch-optimize", "rootcause-verdict", "explain-charts", "chat-general", "chat", "chat-intent",
		"explain-trace:nudge"} { // v0.10.432 (D8)
		if _, ok := evalSystemPrompt(evalSurfaceFromLabel(label)); !ok {
			t.Errorf("%s → %s çözülmüyor", label, evalSurfaceFromLabel(label))
		}
	}
}

// Rota ai_routes.go'da, api.go büyümez, audit var, copilot kapısı yok (veri okuması).
func TestAIEvalsetRoutesRegisteredOutsideAPIGo(t *testing.T) {
	routes, _ := os.ReadFile("ai_routes.go")
	if !strings.Contains(string(routes), "registerAIEvalsetRoutes(mux)") {
		t.Fatal("evalset rotası ai_routes.go'dan kaydedilmiyor")
	}
	apiGo, _ := os.ReadFile("api.go")
	if strings.Contains(string(apiGo), "/api/ai/evalset") {
		t.Fatal("evalset rotası api.go'ya girmiş")
	}
	src, _ := os.ReadFile("ai_evalset.go")
	if !strings.Contains(string(src), `s.audit(r, "ai.evalset.export"`) || strings.Contains(string(src), "requireCopilot(") {
		t.Fatal("audit yok ya da copilot kapısı var (veri okuması kapısız olmalı)")
	}
	// v0.10.431 — exchangeId nokta okuma: pencere/200 tavanından sonra Go
	// süzgeci yok; bulunamayan kimlik 404.
	if !strings.Contains(string(src), "NegativeFeedbackCallByExchange(r.Context(), only)") || strings.Contains(string(src), "fb.ExchangeID != only") ||
		!strings.Contains(string(src), "http.StatusNotFound") {
		t.Fatal("exchangeId nokta okuma + 404 yolu yok")
	}
}
