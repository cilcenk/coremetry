package blocks

import "testing"

// v0.10.541 — blok sıralayıcı: artan seq, kararlı id, final=true, payload aynen.
func TestSequencer(t *testing.T) {
	var s Sequencer
	a := s.Next(TypeChart, map[string]any{"service": "api"})
	b := s.Next(TypeLink, "x")
	if a["id"] != "b1" || a["seq"] != 1 || a["type"] != TypeChart || a["final"] != true || a["payload"].(map[string]any)["service"] != "api" {
		t.Fatalf("ilk blok: %v", a)
	}
	if b["id"] != "b2" || b["seq"] != 2 || b["type"] != TypeLink || b["payload"] != "x" || s.Count() != 2 {
		t.Fatalf("ikinci blok: %v", b)
	}
}
