package insight

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestServerLinkPathsAreRegisteredRoutes — KAYITSIZ-ROTA KAPISI (v0.9.1377).
//
// Sunucu-üretimi bir çip yalnız yolu React router'da KAYITLIYSA çalışır.
// Kayıtlı olmayan bir yol 404 vermez — App.tsx'in catch-all'ına düşer
// (`path="*"` → <Navigate to="/" replace />) ve operatörü sessizce ANA
// SAYFAYA atar. Sessiz olduğu için de kimse bildirmez: çipe basılır, bir
// sayfa açılır, yalnız yanlış olanı.
//
// Bu kapı, sınıfın ÜÇÜNCÜ tekrarından sonra yazıldı:
//   • v0.9.1323 — frontend'in stmtDetailHref'i `/slow-queries` yazıyordu,
//     gerçek rota `/databases/slow-queries`. Düzeltildi.
//   • aynı sürüm — yanlış yazım stmtParam.test.ts'te ÇİVİLİYDİ, yani test
//     bug'ı KORUYORDU. Düzeltildi.
//   • v0.9.1377 — Go tarafındaki İKİZ (links.go) 1323'te hiç
//     dokunulmamıştı ve hâlâ `/slow-queries` üretiyordu; links_test.go da
//     onu çivileyerek koruyordu. İki dosya, aynı hata, dört yıl arayla
//     iki dil.
//
// Neden ikiz hayatta kaldı: 1323'ün düzeltmesi frontend'deydi ve links.go'-
// nun şerhi "aynı şekli stmtDetailHref de üretiyor" diyerek hizanın SÜRDÜĞÜNÜ
// iddia ediyordu. İddia 1323'ten sonra yanlıştı ve hiçbir şey onu ölçmüyordu.
// Şerhler hizayı ilan eder; hizayı ancak kapı sağlar.
//
// Kapı KAYNAKTAN türetiyor: hem yol listesi hem rota listesi dosyalardan
// okunuyor, elle yazılmıyor. Elle yazılan bir liste, kaçırdığı yolu ilk
// günden muaf tutardı.
func TestServerLinkPathsAreRegisteredRoutes(t *testing.T) {
	const appTSX = "../../../frontend/src/App.tsx"

	b, err := os.ReadFile(appTSX)
	if err != nil {
		t.Fatalf("%s okunamadı (yapı değiştiyse pini yeniden konumlandır): %v", appTSX, err)
	}
	app := string(b)

	routeRe := regexp.MustCompile(`<Route\s+path="([^"]+)"`)
	registered := map[string]bool{}
	for _, m := range routeRe.FindAllStringSubmatch(app, -1) {
		registered[m[1]] = true
	}
	if len(registered) < 20 {
		// Regex kayarsa küme küçülür ve kapı HER ŞEYİ geçirir; yeşil
		// kalarak. Ölçülen gerçek sayı bugün 60+.
		t.Fatalf("App.tsx'ten yalnız %d rota çıkarıldı — regex kaymış olmalı", len(registered))
	}
	if !registered["*"] {
		t.Fatalf("catch-all rota (`*`) bulunamadı — kapının tehdit modeli bu, yokluğu ölçümü geçersiz kılar")
	}

	// Sunucunun ürettiği yollar: href("/...") çağrılarının ilk argümanı.
	hrefRe := regexp.MustCompile(`href\("(/[^"]*)"`)
	emitted := map[string][]string{}
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("paket dizini okunamadı: %v", err)
	}
	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", name, err)
		}
		for _, m := range hrefRe.FindAllStringSubmatch(string(src), -1) {
			emitted[m[1]] = append(emitted[m[1]], name)
		}
	}
	if len(emitted) == 0 {
		t.Fatal("hiç sunucu-üretimi yol bulunamadı — regex kaymış olmalı, kapı hiçbir şeyi korumuyor")
	}

	var bad []string
	for p, srcs := range emitted {
		if !registered[p] {
			bad = append(bad, p+" ("+strings.Join(srcs, ", ")+")")
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("KAYITSIZ rota üretiliyor — catch-all operatörü ana sayfaya atar:\n  %s",
			strings.Join(bad, "\n  "))
	}
}

// TestCatchAllStillRedirectsHome — kapının TEHDİT MODELİNİ pinler.
//
// Yukarıdaki kapı "kayıtsız yol zararlıdır" varsayımına dayanıyor ve o
// varsayım App.tsx'in catch-all davranışından geliyor. Catch-all bir gün
// gerçek bir 404 sayfasına dönerse zarar DEĞİŞİR (sessiz yön değişimi
// yerine görünür hata) — kapı yine yararlıdır ama şerhleri yanlış olur.
// Bu test o günü görünür kılar.
func TestCatchAllStillRedirectsHome(t *testing.T) {
	b, err := os.ReadFile("../../../frontend/src/App.tsx")
	if err != nil {
		t.Fatalf("App.tsx okunamadı: %v", err)
	}
	body := string(b)
	if !strings.Contains(body, `<Route path="*" element={<Navigate to="/" replace />} />`) {
		t.Error(`catch-all artık ana sayfaya yönlendirmiyor — routes_test.go'nun şerhlerini güncelle ` +
			`(kayıtsız yolun zararı "sessiz yön değişimi" olmaktan çıkmış olabilir)`)
	}
}
