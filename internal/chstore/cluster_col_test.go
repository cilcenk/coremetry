package chstore

import "testing"

// v0.8.x — cluster promoted from a read-time res_values/attr_values
// indexOf() scan (clusterDeriveExpr) to a MATERIALIZED column on
// spans. The read path uses clusterColExpr, which reads the column
// for new parts and falls back to clusterDeriveExpr for pre-column
// parts. The subtle correctness invariant: the column's MATERIALIZED
// expression (store.go DDL + migration) and the read-time fallback
// MUST embed the SAME clusterDeriveExpr, or new and old parts would
// derive different cluster names for the same span. These asserts
// fail loudly if a future edit decouples them.
func TestClusterColExpr_FallsBackToDeriveNoDrift(t *testing.T) {
	if !contains(clusterColExpr, clusterDeriveExpr) {
		t.Fatal("clusterColExpr must embed clusterDeriveExpr verbatim as the " +
			"old-part fallback — otherwise new/old parts drift")
	}
	if !contains(clusterColExpr, "nullIf(cluster, '')") {
		t.Fatal("clusterColExpr must read the promoted `cluster` column via " +
			"nullIf(cluster, '') before falling back to the derive scan")
	}
	// The column read must come FIRST so CH short-circuits the
	// expensive indexOf() scan on new parts.
	if indexOf(clusterColExpr, "cluster, ''") > indexOf(clusterColExpr, "res_values[indexOf") {
		t.Fatal("the cheap column read must precede the derive scan so coalesce " +
			"short-circuits on new parts")
	}
}

func contains(haystack, needle string) bool { return indexOf(haystack, needle) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
