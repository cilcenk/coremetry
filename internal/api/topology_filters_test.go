package api

import "testing"

// v0.5.326 — locks the curated infra-path patterns introduced by
// v0.5.310's "noise" filter on /api/topology/service. False
// positives sink legitimate edges; false negatives reintroduce
// the clutter the operator complained about ("karman çorman"
// kafka cache-refresh edges).

func TestLooksLikeInfraEdge_AllInfra(t *testing.T) {
	cases := [][]string{
		{"/health"},
		{"/healthz"},
		{"/readyz", "/livez"},
		{"GET /actuator/prometheus", "POST /actuator/health"},
		{"/-/metrics"},
		{"/metrics", "/-/healthy"},
		{"/ping"},
		{"/heartbeat", "/keepalive"},
		{"/.well-known/openid-configuration"},
	}
	for i, top := range cases {
		if !looksLikeInfraEdge(top) {
			t.Errorf("case %d: expected infra, got non-infra for %v", i, top)
		}
	}
}

func TestLooksLikeInfraEdge_MixedKeepsEdge(t *testing.T) {
	// Real business operations alongside one health probe — edge
	// MUST survive (any non-infra label keeps the edge visible).
	mixed := []string{"/api/payment", "/health"}
	if looksLikeInfraEdge(mixed) {
		t.Errorf("mixed edge sunk: %v", mixed)
	}
}

func TestLooksLikeInfraEdge_PureBusiness(t *testing.T) {
	biz := []string{
		"POST /api/v1/checkout",
		"GET /catalog/items",
		"OrderService/CreateOrder",
	}
	if looksLikeInfraEdge(biz) {
		t.Errorf("business edge flagged as infra: %v", biz)
	}
}

func TestLooksLikeInfraEdge_Empty(t *testing.T) {
	// No labels → can't classify as infra (don't drop blindly).
	if looksLikeInfraEdge(nil) {
		t.Error("nil top labels should not classify as infra")
	}
	if looksLikeInfraEdge([]string{}) {
		t.Error("empty top labels should not classify as infra")
	}
}

func TestLooksLikeInfraEdge_CaseInsensitive(t *testing.T) {
	// CH might return labels in any case; the operator's HTTP
	// route normalisation isn't strict either.
	top := []string{"/HEALTH", "/PING"}
	if !looksLikeInfraEdge(top) {
		t.Errorf("uppercase infra not detected: %v", top)
	}
}
