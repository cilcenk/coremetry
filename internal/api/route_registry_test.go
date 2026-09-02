package api

// route_registry_test.go — v0.10.247: defter sözleşmesi. (1) sayım pini:
// kaynak ağacındaki registerRoutesExtra( çağrısı sayısı = defter boyu
// (bir init() unutulmuş/çift kayıt panic'i sessizce yutulmamış);
// (2) api.go "preferences" içermez (kayıt defter üzerinden, api.go
// büyümedi); (3) GET /api/preferences/x buildMux'ta çözülür.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRouteRegistryCountPin(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "route_registry.go" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		calls += strings.Count(string(b), "registerRoutesExtra(")
	}
	if calls != len(extraRouteRegistrars) {
		t.Fatalf("kaynakta %d registerRoutesExtra( çağrısı, defterde %d kayıt — init() eksik ya da çift ad", calls, len(extraRouteRegistrars))
	}
	if len(extraRouteRegistrars) < 3 {
		t.Fatalf("defter en az rollup+annotation+preferences taşımalı, %d", len(extraRouteRegistrars))
	}
}

func TestRouteRegistryKeepsAPIGoOut(t *testing.T) {
	b, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, bad := range []string{"preferences", "registerRollupRoutes(", "registerAnnotationRoutes("} {
		if strings.Contains(src, bad) {
			t.Errorf("api.go %q içeriyor — kayıt route_registry.go defterinden olmalı", bad)
		}
	}
}

func TestRouteRegistryResolvesPreferences(t *testing.T) {
	s := &Server{}
	mux := s.buildMux()
	req := httptest.NewRequest(http.MethodGet, "/api/preferences/x", nil)
	_, pattern := mux.Handler(req)
	if pattern != "GET /api/preferences/{key}" {
		t.Fatalf("kalıp %q, istenen GET /api/preferences/{key}", pattern)
	}
}
