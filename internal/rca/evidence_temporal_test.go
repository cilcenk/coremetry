package rca

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.10.701 — kanıt kataloğunda baş şüpheli ve aday satırları zamansal
// gerekçeyi taşır; yoksa satır eskisi gibi.
func TestEvidenceCatalogCarriesTemporalReason(t *testing.T) {
	h := &chstore.RootCauseHypothesis{
		AnchorKind: "problem", AnchorID: "p1", Service: "checkout",
		TopSuspect: "payment-db", TopScore: 0.6, Confidence: 0.5,
		Candidates: []chstore.ScoredCause{
			{Service: "payment-db", Score: 0.6, Hops: 1, TemporalReason: "co-moves with trigger (ρ=0.82, leads by 1×5m)"},
			{Service: "cache", Score: 0.2, Hops: 2, Reason: "downstream", TemporalReason: "no co-movement with trigger (ρ=-0.05)"},
			{Service: "auth", Score: 0.1, Hops: 1, Reason: "downstream"},
		},
	}
	out := RenderEvidenceCatalog(BuildEvidenceCatalog(h))
	for _, want := range []string{
		"baş şüphelisi: payment-db (skor 0.6, güven 0.50) · co-moves with trigger (ρ=0.82, leads by 1×5m)",
		"aday: cache (skor 0.2, 2 hop) — downstream · no co-movement with trigger (ρ=-0.05)",
		"aday: auth (skor 0.1, 1 hop) — downstream",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("eksik: %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "aday: auth (skor 0.1, 1 hop) — downstream ·") {
		t.Error("gerekçesiz adaya ayraç eklenmemeli")
	}
}
