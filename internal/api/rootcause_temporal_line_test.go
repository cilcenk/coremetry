package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.701 — düzyazı (Explain) prompt'unda aday satırı zamansal gerekçeyi taşır.
func TestRootCausePromptCarriesTemporalReason(t *testing.T) {
	got := buildRootCausePrompt(&chstore.RootCauseHypothesis{
		AnchorKind: "problem", Service: "checkout",
		TopSuspect: "oracle-core", TopScore: 0.62, Confidence: 0.41,
		Candidates: []chstore.ScoredCause{
			{Service: "oracle-core", Score: 0.62, Hops: 1, Reason: "downstream error-share 0.62", TemporalReason: "co-moves with trigger (ρ=0.90, leads by 2×5m)"},
			{Service: "kafka", Score: 0.20, Hops: 2, Reason: "co-firing problem"},
		},
	})
	if !strings.Contains(got, "1. oracle-core (score 0.6, 1 hop(s)) — downstream error-share 0.62 · co-moves with trigger (ρ=0.90, leads by 2×5m)") {
		t.Errorf("zamansal gerekçe satırda yok:\n%s", got)
	}
	if !strings.Contains(got, "2. kafka (score 0.2, 2 hop(s)) — co-firing problem\n") {
		t.Errorf("gerekçesiz aday değişmemeli:\n%s", got)
	}
}
