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

// TestTopoEnvChainRelativeOrder — v0.9.1325, closing the gap v0.9.1318's
// commit body recorded: the test above only proved each fallback comes
// AFTER the typed column, so the four fallbacks could be shuffled among
// themselves and nothing failed.
//
// The order is the whole meaning of a coalesce chain. Two of the rungs
// carry decisions this repo has already paid for:
//
//	deployment.environment.name BEFORE deployment.environment — the newer
//	  semconv spelling wins; the reversed order pins every dual-emitting
//	  service to the legacy value (the v0.8.380 bug class).
//	service.namespace BEFORE k8s.namespace.name — the v0.9.1318 canonical
//	  ruling: semconv's declared namespace outranks the ephemeral k8s
//	  placement, and identity.go's namespace chain is pinned the same way.
//	  Two chains disagreeing means one surface shows a box and the other's
//	  link into it matches nothing.
//
// An env fallback must also never outrank a real env attribute, so the
// namespace pair stays last.
func TestTopoEnvChainRelativeOrder(t *testing.T) {
	want := []string{
		"deployment.environment.name",
		"deployment.environment",
		"service.namespace",
		"k8s.namespace.name",
	}
	for _, prefix := range []string{"", "c."} {
		sql := topoEnvChainSQL(prefix)
		prev := strings.Index(sql, prefix+"deploy_env")
		if prev < 0 {
			t.Fatalf("prefix %q: typed column missing", prefix)
		}
		prevName := "deploy_env"
		for _, key := range want {
			// Quoted so 'deployment.environment' cannot match inside
			// 'deployment.environment.name'.
			pos := strings.Index(sql, "'"+key+"'")
			if pos < 0 {
				t.Fatalf("prefix %q: rung %q missing", prefix, key)
			}
			if pos <= prev {
				t.Errorf("prefix %q: %q resolves BEFORE %q — coalesce sırası bozuldu\n%s",
					prefix, key, prevName, sql)
			}
			prev, prevName = pos, key
		}
	}
}
