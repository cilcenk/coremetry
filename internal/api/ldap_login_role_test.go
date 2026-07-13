package api

import (
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// ldap_login_role_test.go — v0.8.528 regression (operator-reported prod
// bug): an LDAP user manually promoted to admin via the Users page
// dropped back to viewer on the NEXT LDAP login, because the login path
// re-applied the group-mapping role every time — and when no group
// matched, that role was the DefaultRole (viewer) fallback, silently
// clobbering the manual grant. Fix: only refresh from AD when an
// EXPLICIT group mapping matched (roleFromGroup); otherwise preserve the
// existing role.
func TestResolveLdapLoginRole(t *testing.T) {
	ldap := func(role string) *chstore.User { return &chstore.User{AuthProvider: "ldap", Role: role} }
	local := func(role string) *chstore.User { return &chstore.User{AuthProvider: "local", Role: role} }

	cases := []struct {
		name         string
		existing     *chstore.User
		groupRole    string
		fromGroup    bool
		want         string
	}{
		{
			"THE BUG: LDAP user manually admin, no group match → keep admin",
			ldap("admin"), "viewer", false, "admin",
		},
		{
			"LDAP user in admin group → promote to admin",
			ldap("viewer"), "admin", true, "admin",
		},
		{
			"LDAP user removed from admin group (explicit viewer map) → demote",
			ldap("admin"), "viewer", true, "viewer",
		},
		{
			"first-time login, admin group → admin",
			nil, "admin", true, "admin",
		},
		{
			"first-time login, no group match, empty fallback → viewer (guard)",
			nil, "", false, "viewer",
		},
		{
			"local admin converting to LDAP → keep admin (manual pin)",
			local("admin"), "viewer", false, "admin",
		},
		{
			"LDAP user manually editor, no group match → keep editor",
			ldap("editor"), "viewer", false, "editor",
		},
		{
			"invalid group role, no existing → normalised to viewer",
			nil, "superuser", true, "viewer",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveLdapLoginRole(c.existing, c.groupRole, c.fromGroup); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
