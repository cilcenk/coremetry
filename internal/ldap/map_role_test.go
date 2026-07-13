package ldap

import "testing"

// map_role_test.go — v0.8.528: mapRole artık (role, fromGroup) döndürür;
// fromGroup EXPLICIT eşleşmeyi default fallback'ten ayırır (login'in
// manuel-grant koruması buna bağlı).
func TestMapRole(t *testing.T) {
	maps := []GroupRoleMapping{
		{Group: "CN=Admins", Role: "admin"},
		{Group: "CN=Editors", Role: "editor"},
	}
	cases := []struct {
		name      string
		groups    []string
		fallback  string
		wantRole  string
		wantMatch bool
	}{
		{"explicit admin match", []string{"CN=Admins,OU=x"}, "viewer", "admin", true},
		{"highest privilege wins", []string{"CN=Editors", "CN=Admins"}, "viewer", "admin", true},
		{"no match → fallback, fromGroup=false", []string{"CN=Randoms"}, "viewer", "viewer", false},
		{"no groups → fallback", nil, "editor", "editor", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			role, fromGroup := mapRole(c.groups, maps, c.fallback)
			if role != c.wantRole || fromGroup != c.wantMatch {
				t.Fatalf("got (%q,%v), want (%q,%v)", role, fromGroup, c.wantRole, c.wantMatch)
			}
		})
	}
}
