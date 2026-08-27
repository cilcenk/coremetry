package devops

import (
	"strings"
	"testing"
)

// code_window_signature_test.go — v0.10.112. PENCERE DIŞINDA KALAN İMZA.
//
// Operatör spec'i: "metot imzası pencerenin dışında kalıyorsa imzayı
// ayrıca ekle". ±30 satır çoğu metodu kapsar; uzun bir metodun
// ortasındaki hata satırında parametre adları/tipleri görünmez ve model
// "hedefin tanımı bu bağlamda yok" demek zorunda kalır (ya da uydurur).

func numbered(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

func longMethod(sig string, bodyLines, errAt int) []string {
	out := []string{"package com.acme.billing;", "", "import java.util.List;", "",
		"public class CardService {", "    private final CardRepo repo;", "",
		"    public CardService(CardRepo repo) {", "        this.repo = repo;", "    }", "",
		sig}
	for i := 1; i <= bodyLines; i++ {
		if i == errAt {
			out = append(out, "        if (card == null) { throw new IllegalStateException(\"no card\"); }")
			continue
		}
		out = append(out, "        int v"+itoa(i)+" = i + "+itoa(i)+";")
	}
	out = append(out, "    }", "}")
	return out
}

func itoa(i int) string {
	return strings.TrimSpace(strings.Replace(string(rune('0'+i%10)), "\x00", "", -1))
}

func TestEnclosingSignatureTable(t *testing.T) {
	sig := "    public Receipt charge(Card card, long amountKurus, String channelCode) throws HostException {"
	lines := longMethod(sig, 100, 80) // imza satır 12, hata satırı 12+80 = 92
	cases := []struct {
		name     string
		lines    []string
		from     int
		wantText string
		wantLine int
	}{
		{"uzun metot, imza pencere üstünde", lines, 62, strings.TrimSpace(sig), 12},
		{"pencere imzayı içeriyor → boş", lines, 12, "", 0},
		{"dosya başı → boş", lines, 1, "", 0},
		{"yalnız alan/import satırları üstte → boş", lines, 7, "", 0},
		{"ctor bulunur", lines, 10, "public CardService(CardRepo repo) {", 8},
		{"kotlin fun", []string{"class A {", "    fun charge(card: Card, amount: Long): Receipt {", "        val x = 1", "        val y = 2"}, 4, "fun charge(card: Card, amount: Long): Receipt {", 2},
		{"scala def", []string{"object A {", "  def charge(card: Card): Receipt = {", "    val x = 1", "    val y = 2"}, 4, "def charge(card: Card): Receipt = {", 2},
		{"kontrol akışı satırı imza değil", []string{"    public void run(int n) {", "        for (int i = 0; i < n; i++) {", "            if (check(i)) {", "                log(i);"}, 4, "public void run(int n) {", 1},
		{"çağrı satırı (;) imza değil", []string{"    void go() {", "        repo.save(card);", "        helper.run(1, 2);", "        int z = 3;"}, 4, "void go() {", 1},
		{"anotasyonlu imza", []string{"    @Override", "    @Transactional(readOnly = true)", "    protected List<Card> load(String id) {", "        int a = 1;"}, 4, "protected List<Card> load(String id) {", 3},
		{"generic dönüş tipi", []string{"    public static <T extends Base> Map<String, List<T>> index(List<T> in) {", "        int a = 1;"}, 2, "public static <T extends Base> Map<String, List<T>> index(List<T> in) {", 1},
		{"kısa dosya, from taşıyor → boş", []string{"a", "b"}, 9, "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, line := EnclosingSignature(c.lines, c.from)
			if text != c.wantText || line != c.wantLine {
				t.Fatalf("got (%q, %d), want (%q, %d)", text, line, c.wantText, c.wantLine)
			}
		})
	}
}

// TestWindowAroundCarriesSignatureIntoPrompt — WindowAround imzayı
// yalnız pencere dışındayken doldurur; PromptBlock onu satır numarasıyla
// ETİKETLİ basar (sezgisel eşleşme — model/operatör doğrulayabilsin).
func TestWindowAroundCarriesSignatureIntoPrompt(t *testing.T) {
	sig := "    public Receipt charge(Card card, long amountKurus) {"
	content := numbered(longMethod(sig, 100, 80)...)
	w := WindowAround(content, 92, 30)
	if w.Signature != strings.TrimSpace(sig) || w.SignatureLine != 12 {
		t.Fatalf("imza taşınmadı: %q / %d", w.Signature, w.SignatureLine)
	}
	w.Path, w.Frame, w.Line = "/src/CardService.java", "com.acme.billing.CardService.charge(CardService.java:92)", 92
	block := (CodeContext{Repo: "r", Windows: []CodeWindow{w}}).PromptBlock()
	if !strings.Contains(block, "imza (satır 12, pencere dışı): public Receipt charge(Card card, long amountKurus) {") {
		t.Fatalf("prompt'ta imza yok:\n%s", block)
	}
	// Pencere imzayı içeriyorsa etiket YOK.
	w2 := WindowAround(content, 20, 30)
	if w2.Signature != "" {
		t.Errorf("pencere içi imza yine taşındı: %q", w2.Signature)
	}
	// Bütçe kırpması imzayı DÜŞÜRMEZ (Content kırpılır, imza alanı kalır).
	ws, trimmed := ClampCodeWindows([]CodeWindow{w}, 300)
	if !trimmed || len(ws) != 1 || ws[0].Signature == "" {
		t.Errorf("kırpma imzayı kaybetti: trimmed=%v n=%d sig=%q", trimmed, len(ws), ws[0].Signature)
	}
}

// TestPromptBlockTellsTheModelAboutTrimming — kırpma/düşme MODELE
// söylenir (operatör direktifi 2026-08-28); Halved da Trimmed'i doldurur.
func TestPromptBlockTellsTheModelAboutTrimming(t *testing.T) {
	cc := CodeContext{Repo: "r", Windows: []CodeWindow{{Path: "/A.java", Line: 3, FromLine: 1, ToLine: 5, Content: "1| a\n2| b\n3| c\n4| d\n5| e"}}}
	if strings.Contains(cc.PromptBlock(), "kod bağlamı EKSİK") {
		t.Fatal("kırpma yokken not basıldı")
	}
	cc.Trimmed = "kod bütçesi (4000 karakter) doldu — 1 pencere düştü"
	if b := cc.PromptBlock(); !strings.Contains(b, "NOT — kod bağlamı EKSİK: kod bütçesi (4000 karakter) doldu — 1 pencere düştü") ||
		!strings.Contains(b, "kaynak çözülemedi") {
		t.Fatalf("kırpma notu prompt'ta yok:\n%s", b)
	}
	// Halved: iki büyük pencere → yarı bütçe birini düşürür → Trimmed dolu.
	big := strings.Repeat("x", 1500)
	h := CodeContext{Repo: "r", Windows: []CodeWindow{
		{Path: "/A.java", Line: 1, FromLine: 1, ToLine: 1, Content: "1| " + big},
		{Path: "/B.java", Line: 1, FromLine: 1, ToLine: 1, Content: "1| " + big},
	}}.Halved()
	if h.Trimmed == "" || !strings.Contains(h.PromptBlock(), "kod bağlamı EKSİK") {
		t.Fatalf("Halved kırpmayı modele söylemedi: trimmed=%q", h.Trimmed)
	}
}

// TestMissingBlock — kod istendi, çözülemedi: gerekçe + "iddia etme"
// kuralı; pencere varsa boş.
func TestMissingBlock(t *testing.T) {
	cc := CodeContext{Repo: "core-service", Reason: "ağaçta eşleşen dosya yok: CardService.java"}
	b := cc.MissingBlock()
	for _, want := range []string{"KOD BAĞLAMI İSTENDİ — ÇÖZÜLEMEDİ: ağaçta eşleşen dosya yok: CardService.java", "(depo: core-service)", "satır numarası", "kaynak çözülemedi: <dosya>"} {
		if !strings.Contains(b, want) {
			t.Errorf("eksik: %q\n%s", want, b)
		}
	}
	if (CodeContext{Windows: []CodeWindow{{Content: "1| x"}}}).MissingBlock() != "" {
		t.Error("pencere varken MissingBlock boş olmalı")
	}
	if !strings.Contains((CodeContext{}).MissingBlock(), "sebep bilinmiyor") {
		t.Error("sebepsiz bağlamda 'sebep bilinmiyor' yok")
	}
}

// TestLogSummaryNoDoubledRepoForSearchWindows — arama-türevi pencere
// yolu "depo:yol" taşır; özet depoyu bir daha öne yazmaz.
func TestLogSummaryNoDoubledRepoForSearchWindows(t *testing.T) {
	cc := CodeContext{Repo: "core-service", Windows: []CodeWindow{
		{Path: "/src/A.java", FromLine: 1, ToLine: 3},
		{Path: "other-repo:/src/B.java", FromLine: 4, ToLine: 6},
	}}
	got := cc.LogSummary()
	if !strings.Contains(got, "[kod: core-service/src/A.java:1-3 · 3 satır]") {
		t.Errorf("yerel pencere özeti bozuldu: %s", got)
	}
	if !strings.Contains(got, "[kod: other-repo:/src/B.java:4-6 · 3 satır]") || strings.Contains(got, "core-serviceother-repo") {
		t.Errorf("arama penceresinde depo ikilendi: %s", got)
	}
}
