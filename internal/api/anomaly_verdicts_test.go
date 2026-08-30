package api

// anomaly_verdicts_test.go — v0.10.181: kimlik listesi saf ayrıştırıcı; rota
// kaydı api.go dışında (tek satır çağrı).

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestSplitIDs(t *testing.T) {
	if got := splitIDs(" a, b ,,a,c "); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("splitIDs: %v", got)
	}
	if got := splitIDs(""); len(got) != 0 {
		t.Fatalf("boş: %v", got)
	}
	many := strings.Repeat("x,", 300)
	if got := splitIDs(strings.Join(strings.Split(strings.TrimSuffix(many, ","), ","), ",")); len(got) != 1 {
		t.Fatalf("kopyalar elenmedi: %d", len(got))
	}
	parts := make([]string, 0, 260)
	for i := 0; i < 260; i++ {
		parts = append(parts, "id"+strings.Repeat("0", 1)+string(rune('a'+i%26))+strings.Repeat("z", i%7)+string(rune('0'+i%10)))
	}
	if got := splitIDs(strings.Join(parts, ",")); len(got) > 200 {
		t.Fatalf("tavan 200 aşıldı: %d", len(got))
	}
}

func TestAnomalyVerdictRoutesOutsideAPIGo(t *testing.T) {
	apiGo, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(apiGo), "s.registerAnomalyVerdictRoutes(mux)") {
		t.Fatal("registerAnomalyVerdictRoutes api.go'dan çağrılmıyor — rotalar 200 + boş ekran olur")
	}
	// (rootcause/verdict RCA rotası api.go'da meşru — yalnız anomali kararı uçları aranır)
	if strings.Contains(string(apiGo), "/api/anomalies/verdicts") || strings.Contains(string(apiGo), "/api/anomalies/{id}/verdict") {
		t.Fatal("verdict rotası api.go'ya yazılmış (api.go büyümeyecek kuralı)")
	}
	if !strings.Contains(string(apiGo), "EnrichAnomaliesWithVerdicts") {
		t.Fatal("events ucu kararı eklemiyor")
	}
}
