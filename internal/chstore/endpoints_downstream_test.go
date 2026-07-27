package chstore

import (
	"math"
	"strings"
	"testing"
)

// v0.9.311 (brief N4) — "Where the time goes".
//
// No MV carries route→downstream, so this is a sampled read over raw
// spans. That makes two things load-bearing and neither is visible in
// the output: the sampling ORDER and what counts toward the share.
//
// Sampling order: neighbors.go samples by count() DESC, which leans
// toward structurally fat traces. For a LATENCY question that is the
// wrong lean — the panel exists to explain a slow p99, so the sample
// must represent the slow tail. Pinned in the source, since a future
// copy-paste from neighbors.go would silently answer a latency
// question with a structural sample.
func TestDownstreamSamplesBySlowest(t *testing.T) {
	src := mustReadSource(t, "endpoints_downstream.go")
	if !strings.Contains(src, "ORDER BY duration DESC") {
		t.Fatal("the trace sample must be ordered by DURATION — sampling by span count answers a structural question with a latency chart")
	}
	if strings.Contains(src, "ORDER BY count() DESC") {
		t.Fatal("neighbors.go's count-ordered sampling leaked in; it biases toward fat traces, not slow ones")
	}
}

// Both passes over raw spans MUST be bounded. v0.9.231's lesson: the
// second pass without a time predicate runs bloom-filter analysis over
// every daily partition in retention.
func TestDownstreamPassesAreBounded(t *testing.T) {
	src := mustReadSource(t, "endpoints_downstream.go")
	for _, guard := range []string{
		"max_execution_time = 10",
		"heavyScanSpill",
		"shardSkipSetting()",
		"LIMIT ?",
		"AND time >= ? AND time <= ?",
	} {
		if !strings.Contains(src, guard) {
			t.Errorf("missing cost guard %q — this file runs TWO raw-spans passes", guard)
		}
	}
	// Two passes, so two execution caps and two spill settings.
	if n := strings.Count(src, "max_execution_time = 10"); n < 2 {
		t.Errorf("only %d execution cap(s) — both passes need one", n)
	}
	if n := strings.Count(src, "heavyScanSpill"); n < 2 {
		t.Errorf("only %d spill guard(s) — both passes need one", n)
	}
}

// The share list must sum to something the entry duration explains.
// Listing a grandchild beside its own parent would count the same
// milliseconds twice — the reason backends live in a separate list.
func TestFinishEdgesOrdersByShareAndComputesPercentile(t *testing.T) {
	edges := finishEdges(map[string]*edgeAcc{
		"small": {kind: "service", calls: 2, sumMs: 10, durs: []float64{4, 6}},
		"big":   {kind: "db", calls: 3, sumMs: 300, durs: []float64{50, 100, 150}},
		"mid":   {kind: "service", calls: 1, sumMs: 100, durs: []float64{100}},
	})
	if len(edges) != 3 {
		t.Fatalf("got %d edges, want 3", len(edges))
	}
	// "where did the time go" reads top-down.
	if edges[0].Name != "big" || edges[1].Name != "mid" || edges[2].Name != "small" {
		t.Fatalf("edges must be ordered by share desc, got %v %v %v",
			edges[0].Name, edges[1].Name, edges[2].Name)
	}
	if edges[0].AvgMs != 100 {
		t.Fatalf("avg = %v, want 100", edges[0].AvgMs)
	}
	if edges[0].P99Ms != 150 {
		t.Fatalf("p99 over the sample = %v, want the largest of 3 values", edges[0].P99Ms)
	}
	if edges[0].Kind != "db" {
		t.Fatalf("kind must survive so the UI can label it, got %q", edges[0].Kind)
	}
}

// Degenerate shapes that must not panic or invent numbers.
func TestPctOfEdgeCases(t *testing.T) {
	if got := pctOf(nil, 0.99); got != 0 {
		t.Fatalf("empty sample must answer 0, got %v", got)
	}
	if got := pctOf([]float64{7}, 0.99); got != 7 {
		t.Fatalf("single sample is its own p99, got %v", got)
	}
	// Nearest-rank: p50 of 1..4 is the 2nd value.
	if got := pctOf([]float64{1, 2, 3, 4}, 0.5); got != 2 {
		t.Fatalf("p50 = %v, want 2 (nearest-rank)", got)
	}
	if got := pctOf([]float64{1, 2, 3, 4}, 1.0); got != 4 {
		t.Fatalf("p100 = %v, want the max", got)
	}
	// No index can escape the slice.
	for _, p := range []float64{0, 0.001, 0.5, 0.999, 1} {
		if v := pctOf([]float64{1, 2, 3}, p); math.IsNaN(v) || v < 1 || v > 3 {
			t.Fatalf("p%v produced %v, outside the sample", p, v)
		}
	}
}
