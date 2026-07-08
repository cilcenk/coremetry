package chstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// v0.8.385 — env-separation Phase 2: /services and /endpoints consume
// the global Topbar ?env= picker with cluster-parity RAW-FALLBACK
// semantics (KARAR KAYDI, docs/env-separation-audit.md): a non-empty
// env disqualifies the MV fast-path and lands as a `deploy_env = ?`
// conjunct on the bounded raw-spans read. NO MV gained an env
// dimension. These tests pin, without a live ClickHouse:
//
//   • the raw services WHERE carries the env conjunct (+ its arg),
//     and only when env is set;
//   • the raw endpoints filter fragment does the same, coexisting
//     with the cluster conjunct;
//   • an env filter forces the endpoints dispatcher off the MV;
//   • GetEndpointsMV REFUSES env instead of silently returning
//     all-env numbers (the silent-unfiltered class the audit bans).
//
// Style follows env_filter_test.go (v0.8.383, Phase 1 — the
// TraceFilter.Env precedent).

func TestServicesListWhere_EnvConjunct(t *testing.T) {
	from := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	to := from.Add(1 * time.Hour)
	s := &Store{}

	cases := []struct {
		name    string
		cluster string
		env     string
	}{
		{"env only", "", "uat"},
		// Cluster + env stack — an operator triaging one cluster's uat
		// slice needs BOTH conjuncts, not either-or.
		{"env + cluster", "prod-eu", "uat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wc := s.servicesListWhere(context.Background(), 0, from, to, "", nil, tc.cluster, tc.env)
			sql := wc.sql()
			if !strings.Contains(sql, "deploy_env = ?") {
				t.Fatalf("WHERE must carry the env conjunct; got %q", sql)
			}
			found := false
			for _, a := range wc.args {
				if a == tc.env {
					found = true
				}
			}
			if !found {
				t.Fatalf("args must carry the env value %q; got %#v", tc.env, wc.args)
			}
			if tc.cluster != "" && !strings.Contains(sql, " = ?") {
				t.Fatalf("cluster conjunct must survive next to env; got %q", sql)
			}
			// The raw read stays time-bounded regardless of filters —
			// the cluster path's exact bound, inherited.
			if !strings.Contains(sql, "time >= ?") {
				t.Fatalf("raw services WHERE must stay time-bounded; got %q", sql)
			}
		})
	}
}

func TestServicesListWhere_EmptyEnvMeansAll(t *testing.T) {
	from := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	wc := (&Store{}).servicesListWhere(context.Background(), 0, from, from.Add(time.Hour), "", nil, "", "")
	if strings.Contains(wc.sql(), "deploy_env") {
		t.Fatalf("empty env must add no deploy_env predicate; got %q", wc.sql())
	}
}

func TestEndpointsRawFilters_EnvConjunct(t *testing.T) {
	s := &Store{}
	const pathExpr = "http_route"

	sql, args := s.endpointsRawFilters(EndpointsQuery{Env: "uat"}, pathExpr)
	if !strings.Contains(sql, " AND deploy_env = ?") {
		t.Fatalf("env must emit its deploy_env conjunct; got %q", sql)
	}
	if len(args) != 1 || args[0] != "uat" {
		t.Fatalf("args must carry exactly the env value; got %#v", args)
	}

	// Cluster + env stack, args in placeholder order (cluster's
	// conjunct precedes env's in the fragment).
	sql, args = s.endpointsRawFilters(EndpointsQuery{Cluster: "prod-eu", Env: "prep"}, pathExpr)
	if !strings.HasPrefix(sql, " AND "+s.clusterExpr()+" = ?") {
		t.Fatalf("cluster conjunct must lead the fragment; got %q", sql)
	}
	if !strings.HasSuffix(sql, " AND deploy_env = ?") {
		t.Fatalf("env conjunct must survive next to cluster; got %q", sql)
	}
	if len(args) != 2 || args[0] != "prod-eu" || args[1] != "prep" {
		t.Fatalf("args must be [cluster, env] in placeholder order; got %#v", args)
	}
}

func TestEndpointsRawFilters_EmptyEnvMeansAll(t *testing.T) {
	sql, args := (&Store{}).endpointsRawFilters(EndpointsQuery{}, "http_route")
	if sql != "" || len(args) != 0 {
		t.Fatalf("no filters must emit no conjuncts; got %q %#v", sql, args)
	}
}

func TestEndpointsQuery_EnvForcesRaw(t *testing.T) {
	cases := []struct {
		name string
		q    EndpointsQuery
		want bool
	}{
		{"no filters — MV fast path", EndpointsQuery{}, false},
		{"env forces raw", EndpointsQuery{Env: "uat"}, true},
		{"cluster forces raw (pre-existing)", EndpointsQuery{Cluster: "prod-eu"}, true},
		{"both force raw", EndpointsQuery{Cluster: "prod-eu", Env: "uat"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.q.forcesRaw(); got != tc.want {
				t.Fatalf("forcesRaw() = %v, want %v", got, tc.want)
			}
		})
	}
}

// GetEndpointsMV must refuse an env filter LOUDLY — spanmetrics_1m
// has no deploy_env dimension, so honouring the call would silently
// return all-environment numbers under an env filter. The guard runs
// before any conn use, so a zero-value Store is safe here.
func TestGetEndpointsMV_RefusesEnv(t *testing.T) {
	_, err := (&Store{}).GetEndpointsMV(context.Background(), EndpointsQuery{
		From: time.Now().Add(-time.Hour), To: time.Now(), Env: "uat",
	})
	if err == nil {
		t.Fatal("GetEndpointsMV must refuse a non-empty Env — spanmetrics_1m has no env dimension")
	}
	if !errors.Is(err, errEndpointsMVEnv) {
		t.Fatalf("want errEndpointsMVEnv, got %v", err)
	}
}
