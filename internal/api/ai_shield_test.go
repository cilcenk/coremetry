package api

import (
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
}

func TestEveryExplainWrapperCarriesShield(t *testing.T) {
	b, err := os.ReadFile("ai_observability.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if n := strings.Count(src, "Shield:     aiShield"); n < 3 {
		t.Fatalf("CallMeta kurucularının 3'ü Shield taşımalı, %d", n)
	}
	if n := strings.Count(src, "meta.Shield = aiShield"); n < 2 {
		t.Fatalf("surface/JSONSurface sarmalayıcıları varsayılan Shield vermeli, %d", n)
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
