package anomaly

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// rootcause_worker_test.go — v0.8.571. Bu dosyanın İLK testi: audit'in
// "sessiz boşluk" bulgusu — SynthesisInput'a alan eklenip worker doldurmayı
// unutsa hiçbir test kırılmıyordu. Signals TAM OLARAK böyle kayboldu:
// bundle günden beri topluyordu, iki synthInput* fonksiyonu da taşımıyordu.
// Bu testler eşlemeyi ve anomali yolundaki kendini-dışlamayı pin'ler.

func TestSynthInputForProblemThreadsSignals(t *testing.T) {
	p := chstore.Problem{ID: "p1", Service: "payments", StartedAt: 1000}
	b := EvidenceBundle{Signals: []chstore.AnomalyEvent{
		{Kind: "log_pattern", Pattern: "OOM", CurrentRatio: 3, PeakRatio: 7},
		{Kind: "trace_op", Pattern: "GET /pay", CurrentRatio: 4, PeakRatio: 2},
	}}
	in := synthInputForProblem(p, b)
	if len(in.Signals) != 2 {
		t.Fatalf("bundle'daki 2 signal taşınmalı, %d geldi", len(in.Signals))
	}
	// Ratio = max(current, peak): soğumuş bir spike hâlâ kanıttır.
	if in.Signals[0].Ratio != 7 || in.Signals[1].Ratio != 4 {
		t.Fatalf("max(current, peak) eşlemesi bozuk: %+v", in.Signals)
	}
	if in.Signals[0].Kind != "log_pattern" || in.Signals[0].Pattern != "OOM" {
		t.Fatalf("kind/pattern birebir taşınmalı: %+v", in.Signals[0])
	}
}

func TestSynthInputForAnomalySignalFiltering(t *testing.T) {
	ev := chstore.AnomalyEvent{ID: "self", Service: "payments", StartedAt: 1000}
	inputs := evidenceInputs{events: []chstore.AnomalyEvent{
		// Kendisi — DIŞLANMALI (problem yolundaki op.ID == p.ID dışlamasının
		// eşleniği: bir anomali kendi hipotezini doğrulayamaz).
		{ID: "self", Service: "payments", Status: "active", Kind: "trace_op", Pattern: "X", CurrentRatio: 9},
		// Başka servis — dışlanmalı.
		{ID: "e2", Service: "orders", Status: "active", Kind: "log_pattern", Pattern: "Y", CurrentRatio: 9},
		// Cleared — dışlanmalı (10dk'dan bayat).
		{ID: "e3", Service: "payments", Status: "cleared", Kind: "log_pattern", Pattern: "Z", CurrentRatio: 9},
		// Geçerli tek aday.
		{ID: "e4", Service: "payments", Status: "active", Kind: "log_pattern", Pattern: "timeout", CurrentRatio: 5, PeakRatio: 6},
	}}
	out := synthInputForAnomaly(ev, inputs)
	if len(out.Signals) != 1 {
		t.Fatalf("yalnız e4 geçmeli (self/başka-servis/cleared düşer), %d geldi: %+v", len(out.Signals), out.Signals)
	}
	if out.Signals[0].Pattern != "timeout" || out.Signals[0].Ratio != 6 {
		t.Fatalf("yanlış signal/ratio taşındı: %+v", out.Signals[0])
	}
}
