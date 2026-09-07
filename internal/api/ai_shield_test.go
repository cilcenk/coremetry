package api

import (
	"context"
	"os"
	"strings"
	"testing"
)

// v0.10.421 — E6: ortak sayaç ve her sarmalayıcının onu taşıması.
func TestAIShieldCountsUnshownNames(t *testing.T) {
	if got := aiShield("checkout-svc p99 yüksek", "checkout-svc ghost-gateway'e bağlı"); got != 1 {
		t.Fatalf("1 uydurma bekleniyor, %d", got)
	}
	if got := aiShield("checkout-svc", "checkout-svc"); got != 0 {
		t.Fatalf("gösterilen ad sayılmaz, %d", got)
	}
	// v0.10.431 — canlı katalog tohumu: gerçek ad prompt'ta geçmese de
	// uydurma değildir; genel teknik terimler sayılmaz.
	if got := aiShieldWith([]string{"payment-api"})("checkout-svc yavaş", "checkout-svc payment-api'ye bağımlı, rate-limiting devrede, p95-p99 farkı büyük"); got != 0 {
		t.Fatalf("katalog adı + teknik terimler sayılmamalı, %d", got)
	}
	if got := aiShieldWith([]string{"payment-api"})("", "ghost-gateway çöktü"); got != 1 {
		t.Fatalf("katalog dışı uydurma sayılmalı, %d", got)
	}
	var s *Server
	if s.aiShieldFor(context.Background(), "chat-intent") != nil || s.aiShieldFor(context.Background(), "chat-general") != nil {
		t.Fatal("sohbet sınıflandırıcısı/genel sohbet varsayılan kalkan almaz (prompt = yalnız soru)")
	}
	if (&Server{}).aiShieldFor(context.Background(), "explain-problem") == nil || !shieldCountsSurface("chat-guided") {
		t.Fatal("kanıt taşıyan yüzeyler sayılır; katalogsuz sunucu tohumsuz sayaç")
	}
}

func TestEveryExplainWrapperCarriesShield(t *testing.T) {
	b, err := os.ReadFile("ai_observability.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	// v0.10.533 — karar matrisi tek kurucuda: yedi sarmalayıcı aiCall'a delege
	// eder, Shield tohumlaması iki yerde (explainCallCtx + aiCall) yazılıdır.
	if n := strings.Count(src, "s.aiCall("); n != 7 {
		t.Fatalf("yedi sarmalayıcı da aiCall'a delege etmeli, %d", n)
	}
	if !strings.Contains(src, "meta.Shield = s.aiShieldFor(ctx, surface)") || !strings.Contains(src, "Shield:     s.aiShieldFor(r.Context(), surface)") {
		t.Fatal("aiCall ve explainCallCtx yüzey kapılı tohumlu Shield vermeli")
	}
	if strings.Contains(src, "= aiShield\n") || strings.Contains(src, "Shield:     aiShield,") {
		t.Fatal("tohumsuz aiShield sarmalayıcılarda kalmamalı (v0.10.431)")
	}
	for _, f := range []string{"../anomaly/problem_explainer.go", "../anomaly/exception_explainer.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "Shield:") {
			t.Errorf("%s arka plan açıklayıcısı Shield taşımıyor", f)
		}
	}
}
