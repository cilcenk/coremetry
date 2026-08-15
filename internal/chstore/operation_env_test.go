package chstore

import (
	"testing"
	"time"
)

// v0.9.1039 — env(a): the /service Operations table (GetOperationSummary)
// honours the global Topbar env picker. operation_summary_5m carries no
// deploy_env dimension, so a non-empty env must disqualify the MV fast path
// and route to the raw-spans branch (which adds `deploy_env = ?`), exactly
// as servicesUseMV does for /api/services (env_gate_test.go) and
// getDatabasesRaw does for /databases.
//
// This is the value+branch class the codebase keeps burning on: a gate that
// works for the empty case but silently ignores the filter on the off-axis
// branch. Pinning the pure gate here means env can never silently fall back
// to the (env-blind) MV.
func TestOperationsUseMV_EnvDisqualifies(t *testing.T) {
	cases := []struct {
		name   string
		window time.Duration
		env    string
		want   bool
	}{
		{"wide window, no env — MV", time.Hour, "", true},
		{"env set — raw (MV has no deploy_env)", time.Hour, "uat", false},
		{"env set on a wide window — still raw", 24 * time.Hour, "prep", false},
		{"sub-5m window — raw even unfiltered", 2 * time.Minute, "", false},
		{"sub-5m + env — raw", 2 * time.Minute, "int", false},
		{"exactly 5m, no env — MV", 5 * time.Minute, "", true},
		{"exactly 5m + env — raw", 5 * time.Minute, "uat", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := operationsUseMV(tc.window, tc.env); got != tc.want {
				t.Fatalf("operationsUseMV(%v, %q) = %v, want %v",
					tc.window, tc.env, got, tc.want)
			}
		})
	}
}
