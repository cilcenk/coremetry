package logstore

import (
	"strings"
	"testing"
)

// v0.8.400 — env-separation Phase 4, CH-side shape pins.
//
// The logs table stores attributes as PARALLEL ARRAYS (attr_keys/
// attr_values + res_keys/res_values) — NOT Map columns. These tests
// pin the three lookup expressions to the res-array indexOf shape:
//
//   - chLogsEnvExpr — BOTH semconv spellings (the topoEnvChainSQL
//     two-spelling rule v0.8.380, logs-local: no deploy_env leg, no
//     namespace fallback), new key FIRST;
//   - chLogsClusterExpr / chLogsAttrLookupExpr — the map-access fix:
//     pre-v0.8.400 these used resource_attributes[...] / attributes[...]
//     map syntax against columns the logs table doesn't have, so any
//     CH-backend query reaching them failed UNKNOWN_IDENTIFIER
//     (verified live during the Phase 4 audit).

func TestChLogsEnvExpr_BothSpellings(t *testing.T) {
	// Order matters: the NEW semconv key must coalesce first.
	newIdx := strings.Index(chLogsEnvExpr, "'deployment.environment.name'")
	legacyIdx := strings.LastIndex(chLogsEnvExpr, "'deployment.environment'")
	if newIdx < 0 {
		t.Fatal("chLogsEnvExpr must read the new semconv key deployment.environment.name")
	}
	if legacyIdx < 0 {
		t.Fatal("chLogsEnvExpr must keep the legacy deployment.environment fallback")
	}
	if newIdx > legacyIdx {
		t.Fatal("new semconv key must coalesce BEFORE the legacy key (v0.8.379/380 rule)")
	}
	for _, want := range []string{
		"res_values[indexOf(res_keys, 'deployment.environment.name')]",
		"res_values[indexOf(res_keys, 'deployment.environment')]",
	} {
		if !strings.Contains(chLogsEnvExpr, want) {
			t.Errorf("chLogsEnvExpr missing res-array lookup %q:\n%s", want, chLogsEnvExpr)
		}
	}
	for _, banned := range []string{"resource_attributes[", "attributes[", "deploy_env", "namespace"} {
		if strings.Contains(chLogsEnvExpr, banned) {
			t.Errorf("chLogsEnvExpr must not contain %q (logs-local rule):\n%s", banned, chLogsEnvExpr)
		}
	}
}

func TestChLogsClusterExpr_ResArrayLookup(t *testing.T) {
	for _, want := range []string{
		"res_values[indexOf(res_keys, 'k8s.cluster.name')]",
		"res_values[indexOf(res_keys, 'openshift.cluster.name')]",
		"res_values[indexOf(res_keys, 'cluster')]",
	} {
		if !strings.Contains(chLogsClusterExpr, want) {
			t.Errorf("chLogsClusterExpr missing %q:\n%s", want, chLogsClusterExpr)
		}
	}
	if strings.Contains(chLogsClusterExpr, "resource_attributes[") {
		t.Error("chLogsClusterExpr must not use Map access — the logs table has no resource_attributes column")
	}
}

func TestChLogsAttrLookupExpr_ResArrayLookup(t *testing.T) {
	for _, want := range []string{
		"attr_values[indexOf(attr_keys, ?)]",
		"res_values[indexOf(res_keys, ?)]",
	} {
		if !strings.Contains(chLogsAttrLookupExpr, want) {
			t.Errorf("chLogsAttrLookupExpr missing %q:\n%s", want, chLogsAttrLookupExpr)
		}
	}
	// FieldStats binds (field, field) — exactly two placeholders.
	if got := strings.Count(chLogsAttrLookupExpr, "?"); got != 2 {
		t.Errorf("chLogsAttrLookupExpr must carry exactly 2 placeholders, got %d", got)
	}
	for _, banned := range []string{"attributes['", "resource_attributes["} {
		if strings.Contains(chLogsAttrLookupExpr, banned) {
			t.Errorf("chLogsAttrLookupExpr must not use Map access (%q)", banned)
		}
	}
}
