package chstore

import (
	"strings"
	"testing"
)

// v0.8.280 — cluster_name required+validated (the Uptrace "cluster can't be
// empty" discipline, adapted). When cluster_name IS set but doesn't exist in
// system.clusters (typo, wrong env's value pasted in), boot previously died
// later inside `CREATE DATABASE ... ON CLUSTER` with a raw CH code-170 error
// and no guidance. Contract of clusterNameError (pure — the boot probe feeds
// it the system.clusters list):
//   - exact match → nil (valid config, no noise);
//   - case-insensitive match → error naming the correctly-cased cluster;
//   - no match, clusters exist → error listing the available names;
//   - no match, server defines NO clusters → error saying exactly that;
//   - empty configured name → nil (not clusterMode; caller never gates on it).
func TestClusterNameError(t *testing.T) {
	avail := []string{"prod_eu", "prod_us", "staging"}

	t.Run("exact match is valid", func(t *testing.T) {
		if err := clusterNameError("prod_eu", avail); err != nil {
			t.Fatalf("exact match must pass, got: %v", err)
		}
	})

	t.Run("empty configured name is a no-op", func(t *testing.T) {
		if err := clusterNameError("", avail); err != nil {
			t.Fatalf("empty name must pass (single-node), got: %v", err)
		}
	})

	t.Run("case mismatch suggests the real name", func(t *testing.T) {
		err := clusterNameError("PROD_EU", avail)
		if err == nil {
			t.Fatal("case mismatch must be an error (CH cluster names are case-sensitive)")
		}
		if !strings.Contains(err.Error(), "prod_eu") {
			t.Fatalf("error must name the correctly-cased cluster; got: %v", err)
		}
	})

	t.Run("unknown name lists available clusters", func(t *testing.T) {
		err := clusterNameError("prod-eu", avail)
		if err == nil {
			t.Fatal("unknown cluster must be an error")
		}
		for _, want := range []string{"prod-eu", "prod_eu", "prod_us", "staging"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error must mention %q; got: %v", want, err)
			}
		}
	})

	t.Run("no clusters defined on the server", func(t *testing.T) {
		err := clusterNameError("prod_eu", nil)
		if err == nil {
			t.Fatal("configured cluster against a server with no clusters must be an error")
		}
		if !strings.Contains(err.Error(), "no clusters") {
			t.Fatalf("error must say the server defines no clusters; got: %v", err)
		}
	})

	t.Run("long cluster lists are capped, not dumped", func(t *testing.T) {
		many := make([]string, 30)
		for i := range many {
			many[i] = strings.Repeat("c", 3) + string(rune('a'+i))
		}
		err := clusterNameError("nope", many)
		if err == nil {
			t.Fatal("unknown cluster must be an error")
		}
		if n := strings.Count(err.Error(), "ccc"); n > 9 {
			t.Fatalf("available list must be capped (~8), error names %d clusters: %v", n, err)
		}
	})
}
