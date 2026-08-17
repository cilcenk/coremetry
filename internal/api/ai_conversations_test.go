package api

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// v0.9.1139 (AI Assistant Faz 4.1) — konuşma kalıcılığının SÖZLEŞME
// pinleri. Dört sınıf iddia var ve her biri sessizce kırılabilir:
//
//  1. TAVAN sunucuda: A1 onayı "son 40 mesaj" dedi. İstemci 200 mesaj
//     gönderirse blob 200 mesajla büyür — tip hatası vermez, sadece
//     satır şişer ve 64 KB duvarına dayanır;
//  2. SAHİPLİK: ListSavedViews bilinçli olarak owner_id='' PAYLAŞIMLI
//     kovayı da döndürür. `(owner_id = ? OR owner_id = '')` mantığını
//     konuşmalara taşımak, bir kullanıcının sohbetini herkese açardı.
//     Süzgeç eşitlik olmak ZORUNDA;
//  3. BAŞLIK KARARLILIĞI: 40'lık pencere kaydıkça ilk kullanıcı mesajı
//     arşivden düşer. Başlığı her kaydetmede yeniden türetmek, listedeki
//     adı operatörün altından değiştirirdi;
//  4. page='ai-chat' süzgeci GÜVENLİK: uçlar saved_views'ın tamamı
//     üzerinde çalışıyor, kontrol olmadan bir konuşma ucu kayıtlı
//     GÖRÜNÜM silebilir.

func chatMsgs(n int) []aiChatMessage {
	out := make([]aiChatMessage, 0, n)
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		out = append(out, aiChatMessage{Role: role, Text: fmt.Sprintf("m%d", i)})
	}
	return out
}

func TestFitChatBlobCap(t *testing.T) {
	tests := []struct {
		name     string
		in       []aiChatMessage
		maxMsgs  int
		maxBytes int
		wantLen  int
		wantErr  bool
		// wantFirst/wantLast — hangi UÇTAN kırpıldığını pinler. Sadece
		// uzunluğa bakmak, "en YENİleri at" hatasını yeşil geçirir.
		wantFirst string
		wantLast  string
	}{
		{
			name: "tavanın altı dokunulmaz", in: chatMsgs(6),
			maxMsgs: aiChatMaxMessages, maxBytes: aiChatMaxBlobBytes,
			wantLen: 6, wantFirst: "m0", wantLast: "m5",
		},
		{
			name: "tam tavan dokunulmaz", in: chatMsgs(40),
			maxMsgs: aiChatMaxMessages, maxBytes: aiChatMaxBlobBytes,
			wantLen: 40, wantFirst: "m0", wantLast: "m39",
		},
		{
			name: "tavan+1 → en ESKİ düşer", in: chatMsgs(41),
			maxMsgs: aiChatMaxMessages, maxBytes: aiChatMaxBlobBytes,
			wantLen: 40, wantFirst: "m1", wantLast: "m40",
		},
		{
			name: "istemci 200 gönderse de 40 kalır", in: chatMsgs(200),
			maxMsgs: aiChatMaxMessages, maxBytes: aiChatMaxBlobBytes,
			wantLen: 40, wantFirst: "m160", wantLast: "m199",
		},
		{
			// Byte duvarı: 40 uzun cevap tavanı dürüstçe aşabilir; en
			// eskiler tek tek düşer, kalıcılık ÖLMEZ.
			name: "byte duvarı en eskileri düşürür",
			in: []aiChatMessage{
				{Role: "user", Text: strings.Repeat("a", 400)},
				{Role: "assistant", Text: strings.Repeat("b", 400)},
				{Role: "user", Text: strings.Repeat("c", 400)},
			},
			// 500 byte ≈ TEK 400-karakterlik mesaj + zarf; ikincisi sığmaz.
			maxMsgs: aiChatMaxMessages, maxBytes: 500,
			wantLen: 1, wantFirst: strings.Repeat("c", 400), wantLast: strings.Repeat("c", 400),
		},
		{
			name:    "tek mesaj bile sığmıyorsa hata (413)",
			in:      []aiChatMessage{{Role: "user", Text: strings.Repeat("x", 5000)}},
			maxMsgs: aiChatMaxMessages, maxBytes: 1024,
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, raw, err := fitChatBlob(aiChatBlob{Messages: tc.in, UpdatedAt: 1}, tc.maxMsgs, tc.maxBytes)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("hata bekleniyordu, %d mesaj döndü", len(got.Messages))
				}
				return
			}
			if err != nil {
				t.Fatalf("beklenmeyen hata: %v", err)
			}
			if len(got.Messages) != tc.wantLen {
				t.Fatalf("mesaj sayısı = %d, beklenen %d", len(got.Messages), tc.wantLen)
			}
			if got.Messages[0].Text != tc.wantFirst {
				t.Errorf("ilk mesaj = %q, beklenen %q (yanlış uçtan kırpıldı)",
					got.Messages[0].Text, tc.wantFirst)
			}
			if got.Messages[len(got.Messages)-1].Text != tc.wantLast {
				t.Errorf("son mesaj = %q, beklenen %q", got.Messages[len(got.Messages)-1].Text, tc.wantLast)
			}
			if len(raw) > tc.maxBytes {
				t.Errorf("JSON %d byte, duvar %d", len(raw), tc.maxBytes)
			}
			// Döndürülen JSON, döndürülen blob'un ta kendisi olmalı —
			// çağıran onu doğrudan query_string'e yazıyor.
			var back aiChatBlob
			if err := json.Unmarshal([]byte(raw), &back); err != nil {
				t.Fatalf("dönen JSON çözümlenemedi: %v", err)
			}
			if len(back.Messages) != len(got.Messages) {
				t.Errorf("JSON %d mesaj taşıyor, blob %d", len(back.Messages), len(got.Messages))
			}
		})
	}
}

func TestResolveChatTitle(t *testing.T) {
	long := strings.Repeat("ç", 80) // 80 rune, 160 byte — byte kesmesi bozar
	tests := []struct {
		name     string
		explicit string
		existing string
		msgs     []aiChatMessage
		want     string
	}{
		{
			name: "ilk KULLANICI mesajından türer",
			msgs: []aiChatMessage{
				{Role: "user", Text: "checkout neden yavaş?"},
				{Role: "assistant", Text: "db çağrısı"},
			},
			want: "checkout neden yavaş?",
		},
		{
			name: "asistanla başlayan arşivde ilk KULLANICI turu bulunur",
			msgs: []aiChatMessage{
				{Role: "assistant", Text: "merhaba"},
				{Role: "user", Text: "p1 problemler?"},
			},
			want: "p1 problemler?",
		},
		{
			name:     "açık başlık her şeyi yener",
			explicit: "Deploy incelemesi", existing: "eski",
			msgs: []aiChatMessage{{Role: "user", Text: "başka soru"}},
			want: "Deploy incelemesi",
		},
		{
			// Sözleşmenin kalbi: güncelleme başlığı DEĞİŞTİRMEZ.
			name:     "mevcut başlık korunur (pencere kaysa bile)",
			existing: "checkout neden yavaş?",
			msgs:     []aiChatMessage{{Role: "user", Text: "peki loglar?"}},
			want:     "checkout neden yavaş?",
		},
		{
			name: "satır sonları tek satıra iner",
			msgs: []aiChatMessage{{Role: "user", Text: "  ilk satır\n\tikinci   satır  "}},
			want: "ilk satır ikinci satır",
		},
		{
			name: "60 rune tavanı — rune-güvenli",
			msgs: []aiChatMessage{{Role: "user", Text: long}},
			want: strings.Repeat("ç", 59) + "…",
		},
		{
			name: "kullanıcı turu yok → boş (çağıran yedek ad basar)",
			msgs: []aiChatMessage{{Role: "assistant", Text: "yalnız cevap"}},
			want: "",
		},
		{name: "her şey boş → boş"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveChatTitle(tc.explicit, tc.existing, tc.msgs)
			if got != tc.want {
				t.Fatalf("başlık = %q, beklenen %q", got, tc.want)
			}
			if n := len([]rune(got)); n > aiChatTitleMaxRunes {
				t.Errorf("başlık %d rune — tavan %d", n, aiChatTitleMaxRunes)
			}
		})
	}
}

func TestClampChatRunes(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"kısa", 10, "kısa"},
		{"  boşluklar  ", 20, "boşluklar"},
		{"abcdef", 6, "abcdef"},
		{"abcdef", 5, "abcd…"},
		{"ğğğğğ", 3, "ğğ…"}, // çok-byte'lı rune: byte kesmesi bozuk UTF-8 üretir
		{"abc", 1, "a"},
		{"", 5, ""},
	}
	for _, tc := range tests {
		got := clampChatRunes(tc.in, tc.max)
		if got != tc.want {
			t.Errorf("clampChatRunes(%q, %d) = %q, beklenen %q", tc.in, tc.max, got, tc.want)
		}
		if n := len([]rune(got)); n > tc.max {
			t.Errorf("clampChatRunes(%q, %d) = %d rune", tc.in, tc.max, n)
		}
	}
}

func TestSanitizeChatMessages(t *testing.T) {
	in := []aiChatMessage{
		{Role: "user", Text: "geçerli"},
		{Role: "system", Text: "rol tanınmıyor"},  // düşer
		{Role: "assistant", Text: ""},             // düşer
		{Role: "assistant", Text: "   "},          // düşer
		{Role: "assistant", Text: "  girintili "}, // KALIR, trimlenmez
		{Role: "tool", Text: "araç çıktısı"},      // düşer
	}
	got := sanitizeChatMessages(in)
	if len(got) != 2 {
		t.Fatalf("%d mesaj kaldı, beklenen 2: %+v", len(got), got)
	}
	if got[1].Text != "  girintili " {
		t.Errorf("metin trimlenmiş (%q) — kod bloğu girintisi cevabın parçası", got[1].Text)
	}
}

func TestOwnAIConversations(t *testing.T) {
	rows := []chstore.SavedView{
		{ID: "mine-old", OwnerID: "u1", Page: aiChatPage, Name: "eski", CreatedAt: 100},
		{ID: "theirs", OwnerID: "u2", Page: aiChatPage, Name: "başkasının", CreatedAt: 900},
		{ID: "shared", OwnerID: "", Page: aiChatPage, Name: "paylaşımlı kova", CreatedAt: 800},
		{ID: "mine-new", OwnerID: "u1", Page: aiChatPage, Name: "yeni", CreatedAt: 300},
		{ID: "a-view", OwnerID: "u1", Page: "traces", Name: "kayıtlı görünüm", CreatedAt: 999},
		{ID: "tomb", OwnerID: "u1", Page: aiChatPage, Name: "", CreatedAt: 500},
	}

	got := ownAIConversations(rows, "u1", aiChatListLimit)
	var ids []string
	for _, v := range got {
		ids = append(ids, v.ID)
	}
	// Yeni → eski sıra + yalnız kendi ai-chat satırları.
	if strings.Join(ids, ",") != "mine-new,mine-old" {
		t.Fatalf("id'ler = %v; beklenen [mine-new mine-old] (sahiplik + sayfa + tombstone + sıra)", ids)
	}

	// u2, u1'in thread'ini GÖREMEZ.
	for _, v := range ownAIConversations(rows, "u2", aiChatListLimit) {
		if v.OwnerID != "u2" {
			t.Fatalf("u2, %s sahipli satırı gördü (%s)", v.OwnerID, v.ID)
		}
	}

	// Kimliksiz istek paylaşımlı kovayı okumamalı.
	if got := ownAIConversations(rows, "", aiChatListLimit); len(got) != 0 {
		t.Errorf("boş ownerID %d satır döndürdü — owner_id='' PAYLAŞIMLI koCadır", len(got))
	}
	if got := ownAIConversations(rows, "   ", aiChatListLimit); len(got) != 0 {
		t.Errorf("boşluklu ownerID %d satır döndürdü", len(got))
	}

	// Tavan EN YENİLERİ tutar.
	many := make([]chstore.SavedView, 0, 60)
	for i := 0; i < 60; i++ {
		many = append(many, chstore.SavedView{
			ID: fmt.Sprintf("c%02d", i), OwnerID: "u1", Page: aiChatPage,
			Name: "k", CreatedAt: int64(i),
		})
	}
	capped := ownAIConversations(many, "u1", aiChatListLimit)
	if len(capped) != aiChatListLimit {
		t.Fatalf("tavan = %d, beklenen %d", len(capped), aiChatListLimit)
	}
	if capped[0].ID != "c59" || capped[len(capped)-1].ID != "c10" {
		t.Errorf("tavan yanlış uçtan kesti: ilk=%s son=%s", capped[0].ID, capped[len(capped)-1].ID)
	}
}

func TestSummarizeAIConversation(t *testing.T) {
	blob, _ := json.Marshal(aiChatBlob{
		Messages: chatMsgs(4), Subject: "svc:checkout", UpdatedAt: 4242,
	})
	got := summarizeAIConversation(chstore.SavedView{
		ID: "c1", Name: "başlık", Page: aiChatPage, QueryString: string(blob), CreatedAt: 7,
	})
	if got.Messages != 4 || got.Subject != "svc:checkout" || got.UpdatedAt != 4242 {
		t.Fatalf("özet = %+v", got)
	}
	// Özet mesaj GÖVDESİ taşımaz (liste maliyeti) — JSON'da `messages`
	// bir SAYI olmalı.
	raw, _ := json.Marshal(got)
	if !strings.Contains(string(raw), `"messages":4`) {
		t.Errorf("liste öğesi mesaj sayısı yerine gövde taşıyor: %s", raw)
	}

	// Bozuk gövde başlığı/zamanı DÜŞÜRMEZ.
	bad := summarizeAIConversation(chstore.SavedView{
		ID: "c2", Name: "yaşayan başlık", Page: aiChatPage, QueryString: "{bozuk", CreatedAt: 11,
	})
	if bad.Title != "yaşayan başlık" || bad.UpdatedAt != 11 || bad.Messages != 0 {
		t.Fatalf("bozuk blob özeti = %+v", bad)
	}
}

// TestAIConversationRoutesNotCopilotGated — namespace + kapı kararının
// kaynak pini (TestInsightRouteNotCopilotGated emsali).
//
// Buradaki DOĞRU hâl "sarılmamış olmak" ve bir sonraki geliştirici bunu
// eksik kapı sanabilir: geçmişi okumak/silmek LLM istemez, AI kapalıyken
// arşiv kaybolmuş görünmemeli. Rol kapısı da yok — kişisel durum
// (invariant #7: viewer kendi durumunu GÖRÜR).
func TestAIConversationRoutesNotCopilotGated(t *testing.T) {
	b, err := os.ReadFile("ai_routes.go")
	if err != nil {
		t.Fatalf("ai_routes.go okunamadı: %v", err)
	}
	found := 0
	for _, line := range strings.Split(string(b), "\n") {
		code := line
		if i := strings.Index(code, "//"); i >= 0 {
			code = code[:i]
		}
		if !strings.Contains(code, "/api/ai/conversations") {
			continue
		}
		found++
		if strings.Contains(code, "s.requireCopilot(") {
			t.Errorf("konuşma route'u requireCopilot ile sarılmış: %s\n"+
				"Geçmiş LLM'siz de okunmalı (ai_conversations.go dosya başı).",
				strings.TrimSpace(line))
		}
		if strings.Contains(code, "auth.RequireRole") || strings.Contains(code, "auth.RequireAnyRole") {
			t.Errorf("konuşma route'una rol kapısı eklenmiş: %s\n"+
				"Kişisel durum: viewer da kendi sohbetini saklar.", strings.TrimSpace(line))
		}
	}
	if found != 4 {
		t.Errorf("ai_routes.go'da %d konuşma route'u bulundu, beklenen 4 (list/upsert/get/delete)", found)
	}
}
