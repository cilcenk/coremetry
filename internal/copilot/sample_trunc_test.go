package copilot

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// sample_trunc_test.go — v0.10.112. ai_calls ÖRNEĞİ BAŞ + SON.
//
// Kodlu system prompt tek başına 4,3 KB; gerçek exception/trace gövdesi
// onlarca KB. Yalnız baştan kesilen örnek, prompt'un SONUNDAKİ maske
// özetini ("[kod: …]") ve ıska işaretini ("[kod alınamadı: …]") hiç
// göstermiyordu — /ai "kod istendi mi, geldi mi"yi okuyamıyordu.
func TestTruncForSampleKeepsTail(t *testing.T) {
	short := "kısa örnek — şğüöçı"
	if got := truncForSample(short); got != short {
		t.Fatalf("kısa örnek değişti: %q", got)
	}
	// Türkçe çok-baytlı karakterlerle uzun gövde; kuyrukta maske özeti.
	body := strings.Repeat("İşlem gövdesi satırı — ğüşöçı. ", 400) // ≈ 14 KB
	tail := "\n\n[kod: core-service/src/A.java:10-70 · 61 satır]"
	got := truncForSample(body + tail)
	if len(got) > 4096 {
		t.Fatalf("örnek tavanı aşıldı: %d", len(got))
	}
	if !strings.HasSuffix(got, tail) {
		t.Fatalf("kuyruk (maske özeti) korunmadı:\n…%s", got[len(got)-120:])
	}
	if !strings.Contains(got, "…[örnek kırpıldı: ") || !strings.Contains(got, " bayt atlandı]…") {
		t.Fatalf("kırpma işareti yok:\n%s", got[:200])
	}
	if !utf8.ValidString(got) {
		t.Fatal("kesim rune sınırına oturmadı — geçersiz UTF-8")
	}
	if !strings.HasPrefix(got, body[:100]) {
		t.Fatal("baş korunmadı")
	}
}
