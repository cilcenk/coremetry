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

// v0.9.679 — service_name KOLONU denemeleri.
//
// Operatörün SQL çıktısı: metric_points'te 1494 servis, HEPSİ EKSİZ —
// oysa servis listesi trace'ten gelen EKLİ adı gösteriyor. Eşleşme
// ancak eksiz adla kurulabiliyor.
//
// Ama eksiz ad ORTAMLARI BİRLEŞTİRİR: bsa-deposit-uat ve
// bsa-deposit-prod ikisi de bsa-deposit'e iner. Sayı makul göründüğü
// için kimse fark etmez — bu yüzden önce ortamla kısıtlanıyor.
func TestServiceNameAttemptsOrderAndEnvConstraint(t *testing.T) {
	got := serviceNameAttempts("bsa-deposit-uat")
	if len(got) != 4 {
		t.Fatalf("4 deneme bekleniyordu (tam + 2 ortam yazımı + kısıtsız), alınan %d: %v", len(got), got)
	}

	// 1) TAM ad, kısıtsız — en güvenli, hiç belirsizlik yok.
	if got[0].Service != "bsa-deposit-uat" || len(got[0].Filters) != 0 || got[0].EnvAmbiguous {
		t.Errorf("ilk deneme tam ad ve kısıtsız olmalı: %+v", got[0])
	}

	// 2-3) EKSİZ ad + ortam kısıtı. İki semconv yazımı da denenmeli:
	// deployment.environment.name (≥1.27) ve deployment.environment.
	wantKeys := []string{
		"resource.deployment.environment.name",
		"resource.deployment.environment",
	}
	for i, wk := range wantKeys {
		a := got[i+1]
		if a.Service != "bsa-deposit" {
			t.Errorf("deneme %d eksiz ad olmalı: %q", i+1, a.Service)
		}
		if len(a.Filters) != 1 || a.Filters[0].Key != wk {
			t.Errorf("deneme %d ortam anahtarı %q olmalı: %+v", i+1, wk, a.Filters)
		}
		if len(a.Filters[0].Values) != 1 || a.Filters[0].Values[0] != "uat" {
			t.Errorf("deneme %d ortam değeri 'uat' olmalı: %v", i+1, a.Filters[0].Values)
		}
		if a.EnvAmbiguous {
			t.Errorf("deneme %d ortamla KISITLI, belirsiz olmamalı", i+1)
		}
	}

	// 4) Son çare: kısıtsız eksiz ad — ama BELİRSİZ İŞARETLİ.
	last := got[3]
	if last.Service != "bsa-deposit" || len(last.Filters) != 0 {
		t.Errorf("son deneme kısıtsız eksiz ad olmalı: %+v", last)
	}
	if !last.EnvAmbiguous {
		t.Error("son deneme EnvAmbiguous OLMALI — yoksa ortam karışması sessiz kalır")
	}
}

// Ek yoksa tek deneme: eksiz ad = tam ad, ikinci CH turu boşuna.
func TestServiceNameAttemptsNoSuffix(t *testing.T) {
	got := serviceNameAttempts("checkout")
	if len(got) != 1 || got[0].Service != "checkout" || got[0].EnvAmbiguous {
		t.Errorf("tek, kısıtsız, belirsiz-olmayan deneme bekleniyordu: %+v", got)
	}
}

func TestServiceNameAttemptsEmpty(t *testing.T) {
	if got := serviceNameAttempts(""); len(got) != 0 {
		t.Errorf("boş servis için deneme olmamalı: %v", got)
	}
}

// Etiket operatöre HANGİ yolun tuttuğunu söylemeli — belirsiz eşleşme
// özellikle görünür olmalı.
func TestSvcAttemptLabel(t *testing.T) {
	all := serviceNameAttempts("bsa-deposit-uat")
	if l := all[0].Label(); l != "service_name=bsa-deposit-uat" {
		t.Errorf("tam ad etiketi: %q", l)
	}
	if l := all[1].Label(); l != "service_name=bsa-deposit +resource.deployment.environment.name" {
		t.Errorf("ortam kısıtlı etiket: %q", l)
	}
	if l := all[3].Label(); l != "service_name=bsa-deposit (ortam kısıtsız)" {
		t.Errorf("belirsiz etiket ortam kısıtsızlığını SÖYLEMELİ: %q", l)
	}
}
