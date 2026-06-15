package chstore

import (
	"fmt"
	"testing"
)

// rc #3 (root-cause ribbon + list enrichment). GetHypotheses is the single
// batch read the /anomalies + /problems list handlers use to attach the
// root-cause ribbon summary WITHOUT a per-row fetch. The pure id-list guard
// boundHypothesisIDs is what keeps its `anchor_id IN (?, …)` clause bounded —
// drop empties, de-dup, cap. A regression that lets a duplicate or oversized id
// slice through re-opens the exact unbounded-IN fan-out the CH-query invariant
// (CLAUDE.md) guards against, so it gets a table-driven test at ship time.
func TestBoundHypothesisIDs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, nil},
		{"empty slice", []string{}, nil},
		{"all empties drop", []string{"", "", ""}, nil},
		{"passthrough order", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"drops empty entries", []string{"a", "", "b", ""}, []string{"a", "b"}},
		{
			"dedups keeping first occurrence",
			[]string{"a", "b", "a", "c", "b"},
			[]string{"a", "b", "c"},
		},
		{"empty + dup mixed", []string{"", "x", "x", "", "y"}, []string{"x", "y"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := boundHypothesisIDs(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("len = %d, want %d (got=%v)", len(got), len(c.want), got)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("got[%d]=%q, want %q (got=%v)", i, got[i], c.want[i], got)
				}
			}
		})
	}
}

// The cap is the load-bearing bound: an oversized id slice (e.g. a caller that
// forgets its own LIMIT) must NOT produce an IN-list longer than
// hypothesesIDCap. De-dup runs first, so cap applies to distinct ids.
func TestBoundHypothesisIDsCaps(t *testing.T) {
	// 2x the cap of DISTINCT ids → must clamp to exactly the cap.
	ids := make([]string, 0, hypothesesIDCap*2)
	for i := 0; i < hypothesesIDCap*2; i++ {
		ids = append(ids, fmt.Sprintf("id-%d", i))
	}
	got := boundHypothesisIDs(ids)
	if len(got) != hypothesesIDCap {
		t.Fatalf("cap not enforced: got %d, want %d", len(got), hypothesesIDCap)
	}
	// Clamp keeps the FIRST cap ids (order-preserving), so the last kept id
	// is id-(cap-1), never an id past the cap.
	if got[hypothesesIDCap-1] != fmt.Sprintf("id-%d", hypothesesIDCap-1) {
		t.Fatalf("clamp dropped from the wrong end: last kept = %q", got[hypothesesIDCap-1])
	}

	// Duplicates collapse BEFORE the cap: 3x cap copies of the same value is a
	// single id, never cap rows of one value.
	dupHeavy := make([]string, hypothesesIDCap*3)
	for i := range dupHeavy {
		dupHeavy[i] = "same"
	}
	if g := boundHypothesisIDs(dupHeavy); len(g) != 1 {
		t.Fatalf("dedup-before-cap broken: got %d, want 1", len(g))
	}
}

// summaryOf is the honest-no-cause projection the list ribbon depends on: a
// synthesized "no clear cause" hypothesis (empty suspect AND zero confidence)
// must project to nil so the row omits the summary and the ribbon shows the
// honest empty state — NOT a fake suspect. Any real signal (a suspect OR
// non-zero confidence) must survive so the ribbon can render it / say
// "computing…". This is the exact low/no-evidence contract rc #3's UI relies on.
func TestSummaryOf(t *testing.T) {
	cases := []struct {
		name    string
		in      RootCauseHypothesis
		wantNil bool
	}{
		{
			"empty suspect + zero confidence omits",
			RootCauseHypothesis{TopSuspect: "", Confidence: 0},
			true,
		},
		{
			"empty suspect + negative confidence omits",
			RootCauseHypothesis{TopSuspect: "", Confidence: -0.1},
			true,
		},
		{
			"clear suspect survives",
			RootCauseHypothesis{TopSuspect: "oracle-core", TopScore: 0.8, Confidence: 0.7},
			false,
		},
		{
			"confidence without suspect survives (computing/low-evidence)",
			RootCauseHypothesis{TopSuspect: "", Confidence: 0.2},
			false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := summaryOf(c.in)
			if c.wantNil && got != nil {
				t.Fatalf("want nil summary, got %+v", *got)
			}
			if !c.wantNil {
				if got == nil {
					t.Fatalf("want a summary, got nil")
				}
				if got.TopSuspect != c.in.TopSuspect ||
					got.TopScore != c.in.TopScore ||
					got.Confidence != c.in.Confidence {
					t.Fatalf("summary mismatch: got %+v, in %+v", *got, c.in)
				}
			}
		})
	}
}
