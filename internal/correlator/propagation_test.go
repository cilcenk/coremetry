package correlator

import (
	"math"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// edge helpers for the propagation tests.
func e(caller, callee string, calls, errors uint64) chstore.ServiceEdgePair {
	return chstore.ServiceEdgePair{Caller: caller, Callee: callee, Calls: calls, Errors: errors}
}

func causeMap(cs []ScoredCause) map[string]ScoredCause {
	m := map[string]ScoredCause{}
	for _, c := range cs {
		m[c.Service] = c
	}
	return m
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// 1-hop ranking is the error SHARE across the trigger's downstream deps,
// not raw error count or call volume.
func TestRank1HopErrorShare(t *testing.T) {
	cs := RankRootCausesFromEdges([]chstore.ServiceEdgePair{
		e("S", "A", 1000, 8), // high calls, but the share is by ERRORS
		e("S", "B", 10, 2),
	}, "S")
	if len(cs) != 2 {
		t.Fatalf("want 2 candidates, got %d (%v)", len(cs), cs)
	}
	if cs[0].Service != "A" || !approx(cs[0].Score, 0.8) {
		t.Errorf("A should rank first with share 0.8, got %+v", cs[0])
	}
	if cs[1].Service != "B" || !approx(cs[1].Score, 0.2) {
		t.Errorf("B should be 0.2, got %+v", cs[1])
	}
	if cs[0].Hops != 1 {
		t.Errorf("direct dep should be 1 hop, got %d", cs[0].Hops)
	}
}

// A 2-hop dependency is reached with the per-hop decay applied:
// score(E) = decay · share(S→D) · share(D→E).
func TestRank2HopDecay(t *testing.T) {
	cs := causeMap(RankRootCausesFromEdges([]chstore.ServiceEdgePair{
		e("S", "D", 100, 10), // S's only downstream → share 1.0
		e("D", "E", 50, 4),   // D's only downstream → share 1.0
	}, "S"))
	d, ok := cs["D"]
	if !ok || !approx(d.Score, 1.0) || d.Hops != 1 {
		t.Errorf("D should be 1-hop score 1.0, got %+v", d)
	}
	e2, ok := cs["E"]
	if !ok || !approx(e2.Score, 0.5) || e2.Hops != 2 {
		t.Errorf("E should be 2-hop score 0.5 (decay 0.5), got %+v", e2)
	}
	if want := []string{"S", "D", "E"}; len(e2.Path) != 3 ||
		e2.Path[0] != want[0] || e2.Path[1] != want[1] || e2.Path[2] != want[2] {
		t.Errorf("E path should be S→D→E, got %v", e2.Path)
	}
}

// The walk stops at propagationMaxHops: every node up to the cap is scored,
// the first one beyond it is not. Cap-agnostic so it stays correct if the
// hop budget is retuned.
func TestRankMaxHopsCap(t *testing.T) {
	chain := []string{"S", "n1", "n2", "n3", "n4", "n5"} // a chain longer than any sane cap
	edges := make([]chstore.ServiceEdgePair, 0, len(chain)-1)
	for i := 0; i+1 < len(chain); i++ {
		edges = append(edges, e(chain[i], chain[i+1], 10, 5))
	}
	cs := causeMap(RankRootCausesFromEdges(edges, "S"))
	for h := 1; h < len(chain); h++ {
		node := chain[h]
		_, scored := cs[node]
		want := h <= propagationMaxHops
		if scored != want {
			t.Errorf("%s at hop %d: scored=%v, want %v (cap=%d)", node, h, scored, want, propagationMaxHops)
		}
	}
}

// No errors anywhere → no propagation candidates (error-based by design;
// call volume alone does not implicate a dependency).
func TestRankZeroErrorsEmpty(t *testing.T) {
	cs := RankRootCausesFromEdges([]chstore.ServiceEdgePair{
		e("S", "A", 1000, 0),
		e("S", "B", 500, 0),
	}, "S")
	if len(cs) != 0 {
		t.Errorf("zero errors should yield no candidates, got %v", cs)
	}
}

// A cycle S→A→S must not loop and must not score the trigger as its own
// cause.
func TestRankCycleSafe(t *testing.T) {
	cs := causeMap(RankRootCausesFromEdges([]chstore.ServiceEdgePair{
		e("S", "A", 10, 5),
		e("A", "S", 10, 5), // back-edge to the trigger
	}, "S"))
	if _, ok := cs["S"]; ok {
		t.Error("trigger must never be its own root-cause candidate")
	}
	if a, ok := cs["A"]; !ok || a.Hops != 1 {
		t.Errorf("A should be the single 1-hop candidate, got %+v", a)
	}
	if len(cs) != 1 {
		t.Errorf("cycle should yield exactly {A}, got %v", cs)
	}
}

// A self-edge (S→S) is excluded from both the candidate set AND the
// share denominator, so it can't dilute real downstream deps.
func TestRankSelfEdgeExcluded(t *testing.T) {
	cs := causeMap(RankRootCausesFromEdges([]chstore.ServiceEdgePair{
		e("S", "S", 100, 50), // self-edge — ignored entirely
		e("S", "B", 100, 50),
	}, "S"))
	if _, ok := cs["S"]; ok {
		t.Error("self-edge must not make S its own candidate")
	}
	// B is the only real downstream → its share is 1.0, NOT 0.5 (the
	// self-edge's 50 errors must be out of the denominator).
	if b, ok := cs["B"]; !ok || !approx(b.Score, 1.0) {
		t.Errorf("B share should be 1.0 with the self-edge excluded from the denominator, got %+v", b)
	}
}

// When a node is reachable by two paths, the strongest (max) path wins —
// and the score stays in [0,1].
func TestRankReconvergenceTakesMax(t *testing.T) {
	// S→A (share .5) →E (share 1) = .5*.5 = .25 ; S→B (share .5) →E (share 1) = .25
	// both 2-hop, equal → E score .25, but if a DIRECT S→E existed it'd win.
	cs := causeMap(RankRootCausesFromEdges([]chstore.ServiceEdgePair{
		e("S", "A", 10, 5),
		e("S", "B", 10, 5),
		e("A", "E", 10, 9),
		e("B", "E", 10, 9),
	}, "S"))
	ec, ok := cs["E"]
	if !ok {
		t.Fatal("E should be reachable via A and B")
	}
	if ec.Score > 1.0 || ec.Score < 0 {
		t.Errorf("score must stay in [0,1], got %v (sum-not-max would exceed it)", ec.Score)
	}
	if !approx(ec.Score, 0.25) || ec.Hops != 2 {
		t.Errorf("E should be max single-path 0.25 at 2 hops, got %+v", ec)
	}
}

// RootCauseRank goes through the live graph + lock; sanity-check it
// agrees with the pure scorer.
func TestRootCauseRankViaLiveGraph(t *testing.T) {
	c := New(nil)
	c.applyEdges([]chstore.ServiceEdgePair{
		e("S", "A", 100, 9),
		e("S", "B", 100, 1),
	})
	cs := c.RootCauseRank("S")
	if len(cs) != 2 || cs[0].Service != "A" || !approx(cs[0].Score, 0.9) {
		t.Errorf("live RootCauseRank disagrees with scorer: %+v", cs)
	}
}
