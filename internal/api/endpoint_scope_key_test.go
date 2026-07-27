package api

import (
	"strings"
	"testing"
	"time"
)

// v0.9.306 — operator-reported silent-filter bug on /endpoints.
//
// With env=uat selected the TABLE showed uat numbers, but clicking a
// row opened a drawer that aggregated EVERY env for that route — prod
// included. Two different truths on one screen, and nothing said which
// was which. The drawer's reads are raw spans and deploy_env is a typed
// column, so the scope costs a conjunct; it simply was never carried.
//
// Applying the filter is only half the fix. The other half is the cache
// key: env/cluster change the ANSWER, so an entry shared across scopes
// would serve one operator's uat drawer to another looking at prod —
// the v0.5.187 cross-poisoning class, i.e. the same inconsistency
// re-introduced through the cache after being fixed in the query.
func TestEndpointDetailKeyCarriesScope(t *testing.T) {
	from := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	key := func(env, cluster string) string {
		return endpointDetailKey("checkout", "/api/orders", false, from, to, env, cluster)
	}

	base := key("", "")
	for _, tc := range []struct{ env, cluster, label string }{
		{"uat", "", "env alone"},
		{"prod", "", "a DIFFERENT env"},
		{"", "eu-west", "cluster alone"},
		{"uat", "eu-west", "both"},
	} {
		if got := key(tc.env, tc.cluster); got == base {
			t.Errorf("%s must not share a cache entry with the unscoped drawer", tc.label)
		}
	}
	if key("uat", "") == key("prod", "") {
		t.Fatal("two different envs share one entry — whichever lands first decides what the other sees")
	}
	if !strings.Contains(key("uat", "eu-west"), "env=uat") ||
		!strings.Contains(key("uat", "eu-west"), "clu=eu-west") {
		t.Fatalf("key must carry both scope values, got %q", key("uat", "eu-west"))
	}
	if key("uat", "") != key("uat", "") {
		t.Fatal("the key must be deterministic")
	}
}

// The split read is the same drawer, the same scope, the same hazard.
func TestEndpointSplitKeyCarriesScope(t *testing.T) {
	from := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	key := func(env, cluster string) string {
		return endpointSplitKey("checkout", "/api/orders", false, "http.status_code", from, to, env, cluster)
	}
	if key("uat", "") == key("", "") {
		t.Fatal("a scoped split must not share an entry with the unscoped one")
	}
	if key("uat", "") == key("prod", "") {
		t.Fatal("two different envs share one split entry")
	}
	// And the split dimension still separates entries — the new fields
	// must not have displaced it.
	a := endpointSplitKey("checkout", "/api/orders", false, "http.status_code", from, to, "uat", "")
	b := endpointSplitKey("checkout", "/api/orders", false, "peer.service", from, to, "uat", "")
	if a == b {
		t.Fatal("two different split dimensions share one entry")
	}
}
