package api

import (
	"math"
	"os"
	"strings"
	"testing"
)

// v0.10.411 — CoSRE denetimi E8: bütçe doğrulaması saf ve tablolu.
func TestNormalizeAIBudget(t *testing.T) {
	cases := map[string]struct {
		in AIBudget
		ok bool
	}{
		"boş = tavan yok": {AIBudget{}, true},
		"token + p95":     {AIBudget{DailyTokens: 1_000_000, P95Ms: 4000}, true},
		"maliyet":         {AIBudget{DailyCostUSD: 12.5}, true},
		"negatif maliyet": {AIBudget{DailyCostUSD: -1}, false},
		"NaN maliyet":     {AIBudget{DailyCostUSD: math.NaN()}, false},
		"sonsuz maliyet":  {AIBudget{DailyCostUSD: math.Inf(1)}, false},
		"kuruş altı":      {AIBudget{DailyCostUSD: 0.001}, false},
	}
	for name, c := range cases {
		_, err := normalizeAIBudget(c.in)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v, ok bekleniyor=%v", name, err, c.ok)
		}
	}
	if (AIBudget{}).configured() || !(AIBudget{P95Ms: 1}).configured() {
		t.Fatal("configured: sıfır blob yapılandırılmamış, tek tavan yapılandırılmış")
	}
}

// Rota kaydı ai_routes.go'da; api.go büyümez (ai_settings_profiles_test emsali).
func TestAIBudgetRoutesRegisteredOutsideAPIGo(t *testing.T) {
	routes, err := os.ReadFile("ai_routes.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(routes), "registerAIBudgetRoutes(mux)") {
		t.Fatal("bütçe rotaları ai_routes.go'dan kaydedilmiyor")
	}
	apiGo, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(apiGo), "/api/ai/budget") {
		t.Fatal("bütçe rotası api.go'ya girmiş (api.go büyümeyecek kuralı)")
	}
	src, _ := os.ReadFile("ai_budget.go")
	if !strings.Contains(string(src), `s.audit(r, "settings.ai_budget.update"`) {
		t.Fatal("PUT audit kaydı yok")
	}
}
