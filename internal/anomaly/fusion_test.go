package anomaly

import (
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.8.45 (anomaly intelligence Phase 7) — cross-signal fusion. When an
// error_rate anomaly, an OOMKilled log pattern, a checkout-op trace error
// spike, AND a deploy all land on the same service in the same window, that's
// ONE corroborated incident, not four. buildEvidenceBundle must group them +
// score confidence; renderEvidence feeds that to the explainer. This pins the
// grouping/scoring purely (no store, no Copilot — matching the existing
// pure-function test style).
func TestBuildEvidenceBundle_FusesCoFiringSignals(t *testing.T) {
	const T = int64(1_700_000_000_000_000_000) // onset, unix ns
	trigger := chstore.Problem{
		ID: "p1", Service: "checkout", Metric: "error_rate",
		RuleName: "Anomaly · Error rate", StartedAt: T,
	}
	in := evidenceInputs{
		openProblems: []chstore.Problem{
			trigger,
			{ID: "p2", Service: "checkout", Metric: "p99_ms", RuleName: "Anomaly · P99 latency", StartedAt: T}, // co-firing
			{ID: "p3", Service: "payments", Metric: "error_rate", RuleName: "Anomaly · Error rate", StartedAt: T}, // neighbour
			{ID: "p4", Service: "unrelated", Metric: "error_rate", RuleName: "x", StartedAt: T},                   // neither
		},
		events: []chstore.AnomalyEvent{
			{ID: "e1", Kind: "log_pattern", Pattern: "OOMKilled", Service: "checkout", Status: "active", Sample: "Container OOMKilled"},
			{ID: "e2", Kind: "trace_op", Pattern: "POST /checkout", Service: "checkout", Status: "active"},
			{ID: "e3", Kind: "log_pattern", Pattern: "noise", Service: "other", Status: "active"},      // wrong service
			{ID: "e4", Kind: "log_pattern", Pattern: "stale", Service: "checkout", Status: "cleared"},  // not active
		},
		deploys: []chstore.RecentDeployEntry{
			{Service: "checkout", Version: "v2.0.0", FirstSeenNs: T - int64(5*time.Minute)}, // 5m before → causal
			{Service: "checkout", Version: "v1.9.0", FirstSeenNs: T - int64(2*time.Hour)},   // too old
			{Service: "payments", Version: "v3", FirstSeenNs: T - int64(1*time.Minute)},     // wrong service
		},
		adjacency: []chstore.ServiceEdgePair{
			{Caller: "checkout", Callee: "payments"}, // checkout → payments
		},
	}

	b := buildEvidenceBundle(trigger, in)

	if len(b.CoFiring) != 1 || b.CoFiring[0].ID != "p2" {
		t.Errorf("CoFiring = %+v, want [p2]", b.CoFiring)
	}
	if len(b.Signals) != 2 {
		t.Errorf("Signals = %d, want 2 (active checkout log + trace)", len(b.Signals))
	}
	if b.Deploy == nil || b.Deploy.Version != "v2.0.0" {
		t.Errorf("Deploy = %+v, want checkout v2.0.0 (most recent within lookback)", b.Deploy)
	}
	if len(b.Neighbors) != 1 || b.Neighbors[0].Problem.ID != "p3" || b.Neighbors[0].Direction != "calls" {
		t.Errorf("Neighbors = %+v, want [p3 calls]", b.Neighbors)
	}
	if b.Confidence != 5 { // trigger + co-firing + signals + deploy + neighbours
		t.Errorf("Confidence = %d, want 5 (all evidence types present)", b.Confidence)
	}
}

func TestBuildEvidenceBundle_LoneProblemHasNoEvidence(t *testing.T) {
	trigger := chstore.Problem{ID: "p1", Service: "checkout", StartedAt: 1_000_000}
	b := buildEvidenceBundle(trigger, evidenceInputs{openProblems: []chstore.Problem{trigger}})
	if b.Confidence != 1 || len(b.CoFiring) != 0 || len(b.Signals) != 0 || b.Deploy != nil || len(b.Neighbors) != 0 {
		t.Errorf("lone problem bundle = %+v, want confidence 1 + empty evidence", b)
	}
}

// v0.8.68 (correlator Faz 6) — neighbour evidence is ordered root-cause-first:
// the downstream dep carrying the larger share of the trigger's errors ranks
// ahead, so the Copilot leads with the likely source.
func TestBuildEvidenceBundle_RanksDownstreamByPropagation(t *testing.T) {
	const T = int64(1_700_000_000_000_000_000)
	trigger := chstore.Problem{ID: "p1", Service: "checkout", Metric: "error_rate", StartedAt: T}
	in := evidenceInputs{
		openProblems: []chstore.Problem{
			trigger,
			{ID: "pp", Service: "payments", Metric: "error_rate", StartedAt: T},
			{ID: "pi", Service: "inventory", Metric: "error_rate", StartedAt: T},
		},
		adjacency: []chstore.ServiceEdgePair{
			{Caller: "checkout", Callee: "payments"},
			{Caller: "checkout", Callee: "inventory"},
		},
		weightedAdjacency: []chstore.ServiceEdgePair{
			{Caller: "checkout", Callee: "payments", Calls: 100, Errors: 9},
			{Caller: "checkout", Callee: "inventory", Calls: 100, Errors: 1},
		},
	}
	b := buildEvidenceBundle(trigger, in)
	if len(b.Neighbors) != 2 {
		t.Fatalf("want 2 neighbours, got %+v", b.Neighbors)
	}
	if b.Neighbors[0].Problem.Service != "payments" || b.Neighbors[0].Hops != 1 || b.Neighbors[0].Score < 0.89 {
		t.Errorf("payments (90%% of errors) should rank first with score ~0.9, got %+v", b.Neighbors[0])
	}
	if b.Neighbors[1].Problem.Service != "inventory" {
		t.Errorf("inventory should rank second, got %+v", b.Neighbors)
	}
}

// A 2-hop downstream service with its own open problem surfaces as a suspect
// (decayed), even though the 1-hop direction map never saw it.
func TestBuildEvidenceBundle_Surfaces2HopDownstreamSuspect(t *testing.T) {
	const T = int64(1_700_000_000_000_000_000)
	trigger := chstore.Problem{ID: "p1", Service: "checkout", Metric: "error_rate", StartedAt: T}
	in := evidenceInputs{
		openProblems: []chstore.Problem{
			trigger,
			{ID: "pl", Service: "ledger", Metric: "error_rate", StartedAt: T}, // 2-hop downstream
		},
		// 1-hop dir knows only checkout→payments — ledger is NOT here.
		adjacency: []chstore.ServiceEdgePair{
			{Caller: "checkout", Callee: "payments"},
		},
		weightedAdjacency: []chstore.ServiceEdgePair{
			{Caller: "checkout", Callee: "payments", Calls: 100, Errors: 10},
			{Caller: "payments", Callee: "ledger", Calls: 50, Errors: 5},
		},
	}
	b := buildEvidenceBundle(trigger, in)
	var ledger *NeighborProblem
	for i := range b.Neighbors {
		if b.Neighbors[i].Problem.Service == "ledger" {
			ledger = &b.Neighbors[i]
		}
	}
	if ledger == nil {
		t.Fatalf("2-hop downstream ledger should surface as a suspect, got %+v", b.Neighbors)
	}
	if ledger.Hops != 2 || ledger.Direction != "calls" {
		t.Errorf("ledger should be 2-hop downstream (calls), got %+v", *ledger)
	}
	if ledger.Score <= 0 || ledger.Score >= 1 {
		t.Errorf("ledger 2-hop score should be in (0,1) (decayed), got %v", ledger.Score)
	}
}

// No weighted edges (read failure / cold start) must degrade to the prior
// direction-only behaviour — score/hops zero, neighbour still listed.
func TestBuildEvidenceBundle_NoWeightedEdgesDegradesToDirection(t *testing.T) {
	const T = int64(1_700_000_000_000_000_000)
	trigger := chstore.Problem{ID: "p1", Service: "checkout", StartedAt: T}
	in := evidenceInputs{
		openProblems: []chstore.Problem{
			trigger,
			{ID: "pp", Service: "payments", StartedAt: T},
		},
		adjacency: []chstore.ServiceEdgePair{{Caller: "checkout", Callee: "payments"}},
		// weightedAdjacency intentionally empty
	}
	b := buildEvidenceBundle(trigger, in)
	if len(b.Neighbors) != 1 || b.Neighbors[0].Problem.Service != "payments" ||
		b.Neighbors[0].Direction != "calls" || b.Neighbors[0].Score != 0 || b.Neighbors[0].Hops != 0 {
		t.Errorf("no weighted edges → direction-only (score/hops 0), got %+v", b.Neighbors)
	}
}
