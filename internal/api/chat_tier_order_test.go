package api

import (
	"os"
	"strings"
	"testing"
)

// chat_tier_order_test.go — v0.10.30, Copilot denetiminin kapsam
// eleştirmeninden.
//
// ── NEDEN BU KAPI VAR ───────────────────────────────────────────────────
//
// Sohbetin merkezî sözleşmesi bir SIRA:
//
//	guided (canlı telemetri) > drawer bağlamı > RAG dokümanları > serbest tool döngüsü
//
// Sıra, cevabın nereden geleceğini belirliyor ve her kademenin farklı
// bir doğruluk profili var: guided deterministik prefetch yapıyor,
// serbest döngü modele en çok özgürlük veriyor. Biri drawer'ı guided'ın
// üstüne taşısa ya da RAG'i öne alsa, aynı soru BAŞKA bir kaynaktan
// cevaplanmaya başlar — ve hiçbir gate kırılmazdı.
//
// Çünkü sıra, yalnız üç `if handled { return }` bloğunun FİZİKSEL
// YERİNDEN ibaretti. Karşılaştırma: aynı dosyanın komşu sözleşmeleri
// kaynak-pinli (mcp_authz_test.go rol süzgecini, chat_tool_budget_test.go
// bütçeyi, tool_error_contract_test.go hata sözleşmesini tarıyor).
// Mekanizma depoda VAR ve tam bu dosyada üç kez kullanılmış; kademe
// sırasına uygulanmamıştı.
//
// Hafızadaki "gate tek-yazım kör noktası" ve "muhafız dilime bağlanınca"
// dersleriyle aynı sınıf: sözleşme koda yazılı ama hiçbir şey onu
// zorlamıyor.

// tierMarkers — kademelerin sıradaki KİMLİKLERİ.
//
// Her biri o kademeyi çağıran satırın ayırt edici parçası. Adlar
// değişirse test kırmızıya döner ve bu DOĞRU davranış: sırayı taşıyan
// çağrıların yeniden adlandırılması, sözleşmenin gözden geçirilmesi
// gereken bir andır.
var tierMarkers = []struct{ name, marker string }{
	{"guided (canlı telemetri)", "s.copilotChatGuided(ctx, emit,"},
	{"drawer (ekrandaki açıklama)", "s.copilotChatDrawer(ctx, emit,"},
	{"RAG (yüklü dokümanlar)", "s.ragChatAnswer(ctx, emit,"},
	{"serbest tool döngüsü", "toolsForRole(mcptools.ToolList("},
}

func TestChatTierOrderIsPinned(t *testing.T) {
	b, err := os.ReadFile("copilot_chat.go")
	if err != nil {
		t.Fatalf("copilot_chat.go okunamadı: %v", err)
	}
	src := stripGoCommentsAPI(string(b))

	pos := make([]int, len(tierMarkers))
	for i, tm := range tierMarkers {
		pos[i] = strings.Index(src, tm.marker)
		if pos[i] < 0 {
			t.Fatalf("%s kademesi bulunamadı (%q) — kademe SİLİNMİŞ ya da "+
				"yeniden adlandırılmış olabilir; sözleşme gözden geçirilmeli",
				tm.name, tm.marker)
		}
	}
	for i := 1; i < len(pos); i++ {
		if pos[i] <= pos[i-1] {
			t.Errorf("SIRA BOZULDU: %q, %q kademesinden ÖNCE geliyor.\n"+
				"  Sözleşme: guided > drawer > RAG > serbest döngü.\n"+
				"  Sıra cevabın hangi KAYNAKTAN geleceğini belirliyor; değiştirmek "+
				"aynı soruyu başka bir doğruluk profiliyle cevaplamaktır.",
				tierMarkers[i].name, tierMarkers[i-1].name)
		}
	}
}

// TestEarlyTiersReturnBeforeTheLoop — sıranın ANLAMI.
//
// Sıra tek başına yetmez: ilk üç kademe `handled` olduğunda DÖNMELİ.
// Dönmezlerse serbest döngü de koşar, model aynı soruya ikinci bir cevap
// üretir ve operatör iki farklı cevabı arka arkaya görür.
func TestEarlyTiersReturnBeforeTheLoop(t *testing.T) {
	b, err := os.ReadFile("copilot_chat.go")
	if err != nil {
		t.Fatalf("okunamadı: %v", err)
	}
	src := stripGoCommentsAPI(string(b))

	for _, tm := range tierMarkers[:3] {
		i := strings.Index(src, tm.marker)
		if i < 0 {
			t.Fatalf("%s bulunamadı", tm.name)
		}
		// ⚠ PENCERE, BLOĞUN KENDİ SINIRI OLMALI. İlk yazımda sabit 400
		// karakterlik bir pencere kullanmıştım ve test ISIRMADI: pencere
		// KOMŞU kademenin `return`ünü görüyordu, yani bir kademe kendi
		// return'ünü kaybetse bile yeşil kalıyordu. Mutasyon denetimi
		// yakaladı. Sınır artık `if` bloğunun kapanışı.
		// İki iddia İKİ FARKLI yere bakıyor: `handled` KOŞULDA, `return`
		// GÖVDEDE. İlk yazımda ikisini de gövdede aramıştım ve `handled`
		// kontrolü temiz ağaçta bile kırmızı oldu — kapı, koşulu gövde
		// sanıyordu.
		cond := src[i:]
		if br := strings.Index(cond, "{"); br > 0 {
			cond = cond[:br]
		}
		if !strings.Contains(cond, "handled") {
			t.Errorf("%s kademesi `handled` bayrağını okumuyor — kademe her hâlükârda "+
				"devrediyor ya da her hâlükârda sahipleniyor olabilir", tm.name)
		}
		body := tierBlock(src, i)
		if !strings.Contains(body, "return") {
			t.Errorf("%s kademesi `handled` olduğunda DÖNMÜYOR — serbest döngü de "+
				"koşar ve aynı soruya ikinci bir cevap üretilir", tm.name)
		}
	}
}

// TestGuidedIsNotACatchAll — sıranın DİĞER ucu.
//
// Sıra ancak erken kademeler DÜŞEBİLİYORSA anlamlı. Guided her soruyu
// sahiplenseydi serbest döngü ölü kod olurdu ve /api/ai/router-gaps'ın
// (guided'ın kaçırdıklarını sayan uç) tüm varlık sebebi kalmazdı.
//
// v0.10.14 tam bu sınıftan bir kusurdu: RAG cevaplayamadığı soruyu da
// sahipleniyordu ve serbest döngüye hiç sıra gelmiyordu.
func TestGuidedIsNotACatchAll(t *testing.T) {
	b, err := os.ReadFile("copilot_guided.go")
	if err != nil {
		t.Fatalf("copilot_guided.go okunamadı: %v", err)
	}
	src := stripGoCommentsAPI(string(b))
	// Sinyalsiz soruda guided DÜŞMELİ.
	if !strings.Contains(src, "return false, false") {
		t.Error("guided hiçbir koşulda düşmüyor — serbest döngü ölü kod olur, " +
			"router-gaps ölçümü anlamsızlaşır (v0.10.14 sınıfı)")
	}
}

// tierBlock — `if handled …{` satırından o bloğun KAPANIŞINA kadar.
//
// Sabit karakter penceresi yerine gerçek sınır: komşu kademenin gövdesi
// bu bloğa sızmamalı, yoksa kapı yanlış yerdeki bir `return`ü kendi
// kanıtı sanar.
func tierBlock(src string, start int) string {
	rest := src[start:]
	open := strings.Index(rest, "{")
	if open < 0 {
		return ""
	}
	depth := 0
	for i := open; i < len(rest); i++ {
		switch rest[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return rest[open : i+1]
			}
		}
	}
	return rest
}
