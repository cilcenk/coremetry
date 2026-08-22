package anomaly

import (
	"strings"
	"testing"
)

// exception_stack_dedup_test.go — v0.9.1239.
//
// Denetim bulgusu: aynı stacktrace prompt'a 13 kez giriyordu (temsilî
// kopya + örnek trace'in 12 logu, hepsi aynı exception). Taşma
// yeniden-denemesi ise yalnız KOD bloğunu yarıya indiriyor — benzersiz
// kanıt küçülürken tekrar aynen kalıyordu. Katlama kuralları:
// birebir aynı → referans, kırpma farkıyla önek → referans, farklı →
// tam metin, prompt'ta temsilî bölüm YOKSA ilk log tam kalır.

func TestFoldDuplicateLogStacks(t *testing.T) {
	deep := "java.lang.NullPointerException: boom\n" +
		strings.Repeat("\tat com.bsa.card.CardService.load(CardService.java:246)\n", 60)
	other := "java.sql.SQLException: connection reset\n" +
		strings.Repeat("\tat com.bsa.db.Pool.borrow(Pool.java:88)\n", 60)
	// Prompt'ta GÖRÜNEN temsilî kopya (pickExceptionStack ile aynı kırpma).
	primary := truncRunes(deep, 1800)

	tests := []struct {
		name    string
		stacks  []string
		primary string
		want    []string // "" = boş, "REF" = katlanmış, "FULL" = tam metin
	}{
		{
			name:    "birebir aynı → referans",
			stacks:  []string{deep, deep},
			primary: primary,
			want:    []string{"REF", "REF"},
		},
		{
			name:    "kırpma farkı (900 vs 1800) → yine referans",
			stacks:  []string{truncRunes(deep, 900)},
			primary: primary,
			want:    []string{"REF"},
		},
		{
			name:    "farklı stack tam kalır",
			stacks:  []string{deep, other},
			primary: primary,
			want:    []string{"REF", "FULL"},
		},
		{
			name:    "aynı farklı stack ikinci kez → referans",
			stacks:  []string{other, other},
			primary: primary,
			want:    []string{"FULL", "REF"},
		},
		{
			name:    "temsilî yok (log-fallback) → İLK log tam kalır",
			stacks:  []string{deep, deep, deep},
			primary: "",
			want:    []string{"FULL", "REF", "REF"},
		},
		{
			name:    "boş stack'ler boş kalır",
			stacks:  []string{"", deep, ""},
			primary: primary,
			want:    []string{"", "REF", ""},
		},
		{
			name:    "kısa ve farklı → katlanmaz",
			stacks:  []string{"NPE at A.java:1", "NPE at B.java:2"},
			primary: "",
			want:    []string{"FULL", "FULL"},
		},
		{
			name:    "hiç log yok",
			stacks:  nil,
			primary: primary,
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := foldDuplicateLogStacks(tt.stacks, tt.primary)
			if len(got) != len(tt.stacks) {
				t.Fatalf("uzunluk=%d, girdi %d — log satırlarıyla hizalanmaz", len(got), len(tt.stacks))
			}
			for i, want := range tt.want {
				switch want {
				case "":
					if got[i] != "" {
						t.Fatalf("[%d] boş beklenirdi: %q", i, got[i])
					}
				case "REF":
					if got[i] != dupStackRef {
						t.Fatalf("[%d] katlanmalıydı, geldi: %q", i, truncRunes(got[i], 60))
					}
				case "FULL":
					if got[i] == dupStackRef || got[i] == "" {
						t.Fatalf("[%d] tam metin beklenirdi: %q", i, got[i])
					}
					if got[i] != truncRunes(tt.stacks[i], 900) {
						t.Fatalf("[%d] tam metin 900 rune kırpığı olmalı", i)
					}
				}
			}
		})
	}
}

// TestFoldDuplicateLogStacksSavesBudget — bulgunun SAYISAL yarısı:
// 12 kopya prompt'ta ~10.8k rune tutuyordu.
func TestFoldDuplicateLogStacksSavesBudget(t *testing.T) {
	deep := "java.lang.IllegalStateException: x\n" +
		strings.Repeat("\tat com.bsa.a.B.c(B.java:12)\n", 80)
	stacks := make([]string, 12)
	for i := range stacks {
		stacks[i] = deep
	}
	before := 0
	for _, s := range stacks {
		before += len([]rune(truncRunes(s, 900)))
	}
	after := 0
	for _, s := range foldDuplicateLogStacks(stacks, truncRunes(deep, 1800)) {
		after += len([]rune(s))
	}
	if before < 9000 {
		t.Fatalf("test kurgusu zayıf: eski hâl %d rune", before)
	}
	if after > 600 {
		t.Fatalf("katlama bütçe kazandırmadı: %d rune (eski %d)", after, before)
	}
}

func TestSameStackText(t *testing.T) {
	long := "java.lang.NullPointerException: something failed badly here\n" +
		strings.Repeat("\tat com.bsa.x.Y.z(Y.java:10)\n", 10)

	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{name: "birebir", a: long, b: long, want: true},
		{name: "önek (kırpık kopya)", a: truncRunes(long, 150), b: long, want: true},
		{name: "ters yönde önek", a: long, b: truncRunes(long, 150), want: true},
		{name: "CRLF farkı", a: strings.ReplaceAll(long, "\n", "\r\n"), b: long, want: true},
		{name: "baş/son boşluk", a: "  " + long + "\n\n", b: long, want: true},
		{name: "farklı stack", a: long, b: strings.ReplaceAll(long, "Y.java", "Z.java"), want: false},
		{name: "kısa ortak önek katlanmaz", a: "java.lang.NPE\n\tat A.b(A.java:1)", b: "java.lang.NPE\n\tat A.b(A.java:1)\n\tat C.d(C.java:9)", want: false},
		{name: "boş", a: "", b: long, want: false},
		{name: "ikisi de boş", a: "", b: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameStackText(tt.a, tt.b); got != tt.want {
				t.Fatalf("sameStackText=%v, istenen %v", got, tt.want)
			}
		})
	}
}
