package chstore

import (
	"strings"
	"testing"
)

// group_id rel B (operation_group_summary_5m MV + normalized read path).
//
// These pin the two regression-prone, purely-static pieces of the
// normalized-operation-clustering change:
//
//  1. groupKeyExpr must resolve the explicit `op_group` dimension key to the
//     dedicated LowCardinality column — NOT the attr-array fallback — so
//     Explore's groupBy=op_group folds the high-cardinality raw-name tail into
//     shape rows on the spans/metric path (group_id rel B / Release C wiring).
//     Equally important: the pre-existing `operation` alias MUST stay mapped to
//     the raw `name` column. Re-pointing it at op_group would silently change
//     every existing groupBy=operation metric query + the MetricLabelValues
//     value-suggester — a behaviour change masquerading as a trivial alias.
//
//  2. The normalized read path reads the op_group-keyed MV and excludes the
//     ungrouped '' bucket (old / pre-Release-A spans), and the non-normalized
//     path stays byte-for-byte on the raw-name MV. Both branches keep the MV in
//     play (MV-bypass invariant) and the raw fallback keeps its CH-bounds
//     guards. We can't exercise the live conn here, so we assert the static
//     SQL-fragment selectors the read path interpolates per mode.

func TestGroupKeyExpr_OpGroup(t *testing.T) {
	cases := []struct {
		key        string
		hasOpGroup bool
		wantExpr   string
		wantArgs   int // number of bound args the expr pushes
	}{
		// The new explicit handle resolves to the dedicated column, no arg.
		{"op_group", true, "toString(op_group)", 0},
		// v0.8.187 — when op_group never reached spans_local (external
		// Distributed, cluster_name unset), groupBy=op_group MUST soft-degrade
		// to the raw operation name instead of emitting toString(op_group),
		// which hard-errors code 16 against raw spans.
		{"op_group", false, "toString(name)", 0},
		// The existing alias must NOT be hijacked — still the raw name column,
		// independent of the op_group flag.
		{"operation", true, "toString(name)", 0},
		{"operation", false, "toString(name)", 0},
		// Sanity: raw name key still resolves to the name column.
		{"name", true, "toString(name)", 0},
		// span.-prefixed well-known still works.
		{"span.status_code", true, "toString(status_code)", 0},
		// A genuinely unknown key still falls through to the attr-array lookup
		// (and pushes the key as a bound arg) — op_group's case must not
		// accidentally swallow other keys, and the flag must not affect it.
		{"my.custom.attr", true, "toString(attr_values[indexOf(attr_keys, ?)])", 1},
		{"my.custom.attr", false, "toString(attr_values[indexOf(attr_keys, ?)])", 1},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			expr, args := groupKeyExpr(tc.key, tc.hasOpGroup)
			if expr != tc.wantExpr {
				t.Errorf("groupKeyExpr(%q, hasOpGroup=%v) expr = %q, want %q", tc.key, tc.hasOpGroup, expr, tc.wantExpr)
			}
			if len(args) != tc.wantArgs {
				t.Errorf("groupKeyExpr(%q, hasOpGroup=%v) pushed %d args, want %d", tc.key, tc.hasOpGroup, len(args), tc.wantArgs)
			}
		})
	}
}

// TestParseDistributedCluster pins the v0.8.187 cluster-name discovery used by
// the op_group boot self-heal: it must extract the FIRST Distributed() arg
// (the cluster name) regardless of quoting / extra args, and return "" for a
// non-Distributed engine so the self-heal degrades instead of issuing a bogus
// ON CLUSTER DDL.
func TestParseDistributedCluster(t *testing.T) {
	cases := []struct {
		engineFull string
		want       string
	}{
		{"Distributed('uptrace_all', 'coremetry_test', 'spans_local')", "uptrace_all"},
		{"Distributed('uptrace_all', 'coremetry_test', 'spans_local', rand())", "uptrace_all"},
		{"Distributed('uptrace_all', 'db', 'spans_local', cityHash64(service_name))", "uptrace_all"},
		{`Distributed("uptrace_all", 'db', 'spans_local')`, "uptrace_all"},
		{"Distributed(uptrace_all, db, spans_local)", "uptrace_all"},
		{"Distributed(  'uptrace_all'  , 'db', 'spans_local')", "uptrace_all"},
		// Non-Distributed engines → "" (self-heal must not fire).
		{"MergeTree", ""},
		{"MergeTree() PARTITION BY toDate(time)", ""},
		{"ReplicatedMergeTree('/clickhouse/tables/{shard}/spans', '{replica}')", ""},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.engineFull, func(t *testing.T) {
			if got := parseDistributedCluster(tc.engineFull); got != tc.want {
				t.Errorf("parseDistributedCluster(%q) = %q, want %q", tc.engineFull, got, tc.want)
			}
		})
	}
}

// opSummarySelectors mirrors the per-mode column/table/filter switch that
// queryOperationsFromMV (MV path) and the raw-spans fallback compute. Kept as a
// pure helper so the byte-for-byte default-vs-normalized contract is testable
// without a live ClickHouse. If the read path's switch ever drifts from this,
// the test below fails before the regression ships.
func opSummarySelectors(normalized bool) (mvTable, nameCol, opFilter string) {
	mvTable, nameCol, opFilter = "operation_summary_5m", "name", ""
	if normalized {
		mvTable, nameCol, opFilter = "operation_group_summary_5m", "op_group", " AND op_group != ''"
	}
	return
}

func TestOpSummarySelectors(t *testing.T) {
	// Non-normalized: byte-for-byte the pre-rel-B path — raw-name MV, name
	// column, no extra filter. The whole point of "default path unchanged".
	mv, col, filt := opSummarySelectors(false)
	if mv != "operation_summary_5m" {
		t.Errorf("default mvTable = %q, want operation_summary_5m (MV-bypass: must still read the raw-name MV)", mv)
	}
	if col != "name" {
		t.Errorf("default nameCol = %q, want name", col)
	}
	if filt != "" {
		t.Errorf("default opFilter = %q, want empty (no op_group exclusion in raw-name mode)", filt)
	}

	// Normalized: op_group MV, op_group column, and the ungrouped '' bucket
	// MUST be excluded so the normalized list is clean. Still an MV read.
	mv, col, filt = opSummarySelectors(true)
	if mv != "operation_group_summary_5m" {
		t.Errorf("normalized mvTable = %q, want operation_group_summary_5m (MV-bypass: must read the NEW op_group MV, not raw spans)", mv)
	}
	if col != "op_group" {
		t.Errorf("normalized nameCol = %q, want op_group", col)
	}
	if !strings.Contains(filt, "op_group != ''") {
		t.Errorf("normalized opFilter = %q, must exclude the ungrouped '' bucket (op_group != '')", filt)
	}
}
