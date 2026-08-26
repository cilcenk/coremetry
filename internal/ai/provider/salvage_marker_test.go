package provider

import (
	"strings"
	"testing"
)

// v0.10.37 — Copilot denetimi bulgusu: modelin DÜŞÜNCE bloğu, nihai cevap
// kılığında yayına giriyordu.
//
// SalvageAnswer'ın son çaresi <think> bloğunun İÇİNİ döndürüyor.
// Kurtarmanın kendisi MAKUL ve dosyanın kendi yorumu gerekçesini yazmış:
// bazı modeller yalnız düşünce bloğu üretiyor ve o düşünce genelde
// açıklamanın kendisi — başarısız saymak daha kötü.
//
// ⚠ Kusur kurtarma DEĞİL, sonucun gerçek cevaptan AYIRT EDİLEMEZ olması:
//   • `answer` olayıyla normal cevap gibi çıkıyor
//   • ai_calls.response_sample'a normal cevap gibi yazılıyor
//   • bir sonraki turda geçmişe normal cevap gibi biniyor
// Yani modelin SPEKÜLASYON fazı yayına giriyordu ve operatörün bunu
// anlamasının hiçbir yolu yoktu.

func TestSalvagedThinkingIsMarked(t *testing.T) {
	// Yalnız düşünce bloğu üreten model — kurtarma dalı.
	in := "<think>Belki checkout servisi yavaş, ama emin değilim</think>"
	out := SalvageAnswer(in, "", "")

	if out == "" {
		t.Fatal("kurtarma çalışmadı — dal tamamen kopmuş")
	}
	if !IsSalvagedThinking(out) {
		t.Errorf("kurtarılan düşünce İŞARETSİZ çıktı — operatör onu nihai "+
			"cevap sanar:\n%s", out)
	}
	// İçerik korunmalı: işaret bilgiyi silmek için değil, çerçevelemek için.
	if !strings.Contains(out, "checkout servisi yavaş") {
		t.Errorf("düşünce içeriği kaybolmuş: %q", out)
	}
	// Uyarı NE OLDUĞUNU söylemeli.
	if !strings.Contains(out, "çalışma notu") {
		t.Errorf("uyarı ne olduğunu söylemiyor: %q", out)
	}
}

// TestNormalAnswerIsNotMarked — düzeltmenin YENİ kusur üretmemesi.
//
// Normal cevabı "düşünce" diye işaretlemek, operatörün güvenmesi gereken
// cevaba gölge düşürürdü — kurtarma dalını hiç işaretlememekten farklı
// ama yine yanlış.
func TestNormalAnswerIsNotMarked(t *testing.T) {
	for _, tc := range []struct {
		name              string
		content, rc, reas string
	}{
		{"düz cevap", "checkout p99 340ms", "", ""},
		{"düşünce SONRASI cevap", "<think>hmm</think>checkout p99 340ms", "", ""},
		{"reasoning_content ayrı, content dolu", "cevap", "düşünce", ""},
		// ⚠ v0.10.66 — İKİ DURUM BURADAN ÇIKARILDI ("yalnız
		// reasoning_content", "yalnız reasoning") ve artık İŞARETLENİYOR
		// (bkz. TestSeparatedReasoningIsAlsoMarked).
		//
		// v0.10.37 yalnız <think> dalını işaretlemişti ve bu test o
		// tutarsızlığı çiviledi. Karar yanlıştı: 10.37'nin derdi "modelin
		// SPEKÜLASYON fazı nihai cevap kılığında yayına giriyor"du ve
		// content boşken reasoning_content'ten dönen metin TAM OLARAK odur
		// — aynı madde, yalnız sağlayıcı ayrı bir alana koymuş.
		//
		// Ayrım TAŞIYICIDA değil MADDEDE: content doluysa model cevabını
		// yazmıştır; oraya düşmediysek cevap yazılmamış demektir.
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := SalvageAnswer(tc.content, tc.rc, tc.reas)
			if IsSalvagedThinking(out) {
				t.Errorf("normal cevap düşünce diye işaretlendi: %q", out)
			}
		})
	}
}

// TestEmptyStaysEmpty — işaretin TEK BAŞINA gitmemesi.
//
// Boş bir kurtarma işaretlenirse, olmayan bir cevap varmış gibi görünür
// ve çağıranın EmptyAnswerError teşhisi devre dışı kalır.
func TestEmptyStaysEmpty(t *testing.T) {
	for _, in := range []string{"", "   ", "<think></think>", "<think>   </think>"} {
		if out := SalvageAnswer(in, "", ""); out != "" {
			t.Errorf("boş girdi (%q) için işaret üretildi: %q — EmptyAnswerError teşhisi ölür", in, out)
		}
	}
	if got := MarkSalvagedThinking(""); got != "" {
		t.Errorf("MarkSalvagedThinking(\"\") = %q; boş kalmalıydı", got)
	}
}

func TestMarkIsIdempotent(t *testing.T) {
	// Zincir iki kez çağrılırsa uyarı üst üste binerdi.
	once := MarkSalvagedThinking("düşünce")
	if twice := MarkSalvagedThinking(once); twice != once {
		t.Errorf("işaret tekrarlandı:\n%q", twice)
	}
	if n := strings.Count(once, SalvagedThinkingPrefix); n != 1 {
		t.Errorf("işaret %d kez var, 1 olmalı", n)
	}
}

// TestConsumersUseTheHelper — dizge kopyalanmasın.
//
// Bir tüketici uyarıyı kendi kopyasında yazarsa, işaret değiştiğinde
// SESSİZCE kopar ve rozet kaybolur (bu depoda tekrar eden sınıf:
// gate tek-yazım kör noktası).
func TestConsumersUseTheHelper(t *testing.T) {
	if !strings.HasPrefix(SalvagedThinkingPrefix, "⚠") {
		t.Error("uyarı görsel işaretle başlamıyor — metin içinde kaybolur")
	}
	// Kurtarma dalı yardımcıyı ÇAĞIRMALI, ham ThinkingContent dönmemeli.
	out := SalvageAnswer("<think>x</think>", "", "")
	if !strings.HasPrefix(out, SalvagedThinkingPrefix) {
		t.Error("SalvageAnswer son çaresi işaretlemiyor")
	}
}

// TestSeparatedReasoningIsAlsoMarked — v0.10.66.
//
// Sağlayıcı düşünceyi ayrı bir alana koyduğunda (reasoning_content /
// reasoning) ve content BOŞ olduğunda, operatörün eline geçen şey yine
// modelin ÇALIŞMA NOTUDUR. v0.10.37 bunu işaretlemiyordu; yalnız <think>
// dalını işaretliyordu ve fark tamamen TAŞIYICIDAN geliyordu.
func TestSeparatedReasoningIsAlsoMarked(t *testing.T) {
	for _, tc := range []struct{ name, rc, reas string }{
		{"yalnız reasoning_content", "belki checkout yavaş", ""},
		{"yalnız reasoning", "", "belki checkout yavaş"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := SalvageAnswer("", tc.rc, tc.reas)
			if !IsSalvagedThinking(out) {
				t.Errorf("ayrılmış düşünce İŞARETSİZ çıktı — operatör onu nihai "+
					"cevap sanar:\n%s", out)
			}
			if !strings.Contains(out, "checkout yavaş") {
				t.Errorf("içerik kayboldu: %q", out)
			}
		})
	}
	// content DOLUYSA işaret YOK: model cevabını yazmıştır.
	if IsSalvagedThinking(SalvageAnswer("checkout p99 340ms", "düşünce", "")) {
		t.Error("gerçek cevap düşünce diye işaretlendi")
	}
}
