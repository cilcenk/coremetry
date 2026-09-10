package ldap

import (
	"testing"

	goldap "github.com/go-ldap/ldap/v3"
)

// team_attr_test.go — v0.8.430. Operator-reported: users.team showed
// the TOP division ("ENGINEERING") for every LDAP login because AD
// stores the division in `department` (the legacy fallback chain's
// first hit). Pins teamFor's resolution order for every TeamAttribute
// mode and deepestOU's DN parsing.
//
// v0.10.619 — fixture adları SENTETİK (Alice/Bob, Payments/Engineering):
// eski yazım gerçek kişi ve birim adlarını taşıyordu; şekil (DN derinliği,
// bileşik displayName'deki parantez/yıldız/tire) birebir korundu.

func entry(dn string, attrs map[string][]string) *goldap.Entry {
	e := goldap.NewEntry(dn, attrs)
	return e
}

func TestDeepestOU(t *testing.T) {
	tests := []struct {
		dn   string
		want string
	}{
		{"CN=Alice,OU=Payments,OU=ENGINEERING,DC=bank,DC=local", "Payments"},
		{"CN=Alice,OU=ENGINEERING,DC=bank,DC=local", "ENGINEERING"},
		{"CN=Alice,DC=bank,DC=local", ""},
		{"uid=alice,ou=platform,ou=eng,o=bank", "platform"}, // lowercase ou
		{"not-a-dn", ""},
	}
	for _, tc := range tests {
		if got := deepestOU(tc.dn); got != tc.want {
			t.Errorf("deepestOU(%q) = %q, want %q", tc.dn, got, tc.want)
		}
	}
}

func TestTeamFor(t *testing.T) {
	dn := "CN=Alice,OU=Payments,OU=ENGINEERING,DC=bank,DC=local"
	e := entry(dn, map[string][]string{
		"department": {"ENGINEERING"},
		"ou":         {"ENGINEERING"},
		"division":   {"Payments Team"},
	})

	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"legacy default: department wins (the reported wrong value)",
			Config{}, "ENGINEERING"},
		{"explicit attribute overrides the chain",
			Config{TeamAttribute: "division"}, "Payments Team"},
		{"explicit attribute missing on entry → legacy fallback",
			Config{TeamAttribute: "extensionAttribute7"}, "ENGINEERING"},
		{"dn-ou takes the DEEPEST OU (the sub-team container)",
			Config{TeamAttribute: "dn-ou"}, "Payments"},
		{"whitespace-padded config value is trimmed",
			Config{TeamAttribute: "  division  "}, "Payments Team"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := teamFor(e, tc.cfg); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}

	// dn-ou on an OU-less DN falls back to the legacy chain.
	e2 := entry("CN=Svc,DC=bank,DC=local", map[string][]string{"department": {"OPS"}})
	if got := teamFor(e2, Config{TeamAttribute: "dn-ou"}); got != "OPS" {
		t.Fatalf("dn-ou fallback: got %q, want OPS", got)
	}
}

func TestTeamAttrForFetch(t *testing.T) {
	if got := teamAttrForFetch(Config{}); got != "" {
		t.Fatalf("empty config: %q", got)
	}
	if got := teamAttrForFetch(Config{TeamAttribute: "dn-ou"}); got != "" {
		t.Fatalf("dn-ou needs no extra fetch: %q", got)
	}
	if got := teamAttrForFetch(Config{TeamAttribute: "division"}); got != "division" {
		t.Fatalf("explicit attr: %q", got)
	}
}

// TestApplyTeamRegex — v0.8.434. Operator-reported: the AD displayName
// is a composite ("Bob Example (Platform Service Mgmt) * SOFTWARE
// SPECIALIST-Payments") and the SUB-TEAM is the segment after the last
// dash. TeamRegex extracts it; every branch tabled (unit-mixing rule).
func TestApplyTeamRegex(t *testing.T) {
	const operatorDisplay = "Bob Example (Platform Service Mgmt( * SOFTWARE SPECIALIST-Payments"

	tests := []struct {
		name    string
		raw     string
		pattern string
		want    string
	}{
		{"operator's exact composite → trailing segment",
			operatorDisplay, `-([^-]+)$`, "Payments"},
		{"trailing whitespace trimmed",
			"UNVAN- Ekip Adi ", `-([^-]+)$`, "Ekip Adi"},
		{"multi-dash: LAST segment wins with the anchored pattern",
			"A-B-C", `-([^-]+)$`, "C"},
		{"empty pattern passes raw through",
			operatorDisplay, "", operatorDisplay},
		{"no capture group → whole match",
			"team=Payments", `Payments`, "Payments"},
		{"NO match → empty, never the raw composite (the reported bug)",
			"ENGINEERING", `-([^-]+)$`, ""},
		{"invalid pattern is ignored → raw passes through",
			operatorDisplay, `-([`, operatorDisplay},
		{"empty raw stays empty",
			"", `-([^-]+)$`, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := applyTeamRegex(tc.raw, tc.pattern); got != tc.want {
				t.Fatalf("applyTeamRegex(%q, %q) = %q, want %q", tc.raw, tc.pattern, got, tc.want)
			}
		})
	}
}

// End-to-end through teamFor: displayName source + regex — the exact
// operator configuration this shipped for.
func TestTeamForWithRegex(t *testing.T) {
	e := entry("CN=Bob,OU=X,DC=bank,DC=local", map[string][]string{
		"displayName": {"Bob Example (Platform Service Mgmt( * SOFTWARE SPECIALIST-Payments"},
		"department":  {"ENGINEERING"},
	})
	cfg := Config{TeamAttribute: "displayName", TeamRegex: `-([^-]+)$`}
	if got := teamFor(e, cfg); got != "Payments" {
		t.Fatalf("teamFor = %q, want Payments", got)
	}
	// Regex also composes with the legacy chain: department has no dash
	// → empty (not the division name — that was the original complaint).
	if got := teamFor(e, Config{TeamRegex: `-([^-]+)$`}); got != "" {
		t.Fatalf("legacy chain + no-match regex should yield empty, got %q", got)
	}
}
