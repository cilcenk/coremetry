package api

// trace_routes_test.go — v0.10.275 pinleri: rota api.go'da DEĞİL defterde;
// cache anahtarı v3; yanıt analysis taşıyor (BuildTraceAnalysis bağlı —
// feedback-tested-but-unreachable sınıfı).

import (
	"os"
	"strings"
	"testing"
)

func TestTraceRouteLivesInRegistry(t *testing.T) {
	api, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(api), `"GET /api/traces/{id}"`) {
		t.Error(`api.go hâlâ "GET /api/traces/{id}" kaydediyor — rota trace_routes.go'ya taşındı`)
	}
	if strings.Contains(string(api), "func (s *Server) getTrace(") {
		t.Error("getTrace handler'ı hâlâ api.go'da")
	}
	if _, ok := extraRouteRegistrars["trace"]; !ok {
		t.Fatal(`defterde "trace" kaydı yok (init eksik)`)
	}
	src, err := os.ReadFile("trace_routes.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, want := range []string{`"GET /api/traces/{id}"`, `key := "trace:v3:" + id`, `out["analysis"] = chstore.BuildTraceAnalysis(spans, capped)`} {
		if !strings.Contains(body, want) {
			t.Errorf("trace_routes.go eksik: %q", want)
		}
	}
	if strings.Contains(body, `"trace:v2:"`) {
		t.Error("eski cache anahtarı kalmış")
	}
}
