package api

import (
	"errors"
	"fmt"
	"github.com/cilcenk/coremetry/internal/ai/provider"
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/copilot"
)

// v0.10.26 — Copilot denetimi: bağlam taşması sohbet yolunda TEŞHİS
// EDİLMİYORDU. `isContextOverflowErr` yazılmış ve tablo-testliydi ama tek
// çağrı yeri copilot_code.go'ydu; sohbet hatayı sınıflandırmadan SSE'ye
// basıyordu ve operatör şunu görüyordu:
//
//	openai-compat 400: This model's maximum context length is 8192 tokens…
//
// Ne taştığı söylenmiyor, otomatik küçültme yok — oysa kod-explain
// yolunda tam olarak bu vardı (bloğu yarıya indir, bir kez retry).

func msgs(n int) []copilot.ChatMessage {
	out := make([]copilot.ChatMessage, n)
	for i := range out {
		out[i] = copilot.ChatMessage{Role: "user", Text: string(rune('a' + i%26))}
	}
	return out
}

func TestShrinkConvForRetry(t *testing.T) {
	t.Run("yarıya iner", func(t *testing.T) {
		got, ok := shrinkConvForRetry(msgs(10))
		if !ok {
			t.Fatal("küçültülebilirdi ama false döndü")
		}
		if len(got) != 5 {
			t.Errorf("uzunluk %d; 5 bekleniyordu", len(got))
		}
	})

	// ⚠ EN YENİLER korunmalı. Baştan atmak modelin cevaplayacağı SORUYU
	// silmek olurdu — küçültme cevabı imkânsızlaştırmamalı.
	t.Run("SON mesajlar korunur", func(t *testing.T) {
		in := []copilot.ChatMessage{
			{Role: "user", Text: "eski"},
			{Role: "assistant", Text: "orta"},
			{Role: "user", Text: "SON SORU"},
			{Role: "assistant", Text: "kanıt"},
		}
		got, ok := shrinkConvForRetry(in)
		if !ok {
			t.Fatal("küçültülemedi")
		}
		if got[len(got)-1].Text != "kanıt" {
			t.Errorf("son mesaj düşmüş: %+v", got)
		}
		for _, m := range got {
			if m.Text == "eski" {
				t.Error("EN ESKİ mesaj korunmuş — küçültme yanlış uçtan yapılıyor")
			}
		}
	})

	// Küçültecek bir şey kalmadığında false: aksi hâlde çağıran aynı
	// taşmayı bir çağrı daha yakarak tekrarlar.
	t.Run("küçültülemez durumlar", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			in   []copilot.ChatMessage
		}{
			{"tek mesaj", msgs(1)},
			{"boş", nil},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if _, ok := shrinkConvForRetry(tc.in); ok {
					t.Error("küçültülebilir dendi — aynı duvara bir çağrı daha yakılır")
				}
			})
		}
	})

	t.Run("iki mesaj — biri korunur", func(t *testing.T) {
		got, ok := shrinkConvForRetry(msgs(2))
		if !ok || len(got) != 1 {
			t.Errorf("got=%d ok=%v; 1 mesaj beklenirdi", len(got), ok)
		}
	})
}

func TestChatOverflowMessage(t *testing.T) {
	first := chatOverflowMessageTR(false)
	again := chatOverflowMessageTR(true)

	for _, m := range []string{first, again} {
		// Ham İngilizce sağlayıcı metni operatöre ne yapacağını
		// söylemiyordu; Türkçe ve eyleme dönük olmalı.
		if !strings.Contains(m, "bağlam penceresine sığmadı") {
			t.Errorf("ne olduğu söylenmiyor: %q", m)
		}
		if !strings.Contains(m, "daralt") {
			t.Errorf("ne yapılacağı söylenmiyor: %q", m)
		}
		if strings.Contains(strings.ToLower(m), "context length") {
			t.Errorf("ham sağlayıcı metni sızıyor: %q", m)
		}
	}
	// İkinci mesaj küçültmenin DENENDİĞİNİ söylemeli; söylemezse operatör
	// aynı soruyu tekrar sorar ve aynı duvara çarpar.
	if !strings.Contains(again, "yarıya indirilip") {
		t.Errorf("yeniden denendiği söylenmiyor: %q", again)
	}
	if strings.Contains(first, "yarıya indirilip") {
		t.Errorf("hiç denenmemişken denenmiş gibi anlatılıyor: %q", first)
	}
}

// TestOverflowDetectorStillDiscriminates — düzeltmenin yeni kusur
// üretmemesi.
//
// isContextOverflowErr'ın var oluş gerekçesi, HER 400'ü taşma sanmamak:
// response_format'ı reddeden bir 400 de var ve onu taşma sanıp geçmişi
// yarıya indirmek yanlış teşhistir — cevap yine gelmez, üstüne bir çağrı
// daha yakılır. Sohbete bağlarken bu ayrımın korunduğunu pinliyoruz.
func TestOverflowDetectorStillDiscriminates(t *testing.T) {
	if !isContextOverflowErr(errors.New("openai-compat 400: maximum context length is 8192 tokens")) {
		t.Error("gerçek taşma tanınmıyor")
	}
	if isContextOverflowErr(errors.New("openai-compat 400: response_format not supported")) {
		t.Error("response_format reddi taşma sanıldı — geçmiş boşuna yarıya iner")
	}
	if isContextOverflowErr(errors.New("dial tcp: connection refused")) {
		t.Error("ağ hatası taşma sanıldı")
	}
}

// TestChatWiresOverflowHandling — KABLOLAMA PİNİ.
//
// Saf çekirdek yeşil ama döngü onu çağırmıyorsa kusur yerinde kalır —
// tam olarak bu bulgunun kendisi (isContextOverflowErr yazılıydı,
// çağrılmıyordu).
func TestChatWiresOverflowHandling(t *testing.T) {
	b, err := os.ReadFile("copilot_chat.go")
	if err != nil {
		t.Fatalf("copilot_chat.go okunamadı: %v", err)
	}
	src := stripGoCommentsAPI(string(b))

	for _, must := range []string{
		"isContextOverflowErr(err) && !overflowRetried",
		"shrinkConvForRetry(conv)",
		"chatOverflowMessageTR(overflowRetried)",
		"overflowRetried = true",
	} {
		if !strings.Contains(src, must) {
			t.Errorf("sohbet taşmayı ele almıyor, kayıp: %s", must)
		}
	}
	// Yeniden deneme turu SAYILMAMALI: sayılırsa bir tur kaybedilir ve
	// model bir adım eksik düşünür.
	if !strings.Contains(src, "round--") {
		t.Error("yeniden deneme turu geri alınmıyor — döngü bir tur kaybeder")
	}
}

// TestShrinkNeverOrphansToolResults — v0.10.52.
//
// ⚠ Bu kusur v0.10.26'nın kurtarma yolunu tam ihtiyaç anında yarı yarıya
// çalışmaz hâle getiriyordu.
//
// Tool sonuçları taşıyan bir mesaj sağlayıcıya her sonuç için ayrı bir
// `{"role":"tool","tool_call_id":…}` mesajı olarak gidiyor
// (provider/tools.go). OpenAI uyumlu sunucular, aynı id'yi taşıyan asistan
// `tool_calls` mesajı ÖNCESİNDE yoksa bunu REDDEDİYOR.
//
// Ham `conv[len-keep:]` kesimi çifti PARİTEYE göre bölüyordu: korunan ilk
// mesaj bir tool-sonucu olduğunda yeniden deneme 400 ile ölüyor ve
// operatör, taşmayı dürüstçe anlatan cümle yerine ham sağlayıcı hatası
// görüyordu.
//
// Bu yüzden test TEK bir uzunluk değil, HER uzunluğu deniyor: kusur
// paritede yaşıyordu, tek örnek onu ıskalardı.
func TestShrinkNeverOrphansToolResults(t *testing.T) {
	mkUser := func(q string) copilot.ChatMessage {
		return copilot.ChatMessage{Role: "user", Text: q}
	}
	mkCall := func(id string) copilot.ChatMessage {
		return copilot.ChatMessage{Role: "assistant",
			ToolCalls: []provider.ToolCall{{ID: id, Name: "list_services"}}}
	}
	mkResult := func(id string) copilot.ChatMessage {
		return copilot.ChatMessage{Role: "user",
			ToolResults: []provider.ToolResult{{CallID: id, Name: "list_services", Content: "{}"}}}
	}

	for n := 2; n <= 24; n++ {
		conv := []copilot.ChatMessage{mkUser("checkout yavaş mı?")}
		for i := 1; len(conv) < n; i++ {
			conv = append(conv, mkCall(fmt.Sprintf("c%d", i)))
			if len(conv) < n {
				conv = append(conv, mkResult(fmt.Sprintf("c%d", i)))
			}
		}
		got, ok := shrinkConvForRetry(conv)
		if !ok {
			continue // küçültecek bir şey yok — meşru
		}
		if len(got) == 0 {
			t.Fatalf("n=%d: küçültme her şeyi sildi", n)
		}
		if len(got[0].ToolResults) > 0 {
			t.Errorf("n=%d: korunan İLK mesaj öksüz bir tool sonucu — sağlayıcı "+
				"eşleşen tool_calls olmadan role:tool mesajını REDDEDER ve taşma "+
				"kurtarması 400 ile ölür", n)
		}
		if len(got) >= len(conv) {
			t.Errorf("n=%d: küçültme gerçekten küçültmedi (%d → %d)", n, len(conv), len(got))
		}
	}
}
