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

// TestEnrichHelperDelegatesToCanonicalChain — api yardımcısının
// zinciri KENDİ yazmadığını, kanonik chstore çağrısına delege
// ettiğini sabitler.
//
// Bu test v0.9.553'te "yardımcı iki adımı da içeriyor mu" diye
// yazılmıştı; v0.9.554'te zincir chstore'a taşınınca düştü. Düşmesi
// DOĞRUYDU: test sözleşmeyi değil uygulamanın YERİNİ sabitlemişti.
// Sözleşmenin kendisi (iki adım, doğru sırada) artık kaynağında
// test ediliyor — chstore/enrich_chain_test.go.
func TestEnrichHelperDelegatesToCanonicalChain(t *testing.T) {
	b, err := os.ReadFile("problem_enrich.go")
	if err != nil {
		t.Fatalf("problem_enrich.go okunamadı: %v", err)
	}
	if !strings.Contains(string(b), "EnrichProblemsForRead(") {
		t.Error("kanonik zincire delege edilmiyor — yardımcı zinciri " +
			"kendi kurarsa MCP tarafıyla ayrışabilir, ki v0.9.554'te " +
			"düzeltilen hata tam olarak buydu")
	}
}
