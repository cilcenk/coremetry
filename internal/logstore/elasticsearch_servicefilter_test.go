package logstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// v0.8.239 — operator-reported: the service-detail Logs tab was the
// ONLY empty log surface on an ECS-template-mapped external cluster.
// Two independent roots, both silent-0:
//
//  1. The service filter was a single term on `<field>.keyword` — that
//     sub-field only exists on DYNAMIC text mappings; ECS templates
//     type service.name as keyword directly, so the term matched
//     nothing. The filter must try BOTH shapes.
//  2. A misconfigured index template (separator mismatch vs the real
//     naming) resolves to a non-existent concrete index, which
//     allow_no_indices turns into a clean 0. queryIndices must verify
//     a concrete resolution against the index inventory and fall back
//     to the pattern.

func TestServiceFilterTriesBothFieldShapes(t *testing.T) {
	s := &ESStore{}
	s.cfg.defaults()
	s.fields = s.cfg.Fields
	raw, err := json.Marshal(s.buildQuery(Filter{Service: "facex-bpm"}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	q := string(raw)
	for _, want := range []string{
		`"service.name.keyword":"facex-bpm"`, // dynamic mapping
		`"service.name":"facex-bpm"`,         // ECS keyword-typed (guarded)
		// operator-directed fallback: pipeline stamps the workload on
		// the container name (both spellings, both shapes).
		`"kubernetes.container.name.keyword":"facex-bpm"`,
		`"kubernetes.container.name":"facex-bpm"`,
		`"kubernetes.container_name.keyword":"facex-bpm"`,
		`"kubernetes.container_name":"facex-bpm"`,
		// every bare term must ride with its exists-guard so dynamic
		// mappings never token-match through the analyzed field.
		`"must_not":[{"exists":{"field":"service.name.keyword"}}]`,
		`"must_not":[{"exists":{"field":"kubernetes.container_name.keyword"}}]`,
		`"minimum_should_match":1`,
	} {
		if !strings.Contains(q, want) {
			t.Errorf("missing %s in: %s", want, q)
		}
	}
}

func TestIndexKnown(t *testing.T) {
	names := []string{
		"app-checkout.prod",                        // plain index
		"app-facex-bpm-int-000079",                 // rollover child
		"app-orders.prod-2026.07.03",               // dated child
		".ds-app-identityhub-int-2026.06.24-000391", // data-stream backing
	}
	cases := []struct {
		resolved string
		want     bool
	}{
		{"app-checkout.prod", true},        // exact
		{"app-facex-bpm-int", true},        // rollover parent
		{"app-orders.prod", true},          // dated parent
		{"app-identityhub-int", true},      // data-stream name via .ds- backing
		{"app-checkout.uat", false},        // wrong namespace
		{"app-identityhub", false},         // prefix of a LONGER stream name must NOT match…
		{"app-facex", false},               // …same (app-facex-bpm-int is a different stream)
	}
	for _, c := range cases {
		if got := indexKnown(names, c.resolved); got != c.want {
			t.Errorf("indexKnown(%q) = %v, want %v", c.resolved, got, c.want)
		}
	}
}

func TestQueryIndicesTemplateFallsBackWhenResolvedUnknown(t *testing.T) {
	ctx := context.Background()
	s := &ESStore{cfg: ESConfig{Index: "app-*", IndexTemplate: "app-{service}.{namespace}"}}
	s.NamespaceResolver = func(context.Context, string) string { return "prod" }
	// Seed the inventory cache (fresh) so no HTTP round-trip happens.
	s.idxCache.names = []string{".ds-app-checkout-prod-2026.07.03-000001"}
	s.idxCache.fetched = time.Now()

	// Template resolves app-checkout.prod (dot separator) but the cluster
	// names streams app-checkout-prod (dash) → unknown → pattern fallback.
	got := s.queryIndices(ctx, Filter{Service: "checkout"})
	if len(got) != 1 || got[0] != "app-*" {
		t.Fatalf("mis-separator template must fall back to the pattern, got %v", got)
	}

	// Matching naming → template short-circuit still wins.
	s2 := &ESStore{cfg: ESConfig{Index: "app-*", IndexTemplate: "app-{service}-{namespace}"}}
	s2.NamespaceResolver = func(context.Context, string) string { return "prod" }
	s2.idxCache.names = []string{".ds-app-checkout-prod-2026.07.03-000001"}
	s2.idxCache.fetched = time.Now()
	got = s2.queryIndices(ctx, Filter{Service: "checkout"})
	if len(got) != 1 || got[0] != "app-checkout-prod" {
		t.Fatalf("known resolution must keep the template index, got %v", got)
	}

	// Empty inventory (listing failed / no _cat privilege) must never
	// block a template — the check can't be the reason it stops working.
	s3 := &ESStore{cfg: ESConfig{Index: "app-*", IndexTemplate: "app-{service}.{namespace}"}}
	s3.NamespaceResolver = func(context.Context, string) string { return "prod" }
	s3.idxCache.names = []string{}
	s3.idxCache.fetched = time.Now()
	got = s3.queryIndices(ctx, Filter{Service: "checkout"})
	if len(got) != 1 || got[0] != "app-checkout.prod" {
		t.Fatalf("empty inventory must skip the existence check, got %v", got)
	}
}
