package chstore

import (
	"strings"
	"testing"
)

// service_display_filters_test.go — v0.9.345.
//
// errorsOnly / minSpans / minP99 ran in the BROWSER over the 50 rows of the
// current /services page. At 1000s of services "Errors only" could empty page
// 1 while erroring services sat on page 7, and nothing on screen said so —
// the last instance of the family swept out of the triage queue
// (v0.9.322/330/335/336) and /api/problems (v0.9.342).
//
// They are aggregate predicates, so they belong in the HAVING, where the
// LIMIT/OFFSET can respect them: paging now walks the MATCHING services.
//
// The predicate is built ONCE and rendered against each query's aliases,
// because the raw-spans path and the MV path name the same quantities
// differently and the operator switches between them just by picking a
// cluster or an env. Two builders would eventually disagree about what
// "Errors only" means depending on an unrelated filter.
func TestServiceDisplayFiltersHaving(t *testing.T) {
	cases := []struct {
		name     string
		f        ServiceDisplayFilters
		wantSQL  string
		wantArgs int
	}{
		// No constraint → no HAVING at all. An empty input box must not
		// become `span_count >= 0`, which would still be a clause to plan.
		{"empty", ServiceDisplayFilters{}, "", 0},
		{"errors only", ServiceDisplayFilters{ErrorsOnly: true}, " HAVING errs > 0", 0},
		{"min spans", ServiceDisplayFilters{MinSpans: 1000}, " HAVING spans >= ?", 1},
		{"min p99", ServiceDisplayFilters{MinP99Ms: 250}, " HAVING p99_ms >= ?", 1},
		{"all three", ServiceDisplayFilters{ErrorsOnly: true, MinSpans: 10, MinP99Ms: 5},
			" HAVING errs > 0 AND spans >= ? AND p99_ms >= ?", 2},
		// Zero means "no constraint" — that is what a blank box sends, and a
		// service with genuinely zero spans is not what the operator asked to
		// exclude by leaving the field empty.
		{"explicit zeros are not constraints",
			ServiceDisplayFilters{MinSpans: 0, MinP99Ms: 0}, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql, args := tc.f.having("spans", "errs", "p99_ms")
			if sql != tc.wantSQL {
				t.Errorf("sql = %q, want %q", sql, tc.wantSQL)
			}
			if len(args) != tc.wantArgs {
				t.Errorf("args = %d, want %d", len(args), tc.wantArgs)
			}
		})
	}
}

// The aliases are parameters precisely so the two read paths can use one
// builder. Pin that they are actually honoured — a hardcoded column name
// would compile and then fail at query time on whichever path did not match.
func TestServiceDisplayFiltersUsesGivenAliases(t *testing.T) {
	f := ServiceDisplayFilters{ErrorsOnly: true, MinSpans: 1, MinP99Ms: 1}

	raw, _ := f.having("span_count", "error_count", "p99_ms")
	for _, want := range []string{"error_count > 0", "span_count >= ?"} {
		if !strings.Contains(raw, want) {
			t.Errorf("raw-path HAVING %q is missing %q", raw, want)
		}
	}
	mv, _ := f.having("spans", "errs", "p99_ms")
	for _, want := range []string{"errs > 0", "spans >= ?"} {
		if !strings.Contains(mv, want) {
			t.Errorf("MV-path HAVING %q is missing %q", mv, want)
		}
	}
	// Argument COUNT must not depend on the aliases — the binds are the same
	// two values either way, and a mismatch here is a placeholder-order bug.
	_, a1 := f.having("span_count", "error_count", "p99_ms")
	_, a2 := f.having("spans", "errs", "p99_ms")
	if len(a1) != len(a2) {
		t.Errorf("arg count differs by alias set: %d vs %d", len(a1), len(a2))
	}
}

// Active() drives whether the page still needs its "filtered on this page
// only" affordance. It must agree with having(): if one says "no constraint"
// while the other emits SQL, the UI and the query disagree.
func TestServiceDisplayFiltersActiveAgreesWithHaving(t *testing.T) {
	for _, f := range []ServiceDisplayFilters{
		{},
		{ErrorsOnly: true},
		{MinSpans: 5},
		{MinP99Ms: 5},
		{ErrorsOnly: true, MinSpans: 5, MinP99Ms: 5},
	} {
		sql, _ := f.having("spans", "errs", "p99_ms")
		if f.Active() != (sql != "") {
			t.Errorf("Active()=%v but HAVING=%q for %+v", f.Active(), sql, f)
		}
	}
}
