package chstore

import (
	"strings"
	"testing"
)

// v0.9.511 — iş boyutu ifadesi ÜÇ maliyet katmanına çözülür ve sıra
// sözleşmenin parçası: terfi etmiş kolon > well-known > dizi araması.
// Yanlış katmana düşmek sessizce pahalı sorgu üretir (dizi taraması
// milyarlarca satırda terfi etmiş kolondan kat kat pahalı).
func TestBusinessDimExpr(t *testing.T) {
	// Terfi etmiş kolon kaydı boot'ta doluyor; testte elle kuruyoruz.
	withPromoted(t, "CHANNEL_CODE", "attr_channel_code")

	t.Run("terfi etmis kolon dogrudan kullanilir", func(t *testing.T) {
		expr, args := businessDimExpr("CHANNEL_CODE")
		if expr != "attr_channel_code" {
			t.Errorf("expr = %q, terfi etmiş kolon beklenir", expr)
		}
		if len(args) != 0 {
			t.Errorf("terfi etmiş kolonda bind arg olmamalı, got %v", args)
		}
	})

	t.Run("bilinmeyen anahtar dizi yoluna duser ve BIND edilir", func(t *testing.T) {
		expr, args := businessDimExpr("SOME_CUSTOM_KEY")
		if !strings.Contains(expr, "indexOf(attr_keys, ?)") {
			t.Errorf("expr = %q, dizi yolu beklenir", expr)
		}
		// Anahtar SQL'e gömülmemeli — enjeksiyon yüzeyi kapalı kalmalı.
		if strings.Contains(expr, "SOME_CUSTOM_KEY") {
			t.Error("anahtar SQL'e gömülmüş — bind arg olmalı")
		}
		if len(args) != 1 || args[0] != "SOME_CUSTOM_KEY" {
			t.Errorf("args = %v, anahtar bind edilmeli", args)
		}
	})
}
