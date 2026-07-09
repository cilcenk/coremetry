package chstore

import (
	"strings"
	"testing"
)

// v0.8.400 — env-separation Phase 4: GetLogs' env conjunct
// (logsEnvChainSQL). Pins the logs-local two-spelling rule:
//
//   - BOTH semconv spellings, new key (deployment.environment.name)
//     coalescing BEFORE the legacy key (deployment.environment) — the
//     v0.8.379 ingest / v0.8.380 topoEnvChainSQL rule;
//   - res-array indexOf lookups (the logs table stores attributes as
//     parallel arrays, not Map columns);
//   - NO deploy_env column leg (logs has no typed env column) and NO
//     namespace fallback (an env-less log row stays honestly env-less).
func TestLogsEnvChainSQL(t *testing.T) {
	newIdx := strings.Index(logsEnvChainSQL, "'deployment.environment.name'")
	legacyIdx := strings.LastIndex(logsEnvChainSQL, "'deployment.environment'")
	if newIdx < 0 || legacyIdx < 0 {
		t.Fatalf("logsEnvChainSQL must read BOTH spellings (unit-mixing rule):\n%s", logsEnvChainSQL)
	}
	if newIdx > legacyIdx {
		t.Fatalf("new semconv key must coalesce BEFORE the legacy key:\n%s", logsEnvChainSQL)
	}
	for _, want := range []string{
		"res_values[indexOf(res_keys, 'deployment.environment.name')]",
		"res_values[indexOf(res_keys, 'deployment.environment')]",
		"coalesce(",
	} {
		if !strings.Contains(logsEnvChainSQL, want) {
			t.Errorf("logsEnvChainSQL missing %q:\n%s", want, logsEnvChainSQL)
		}
	}
	for _, banned := range []string{"deploy_env", "service.namespace", "k8s.namespace.name"} {
		if strings.Contains(logsEnvChainSQL, banned) {
			t.Errorf("logsEnvChainSQL must not contain %q (logs-local: no typed column, no namespace approximation):\n%s",
				banned, logsEnvChainSQL)
		}
	}
}
