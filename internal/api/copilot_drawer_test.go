package api

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/copilot"
)

// v0.9.479 pinleri — AI çekmecesi sohbetinin bağlam devri.
//
// Operatör raporu (prod fotoğrafı): çekmecedeki "Chat'te devam et"
// global CoSRE penceresini açıyordu ve sohbet ekrandaki açıklamayı
// bilmiyordu — ekranda exception dururken CoSRE "bu trace ID'ye ait
// veri yok" deyip filo geneli JVM GC uyarılarını anlattı.
//
// En kritik pin ilk testte: `context.explain` YOKKEN guided narration
// bloğu bayt-bayt v0.9.478'deki metindir. Global chat davranışının
// değişmediğinin garantisi budur.

func TestGuidedNarrationUserAbsentExplain(t *testing.T) {
	// v0.9.478'e kadar copilot_guided.go'nun ürettiği ifadenin birebir
	// kopyası — bu literal DEĞİŞİRSE global chat promptu değişmiş demektir.
	legacy := func(question, evidence string) string {
		return "SORU: " + question + "\n\nVERİ:\n" + evidence
	}
	cases := []struct{ name, question, evidence, explain string }{
		{"boş explain", "payments nasıl?", "p99: 210ms\nhata: %0.4", ""},
		{"yalnız boşluk", "payments nasıl?", "p99: 210ms", "   \n\t "},
		{"boş kanıt", "açık problemler?", "", ""},
		{"çok satırlı kanıt", "checkout hata logları?", "l1\nl2\nl3", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := guidedNarrationUser(c.question, c.evidence, c.explain)
			if want := legacy(c.question, c.evidence); got != want {
				t.Fatalf("explain'siz blok değişti\n got=%q\nwant=%q", got, want)
			}
		})
	}
}

func TestGuidedNarrationUserWithExplain(t *testing.T) {
	got := guidedNarrationUser("peki hata logları?", "log: 12 error", "NPE checkout'ta, 34 kez")
	if !strings.HasPrefix(got, "SORU: peki hata logları?\n\nVERİ:\nlog: 12 error") {
		t.Fatalf("taban blok korunmadı: %q", got)
	}
	if !strings.Contains(got, "EKRANDAKİ AÇIKLAMA") || !strings.Contains(got, "NPE checkout'ta, 34 kez") {
		t.Fatalf("açıklama bloğu eklenmedi: %q", got)
	}
}

func TestClampDrawerExplain(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantRunes  int
		wantSuffix bool
		wantEmpty  bool
	}{
		{name: "boş", in: "", wantEmpty: true},
		{name: "yalnız boşluk", in: "  \n ", wantEmpty: true},
		{name: "kısa", in: "NPE checkout'ta", wantRunes: len([]rune("NPE checkout'ta"))},
		{name: "tam sınır", in: strings.Repeat("a", drawerExplainMaxRunes), wantRunes: drawerExplainMaxRunes},
		{
			name: "sınır+1 kırpılır", in: strings.Repeat("a", drawerExplainMaxRunes+1),
			wantRunes: drawerExplainMaxRunes + len([]rune(drawerTruncSuffix)), wantSuffix: true,
		},
		{
			// Türkçe/çok-baytlı: kesme RUNE bazlı olmalı — byte kesmesi
			// karakteri ortadan bölerdi (bozuk UTF-8 modele gider).
			name: "çok-baytlı kırpma", in: strings.Repeat("ğ", drawerExplainMaxRunes+50),
			wantRunes: drawerExplainMaxRunes + len([]rune(drawerTruncSuffix)), wantSuffix: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := clampDrawerExplain(c.in)
			if c.wantEmpty {
				if got != "" {
					t.Fatalf("boş beklendi, %q geldi", got)
				}
				return
			}
			if n := len([]rune(got)); n != c.wantRunes {
				t.Fatalf("rune sayısı=%d, beklenen=%d", n, c.wantRunes)
			}
			if !utf8Valid(got) {
				t.Fatal("kırpma geçersiz UTF-8 üretti")
			}
			if c.wantSuffix != strings.HasSuffix(got, drawerTruncSuffix) {
				t.Fatalf("kısaltma eki beklendi=%v, got=%q", c.wantSuffix, got[max0(len(got)-20):])
			}
		})
	}
}

func TestDrawerSuppressesGuided(t *testing.T) {
	const ex = "NPE checkout'ta, 34 kez"
	cases := []struct {
		name     string
		explain  string
		route    guidedRoute
		question string
		want     bool
	}{
		{
			// Operatör raporundaki tam senaryo: ekranda exception,
			// servissiz pod_health rotası filo geneli JVM GC anlatıyordu.
			name:    "explain + servissiz rota → guided atlanır",
			explain: ex, route: guidedRoute{Intent: guidedPodHealth}, question: "bu neden oluyor?", want: true,
		},
		{
			name:    "explain yok → guided aynen çalışır",
			explain: "", route: guidedRoute{Intent: guidedPodHealth}, question: "bu neden oluyor?", want: false,
		},
		{
			name:    "explain yalnız boşluk → guided aynen çalışır",
			explain: "  ", route: guidedRoute{Intent: guidedProblems}, question: "açık problemler?", want: false,
		},
		{
			name:    "rota yok → bastırma kararı guided'a ait değil",
			explain: ex, route: guidedRoute{Intent: guidedNone}, question: "bu neden oluyor?", want: false,
		},
		{
			// Somut servis çözüldüyse guided GERÇEK telemetriyi getirir;
			// çekmecede de doğru cevap odur.
			name:    "servis çözüldü → guided kalır",
			explain: ex, route: guidedRoute{Intent: guidedServiceHealth, Service: "checkout"},
			question: "checkout nasıl?", want: false,
		},
		{
			name:    "aile çözüldü → guided kalır",
			explain: ex, route: guidedRoute{Intent: guidedFamilyHealth, Family: []string{"a", "b"}},
			question: "mobile bff'ler nasıl?", want: false,
		},
		{
			// Operatör açıkça filoyu istiyorsa niyet nettir.
			name:    "açık filo kapsamı → guided kalır",
			explain: ex, route: guidedRoute{Intent: guidedProblems}, question: "tüm servislerde bu hata var mı?", want: false,
		},
		{
			name:    "İngilizce filo kapsamı → guided kalır",
			explain: ex, route: guidedRoute{Intent: guidedProblems}, question: "all services affected?", want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := drawerSuppressesGuided(c.explain, c.route, c.question); got != c.want {
				t.Fatalf("got=%v want=%v", got, c.want)
			}
		})
	}
}

func TestDrawerHistoryBlock(t *testing.T) {
	msgs := []copilot.ChatMessage{
		{Role: "user", Text: "Bu exception grubunun kök nedeni ne? (fp1)"},
		{Role: "assistant", Text: "NPE, checkout'ta"},
		{Role: "user", Text: ""}, // tool-result turu — atlanmalı
		{Role: "user", Text: "peki nasıl düzeltirim?"},
	}
	got := drawerHistoryBlock(msgs)
	want := "K: Bu exception grubunun kök nedeni ne? (fp1)\nC: NPE, checkout'ta"
	if got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
	// Aktif soru geçmişe girmemeli (yoksa modele iki kez sorulur).
	if strings.Contains(got, "nasıl düzeltirim") {
		t.Fatal("aktif soru geçmişe sızdı")
	}
}

func TestDrawerHistoryBlockEdges(t *testing.T) {
	if got := drawerHistoryBlock(nil); got != "" {
		t.Fatalf("boş konuşma boş blok vermeli, got=%q", got)
	}
	// Tek turluk konuşma: aktif sorudan önce hiçbir şey yok.
	if got := drawerHistoryBlock([]copilot.ChatMessage{{Role: "user", Text: "bu neden oluyor?"}}); got != "" {
		t.Fatalf("tek tur boş blok vermeli, got=%q", got)
	}
	// Uzun konuşma drawerHistoryTurns ile sınırlanır ve EN YENİ turları
	// tutar (küçük modelde bağlam bütçesi).
	var long []copilot.ChatMessage
	for i := 0; i < 20; i++ {
		long = append(long, copilot.ChatMessage{Role: "user", Text: "s" + string(rune('a'+i))})
		long = append(long, copilot.ChatMessage{Role: "assistant", Text: "c" + string(rune('a'+i))})
	}
	long = append(long, copilot.ChatMessage{Role: "user", Text: "aktif soru"})
	got := drawerHistoryBlock(long)
	if n := len(strings.Split(got, "\n")); n != drawerHistoryTurns {
		t.Fatalf("satır=%d, beklenen=%d", n, drawerHistoryTurns)
	}
	if !strings.HasSuffix(got, "C: ct") { // 20. tur (i=19 → 't')
		t.Fatalf("en yeni tur korunmadı: %q", got)
	}
}

func TestDrawerNarrationUser(t *testing.T) {
	msgs := []copilot.ChatMessage{
		{Role: "user", Text: "Bu exception grubunun kök nedeni ne? (fp1)"},
		{Role: "assistant", Text: "NPE, checkout'ta"},
		{Role: "user", Text: "peki nasıl düzeltirim?"},
	}
	got := drawerNarrationUser("peki nasıl düzeltirim?", "NPE checkout'ta, 34 kez", msgs, "")
	for _, want := range []string{
		"EKRANDAKİ AÇIKLAMA", "NPE checkout'ta, 34 kez",
		"KONUŞMA (K: operatör, C: sen):", "K: Bu exception grubunun kök nedeni ne? (fp1)",
		"SORU: peki nasıl düzeltirim?",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("blokta %q yok:\n%s", want, got)
		}
	}
	// Soru bloğun SONUNDA olmalı — küçük model son talimatı en iyi izler.
	if !strings.HasSuffix(got, "SORU: peki nasıl düzeltirim?") {
		t.Fatalf("soru sonda değil:\n%s", got)
	}
	// Geçmişsiz çağrıda KONUŞMA bölümü hiç yazılmaz.
	solo := drawerNarrationUser("bu neden oluyor?", "NPE", []copilot.ChatMessage{{Role: "user", Text: "bu neden oluyor?"}}, "")
	if strings.Contains(solo, "KONUŞMA") {
		t.Fatalf("boş geçmişte KONUŞMA bölümü yazıldı:\n%s", solo)
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
