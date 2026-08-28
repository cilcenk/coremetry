package api

import (
	"testing"
	"time"
)

// v0.10.135 — ?at= üç birimde gelir (s / ms / ns); HER birim test edilir
// (v0.6.36 birim-karıştırma dersi). Aynı an, üç yazım, tek sonuç. FE
// linkleri ms taşır (entityHref → Date.now ölçeği); ms'yi saniye sanmak
// 50k yıl ileri bir "an" üretir ve her pivot "o an geçerli kayıt yok" derdi.
func TestParseAtUnits(t *testing.T) {
	want := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct{ name, in string }{
		{"s", "1787918400"}, {"ms", "1787918400000"}, {"ns", "1787918400000000000"},
	} {
		if got := parseAt(c.in); !got.Equal(want) {
			t.Errorf("%s: %s → %v, beklenen %v", c.name, c.in, got, want)
		}
	}
	if !parseAt("").IsZero() || !parseAt("x").IsZero() {
		t.Fatal("boş/bozuk → sıfır zaman")
	}
}
