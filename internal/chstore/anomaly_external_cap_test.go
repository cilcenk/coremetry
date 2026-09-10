package chstore

import "testing"

// v0.10.587 — dış hat açılış tavanı ayarı. 0 = ayarlanmamış → varsayılan;
// aralık dışı → varsayılan; meşru değer aynen. Eski settings satırı alanı
// taşımaz, sıfırı "kapalı" okumak seli geri getirirdi.
func TestNormalizeExternalOpenCap(t *testing.T) {
	d := DefaultAnomalySensitivity().ExternalOpenCapPerTick
	if d != 20 {
		t.Fatalf("varsayılan 20 olmalı, %d", d)
	}
	cases := map[int]int{0: 20, -5: 20, 201: 20, 1: 1, 50: 50, 200: 200}
	for in, want := range cases {
		c := DefaultAnomalySensitivity()
		c.ExternalOpenCapPerTick = in
		if got := NormalizeAnomalySensitivity(c).ExternalOpenCapPerTick; got != want {
			t.Errorf("in=%d → %d, beklenen %d", in, got, want)
		}
	}
}
