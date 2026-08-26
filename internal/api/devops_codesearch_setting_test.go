package api

import (
	"strings"
	"testing"
)

// devops_codesearch_setting_test.go — v0.10.75.
//
// v0.10.74 organizasyon kod aramasını ekledi ama ayar YALNIZ blob'da
// yaşıyordu: operatör onu ekrandan açamıyordu. Açılamayan bir ayar,
// olmayan bir özelliktir.
//
// ⚠ Ayarın UÇTAN UCA gitmesi gerekiyor — DTO → merge → snapshot → ekran.
// Zincirin bir halkası düşerse kutu görünür ama hiçbir şey yapmaz; ve
// bunu hiçbir şey söylemez.

func TestCodeSearchSettingRoundTrips(t *testing.T) {
	src := readSourceFile(t, "devops_handlers.go")

	for _, must := range []struct{ what, needle string }{
		{"girdi DTO'su", `CodeSearch bool `},
		{"merge (kaydetme)", "CodeSearch:         in.CodeSearch,"},
		{"GET (geri okuma)", `"codeSearch":         snap.CodeSearch,`},
	} {
		if !strings.Contains(src, must.needle) {
			t.Errorf("%s eksik — ayar zinciri kopuk, kutu görünür ama etkisiz olur",
				must.what)
		}
	}
}

// TestCodeSearchDefaultsOff — VARSAYILAN KAPALI KALMALI.
//
// Arama yeni bir ağ çağrısı, ayrı bir uzantı ve ek bir PAT kapsamı
// istiyor; API şekli yalnız operatörün örneğinde doğrulanabilir. Sessizce
// açık gelmesi, doğrulanmamış bir ucu prod kod yoluna sokardı.
func TestCodeSearchDefaultsOff(t *testing.T) {
	src := readRepoFile(t, "../devops/client.go")
	// Sıfır değer false; ayrıca hiçbir yerde "= true" ile öntanımlanmamalı.
	if strings.Contains(flatWS(src), "CodeSearch: true") {
		t.Error("kod araması bir yerde VARSAYILAN AÇIK yapılmış")
	}
	dev := readRepoFile(t, "../devops/code.go")
	if !strings.Contains(dev, "if cfg.CodeSearch &&") {
		t.Error("arama ayara bağlı değil — kapalıyken de koşar")
	}
}

// TestSettingsUIExposesTheToggle — AÇILAMAYAN AYAR YOK HÜKMÜNDE.
func TestSettingsUIExposesTheToggle(t *testing.T) {
	ui := readRepoFile(t, "../../frontend/src/pages/settings/DevOpsTab.tsx")
	if !strings.Contains(ui, "checked={codeSearch}") {
		t.Error("Ayarlar ekranında kutu yok — operatör aramayı açamaz")
	}
	// ⚠ İDDİA HEDEFLİ. İlk yazımı yalnız "codeSearch," arıyordu ve o
	// dizge `const [codeSearch, setCodeSearch]` DESTRUCTURING'inde de
	// geçiyor — mutasyon denemesinde gövdeden çıkardım ve test YEŞİL
	// kaldı. Gevşek desen yanlış yeri ölçüyordu.
	//
	// Ölçülmesi gereken şey: değerin SUNUCUYA GİDEN gövdede olması.
	if !strings.Contains(flatWS(ui), "insecureSkipVerify: insecure, codeSearch,") {
		t.Error("kutu istek gövdesinde YOK — açılır ama sunucuya gitmez")
	}
	// Ne gerektirdiği ekranda YAZILI olmalı: uzantı yoksa operatör
	// "bozuk" diye okur.
	for _, must := range []string{"Code Search", "Code (Read)"} {
		if !strings.Contains(ui, must) {
			t.Errorf("ekran %q gereksinimini söylemiyor — eksikse operatör "+
				"özelliği bozuk sanır", must)
		}
	}
}
