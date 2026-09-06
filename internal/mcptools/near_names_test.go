package mcptools

import (
	"strings"
	"testing"
)

// near_names_test.go — v0.10.464 (CoSRE sohbet paritesi D3): bulanık servis
// adı çözümü tool katmanında. "mobile bff" tireli katalog adıyla alt-dize
// olarak asla eşleşmiyordu (list_services name_contains); jeton kapsaması
// çözer. Adlar SENTETİK.

var nnLive = []string{"mobile-commercial-bff-prod", "mobile-retail-bff-prod", "checkout-service", "payment-service", "inventory"}

func TestNearNamesMoved(t *testing.T) {
	cases := map[string][]string{
		"checkout-service": {"checkout-service"},
		"checkout":         {"checkout-service"},
		"mobile bff":       {"mobile-commercial-bff-prod", "mobile-retail-bff-prod"},
		"mobile retail":    {"mobile-retail-bff-prod"},
		"chekout-service":  {"checkout-service"},
		"ap":               nil,
		"zzqx":             nil,
	}
	for q, want := range cases {
		if got := NearNames(q, nnLive, 8); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%q → %v, want %v", q, got, want)
		}
	}
}

func TestResolveServiceAmong(t *testing.T) {
	if ex, c := ResolveServiceAmong("Checkout-Service", nnLive, 8); ex != "checkout-service" || c != nil {
		t.Fatalf("harfe duyarsız tam eş: %q %v", ex, c)
	}
	if ex, c := ResolveServiceAmong("mobile retail bff", nnLive, 8); ex != "mobile-retail-bff-prod" || c != nil {
		t.Fatalf("tek aday çözülmeli: %q %v", ex, c)
	}
	if ex, c := ResolveServiceAmong("mobile bff", nnLive, 8); ex != "" || len(c) != 2 {
		t.Fatalf("iki aday sorulmalı: %q %v", ex, c)
	}
	if ex, c := ResolveServiceAmong("zzqx", nnLive, 8); ex != "" || c != nil {
		t.Fatalf("katalogda yok → boş: %q %v", ex, c)
	}
	if ex, c := ResolveServiceAmong("  ", nnLive, 8); ex != "" || c != nil {
		t.Fatalf("boş ifade → boş: %q %v", ex, c)
	}
}
