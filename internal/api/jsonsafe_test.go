package api

import (
	"encoding/json"
	"math"
	"testing"
)

type sample struct {
	A float64
	B float64
	C string
}

// v0.5.303 — lock the contract: NaN/Inf inside structs reachable
// via *T, []T, map[string]T, or nested combinations must be
// zeroed before json.Marshal would otherwise reject the value.

func TestSanitizeFloats_PtrStruct(t *testing.T) {
	v := &sample{A: math.NaN(), B: math.Inf(1), C: "ok"}
	sanitizeFloats(v)
	if v.A != 0 || v.B != 0 {
		t.Fatalf("expected zeroed floats, got A=%v B=%v", v.A, v.B)
	}
	if _, err := json.Marshal(v); err != nil {
		t.Fatalf("post-scrub marshal: %v", err)
	}
}

func TestSanitizeFloats_SliceOfStructs(t *testing.T) {
	s := []sample{
		{A: math.NaN(), B: 1.5, C: "x"},
		{A: 2.5, B: math.Inf(-1), C: "y"},
	}
	sanitizeFloats(s)
	if s[0].A != 0 || s[1].B != 0 {
		t.Fatalf("slice scrub missed: %+v", s)
	}
	if _, err := json.Marshal(s); err != nil {
		t.Fatalf("post-scrub marshal: %v", err)
	}
}

func TestSanitizeFloats_MapValueStruct(t *testing.T) {
	m := map[string]sample{
		"a": {A: math.NaN(), B: 3.14, C: "z"},
	}
	sanitizeFloats(m)
	if m["a"].A != 0 {
		t.Fatalf("map-of-struct scrub missed: %+v", m)
	}
}

func TestSanitizeFloats_MapInterfaceFloat(t *testing.T) {
	m := map[string]any{
		"ratio": math.NaN(),
		"name":  "svc",
	}
	sanitizeFloats(m)
	if v, ok := m["ratio"].(float64); !ok || v != 0 {
		t.Fatalf("map[string]any float NaN not zeroed: %v", m["ratio"])
	}
}

func TestSanitizeFloats_PreservesFinite(t *testing.T) {
	v := &sample{A: 1.5, B: -2.5, C: "ok"}
	sanitizeFloats(v)
	if v.A != 1.5 || v.B != -2.5 {
		t.Fatalf("finite floats clobbered: %+v", v)
	}
}
