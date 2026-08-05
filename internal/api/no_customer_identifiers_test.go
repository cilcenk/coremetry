package api

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// v0.9.698 — MÜŞTERİ/KURUM ADI DEPODA GEÇMEZ.
//
// Operatörün sert kısıtı: kurum adı koda, yoruma, commit mesajına,
// dokümana YAZILMAZ. Buna rağmen izlenen dosyalarda 4 dosyada 6 yerde
// birikmişti — biri v0.9.660'ta bugün eklenmiş bir yorumdaki örnek
// e-posta adresiydi. Kural insan dikkatiyle korunuyordu; korunmadı.
//
// NEDEN BASE64: kuralın kendisi "bu ad depoda geçmesin" diyor. Yasaklı
// jetonu testin içine DÜZ YAZMAK ihlalin ta kendisi olurdu — kapı,
// koruduğu kuralı çiğneyemez.
//
// Yan fayda: kodlanmış jeton, bu dosyanın kendi metniyle eşleşmiyor.
// Bu kod tabanında "test kendi literalini kod sanıyor" tuzağı bugün
// beş kez ısırdı (make audit CHECK 7 dahil); burada yapısal olarak
// imkânsız.
//
// YENİ MÜŞTERİ EKLERKEN: adı base64'leyip listeye ekle, düz yazma.
//
//	printf '%s' 'AdBurada' | base64
var forbiddenIdentifiersB64 = []string{
	"QWtiYW5r", // v0.8.185/186 olay yorumlarında + v0.9.660 örnek e-postada geçiyordu
}

// Taramadan muaf dizinler: üretilmiş / bağımlılık / ikili içerik.
var scanSkipDirs = map[string]bool{
	".git": true, "node_modules": true, "dist": true, "build": true,
	"vendor": true, ".next": true, "coverage": true,
}

// Yalnız METİN uzantıları taranıyor. İkili dosyada (imaj, dmg) eşleşme
// arayıp yanlış pozitif üretmenin anlamı yok.
var scanExts = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".css": true, ".html": true, ".md": true, ".yaml": true, ".yml": true,
	".json": true, ".sh": true, ".sql": true, ".tpl": true, ".txt": true,
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// internal/api → depo kökü iki üst dizin.
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("depo kökü bulunamadı (%s): %v", root, err)
	}
	return root
}

func TestNoCustomerIdentifiersInRepo(t *testing.T) {
	var needles []string
	for _, enc := range forbiddenIdentifiersB64 {
		b, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			t.Fatalf("yasaklı jeton çözülemedi: %v", err)
		}
		needles = append(needles, strings.ToLower(string(b)))
	}

	root := repoRoot(t)

	// YALNIZ İZLENEN DOSYALAR. Kural "depoda geçmesin" diyor; operatörün
	// yerel çalışma dosyaları (indirilmiş prod manifest'leri, notlar)
	// depoda DEĞİL ve orada kurum adının bulunması meşru.
	//
	// İlk yazımım tüm ağacı yürüyordu ve tam da bu meşru dosyalarda
	// kırmızıya döndü. Yanlış yere kızan bir kapı, görmezden gelinen bir
	// kapıdır — kapsamı daraltmak testi zayıflatmıyor, DOĞRU yere
	// nişanlıyor.
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Skipf("git ls-files çalışmadı, kapsam belirlenemiyor: %v", err)
	}
	tracked := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	if len(tracked) < 100 {
		t.Fatalf("izlenen dosya sayısı şüpheli düşük (%d) — kapı boşa taramış olabilir", len(tracked))
	}

	var hits []string
	for _, rel := range tracked {
		if rel == "" {
			continue
		}
		if !scanExts[strings.ToLower(filepath.Ext(rel))] {
			continue
		}
		if strings.HasPrefix(rel, "scratchpad/") {
			continue
		}
		if top := strings.SplitN(rel, string(filepath.Separator), 2)[0]; scanSkipDirs[top] {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		lower := strings.ToLower(string(b))
		for _, n := range needles {
			if strings.Contains(lower, n) {
				// EŞLEŞEN METNİ YAZDIRMA — yalnız konum. Test çıktısı
				// da bir sızıntı yüzeyidir.
				hits = append(hits, rel)
				break
			}
		}
	}

	if len(hits) > 0 {
		t.Errorf("kurum/müşteri adı %d dosyada geçiyor (içerik bilinçli yazdırılmadı): %s",
			len(hits), strings.Join(hits, ", "))
	}
}

// Kapının GERÇEKTEN aradığını kanıtlar: jeton çözülüyor ve boş değil.
// Bu olmadan bozuk bir base64 sessizce "hiçbir şey arama"ya dönerdi ve
// test sonsuza kadar yeşil kalırdı — kapının en sinsi ölüm şekli.
func TestForbiddenIdentifierListIsLive(t *testing.T) {
	if len(forbiddenIdentifiersB64) == 0 {
		t.Fatal("yasaklı liste boş — kapı hiçbir şey korumuyor")
	}
	for _, enc := range forbiddenIdentifiersB64 {
		b, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			t.Fatalf("çözülemedi: %v", err)
		}
		if len(strings.TrimSpace(string(b))) < 3 {
			t.Fatalf("çözülen jeton çok kısa (%d) — yanlış pozitif üretir", len(b))
		}
	}
}
