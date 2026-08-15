package mcptools

import (
	"os"
	"strings"
	"testing"
)

// v0.9.1061 regresyon pini — get_problem_root_cause map'i elle kurulur;
// hipoteze eklenen bir alan burada anılmadıkça MCP gövdesinden sessizce
// DÜŞER (v0.9.1057'nin exemplar'ı tam böyle düştü: CH satırında dolu,
// MCP cevabında yok — canlı doğrulama yakaladı; struct'ın JSON-tag testi
// chstore'da yeşildi çünkü bu map struct'ı serialize etmiyor). Kaynak
// pini: exemplar anahtarı map kurulumunda geçmek zorunda.
func TestRootCauseToolCarriesExemplar(t *testing.T) {
	src, err := os.ReadFile("tools.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `out["exemplarTraceId"] = h.ExemplarTraceID`) {
		t.Fatal("get_problem_root_cause map'i exemplarTraceId taşımıyor — hipotez alanı MCP'den düşer")
	}
}
