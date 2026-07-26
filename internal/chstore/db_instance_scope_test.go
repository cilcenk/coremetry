package chstore

import (
	"os"
	"strings"
	"testing"
)

// v0.9.279 — the modal and the panel must resolve an instance identically.
// If they drift, the drill chart silently describes a different population than
// the panel it was opened from, and nothing errors.
//
// Pinned against the panel helpers' own source so a receiver-vocabulary change
// on either side fails here.
func TestDBInstanceScopeMatchesPanels(t *testing.T) {
	panels := map[string]string{
		"oracle":     "oracle.go",
		"postgresql": "postgres.go",
		"mysql":      "mysql.go",
		"redis":      "redis.go",
	}
	for engine, file := range panels {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		k, ok := dbInstanceScopeKeys[engine]
		if !ok {
			t.Errorf("%s has a panel but no scope entry — the drill modal cannot narrow "+
				"and will blend every instance of this engine", engine)
			continue
		}
		for _, key := range []string{k.attrKey, k.altKey} {
			if !strings.Contains(string(src), "'"+key+"'") {
				t.Errorf("%s: panel does not use %q, but dbInstanceScopeKeys does — the modal\n"+
					"would scope by a key the panel never matches, drawing an EMPTY chart", file, key)
			}
		}
	}
}

func TestDBInstanceScopeClause(t *testing.T) {
	// Two binds, always: the value goes into both branches of the OR.
	if sql, n := dbInstanceScopeClause("oracle", "corebank-dg.prod"); n != 2 ||
		!strings.Contains(sql, "attr_keys, 'instance'") ||
		!strings.Contains(sql, "res_keys, 'service.name'") {
		t.Errorf("oracle clause wrong: %q (%d args)", sql, n)
	}
	// Oracle is the only engine whose fallback lives in res_keys.
	for _, e := range []string{"postgresql", "mysql", "redis"} {
		sql, _ := dbInstanceScopeClause(e, "host-1")
		if strings.Contains(sql, "res_keys") {
			t.Errorf("%s must not read res_keys — its panel uses attr_keys for both branches", e)
		}
	}
	// No narrowing rather than empty results: an untagged deployment keeps the
	// blended reading it already had instead of losing the chart entirely.
	for _, c := range []struct{ engine, instance string }{
		{"oracle", ""},
		{"oracle", "unknown"},
		{"cassandra", "node-1"}, // engine with no receiver panel
	} {
		if sql, n := dbInstanceScopeClause(c.engine, c.instance); sql != "" || n != 0 {
			t.Errorf("dbInstanceScopeClause(%q,%q) must be empty, got %q", c.engine, c.instance, sql)
		}
	}
}
