package chstore

import (
	"strings"
	"testing"
)

// incident_rootcause_test.go — v0.10.698 (Dynatrace paritesi #1, dilim B).
//
// Incident satırının kök nedeni bağlı problemlerin hipotezlerinden seçilir.
// Seçici saf; burada sözleşmesi çivilenir: adı olmayan / eşik altı aday
// seçilmez, en yüksek güven kazanır, eşitlikte skor, yine eşitse ad
// (determinizm — iki okuma aynı cevabı verir).
func TestPickIncidentRootCause(t *testing.T) {
	rc := func(s string, conf, score float64) RootCauseSummary {
		return RootCauseSummary{TopSuspect: s, Confidence: conf, TopScore: score}
	}
	cases := []struct {
		name string
		in   []RootCauseSummary
		want string // "" = nil beklenir
	}{
		{"boş küme → nil", nil, ""},
		{"yalnız adsız 'hesaplanıyor' özeti → nil", []RootCauseSummary{rc("", 0.4, 0)}, ""},
		{"eşik altı (ribbon 0.05) → nil", []RootCauseSummary{rc("shop-db", 0.05, 9)}, ""},
		{"en yüksek güven kazanır", []RootCauseSummary{rc("shop-payment", 0.3, 1), rc("shop-db", 0.8, 0.2), rc("shop-cart", 0.5, 5)}, "shop-db"},
		{"güven eşit → skor", []RootCauseSummary{rc("shop-cart", 0.6, 1), rc("shop-db", 0.6, 3)}, "shop-db"},
		{"güven+skor eşit → ad sırası", []RootCauseSummary{rc("shop-payment", 0.6, 2), rc("shop-cart", 0.6, 2)}, "shop-cart"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pickIncidentRootCause(c.in)
			if c.want == "" {
				if got != nil {
					t.Fatalf("nil beklendi, %+v geldi", *got)
				}
				return
			}
			if got == nil || got.TopSuspect != c.want {
				t.Fatalf("beklenen %q, gelen %+v", c.want, got)
			}
		})
	}
	// Girdi sırasından bağımsız (permütasyon): aynı üç aday ters sırada.
	a := pickIncidentRootCause([]RootCauseSummary{rc("a", 0.6, 2), rc("b", 0.6, 2), rc("c", 0.9, 0)})
	b := pickIncidentRootCause([]RootCauseSummary{rc("c", 0.9, 0), rc("b", 0.6, 2), rc("a", 0.6, 2)})
	if a == nil || b == nil || a.TopSuspect != "c" || b.TopSuspect != "c" {
		t.Fatalf("permütasyon değişmezliği bozuk: %+v vs %+v", a, b)
	}
}

// Eşik ribbon ile aynı olmalı: iki yüzey aynı problemi farklı yorumlamasın.
func TestIncidentRootCauseThresholdMatchesRibbon(t *testing.T) {
	if incidentRootCauseMinConfidence != 0.05 {
		t.Fatalf("eşik 0.05 olmalı (RootCauseRibbon `conf > 0.05`), %v", incidentRootCauseMinConfidence)
	}
	fe, err := readRepoFile("../../frontend/src/components/RootCauseRibbon.tsx")
	if err != nil {
		t.Skip("frontend kaynağı yok: " + err.Error())
	}
	if !strings.Contains(fe, "conf > 0.05") {
		t.Error("RootCauseRibbon eşiği değişmiş — incidentRootCauseMinConfidence ile hizala")
	}
}

// Toplu okuma şekli: tek IN-listesi + tavan + max_execution_time; tek tek
// IncidentProblems çağrısı (N+1) değil.
func TestIncidentRootCauseBatchShape(t *testing.T) {
	src := mustReadSource(t, "incident_rootcause.go")
	for _, want := range []string{
		"FROM incident_problems FINAL",
		"WHERE incident_id IN (",
		"max_execution_time = 10",
		`GetHypotheses(ctx, "problem", pids)`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("incident_rootcause.go %q içermeli", want)
		}
	}
	if strings.Contains(src, "s.IncidentProblems(ctx") {
		t.Error("incident başına IncidentProblems çağrısı N+1 — toplu IncidentProblemIDs kullan")
	}
}
