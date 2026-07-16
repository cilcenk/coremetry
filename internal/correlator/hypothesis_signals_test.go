package correlator

import (
	"reflect"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// hypothesis_signals_test.go — v0.8.571. EvidenceBundle.Signals toplanıyor
// ama sentez hattına hiç girmiyordu (operatör-bildirimli); Synthesize artık
// dördüncü kanıt katmanı olarak [0.30, 0.60] bandında skorluyor. Testler
// sabit-SEMBOLİK (retune'a dayanıklı); sınır invariantları yalnız ordering
// assert'leriyle pin'li — evin deseni.

func TestSynthesizeSignalsOnly(t *testing.T) {
	in := SynthesisInput{Signals: []SignalEvidence{
		{Kind: "log_pattern", Pattern: "OOM killed", Ratio: 6.0},
	}}
	h := Synthesize("problem", "p1", "payments", 1000, in)
	if len(h.Candidates) != 1 || h.TopSuspect != "payments" {
		t.Fatalf("tek signal → tek aday (anchor servis): %+v", h.Candidates)
	}
	// Skor sembolik: taban + bonus + span·norm, norm=(6−2)/(10−2)=0.5.
	want := signalTierBase + signalLogPatternBonus + signalRatioSpan*0.5
	if !hApprox(h.Candidates[0].Score, want) {
		t.Fatalf("skor=%v istenen=%v", h.Candidates[0].Score, want)
	}
	// Breadth 1/4.
	wantConf := confidenceBreadthWeight*(1.0/float64(maxEvidenceTypes)) + confidenceStrengthWeight*want
	if !hApprox(h.Confidence, wantConf) {
		t.Fatalf("confidence=%v istenen=%v", h.Confidence, wantConf)
	}
	if h.Candidates[0].Reason == "" {
		t.Fatal("signal adayı Reason taşımalı (prompt'a giren metin)")
	}
}

func TestSignalScoreBounds(t *testing.T) {
	cases := []struct {
		name string
		sig  SignalEvidence
		want float64
	}{
		{"tetik tabanı ratio=2 → norm 0", SignalEvidence{Kind: "trace_op", Ratio: 2}, signalTierBase},
		{"doygunluk ratio≥10 → tavan", SignalEvidence{Kind: "trace_op", Ratio: 40}, signalTierBase + signalRatioSpan},
		{"yeni-template ratio=0 → taban (negatif norm clamp)", SignalEvidence{Kind: "trace_op", Ratio: 0}, signalTierBase},
		{"log_pattern tavanı", SignalEvidence{Kind: "log_pattern", Ratio: 10}, signalTierBase + signalLogPatternBonus + signalRatioSpan},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := signalScore(c.sig); !hApprox(got, c.want) {
				t.Fatalf("signalScore=%v istenen=%v", got, c.want)
			}
		})
	}
	// Bandın iki komşu-katman invariantı: tavan < prop tavanı < deploy tabanı,
	// taban > co-firing.
	ceil := signalScore(SignalEvidence{Kind: "log_pattern", Ratio: 999})
	if ceil >= propTierBase || propTierBase >= deployBaseScore {
		t.Fatalf("katman tavan sırası bozuk: signal=%v prop=%v deploy=%v", ceil, propTierBase, deployBaseScore)
	}
	if signalTierBase <= cofiringScore {
		t.Fatal("signal tabanı co-firing'in üstünde kalmalı")
	}
}

func TestSynthesizeSignalKindOrdering(t *testing.T) {
	// Eşit ratio'da log_pattern üstte (imza taşıyan kanıt kazanır)…
	in := SynthesisInput{Signals: []SignalEvidence{
		{Kind: "trace_op", Pattern: "GET /pay", Ratio: 5},
		{Kind: "log_pattern", Pattern: "timeout", Ratio: 5},
	}}
	h := Synthesize("problem", "p1", "svc", 1000, in)
	if h.Candidates[0].Reason == "" || h.Candidates[0].Score <= h.Candidates[1].Score {
		t.Fatalf("eşit ratio'da log_pattern önde olmalı: %+v", h.Candidates)
	}
	// …ama yüksek-ratio trace_op düşük-ratio log_pattern'i geçebilir
	// (bantlar bilerek örtüşük).
	in2 := SynthesisInput{Signals: []SignalEvidence{
		{Kind: "log_pattern", Pattern: "warn", Ratio: 2},
		{Kind: "trace_op", Pattern: "POST /x", Ratio: 10},
	}}
	h2 := Synthesize("problem", "p1", "svc", 1000, in2)
	if h2.Candidates[0].Score <= h2.Candidates[1].Score ||
		signalScore(in2.Signals[1]) <= signalScore(in2.Signals[0]) {
		t.Fatalf("yüksek-ratio trace_op düşük-ratio log_pattern'i geçmeli: %+v", h2.Candidates)
	}
}

func TestSynthesizeSignalsDeterministic(t *testing.T) {
	// Eşit skorlu iki signal: Service tie-break AYIRT EDEMEZ (ikisi de
	// anchor) — (Kind, Pattern) ön-sıralaması bayt-özdeşliği taşır.
	a := SignalEvidence{Kind: "log_pattern", Pattern: "aaa", Ratio: 5}
	b := SignalEvidence{Kind: "log_pattern", Pattern: "bbb", Ratio: 5}
	h1 := Synthesize("problem", "p1", "svc", 1000, SynthesisInput{Signals: []SignalEvidence{a, b}})
	h2 := Synthesize("problem", "p1", "svc", 1000, SynthesisInput{Signals: []SignalEvidence{b, a}})
	if !reflect.DeepEqual(h1, h2) {
		t.Fatalf("permütasyon çıktıyı değiştirdi:\n%+v\n%+v", h1, h2)
	}
	if h1.Candidates[0].Reason == h1.Candidates[1].Reason {
		t.Fatal("iki farklı pattern iki farklı Reason üretmeli")
	}
}

func TestSynthesizeSignalCap(t *testing.T) {
	sigs := make([]SignalEvidence, 0, signalMaxCandidates+3)
	for i := 0; i < signalMaxCandidates+3; i++ {
		sigs = append(sigs, SignalEvidence{Kind: "trace_op", Pattern: string(rune('a' + i)), Ratio: 5})
	}
	h := Synthesize("problem", "p1", "svc", 1000, SynthesisInput{Signals: sigs})
	if len(h.Candidates) != signalMaxCandidates {
		t.Fatalf("signal adayları %d'e cap'lenmeli, %d geldi", signalMaxCandidates, len(h.Candidates))
	}
}

func TestSynthesizeBreadthWithoutSignals(t *testing.T) {
	// Diğer üç katman dolu, Signals boş → breadth 3/4 (eski 1.0 değil).
	in := SynthesisInput{
		Deploy:           &chstore.RecentDeploy{Version: "v1", AgeSeconds: 60},
		FreshnessFrac:    0.5,
		Neighbours:       []ScoredCause{{Service: "db", Score: 0.8, Hops: 1}},
		CoFiringServices: []string{"svc"},
	}
	h := Synthesize("problem", "p1", "svc", 1000, in)
	top := deployBaseScore + deployFreshnessBonusMax*0.5
	wantConf := confidenceBreadthWeight*(3.0/float64(maxEvidenceTypes)) + confidenceStrengthWeight*top
	if !hApprox(h.Confidence, wantConf) {
		t.Fatalf("breadth 3/4 beklenirdi: conf=%v istenen=%v", h.Confidence, wantConf)
	}
}
