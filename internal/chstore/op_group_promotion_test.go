package chstore

import (
	"strings"
	"testing"
)

// op_group_promotion_test.go — v0.9.350.
//
// operation_group_summary_5m was NOT in highVolumeTables while its sibling
// operation_summary_5m has been since v0.5.435. In cluster mode that leaves it
// a bare per-shard table — and queryOperationsFromMV swaps between the two on
// a single `normalized` boolean, so the SAME function fans out across shards
// in one branch and reads one shard's slice in the other. The v0.8.356/358
// one-shard-undercount class.
//
// It was dormant, not harmless: cluster_name is typically unset on the live
// install, so op_group can't be added to spans_local and the MV is dropped at
// boot. It arms the moment cluster_name is set CORRECTLY — which is the
// documented fix path. Fixing one thing was going to silently arm another.
func TestOpGroupMVIsPromoted(t *testing.T) {
	if !highVolumeTables["operation_group_summary_5m"] {
		t.Error("operation_group_summary_5m must be in highVolumeTables — without it, cluster mode reads one shard's slice")
	}
	// The pair must agree. If they ever diverge again, the read path's
	// `normalized` toggle silently changes the SCOPE of the answer.
	if highVolumeTables["operation_summary_5m"] != highVolumeTables["operation_group_summary_5m"] {
		t.Error("the two operation rollups must be registered identically — the read path swaps between them on one boolean")
	}
}

func TestOpGroupShardKeyMatchesSibling(t *testing.T) {
	got := defaultShardPolicy["operation_group_summary_5m"]
	want := defaultShardPolicy["operation_summary_5m"]
	if got == "" {
		t.Fatal("operation_group_summary_5m has no shard key")
	}
	if got != want {
		t.Errorf("shard key %q != sibling's %q — a service's rows should land the same way in both rollups", got, want)
	}
}

// Registering the table changes its STORAGE NAME in cluster mode (bare →
// _local). An install that already created it bare-name would break on the
// next boot unless the promotion RENAME runs, so the two changes are one
// atomic decision — registration without promotion is worse than neither.
func TestOpGroupIsInThePromotionList(t *testing.T) {
	src := mustReadSource(t, "store.go")
	i := strings.Index(src, "promoteCombinedMVs(ctx, []string{")
	if i < 0 {
		t.Fatal("promoteCombinedMVs call not found")
	}
	end := strings.Index(src[i:], "})")
	if end < 0 {
		t.Fatal("promotion list not terminated")
	}
	if !strings.Contains(src[i:i+end], `"operation_group_summary_5m"`) {
		t.Error("operation_group_summary_5m is registered in highVolumeTables but missing from the promotion list — an existing bare-name install would look for a _local table that was never renamed")
	}
}
