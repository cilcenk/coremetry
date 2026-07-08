package chstore

import (
	"strings"
	"testing"
)

// v0.8.380 — audit-found: all four topology passes derived env from
// the LEGACY deployment.environment attr only, ignoring the typed
// deploy_env column (populated for both semconv spellings since
// v0.8.379) — deployment.environment.name emitters (the operator's
// int/uat/prep test envs) got no env chip on the service map. One
// shared chain now feeds every pass.
func TestTopoEnvChainSQL(t *testing.T) {
	for _, prefix := range []string{"", "c."} {
		sql := topoEnvChainSQL(prefix)
		// deploy_env must LEAD the chain.
		first := strings.Index(sql, prefix+"deploy_env")
		if first < 0 {
			t.Fatalf("prefix %q: typed column missing\n%s", prefix, sql)
		}
		for _, key := range []string{
			"deployment.environment.name",
			"deployment.environment",
			"service.namespace",
			"k8s.namespace.name",
		} {
			pos := strings.Index(sql, "'"+key+"'")
			if pos < 0 {
				t.Errorf("prefix %q: fallback %q missing", prefix, key)
			} else if pos < first {
				t.Errorf("prefix %q: %q resolves before the typed column", prefix, key)
			}
		}
		// Every column reference carries the scope prefix.
		if prefix != "" && strings.Contains(sql, "indexOf(res_keys") {
			t.Errorf("prefix %q: unqualified res_keys leaked\n%s", prefix, sql)
		}
	}
}
