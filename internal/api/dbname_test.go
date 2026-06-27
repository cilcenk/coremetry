package api

import (
	"os"
	"strings"
	"testing"
)

// v0.8.195 — regression guard for the operator-reported PRODUCTION bug: several
// SQL builders in api.go hardcoded `FROM coremetry.spans`, so on an install
// whose ClickHouse database is not the literal "coremetry" (e.g. coremetry_prod)
// those queries failed with "Database coremetry does not exist" and the
// /explore attribute autocomplete + related reads returned errors.
//
// The chstore connection already defaults to the configured database
// (cfg.Database), so telemetry tables MUST be referenced UNQUALIFIED. This test
// fails the build if any `coremetry.<telemetry-table>` literal creeps back in.
func TestNoHardcodedDatabaseInSQL(t *testing.T) {
	src, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatalf("read api.go: %v", err)
	}
	s := string(src)
	for _, tbl := range []string{"spans", "logs", "metric_points", "profiles"} {
		if strings.Contains(s, "coremetry."+tbl) {
			t.Errorf("api.go hardcodes `coremetry.%s` — breaks on a non-default CH database "+
				"(e.g. coremetry_prod). Use the unqualified table name; the conn defaults to cfg.Database.", tbl)
		}
	}
}
