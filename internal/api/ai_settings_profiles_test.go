package api

// ai_settings_profiles_test.go — v0.10.175: görünüm anahtar sızdırmaz,
// varsayılan işaretli; rota kayıtları TestMuxRoutePatterns'a girer (aynı
// mux); kayıt satırı api.go'da değil ai_routes.go'da.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/copilot"
)

func TestAIProfileViewsNoKeyEcho(t *testing.T) {
	views := aiProfileViews([]copilot.ModelProfile{
		{ID: "a", Provider: "openai", APIKey: "sekret", Model: "m", BaseURL: "http://a/v1"},
		{ID: "b", Provider: "anthropic", Model: "c"},
	}, "b")
	raw, _ := json.Marshal(views)
	if strings.Contains(string(raw), "sekret") || strings.Contains(string(raw), "apiKey") {
		t.Fatalf("anahtar yanıta sızdı: %s", raw)
	}
	if !views[0].HasKey || views[1].HasKey || !views[1].Default || views[0].Default {
		t.Fatalf("görünüm bayrakları: %+v", views)
	}
}

func TestAIProfileRoutesRegisteredOutsideAPIGo(t *testing.T) {
	routes, err := os.ReadFile("ai_routes.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(routes), "registerAISettingsProfileRoutes(mux)") {
		t.Fatal("profil rotaları ai_routes.go'dan kaydedilmiyor")
	}
	apiGo, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(apiGo), "/api/settings/ai/profiles") {
		t.Fatal("profil rotası api.go'ya girmiş (api.go büyümeyecek kuralı)")
	}
}

func TestResolveLegacyAPIKey(t *testing.T) {
	if resolveLegacyAPIKey("", "cur", false) != "cur" || resolveLegacyAPIKey("new", "cur", false) != "new" || resolveLegacyAPIKey("", "cur", true) != "" || resolveLegacyAPIKey("new", "cur", true) != "" {
		t.Fatal("boş = korunur, clearKey = sil, dolu = yeni")
	}
}
