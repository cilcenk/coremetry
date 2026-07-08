package api

import (
	"strings"
	"testing"
	"time"
)

// v0.8.385 — env-separation Phase 2: /api/services + /api/endpoints
// consume the global ?env= picker. These tests pin the api-layer half
// of the slice:
//
//   • servicesUseMV — a non-empty env (like cluster before it,
//     v0.5.372) disqualifies the service_summary_5m fast path; the
//     read falls to the bounded raw-spans branch (KARAR KAYDI:
//     cluster-parity raw-fallback, NO MV changes);
//   • both list cache keys carry env (hash-ALL-inputs, the v0.5.187
//     class) — without it an env-filtered response would cross-poison
//     the unfiltered one inside the same 30s bucket.

func TestServicesUseMV_EnvDisqualifies(t *testing.T) {
	cases := []struct {
		name    string
		window  time.Duration
		cluster string
		env     string
		want    bool
	}{
		{"wide window, no filters — MV", time.Hour, "", "", true},
		{"env set — raw", time.Hour, "", "uat", false},
		{"cluster set — raw (pre-existing)", time.Hour, "prod-eu", "", false},
		{"both set — raw", time.Hour, "prod-eu", "uat", false},
		{"sub-5m window — raw even unfiltered", 2 * time.Minute, "", "", false},
		{"exactly 5m — MV", 5 * time.Minute, "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := servicesUseMV(tc.window, tc.cluster, tc.env); got != tc.want {
				t.Fatalf("servicesUseMV(%v, %q, %q) = %v, want %v",
					tc.window, tc.cluster, tc.env, got, tc.want)
			}
		})
	}
}

func TestServicesListKey_CarriesEnv(t *testing.T) {
	key := func(env string) string {
		return servicesListKey(false, 50, 0, "b", "", "spanCount", "desc", "", "", "", env, false)
	}
	uat, prep, all := key("uat"), key("prep"), key("")
	if uat == prep || uat == all || prep == all {
		t.Fatalf("distinct envs must produce distinct keys: uat=%q prep=%q all=%q", uat, prep, all)
	}
	if !strings.Contains(uat, "env=uat") {
		t.Fatalf("key must carry the env value; got %q", uat)
	}
	// Same inputs → same key (stability half of the v0.5.187 contract).
	if key("uat") != uat {
		t.Fatal("servicesListKey must be deterministic")
	}
}

func TestEndpointsListKey_CarriesEnv(t *testing.T) {
	key := func(env string) string {
		return endpointsListKey("b", "", "", "", env, 500, false, false, "calls", "desc")
	}
	uat, prep, all := key("uat"), key("prep"), key("")
	if uat == prep || uat == all || prep == all {
		t.Fatalf("distinct envs must produce distinct keys: uat=%q prep=%q all=%q", uat, prep, all)
	}
	if !strings.Contains(uat, "env=uat") {
		t.Fatalf("key must carry the env value; got %q", uat)
	}
	if key("uat") != uat {
		t.Fatal("endpointsListKey must be deterministic")
	}
}
