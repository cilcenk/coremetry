package chstore

import "testing"

// v0.8.208 — operator-reported: on an external 4-node Distributed cluster a
// fresh schema create crashed with METADATA_MISMATCH (code 342) — the stored
// skip-index set of coremetry.spans_local in ZooKeeper differed from the new
// CREATE. Root cause: an earlier partial reset left an ORPHAN znode that
// DROP DATABASE couldn't clean (the local replica was already gone, so the drop
// skipped it). ResetSchema now sweeps those orphans via SYSTEM DROP REPLICA …
// FROM ZKPATH. These pin the two pure pieces of that sweep: the statement shape
// (incl. quote-escaping, so a macro value can't break the literal) and the
// scope (only sharded Replicated tables — never an admin/config table that
// shares the replica_path prefix).

func TestDropReplicaZkStmt(t *testing.T) {
	cases := []struct {
		name      string
		replica   string
		tablePath string
		want      string
	}{
		{
			name:      "plain",
			replica:   "chc-0",
			tablePath: "/clickhouse/tables/01/spans",
			want:      "SYSTEM DROP REPLICA 'chc-0' FROM ZKPATH '/clickhouse/tables/01/spans'",
		},
		{
			name:      "single quote in replica is escaped",
			replica:   "rep'1",
			tablePath: "/clickhouse/tables/02/logs",
			want:      "SYSTEM DROP REPLICA 'rep''1' FROM ZKPATH '/clickhouse/tables/02/logs'",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dropReplicaZkStmt(c.replica, c.tablePath); got != c.want {
				t.Fatalf("dropReplicaZkStmt(%q, %q)\n got: %s\nwant: %s", c.replica, c.tablePath, got, c.want)
			}
		})
	}
}

// The sweep must clean the sharded Replicated tables (where index-set evolution
// causes the mismatch) and must NOT touch admin/config tables — those share the
// replica_path prefix but a stray DROP REPLICA on them would corrupt operator
// state (alert_rules, users, …). Pin the membership the sweep filters on.
func TestZnodeSweepScope(t *testing.T) {
	mustSweep := []string{
		"spans", "logs", "metric_points", "profiles",
		"service_summary_5m", "operation_summary_5m", "trace_summary_5m",
		"trace_service_index_5m", "topology_edges_5m",
	}
	for _, tbl := range mustSweep {
		if !highVolumeTables[tbl] {
			t.Errorf("sharded Replicated table %q is not in the sweep set — orphan znodes for it would survive a reset", tbl)
		}
	}
	mustSkip := []string{"alert_rules", "problems", "users", "saved_views", "system_settings", "dashboards"}
	for _, tbl := range mustSkip {
		if highVolumeTables[tbl] {
			t.Errorf("admin/config table %q is in the sweep set — a reset could DROP REPLICA its znode and corrupt operator state", tbl)
		}
	}
}

// v0.8.222 — scale-audit risk: the orphan-znode sweep matched purely by table
// NAME under <replica_path>/<shard>/<table> (no DB/uuid component), so on the
// SHARED default "/clickhouse/tables" a co-tenant's same-named Replicated table
// could have its live replica DROP REPLICA'd. zkSweepEnabled is the gate: only
// a DEDICATED, operator-set replica_path is swept. Pin it so the safety can't
// silently regress.
func TestZkSweepEnabled(t *testing.T) {
	cases := map[string]bool{
		"":                            false, // unset → defaults to the shared prefix
		"/clickhouse/tables":          false, // the shared default
		"/clickhouse/tables/":         false, // trailing slash normalised
		"/clickhouse/tables/coremetry": true, // dedicated → safe
		"/ch/coremetry":               true,
	}
	for path, want := range cases {
		if got := zkSweepEnabled(path); got != want {
			t.Errorf("zkSweepEnabled(%q) = %v, want %v", path, got, want)
		}
	}
}
