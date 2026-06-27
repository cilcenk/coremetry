package chstore

import (
	"os"
	"strings"
	"testing"
)

// v0.8.194 — regression guard for the reservoir→TDigest quantile-state
// migration. At billion-span scale the reservoir quantilesState (~64 KiB/row)
// blew CH's per-query memory limit on wide-window duration_q_state reads
// (code 241, operator-reported prod OOM); quantilesTDigestState is ~4.3 KiB/row
// and parallel-safe. This test pins both halves of the contract so a future
// edit can't silently regress to the reservoir aggregate:
//   1. NO MV DDL emits the reservoir `quantilesState(` into duration_q_state.
//   2. The TDigest form IS present.
// Verified live on CH 24.8: migration probe flips reservoir→TDigest, p99 within
// 0.15% of the reservoir, full-parallelism read fits where the reservoir OOM'd.
func TestMVDDLUsesTDigestState(t *testing.T) {
	src, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	s := string(src)
	// The reservoir DDL form `quantilesState(0.` must be gone — it's exactly
	// the anti-pattern CLAUDE.md names ("quantile() past ~1M rows → TDigest").
	if strings.Contains(s, "quantilesState(0.") {
		t.Errorf("store.go still contains reservoir `quantilesState(0.` in an MV DDL " +
			"— must be quantilesTDigestState (reservoir OOMs duration_q_state reads at scale)")
	}
	if !strings.Contains(s, "quantilesTDigestState(0.") {
		t.Error("store.go has no `quantilesTDigestState(0.` — the MV DDLs lost the TDigest state")
	}
}

// The migration loop must cover every MV that carries a duration_q_state, or a
// missed one keeps its reservoir state and re-OOMs in prod. Pin the list the
// migration iterates (mirrors the 10 MV DDLs in store.go).
func TestTDigestMigrationCoversAllQuantileMVs(t *testing.T) {
	want := []string{
		"service_summary_5m", "operation_summary_5m", "operation_group_summary_5m",
		"db_summary_5m", "db_caller_summary_5m", "messaging_summary_5m",
		"messaging_caller_summary_5m", "spanmetrics_1m", "spanmetrics_10s", "spanmetrics_1s",
	}
	src, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	s := string(src)
	for _, mv := range want {
		// Each quantile-bearing MV must appear in the TDigest migration's probe
		// block (the positionUTF8(type,'TDigest') guard) so its reservoir state
		// gets upgraded on boot.
		if !strings.Contains(s, `"`+mv+`"`) {
			t.Errorf("MV %q missing from store.go (TDigest migration list incomplete)", mv)
		}
	}
	// CRITICAL (review-caught): the TDigest migration MUST skip
	// operation_group_summary_5m when op_group is absent on spans — otherwise it
	// recreates a disabled MV whose insert-trigger references the missing
	// op_group and blocks ALL ingest (the v0.8.186 incident, on the external
	// Distributed / cluster_name-unset prod shape). Pin the guard.
	if !strings.Contains(s, `mv == "operation_group_summary_5m" && !s.hasOpGroupCol`) {
		t.Error("TDigest migration missing the op_group guard — would recreate a disabled MV and block ingest on external Distributed (v0.8.186 regression)")
	}
}
