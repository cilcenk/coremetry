package ldap

import "testing"

// team_candidates_test.go — v0.8.523. Operatörün AD bileşik şekli:
// "Ad Soyad (Bölüm) - ÜNVAN-EKİP"; ekip adı tire içerir (SY-… gibi).
// Adaylar arasında DOĞRU ekip mutlaka bulunmalı; regex bilmeden UI'dan
// tıklanarak seçilecek.
func TestTeamCandidatesCompositeShape(t *testing.T) {
	val := "Ad Soyad (Teknoloji Örnek Bölümü) - YAZILIM UZMANI-XY-Dijital Ekip"
	got := TeamCandidates(val)
	byExt := map[string]string{}
	for _, c := range got {
		byExt[c.Extracted] = c.Pattern
	}
	// Doğru ekip (tireli ad bütün) aday listesinde OLMALI.
	if _, ok := byExt["XY-Dijital Ekip"]; !ok {
		t.Fatalf("tireli ekip adayı eksik; adaylar: %+v", got)
	}
	// Tam değer ve parantez içi de seçenek olarak dursun.
	if _, ok := byExt[val]; !ok {
		t.Fatal("tam değer adayı eksik")
	}
	if _, ok := byExt["Teknoloji Örnek Bölümü"]; !ok {
		t.Fatal("parantez içi adayı eksik")
	}
	// Dedup: aynı çıktıyı veren iki desen tek aday olmalı.
	seen := map[string]int{}
	for _, c := range got {
		seen[c.Extracted]++
		if seen[c.Extracted] > 1 {
			t.Fatalf("duplicate extracted: %q", c.Extracted)
		}
	}
}

func TestTeamCandidatesEdges(t *testing.T) {
	if got := TeamCandidates("   "); got != nil {
		t.Fatal("boş değer aday üretmemeli")
	}
	// Ayraçsız düz değer → yalnız "tam değer".
	got := TeamCandidates("PlatformEkibi")
	if len(got) != 1 || got[0].Pattern != "" || got[0].Extracted != "PlatformEkibi" {
		t.Fatalf("düz değerde tek 'tam değer' adayı beklenirdi: %+v", got)
	}
}

func TestCandidateEligible(t *testing.T) {
	cases := []struct {
		name string
		vals []string
		want bool
	}{
		{"tek değer", []string{"Ekip A"}, true},
		{"boş liste", nil, false},
		{"çok değerli (memberOf)", []string{"a", "b", "c", "d"}, false},
		{"binary placeholder", []string{"[1234 bytes]"}, false},
		{"aşırı uzun", []string{string(make([]byte, 400))}, false},
	}
	for _, c := range cases {
		if got := candidateEligible(c.vals); got != c.want {
			t.Fatalf("%s: %v beklenirdi", c.name, c.want)
		}
	}
}
