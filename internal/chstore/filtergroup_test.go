package chstore

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// v0.8.x — trace-query gap-2 (grouped AND/OR query builder).
//
// FilterGroup is the additive, default-off upgrade over the flat
// conjunction-only []FilterExpr path. The HIGHEST-RISK contract is
// back-compat: a flat-AND FilterGroup MUST emit byte-identical SQL + args to
// the legacy ApplyFilters path, or saved views / shared URLs / DQL / facets
// silently change shape and an operator's pinned query starts returning
// different rows. These tests pin:
//
//   (1) flat-AND FilterGroup == legacy []FilterExpr, byte-for-byte (SQL+args),
//       both standalone (BuildFilterGroupWhere vs BuildFilterWhere) and woven
//       through the full buildGetTracesWhere predicate stack.
//   (2) OR / nested-group precedence parenthesisation.
//   (3) malformed / empty leaves are silently skipped (mirrors ApplyFilters).
//   (4) the MV-gate guards (IsFlatAnd / hasPredicate) classify groups
//       correctly so an OR query falls to the raw-spans path (like Search).

// sampleFilters is a representative legacy filter set exercising the
// well-known-column path (service.name), the array-lookup path (custom attr),
// the numeric-cast path (>=) and a multi-value IN — every SQL() branch that a
// flat-AND group must reproduce identically.
func sampleFilters() []FilterExpr {
	return []FilterExpr{
		{Key: "service.name", Op: "=", Values: []string{"checkout"}},
		{Key: "http.status_code", Op: ">=", Values: []string{"500"}},
		{Key: "tenant", Op: "IN", Values: []string{"acme", "globex"}},
		{Key: "db.system", Op: "EXISTS"},
	}
}

func TestFlatAndGroup_ByteIdenticalToLegacy_Standalone(t *testing.T) {
	filters := sampleFilters()

	legacySQL, legacyArgs := BuildFilterWhere(filters)

	// A flat-AND group wrapping the SAME leaves, no nested groups.
	g := FilterGroup{Join: "AND", Filters: filters}
	groupSQL, groupArgs := BuildFilterGroupWhere(g)

	if legacySQL != groupSQL {
		t.Fatalf("back-compat regression: flat-AND group SQL differs from legacy.\nlegacy: %q\ngroup:  %q", legacySQL, groupSQL)
	}
	if !reflect.DeepEqual(legacyArgs, groupArgs) {
		t.Fatalf("back-compat regression: flat-AND group args differ from legacy.\nlegacy: %#v\ngroup:  %#v", legacyArgs, groupArgs)
	}
}

func TestFlatAndGroup_ByteIdenticalToLegacy_InGetTracesWhere(t *testing.T) {
	// The contract that actually matters end-to-end: the SAME predicate stack
	// (time / service / error / duration + filters) must come out identical
	// whether the operator's filters arrive via the legacy .Filters slice or a
	// flat-AND .FilterRoot. This is what a saved view / shared URL rides on.
	from := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)
	to := from.Add(1 * time.Hour)
	filters := sampleFilters()

	legacy := buildGetTracesWhere(TraceFilter{
		Service: "checkout", From: from, To: to, HasError: true, MinMs: 25,
		Filters: filters,
	})
	grouped := buildGetTracesWhere(TraceFilter{
		Service: "checkout", From: from, To: to, HasError: true, MinMs: 25,
		FilterRoot: &FilterGroup{Join: "AND", Filters: filters},
	})

	if legacy.sql() != grouped.sql() {
		t.Fatalf("back-compat regression: flat-AND FilterRoot WHERE differs from legacy Filters.\nlegacy:  %q\ngrouped: %q", legacy.sql(), grouped.sql())
	}
	if !reflect.DeepEqual(legacy.args, grouped.args) {
		t.Fatalf("back-compat regression: flat-AND FilterRoot args differ.\nlegacy:  %#v\ngrouped: %#v", legacy.args, grouped.args)
	}
}

func TestFlatAndGroup_DefaultJoinIsAnd(t *testing.T) {
	// An empty / missing / unknown join must default to AND (the safe,
	// legacy-equivalent operator) — a malformed URL must never silently
	// turn an AND query into an OR one.
	filters := sampleFilters()
	legacySQL, legacyArgs := BuildFilterWhere(filters)
	for _, join := range []string{"", "and", "AnD", "bogus"} {
		g := FilterGroup{Join: join, Filters: filters}
		if !g.isFlatAnd() {
			t.Fatalf("join %q should classify as flat-AND", join)
		}
		sql, args := BuildFilterGroupWhere(g)
		if sql != legacySQL {
			t.Fatalf("join %q: SQL %q != legacy %q", join, sql, legacySQL)
		}
		if !reflect.DeepEqual(args, legacyArgs) {
			t.Fatalf("join %q: args %#v != legacy %#v", join, args, legacyArgs)
		}
	}
}

func TestOrGroup_Parenthesisation(t *testing.T) {
	// (A OR B) renders as a single parenthesised fragment with the OR join,
	// so when ANDed onto the surrounding time/service predicates the OR can't
	// bind across them.
	g := FilterGroup{Join: "OR", Filters: []FilterExpr{
		{Key: "http.status_code", Op: ">=", Values: []string{"500"}},
		{Key: "db.system", Op: "=", Values: []string{"oracle"}},
	}}
	sql, args := BuildFilterGroupSQL(g)
	if !strings.HasPrefix(sql, "(") || !strings.HasSuffix(sql, ")") {
		t.Fatalf("OR group must be wrapped in parens; got %q", sql)
	}
	if !strings.Contains(sql, " OR ") {
		t.Fatalf("OR group must join with OR; got %q", sql)
	}
	if strings.Contains(sql, " AND ") {
		t.Fatalf("OR group must not contain AND; got %q", sql)
	}
	// Args preserve leaf order: http_status cast arg, then db_system value.
	wantArgs := []any{"500", "oracle"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("OR group args = %#v, want %#v", args, wantArgs)
	}
}

func TestNestedGroup_OneLevelPrecedence(t *testing.T) {
	// (http.status >= 500 OR db.system = oracle) AND env = prod
	// Top-level AND, with one nested OR group + one top-level leaf.
	g := FilterGroup{
		Join: "AND",
		Filters: []FilterExpr{
			{Key: "deployment.environment", Op: "=", Values: []string{"prod"}},
		},
		Groups: []FilterGroup{{
			Join: "OR",
			Filters: []FilterExpr{
				{Key: "http.status_code", Op: ">=", Values: []string{"500"}},
				{Key: "db.system", Op: "=", Values: []string{"oracle"}},
			},
		}},
	}
	sql, _ := BuildFilterGroupSQL(g)
	// The nested OR must be its own parenthesised sub-fragment so the
	// top-level AND can't reach inside it.
	if !strings.Contains(sql, " OR ") {
		t.Fatalf("nested group lost its OR; got %q", sql)
	}
	if !strings.Contains(sql, "deploy_env = ?") {
		t.Fatalf("top-level leaf missing; got %q", sql)
	}
	// Count parens: outer wrap + nested OR wrap = at least two '(' levels.
	if strings.Count(sql, "(") < 2 {
		t.Fatalf("expected nested parenthesisation (>=2 '('); got %q", sql)
	}
	// The nested OR fragment must be self-contained: the substring
	// "( ... OR ... )" appears before it is ANDed with the env leaf.
	orStart := strings.Index(sql, "http_status")
	andEnv := strings.Index(sql, "deploy_env")
	if orStart < 0 || andEnv < 0 {
		t.Fatalf("expected both nested and top-level predicates; got %q", sql)
	}
}

func TestGroup_SkipsMalformedLeaves(t *testing.T) {
	// Mirror ApplyFilters' silent-skip contract: a leaf with a bad operator
	// or missing value is dropped, not errored. A group of ONLY-bad leaves
	// contributes nothing (empty fragment).
	allBad := FilterGroup{Join: "OR", Filters: []FilterExpr{
		{Key: "x", Op: "BOGUS", Values: []string{"1"}}, // invalid op
		{Key: "", Op: "=", Values: []string{"1"}},      // missing key
		{Key: "y", Op: "=", Values: nil},               // missing value
	}}
	if sql, args := BuildFilterGroupSQL(allBad); sql != "" || args != nil {
		t.Fatalf("group of only-malformed leaves must yield empty; got sql=%q args=%#v", sql, args)
	}
	if allBad.hasPredicate() {
		t.Fatalf("group of only-malformed leaves must report no predicate")
	}

	// A mixed group keeps the good leaf only — and a SINGLE surviving OR leaf
	// reduces to that leaf's bare SQL (no spurious OR).
	mixed := FilterGroup{Join: "OR", Filters: []FilterExpr{
		{Key: "x", Op: "BOGUS", Values: []string{"1"}},
		{Key: "service.name", Op: "=", Values: []string{"checkout"}},
	}}
	sql, args := BuildFilterGroupSQL(mixed)
	if sql != "service_name = ?" {
		t.Fatalf("single surviving leaf should reduce to its bare SQL; got %q", sql)
	}
	if !reflect.DeepEqual(args, []any{"checkout"}) {
		t.Fatalf("single surviving leaf args = %#v", args)
	}
}

func TestFilterGroup_MVGateClassification(t *testing.T) {
	// The MV gate calls IsFlatAnd / hasPredicate on an optional *FilterGroup.
	// Pin the classifications the gate relies on:
	//   - nil root             → flat-AND, no predicate (MV eligible)
	//   - empty flat-AND       → flat-AND, no predicate (MV eligible)
	//   - flat-AND with leaves → flat-AND, HAS predicate (MV NOT eligible —
	//     same as any legacy non-empty Filters)
	//   - OR group             → NOT flat-AND, HAS predicate (MV NOT eligible)
	//   - nested group         → NOT flat-AND
	var nilRoot *FilterGroup
	if !nilRoot.IsFlatAnd() || nilRoot.hasPredicate() {
		t.Fatalf("nil root must be flat-AND with no predicate")
	}

	emptyAnd := &FilterGroup{Join: "AND"}
	if !emptyAnd.IsFlatAnd() || emptyAnd.hasPredicate() {
		t.Fatalf("empty flat-AND must be flat-AND with no predicate")
	}

	flatLeaves := &FilterGroup{Join: "AND", Filters: sampleFilters()}
	if !flatLeaves.IsFlatAnd() {
		t.Fatalf("flat-AND-with-leaves must classify flat-AND")
	}
	if !flatLeaves.hasPredicate() {
		t.Fatalf("flat-AND-with-leaves must report a predicate (disqualifies MV like legacy Filters)")
	}

	orGroup := &FilterGroup{Join: "OR", Filters: []FilterExpr{
		{Key: "a", Op: "=", Values: []string{"1"}},
		{Key: "b", Op: "=", Values: []string{"2"}},
	}}
	if orGroup.IsFlatAnd() {
		t.Fatalf("OR group must NOT classify flat-AND")
	}
	if !orGroup.hasPredicate() {
		t.Fatalf("OR group must report a predicate")
	}

	nested := &FilterGroup{Join: "AND", Groups: []FilterGroup{{Join: "OR"}}}
	if nested.IsFlatAnd() {
		t.Fatalf("group with nested Groups must NOT classify flat-AND")
	}
}

func TestApplyFilterGroup_FlatAndDelegatesVerbatim(t *testing.T) {
	// ApplyFilterGroup's flat-AND path must add the EXACT same conds, in the
	// same order, as ApplyFilters — this is the delegation that guarantees
	// byte-identical output. Compare conds count + each cond string.
	filters := sampleFilters()

	var legacy whereClause
	ApplyFilters(&legacy, filters)

	var grouped whereClause
	ApplyFilterGroup(&grouped, FilterGroup{Join: "AND", Filters: filters})

	if !reflect.DeepEqual(legacy.conds, grouped.conds) {
		t.Fatalf("flat-AND ApplyFilterGroup conds differ from ApplyFilters.\nlegacy:  %#v\ngrouped: %#v", legacy.conds, grouped.conds)
	}
	if !reflect.DeepEqual(legacy.args, grouped.args) {
		t.Fatalf("flat-AND ApplyFilterGroup args differ from ApplyFilters.\nlegacy:  %#v\ngrouped: %#v", legacy.args, grouped.args)
	}
	// And the OR path adds exactly ONE combined conjunct (not N).
	var orWC whereClause
	ApplyFilterGroup(&orWC, FilterGroup{Join: "OR", Filters: filters})
	if len(orWC.conds) != 1 {
		t.Fatalf("OR group must add exactly one combined conjunct; got %d: %#v", len(orWC.conds), orWC.conds)
	}
}
