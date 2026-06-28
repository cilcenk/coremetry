package chstore

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/config"
)

// TestShardSkipSetting — v0.8.209 regression guard for the prod
// "cluster sometimes returns no results" incident.
//
// Symptom: against an EXTERNAL Distributed `spans` table with
// clickhouse.cluster_name UNSET, ~14 hot service-scoped read paths
// shipped a hardcoded `optimize_skip_unused_shards = 1`. ClickHouse
// then pruned the fan-out to the single shard that the operator's
// shard expression resolved `service_name=?` to — which is NOT
// Coremetry's assumed `cityHash64(service_name)` ownership — so each
// query returned only one shard's slice and the operator saw
// inconsistent / empty results.
//
// Root cause: skip-unused-shards is only provably correct when
// Coremetry OWNS the Distributed wrapper + its shard key, i.e. when
// clusterMode() is true (cluster_name set → adaptDDL created the
// wrapper with cityHash64(service_name)). With cluster_name unset
// Coremetry doesn't own the shard key, so the prune may target the
// wrong shard. shardSkipSetting() gates on clusterMode(): "= 1" only
// when we own the key, "= 0" (full fan-out, complete) otherwise —
// a harmless no-op single-node. Generalizes the rescue at repo.go:905.
//
// clusterMode() reads ONLY s.cfg.ClusterName (TrimSpace != ""), so a
// minimal &Store literal exercises the pure decision.
func TestShardSkipSetting(t *testing.T) {
	cases := []struct {
		name        string
		clusterName string
		want        string
	}{
		{
			name:        "cluster_name set (we own the wrapper+shard key)",
			clusterName: "coremetry",
			want:        "optimize_skip_unused_shards = 1",
		},
		{
			name:        "cluster_name unset (external Distributed — don't prune)",
			clusterName: "",
			want:        "optimize_skip_unused_shards = 0",
		},
		{
			name:        "cluster_name whitespace-only (clusterMode trims → unset)",
			clusterName: "  ",
			want:        "optimize_skip_unused_shards = 0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Store{cfg: config.CHConfig{ClusterName: tc.clusterName}}
			if got := s.shardSkipSetting(); got != tc.want {
				t.Fatalf("shardSkipSetting() with ClusterName=%q = %q; want %q",
					tc.clusterName, got, tc.want)
			}
			// Exported form must match the unexported one exactly —
			// the api package consumes ShardSkipSetting().
			if got := s.ShardSkipSetting(); got != tc.want {
				t.Fatalf("ShardSkipSetting() with ClusterName=%q = %q; want %q",
					tc.clusterName, got, tc.want)
			}
		})
	}
}
