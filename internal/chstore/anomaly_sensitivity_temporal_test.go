package chstore

import "testing"

// v0.10.700 — TemporalRanking anahtarı: varsayılan gölge, yalnız "on" açar,
// Normalize somutlaştırır ve PUT'ta alanı düşürmez.
func TestTemporalRankingNormalize(t *testing.T) {
	for in, want := range map[string]string{"": "shadow", "shadow": "shadow", "ON": "on", "on": "on", "garbage": "shadow", " on ": "on"} {
		if got := normalizeTemporalRanking(in); got != want {
			t.Errorf("%q → %q, beklenen %q", in, got, want)
		}
	}
	if DefaultAnomalySensitivity().TemporalRankingOn() {
		t.Fatal("varsayılan gölge olmalı")
	}
	c := NormalizeAnomalySensitivity(AnomalySensitivityConfig{TemporalRanking: "on"})
	if c.TemporalRanking != "on" || !c.TemporalRankingOn() {
		t.Fatalf("Normalize alanı taşımalı: %+v", c.TemporalRanking)
	}
	if NormalizeAnomalySensitivity(AnomalySensitivityConfig{}).TemporalRanking != "shadow" {
		t.Fatal("boş blob shadow'a somutlaşmalı")
	}
}
