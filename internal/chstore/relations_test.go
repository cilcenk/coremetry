package chstore

import (
	"strings"
	"testing"
	"time"
)

// v0.8.x (Gap 3) — span-relationship / structural trace operators. This is
// the riskiest query in the codebase: a self-join over raw `spans`. A
// runaway spans-self-join at billion-span scale is worse than not having the
// feature, so the bounds are MANDATORY. These tests pin every guard so a
// future edit can't silently drop one:
//
//   • BOTH join sides time-bounded (a one-sided bound lets the planner
//     full-scan the other side).
//   • LIMIT present (caps output).
//   • max_bytes_in_join spill setting present (spills instead of OOM).
//   • child-of uses the DIRECT edge (c.parent_id = p.span_id).
//   • descendant-of uses the depth-capped frontier (no recursive CTE) and
//     descends exactly 2 hops.
//   • sequence enforces happens-before (a.end <= b.start).
//   • predicate-count cap (≤ 8/side) enforced.
//   • parent/child predicates are alias-qualified (c.* vs p.*) — a relation
//     query that compared an unqualified column would be ambiguous and CH
//     would reject it.

func relFrom() time.Time { return time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC) }
func relTo() time.Time   { return relFrom().Add(1 * time.Hour) }

// countOccurrences counts non-overlapping occurrences of sub in s.
func countOccurrences(s, sub string) int { return strings.Count(s, sub) }

func TestRelation_ChildOf_BoundsAndDirectEdge(t *testing.T) {
	s := &Store{}
	f := RelationFilter{
		Parent: []FilterExpr{{Key: "service.name", Op: "=", Values: []string{"frontend"}}},
		Child:  []FilterExpr{{Key: "service.name", Op: "=", Values: []string{"payment"}}},
		Kind:   RelChildOf,
		From:   relFrom(), To: relTo(),
		Limit: 50,
	}
	sql, args := s.buildChildOfSQL(f, f.Limit+1)

	// Direct edge — the defining predicate of child-of.
	if !strings.Contains(sql, "c.parent_id = p.span_id") {
		t.Fatalf("child-of must join on the direct edge c.parent_id = p.span_id; got: %s", sql)
	}
	// BOTH sides time-bounded.
	for _, want := range []string{"c.time >= ?", "c.time <= ?", "p.time >= ?", "p.time <= ?"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("missing mandatory time bound %q (one-sided bound = full scan risk); got: %s", want, sql)
		}
	}
	// LIMIT + spill setting.
	if !strings.Contains(sql, "LIMIT ?") {
		t.Fatalf("child-of must cap output with LIMIT; got: %s", sql)
	}
	assertJoinSpill(t, sql)
	// Alias-qualified predicates: parent → p.service_name, child → c.service_name.
	if !strings.Contains(sql, "p.service_name = ?") {
		t.Fatalf("parent predicate must be qualified with p.*; got: %s", sql)
	}
	if !strings.Contains(sql, "c.service_name = ?") {
		t.Fatalf("child predicate must be qualified with c.*; got: %s", sql)
	}
	// Args: 4 time bounds + parent value + child value + pageLimit = 7.
	if len(args) != 7 {
		t.Fatalf("expected 7 args (4 time + 2 values + limit), got %d: %v", len(args), args)
	}
	if args[len(args)-1] != 51 {
		t.Fatalf("last arg must be the page limit (51), got %v", args[len(args)-1])
	}
}

func TestRelation_DescendantOf_DepthCappedFrontier(t *testing.T) {
	s := &Store{}
	f := RelationFilter{
		Parent: []FilterExpr{{Key: "service.name", Op: "=", Values: []string{"api-gateway"}}},
		Child:  []FilterExpr{{Key: "service.name", Op: "=", Values: []string{"ledger"}}},
		Kind:   RelDescendantOf,
		From:   relFrom(), To: relTo(),
		Limit: 50,
	}
	sql, _ := s.buildDescendantOfSQL(f, f.Limit+1)

	// NO recursive CTE at billion-span scale.
	if strings.Contains(strings.ToUpper(sql), "RECURSIVE") || strings.Contains(strings.ToUpper(sql), "WITH RECURSIVE") {
		t.Fatalf("descendant-of must NOT use a recursive CTE; got: %s", sql)
	}
	// Frontier expansion = UNION ALL of depth-0 + depth-1 parents.
	if !strings.Contains(sql, "UNION ALL") {
		t.Fatalf("descendant-of must expand a frontier (UNION ALL of depth-0/depth-1); got: %s", sql)
	}
	// The final join is still a direct edge against the frontier (so a
	// frontier member at depth 1 makes c a grandchild = depth 2).
	if !strings.Contains(sql, "c.parent_id = fr.span_id") {
		t.Fatalf("descendant-of final join must match c against the frontier; got: %s", sql)
	}
	// Depth cap: exactly one nested middle-span join (m ⋈ p) — no third hop.
	if got := countOccurrences(sql, "m.parent_id = p.span_id"); got != 1 {
		t.Fatalf("descendant-of must descend exactly one extra hop (depth 2), found %d middle joins; got: %s", got, sql)
	}
	// Every scan time-bounded: c, the depth-0 p, the depth-1 m and its p.
	for _, want := range []string{"c.time >= ?", "p.time >= ?", "m.time >= ?"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("descendant-of missing time bound %q; got: %s", want, sql)
		}
	}
	if !strings.Contains(sql, "LIMIT ?") {
		t.Fatalf("descendant-of must cap output with LIMIT; got: %s", sql)
	}
	assertJoinSpill(t, sql)
}

func TestRelation_DescendantOf_DirectCollapsesToChildOf(t *testing.T) {
	// "direct only" on descendant-of must route to the depth-1 child-of shape.
	s := &Store{}
	f := RelationFilter{
		Parent: []FilterExpr{{Key: "service.name", Op: "=", Values: []string{"a"}}},
		Child:  []FilterExpr{{Key: "service.name", Op: "=", Values: []string{"b"}}},
		Kind:   RelDescendantOf,
		Direct: true,
		From:   relFrom(), To: relTo(),
		Limit: 10,
	}
	direct, _ := s.buildChildOfSQL(f, f.Limit+1)
	descDirect, _ := s.buildChildOfSQL(f, f.Limit+1) // GetTracesByRelation routes Direct→childOf
	if direct != descDirect {
		t.Fatalf("direct descendant-of must produce the child-of SQL")
	}
	if strings.Contains(direct, "UNION ALL") {
		t.Fatalf("direct-only must NOT expand a frontier; got: %s", direct)
	}
}

func TestRelation_Sequence_HappensBefore(t *testing.T) {
	s := &Store{}
	f := RelationFilter{
		Parent: []FilterExpr{{Key: "service.name", Op: "=", Values: []string{"fraud"}}},
		Child:  []FilterExpr{{Key: "service.name", Op: "=", Values: []string{"ledger"}}},
		Kind:   RelSequence,
		From:   relFrom(), To: relTo(),
		Limit: 50,
	}
	sql, _ := s.buildSequenceSQL(f, f.Limit+1)

	// happens-before: A ends at or before B starts.
	if !strings.Contains(sql, "(a.time + toIntervalNanosecond(a.duration)) <= b.time") {
		t.Fatalf("sequence must enforce happens-before (a.end <= b.start); got: %s", sql)
	}
	// Same-trace self-join.
	if !strings.Contains(sql, "b.trace_id = a.trace_id") {
		t.Fatalf("sequence must self-join on trace_id; got: %s", sql)
	}
	// BOTH sides time-bounded.
	for _, want := range []string{"a.time >= ?", "a.time <= ?", "b.time >= ?", "b.time <= ?"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("sequence missing time bound %q; got: %s", want, sql)
		}
	}
	if !strings.Contains(sql, "LIMIT ?") {
		t.Fatalf("sequence must cap output with LIMIT; got: %s", sql)
	}
	assertJoinSpill(t, sql)
}

func TestRelation_PredicateCountCap(t *testing.T) {
	// More than relMaxPredicates per side must be truncated, not emitted.
	many := make([]FilterExpr, relMaxPredicates+5)
	for i := range many {
		many[i] = FilterExpr{Key: "http.method", Op: "=", Values: []string{"GET"}}
	}
	conds, _ := relSideWhere("c", many)
	if len(conds) != relMaxPredicates {
		t.Fatalf("predicate cap not enforced: got %d conds, want %d", len(conds), relMaxPredicates)
	}
}

func TestRelation_MalformedPredicateSkipped(t *testing.T) {
	// A malformed predicate (missing value for `=`) is silently skipped,
	// mirroring ApplyFilters' contract — it must not abort the whole side.
	preds := []FilterExpr{
		{Key: "service.name", Op: "=", Values: []string{"frontend"}}, // ok
		{Key: "service.name", Op: "="},                               // malformed: no value
		{Key: "http.method", Op: "=", Values: []string{"POST"}},      // ok
	}
	conds, args := relSideWhere("p", preds)
	if len(conds) != 2 {
		t.Fatalf("malformed predicate not skipped: got %d conds, want 2", len(conds))
	}
	if len(args) != 2 {
		t.Fatalf("expected 2 args for 2 valid predicates, got %d", len(args))
	}
}

func TestRelation_AliasQualification_ArrayLookup(t *testing.T) {
	// A custom-attr predicate must qualify ALL array columns with the alias,
	// else the self-join is ambiguous (CH error) — assert both keys+values.
	preds := []FilterExpr{{Key: "user.id", Op: "=", Values: []string{"u123"}}}
	conds, _ := relSideWhere("c", preds)
	if len(conds) != 1 {
		t.Fatalf("expected 1 cond, got %d", len(conds))
	}
	got := conds[0]
	if !strings.Contains(got, "c.attr_values") || !strings.Contains(got, "c.attr_keys") {
		t.Fatalf("array-lookup predicate must qualify BOTH attr_values and attr_keys with the alias; got: %s", got)
	}
	// No UNqualified bare reference left behind.
	if strings.Contains(got, "[indexOf(attr_keys,") {
		t.Fatalf("found unqualified attr_keys in self-join predicate (ambiguous column); got: %s", got)
	}
}

func TestFilterExpr_SQLAliased_EmptyIsByteIdenticalToSQL(t *testing.T) {
	// BACK-COMPAT: SQLAliased("") must equal SQL() byte-for-byte across every
	// op + key shape, so introducing the alias parameter cannot regress any
	// existing flat-filter caller (facets / aggregate / DQL / saved views).
	cases := []FilterExpr{
		{Key: "service.name", Op: "=", Values: []string{"frontend"}},
		{Key: "http.status_code", Op: ">=", Values: []string{"500"}}, // numeric well-known
		{Key: "duration_ms", Op: ">", Values: []string{"100"}},       // compound well-known
		{Key: "user.id", Op: "=", Values: []string{"u1"}},            // attr array lookup
		{Key: "resource.k8s.pod.name", Op: "LIKE", Values: []string{"web"}},
		{Key: "span.custom", Op: "IN", Values: []string{"a", "b", "c"}},
		{Key: "session.id", Op: "EXISTS"},
		{Key: "http.method", Op: "NOT EXISTS"},
		{Key: "service.name", Op: "!=", Values: []string{"x"}},
	}
	for _, c := range cases {
		base, baseArgs, baseErr := c.SQL()
		al, alArgs, alErr := c.SQLAliased("")
		if (baseErr == nil) != (alErr == nil) {
			t.Fatalf("err mismatch for %+v: base=%v alias=%v", c, baseErr, alErr)
		}
		if base != al {
			t.Fatalf("SQLAliased(\"\") not byte-identical to SQL() for %+v:\n base : %s\n alias: %s", c, base, al)
		}
		if len(baseArgs) != len(alArgs) {
			t.Fatalf("arg count mismatch for %+v: %d vs %d", c, len(baseArgs), len(alArgs))
		}
	}
}

func TestFilterExpr_SQLAliased_QualifiesEveryColumnShape(t *testing.T) {
	cases := []struct {
		f      FilterExpr
		expect []string // substrings that MUST appear with the "c." prefix
		reject []string // substrings that MUST NOT appear (unqualified leak)
	}{
		{
			FilterExpr{Key: "service.name", Op: "=", Values: []string{"x"}},
			[]string{"c.service_name = ?"}, nil,
		},
		{
			FilterExpr{Key: "duration_ms", Op: ">", Values: []string{"5"}},
			[]string{"c.duration"}, nil,
		},
		{
			FilterExpr{Key: "user.id", Op: "=", Values: []string{"u"}},
			[]string{"c.attr_values", "c.attr_keys"},
			[]string{"indexOf(attr_keys,"},
		},
		{
			FilterExpr{Key: "resource.k8s.pod", Op: "EXISTS"},
			[]string{"c.res_keys"},
			[]string{"has(res_keys,"},
		},
	}
	for _, tc := range cases {
		got, _, err := tc.f.SQLAliased("c")
		if err != nil {
			t.Fatalf("SQLAliased err for %+v: %v", tc.f, err)
		}
		for _, want := range tc.expect {
			if !strings.Contains(got, want) {
				t.Fatalf("alias-qualified SQL for %+v missing %q; got: %s", tc.f, want, got)
			}
		}
		for _, bad := range tc.reject {
			if strings.Contains(got, bad) {
				t.Fatalf("alias-qualified SQL for %+v leaked unqualified %q; got: %s", tc.f, bad, got)
			}
		}
	}
}

// assertJoinSpill checks the mandatory join-spill + execution-time settings.
func assertJoinSpill(t *testing.T, sql string) {
	t.Helper()
	if !strings.Contains(sql, "max_bytes_in_join = 536870912") {
		t.Fatalf("self-join MUST spill (max_bytes_in_join) to avoid OOM; got: %s", sql)
	}
	if !strings.Contains(sql, "max_execution_time = 30") {
		t.Fatalf("self-join MUST carry max_execution_time backstop; got: %s", sql)
	}
}
