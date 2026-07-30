package chstore

import "testing"

// team_aliases_test.go — v0.9.427 pinleri: LDAP↔telemetri takım adı
// eşlemesi. Türkçe İ katlaması (ToLower'ın combining-dot artığı) ve
// alias çözümü; boş tablo = eski case-insensitive davranış.
func TestTeamAliases(t *testing.T) {
	ta := TeamAliases{Aliases: map[string]string{
		"dijitalsy":  "SY-Dijital Bankacılık",
		"avengersy":  "SY-Krediler ve Sigorta",
		"SY-KREDİLER VE SİGORTA": "SY-Krediler ve Sigorta", // kendi-kendine alias zararsız
	}}

	cases := []struct {
		a, b string
		want bool
	}{
		// Operatörün gerçek senaryoları:
		{"SY-Dijital Bankacılık", "dijitalsy", true},
		{"dijitalsy", "SY-DİJİTAL BANKACILIK", true}, // Türkçe İ katlaması
		{"avengersy", "SY-Krediler ve Sigorta", true},
		{"AvengerSY", "sy-krediler ve sigorta", true},
		// Alias'sız adlar: normalizasyonlu eşitlik (eski EqualFold kapsanır).
		{"SY-Ödemeler", "sy-ödemeler", true},
		{"  SY-Ödemeler ", "SY-Ödemeler", true},
		// Farklı takımlar eşleşmez.
		{"dijitalsy", "avengersy", false},
		{"SY-Dijital Bankacılık", "SY-Krediler ve Sigorta", false},
		// Boş ad asla eşleşmez (boş-boş dahil — filtre semantiği).
		{"", "", false},
		{"", "dijitalsy", false},
	}
	for _, c := range cases {
		if got := ta.TeamEqual(c.a, c.b); got != c.want {
			t.Errorf("TeamEqual(%q, %q) = %v, want %v (canon: %q vs %q)",
				c.a, c.b, got, c.want, ta.CanonTeam(c.a), ta.CanonTeam(c.b))
		}
	}

	// Boş tablo → düz normalize eşitlik.
	var empty TeamAliases
	if !empty.TeamEqual("TeamA", "teama") || empty.TeamEqual("TeamA", "TeamB") {
		t.Errorf("boş tablo davranışı bozuk")
	}
}
