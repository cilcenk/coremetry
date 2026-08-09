package api

import (
	"strings"
	"testing"
	"time"
)

// endpoints_callers_key_test.go — v0.9.839. Pins the cache key of the
// NEW /api/endpoints/callers read against the v0.5.187 cross-poisoning
// class, the same way endpoints_detail_key_test.go pins its siblings.
//
// Original symptom of that class: a key that dropped an input served
// one operator's answer to a different question. Every input this
// handler branches on — the (service, path) NUL-separated digest, the
// signature flag, env, cluster, the row limit and the minute-bucketed
// window — must therefore change the key. limit is the one that is
// easy to forget: it changes the answer's LENGTH, so sharing an entry
// across limits truncates a panel silently.

func TestEndpointCallersKeyVariesWithEveryInput(t *testing.T) {
	from := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	base := endpointCallersKey("checkout", "/orders/:id", false, from, to, "", "", 20)

	cases := []struct {
		name string
		key  string
	}{
		{"service", endpointCallersKey("payments", "/orders/:id", false, from, to, "", "", 20)},
		{"path", endpointCallersKey("checkout", "/orders", false, from, to, "", "", 20)},
		{"sig", endpointCallersKey("checkout", "/orders/:id", true, from, to, "", "", 20)},
		{"env", endpointCallersKey("checkout", "/orders/:id", false, from, to, "uat", "", 20)},
		{"cluster", endpointCallersKey("checkout", "/orders/:id", false, from, to, "", "eu-1", 20)},
		{"limit", endpointCallersKey("checkout", "/orders/:id", false, from, to, "", "", 5)},
		{"from", endpointCallersKey("checkout", "/orders/:id", false, from.Add(-time.Hour), to, "", "", 20)},
		{"to", endpointCallersKey("checkout", "/orders/:id", false, from, to.Add(time.Hour), "", "", 20)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.key == base {
				t.Errorf("%s does not change the cache key: %s", tc.name, tc.key)
			}
		})
	}
}

// The boundary-forgery guarantee has to hold on THIS key too: paths are
// operator-controlled free text, so a crafted path must not be able to
// impersonate another (service, path) tuple by shifting the separator.
func TestEndpointCallersKeyBoundaryForgery(t *testing.T) {
	from := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	a := endpointCallersKey("check", "out/orders", false, from, to, "", "", 20)
	b := endpointCallersKey("checkout", "/orders", false, from, to, "", "", 20)
	if a == b {
		t.Fatalf("concat-identical tuples share a key: %s", a)
	}
}

// Within one minute the key is stable — that is what makes the 60s TTL
// actually hit when a page re-mounts. Across minutes it is not.
func TestEndpointCallersKeyMinuteBucketed(t *testing.T) {
	from := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	same := endpointCallersKey("checkout", "/o", false, from.Add(17*time.Second), to, "", "", 20)
	base := endpointCallersKey("checkout", "/o", false, from, to, "", "", 20)
	if same != base {
		t.Errorf("same-minute keys differ:\n %s\n %s", base, same)
	}
	next := endpointCallersKey("checkout", "/o", false, from.Add(time.Minute), to, "", "", 20)
	if next == base {
		t.Errorf("next-minute key collides with base: %s", base)
	}
	if !strings.HasPrefix(base, "endpoints-callers:") {
		t.Errorf("key lost its namespace prefix: %s", base)
	}
}
