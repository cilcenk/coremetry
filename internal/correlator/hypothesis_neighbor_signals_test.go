package correlator

import (
	"math"
	"testing"
)

// v0.9.1056 (Faz 1.1) regresyon pinleri — komşu-sinyal tier'ı (2.6).
// K1: fusion komşu servislerin aktif anomalilerini belleğe alıp aynı-servis
// filtresiyle atıyordu; hipotez bu en erken imzalı ipucuya kördü. Bu tablo
// üç sözleşmeyi mühürler: bant/çarpan aritmetiği, tier emisyonu + tavan,
// ve breadth katlaması (komşu kanalı TEK — payda 4 sabit, komşu-sinyalsiz
// girdilerde çıktı bayt-bayt eski).

func TestNeighbourSignalScore(t *testing.T) {
	cases := []struct {
		name string
		sig  NeighbourSignalEvidence
		want float64
	}{
		{"taban: ratio floor'da downstream trace_op",
			NeighbourSignalEvidence{Kind: "trace_op", Ratio: 2.0, Downstream: true}, 0.28},
		{"doygun ratio bandın tepesinde",
			NeighbourSignalEvidence{Kind: "trace_op", Ratio: 10.0, Downstream: true}, 0.52},
		{"log_pattern bonusu",
			NeighbourSignalEvidence{Kind: "log_pattern", Ratio: 2.0, Downstream: true}, 0.33},
		{"upstream çarpanı 0.6",
			NeighbourSignalEvidence{Kind: "trace_op", Ratio: 2.0, Downstream: false}, 0.28 * 0.6},
		{"yeni-şablon (ratio 0) tabana kilitli",
			NeighbourSignalEvidence{Kind: "trace_op", Ratio: 0, Downstream: true}, 0.28},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := neighbourSignalScore(tc.sig); math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("score=%.4f, want %.4f", got, tc.want)
			}
		})
	}
	// Bant tavanı kendi-sinyal bandının altında kalmalı: en güçlü komşu
	// log_pattern (0.57) < deploy tabanı (0.80) ve ≤ propagation tavanı.
	top := neighbourSignalScore(NeighbourSignalEvidence{Kind: "log_pattern", Ratio: 99, Downstream: true})
	if top >= deployBaseScore {
		t.Fatalf("komşu-sinyal tavanı (%.2f) deploy tabanını aşıyor", top)
	}
}

func TestSynthesizeNeighbourSignalTier(t *testing.T) {
	in := SynthesisInput{
		NeighbourSignals: []NeighbourSignalEvidence{
			{Service: "payment-db", Kind: "log_pattern", Pattern: "connection reset", Ratio: 6.2, Downstream: true, Hops: 1},
			{Service: "auth-svc", Kind: "trace_op", Pattern: "POST /verify", Ratio: 3.0, Downstream: false, Hops: 1},
		},
	}
	h := Synthesize("problem", "p1", "checkout", 42, in)

	if h.TopSuspect != "payment-db" {
		t.Fatalf("top suspect %q, want payment-db (downstream log_pattern en güçlü)", h.TopSuspect)
	}
	if len(h.Candidates) != 2 {
		t.Fatalf("candidates=%d, want 2", len(h.Candidates))
	}
	// Aday servis adları komşununki, path anchor→komşu.
	if h.Candidates[0].Service != "payment-db" || h.Candidates[0].Hops != 1 {
		t.Fatalf("aday şekli bozuk: %+v", h.Candidates[0])
	}
	if len(h.Candidates[0].Path) != 2 || h.Candidates[0].Path[0] != "checkout" {
		t.Fatalf("path anchor'dan başlamıyor: %v", h.Candidates[0].Path)
	}

	// Breadth katlaması: yalnız komşu-sinyal varken breadth = 1/4; aynı
	// girdiye propagation komşusu eklemek breadth'i DEĞİŞTİRMEZ (tek kanal).
	onlySignals := h.Confidence - confidenceStrengthWeight*h.TopScore
	in2 := in
	in2.Neighbours = []ScoredCause{{Service: "payment-db", Score: 0.9, Hops: 1}}
	h2 := Synthesize("problem", "p1", "checkout", 42, in2)
	withBoth := h2.Confidence - confidenceStrengthWeight*h2.TopScore
	if math.Abs(onlySignals-withBoth) > 1e-9 {
		t.Fatalf("komşu kanalı iki kez sayıldı: breadth %.4f → %.4f", onlySignals, withBoth)
	}
}

func TestSynthesizeNeighbourSignalCap(t *testing.T) {
	var in SynthesisInput
	for _, svc := range []string{"a", "b", "c", "d", "e"} {
		in.NeighbourSignals = append(in.NeighbourSignals, NeighbourSignalEvidence{
			Service: svc, Kind: "trace_op", Pattern: "op", Ratio: 5, Downstream: true, Hops: 1,
		})
	}
	h := Synthesize("problem", "p1", "checkout", 42, in)
	if len(h.Candidates) != neighborSignalMaxCandidates {
		t.Fatalf("tavan çalışmadı: %d aday, want %d", len(h.Candidates), neighborSignalMaxCandidates)
	}
}
