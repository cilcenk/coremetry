package chstore

import (
	"strings"
	"testing"
)

// v0.8.190 — regression guard for the PRODUCTION boot abort on the
// external Distributed cluster: the trace_summary_5m entry_route_state
// upgrade issued a bare `DROP TABLE <mv> ... SYNC`, which tripped CH's
// max_table_size_to_drop guard (default 50 GB) on a 65 GB inner table
// (`.inner_id.<uuid>`) → code 359 → migrate() error → crash loop.
//
// Verified against CH 24.8: a per-query SETTINGS override on
// `DROP TABLE <combined_mv>` does NOT reach the inner-table drop — the
// inner must be dropped DIRECTLY with the override. innerDropStmt is
// that statement; this test pins the two properties a future
// copy-paste could silently lose: (1) it targets the inner
// `.inner_id.<uuid>` storage, not the MV object; (2) it carries the
// volume-guard overrides. Either omission re-introduces the crash.
func TestInnerDropStmt(t *testing.T) {
	cases := []struct {
		name      string
		uuid      string
		onCluster string
	}{
		{"single-node", "ab5ccef7-e4a1-4730-9bc5-7d12baf b14ed", ""},
		{"cluster", "04ecee62-bdda-4667-81fc-66288286c0c6", " ON CLUSTER `prod`"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := innerDropStmt(tc.uuid, tc.onCluster)

			// Must target the hidden inner storage, not the MV object —
			// dropping the MV object respects only the server-default guard.
			if !strings.Contains(got, "`.inner_id."+tc.uuid+"`") {
				t.Errorf("must target inner `.inner_id.<uuid>`: %q", got)
			}
			// The volume-guard overrides are the whole point of the fix.
			if !strings.Contains(got, "max_table_size_to_drop = 0") {
				t.Errorf("missing max_table_size_to_drop override: %q", got)
			}
			if !strings.Contains(got, "max_partition_size_to_drop = 0") {
				t.Errorf("missing max_partition_size_to_drop override: %q", got)
			}
			// SYNC must precede SETTINGS — DROP grammar is
			// `DROP TABLE ... [ON CLUSTER ...] [SYNC] [SETTINGS ...]`.
			syncIdx := strings.Index(got, " SYNC")
			setIdx := strings.Index(got, "SETTINGS")
			if syncIdx < 0 || setIdx < 0 || syncIdx > setIdx {
				t.Errorf("SYNC must come before SETTINGS: %q", got)
			}
			// ON CLUSTER (when present) sits before SYNC.
			if tc.onCluster != "" {
				ocIdx := strings.Index(got, tc.onCluster)
				if ocIdx < 0 || ocIdx > syncIdx {
					t.Errorf("ON CLUSTER must come before SYNC: %q", got)
				}
			}
		})
	}
}
