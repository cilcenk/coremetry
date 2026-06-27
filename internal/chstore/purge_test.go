package chstore

import "testing"

// The purge "factory reset" is destructive, so its safety contract is that the
// allowlist NEVER contains a config / operator-owned table. This test fails the
// build if a config table ever leaks into telemetryPurgeTables — the one thing
// that must never regress.
func TestPurgeAllowlistExcludesConfig(t *testing.T) {
	purge := map[string]bool{}
	for _, name := range telemetryPurgeTables {
		if purge[name] {
			t.Errorf("duplicate %q in telemetryPurgeTables", name)
		}
		purge[name] = true
	}
	for _, c := range configPreserveTables {
		if purge[c] {
			t.Errorf("config/operator table %q is in the purge allowlist — factory reset would wipe it", c)
		}
	}
	// audit_log records the purge itself — it must survive.
	if purge["audit_log"] {
		t.Error("audit_log must NOT be in the purge allowlist (it's the audit trail)")
	}
	// system_settings holds LDAP / Copilot / branding / sampling — wiping it
	// would log the operator out of their own config. Must be preserved.
	if purge["system_settings"] {
		t.Error("system_settings must NOT be purged (LDAP + all config live here)")
	}
	// Operator-AUTHORED tables (manual incidents, post-mortem notes, acks,
	// triage, deploy annotations, remediation history) must be in the preserve
	// set — an adversarial review caught these in the purge list. Pinning them
	// here makes the intersection check above actually catch a re-add.
	preserve := map[string]bool{}
	for _, name := range configPreserveTables {
		preserve[name] = true
	}
	for _, mustKeep := range []string{
		"problems", "incidents", "incident_events", "incident_problems",
		"exception_groups", "events", "runbook_executions",
	} {
		if !preserve[mustKeep] {
			t.Errorf("operator-authored table %q must be in configPreserveTables (not purged)", mustKeep)
		}
		if purge[mustKeep] {
			t.Errorf("operator-authored table %q is in the purge allowlist — would erase operator work", mustKeep)
		}
	}
}
