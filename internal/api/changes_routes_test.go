package api

import (
	"os"
	"strings"
	"testing"
)

// v0.10.545 — /api/changes: ayrı dosya + registry kaydı, cache anahtarı tüm
// girdileri taşır, gövde tool ile ortak (ListDeploymentsWindow).
func TestChangesRouteContracts(t *testing.T) {
	b, err := os.ReadFile("changes_routes.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`registerRoutesExtra("changes", (*Server).registerChangesRoutes)`,
		`"GET /api/changes", s.listChanges)`, // make audit rota tekrar sayacı test dosyasını da tarar: tam çağrı yazımı burada YOK
		`mcptools.ListDeploymentsWindow(ctx, s.mcpDeps(),`,
		`key := fmt.Sprintf("changes:v1:c=%s:ns=%s:svc=%s:lim=%d:from=%d:to=%d"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("eksik: %s", want)
		}
	}
	if api, _ := os.ReadFile("api.go"); strings.Contains(string(api), "/api/changes") {
		t.Fatal("rota api.go'ya değil kendi dosyasına")
	}
}
