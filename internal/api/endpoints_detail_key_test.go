package api

import (
	"strings"
	"testing"
	"time"
)

// endpoints_detail_key_test.go — v0.8.360 (Stage-2 slice E2). Pins the
// cache-key construction for /api/endpoints/detail + /split against the
// v0.5.187 bug class:
//
//   • (service, path) fold with a NUL separator — two different tuples
//     whose concatenation is identical must NOT share a digest (paths are
//     operator-controlled free text, so "svc" + "a|b" vs "svc|a" + "b"
//     style boundary forgeries are real inputs).
//   • windows are minute-bucketed — clicks within the same minute share
//     one upstream trip; different minutes do not.
//   • every input (sig, by) lands in the key.

func TestEndpointKeyDigestBoundaries(t *testing.T) {
	cases := []struct {
		name           string
		aSvc, aPath    string
		bSvc, bPath    string
		wantCollision  bool
	}{
		{"same tuple", "checkout", "/orders/:id", "checkout", "/orders/:id", true},
		{"different path", "checkout", "/orders/:id", "checkout", "/orders", false},
		{"different service", "checkout", "/orders", "payments", "/orders", false},
		{"boundary forgery: concat-identical tuples", "check", "out/orders", "checkout", "/orders", false},
		{"empty vs shifted", "", "checkout/orders", "checkout", "/orders", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := endpointKeyDigest(tc.aSvc, tc.aPath)
			b := endpointKeyDigest(tc.bSvc, tc.bPath)
			if (a == b) != tc.wantCollision {
				t.Errorf("digest(%q,%q)=%s digest(%q,%q)=%s, wantCollision=%v",
					tc.aSvc, tc.aPath, a, tc.bSvc, tc.bPath, b, tc.wantCollision)
			}
		})
	}
}

func TestEndpointDetailKeyInputs(t *testing.T) {
	from := time.Date(2026, 7, 7, 10, 30, 12, 0, time.UTC)
	to := time.Date(2026, 7, 7, 11, 30, 47, 0, time.UTC)
	base := endpointDetailKey("checkout", "/orders/:id", false, from, to)

	// Same minute, different seconds → SAME key (shared upstream trip).
	if got := endpointDetailKey("checkout", "/orders/:id", false,
		from.Add(20*time.Second), to.Add(-30*time.Second)); got != base {
		t.Errorf("same-minute window changed the key: %s vs %s", got, base)
	}
	// Every other input must move the key.
	variants := map[string]string{
		"sig":     endpointDetailKey("checkout", "/orders/:id", true, from, to),
		"path":    endpointDetailKey("checkout", "/orders", false, from, to),
		"service": endpointDetailKey("payments", "/orders/:id", false, from, to),
		"from":    endpointDetailKey("checkout", "/orders/:id", false, from.Add(time.Minute), to),
		"to":      endpointDetailKey("checkout", "/orders/:id", false, from, to.Add(time.Minute)),
	}
	for name, k := range variants {
		if k == base {
			t.Errorf("changing %s did not change the key: %s", name, k)
		}
	}
}

func TestEndpointSplitKeyInputs(t *testing.T) {
	from := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	a := endpointSplitKey("checkout", "/orders", false, "http.method", from, to)
	b := endpointSplitKey("checkout", "/orders", false, "host.name", from, to)
	if a == b {
		t.Errorf("split dimension not in key: %s", a)
	}
	if !strings.Contains(a, "by=http.method") {
		t.Errorf("split key missing by fragment: %s", a)
	}
}
