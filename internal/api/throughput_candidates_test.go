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

// v0.9.678 — service_name KOLONU adayları.
//
// Operatörün sorusu ("ingest env kesiyor olabilir mi?") bu boşluğu
// açığa çıkardı. Cevap hayır (ingest birebir yazıyor), ama tam bu
// yüzden metriğin service_name'i EKSİZ olabiliyor: OTel servis adını
// eksiz tutup ortamı ayrı özniteliğe koyuyor. Kolon TAM eşleşme
// yaptığı için etiket tarafındaki regex hilesi burada işlemiyor.
func TestServiceNameCandidatesTriesBothForms(t *testing.T) {
	got := serviceNameCandidates("bsa-chatbot-ai-integration-uat")
	want := []string{"bsa-chatbot-ai-integration-uat", "bsa-chatbot-ai-integration"}
	if len(got) != len(want) {
		t.Fatalf("iki aday bekleniyordu: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("aday %d: %q, beklenen %q", i, got[i], want[i])
		}
	}
}

// Ek YOKSA aynı değeri iki kez sorgulamamalı — boşuna CH turu.
func TestServiceNameCandidatesNoDuplicateWhenNoSuffix(t *testing.T) {
	got := serviceNameCandidates("checkout")
	if len(got) != 1 || got[0] != "checkout" {
		t.Errorf("tek aday olmalı, alınan %v", got)
	}
}

func TestServiceNameCandidatesEmpty(t *testing.T) {
	if got := serviceNameCandidates(""); len(got) != 0 {
		t.Errorf("boş servis için aday olmamalı, alınan %v", got)
	}
}
