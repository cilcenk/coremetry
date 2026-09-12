package anomaly

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.701 (parite #2, dilim 2) — zamansal gerekçe prompt'ta aday satırına
// iner (baş şüpheli + diğer adaylar); yoksa satır bayt-özdeş (eski pin).
func TestHypothesisPromptBlockTR_TemporalReason(t *testing.T) {
	h := &chstore.RootCauseHypothesis{
		AnchorKind: "problem", AnchorID: "p1", Service: "checkout",
		TopSuspect: "payment-db", TopScore: 0.6, Confidence: 0.5,
		Candidates: []chstore.ScoredCause{
			{Service: "payment-db", Score: 0.6, Hops: 1, Reason: "downstream dependency — 80% of error share, 1-hop",
				TemporalReason: "co-moves with trigger (ρ=0.82, leads by 1×5m)"},
			{Service: "cache", Score: 0.2, Hops: 2, Reason: "downstream dependency — 30% of error share, 2-hop",
				TemporalReason: "no co-movement with trigger (ρ=-0.05)"},
			{Service: "auth", Score: 0.1, Hops: 1},
		},
	}
	got := HypothesisPromptBlockTR(h)
	for _, want := range []string{
		"Baş şüpheli: payment-db (skor 0.60, güven 0.50) — downstream dependency — 80% of error share, 1-hop · co-moves with trigger (ρ=0.82, leads by 1×5m)",
		"cache (skor 0.20, 2-hop) — downstream dependency — 30% of error share, 2-hop · no co-movement with trigger (ρ=-0.05)",
		"auth (skor 0.10, 1-hop)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("eksik: %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "auth (skor 0.10, 1-hop) ·") {
		t.Error("gerekçesiz adaya ayraç eklenmemeli")
	}
}
