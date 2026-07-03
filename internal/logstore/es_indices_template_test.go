package logstore

import (
	"context"
	"testing"
	"time"
)

// v0.8.231 — operator's ES layout is per-service indices
// (app-<service>.<namespace>); a configured IndexTemplate short-
// circuits service-pinned queries to the concrete index instead of the
// app-* fan-out. Per the unit-mixing rule these tests exercise EVERY
// placeholder combination at ship time: an untested branch here means
// a silently wrong (possibly cluster-wide) index target.

func TestResolveIndexTemplate(t *testing.T) {
	cases := []struct {
		name             string
		tpl, service, ns string
		want             string
	}{
		{"both placeholders resolved", "app-{service}.{namespace}", "checkout", "prod", "app-checkout.prod"},
		{"namespace unresolved → wildcard", "app-{service}.{namespace}", "checkout", "", "app-checkout.*"},
		{"no service → template path off", "app-{service}.{namespace}", "", "prod", ""},
		{"no template → template path off", "", "checkout", "prod", ""},
		{"service-only template", "app-{service}-*", "checkout", "", "app-checkout-*"},
		{"namespace-only template", "logs-{namespace}", "checkout", "prod", "logs-prod"},
		{"namespace-only template, unresolved", "logs-{namespace}", "checkout", "", "logs-*"},
		{"no placeholders at all", "static-index", "checkout", "prod", "static-index"},
		{"service repeated", "{service}/{service}", "a", "", "a/a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveIndexTemplate(c.tpl, c.service, c.ns); got != c.want {
				t.Fatalf("resolveIndexTemplate(%q, %q, %q) = %q, want %q",
					c.tpl, c.service, c.ns, got, c.want)
			}
		})
	}
}

func TestQueryIndicesTemplateShortCircuit(t *testing.T) {
	ctx := context.Background()

	// Service-pinned + template + resolver → the one concrete index.
	// idxCache seeded EMPTY-but-fresh: the v0.8.239 existence check
	// skips on an empty inventory, and a stale cache would otherwise
	// hit the network (nil client) in this unit test.
	s := &ESStore{cfg: ESConfig{Index: "app-*", IndexTemplate: "app-{service}.{namespace}"}}
	s.idxCache.fetched = time.Now()
	s.NamespaceResolver = func(_ context.Context, svc string) string {
		if svc == "checkout" {
			return "prod"
		}
		return ""
	}
	got := s.queryIndices(ctx, Filter{Service: "checkout"})
	if len(got) != 1 || got[0] != "app-checkout.prod" {
		t.Fatalf("template short-circuit = %v, want [app-checkout.prod]", got)
	}

	// Unknown service → wildcard namespace, still ONE family (never the
	// bare app-* fan-out, never an empty list = "all" to ES).
	got = s.queryIndices(ctx, Filter{Service: "mystery"})
	if len(got) != 1 || got[0] != "app-mystery.*" {
		t.Fatalf("unresolved-ns = %v, want [app-mystery.*]", got)
	}

	// Nil resolver behaves like unresolved.
	s2 := &ESStore{cfg: ESConfig{Index: "app-*", IndexTemplate: "app-{service}.{namespace}"}}
	got = s2.queryIndices(ctx, Filter{Service: "checkout"})
	if len(got) != 1 || got[0] != "app-checkout.*" {
		t.Fatalf("nil resolver = %v, want [app-checkout.*]", got)
	}

	// No service filter → template path off, falls back to the pattern
	// (zero window keeps it off the _cat path so no HTTP client needed).
	got = s2.queryIndices(ctx, Filter{})
	if len(got) != 1 || got[0] != "app-*" {
		t.Fatalf("no-service fallback = %v, want [app-*]", got)
	}

	// No template → pattern path regardless of service.
	s3 := &ESStore{cfg: ESConfig{Index: "app-*"}}
	got = s3.queryIndices(ctx, Filter{Service: "checkout"})
	if len(got) != 1 || got[0] != "app-*" {
		t.Fatalf("no-template fallback = %v, want [app-*]", got)
	}
}
