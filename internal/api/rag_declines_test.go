package api

import (
	"strings"
	"testing"
)

// v0.10.14 — operatör sordu: "guided intent uymasa da normal chat gibi
// konuşulamaz mı kullanıcılar LLM ile?"
//
// Cevap evet, ve o yol zaten vardı; sorun RAG'ın cevaplayamadığı soruyu
// da SAHİPLENMESİYDİ. Bu yüklem, bir cevabın operatöre gösterilip
// gösterilmeyeceğine karar veriyor — iki yön de pahalı, o yüzden
// tablo-testli.

func TestRAGDeclined(t *testing.T) {
	declines := []struct{ name, answer string }{
		// Prompt'un modele söylettiği cümlenin ta kendisi — operatörün
		// ekranda gördüğü şey.
		{"prompt'un tam cümlesi", "Yüklü dokümanlarda bu bilgi yok."},
		{"noktasız", "Yüklü dokümanlarda bu bilgi yok"},
		{"türkçe karaktersiz varyant", "Yuklu dokumanlarda bu bilgi yok."},
		{"cümle içinde", "Maalesef yüklü dokümanlarda bu bilgi yok, başka bir şey sorabilirsiniz."},
		{"boş cevap", ""},
		{"yalnız boşluk", "   \n  "},
	}
	for _, tc := range declines {
		t.Run("REDDETME/"+tc.name, func(t *testing.T) {
			if !ragDeclined(tc.answer) {
				t.Errorf("%q reddetme sayılmadı — ölü cevap son söz olur ve serbest döngü sıra alamaz", tc.answer)
			}
		})
	}

	// ── GEÇERLİ cevaplar: sahiplenilmeli ──────────────────────────────
	// Yanlış pozitif, iyi bir doküman cevabını çöpe atıp serbest döngüye
	// gereksiz bir tur attırır — kaynak atıfları da kaybolur.
	keeps := []struct{ name, answer string }{
		{"düz doküman cevabı", "Kanal kodu §2'ye göre 001 mobil bankacılığı ifade eder."},
		{"belirsizlik İÇEREN ama cevap veren", "Kesin bilmiyorum ama kaynak §2'ye göre bu alan zorunlu."},
		{"kısmi bilgi", "Dokümanlarda yalnız kısmi bilgi var: §1 şunu diyor…"},
		{"başka bir yokluk ifadesi — konu dışı", "Bu serviste açık problem yok."},
	}
	for _, tc := range keeps {
		t.Run("SAHİPLEN/"+tc.name, func(t *testing.T) {
			if ragDeclined(tc.answer) {
				t.Errorf("%q reddetme sayıldı — geçerli bir doküman cevabı atılıyor", tc.answer)
			}
		})
	}
}

// TestRAGDeclineMarkersStayNarrow — liste GENİŞLEMEMELİ.
//
// "bilmiyorum" gibi genel ifadeler buraya girerse, model bir doküman
// cevabını "kesin bilmiyorum ama §2'ye göre…" diye başlattığında o
// GEÇERLİ cevap atılır. Reddetme cümlesi prompt'un DAYATTIĞI sabit bir
// cümledir; yüklem ona bağlı kalmalı, genel bir belirsizlik dedektörüne
// dönüşmemeli.
func TestRAGDeclineMarkersStayNarrow(t *testing.T) {
	for _, m := range ragDeclineMarkers {
		if len(m) < 20 {
			t.Errorf("işaret %q fazla kısa/genel — yanlış pozitif üretir", m)
		}
	}
	if ragDeclined("bilmiyorum") {
		t.Error("genel belirsizlik reddetme sayılıyor — liste genişlemiş")
	}
}

// TestRAGChatActuallyDeclines — saf çekirdek yeşil ama BAĞLANTI pinli
// değilse kusur yerinde kalır.
//
// Bunu kendi mutasyon denetimim doğurdu: `ragChatAnswer`'dan
// `if ragDeclined(raw) { return false, false }` satırını sildiğimde
// yukarıdaki tablo testleri YEŞİL kaldı. Yüklem kusursuz, yalnız kimse
// çağırmıyor — v0.10.11'de aynı sınıfı yaşamıştım.
//
// Kaynak taraması, çünkü ragChatAnswer canlı RAG + LLM istiyor;
// korunması gereken şey ise KABLOLAMA.
func TestRAGChatActuallyDeclines(t *testing.T) {
	src := readAPISourceNoComments(t, "rag.go")
	if !strings.Contains(src, "ragDeclined(raw)") {
		t.Error("ragChatAnswer reddetmeyi KONTROL ETMİYOR — " +
			"'bu bilgi yok' cevabı yine son söz olur, serbest döngü sıra alamaz")
	}
	// Akış kapalı olmalı: açıkken reddedilen cevap ekrana yazılır ve
	// serbest döngünün gerçek cevabıyla ARKA ARKAYA İKİ cevap görünür.
	if strings.Contains(src, `emit("delta", map[string]string{"text": delta})`) {
		t.Error("rag-chat token akışı yeniden açılmış — reddedilen cevap " +
			"ekrana yazılır ve operatör iki cevap görür")
	}
}
