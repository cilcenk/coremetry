package logstore

import "testing"

// bare_hex_id_test.go — v0.8.521 regression (operator-reported: trace
// attributes → "open in Logs" hiç sonuç bulmuyordu). Search kutusuna
// yapıştırılan çıplak id kolon eşleşmesine yükselir; normal metin
// yükselMEZ.
func TestIsBareHexID(t *testing.T) {
	cases := []struct {
		q    string
		want bool
	}{
		{"4bf92f3577b34da6a3ce929d0e0e4736", true},   // 32-hex trace
		{"00f067aa0ba902b7", true},                    // 16-hex span
		{"  4BF92F3577B34DA6A3CE929D0E0E4736  ", true}, // boşluk + büyük harf
		{"4bf92f3577b34da6a3ce929d0e0e473", false},   // 31 hane
		{"4bf92f3577b34da6a3ce929d0e0e4736a", false},  // 33 hane
		{"connection refused", false},
		{"4bf92f35-77b3-4da6-a3ce-929d0e0e4736", false}, // tireli UUID — serbest metin kalır
		{"", false},
	}
	for _, c := range cases {
		if got := isBareHexID(c.q); got != c.want {
			t.Fatalf("isBareHexID(%q) = %v, want %v", c.q, got, c.want)
		}
	}
}
