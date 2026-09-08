package api

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.562 — insight şeridi saf birleştirici: hipotez yok → hücre boş + not;
// en yakın rollout |Δt| ile; ilk anomali yalnız açılış penceresinde en erken;
// benzer geçmiş anchor'ı ve çözülmemişleri saymaz, son çözülenin süresi/atananı.
func TestBuildProblemInsight(t *testing.T) {
	const onset = int64(1_700_000_000_000_000_000)
	p := chstore.Problem{ID: "p1", Service: "checkout", RuleID: "r1", Status: "open", StartedAt: onset}
	empty := buildProblemInsight(p, nil, nil, nil)
	if empty.HypothesisComputed || empty.Rollout != nil || empty.FirstAnomaly != nil || empty.Similar != nil || empty.Note == "" {
		t.Fatalf("boş: %+v", empty)
	}
	h := &chstore.RootCauseHypothesis{TopSuspect: "ledger", Confidence: 0.82, Deep: &chstore.DeepEvidence{Rollouts: []chstore.RolloutEvidence{
		{Workload: "far", StartedAtNs: onset - 3600e9, ImageTag: "v1"},
		{Workload: "near", StartedAtNs: onset - 240e9, Revision: "rev-9"},
		{Workload: "after", StartedAtNs: onset + 600e9, ImageTag: "v3"},
	}}}
	events := []chstore.AnomalyEvent{
		{Kind: "log_pattern", Service: "checkout", StartedAt: onset - 120e9},
		{Kind: "trace_op", Service: "checkout", StartedAt: onset - 600e9},
		{Kind: "trace_op", Service: "checkout", StartedAt: onset - 3600e9}, // pencere dışı
		{Kind: "log_pattern", Service: "checkout", StartedAt: onset + 3600e9},
	}
	resolved := onset + 2460e9
	similar := []chstore.Problem{
		{ID: "p1", Status: "resolved"},
		{ID: "old1", Status: "resolved", StartedAt: onset, ResolvedAt: &resolved, Assignee: "ops-core"},
		{ID: "old2", Status: "open"},
		{ID: "old3", Status: "resolved", StartedAt: onset},
	}
	in := buildProblemInsight(p, h, events, similar)
	if !in.HypothesisComputed || in.TopSuspect != "ledger" || in.Confidence != 0.82 {
		t.Fatalf("hipotez: %+v", in)
	}
	if in.Rollout == nil || in.Rollout.Workload != "near" || in.Rollout.Version != "rev-9" || in.Rollout.DeltaS != -240 {
		t.Fatalf("en yakın rollout: %+v", in.Rollout)
	}
	if in.FirstAnomaly == nil || in.FirstAnomaly.At != onset-600e9 || in.FirstAnomaly.Kind != "trace_op" {
		t.Fatalf("ilk anomali: %+v", in.FirstAnomaly)
	}
	if in.Similar == nil || in.Similar.Count != 2 || in.Similar.LastID != "old1" || in.Similar.LastDurationS != 2460 || in.Similar.LastAssignee != "ops-core" {
		t.Fatalf("benzer: %+v", in.Similar)
	}
	if in.Note != "" {
		t.Fatalf("her hücre doluyken not boş: %q", in.Note)
	}
	if problemInsightKey("a", "open") == problemInsightKey("a", "resolved") || problemInsightKey("a", "open") == problemInsightKey("b", "open") {
		t.Fatal("anahtar id + durum")
	}
}
