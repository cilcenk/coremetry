package chstore

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/config"
)

// TestAdaptDDL_SingleNode locks in the invariant that single-node
// mode is a no-op transformer. Any divergence here would silently
// break every existing deployment.
func TestAdaptDDL_SingleNode(t *testing.T) {
	s := &Store{cfg: config.CHConfig{}} // ClusterName empty
	got := s.adaptDDL("CREATE TABLE IF NOT EXISTS spans (id String) ENGINE = MergeTree() ORDER BY id")
	if len(got) != 1 || !strings.Contains(got[0], "MergeTree()") {
		t.Fatalf("single-node should pass through unchanged, got %#v", got)
	}
}

// TestAdaptDDL_HighVolumeTable verifies the spans CREATE picks up
// `_local`, ON CLUSTER, ReplicatedMergeTree, and a Distributed
// wrapper.
func TestAdaptDDL_HighVolumeTable(t *testing.T) {
	s := &Store{cfg: config.CHConfig{
		ClusterName: "ch_cluster",
		ReplicaPath: "/ch/tbl",
	}}
	got := s.adaptDDL("CREATE TABLE IF NOT EXISTS spans (id String) ENGINE = MergeTree() ORDER BY id")
	if len(got) != 2 {
		t.Fatalf("expected 2 stmts (local + distributed), got %d: %#v", len(got), got)
	}
	if !strings.Contains(got[0], "spans_local") {
		t.Errorf("local table not renamed: %s", got[0])
	}
	if !strings.Contains(got[0], "ON CLUSTER `ch_cluster`") {
		t.Errorf("ON CLUSTER missing: %s", got[0])
	}
	if !strings.Contains(got[0], "ReplicatedMergeTree('/ch/tbl/{shard}/spans', '{replica}')") {
		t.Errorf("Replicated engine wrong: %s", got[0])
	}
	if !strings.Contains(got[1], "ENGINE = Distributed(`ch_cluster`, currentDatabase(), spans_local, rand())") {
		t.Errorf("Distributed wrapper wrong: %s", got[1])
	}
}

// TestAdaptDDL_AdminTable: ReplacingMergeTree(version) preserves
// its existing args while gaining the Replicated ZK path. No
// Distributed wrapper because admin tables aren't sharded.
func TestAdaptDDL_AdminTable(t *testing.T) {
	s := &Store{cfg: config.CHConfig{ClusterName: "c", ReplicaPath: "/p"}}
	got := s.adaptDDL("CREATE TABLE IF NOT EXISTS users (id String, version UInt64) ENGINE = ReplacingMergeTree(version) ORDER BY id")
	if len(got) != 1 {
		t.Fatalf("admin table should be 1 stmt, got %d", len(got))
	}
	if !strings.Contains(got[0], "ENGINE = ReplicatedReplacingMergeTree('/p/{shard}/users', '{replica}', version)") {
		t.Errorf("merged args wrong: %s", got[0])
	}
	if strings.Contains(got[0], "users_local") {
		t.Error("admin table should NOT get _local suffix")
	}
}

// TestAdaptDDL_MV checks the MV path: own _local + engine swap +
// FROM rewrite to read the sharded source's local flavour +
// Distributed wrapper.
func TestAdaptDDL_MV(t *testing.T) {
	s := &Store{cfg: config.CHConfig{ClusterName: "c", ReplicaPath: "/p"}}
	src := `CREATE MATERIALIZED VIEW IF NOT EXISTS service_summary_5m
		ENGINE = AggregatingMergeTree
		ORDER BY (service_name, time_bucket)
		AS SELECT service_name, countState() AS c FROM spans GROUP BY service_name, time_bucket`
	got := s.adaptDDL(src)
	if len(got) != 2 {
		t.Fatalf("MV should produce local + distributed, got %d: %#v", len(got), got)
	}
	if !strings.Contains(got[0], "service_summary_5m_local") {
		t.Errorf("MV not renamed: %s", got[0])
	}
	if !strings.Contains(got[0], "ReplicatedAggregatingMergeTree") {
		t.Errorf("MV engine not swapped: %s", got[0])
	}
	if !strings.Contains(got[0], "FROM spans_local") {
		t.Errorf("MV FROM not rewritten: %s", got[0])
	}
}

// TestAdaptDDL_AlterTable: an ALTER hits the local table, no
// Distributed wrapper needed.
func TestAdaptDDL_AlterTable(t *testing.T) {
	s := &Store{cfg: config.CHConfig{ClusterName: "c"}}
	got := s.adaptDDL("ALTER TABLE spans ADD INDEX IF NOT EXISTS idx_kind kind TYPE set(0) GRANULARITY 4")
	if len(got) != 1 {
		t.Fatalf("ALTER should be 1 stmt, got %d", len(got))
	}
	if !strings.Contains(got[0], "ALTER TABLE spans_local ON CLUSTER `c`") {
		t.Errorf("ALTER target wrong: %s", got[0])
	}
}
