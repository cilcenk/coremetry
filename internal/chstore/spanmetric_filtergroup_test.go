package chstore

import (
	"reflect"
	"testing"
)

// v0.8.x — trace-query gap-2 extended into Explore (spanMetric).
//
// SpanMetricFilter gained an optional FilterRoot *FilterGroup so an Explore
// panel can express grouped AND/OR predicates (mirroring TraceFilter). The
// HIGHEST-RISK contract is identical to the gap-2 trace path: a flat-AND
// FilterRoot MUST build a byte-identical WHERE to the legacy Filters slice, or
// a saved / shared Explore view silently changes shape. These tests pin:
//
//   (1) the WHERE QuerySpanMetric builds from a flat-AND FilterRoot is
//       byte-identical (conds + args) to the legacy Filters path — proven by
//       replaying the exact branch QuerySpanMetric uses
//       (FilterRoot != nil ? ApplyFilterGroup : ApplyFilters).
//   (2) effectiveFastPathFilters resolves a flat-AND FilterRoot's leaves to the
//       same slice the legacy path would inspect, so a grouped
//       `service.name = X` stays MV-eligible; an OR / nested root never reaches
//       the fast-path (gated upstream) and falls back to f.Filters.
//   (3) IsFlatAnd classifies the MV gate correctly: nil + flat-AND stay on the
//       MV fast-path; OR / nested groups disqualify it (like a free-text
//       Search), so they read raw spans.

// applySpanMetricFilter replays the exact predicate branch QuerySpanMetric runs
// (spanmetric.go): FilterRoot supersedes Filters; a flat-AND root delegates to
// ApplyFilters inside ApplyFilterGroup. Pure function (no Store / no ctx) so the
// back-compat contract is testable without a live CH connection.
func applySpanMetricFilter(f SpanMetricFilter) whereClause {
	var wc whereClause
	if f.FilterRoot != nil {
		ApplyFilterGroup(&wc, *f.FilterRoot)
	} else {
		ApplyFilters(&wc, f.Filters)
	}
	return wc
}

func TestSpanMetric_FlatAndFilterRoot_ByteIdenticalToLegacy(t *testing.T) {
	filters := sampleFilters() // shared with filtergroup_test.go

	legacy := applySpanMetricFilter(SpanMetricFilter{Filters: filters})
	grouped := applySpanMetricFilter(SpanMetricFilter{
		FilterRoot: &FilterGroup{Join: "AND", Filters: filters},
	})

	if !reflect.DeepEqual(legacy.conds, grouped.conds) {
		t.Fatalf("back-compat regression: flat-AND FilterRoot conds differ from legacy Filters.\nlegacy:  %#v\ngrouped: %#v", legacy.conds, grouped.conds)
	}
	if !reflect.DeepEqual(legacy.args, grouped.args) {
		t.Fatalf("back-compat regression: flat-AND FilterRoot args differ from legacy Filters.\nlegacy:  %#v\ngrouped: %#v", legacy.args, grouped.args)
	}

	// And an OR FilterRoot must add exactly ONE combined conjunct (not N) so the
	// boolean structure survives ANDing onto time/groupBy predicates.
	or := applySpanMetricFilter(SpanMetricFilter{
		FilterRoot: &FilterGroup{Join: "OR", Filters: filters},
	})
	if len(or.conds) != 1 {
		t.Fatalf("OR FilterRoot must add exactly one combined conjunct; got %d: %#v", len(or.conds), or.conds)
	}
}

func TestSpanMetric_EffectiveFastPathFilters(t *testing.T) {
	legacyLeaf := []FilterExpr{{Key: "service.name", Op: "=", Values: []string{"checkout"}}}

	// nil root → legacy f.Filters slice (identity).
	got := SpanMetricFilter{Filters: legacyLeaf}.effectiveFastPathFilters()
	if !reflect.DeepEqual(got, legacyLeaf) {
		t.Fatalf("nil root: effectiveFastPathFilters = %#v, want legacy %#v", got, legacyLeaf)
	}

	// flat-AND root → the root's leaves (so a grouped service.name = X stays
	// MV-eligible, byte-identical to passing it via f.Filters).
	got = SpanMetricFilter{
		FilterRoot: &FilterGroup{Join: "AND", Filters: legacyLeaf},
	}.effectiveFastPathFilters()
	if !reflect.DeepEqual(got, legacyLeaf) {
		t.Fatalf("flat-AND root: effectiveFastPathFilters = %#v, want %#v", got, legacyLeaf)
	}

	// OR root → never reaches the fast-path (gated upstream by IsFlatAnd), but
	// the resolver returns f.Filters defensively rather than the OR leaves.
	or := SpanMetricFilter{
		Filters:    legacyLeaf,
		FilterRoot: &FilterGroup{Join: "OR", Filters: legacyLeaf},
	}
	if !reflect.DeepEqual(or.effectiveFastPathFilters(), legacyLeaf) {
		t.Fatalf("OR root: effectiveFastPathFilters should fall back to f.Filters")
	}
}

func TestSpanMetric_MVGate_FlatAndStaysEligible(t *testing.T) {
	// QuerySpanMetric gates the MV fast-paths on f.FilterRoot.IsFlatAnd().
	// Pin the classification the gate relies on: nil + flat-AND keep the MV;
	// OR / nested disqualify it (same as a free-text Search).
	var nilRoot *FilterGroup
	if !nilRoot.IsFlatAnd() {
		t.Fatalf("nil FilterRoot must classify flat-AND (MV eligible)")
	}
	flat := &FilterGroup{Join: "AND", Filters: sampleFilters()}
	if !flat.IsFlatAnd() {
		t.Fatalf("flat-AND FilterRoot must classify flat-AND (MV eligible)")
	}
	or := &FilterGroup{Join: "OR", Filters: []FilterExpr{
		{Key: "a", Op: "=", Values: []string{"1"}},
		{Key: "b", Op: "=", Values: []string{"2"}},
	}}
	if or.IsFlatAnd() {
		t.Fatalf("OR FilterRoot must NOT classify flat-AND (falls to raw spans)")
	}
	nested := &FilterGroup{Join: "AND", Groups: []FilterGroup{{Join: "OR"}}}
	if nested.IsFlatAnd() {
		t.Fatalf("nested FilterRoot must NOT classify flat-AND")
	}
}
