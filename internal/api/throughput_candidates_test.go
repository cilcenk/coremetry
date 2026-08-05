package api

import "testing"

// v0.9.671 — AYNI HATA BİR GÜNDE İKİ KEZ.
//
// Boş bir girdi, aday döngüsünden ÖNCE varsayılana çevrilirse döngü tek
// elemana iner ve liste hiç denenmez. Özellik çalışıyor görünür, yalnız
// hiçbir şey bulamaz — sessiz.
//   • v0.9.668, metrik adı: operatörün ekran görüntüsü kanıtladı —
//     "Denenen adlar: <tek ad>", oysa aradığı ad önerilerin içindeydi.
//   • v0.9.671'i yazarken aynısını jobLabel'da tekrarladım.

func TestThroughputMetricCandidatesTriesFullListWhenUnspecified(t *testing.T) {
	got := throughputMetricCandidates("", "configured.metric")
	if len(got) < 3 {
		t.Fatalf("boş girdide TÜM adaylar denenmeli, alınan %v", got)
	}
	if got[0] != "configured.metric" {
		t.Errorf("ayardaki ad ÖNCE denenmeli, alınan %q", got[0])
	}
	// Ayardaki ad listede de varsa iki kez denenmemeli.
	seen := map[string]int{}
	for _, c := range got {
		seen[c]++
	}
	for c, n := range seen {
		if n > 1 {
			t.Errorf("%q %d kez — tekrar eden aday", c, n)
		}
	}
}

// Operatör açıkça bir ad verdiyse BAŞKASINI denememeli: "?metric=X"
// diye sorup Y'nin grafiğini almak, sessizce yanlış veri göstermektir.
func TestThroughputMetricCandidatesHonoursExplicit(t *testing.T) {
	got := throughputMetricCandidates("my.metric", "configured.metric")
	if len(got) != 1 || got[0] != "my.metric" {
		t.Errorf("açık istek tek aday olmalı, alınan %v", got)
	}
}

func TestIdentityLabelCandidatesTriesFullListWhenUnspecified(t *testing.T) {
	got := identityLabelCandidates("")
	if len(got) < 2 {
		t.Fatalf("boş girdide TÜM etiketler denenmeli, alınan %v", got)
	}
	// Operatörün kurulumunda kimlik `name` etiketinde (v0.9.671).
	var hasName bool
	for _, l := range got {
		if l == "name" {
			hasName = true
		}
	}
	if !hasName {
		t.Error("`name` aday olmalı — operatörün kurulumunda kimlik orada")
	}
}

func TestIdentityLabelCandidatesHonoursExplicit(t *testing.T) {
	got := identityLabelCandidates("kubernetes_job")
	if len(got) != 1 || got[0] != "kubernetes_job" {
		t.Errorf("açık etiket tek aday olmalı, alınan %v", got)
	}
}
