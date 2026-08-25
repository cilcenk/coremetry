package api

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// version_chain_test.go — SÜRÜM ZİNCİRİ KAPISI (v0.9.1383).
//
// `/release` skill'i bir sonraki sürümü kaynaktan DEĞİL, git tag'lerinden
// hesaplıyor ve hesabı bir regex'e dayanıyor:
//
//	git tag --sort=-v:refname | grep -E '^v0\.9\.[0-9]+$' | head -1
//
// Bu, depoda mevcut zincir yazımını REGEX OLARAK bekleyen tek
// çalıştırılabilir yer. 1.0 kesildiği gün güncellenmezse kesimden sonraki
// ilk `/release` çağrısı **v0.9.1383** hesaplar — ve skill'in kendi
// "monotonik / tag'i tekrar kullanma" kontrolü YEŞİL kalır, çünkü
// gerçekten yeni bir tag'dir. Yani hata sessiz: sürüm zinciri iki kola
// ayrılır ve kimse bir şey görmez.
//
// Bunu 1.0 hazırlık denetimi (2026-08-25) buldu; prosedürün kendisi bu
// adımı HİÇ içermiyordu. `docs/RELEASE-1.0.md` §1.3 artık içeriyor — ama
// bir dokümanın adımı, koşulmadığında sessizdir. Kapı onu sesli yapıyor.
//
// KAPI: en yeni sürüm tag'i, `/release` skill'inin aradığı desene
// UYMAK ZORUNDA. Zincir v1.0'a geçtiğinde bu test kırmızıya döner ve
// düzeltmesi skill'in regex'ini çevirmektir — yani kapı, atlanan adımı
// kesimden hemen sonra görünür kılar.
//
// Neden `internal/api`: bu paket zaten sürüm sözleşmesinin evi
// (version_override_test.go). Ayrı bir paket açmak, tek testlik bir ev
// kurmak olurdu.
const releaseSkillPath = "../../.claude/skills/release/SKILL.md"

// skillTagPattern — skill'in sonraki-sürüm hesabındaki grep -E deseni.
func skillTagPattern(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(releaseSkillPath)
	if err != nil {
		t.Skipf("release skill okunamadı (%v) — skill'siz checkout'ta kapı kapsam dışı", err)
	}
	m := regexp.MustCompile(`grep -E '(\^v[^']+)'`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatalf("%s içinde sonraki-sürüm grep deseni bulunamadı — skill yeniden yazıldıysa bu kapıyı da güncelle", releaseSkillPath)
	}
	return m[1]
}

// chainOf — bir desenden ya da tag'den "MAJOR.MINOR" zincirini çıkarır.
// `^v1\.0\.[0-9]+$` → "01.00" · `v0.9.1386` → "00.09". Sıfır dolgulu,
// çünkü karşılaştırma dizge üzerinden yapılıyor ve "10" > "9" olmalı.
func chainOf(s string) string {
	m := regexp.MustCompile(`v(?:\\)?\.?(\d+)(?:\\)?\.(\d+)`).FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return fmt.Sprintf("%02s.%02s", m[1], m[2])
}

func TestReleaseSkillRegexMatchesTheLiveTagChain(t *testing.T) {
	pattern := skillTagPattern(t)

	// En yeni sürüm tag'i — sürüm-sıralı, ilk satır.
	out, err := exec.Command("git", "tag", "--sort=-v:refname").Output()
	if err != nil {
		t.Skipf("git tag okunamadı (%v) — tarball checkout'ta kapı kapsam dışı", err)
	}
	var latest string
	for _, ln := range strings.Split(string(out), "\n") {
		ln = strings.TrimSpace(ln)
		// Yalnız vX.Y.Z biçimli sürüm tag'leri; ara etiketler (rc, nightly)
		// zinciri temsil etmez.
		if regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(ln) {
			latest = ln
			break
		}
	}
	if latest == "" {
		t.Skip("sürüm tag'i yok — taze klonda kapı kapsam dışı")
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("skill deseni derlenemedi %q: %v", pattern, err)
	}

	// ⚠ İLK YAZIMIM KESİMİ BLOKLUYORDU (v0.9.1387'de düzeltildi).
	//
	// Kapı "en yeni tag desene uymalı" diyordu. Ama kesim commit'inde
	// desen `^v1\.0\.` olur ve en yeni tag hâlâ `v0.9.X`tir — tag ancak
	// gate'ler geçtikten SONRA atılır. Yani kapı, korumak için yazıldığı
	// kesimi imkânsız kılıyordu: tavuk-yumurta.
	//
	// Doğru değişmez YÖNLÜ: desen zincirin GERİSİNDE kalamaz. İleri
	// hamle (desen v1.0, tag hâlâ v0.9) kesim ANIDIR ve meşrudur; geri
	// kalma (tag v1.0, desen hâlâ v0.9) atlanmış adımdır ve tam da
	// yakalamak istediğim sessiz çatallanma.
	pc, tc := chainOf(pattern), chainOf(latest)
	if pc == "" || tc == "" {
		t.Fatalf("zincir çıkarılamadı: desen=%q tag=%q", pattern, latest)
	}
	if pc > tc {
		t.Logf("desen zinciri (%s) tag zincirinden (%s) İLERİDE — kesim anı, beklenen durum", pc, tc)
		return
	}
	if !re.MatchString(latest) {
		t.Errorf(`SÜRÜM ZİNCİRİ ÇATALLANDI.

  en yeni tag       : %s
  /release deseni   : %s
  desenin yeri      : %s:39

Bir sonraki `+"`/release`"+` çağrısı bu tag'i GÖRMEYECEK ve zincirin ESKİ
kolundan devam edecek — üstelik "yeni bir tag" olduğu için kendi
monotoniklik kontrolünü de geçecek. Düzeltme: skill'deki grep desenini
yeni zincire çevir (docs/RELEASE-1.0.md §1.3).`, latest, pattern, releaseSkillPath)
	}
}

// TestReleaseProcedureNamesTheSkillStep — dokümanın kendisi bu adımı
// TAŞIYOR mu?
//
// Kapı yukarıda sesi veriyor, ama sesi duyan kişinin ne yapacağını
// dokümandan okuması gerek. İlk yazımda bu adım YOKTU ve eksikliği ancak
// bir denetimle görülebildi. Bu test, adımın dokümandan sessizce
// düşmesini engelliyor.
func TestReleaseProcedureNamesTheSkillStep(t *testing.T) {
	b, err := os.ReadFile("../../docs/RELEASE-1.0.md")
	if err != nil {
		t.Skipf("prosedür okunamadı: %v", err)
	}
	body := string(b)
	for _, want := range []string{
		".claude/skills/release/SKILL.md", // hangi dosya
		"CLAUDE.md",                       // zincir yazımının diğer evi
		"vitest",                          // gate zincirinden atlanamayan
	} {
		if !strings.Contains(body, want) {
			t.Errorf("docs/RELEASE-1.0.md artık %q'dan söz etmiyor — kesim günü koşulacak bir reçeteden adım düşmüş", want)
		}
	}
}
