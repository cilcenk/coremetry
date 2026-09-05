package api

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// v0.10.432 (CoSRE router boşlukları D8) — ?src= soneki: yalnız whitelist
// ("nudge") ve yalnız explain-* yüzeylerinde; üç ctx kurucusu da istek
// üzerinden (path değil) etiket türetir.
func TestAISurfaceFromRequestSrc(t *testing.T) {
	cases := map[string]string{
		"/api/copilot/explain-trace/abc?src=nudge":          "explain-trace:nudge",
		"/api/copilot/explain-trace/abc?stream=1&src=nudge": "explain-trace:nudge",
		"/api/copilot/explain-trace/abc":                    "explain-trace",
		"/api/copilot/explain-trace/abc?src=evil":           "explain-trace",
		"/api/copilot/chat?src=nudge":                       "chat",
		"/api/insight/problem/p1?src=nudge":                 "insight-problem",
	}
	for target, want := range cases {
		r := httptest.NewRequest("POST", target, nil)
		if got := aiSurfaceFromRequest(r); got != want {
			t.Errorf("%s → %q, want %q", target, got, want)
		}
	}
	src, err := os.ReadFile("ai_observability.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "surface := aiSurfaceFromPath(r.URL.Path)") {
		t.Fatal("ctx kurucuları aiSurfaceFromRequest(r) kullanmalı — src soneki düşer")
	}
	if n := strings.Count(string(src), "surface := aiSurfaceFromRequest(r)"); n < 3 {
		t.Fatalf("üç ctx kurucusu da istekten türetmeli, %d", n)
	}
}
