// v0.9.1281 — kalıcı verdict gövdesinin kırpma sözleşmesi.
//
// Semptom (bu dilimden ÖNCE): verdict metni hiçbir yere yazılmıyordu;
// kalıcılaştırırken tavansız yazmak state tablosunu tek bir kaçak model
// yanıtıyla şişirebilirdi. Tablo FINAL ile okunuyor (kalite paneli,
// LEARN imzaları), yani şişme okuma yolunu da yavaşlatır.
//
// Asıl tuzak KIRPMA NOKTASI: ham `s[:n]` çok baytlı bir karakterin
// ortasından keser ve ClickHouse'a geçersiz UTF-8 yazar. Coremetry'nin
// verdict metni TÜRKÇE (yerel gemma4, Türkçe prompt) — "ı/ş/ğ/ü" 2 bayt,
// yani sınır-ortası kesme istisna değil BEKLENEN durum.
package chstore

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTrimRCAVerdictBody(t *testing.T) {
	// Sınırın TAM üstünde, çok baytlı bir karakterin ORTASINA düşen
	// gövde. 'a' öneki rune başlangıçlarını tek baytlara kaydırıyor:
	// runeler 1, 3, 5… baytlarında başlıyor, dolayısıyla 8192. bayt bir
	// TAKİP baytı. Naif `s[:8192]` burada bozuk UTF-8 üretir.
	multibyte := "a" + strings.Repeat("ş", 6000)
	if utf8.RuneStart(multibyte[rcaVerdictBodyMax]) {
		t.Fatalf("fixture kurulumu bozuk: %d. bayt rune başlangıcı — kesme tuzağı test edilemiyor",
			rcaVerdictBodyMax)
	}

	tests := []struct {
		name     string
		in       string
		wantLen  int  // beklenen bayt uzunluğu (0 = kontrol etme)
		wantCut  bool // "…" eklendi mi
		wantBody string
	}{
		{name: "boş", in: "", wantBody: ""},
		{name: "yalnız boşluk", in: "   \n\t ", wantBody: ""},
		{
			name: "boşluklar kırpılır", in: "  kök neden: db havuzu  ",
			wantBody: "kök neden: db havuzu",
		},
		{
			name: "tavanın altı olduğu gibi",
			in:   strings.Repeat("a", rcaVerdictBodyMax-1),
			// Kırpma YOK: tavanın altı hiç dokunulmadan geçmeli, yoksa
			// her normal verdict sonuna "…" alırdı.
			wantLen: rcaVerdictBodyMax - 1,
		},
		{
			// SINIR: tam tavan kadar. `<=` yerine `<` yazılsaydı burası
			// gereksiz yere kırpılırdı — off-by-one'ın yaşadığı yer.
			name:    "tam tavan dokunulmaz",
			in:      strings.Repeat("a", rcaVerdictBodyMax),
			wantLen: rcaVerdictBodyMax,
		},
		{
			name:    "tavanın bir baytı üstü kırpılır",
			in:      strings.Repeat("a", rcaVerdictBodyMax+1),
			wantCut: true,
			// 8192 bayt + "…" (3 bayt).
			wantLen: rcaVerdictBodyMax + len("…"),
		},
		{
			name:    "çok baytlı sınır bozulmaz",
			in:      multibyte,
			wantCut: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := trimRCAVerdictBody(tc.in)

			// HER durumda geçerli UTF-8 — testin asıl sebebi bu.
			if !utf8.ValidString(got) {
				t.Fatalf("geçersiz UTF-8 üretildi (uzunluk %d)", len(got))
			}
			if len(got) > rcaVerdictBodyMax+len("…") {
				t.Fatalf("tavan aşıldı: %d bayt", len(got))
			}
			if cut := strings.HasSuffix(got, "…"); cut != tc.wantCut {
				t.Fatalf("kırpma işareti = %v, beklenen %v", cut, tc.wantCut)
			}
			if tc.wantBody != "" || tc.in == "" || strings.TrimSpace(tc.in) == "" {
				if got != tc.wantBody {
					t.Fatalf("gövde = %q, beklenen %q", got, tc.wantBody)
				}
			}
			if tc.wantLen > 0 && len(got) != tc.wantLen {
				t.Fatalf("uzunluk = %d, beklenen %d", len(got), tc.wantLen)
			}
		})
	}
}

// TestTrimRCAVerdictBodyKeepsPrefix — kırpma BAŞTAN alır, sondan değil.
// Verdict'in ilk cümlesi kararın kendisi ("kök neden X'te"); sondan
// başlayan bir kırpma tam da okunması gereken kısmı atardı.
func TestTrimRCAVerdictBodyKeepsPrefix(t *testing.T) {
	head := "KÖK NEDEN: payments-db bağlantı havuzu tükendi. "
	got := trimRCAVerdictBody(head + strings.Repeat("x", rcaVerdictBodyMax))
	if !strings.HasPrefix(got, head) {
		t.Fatalf("baş kısım korunmadı: %.60q", got)
	}
}
