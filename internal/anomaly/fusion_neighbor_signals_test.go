package anomaly

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.1056 (Faz 1.1) — buildEvidenceBundle komşu anomalilerini artık
// atmıyor. Üyelik kuralı NeighborProblem'inkiyle aynı (1-hop komşu VEYA
// skorlu downstream); alakasız servislerin olayları yine düşer;
// aynı-servis olaylar Signals'ta kalır; confidence komşu kanalını TEK
// sayar (/5 ölçeği sabit).
func TestBuildEvidenceBundleNeighborSignals(t *testing.T) {
	p := chstore.Problem{ID: "p1", Service: "checkout", StartedAt: 1_000_000}
	in := evidenceInputs{
		adjacency: []chstore.ServiceEdgePair{
			{Caller: "checkout", Callee: "payment-db"},
			{Caller: "mobile-bff", Callee: "checkout"},
		},
		events: []chstore.AnomalyEvent{
			{ID: "e1", Service: "checkout", Status: "active", Kind: "log_pattern", Pattern: "own"},
			{ID: "e2", Service: "payment-db", Status: "active", Kind: "log_pattern", Pattern: "connection reset", CurrentRatio: 6.2},
			{ID: "e3", Service: "mobile-bff", Status: "active", Kind: "trace_op", Pattern: "GET /x"},
			{ID: "e4", Service: "unrelated-svc", Status: "active", Kind: "trace_op", Pattern: "noise"},
			{ID: "e5", Service: "payment-db", Status: "resolved", Kind: "trace_op", Pattern: "stale"},
		},
	}
	b := buildEvidenceBundle(p, in)

	if len(b.Signals) != 1 || b.Signals[0].ID != "e1" {
		t.Fatalf("aynı-servis sinyali Signals'ta kalmalıydı: %+v", b.Signals)
	}
	if len(b.NeighborSignals) != 2 {
		t.Fatalf("komşu sinyalleri=%d, want 2 (alakasız + resolved düşer): %+v",
			len(b.NeighborSignals), b.NeighborSignals)
	}
	byService := map[string]string{}
	for _, ns := range b.NeighborSignals {
		byService[ns.Event.Service] = ns.Direction
	}
	if byService["payment-db"] != "calls" || byService["mobile-bff"] != "called_by" {
		t.Fatalf("yön etiketleri yanlış: %v", byService)
	}

	// Confidence: trigger(1) + kendi-servis sinyali(1) + komşu kanalı(1)
	// = 3. Komşu SİNYALLERİ ayrı kanal açmaz: komşu problemi de eklesek
	// sayı değişmemeli (tek kanal — /5 ölçeği sabit).
	if b.Confidence != 3 {
		t.Fatalf("confidence=%d, want 3", b.Confidence)
	}
	in.openProblems = []chstore.Problem{{ID: "p2", Service: "payment-db", Status: "open"}}
	b2 := buildEvidenceBundle(p, in)
	if b2.Confidence != 3 {
		t.Fatalf("komşu kanalı iki kez sayıldı: confidence=%d, want 3", b2.Confidence)
	}
}
