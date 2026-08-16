package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// v0.9.1118 (AI Faz 0.2) kaynak-pinleri — 503 ön-kapısı sınıfının
// mezar taşları. Bu blok 20 handler'a kopyalanmıştı ve üç kez
// (v0.9.1071/1080/1101) kapısız yeni uç regresyonu yaşandı. İki kural:
//
//  1. ai_routes.go'daki HER POST /api/copilot/ kaydı requireCopilot
//     ile sarılı olmalı — sarımsız yeni uç bu testte kırmızı yanar.
//  2. Paket içinde handler-içi `!s.copilot.Active()` kapısı kalmamalı;
//     izinli istisnalar middleware'in kendisi (ai_routes.go) ve
//     domain'e gömülü rootcause verdict gövdesi (rootcause.go).
func TestRequireCopilotRouteCoverage(t *testing.T) {
	b, err := os.ReadFile("ai_routes.go")
	if err != nil {
		t.Fatalf("ai_routes.go okunamadı: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, `"POST   /api/copilot/`) &&
			!strings.Contains(line, "s.requireCopilot(") {
			t.Errorf("requireCopilot sarımı olmayan copilot route'u: %s", strings.TrimSpace(line))
		}
	}
}

func TestNoInlineCopilotGates(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"ai_routes.go": true, "rootcause.go": true}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || allowed[f] {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", f, err)
		}
		if strings.Contains(string(b), "!s.copilot.Active()") {
			t.Errorf("%s: handler-içi copilot kapısı — route'u ai_routes.go'da requireCopilot ile sar", f)
		}
	}
}
