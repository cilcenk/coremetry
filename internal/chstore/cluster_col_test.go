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

// v0.8.162 — operator-reported (external Distributed cluster, cluster_name
// unset): the cluster warm query spammed code 47 ("Identifier
// '__table1.cluster' cannot be resolved") because clusterColExpr references
// the materialized `cluster` column unconditionally, but on an external
// Distributed `spans` the column never reaches spans_local. clusterExpr()
// now gates the column reference on s.hasClusterCol (probed at boot): when
// the column is absent every cluster query must use the pure derive — which
// references ONLY res_values/attr_values, never the `cluster` column — so it
// resolves against spans_local everywhere.
func TestClusterExpr_DropsColumnRefWhenAbsent(t *testing.T) {
	withCol := (&Store{hasClusterCol: true}).clusterExpr()
	if withCol != clusterColExpr {
		t.Fatalf("hasClusterCol=true must use the column-aware clusterColExpr, got %q", withCol)
	}

	noCol := (&Store{hasClusterCol: false}).clusterExpr()
	if noCol != clusterDeriveExpr {
		t.Fatalf("hasClusterCol=false must use the pure derive, got %q", noCol)
	}
	// The whole point: with the column absent the expression must NOT
	// reference `cluster` at all (that's the code-47 trigger on spans_local).
	if contains(noCol, "nullIf(cluster, '')") || contains(noCol, "cluster, ''") {
		t.Fatal("the no-column expression must not reference the `cluster` " +
			"column — it fails with code 47 on an external Distributed spans_local")
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
