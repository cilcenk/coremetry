package api

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cilcenk/coremetry/internal/ai/assemble"
	"github.com/cilcenk/coremetry/internal/copilot"
)

// v0.9.1231 pinleri — guided anlatımının konuşma geçmişi.
//
// Semptom: sohbet turlarının ÇOĞUNU cevaplayan guided yol tek-tur
// körüydü — narration çağrısına yalnız aktif soru + prefetch paketi
// giriyordu. Aynı takip sorusu ("peki dünkü?", "onu logla kıyasla")
// serbest tool döngüsüne düştüğünde referansını buluyor, guided'a
// düştüğünde kaybediyordu; fark operatöre görünmüyordu.
//
// Bu dosya SAF DİKİŞİ (guidedHistorySection) çiviler. Sözleşmenin dört
// köşesi: geçmiş yoksa bölüm de yok (bayt-bayt eski metin), bütçe
// aşılırsa EN ESKİ tur düşer, kırpma SÖYLENİR, aktif soru geçmişe
// ASLA sızmaz.

func TestGuidedHistorySection(t *testing.T) {
	// Aktif soru başlıktaki ÖRNEK ifadeyle çakışmamalı — yoksa "sızdı mı"
	// assert'i başlığın kendi metnini yakalar (sahte kırmızı).
	const active = "onu logla kıyaslar mısın?"
	cases := []struct {
		name        string
		msgs        []copilot.ChatMessage
		wantEmpty   bool
		wantRows    []string
		wantTrimmed bool
	}{
		{
			name:      "geçmiş yok — nil",
			msgs:      nil,
			wantEmpty: true,
		},
		{
			name:      "tek tur — aktif sorudan öncesi boş",
			msgs:      []copilot.ChatMessage{{Role: "user", Text: active}},
			wantEmpty: true,
		},
		{
			name: "yalnız metinsiz tool-result turları",
			msgs: []copilot.ChatMessage{
				{Role: "assistant", Text: "   "},
				{Role: "user", Text: ""},
				{Role: "user", Text: active},
			},
			wantEmpty: true,
		},
		{
			name: "normal geçmiş — rol önekleri ve sıra",
			msgs: []copilot.ChatMessage{
				{Role: "user", Text: "payments hata oranı ne?"},
				{Role: "assistant", Text: "son 30dk %2.1"},
				{Role: "user", Text: ""}, // tool-result turu — atlanır
				{Role: "user", Text: active},
			},
			wantRows: []string{"K: payments hata oranı ne?", "C: son 30dk %2.1"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := guidedHistorySection(c.msgs)
			if c.wantEmpty {
				if got != "" {
					t.Fatalf("boş bölüm bekleniyordu, got=%q", got)
				}
				return
			}
			if !strings.HasPrefix(got, guidedHistoryHeader) {
				t.Fatalf("başlık yok: %q", got)
			}
			body := strings.TrimPrefix(got, guidedHistoryHeader)
			if want := strings.Join(c.wantRows, "\n"); body != want {
				t.Fatalf("gövde\n got=%q\nwant=%q", body, want)
			}
			if assemble.HasTrimNote(got) != c.wantTrimmed {
				t.Fatalf("kırpma notu=%v, beklenen=%v", assemble.HasTrimNote(got), c.wantTrimmed)
			}
			// Aktif soru geçmişe ASLA girmez: SORU: satırında zaten var,
			// ikinci kez yazmak modele iki kez sormak olurdu.
			if strings.Contains(got, active) {
				t.Fatalf("aktif soru geçmişe sızdı: %q", got)
			}
		})
	}
}

func TestGuidedHistorySectionTurnCap(t *testing.T) {
	var msgs []copilot.ChatMessage
	for i := 0; i < 20; i++ {
		msgs = append(msgs,
			copilot.ChatMessage{Role: "user", Text: "s" + string(rune('a'+i))},
			copilot.ChatMessage{Role: "assistant", Text: "c" + string(rune('a'+i))})
	}
	msgs = append(msgs, copilot.ChatMessage{Role: "user", Text: "aktif soru"})
	got := guidedHistorySection(msgs)
	body := trimHistoryHead(t, got)
	rows := strings.Split(body, "\n")
	if len(rows) != guidedHistoryTurns {
		t.Fatalf("satır=%d, beklenen=%d (%q)", len(rows), guidedHistoryTurns, body)
	}
	// EN YENİ turlar tutulur, en eskiler düşer.
	if last := rows[len(rows)-1]; last != "C: ct" { // i=19 → 't'
		t.Fatalf("en yeni tur korunmadı: %q", last)
	}
	if strings.Contains(body, "K: sa") {
		t.Fatal("en eski tur düşmemiş")
	}
	if !assemble.HasTrimNote(got) {
		t.Fatal("kırpma SÖYLENMEDİ (sessiz kırpma = modelin uydurma hatırlaması)")
	}
}

func TestGuidedHistorySectionRuneBudget(t *testing.T) {
	// Altı tur × 400 rune = 2400 > guidedHistoryMaxRunes: en eskiler düşer.
	var msgs []copilot.ChatMessage
	for i := 0; i < 6; i++ {
		msgs = append(msgs, copilot.ChatMessage{
			Role: "user",
			Text: string(rune('a'+i)) + strings.Repeat("x", 399),
		})
	}
	msgs = append(msgs, copilot.ChatMessage{Role: "user", Text: "aktif soru"})
	got := guidedHistorySection(msgs)
	body := trimHistoryHead(t, got)
	if n := utf8.RuneCountInString(body); n > guidedHistoryMaxRunes {
		t.Fatalf("gövde bütçeyi aştı: %d > %d", n, guidedHistoryMaxRunes)
	}
	if strings.Contains(body, "K: a") {
		t.Fatal("bütçe aşımında EN ESKİ tur düşmeliydi")
	}
	if !strings.Contains(body, "K: f") {
		t.Fatal("en yeni tur düştü — kesme yanlış uçtan")
	}
	if !assemble.HasTrimNote(got) {
		t.Fatal("kırpma SÖYLENMEDİ")
	}
}

func TestGuidedHistorySectionSingleOversizedTurn(t *testing.T) {
	// ClampHistory'nin "EN AZ BİR tur" köşesi: tek dev tur bütçeyi tek
	// başına aşar. Guided'da kanıt geçmişten önce gelir → kuyruktan kesilir.
	msgs := []copilot.ChatMessage{
		{Role: "user", Text: strings.Repeat("y", 5000)},
		{Role: "user", Text: "aktif soru"},
	}
	got := guidedHistorySection(msgs)
	body := trimHistoryHead(t, got)
	if n := utf8.RuneCountInString(body); n > guidedHistoryMaxRunes {
		t.Fatalf("dev tur kesilmedi: %d > %d", n, guidedHistoryMaxRunes)
	}
	if !assemble.HasTrimNote(got) {
		t.Fatal("kesme SÖYLENMEDİ")
	}
}

func TestGuidedHistorySectionUpstreamTrimNote(t *testing.T) {
	// copilot_chat.go klamp yaptığında işareti SAHTE bir kullanıcı turu
	// olarak enjekte eder. Onu "K:" ile yazmak konuşmayı tahrif ederdi;
	// bilgisi bölümün kendi notuna döner.
	msgs := []copilot.ChatMessage{
		{Role: "user", Text: assemble.HistoryTrimNote},
		{Role: "user", Text: "payments nasıl?"},
		{Role: "assistant", Text: "p99 210ms"},
		{Role: "user", Text: "aktif soru"},
	}
	got := guidedHistorySection(msgs)
	if strings.Contains(got, "K: "+assemble.HistoryTrimNote) {
		t.Fatalf("yukarı-akış işareti operatör turu gibi yazıldı: %q", got)
	}
	if !assemble.HasTrimNote(got) {
		t.Fatal("yukarı-akış kırpması bölümde söylenmedi")
	}
	if strings.Count(got, assemble.HistoryTrimNote) != 1 {
		t.Fatalf("işaret tekrarlandı: %q", got)
	}
	body := trimHistoryHead(t, got)
	if body != "K: payments nasıl?\nC: p99 210ms" {
		t.Fatalf("gövde beklenmedik: %q", body)
	}
}

func TestGuidedNarrationUserAppendsHistoryLast(t *testing.T) {
	msgs := []copilot.ChatMessage{
		{Role: "user", Text: "payments hata oranı ne?"},
		{Role: "assistant", Text: "son 30dk %2.1"},
		{Role: "user", Text: "onu logla kıyaslar mısın?"},
	}
	got := guidedNarrationUser("onu logla kıyaslar mısın?", "p99: 210ms", "NPE checkout'ta", msgs)
	if !strings.HasPrefix(got, "SORU: onu logla kıyaslar mısın?\n\nVERİ:\np99: 210ms") {
		t.Fatalf("taban blok korunmadı: %q", got)
	}
	iEv := strings.Index(got, "VERİ:")
	iEx := strings.Index(got, "EKRANDAKİ AÇIKLAMA")
	iHist := strings.Index(got, guidedHistoryHeader)
	if iHist < 0 {
		t.Fatalf("geçmiş bölümü eklenmedi: %q", got)
	}
	// Sıra sözleşmesi: kanıt → açıklama → geçmiş. Geçmiş merdivenin en
	// alt basamağı; kanıtı bloğun başından itemez.
	if !(iEv < iEx && iEx < iHist) {
		t.Fatalf("sıra bozuldu: veri=%d açıklama=%d geçmiş=%d", iEv, iEx, iHist)
	}
	if !strings.Contains(got, "K: payments hata oranı ne?") {
		t.Fatalf("geçmiş turu taşınmadı: %q", got)
	}
	// Aktif soru yalnız SORU: satırında geçer.
	if n := strings.Count(got, "onu logla kıyaslar mısın?"); n != 1 {
		t.Fatalf("aktif soru %d kez geçti, 1 beklenir: %q", n, got)
	}
}

// trimHistoryHead — bölümden başlığı (ve varsa kırpma notu satırını)
// söker; testler yalnız GÖVDEYİ ölçsün.
func trimHistoryHead(t *testing.T, sec string) string {
	t.Helper()
	if !strings.HasPrefix(sec, guidedHistoryHeader) {
		t.Fatalf("başlık yok: %q", sec)
	}
	body := strings.TrimPrefix(sec, guidedHistoryHeader)
	if strings.HasPrefix(body, assemble.HistoryTrimNote+"\n") {
		body = strings.TrimPrefix(body, assemble.HistoryTrimNote+"\n")
	}
	return body
}
