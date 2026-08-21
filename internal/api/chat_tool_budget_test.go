package api

// v0.9.1230 — tool sonucunun MODEL tarafındaki bütçesi.
//
// Belirti (ölçüldü, v0.9.1230 perf denetimi): ToolResult.Content
// sınırsızdı — mcptools klampları SATIR sayar, boyut saymaz — ve
// konuşma her turda bütün olarak yeniden gönderildiği için tek bir
// şişkin sonucun bedeli 5 tura kadar yeniden ödeniyordu.
//
// Bu dosya iki şeyi birden korur: tavanın kendisi, ve tavanın
// SÖYLENMESİ. Sessiz kırpma bu evde yasak sınıf.

import (
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClampToolResultForModel(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantTrunc bool
		wantRunes int // kırpılmamış hâlde giriş kadar; kırpılmışta tavan
	}{
		{"bos", "", false, 0},
		{"kisa", `{"services":[]}`, false, 15},
		{"tam tavanda", strings.Repeat("a", chatToolResultMaxRunes), false, chatToolResultMaxRunes},
		{"tavanin bir altinda", strings.Repeat("a", chatToolResultMaxRunes-1), false, chatToolResultMaxRunes - 1},
		{"tavanin bir ustunde", strings.Repeat("a", chatToolResultMaxRunes+1), true, chatToolResultMaxRunes},
		{"cok buyuk", strings.Repeat("a", chatToolResultMaxRunes*4), true, chatToolResultMaxRunes},
		// ÇOK BAYTLI: rune sayısı tavanın altında ama BAYT sayısı çok
		// üstünde. Bayt tabanlı bir tavan burada yanlışlıkla kırpardı.
		{"turkce tavan alti", strings.Repeat("ş", chatToolResultMaxRunes-10), false, chatToolResultMaxRunes - 10},
		{"turkce tavan ustu", strings.Repeat("ş", chatToolResultMaxRunes+10), true, chatToolResultMaxRunes},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, trunc := clampToolResultForModel(c.in)
			if trunc != c.wantTrunc {
				t.Fatalf("truncated=%v, beklenen %v", trunc, c.wantTrunc)
			}
			if !utf8.ValidString(out) {
				t.Fatal("çıktı geçerli UTF-8 değil — bayt sınırından kesilmiş")
			}
			if !trunc {
				if out != c.in {
					t.Fatal("kırpılmamış girdi DEĞİŞTİRİLMEMELİ")
				}
				if strings.Contains(out, "kırpıldı") {
					t.Fatal("kırpılmamış girdiye not eklenmiş")
				}
				return
			}
			// Kırpılmış: gövde tam tavan kadar, sonrasında NOT var.
			body := out
			idx := strings.Index(out, "\n\n[kırpıldı:")
			if idx < 0 {
				t.Fatal("kırpma NOTU yok — sessiz kırpma yasak sınıf")
			}
			body = out[:idx]
			if n := utf8.RuneCountInString(body); n != c.wantRunes {
				t.Fatalf("gövde %d rune, beklenen %d", n, c.wantRunes)
			}
			// Not, atlanan miktarı SAYIYLA söylemeli — "bir şeyler eksik"
			// yetmez, model ne kadarını kaçırdığını bilmeli.
			dropped := utf8.RuneCountInString(c.in) - c.wantRunes
			if !strings.Contains(out, sprintfTruncNote(c.wantRunes, dropped)) {
				t.Fatalf("not beklenen sayıları taşımıyor (kept=%d dropped=%d): %q",
					c.wantRunes, dropped, out[idx:])
			}
		})
	}
}

// Rune sınırı testi, en sinsi hâliyle: tavan tam bir çok-baytlı runenin
// ORTASINA denk geldiğinde bile çıktı geçerli UTF-8 kalmalı ve rune
// SAYISI tavanı aşmamalı.
func TestClampToolResultRuneBoundary(t *testing.T) {
	// Tavan tam sınırdayken bir sonraki rune 4 baytlı olsun (emoji).
	in := strings.Repeat("a", chatToolResultMaxRunes) + "🚀" + strings.Repeat("b", 100)
	out, trunc := clampToolResultForModel(in)
	if !trunc {
		t.Fatal("kırpılmalıydı")
	}
	if !utf8.ValidString(out) {
		t.Fatal("bozuk UTF-8 — bayt dilimlemesi yapılmış")
	}
	if strings.Contains(out, "🚀") {
		t.Fatal("tavanın ötesindeki rune sızmış")
	}
}

// MUTASYON PİNİ — serbest döngü bu iki kararı da UYGULAMALI. Saf
// fonksiyonun testi geçse bile çağrılmıyorsa kazanç sıfırdır
// (v0.9.982 dersi: saf-test ≠ BAĞLANMA).
func TestChatLoopWiresCatalogAndResultBudget(t *testing.T) {
	b, err := os.ReadFile("copilot_chat.go")
	if err != nil {
		t.Fatalf("copilot_chat.go okunamadı: %v", err)
	}
	src := stripGoComments(string(b))
	if !strings.Contains(src, "Description: t.ChatDescription()") {
		t.Error("sohbet spec'i KOMPAKT kataloğu kullanmıyor — " +
			"copilot.ToolSpec{… Description: t.ChatDescription() …} bekleniyor")
	}
	if strings.Contains(src, "Description: t.Description") {
		t.Error("tam İngilizce açıklama hâlâ spec'e giriyor — her tur ~24 KB")
	}
	if !strings.Contains(src, "clampToolResultForModel(tr.Content)") {
		t.Error("tool sonucu MODEL bütçesine indirilmiyor — " +
			"clampToolResultForModel(tr.Content) bekleniyor")
	}
	// Sıra sözleşmesi: klamp step-result yayınından SONRA gelmeli, yoksa
	// çipin `bytes`/önizlemesi kırpılmış boyu gösterir ve operatörün
	// "ne kadarını görmüyorum" sorusu cevapsız kalır.
	idxEmit := strings.Index(src, `emit("step-result", stepEv)`)
	idxClamp := strings.Index(src, "clampToolResultForModel(tr.Content)")
	if idxEmit < 0 || idxClamp < 0 || idxClamp < idxEmit {
		t.Error("klamp, step-result yayınından SONRA uygulanmalı (çip gerçek boyu göstermeli)")
	}
	// Ve klamp, sonucun konuşmaya girmesinden ÖNCE.
	idxAppend := strings.Index(src[idxClamp:], "results = append(results, tr)")
	if idxAppend < 0 {
		t.Error("klamp ile results append arasındaki sıra bozulmuş")
	}
}
