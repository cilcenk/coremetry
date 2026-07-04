package ldap

import (
	"testing"

	goldap "github.com/go-ldap/ldap/v3"
)

// v0.8.266 — LDAP directory identity (operator: "organizasyon, ad
// soyad, ekip bilgisi de gelsin"). Pins the AD→inetOrgPerson
// fallback order (department→ou, company→o) and whitespace
// handling so a mapping edit can't silently start reading the
// wrong attribute family.
func TestDirText(t *testing.T) {
	entry := func(vals map[string]string) *goldap.Entry {
		e := &goldap.Entry{}
		for name, v := range vals {
			e.Attributes = append(e.Attributes,
				&goldap.EntryAttribute{Name: name, Values: []string{v}})
		}
		return e
	}

	if got := dirText(entry(map[string]string{"department": "Fraud Ops", "ou": "Legacy"}), "department", "ou"); got != "Fraud Ops" {
		t.Fatalf("department must win over ou, got %q", got)
	}
	if got := dirText(entry(map[string]string{"ou": "Payments"}), "department", "ou"); got != "Payments" {
		t.Fatalf("ou must fill in when department is absent, got %q", got)
	}
	// Whitespace-only counts as absent — some directories pad values.
	if got := dirText(entry(map[string]string{"department": "   ", "ou": "Core"}), "department", "ou"); got != "Core" {
		t.Fatalf("whitespace-only department must fall through, got %q", got)
	}
	if got := dirText(entry(map[string]string{"company": "  Acme Bank  "}), "company", "o"); got != "Acme Bank" {
		t.Fatalf("values must be trimmed, got %q", got)
	}
	if got := dirText(entry(nil), "company", "o"); got != "" {
		t.Fatalf("no attributes → empty, got %q", got)
	}
}
