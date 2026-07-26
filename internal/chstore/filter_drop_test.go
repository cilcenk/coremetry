package chstore

import (
	"encoding/json"
	"testing"
)

// v0.9.269 — the DB drill modal shipped `{key, op, value}` while FilterExpr
// binds `{k, op, v}` by JSON tag. Only `op` bound; Key arrived empty, SQL()
// returned "missing key", and ApplyFilters dropped the clause. The operator
// saw a chart drawn WITHOUT the filter, under a modal printing the filter
// chip — a wider result that looks entirely plausible.
//
// This pins the wire contract itself, so a producer that invents a
// lookalike shape fails here instead of in production.
func TestFilterExprWireContract(t *testing.T) {
	// The shape the frontend must send.
	const good = `{"k":"tablespace_name","op":"=","v":["SYSTEM"]}`
	var f FilterExpr
	if err := json.Unmarshal([]byte(good), &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if f.Key != "tablespace_name" || f.Op != "=" || len(f.Values) != 1 || f.Values[0] != "SYSTEM" {
		t.Fatalf("wire contract drifted: %+v", f)
	}

	// The shape that shipped, and why it was invisible: it parses cleanly as
	// JSON and yields a struct that is empty where it matters.
	const bad = `{"key":"tablespace_name","op":"=","value":"SYSTEM"}`
	var b FilterExpr
	if err := json.Unmarshal([]byte(bad), &b); err != nil {
		t.Fatalf("the broken shape must still PARSE — that is what made it silent: %v", err)
	}
	if b.Key != "" || len(b.Values) != 0 {
		t.Fatalf("expected key/values to bind to nothing, got %+v", b)
	}
	if _, _, err := b.SQL(); err == nil {
		t.Error("a filter with no key must fail to compile — otherwise it would " +
			"silently widen the query instead of narrowing it")
	}
}

// A clause that cannot compile must not quietly vanish into an unfiltered
// query. We keep skipping (one bad clause shouldn't 500 every caller) but the
// WHERE must be demonstrably narrower when the clause is good, and unchanged
// when it is dropped — which is exactly the failure the operator could not see.
func TestDroppedFilterLeavesQueryUnfiltered(t *testing.T) {
	var good whereClause
	ApplyFilters(&good, []FilterExpr{{Key: "db.system", Op: "=", Values: []string{"oracle"}}})

	var dropped whereClause
	ApplyFilters(&dropped, []FilterExpr{{Key: "", Op: "=", Values: []string{"SYSTEM"}}})

	if good.sql() == "" {
		t.Fatal("a valid filter produced no WHERE — the guard below would be meaningless")
	}
	if dropped.sql() != "" {
		t.Errorf("expected the malformed clause to be skipped, got %q", dropped.sql())
	}
	if good.sql() == dropped.sql() {
		t.Error("valid and dropped filters produced the same WHERE — the test cannot " +
			"distinguish filtered from unfiltered")
	}
}
