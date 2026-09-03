package api

import (
	"os"
	"strings"
	"testing"
)

// trace_facets_routes_test.go — v0.10.302: iki uç deftere kayıtlı, admin
// kapılı; api.go büyümedi.
func TestTraceFacetsRoutesRegistryBacked(t *testing.T) {
	b, err := os.ReadFile("trace_facets_routes.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{`registerRoutesExtra("trace-facets"`, `"GET /api/settings/trace-facets"`, `"PUT /api/settings/trace-facets"`, "auth.RequireRole(auth.RoleAdmin, s.putTraceFacets)", `s.audit(r, "settings.trace_facets.update"`, `s.publishConfigReload(r.Context(), "trace-facets")`} {
		if !strings.Contains(src, want) {
			t.Errorf("%q yok", want)
		}
	}
	api := readAPISourceNoComments(t, "api.go")
	if strings.Contains(api, "trace-facets") {
		t.Error("api.go büyümemeli")
	}
}
