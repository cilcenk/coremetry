// salvage_singleton_test.go — FAZ 1.2'nin YAPISAL kapısı.
//
// Denetim (2026-08-16) kurtarma zincirinin ÜÇ kopyasını saymıştı:
// copilot.go (buffered explain), stream.go (SSE biriktirici) ve Faz
// 1.1'de bilinçli geçici kopya olarak açılan bu paket. Faz 1.2 o sayıyı
// 1'e indiriyor. Kod incelemesi bunu bir kez doğrular; bu test HER
// koşuda doğrular.
//
// Neden gövde/davranış testi yetmiyor: ikinci bir kopya EKLEMEK hiçbir
// davranış testini kırmaz — iki kopya bugün aynı şeyi yapar, sonra
// biri düzeltilir öteki unutulur. 1024 max_tokens paritesi tam olarak
// böyle ~1000 sürüm yaşadı (v0.9.1120).
//
// Tarayıcı bilinçli olarak DAR: yalnız `^func ` çıpası (yorum satırları
// `//` ile başladığı için sayılmaz) ve non-test dosyalar. Önek çakışması
// yok — tam parantezli imza aranıyor, `StripThinkingFoo` eşleşmez.
package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scanNonTestGoFiles — internal/ altındaki tüm non-test .go dosyaları.
// Depo kökündeki scratchpad/ (eski worktree kopyaları) KAPSAM DIŞI:
// orada canlı olmayan kopyalar var ve onları saymak kapıyı sürekli
// kırmızı tutardı.
func scanNonTestGoFiles(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join("..", "..", "..", "internal")
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("internal/ bulunamadı (%v) — kapı yolu bozulmuş, testin kendisi anlamsızlaşır", err)
	}
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(path)] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(out) < 50 {
		// Tarayıcı hiçbir şey bulmazsa test SESSİZCE geçerdi ("0 kopya
		// var" = "1 değil" bile değil). Taban kontrolü o hâli hataya
		// çevirir.
		t.Fatalf("yalnız %d dosya tarandı — tarayıcı bozuk, sonuç anlamsız", len(out))
	}
	return out
}

func TestSalvageChainHasExactlyOneImplementation(t *testing.T) {
	files := scanNonTestGoFiles(t)

	// Her biri zincirin bir parçası; üçü de TEK dosyada (salvage.go)
	// yaşamalı.
	wanted := []string{
		"func StripThinking(",
		"func ThinkingContent(",
		"func SalvageAnswer(",
		// Silinen küçük-harfli ikizler. Geri gelirlerse "thin alias"
		// niyetiyle bile olsa iki yazılış demektir.
		"func stripThinking(",
		"func thinkingContent(",
	}
	for _, sig := range wanted {
		var hits []string
		for path, src := range files {
			for _, line := range strings.Split(src, "\n") {
				if strings.HasPrefix(line, sig) {
					hits = append(hits, path)
					break
				}
			}
		}
		lower := strings.HasPrefix(sig, "func s") || strings.HasPrefix(sig, "func t")
		if lower {
			if len(hits) != 0 {
				t.Errorf("%s hâlâ tanımlı (%v) — küçük-harfli ikizler Faz 1.2'de silindi", sig, hits)
			}
			continue
		}
		if len(hits) != 1 {
			t.Errorf("%s %d yerde tanımlı (%v), want 1", sig, len(hits), hits)
			continue
		}
		if !strings.HasSuffix(hits[0], "internal/ai/provider/salvage.go") {
			t.Errorf("%s beklenmedik dosyada: %s", sig, hits[0])
		}
	}
}

// TestThinkStripIdiomLivesInOneFile — imza adı değiştirilerek ikinci
// bir kopya açılabilir; bu kapı DEYİMİ sayıyor: "son </think> sonrasını
// al". stream.go'nun artımlı SSE kapısı (strings.Index + thinkClose)
// KASITLI olarak ayrı bir mekanizma — akış sırasında tampon içinde
// arıyor, tam metin üstünde değil — ve bu kapıya takılmaz.
func TestThinkStripIdiomLivesInOneFile(t *testing.T) {
	files := scanNonTestGoFiles(t)
	var hits []string
	for path, src := range files {
		if strings.Contains(src, `strings.LastIndex(s, "</think>")`) {
			hits = append(hits, path)
		}
	}
	if len(hits) != 1 || !strings.HasSuffix(hits[0], "internal/ai/provider/salvage.go") {
		t.Errorf("`son </think>` deyimi %d dosyada (%v), want yalnız internal/ai/provider/salvage.go", len(hits), hits)
	}
}

// TestBufferedBuildersAreGone — Faz 1.2'nin silme sözü. Bu üç üretici
// gövde şeklinin üç ayrı yazılışıydı; geri gelmeleri paritenin yeniden
// ayrışabileceği anlamına gelir.
func TestBufferedBuildersAreGone(t *testing.T) {
	files := scanNonTestGoFiles(t)
	for _, name := range []string{
		"explainOpenAIWithUsage",
		"explainAnthropicWithUsage",
		"explainGitHubWithUsage",
		"parseOpenAIChatResponse",
		"parseAnthropicResponse",
	} {
		for path, src := range files {
			if strings.Contains(src, "func "+name+"(") || strings.Contains(src, ") "+name+"(") {
				t.Errorf("%s hâlâ tanımlı: %s", name, path)
			}
		}
	}
}
