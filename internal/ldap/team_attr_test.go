package ldap

import (
	"testing"

	goldap "github.com/go-ldap/ldap/v3"
)

// team_attr_test.go — v0.8.430. Operator-reported: users.team showed
// the TOP division ("TEKNOLOJİ") for every LDAP login because AD
// stores the division in `department` (the legacy fallback chain's
// first hit). Pins teamFor's resolution order for every TeamAttribute
// mode and deepestOU's DN parsing.

func entry(dn string, attrs map[string][]string) *goldap.Entry {
	e := goldap.NewEntry(dn, attrs)
	return e
}

func TestDeepestOU(t *testing.T) {
	tests := []struct {
		dn   string
		want string
	}{
		{"REDACTED", "REDACTED"},
		{"REDACTED", "REDACTED"},
		{"REDACTED", ""},
		{"uid=cenk,ou=platform,ou=eng,o=bank", "platform"}, // lowercase ou
		{"not-a-dn", ""},
	}
	for _, tc := range tests {
		if got := deepestOU(tc.dn); got != tc.want {
			t.Errorf("deepestOU(%q) = %q, want %q", tc.dn, got, tc.want)
		}
	}
}

func TestTeamFor(t *testing.T) {
	dn := "REDACTED"
	e := entry(dn, map[string][]string{
		"department": {"REDACTED"},
		"ou":         {"REDACTED"},
		"division":   {"REDACTED"},
	})

	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"legacy default: department wins (the reported wrong value)",
			Config{}, "REDACTED"},
		{"explicit attribute overrides the chain",
			Config{TeamAttribute: "division"}, "REDACTED"},
		{"explicit attribute missing on entry → legacy fallback",
			Config{TeamAttribute: "extensionAttribute7"}, "REDACTED"},
		{"dn-ou takes the DEEPEST OU (the sub-team container)",
			Config{TeamAttribute: "dn-ou"}, "REDACTED"},
		{"whitespace-padded config value is trimmed",
			Config{TeamAttribute: "  division  "}, "REDACTED"},
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
