package api

// v0.9.1195 (AI Faz 5.2) — terfi içeriğinin saf parçaları.

import (
	"strings"
	"testing"
)

func TestCuratedDocTitle(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"kısa soru aynen", "checkout neden yavaş", "KB: checkout neden yavaş"},
		{"satır sonları tek boşluğa", "checkout\nneden\n\nyavaş", "KB: checkout neden yavaş"},
		{"boş prompt dürüst ad", "", "KB: (sorusuz cevap)"},
		{"yalnız boşluk", "  \n\t ", "KB: (sorusuz cevap)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := curatedDocTitle(c.in); got != c.want {
				t.Errorf("curatedDocTitle(%q) = %q, beklenen %q", c.in, got, c.want)
			}
		})
	}

	t.Run("tavan RUNE cinsinden — Türkçe soru karakter ortasından bölünmez", func(t *testing.T) {
		in := strings.Repeat("ş", 80)
		got := curatedDocTitle(in)
		if !strings.HasSuffix(got, "…") {
			t.Fatalf("uzun başlık kırpma imi taşımalı: %q", got)
		}
		for _, r := range got {
			if r == '�' {
				t.Fatalf("bozuk rune — bayt kesmesi sızmış: %q", got)
			}
		}
		if n := len([]rune(strings.TrimPrefix(strings.TrimSuffix(got, "…"), "KB: "))); n != 60 {
			t.Errorf("kırpılan gövde %d rune, beklenen 60", n)
		}
	})
}

func TestCuratedDocText(t *testing.T) {
	got := curatedDocText("chat", "  checkout neden yavaş?  ", "  Kök neden DB havuzu.  ")
	// Retrieval chunk'ı tek başına okunur: hangi yarının soru olduğu
	// İÇERİKTEN anlaşılmalı; yüzey etiketi bağlam taşır.
	for _, want := range []string{"Soru:\ncheckout neden yavaş?", "operatör onaylı, yüzey: chat", "Kök neden DB havuzu."} {
		if !strings.Contains(got, want) {
			t.Errorf("gövde %q içermeli:\n%s", want, got)
		}
	}
	// Yüzeysiz çağrı etiketi atlar, biçimi bozmaz.
	if s2 := curatedDocText("", "s", "c"); !strings.Contains(s2, "Cevap (operatör onaylı):") {
		t.Errorf("yüzeysiz biçim bozuk:\n%s", s2)
	}
}
