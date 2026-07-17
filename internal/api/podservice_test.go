package api

import "testing"

// v0.9.11 — pod↔servis çözümleme sözleşmesi (korelasyon audit'i):
// tek aday direkt, çoklu aday metadata-namespace ayrıştırmasıyla,
// çözülemeyen belirsizlik YANLIŞ ETİKET yerine boş.
func TestPickPodService(t *testing.T) {
	ns := map[string]string{
		"payments-api": "payments",
		"fraud-agent":  "security",
		"twin-a":       "payments",
		"twin-b":       "payments",
	}
	cases := []struct {
		name  string
		cands []string
		podNS string
		want  string
	}{
		{"no candidates", nil, "payments", ""},
		{"single candidate wins regardless of ns", []string{"payments-api"}, "other", "payments-api"},
		{"multi resolved by namespace", []string{"payments-api", "fraud-agent"}, "payments", "payments-api"},
		{"multi resolved to the other", []string{"payments-api", "fraud-agent"}, "security", "fraud-agent"},
		{"multi unresolvable — no ns match", []string{"payments-api", "fraud-agent"}, "infra", ""},
		{"multi unresolvable — two in same ns", []string{"twin-a", "twin-b"}, "payments", ""},
		{"multi without pod namespace", []string{"payments-api", "fraud-agent"}, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pickPodService(c.cands, c.podNS, ns); got != c.want {
				t.Fatalf("pickPodService(%v, %q) = %q, want %q", c.cands, c.podNS, got, c.want)
			}
		})
	}
}
