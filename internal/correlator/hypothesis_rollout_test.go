package correlator

// hypothesis_rollout_test.go — v0.10.242 Problem↔Rollout korelasyonu D2.
// Sözleşme: rollout adayı deploy ile aynı "ne değişti" kanıt türünü
// paylaşır (yalnız rollout → breadth 1 tür; deploy+rollout → yine 1 tür);
// imaj tag'i deploy sürümüyle eşleşen aday deploy adayına KATLANIR (tek
// satır, puan ↑, tavan 0.95); eşleşmeyen aday Kind="rollout" ayrı satır;
// deployMatchesTag sürüm yazımlarını tolere eder.

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func TestSynthesizeRolloutOnly(t *testing.T) {
	in := SynthesisInput{Rollouts: []RolloutCandidate{
		{Subject: "rollout:c/ns/api@rs-2", ImageTag: "2.0", Score: 0.90, Reason: "ns/api deployment 1.9 → 2.0 problem başlangıcından 12 dk önce"},
	}}
	h := Synthesize("problem", "p1", "api", 1, in)
	if len(h.Candidates) != 1 {
		t.Fatalf("aday sayısı %d", len(h.Candidates))
	}
	c := h.Candidates[0]
	if c.Kind != CauseKindRollout || c.Service != "rollout:c/ns/api@rs-2" || c.Score != 0.90 {
		t.Errorf("aday: %+v", c)
	}
	if h.TopSuspect != "rollout:c/ns/api@rs-2" {
		t.Errorf("TopSuspect %s", h.TopSuspect)
	}
	// Tek kanıt türü: breadth 1/4.
	wantConf := confidenceBreadthWeight*(1.0/float64(maxEvidenceTypes)) + confidenceStrengthWeight*0.90
	if diff := h.Confidence - wantConf; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("confidence %.4f, istenen %.4f", h.Confidence, wantConf)
	}
}

func TestSynthesizeRolloutMergesWithDeploy(t *testing.T) {
	in := SynthesisInput{
		Deploy:        &chstore.RecentDeploy{Version: "api:2.0", AgeSeconds: 600},
		FreshnessFrac: 0,
		Rollouts: []RolloutCandidate{
			{Subject: "rollout:c/ns/api@rs-2", ImageTag: "2.0", Score: 0.90, Reason: "aynı imaj"},
			{Subject: "rollout:c/ns/worker@rs-7", ImageTag: "7.1", Score: 0.50, Reason: "başka iş yükü"},
		},
	}
	h := Synthesize("problem", "p1", "api", 1, in)
	if len(h.Candidates) != 2 {
		t.Fatalf("aday sayısı %d (deploy+rollout aynı imaj TEK satır olmalı): %+v", len(h.Candidates), h.Candidates)
	}
	top := h.Candidates[0]
	if top.Service != "api" || top.Kind != "" {
		t.Errorf("birleşik aday deploy satırı olmalı: %+v", top)
	}
	if top.Score != 0.95 || !strings.Contains(top.Reason, "rollout kaydıyla doğrulandı") {
		t.Errorf("birleşik puan/gerekçe: %.2f %q", top.Score, top.Reason)
	}
	if h.Candidates[1].Kind != CauseKindRollout || h.Candidates[1].Score != 0.50 {
		t.Errorf("eşleşmeyen rollout ayrı satır: %+v", h.Candidates[1])
	}
	// deploy + rollout = tek kanıt türü.
	wantConf := confidenceBreadthWeight*(1.0/float64(maxEvidenceTypes)) + confidenceStrengthWeight*0.95
	if diff := h.Confidence - wantConf; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("confidence %.4f, istenen %.4f", h.Confidence, wantConf)
	}
}

func TestDeployMatchesTag(t *testing.T) {
	cases := []struct {
		v, tag string
		want   bool
	}{
		{"2.0", "2.0", true},
		{"v2.0", "2.0", true},
		{"api:2.0", "2.0", true},
		{"registry/api@sha256abc", "sha256abc", true},
		{"2.0", "2.1", false},
		{"", "2.0", false},
		{"2.0", "", false},
		{"api:", "", false},
	}
	for _, c := range cases {
		if got := deployMatchesTag(c.v, c.tag); got != c.want {
			t.Errorf("%q vs %q: %v", c.v, c.tag, got)
		}
	}
}
