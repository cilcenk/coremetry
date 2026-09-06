package chstore

import (
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
// hasError page loads. Contract pinned here:
//   - no filter / hasError / rootOnly / rootOnly+hasError → slice
//   - hasError sets the row-level errors prefilter, rootOnly never does
//   - minMs / maxMs (alone or combined) keep the light stage 1
func TestNoServiceSlicePlan_v0_10_494(t *testing.T) {
	cases := []struct {
		name             string
		f                TraceFilter
		ok, errors, root bool
	}{
		{"no filter", TraceFilter{}, true, false, false},
		{"hasError only", TraceFilter{HasError: true}, true, true, false},
		{"rootOnly only (the reported shape)", TraceFilter{RootOnly: true}, true, false, true},
		{"rootOnly + hasError", TraceFilter{RootOnly: true, HasError: true}, true, true, true},
		{"minMs", TraceFilter{MinMs: 100}, false, false, false},
		{"maxMs", TraceFilter{MaxMs: 100}, false, false, false},
		{"rootOnly + minMs", TraceFilter{RootOnly: true, MinMs: 100}, false, false, true},
		{"hasError + maxMs", TraceFilter{HasError: true, MaxMs: 100}, false, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := noServiceSlicePlan(c.f)
			if p.ok != c.ok || p.errorsPrefilter != c.errors || p.rootPostAgg != c.root {
				t.Fatalf("plan=%+v want ok=%v errors=%v root=%v", p, c.ok, c.errors, c.root)
			}
		})
	}
}

// Source pin: the plan must be what getTracesFromMV consults, the slice
// call must consume its prefilter, rootOnly must widen the time-sort budget
// to the recency slice (pages stay honest via RankedWithin), and the light
// stage 1 must sit BEHIND the slice branch — a pure helper nobody calls
// pins nothing (feedback-tested-but-unreachable).
func TestNoServiceSlicePlan_reachable_v0_10_494(t *testing.T) {
	body := funcBody(t, "repo.go", "func (s *Store) getTracesFromMV(")
	for _, want := range []string{
		"plan := noServiceSlicePlan(f)",
		"errorsOnly := plan.errorsPrefilter",
		"if budgetOK && plan.ok {",
		"if plan.ok && plan.rootPostAgg && !ranked {",
		"if (ranked || plan.rootPostAgg) && f.RankedWithin != nil {",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("getTracesFromMV missing %q", want)
		}
	}
	slice := strings.Index(body, "s.traceRecencySlice(ctx, s1f, budget, errorsOnly)")
	light := strings.Index(body, "traceStage1LightSQL(s1f, having)")
	if slice < 0 || light < 0 || light < slice {
		t.Fatalf("slice branch must precede the light stage 1: slice=%d light=%d", slice, light)
	}
}
