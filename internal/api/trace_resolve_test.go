package api

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// v0.9.632 — operator-reported (prod, v0.9.631): Tempo fallback bir
// trace'i bulup 62 span'lik waterfall'ı çizdiği hâlde "Explain trace"
// HTTP 404 "trace not found" veriyordu.
//
// Sebep: /api/traces/{id} handler'ı CH ıskalayınca Tempo'ya düşüyordu,
// buildTraceExplainInput ise DOĞRUDAN s.store.GetTrace çağırıyordu.
// Aynı boşluk beş çağrı yerindeydi.
//
// Bu testler kuralın TEK YERDE kalmasını çiviliyor: bir daha
// s.store.GetTrace'e doğrudan düşen bir trace yüzeyi eklenirse kırmızı
// olur.

// stripGoComments — // ve /* */ yorumlarını atar.
//
// ZORUNLU: bu düzeltmenin açıklamaları `s.store.GetTrace` dizgisini
// ALINTILIYOR (hatanın ne olduğunu anlatmak için). Yorumları sıyırmadan
// tarayan bir test kendi açıklamasıyla eşleşir — bu oturumda üç kez
// yaşandı (Go, TS, SQL taramalarında).
func stripGoComments(src string) string {
	src = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(src, "")
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestStripGoCommentsDropsQuotedSymbol(t *testing.T) {
	in := "// s.store.GetTrace burada YALNIZ yorumda\nx := 1\n/* s.store.GetTrace */\ny := 2\n"
	got := stripGoComments(in)
	if strings.Contains(got, "GetTrace") {
		t.Fatalf("yorumlar sıyrılmadı: %q", got)
	}
	if !strings.Contains(got, "x := 1") || !strings.Contains(got, "y := 2") {
		t.Fatalf("kod satırları kayboldu: %q", got)
	}
}

// tempo.go MUAF: oradaki iki handler Grafana'nın Tempo datasource'una
// Tempo API'si SUNUYOR. Onları fallback'e bağlamak Tempo'dan Tempo'ya
// düşmek, yani döngü olurdu.
var traceResolveExempt = map[string]string{
	"tempo.go":         "Grafana'ya Tempo API'si sunuyor — fallback döngü olurdu",
	"trace_resolve.go": "kuralın KENDİSİ — CH çağrısı burada olmak zorunda",
}

func TestTraceSurfacesUseSharedResolver(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	direct := regexp.MustCompile(`s\.store\.GetTrace\(`)

	found := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if _, ok := traceResolveExempt[name]; ok {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(stripGoComments(string(raw)), "\n") {
			if direct.MatchString(line) {
				found++
				t.Errorf("%s:%d doğrudan s.store.GetTrace çağırıyor — Tempo fallback'i kaçırır, "+
					"resolveTraceSpans kullanılmalı (trace_resolve.go):\n  %s",
					name, i+1, strings.TrimSpace(line))
			}
		}
	}
	_ = found
}

// Muaf dosya GERÇEKTEN doğrudan çağırıyor olmalı: muafiyet bir gün
// gereksizleşirse (handler değişirse) bunu bilmek isteriz, yoksa liste
// sessizce bayatlar.
func TestExemptFileStillNeedsExemption(t *testing.T) {
	raw, err := os.ReadFile("tempo.go")
	if err != nil {
		t.Skip("tempo.go yok")
	}
	if !strings.Contains(stripGoComments(string(raw)), "s.store.GetTrace(") {
		t.Error("tempo.go artık doğrudan GetTrace çağırmıyor — muafiyet listesinden çıkarılmalı")
	}
}

// Çözümleyicinin sözleşmesi kaynakta okunabilir olmalı: Tempo hatası
// isteği DÜŞÜRMEZ (özgün fallback'in davranışı).
func TestResolverSwallowsTempoErrors(t *testing.T) {
	raw, err := os.ReadFile("trace_resolve.go")
	if err != nil {
		t.Fatal(err)
	}
	body := stripGoComments(string(raw))
	if !strings.Contains(body, "log.Printf") {
		t.Error("Tempo hatası loglanmalı — sessiz yutma yanlış yapılandırmayı görünmez yapar")
	}
	if strings.Contains(body, "return nil, \"\", terr") {
		t.Error("Tempo hatası isteği DÜŞÜRMEMELİ; operatör fallback yokmuş gibi boş sonuç görmeli")
	}
}
