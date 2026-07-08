package chstore

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/config"
)

// v0.8.388 — D1-audit finding: the TDigest boot probe appended _local
// to EVERY MV in cluster mode, but the unpromoted MVs (spanmetrics_*,
// operation_group_summary_5m — outside highVolumeTables) exist under
// their bare names; probing <mv>_local found no column → a false
// "upgrading …reservoir→TDigest" log + no-op drop/recreate every boot.
func TestMVStorageName(t *testing.T) {
	clustered := &Store{cfg: config.CHConfig{ClusterName: "coremetry"}}
	single := &Store{}
	cases := []struct {
		mv        string
		clustered string
	}{
		{"service_summary_5m", "service_summary_5m_local"},   // promoted
		{"db_summary_5m", "db_summary_5m_local"},             // promoted
		{"db_statement_summary_5m", "db_statement_summary_5m_local"}, // promoted day-one (v0.8.375)
		{"spanmetrics_1m", "spanmetrics_1m"},                 // unpromoted
		{"spanmetrics_10s", "spanmetrics_10s"},               // unpromoted
		{"spanmetrics_1s", "spanmetrics_1s"},                 // unpromoted
		{"operation_group_summary_5m", "operation_group_summary_5m"}, // unpromoted
	}
	for _, c := range cases {
		if got := clustered.mvStorageName(c.mv); got != c.clustered {
			t.Errorf("clustered %s → %q, want %q", c.mv, got, c.clustered)
		}
		if got := single.mvStorageName(c.mv); got != c.mv {
			t.Errorf("single-node %s → %q, want bare name", c.mv, got)
		}
	}
}
