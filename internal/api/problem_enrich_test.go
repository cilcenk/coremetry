// v0.9.553 regresyon testi — deploy+öncelik zincirinin ayrışmaması.
//
// Operatör raporu: "Bazen P1 alertleri karıştırıyor ya da ben Problems
// sekmesinde chatte yazanları göremiyorum, orada da conflict var."
//
// Kök sebep: "P1" bir CH kolonu değil, okuma anı hesabı. Hesabın
// kritik kolu RecentDeploy'a bakıyor; deploy zenginleştirmesi
// koşmamışsa aynı satır P2 oluyor. Sayfa yolları ikisini sırayla
// çağırıyordu, sohbet yolları yalnız priority'yi — sonuç: taze deploy
// sonrası kritik problem sayfada P1, sohbette P2.
//
// Değişmez kural api.go'da YORUM olarak yazılıydı ("the deploys enrich
// must run before the priority enrich"). Yorum derlenmez; beş çağrı
// noktası onu ihlal etti. Bu test kuralı DERLENEBİLİR hale getirmenin
// tamamlayıcısı: kaynak taraması, birinin yeniden yalın
// EnrichProblemsWithPriority çağırmasını yakalar.
package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoBareePriorityEnrich — problem listesi gösteren hiçbir yol
// EnrichProblemsWithPriority'yi TEK BAŞINA çağırmamalı.
//
// Tek meşru istisna problem_enrich.go: zincirin kendisi orada tanımlı.
func TestNoBarePriorityEnrich(t *testing.T) {
	const helperFile = "problem_enrich.go"
	const bare = "chstore.EnrichProblemsWithPriority("

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("dizin okunamadı: %v", err)
	}

	var ihlaller []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if name == helperFile {
			continue // zincirin tanımlandığı yer
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", name, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, bare) {
				ihlaller = append(ihlaller, fmt.Sprintf("%s:%d", name, i+1))
			}
		}
	}

	if len(ihlaller) > 0 {
		t.Errorf("Yalın EnrichProblemsWithPriority çağrısı bulundu: %v\n\n"+
			"Bu, v0.9.553'te düzeltilen hatanın ta kendisi: deploy\n"+
			"zenginleştirmesi koşmadan hesaplanan öncelik, taze deploy\n"+
			"sonrası kritik problemleri P1 yerine P2 gösterir ve o yüzey\n"+
			"Problems sayfasıyla ÇELİŞİR. Bunun yerine\n"+
			"s.enrichProblemsForRead(ctx, probs) kullan — sıra içeride sabit.",
			ihlaller)
	}
}

// TestEnrichHelperDoesBothSteps — yardımcının İKİ adımı da yaptığını
// sabitler. Birinin deploy adımını "pahalı" diye çıkarması, hatayı
// tek satırda geri getirirdi ve yukarıdaki tarama bunu göremezdi
// (çağrı noktaları hâlâ doğru fonksiyonu çağırıyor olurdu).
func TestEnrichHelperDoesBothSteps(t *testing.T) {
	b, err := os.ReadFile("problem_enrich.go")
	if err != nil {
		t.Fatalf("problem_enrich.go okunamadı: %v", err)
	}
	src := string(b)
	for _, must := range []string{
		"EnrichProblemsWithDeploys(",
		"EnrichProblemsWithPriority(",
	} {
		if !strings.Contains(src, must) {
			t.Errorf("%s kaybolmuş — öncelik hesabı RecentDeploy'a bağlı, "+
				"deploy adımı olmadan P1'ler P2 görünür", must)
		}
	}
	// Sıra da önemli: deploy ÖNCE gelmeli.
	iDeploy := strings.Index(src, "EnrichProblemsWithDeploys(")
	iPrio := strings.Index(src, "EnrichProblemsWithPriority(")
	if iDeploy > iPrio {
		t.Error("sıra ters: öncelik hesabı deploy zenginleştirmesinden ÖNCE " +
			"koşuyor — RecentDeploy henüz nil olduğu için postDeploy dalı " +
			"hiç ateşlemez")
	}
}
