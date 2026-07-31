package api

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/cilcenk/coremetry/internal/copilot"
)

// copilot_drawer.go — AI çekmecesi içindeki sohbetin BAĞLAM DEVRİ
// (v0.9.479, operatör raporu).
//
// Semptom (prod fotoğrafı): v0.9.477 AI çekmecesindeki "Chat'te devam
// et →" GLOBAL CoSRE penceresini çekmecenin ÜSTÜNE açıyordu (iki üst
// üste yüzey) ve sohbet ekrandaki açıklamayı BİLMİYORDU — ekranda bir
// exception grubu dururken CoSRE "bu trace ID'ye ait veri yok" deyip
// filo geneli JVM GC uyarılarını anlatıyordu.
//
// Kök neden iki katmanlı:
//  1. Global chat ayrı bir yüzeydi; çekmeceyle arasında `coremetry:
//     ai-ask` üstünden yalnız bir SORU METNİ geçiyordu, açıklama değil.
//  2. Guided router (copilot_guided.go) yalnız SON kullanıcı mesajına
//     bakar ve veriyi KENDİ prefetch eder; konuşma geçmişi narration
//     çağrısına hiç girmez. Yani konuşmaya sentetik bir "önceki cevap"
//     turu eklemek TEK BAŞINA çözmezdi — açıklama metni modele hiç
//     ulaşmazdı. Üstelik özneye oturmayan bir rota (servissiz
//     pod_health) filo geneli veri çekip konuyu tamamen kaçırıyordu.
//
// Çözüm: chat isteğine opsiyonel `context.explain` alanı. Alan BOŞKEN
// her şey bayt-bayt eski davranış (TestGuidedNarrationUserAbsentExplain
// bunu pinler). Doluyken:
//
//	a) guided rota somut bir özneye (servis/aile) oturmuyorsa guided
//	   ATLANIR — filo prefetch'i konuyu ıskalıyordu (drawerSuppressesGuided);
//	b) guided çalışırsa açıklama narration bloğuna ek bağlam olarak
//	   girer, cevap ekrandaki konuya bağlı kalır (guidedNarrationUser);
//	c) guided almazsa buradaki tek-çağrılı explain-grounded yol
//	   cevaplar — guided'ın prefetch+narrate deseninin aynısı, prefetch
//	   yerine operatörün AZ ÖNCE OKUDUĞU açıklama. Küçük model (gemma4)
//	   için doğru şekil bu: tool döngüsü değil, hazır bağlam + tek
//	   anlatım çağrısı.
//
// Not: explain bağlamı varken serbest tool döngüsüne ve RAG'a
// düşülmez — çekmece sohbeti ÖZNE-KAPSAMLIDIR. Filo geneli ve doküman
// soruları global CoSRE penceresinde kalır (tasarım kararı).

// drawerExplainMaxRunes — açıklama bloğunun üst sınırı. Küçük modelde
// bağlam penceresi dar; tipik açıklama 1-3 KB, runbook çıktıları
// taşabilir. Rune bazlı kesilir (Türkçe metin: byte kesmesi karakter
// bölerdi) ve kesildiği modele SÖYLENİR.
const drawerExplainMaxRunes = 3000

// drawerHistoryTurns — narration bloğuna sığdırılan önceki tur sayısı
// (aktif soru hariç). Çekmece sohbeti kısa takiplerden ibarettir;
// tamamı taşınırsa küçük modelin bağlamı açıklamayı bastırır.
const drawerHistoryTurns = 6

const drawerTruncSuffix = "\n…(kısaltıldı)"

// clampDrawerExplain — açıklamayı buda + kırp. Saf; tablo-testli.
func clampDrawerExplain(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= drawerExplainMaxRunes {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:drawerExplainMaxRunes])) + drawerTruncSuffix
}

// drawerSuppressesGuided — explain bağlamı varken guided rotanın
// bırakılıp bırakılmayacağı. Somut özne (servis ya da aile) yoksa
// guided prefetch FİLO GENELİNE düşer ve operatörün ekranındaki konuyu
// ıskalar — operatör raporundaki tam senaryo. Operatör açıkça filo
// istiyorsa ("tüm servislerde bu hata var mı?") guided kalır.
// Saf; tablo-testli.
func drawerSuppressesGuided(explain string, route guidedRoute, question string) bool {
	if strings.TrimSpace(explain) == "" || route.Intent == guidedNone {
		return false
	}
	if route.Service != "" || len(route.Family) > 0 {
		return false
	}
	return !wantsFleetScope(guidedTokens(normalizeGuidedMsg(question)))
}

// guidedNarrationUser — guided narration çağrısının user bloğu.
// explain BOŞKEN üretilen metin v0.9.478'dekiyle bayt-bayt aynıdır
// (regresyon testi bunu pinler); doluyken açıklama ek bir blok olarak
// eklenir — guidedChatPrompt'un "SADECE verilen veriye dayan" kuralı
// böylece açıklamayı da kapsar. Saf; tablo-testli.
func guidedNarrationUser(question, evidence, explain string) string {
	base := "SORU: " + question + "\n\nVERİ:\n" + evidence
	ex := clampDrawerExplain(explain)
	if ex == "" {
		return base
	}
	return base + "\n\nEKRANDAKİ AÇIKLAMA (operatör bu cevabı az önce okudu; soru buna dair olabilir):\n" + ex
}

// drawerChatPrompt — explain-grounded yolun sistem promptu. Türkçe-
// native (2B dersi: İngilizce talimat + Türkçe cevap küçük modelde
// kod-değiştirme vergisi). Guided promptuyla aynı posture, tek farkla:
// veri bloğu sunucu prefetch'i değil, operatörün okuduğu açıklamadır.
const drawerChatPrompt = `Sen Coremetry'nin gözlemlenebilirlik asistanısın. Operatör ekranda bir AI
açıklaması okudu ve AYNI KONU üzerine takip sorusu soruyor.

KURALLAR:
- SADECE sana verilen AÇIKLAMA ve KONUŞMA bloklarına dayan. Yeni servis adı, sayı,
  trace ID ya da metrik UYDURMA.
- Önce sorunun cevabını 1-2 cümlede ver, sonra açıklamadaki somut kanıtı (sayı, id,
  servis adı) göster.
- Açıklamada cevap YOKSA bunu açıkça söyle ve operatöre hangi sayfaya bakması
  gerektiğini öner; tahmin yürütme.
- latency, span, p99, timeout, deploy, trace gibi teknik terimleri ÇEVİRME.
- Kısa ve taranabilir yaz: madde işaretleri kullan, 8 maddeyi geçme.` + copilot.AnswerInTurkish

// drawerHistoryBlock — çekmece içinde sorulmuş önceki turların kompakt
// dökümü (aktif soru HARİÇ; boş metinli tool-result turları atlanır).
// Sıra eskiden yeniye. Saf; tablo-testli.
func drawerHistoryBlock(msgs []copilot.ChatMessage) string {
	// Aktif soru = son, metni dolu kullanıcı turu (lastUserText ile aynı
	// kural). Ondan öncesi geçmiştir.
	cut := len(msgs)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && strings.TrimSpace(msgs[i].Text) != "" {
			cut = i
			break
		}
	}
	var rows []string
	for i := cut - 1; i >= 0 && len(rows) < drawerHistoryTurns; i-- {
		txt := strings.TrimSpace(msgs[i].Text)
		if txt == "" {
			continue
		}
		tag := "C: "
		if msgs[i].Role == "user" {
			tag = "K: "
		}
		rows = append(rows, tag+txt)
	}
	if len(rows) == 0 {
		return ""
	}
	for l, r := 0, len(rows)-1; l < r; l, r = l+1, r-1 {
		rows[l], rows[r] = rows[r], rows[l]
	}
	return strings.Join(rows, "\n")
}

// drawerNarrationUser — explain-grounded yolun user bloğu. Saf;
// tablo-testli.
func drawerNarrationUser(question, explain string, msgs []copilot.ChatMessage) string {
	var b strings.Builder
	b.WriteString("EKRANDAKİ AÇIKLAMA (operatörün az önce okuduğu CoSRE cevabı):\n")
	b.WriteString(clampDrawerExplain(explain))
	if h := drawerHistoryBlock(msgs); h != "" {
		b.WriteString("\n\nKONUŞMA (K: operatör, C: sen):\n")
		b.WriteString(h)
	}
	b.WriteString("\n\nSORU: ")
	b.WriteString(question)
	return b.String()
}

// copilotChatDrawer — çekmece sohbetinin tek-çağrılı cevabı. Guided
// almadıysa ve explain bağlamı varsa çağrılır; handled=true dönerse
// exchange tamamlanmıştır (RAG ve serbest tool döngüsü çalışmaz).
// ai_calls satırı "chat-drawer" yüzeyiyle düşer — /ai sayfası çekmece
// sohbetinin kalitesini guided'dan ayrı izleyebilsin.
func (s *Server) copilotChatDrawer(ctx context.Context, emit func(string, any), msgs []copilot.ChatMessage, explain string) (handled, ok bool) {
	ex := clampDrawerExplain(explain)
	question := strings.TrimSpace(lastUserText(msgs))
	if ex == "" || question == "" {
		return false, false
	}
	// Şeffaflık: guided'ın "bağlam: … (önceki sorudan)" çipiyle aynı dil.
	emitGuidedStep(emit, "bağlam: ekrandaki AI açıklaması", "")

	raw, err := s.copilotStreamSurface(ctx, "chat-drawer", drawerChatPrompt,
		drawerNarrationUser(question, ex, msgs), func(delta string) {
			emit("delta", map[string]string{"text": delta})
		})
	if err != nil {
		emit("error", map[string]string{"error": err.Error()})
		return true, false
	}
	// Deterministik kaynak dipnotu — guided yoldaki "Kaynak: …" ile aynı
	// sözleşme; modele bırakılmaz.
	emit("answer", map[string]any{
		"text":       strings.TrimSpace(raw) + "\n\nKaynak: ekrandaki AI açıklaması",
		"exchangeId": copilot.MetaFromContext(ctx).ExchangeID,
	})
	return true, true
}
