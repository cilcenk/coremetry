// v0.9.557 regresyon testi — serveCached closure'ları VERİLEN ctx'i
// kullanmalı, isteğinkini değil.
//
// Bug sınıfı: serveCached'in fn'i İKİ yolda koşar.
//
//	miss    → ctx = r.Context()          (istek canlı)
//	SWR     → ctx = context.Background() (cache.go:349; istek BİTTİ)
//
// Closure verilen ctx'i yok sayıp r.Context()'e kapanırsa, arka plan
// tazeleme her seferinde context.Canceled ile düşer. Belirti sinsi:
// bayat içerik sunulmaya devam eder ve sert pencere dolunca operatör
// tam bir bekleme öder — yani "hızlı ama bayat" ile "yavaş" arasında
// salınır, hiç düzgün tazelenmez.
//
// Bu hata BİR KEZ düzeltildi (v0.8.319, cache.go:351-354) ama düzeltme
// serveCached'in KENDİSİNE yapılmıştı; ctx'i kullanmayan çağrı
// noktaları kapsam dışında kaldı ve rootcause.go:424 öyle kaldı.
// Yorum yerine tarama: kural artık test edilebilir.
package api

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// reqCtxInClosure — closure gövdesinde isteğe kapanan çağrılar:
// r.Context() doğrudan, ya da `r` alan bir sarmalayıcıya geçirilmesi
// (copilotExplain(r, ...) gibi).
var reqCtxInClosure = regexp.MustCompile(`r\.Context\(\)|\(r,\s`)

func TestServeCachedClosuresUseGivenContext(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	var ihlaller []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			if !strings.Contains(l, "s.serveCached(") {
				continue
			}
			// Closure gövdesini süslü parantez sayarak yürü.
			depth, started := 0, false
			for j := i; j < len(lines) && j < i+60; j++ {
				seg := lines[j]
				if strings.Contains(seg, "func(ctx") {
					started = true
				}
				if !started {
					continue
				}
				if j > i && reqCtxInClosure.MatchString(seg) {
					ihlaller = append(ihlaller, fmt.Sprintf("%s:%d → %s", f, j+1, strings.TrimSpace(seg)))
				}
				depth += strings.Count(seg, "{") - strings.Count(seg, "}")
				if depth <= 0 && j > i {
					break
				}
			}
		}
	}

	if len(ihlaller) > 0 {
		t.Errorf("serveCached closure'ı isteğin context'ine kapanıyor:\n  %s\n\n"+
			"SWR arka plan tazelemesi taze bir context.Background() ile koşar "+
			"(cache.go:349) çünkü istek çoktan bitmiştir. Closure hâlâ ölü "+
			"r.Context()'i kullanırsa HER tazeleme context.Canceled ile düşer "+
			"ve içerik sert pencere dolana kadar bayat kalır.\n"+
			"Çözüm: fn'e verilen ctx'i kullan (gerekiyorsa kimliği closure "+
			"DIŞINDA yakalayıp copilot.WithMeta ile ctx'e koy).",
			strings.Join(ihlaller, "\n  "))
	}
}
