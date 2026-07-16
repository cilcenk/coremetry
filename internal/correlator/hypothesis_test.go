package correlator

// Table-driven coverage for the PURE root-cause fuser (rc #2 of the anomaly →
// root-cause feature, v0.8.x). Synthesize is the JUDGEMENT layer on top of the
// evidence bundle: it must rank deploy ≫ propagation ≫ co-firing, emit honest
// zero confidence on empty evidence, and be deterministic (stable, name
// tie-broken) so the persisted hypothesis is reproducible. A regression here
// silently mis-ranks the operator's root-cause ribbon, so every tier + the
// tie-break is exercised.

import (
	"reflect"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func hApprox(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}

// Deploy is the strongest signal: even alongside a maximal-share propagation
// suspect AND a co-firing problem, the deploy must rank #1.
func TestSynthesizeDeployDominates(t *testing.T) {
	in := SynthesisInput{
		Deploy: &chstore.RecentDeploy{Version: "v2.3.1", AgeSeconds: 120},
		// FreshnessFrac near 1 → deploy score near the 0.95 cap.
		FreshnessFrac: 0.93,
		Neighbours: []ScoredCause{
			{Service: "oracle", Score: 1.0, Hops: 1, Path: []string{"payments", "oracle"}},
		},
		CoFiringServices: []string{"payments"},
		// v0.8.571 — a max-strength signal (log_pattern, saturated ratio →
		// the tier ceiling 0.60) keeps "all evidence types present" TRUE
		// under maxEvidenceTypes=4, and pins that even the STRONGEST signal
		// stays under a fresh deploy AND a share-1.0 1-hop neighbour.
		Signals: []SignalEvidence{{Kind: "log_pattern", Pattern: "conn refused", Ratio: 99}},
	}
	h := Synthesize("anomaly", "abc123", "payments", 1000, in)

	if h.TopSuspect != "payments" {
		t.Fatalf("deploy should dominate: TopSuspect=%q, want payments", h.TopSuspect)
	}
	if len(h.Candidates) != 4 {
		t.Fatalf("want 4 candidates (deploy+prop+signal+cofiring), got %d", len(h.Candidates))
	}
	// Order: deploy (≈0.94) > oracle prop (0.70) > payments signal (0.60 ceiling)
	// > payments cofiring (0.20).
	if got := []string{h.Candidates[0].Service, h.Candidates[1].Service, h.Candidates[2].Service, h.Candidates[3].Service}; !reflect.DeepEqual(got, []string{"payments", "oracle", "payments", "payments"}) {
		t.Fatalf("rank order wrong: %v", got)
	}
	if h.Candidates[2].Score >= propTierBase {
		t.Fatalf("signal ceiling must stay under a share-1.0 propagation: %v", h.Candidates[2].Score)
	}
	wantTop := deployBaseScore + deployFreshnessBonusMax*0.93
	if !hApprox(h.TopScore, wantTop) {
		t.Fatalf("TopScore=%v want %v", h.TopScore, wantTop)
	}
	if h.RecentDeploy == nil || h.RecentDeploy.Version != "v2.3.1" {
		t.Fatalf("RecentDeploy should carry through, got %+v", h.RecentDeploy)
	}
	// All three evidence types present → breadth 1.0; confidence blends breadth
	// + top score. Must be high but ≤1.
	wantConf := confidenceBreadthWeight*1.0 + confidenceStrengthWeight*wantTop
	if !hApprox(h.Confidence, wantConf) {
		t.Fatalf("Confidence=%v want %v", h.Confidence, wantConf)
	}
}

// No deploy, no co-firing: the propagation suspects rank purely by their
// hop-decayed error share, best first, scaled into the propagation tier.
func TestSynthesizePropagationOnly(t *testing.T) {
	in := SynthesisInput{
		Neighbours: []ScoredCause{
			{Service: "oracle", Score: 0.62, Hops: 1, Path: []string{"payments", "oracle"}},
			{Service: "kafka", Score: 0.25, Hops: 2, Path: []string{"payments", "ledger", "kafka"}},
		},
	}
	h := Synthesize("anomaly", "id1", "payments", 1, in)

	if h.TopSuspect != "oracle" {
		t.Fatalf("TopSuspect=%q want oracle", h.TopSuspect)
	}
	if !hApprox(h.TopScore, propTierBase*0.62) {
		t.Fatalf("TopScore=%v want %v", h.TopScore, propTierBase*0.62)
	}
	if len(h.Candidates) != 2 {
		t.Fatalf("want 2 candidates, got %d", len(h.Candidates))
	}
	if h.Candidates[1].Service != "kafka" {
		t.Fatalf("second candidate=%q want kafka", h.Candidates[1].Service)
	}
	// Only one evidence type (propagation) → breadth 1/3.
	wantConf := confidenceBreadthWeight*(1.0/float64(maxEvidenceTypes)) + confidenceStrengthWeight*(propTierBase*0.62)
	if !hApprox(h.Confidence, wantConf) {
		t.Fatalf("Confidence=%v want %v", h.Confidence, wantConf)
	}
	// Zero-score / empty-name neighbours are dropped, never emitted.
	in.Neighbours = append(in.Neighbours, ScoredCause{Service: "noise", Score: 0})
	h2 := Synthesize("anomaly", "id1", "payments", 1, in)
	if len(h2.Candidates) != 2 {
		t.Fatalf("zero-score neighbour should be dropped, got %d candidates", len(h2.Candidates))
	}
}

// Only co-firing same-service problems: the flat lowest tier, deduped + name
// sorted, low confidence (corroboration without a clear cause).
func TestSynthesizeCoFiringOnly(t *testing.T) {
	in := SynthesisInput{
		// duplicate + out-of-order to assert dedup + sort.
		CoFiringServices: []string{"payments", "auth", "payments"},
	}
	h := Synthesize("problem", "p1", "payments", 5, in)

	if len(h.Candidates) != 2 {
		t.Fatalf("want 2 deduped co-firing candidates, got %d", len(h.Candidates))
	}
	if h.Candidates[0].Service != "auth" || h.Candidates[1].Service != "payments" {
		t.Fatalf("co-firing not name-sorted: %v", []string{h.Candidates[0].Service, h.Candidates[1].Service})
	}
	if !hApprox(h.TopScore, cofiringScore) {
		t.Fatalf("TopScore=%v want %v", h.TopScore, cofiringScore)
	}
	if h.RecentDeploy != nil {
		t.Fatalf("no deploy should carry through, got %+v", h.RecentDeploy)
	}
	wantConf := confidenceBreadthWeight*(1.0/float64(maxEvidenceTypes)) + confidenceStrengthWeight*cofiringScore
	if !hApprox(h.Confidence, wantConf) {
		t.Fatalf("Confidence=%v want %v", h.Confidence, wantConf)
	}
}

// Empty evidence → honest zero: no candidates, empty suspect, zero confidence.
// Never fabricate a guess.
func TestSynthesizeEmptyEvidence(t *testing.T) {
	h := Synthesize("anomaly", "empty", "payments", 9, SynthesisInput{})

	if len(h.Candidates) != 0 {
		t.Fatalf("empty evidence must yield no candidates, got %d", len(h.Candidates))
	}
	if h.Candidates == nil {
		t.Fatalf("Candidates should be non-nil empty slice (clean JSON []), got nil")
	}
	if h.TopSuspect != "" {
		t.Fatalf("TopSuspect should be empty, got %q", h.TopSuspect)
	}
	if h.TopScore != 0 {
		t.Fatalf("TopScore should be 0, got %v", h.TopScore)
	}
	if h.Confidence != 0 {
		t.Fatalf("Confidence should be 0 on empty evidence, got %v", h.Confidence)
	}
	// Anchor fields still stamped.
	if h.AnchorKind != "anomaly" || h.AnchorID != "empty" || h.Service != "payments" || h.ComputedAt != 9 {
		t.Fatalf("anchor fields not stamped: %+v", h)
	}
}

// Tie-break determinism: two propagation suspects with the IDENTICAL score must
// order by Service name ascending, reproducibly, regardless of input order.
func TestSynthesizeTieBreakDeterministic(t *testing.T) {
	mk := func(first, second string) chstore.RootCauseHypothesis {
		return Synthesize("anomaly", "tie", "svc", 1, SynthesisInput{
			Neighbours: []ScoredCause{
				{Service: first, Score: 0.4, Hops: 1},
				{Service: second, Score: 0.4, Hops: 1},
			},
		})
	}
	a := mk("zeta", "alpha")
	b := mk("alpha", "zeta")

	if a.Candidates[0].Service != "alpha" || a.Candidates[1].Service != "zeta" {
		t.Fatalf("tie not name-ordered: %v", []string{a.Candidates[0].Service, a.Candidates[1].Service})
	}
	// Order independent of input order → identical hypotheses.
	if !reflect.DeepEqual(a.Candidates, b.Candidates) {
		t.Fatalf("tie-break not input-order independent:\n a=%v\n b=%v", a.Candidates, b.Candidates)
	}
	if a.TopSuspect != "alpha" {
		t.Fatalf("TopSuspect=%q want alpha", a.TopSuspect)
	}
}
