package devops

import (
	"os"
	"strings"
	"testing"
)

// dryrun_derived_test.go — v0.10.58.
//
// Operatör bildirdi: "Kodu incele dediğimde doğru repoyu bulmuyor."
// Ekranda ise iki YEŞİL TİK duruyordu:
//
//	✓ Depo adı   callcenter-profile (kaynak: konvansiyon)
//	✓ Proje      BSA (kaynak: konvansiyon)
//	✗ Branş      http 401
//
// ⚠ O iki tik YANILTICI. İkisi de ResolveRepo'nun ürettiği saf birer
// TAHMİN — fonksiyon ağa hiç çıkmıyor, DevOps'a hiçbir şey sorulmuyor.
// İlk gerçek çağrı bir sonraki adımda. Yeşil tik operatöre "doğrulandı"
// der; doğrulanmamış bir tahmine yeşil tik vermek, tam da bu ekranın
// cevaplaması gereken soruyu ("depo doğru mu") cevaplamış gibi yapmaktır.
//
// Bu, deponun imza kusur sınıfı: ölçülmemiş bir şeyi ölçülmüş gibi
// sunmak.

func TestDerivedStepsAreNotMarkedVerified(t *testing.T) {
	src := readDevopsSource(t, "resolve_dryrun.go")

	// Depo ve proje adımları addDerived kullanmalı.
	for _, step := range []string{`"Depo adı"`, `"Proje"`} {
		i := strings.Index(src, "out.addDerived(DryRunStepRepo, "+step)
		if step == `"Proje"` {
			i = strings.Index(src, "out.addDerived(DryRunStepProject, "+step)
		}
		if i < 0 {
			t.Errorf("%s adımı hâlâ doğrulanmış gibi işaretleniyor — DevOps'a "+
				"sorulmadan üretilen bir tahmine yeşil tik veriliyor", step)
		}
	}

	// addDerived, "sorulmadı" ifadesini metne KENDİ koymalı: rozet
	// kaybolsa bile cümle kalsın.
	if !strings.Contains(src, "türetildi, DevOps'a sorulmadı") {
		t.Error("türetilmiş adım metninde 'sorulmadı' ifadesi yok — rozete " +
			"bağlı tek bir işaret, rozet değişince sessizce kaybolur")
	}
	// Derived alanı zarfta taşınmalı.
	if !strings.Contains(src, `json:"derived,omitempty"`) {
		t.Error("Derived zarfta taşınmıyor — arayüz ayırt edemez")
	}
}

// TestPinStepSaysWhereToSetIt — operatörün SORDUĞU şey.
//
// Operatör: "alternatif olarak service catalog'a gir, url'i varsa oradan
// da bakabiliriz." Yetenek ZATEN vardı (ResolveRepo tam URL kabul ediyor,
// _git/ segmentini çözüyor, projeyi de URL'den alıyor) ama ekran nereye
// yazılacağını hiç söylemiyordu.
func TestPinStepSaysWhereToSetIt(t *testing.T) {
	src := readDevopsSource(t, "resolve_dryrun.go")
	for _, must := range []string{"servis kataloğu", "Repository", "tam DevOps linki"} {
		if !strings.Contains(src, must) {
			t.Errorf("pin yok mesajı %q demiyor — operatör pini nereye yazacağını "+
				"ekrandan öğrenemiyor", must)
		}
	}
}

// TestFrontendRendersThreeStates — rozet ÜÇ durumu ayırt etmeli.
func TestFrontendRendersThreeStates(t *testing.T) {
	b, err := os.ReadFile("../../frontend/src/pages/settings/DevOpsTab.tsx")
	if err != nil {
		t.Fatalf("DevOpsTab okunamadı: %v", err)
	}
	src := strings.Join(strings.Fields(string(b)), " ")
	if !strings.Contains(src, "st.derived ? 'info' : st.ok ? 'success' : 'danger'") {
		t.Error("rozet üç durumu ayırt etmiyor — türetilmiş adım yine yeşil çıkar")
	}
}

func readDevopsSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("%s okunamadı: %v", name, err)
	}
	return string(b)
}
