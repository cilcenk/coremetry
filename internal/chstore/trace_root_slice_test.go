package chstore

import (
	"os"
	"strings"
	"testing"
)

// v0.10.494 — regression: Operator-reported (prod, external Distributed
// CH): /traces with NO service filter + rootOnly + a 12 h window returned
// HTTP 500 — CH code 241 "memory limit exceeded 3.73 GiB" while executing
// SourceFromNativeStream (partial GROUP BY trace_id states merging on the
// initiator). 6 h worked; 24 h / 7 d must work too. Root cause: the
// no-service MV path put the rootOnly HAVING on the LIGHT stage 1
// (traceStage1LightSQL), a whole-window `GROUP BY trace_id HAVING
// argMaxIfMerge(root_service_state) != ”` — memory linear in the window
// (local 2-shard, 12 h: 51 MiB / 11.6 s). Stage 2 already applies that
// HAVING over the bounded holders list, so rootOnly now rides the
// aggregation-free recency slice (v0.9.277 shape) like the plain and
// hasError page loads.
//
// v0.10.497 widened the contract: minMs/maxMs ride the slice too and the
// light stage is deleted. Pinned here:
//   - every no-service filter combination → the recency slice
//   - hasError sets the row-level errors prefilter; rootOnly/min/max never do
//   - rootOnly / minMs / maxMs mark postAgg (stage 2 HAVING + RankedWithin)
func TestNoServiceSlicePlan_v0_10_494(t *testing.T) {
	cases := []struct {
		name        string
		f           TraceFilter
		errors, agg bool
	}{
		{"no filter", TraceFilter{}, false, false},
		{"hasError only", TraceFilter{HasError: true}, true, false},
		{"rootOnly only (the reported shape)", TraceFilter{RootOnly: true}, false, true},
		{"rootOnly + hasError", TraceFilter{RootOnly: true, HasError: true}, true, true},
		{"minMs", TraceFilter{MinMs: 100}, false, true},
		{"maxMs", TraceFilter{MaxMs: 100}, false, true},
		{"rootOnly + minMs", TraceFilter{RootOnly: true, MinMs: 100}, false, true},
		{"hasError + maxMs", TraceFilter{HasError: true, MaxMs: 100}, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := noServiceSlicePlan(c.f)
			if p.errorsPrefilter != c.errors || p.postAgg != c.agg {
				t.Fatalf("plan=%+v want errors=%v postAgg=%v", p, c.errors, c.agg)
			}
		})
	}
}

// Source pin: the plan must be what getTracesFromMV consults, the slice
// call must consume its prefilter, post-aggregate filters must widen the
// time-sort budget to the recency slice (pages stay honest via
// RankedWithin), and the whole-window light stage must stay deleted — a
// pure helper nobody calls pins nothing (feedback-tested-but-unreachable).
func TestNoServiceSlicePlan_reachable_v0_10_494(t *testing.T) {
	body := funcBody(t, "repo.go", "func (s *Store) getTracesFromMV(")
	for _, want := range []string{
		"plan := noServiceSlicePlan(f)",
		"errorsOnly := plan.errorsPrefilter",
		"if budgetOK {",
		"if plan.postAgg && !ranked {",
		"if (ranked || plan.postAgg) && f.RankedWithin != nil {",
		"s.traceRecencySlice(ctx, s1f, budget, errorsOnly)",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("getTracesFromMV missing %q", want)
		}
	}
	// v0.10.497 — no no-service stage 1 may GROUP BY trace_id over the
	// window again (the v0.10.494 241 shape).
	b, err := os.ReadFile("repo.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "traceStage1LightSQL(") {
		t.Fatal("traceStage1LightSQL must not come back — it is the v0.10.494 241 shape")
	}
}
