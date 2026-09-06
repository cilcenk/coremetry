package chstore

import (
	"strings"
	"testing"
)

// v0.10.499 — the raw list path (search / attr filters / env / cluster
// present) sorted by service or operation fell through every bounded
// stage to the single-pass `GROUP BY trace_id` over the whole window with
// string states, and with rootOnly the whole-window `GLOBAL IN
// (trace_summary_5m …)` root subquery (the v0.10.238 / v0.10.494 241
// shape) — found in the v0.10.494 review. Contract: string-key sorts take
// the light path with a recency stage 1 (newest traceRecencySliceN, no
// OFFSET), the root check over all candidates, and stage 2 doing the
// sort + LIMIT/OFFSET; RankedWithin reports the candidate count.
func TestRawRankedSortTakesLightPath_v0_10_499(t *testing.T) {
	for _, sort := range []string{"service", "operation"} {
		if !rawRankedSort(sort) {
			t.Fatalf("%s must be a ranked raw sort", sort)
		}
		for _, root := range []bool{false, true} {
			if !rawListLightEligible(TraceFilter{Sort: sort}, root) {
				t.Fatalf("sort=%s root=%v must be light-eligible", sort, root)
			}
		}
	}
	for _, sort := range []string{"", "time", "duration", "spans", "status"} {
		if rawRankedSort(sort) {
			t.Fatalf("%q must not be a ranked raw sort", sort)
		}
	}
}

func TestRawRankedSortBranchWired_v0_10_499(t *testing.T) {
	body := funcBody(t, "repo.go", "func (s *Store) GetTraces(")
	for _, want := range []string{
		"rankedRaw := rawRankedSort(f.Sort)",
		"s1Limit, s1Offset = traceRecencySliceN, 0",
		"stage1Seen := len(cands)",
		"cands = applyRootFilterPageCands(cands, hasRoot, 0, len(cands))",
		"args = append(args, pageLimit, f.Offset)",
		"hasMore = len(out) > f.Limit",
		"*f.RankedWithin = stage1Seen",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("GetTraces light path missing %q", want)
		}
	}
}
