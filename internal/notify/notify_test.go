package notify

import (
	"testing"
	"time"
)

// v0.7.22 — per-runbook notification channel types. runbookNotifyTypes maps a
// runbook's selected channel TYPES to a lookup set; an EMPTY selection MUST
// default to email — both the sensible default and back-compat for runbooks
// created before the selector (their notify_channels column is empty). A
// regression dropping that empty->email default would silently STOP completion
// notifications on every existing runbook.
func TestRunbookNotifyTypes(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string // must be present
		deny []string // must be absent
	}{
		{"nil defaults to email", nil, []string{"email"}, []string{"slack", "webhook"}},
		{"empty slice defaults to email", []string{}, []string{"email"}, []string{"slack"}},
		{"blank/whitespace-only defaults to email", []string{"", "  "}, []string{"email"}, []string{"slack"}},
		{"explicit single", []string{"slack"}, []string{"slack"}, []string{"email", "webhook"}},
		{"explicit multi", []string{"email", "webhook"}, []string{"email", "webhook"}, []string{"slack", "teams"}},
		{"case-insensitive + trimmed", []string{"Slack", " WEBHOOK "}, []string{"slack", "webhook"}, []string{"email"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := runbookNotifyTypes(c.in)
			for _, w := range c.want {
				if !got[w] {
					t.Errorf("want %q present, set=%v", w, got)
				}
			}
			for _, d := range c.deny {
				if got[d] {
					t.Errorf("want %q absent, set=%v", d, got)
				}
			}
		})
	}
}

// TestZoomHTTPClientHasTimeout locks in the per-request deadline
// guard on the package-level Zoom client. Pre-v0.4.79 the Zoom
// integration used http.DefaultClient (no timeout) — a regional
// outage at Zoom would hang every alert-sender goroutine and
// stack the evaluator's queue.
//
// The unit test stays cheap (no network) so it runs in every
// `go test ./...` cycle and catches anyone who reverts to the
// default client by accident.
func TestZoomHTTPClientHasTimeout(t *testing.T) {
	if zoomHTTPClient == nil {
		t.Fatal("zoomHTTPClient must be non-nil")
	}
	if zoomHTTPClient.Timeout <= 0 {
		t.Fatal("zoomHTTPClient must have a positive Timeout — a regional Zoom outage would otherwise stall every alert send")
	}
	if zoomHTTPClient.Timeout > 60*time.Second {
		t.Fatalf("zoomHTTPClient.Timeout=%s is too long; evaluator runs every minute, send batches must clear faster",
			zoomHTTPClient.Timeout)
	}
}
