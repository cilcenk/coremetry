package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// v0.8.346 (HA audit H6) — regression: NO role guard existed despite
// main.go's comment claiming one. Every role registered POST /v1/*, but
// only ingest pods start the consumers — a collector pointed at an
// api-role pod (mis-wired Service, wrong endpoint) had its Exports
// 200-OK'd into channels nobody drained: a silent telemetry black hole.
// Off-role pods now answer 501 with a pointer at the ingest Service —
// a refusal the collector logs and retries elsewhere.
func TestOtlpRouteGuard(t *testing.T) {
	accepted := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("ingest role passes through", func(t *testing.T) {
		rec := httptest.NewRecorder()
		otlpRouteGuard(false, accepted).ServeHTTP(rec, httptest.NewRequest("POST", "/v1/traces", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200 passthrough", rec.Code)
		}
	})

	t.Run("off-role answers 501 with guidance, never OK", func(t *testing.T) {
		rec := httptest.NewRecorder()
		otlpRouteGuard(true, accepted).ServeHTTP(rec, httptest.NewRequest("POST", "/v1/traces", nil))
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("code = %d, want 501", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "ingest") {
			t.Fatalf("body must point the operator at the ingest Service, got %q", rec.Body.String())
		}
	})
}
