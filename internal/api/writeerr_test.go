package api

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// v0.7.13 regression — writeErr returned 500 + logged "[api] error: …" for
// EVERY error. When a client hung up (browser navigated away / React Query
// superseded a poll) a handler bubbled up context.Canceled, so coremetry-api's
// own request spans showed phantom http_status=500 — inflating self-obs
// error_rate and tripping false anomalies. context.Canceled must map to 499
// (client closed) with no body; context.DeadlineExceeded (a real server-side
// timeout) and every other error must stay 500 + JSON body so genuine
// failures still surface.
func TestWriteErr(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantBody bool // expect a JSON error body
	}{
		{"client cancel → 499, no body", context.Canceled, statusClientClosedRequest, false},
		{"wrapped client cancel → 499", fmt.Errorf("ch query: %w", context.Canceled), statusClientClosedRequest, false},
		{"deadline exceeded → 500 (real timeout)", context.DeadlineExceeded, 500, true},
		{"generic error → 500 + body", errors.New("boom"), 500, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeErr(rec, c.err)
			if rec.Code != c.wantCode {
				t.Errorf("code = %d, want %d", rec.Code, c.wantCode)
			}
			hasBody := strings.Contains(rec.Body.String(), `"error"`)
			if hasBody != c.wantBody {
				t.Errorf("body present = %v, want %v (body=%q)", hasBody, c.wantBody, rec.Body.String())
			}
		})
	}
}
