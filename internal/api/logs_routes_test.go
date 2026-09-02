package api

import (
	"os"
	"strings"
	"testing"
)

// logs_routes_test.go — v0.10.281: taşınan dokuz /api/logs kalıbı ADLA
// pinli. TestMuxRoutePatterns çakışmayı görür, eksikliği görmez; bir
// kalıp düşerse istemci 404 değil "boş 200" görür.
var logsRoutePatterns = []string{
	`"GET /api/logs"`,
	`"POST /api/logs/search"`,
	`"GET /api/logs/stream"`,
	`"GET /api/logs/timeseries"`,
	`"GET /api/logs/fields"`,
	`"GET /api/logs/fieldstats"`,
	`"GET /api/logs/field-values"`,
	`"GET /api/logs/context"`,
	`"GET /api/logs/templates"`,
}

func TestLogsRoutesLiveInLogsRoutesFile(t *testing.T) {
	lr, err := os.ReadFile("logs_routes.go")
	if err != nil {
		t.Fatal(err)
	}
	api := readAPISourceNoComments(t, "api.go")
	for _, p := range logsRoutePatterns {
		if !strings.Contains(string(lr), p) {
			t.Errorf("logs_routes.go %s kalıbını taşımıyor — rota DÜŞTÜ", p)
		}
		if strings.Contains(api, p) {
			t.Errorf("api.go hâlâ %s kaydediyor — çift kayıt ya da taşıma yarım", p)
		}
	}
	if !strings.Contains(string(lr), `registerRoutesExtra("logs"`) {
		t.Error("logs_routes.go deftere kayıt olmuyor")
	}
}
