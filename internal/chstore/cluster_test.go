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
	// v0.5.419 — spans default shard key is cityHash64(service_name)
	// per defaultShardPolicy (was rand() pre-v0.5.419).
	if !strings.Contains(got[1], "ENGINE = Distributed(`ch_cluster`, currentDatabase(), spans_local, cityHash64(service_name))") {
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

// TestAdaptDDL_TOFormMV — v0.8.329: span_links_reverse_mv is the codebase's
// first TO-form MV (explicit target table, no ENGINE of its own). Pins the
// cluster-mode contract its DDL comment promises:
//   - the MV name is NOT in highVolumeTables → no _local rename, no
//     Distributed wrapper for the trigger itself (1 statement out);
//   - FROM span_links rewrites to span_links_local (each shard triggers on
//     its own slice) WITHOUT bleeding into the TO clause or the _local name;
//   - TO span_links_reverse stays the BARE name — that's the Distributed
//     wrapper, whose cityHash64(linked_trace_id) policy re-shards reverse
//     rows so LinksToTrace is a single-shard PK scan;
//   - no ENGINE clause → the Replicated engine swap must not touch it.
func TestAdaptDDL_TOFormMV(t *testing.T) {
	s := &Store{cfg: config.CHConfig{ClusterName: "c", ReplicaPath: "/p"}}
	src := `CREATE MATERIALIZED VIEW IF NOT EXISTS span_links_reverse_mv
		TO span_links_reverse
		AS SELECT trace_id, span_id, linked_trace_id, linked_span_id,
		time, service_name, attr_keys, attr_values
		FROM span_links`
	got := s.adaptDDL(src)
	if len(got) != 1 {
		t.Fatalf("TO-form MV must be 1 stmt (no wrapper — it has no storage), got %d: %#v", len(got), got)
	}
	if !strings.Contains(got[0], "span_links_reverse_mv ON CLUSTER `c`") {
		t.Errorf("ON CLUSTER missing / MV wrongly renamed: %s", got[0])
	}
	if !strings.Contains(got[0], "FROM span_links_local") {
		t.Errorf("MV FROM not rewritten to the shard-local source: %s", got[0])
	}
	if !strings.Contains(got[0], "TO span_links_reverse\n") && !strings.Contains(got[0], "TO span_links_reverse ") {
		t.Errorf("TO target must stay the bare Distributed name: %s", got[0])
	}
	if strings.Contains(got[0], "TO span_links_reverse_local") {
		t.Errorf("TO target must NOT be rewritten to _local (re-sharding by linked_trace_id happens via the wrapper): %s", got[0])
	}
	if strings.Contains(got[0], "Replicated") {
		t.Errorf("engine swap must not touch an ENGINE-less TO-form MV: %s", got[0])
	}
}

// TestIsClusterUnsupportedAlter — locks the exact substring we
// recognise so a future code-gen change to the CH client error
// format can't silently make the migration fatal again.
//
// Background: a multi-host CH deployment with externally-managed
// Distributed schema (operator didn't set cluster_name in our
// config) raises CH code 48 when our migration tries to add a
// skip index to the Distributed engine. We soften that into a
// warning — but only for that exact error.
func TestIsClusterUnsupportedAlter(t *testing.T) {
	cases := []struct {
		err  string
		want bool
	}{
		{"code: 48, message: Alter of type 'ADD_INDEX' is not supported by storage Distributed", true},
		{"is not supported by storage Distributed", true},
		// v0.8.162 — MODIFY TTL on a Distributed wrapper raises CH code
		// 36; retention.go now skips it the same way as the index ALTERs.
		{"code: 36, message: Engine Distributed doesn't support TTL clause", true},
		// Unrelated DDL failures must still bubble up — we don't
		// want to swallow real schema bugs.
		{"code: 60, message: Table doesn't exist", false},
		{"permission denied", false},
		{"", false},
	}
	for _, c := range cases {
		var e error
		if c.err != "" {
			e = errString(c.err)
		}
		if got := isClusterUnsupportedAlter(e); got != c.want {
			t.Errorf("isClusterUnsupportedAlter(%q) = %v, want %v", c.err, got, c.want)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

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

// TestAdaptDDL_AlterAddColumn — v0.5.362. Column-list ALTERs must
// hit BOTH the _local ReplicatedMergeTree (so each shard accepts
// the new column) and the Distributed wrapper (so INSERT/SELECT
// against the bare name see the new schema). Pre-fix the wrapper
// stayed behind and runtime INSERT errored "no such column …".
func TestAdaptDDL_AlterAddColumn(t *testing.T) {
	s := &Store{cfg: config.CHConfig{ClusterName: "c"}}
	got := s.adaptDDL(`ALTER TABLE metric_points ADD COLUMN IF NOT EXISTS bucket_bounds Array(Float64) DEFAULT []`)
	if len(got) != 2 {
		t.Fatalf("ADD COLUMN should be 2 stmts (_local + wrapper), got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "ALTER TABLE metric_points_local ON CLUSTER `c`") {
		t.Errorf("first stmt should target _local, got: %s", got[0])
	}
	if !strings.Contains(got[1], "ALTER TABLE metric_points ON CLUSTER `c`") ||
		strings.Contains(got[1], "metric_points_local") {
		t.Errorf("second stmt should target bare Distributed name, got: %s", got[1])
	}
}
