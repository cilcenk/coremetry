package api

import "testing"

// v0.10.343 — "Trace ID…" kutusuna kimlik yazıldı: 32-hex trace id, değilse arama.
func TestIdentityFromTraceIDParam(t *testing.T) {
	if s, tid := identityFromTraceIDParam("0FCD70A94BA1F695EA079750E71A7C10", ""); tid != "0fcd70a94ba1f695ea079750e71a7c10" || s != "" {
		t.Fatalf("32-hex trace id (küçük harf): %q %q", s, tid)
	}
	if s, tid := identityFromTraceIDParam(" 060209Dr8G0037156551 ", ""); tid != "" || s != "060209Dr8G0037156551" {
		t.Fatalf("kimlik → search (ham, harf duyarlı): %q %q", s, tid)
	}
	if s, tid := identityFromTraceIDParam("060209Dr8G0037156551", "POST /x"); tid != "" || s != "POST /x" {
		t.Fatalf("search doluysa korunur, traceId düşer: %q %q", s, tid)
	}
	if s, tid := identityFromTraceIDParam("", "q"); tid != "" || s != "q" {
		t.Fatalf("boş traceId no-op: %q %q", s, tid)
	}
}
