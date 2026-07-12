package ldap

import (
	"strings"
	"testing"
)

// resolve_user_filter_test.go — v0.8.524 regression (operator-reported:
// "Inspect sanki bind user'ına bakıyor, yazdığım kullanıcıyı bulmuyor").
// InspectUser ham ReplaceAll kullanıyordu: {{username}} placeholder'sız
// (Dex-stili) filtrede kullanıcı adı filtreye hiç girmiyor, geniş
// filtrenin İLK kaydı dönüyordu. Artık login'in resolveUserFilter'ı
// kullanılıyor — bu tablo iki yolun ortak sözleşmesini sabitler.
func TestResolveUserFilter(t *testing.T) {
	cases := []struct {
		name, raw, user string
		mustContain     []string
	}{
		{
			"placeholder'lı filtre — birebir substitüsyon",
			"(&(objectclass=person)(sAMAccountName={{username}}))", "n123",
			[]string{"(sAMAccountName=n123)"},
		},
		{
			"placeholder'SIZ ön-filtre — kullanıcı clause'u AND'lenir (bildirilen bug)",
			"(objectclass=person)", "n123",
			[]string{"(&(objectclass=person)", "(sAMAccountName=n123)", "(userPrincipalName=n123)", "(mail=n123)"},
		},
		{
			"boş filtre — yalnız kullanıcı clause'u",
			"", "n123",
			[]string{"(|(sAMAccountName=n123)"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveUserFilter(c.raw, c.user)
			for _, m := range c.mustContain {
				if !strings.Contains(got, m) {
					t.Fatalf("%q filtrede yok: %s", m, got)
				}
			}
		})
	}
}
