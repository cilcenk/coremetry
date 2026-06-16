package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.8.186 — operator-reported PRODUCTION ingest outage on an external
// Distributed ClickHouse with cfg.ClusterName unset. The v0.8.172 op_group
// span column (EXPLICIT Go-written, bound positionally in the spans INSERT)
// never reached the per-shard spans_local because adaptDDL can't ON CLUSTER
// the ALTER without a cluster name. The INSERT then failed every flush with
// code 16 ("No such column op_group in table spans_local") → 10000 spans lost
// per batch → zero ingest.
//
// The fix makes op_group OPTIONAL in the INSERT: when s.hasOpGroupCol is
// false the column AND its per-row value are BOTH dropped, in lockstep, so
// the statement matches the real schema. The single failure mode this test
// pins is POSITIONAL MISALIGNMENT — if the column list and the value list
// ever disagree on whether op_group is present (or its position), CH writes
// every span's data into the wrong column and corrupts the whole table. So we
// assert, for BOTH toggle states, that:
//   - the value count equals the column count (the alignment invariant);
//   - op_group appears in the column list iff withOpGroup;
//   - op_group, when present, is LAST (the physical-order contract);
//   - the base column list (sans op_group) matches the spans CREATE TABLE
//     order in store.go exactly, sans the materialized `cluster` column.

// spansCreateOrder is the physical, non-materialized column order of the spans
// table as declared in the CREATE TABLE in store.go (trace_id … op_group),
// EXCLUDING the materialized `cluster` column (never in an INSERT). If the
// schema gains/reorders a column, this slice must change in lockstep with
// spansInsertColumns + spanAppendArgs — and this test fails loudly until it
// does.
var spansCreateOrder = []string{
	"trace_id", "span_id", "parent_id", "name", "kind",
	"service_name", "host_name", "deploy_env", "status_code", "status_msg",
	"time", "duration",
	"db_system", "db_statement", "http_method", "http_route", "http_status",
	"rpc_system", "rpc_method", "peer_service", "msg_system",
	"attr_keys", "attr_values", "res_keys", "res_values",
	"events", "scope_name", "op_group",
}

func TestSpansInsert_ColumnValueAlignment(t *testing.T) {
	sp := &Span{
		TraceID: "t", SpanID: "s", ParentID: "p", Name: "GET /x", Kind: "server",
		ServiceName: "svc", HostName: "h", DeployEnv: "prod",
		StatusCode: "ok", StatusMsg: "", Time: time.Unix(1, 0), Duration: 42,
		DBSystem: "", DBStatement: "", HTTPMethod: "GET", HTTPRoute: "/x", HTTPStatus: 200,
		RPCSystem: "", RPCMethod: "", PeerService: "", MsgSystem: "",
		AttrKeys: []string{"a"}, AttrValues: []string{"1"},
		ResKeys: []string{"r"}, ResValues: []string{"2"},
		Events: "[]", ScopeName: "scope", OpGroup: "GET /x/:id",
	}

	for _, withOpGroup := range []bool{true, false} {
		cols := spansInsertColumnNames(withOpGroup)
		args := spanAppendArgs(sp, withOpGroup)

		// THE alignment invariant: one value per column, both ways.
		if len(cols) != len(args) {
			t.Fatalf("withOpGroup=%v: column/value count mismatch — %d columns, %d values (positional corruption risk)",
				withOpGroup, len(cols), len(args))
		}

		// op_group present iff withOpGroup, and LAST when present.
		lastIsOpGroup := cols[len(cols)-1] == "op_group"
		anyOpGroup := false
		for _, c := range cols {
			if c == "op_group" {
				anyOpGroup = true
			}
		}
		if anyOpGroup != withOpGroup {
			t.Fatalf("withOpGroup=%v: op_group presence in column list = %v, want %v", withOpGroup, anyOpGroup, withOpGroup)
		}
		if withOpGroup && !lastIsOpGroup {
			t.Fatalf("withOpGroup=true: op_group must be the LAST column, got %q", cols[len(cols)-1])
		}

		// The SQL must list exactly these columns in order.
		gotSQL := spansInsertSQL(withOpGroup)
		wantSQL := "INSERT INTO spans (" + strings.Join(cols, ", ") + ")"
		if gotSQL != wantSQL {
			t.Fatalf("withOpGroup=%v: spansInsertSQL = %q, want %q", withOpGroup, gotSQL, wantSQL)
		}
	}
}

// TestSpansInsert_ColumnsMatchSchema pins the INSERT column list to the spans
// CREATE TABLE physical order. The with-op_group list must equal the full
// schema order; the without-op_group list must equal it minus the trailing
// op_group. A future column added to the schema (or a reorder) breaks this
// until spansInsertColumns + spanAppendArgs are updated together — exactly the
// guardrail that keeps the positional INSERT honest.
func TestSpansInsert_ColumnsMatchSchema(t *testing.T) {
	// With op_group: must equal the full CREATE order.
	withCols := spansInsertColumnNames(true)
	if len(withCols) != len(spansCreateOrder) {
		t.Fatalf("with-op_group column count = %d, schema order count = %d", len(withCols), len(spansCreateOrder))
	}
	for i := range spansCreateOrder {
		if withCols[i] != spansCreateOrder[i] {
			t.Fatalf("with-op_group column[%d] = %q, schema = %q", i, withCols[i], spansCreateOrder[i])
		}
	}

	// Without op_group: must equal the CREATE order minus the trailing op_group.
	wantBase := spansCreateOrder[:len(spansCreateOrder)-1]
	base := spansInsertColumnNames(false)
	if len(base) != len(wantBase) {
		t.Fatalf("without-op_group column count = %d, want %d", len(base), len(wantBase))
	}
	for i := range wantBase {
		if base[i] != wantBase[i] {
			t.Fatalf("without-op_group column[%d] = %q, want %q", i, base[i], wantBase[i])
		}
	}
	if last := spansCreateOrder[len(spansCreateOrder)-1]; last != "op_group" {
		t.Fatalf("schema order must end in op_group, ends in %q — update this test if the schema changed", last)
	}
}

// TestSpansInsertColumns_NoOpGroupLeak guards the base slice never silently
// gains op_group (which would double it when withOpGroup appends another).
func TestSpansInsertColumns_NoOpGroupLeak(t *testing.T) {
	for _, c := range spansInsertColumns {
		if c == "op_group" {
			t.Fatal("spansInsertColumns (the base list) must NOT contain op_group — it is appended conditionally")
		}
		if c == "cluster" {
			t.Fatal("spansInsertColumns must NOT contain the materialized `cluster` column — it is never in an INSERT")
		}
	}
}
