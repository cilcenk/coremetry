package anomaly

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// exception_context_test.go — v0.9.415 pinleri. pickDeploysAroundStart
// v0.9.414 verify bulgularını kalıcı kilitler: (1) düz "son 5" kesimi
// uzun ömürlü gruplarda başlangıçtan hemen önceki asıl adayı düşürüyordu,
// (2) FirstSeen SONRASI deploy'lar "-N dk önce" diye yazılıp LLM'e yanlış
// kanıt oluyordu.

func TestPickDeploysAroundStart(t *testing.T) {
	min := int64(time.Minute)
	first := int64(1000000) * min // FirstSeen
	dep := func(v string, offMin int64) chstore.Deploy {
		return chstore.Deploy{Version: v, TimeUnixNs: first + offMin*min}
	}

	// Uzun ömürlü grup: başlangıçtan önce 5, sonra 4 deploy (ASC).
	deps := []chstore.Deploy{
		dep("v1", -300), dep("v2", -200), dep("v3", -90), dep("v4", -30), dep("v5", -5),
		dep("v6", 60), dep("v7", 120), dep("v8", 600), dep("v9", 1200),
	}
	parts := pickDeploysAroundStart(deps, first)
	if len(parts) != 5 {
		t.Fatalf("5 parça beklenirdi (önce 3 + sonra 2), %d geldi: %v", len(parts), parts)
	}
	// Asıl aday (v5, başlangıçtan 5 dk önce) MUTLAKA listede.
	joined := strings.Join(parts, " | ")
	if !strings.Contains(joined, "v5 (grubun başlangıcından 5 dk ÖNCE)") {
		t.Errorf("başlangıçtan hemen önceki deploy düşmüş: %s", joined)
	}
	// Önce-tarafı en yakın 3: v3, v4, v5 (v1/v2 elenir).
	if strings.Contains(joined, "v1") || strings.Contains(joined, "v2") {
		t.Errorf("uzak önce-deploy'ları elenmeli: %s", joined)
	}
	// Sonra-tarafı ilk 2 ve yön AÇIK "SONRA" — asla negatif "önce" değil.
	if !strings.Contains(joined, "v6 (grubun başlangıcından 60 dk SONRA") ||
		!strings.Contains(joined, "v7 (grubun başlangıcından 120 dk SONRA") {
		t.Errorf("sonra-deploy'ları yönlü yazılmalı: %s", joined)
	}
	if strings.Contains(joined, "-") && strings.Contains(joined, "dk ÖNCE") &&
		strings.Contains(joined, "-60") {
		t.Errorf("negatif 'önce' sızmış: %s", joined)
	}

	// Hepsi başlangıçtan önce → yalnız son 3, SONRA yok.
	pre := deps[:5]
	parts2 := pickDeploysAroundStart(pre, first)
	if len(parts2) != 3 || strings.Contains(strings.Join(parts2, " "), "SONRA") {
		t.Errorf("yalnız-önce durumunda son 3 beklenirdi: %v", parts2)
	}
}

func TestAssembleExceptionPrompt(t *testing.T) {
	g := &chstore.ExceptionGroup{
		Type: "java.lang.NullPointerException", Message: "boom", Service: "checkout",
		State: "new", Occurrences: 700,
		FirstSeen: time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC).UnixNano(),
		LastSeen:  time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC).UnixNano(),
	}
	// Tüm bloklar dolu → hepsi sırayla yer alır.
	p := assembleExceptionPrompt(g, "toplam=700", "at com.x.Y.z(Y.java:1)", "\n\nTRACE_BLOK", "\n\nLOG_BLOK", "\n\nDEPLOY_BLOK")
	for _, want := range []string{
		"java.lang.NullPointerException", "checkout", "Occurrence trendi: toplam=700",
		"Temsilî STACKTRACE", "TRACE_BLOK", "LOG_BLOK", "DEPLOY_BLOK",
		"yayılan (propagate) hataları kök sanma",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt %q içermeli", want)
		}
	}
	// Boş bloklar başlık bırakmaz.
	p2 := assembleExceptionPrompt(g, "", "", "", "", "")
	if strings.Contains(p2, "Occurrence trendi") || strings.Contains(p2, "STACKTRACE") {
		t.Errorf("boş bloklar başlık üretmemeli:\n%s", p2)
	}
}

// isExceptionExplainCandidate — inbox P1 formülüyle (exceptionPriority)
// birebir: last_seen ≤5dk VE occurrences ≥500; özet doluysa asla.
func TestIsExceptionExplainCandidate(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	mk := func(ageMin int64, occ uint64, summary string) chstore.ExceptionGroup {
		return chstore.ExceptionGroup{
			LastSeen: now.Add(-time.Duration(ageMin) * time.Minute).UnixNano(),
			Occurrences: occ, AISummary: summary,
		}
	}
	cases := []struct {
		name string
		g    chstore.ExceptionGroup
		want bool
	}{
		{"P1 taze+yoğun", mk(2, 600, ""), true},
		{"eşik altı occurrences", mk(2, 400, ""), false},
		{"bayat", mk(10, 600, ""), false},
		{"özet zaten var", mk(2, 600, "hazır"), false},
	}
	for _, c := range cases {
		if got := isExceptionExplainCandidate(c.g, now); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTruncRunes(t *testing.T) {
	// Türkçe çok-baytlı karakterler ortadan kesilmez (rune-güvenli).
	s := "şeçöğüişeçöğüi"
	got := truncRunes(s, 5)
	if got != "şeçöğ…" {
		t.Errorf("truncRunes rune-güvenli değil: %q", got)
	}
	if truncRunes("abc", 5) != "abc" {
		t.Errorf("kısa string dokunulmamalı")
	}
}
