package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

// AI konuşma kalıcılığı — v0.9.1139 (AI Assistant Faz 4.1,
// docs/plans/ai-assistant-design-2026-08-16.md §4-Faz4; A1 operatör
// onayı 2026-08-16: "evet, saved_views blob'u; yeni tablo YOK").
//
// Bugüne kadar global CoSRE penceresi EFEMERDİ: sekme yenilendiğinde
// konuşma yok oluyordu (useChatThread'in dosya başındaki notu). Bu
// dilim onu kalıcı kılar — ama YENİ ŞEMA AÇMADAN.
//
// NEDEN saved_views (invariant #5): "kullanıcının kaydettiği durum"
// için depoda TEK tablo var ve sözleşmesi bu iş için yeterli —
// page='ai-chat', owner_id=kullanıcı, name=başlık, query_string=JSON
// gövde, created_at=SON ETKİNLİK, ReplacingMergeTree(version) her
// upsert'te satırı değiştirir. Yeni bir `ai_conversations` tablosu
// ancak invariant istisnası kararıyla açılabilirdi (tasarım dokümanı
// A1 sorusu) ve operatör istisnayı REDDETTİ.
//
// query_string'in bir URL olmadığı tek yer burası: kolon adı saved
// view mirasından geliyor, tipi String ve gövde bir JSON blob
// (aiChatBlob). Bunu bir alan-adı yanlışlığı sanıp yeni kolon EKLEME —
// karar bilinçli.
//
// created_at = SON ETKİNLİK (kuruluş değil): store'un ListSavedViews
// sıralaması `pinned DESC, created_at DESC` ve thread listesi
// "en son konuşulan üstte" olmak zorunda. Her upsert created_at'i
// tazeler; blob'un `updatedAt` alanı aynı damgayı taşır ki istemci
// listeyi ikinci bir okuma yapmadan gösterebilsin.
//
// Rol kapısı YOK: bu KİŞİSEL durum. Kimliği doğrulanmış her kullanıcı
// (viewer dahil) kendi konuşmasını okur/yazar/siler — invariant #7'nin
// "viewer kendi durumunu GÖRÜR" ruhu. requireCopilot ile de SARILMIYOR
// ve bu kasıtlı: geçmişi okumak LLM istemez, AI kapalıyken de
// konuşma arşivi görünür olmalı (insight.go'nun aynı gerekçesi;
// TestAIConversationRoutesNotCopilotGated pinler).
//
// Denetim (audit) kaydı YOK: kaydetme her tamamlanan alışverişten
// sonra koşar (20 turlu bir sohbet = 20 yazım). ai_feedback.go'daki
// aynı gerekçe geçerli — yüksek frekanslı, kullanıcıya ait durum;
// admin/config mutasyonu değil. saved_view.create denetimi ADLANDIRILMIŞ
// ve başka operatörlerin GÖRDÜĞÜ bir artefakt içindi.
//
// serveCached YOK: bunlar kişisel, mutasyona uğrayan satırlar. Bir TTL
// cache'i "az önce yazdığım turu geri okuyamıyorum" hâlini üretirdi;
// hot-read cache kuralı toplu (aggregate) okumalar içindir.
const (
	// aiChatPage — saved_views ayrımı. Bu değeri DEĞİŞTİRMEK mevcut
	// konuşmaları görünmez kılar (satırlar yaşar, sorgu bulmaz).
	aiChatPage = "ai-chat"
	// aiChatMaxMessages — A1 onayının "son 40 mesaj" tavanı. SUNUCU
	// uygular: istemcinin ne gönderdiği önemsiz (kırpma testli).
	aiChatMaxMessages = 40
	// aiChatMaxBlobBytes — depolanan JSON gövdenin duvarı. 64 KB, 40
	// mesajlık normal bir sohbetin (≈1-2 KB/cevap) rahat üstünde.
	aiChatMaxBlobBytes = 64 << 10
	// aiChatMaxRequestBytes — istek gövdesi duvarı (putBranding
	// emsali). Blob duvarının 4 katı: kırpma öncesi 40'tan fazla mesaj
	// gönderen dürüst bir istemciyi reddetmemek için pay var.
	aiChatMaxRequestBytes = 256 << 10
	// aiChatListLimit — FAB çekmecesindeki "Geçmiş" bölümünün tavanı.
	aiChatListLimit = 50
	// aiChatTitleMaxRunes — otomatik başlık tavanı; RUNE cinsinden
	// (Türkçe başlıklar byte kesmesiyle bozulur).
	aiChatTitleMaxRunes = 60
	// aiChatMaxSubjectRunes — çekmece öznesi kodeği (formatAiParam).
	aiChatMaxSubjectRunes = 200
)

// aiChatMessage — blob'daki tek mesaj. FE'deki ChatMessage ile birebir
// (role + text); araç çağrıları sunucu-içi kalır, arşive girmez.
type aiChatMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// aiChatBlob — query_string kolonunda yaşayan gövde.
type aiChatBlob struct {
	Messages []aiChatMessage `json:"messages"`
	// Subject — çekmece sohbetinin öznesi (`?ai=` kodeği). Global
	// pencere boş bırakır; alan ileriye dönük (Faz 4 tasarımı) ve
	// listede kaynak etiketi olarak gösterilebilir.
	Subject string `json:"subject,omitempty"`
	// UpdatedAt — unix ns; satırın created_at'iyle aynı damga.
	UpdatedAt int64 `json:"updatedAt"`
}

// aiConversationSummary — liste yanıtı. MESAJ GÖVDESİ TAŞIMAZ: çekmece
// listesi başlık + zaman gösteriyor, 50 threadlik mesaj arşivini tele
// koymak ES/CH maliyet disiplininin FE ayağına aykırı olurdu.
type aiConversationSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt int64  `json:"updatedAt"` // unix ns (FE tsRel)
	Messages  int    `json:"messages"`  // mesaj SAYISI
	Subject   string `json:"subject,omitempty"`
}

// aiConversation — tekil okuma + upsert yanıtı (mesajlarla birlikte).
type aiConversation struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	UpdatedAt int64           `json:"updatedAt"`
	Subject   string          `json:"subject,omitempty"`
	Messages  []aiChatMessage `json:"messages"`
}

// ── Saf yardımcılar (table-driven testli: ai_conversations_test.go) ──

// clampChatRunes — rune-güvenli kırpma. Tavanı aşan metin `…` ile
// biter ve SONUÇ tam `max` rune olur (v0.9.842'nin byte-kesme tuzağı:
// `s[:n]` Türkçe metni yarım rune'la bozar).
func clampChatRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

// sanitizeChatMessages — istemciden geleni arşivlenebilir şekle indirir:
// rolü tanınmayan (user/assistant dışı) ve metni boş olan turlar düşer.
// Metin TRİMLENMEZ: kod bloklarının girintisi cevabın parçası.
func sanitizeChatMessages(in []aiChatMessage) []aiChatMessage {
	out := make([]aiChatMessage, 0, len(in))
	for _, m := range in {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		if strings.TrimSpace(m.Text) == "" {
			continue
		}
		out = append(out, m)
	}
	return out
}

// collapseChatWS — başlık için tek satıra indirger (bir soru satır
// sonu taşıyabilir; başlıkta `\n` çizim bozar).
func collapseChatWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// resolveChatTitle — başlığın ÜÇ BASAMAKLI kaynağı:
//
//  1. çağıranın açıkça gönderdiği başlık (ileride "yeniden adlandır");
//  2. satırda ZATEN duran başlık — bir güncelleme başlığı DEĞİŞTİRMEZ.
//     Bu önemli: 40-mesaj penceresi kaydıkça ilk kullanıcı mesajı
//     arşivden düşer ve her kaydetmede başlık kendiliğinden değişirdi
//     (operatör listede aradığı konuşmayı bulamazdı);
//  3. ilk KULLANICI mesajı, tek satıra indirilip 60 rune'a kırpılmış.
//
// Hiçbiri yoksa boş döner — çağıran bunu satır yazmamak için kullanır:
// saved_views'ta name=” TOMBSTONE demektir (DeleteSavedView), yani
// başlıksız bir satır kendini silinmiş gösterir.
func resolveChatTitle(explicit, existing string, msgs []aiChatMessage) string {
	if t := clampChatRunes(collapseChatWS(explicit), aiChatTitleMaxRunes); t != "" {
		return t
	}
	if t := strings.TrimSpace(existing); t != "" {
		return t
	}
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		if t := clampChatRunes(collapseChatWS(m.Text), aiChatTitleMaxRunes); t != "" {
			return t
		}
	}
	return ""
}

// fitChatBlob — depolanacak gövdeyi tavanların içine oturtur ve GERÇEK
// JSON'u döndürür (ölçüm marshal edilmiş hâl üzerinde: subject +
// updatedAt de byte harcıyor, mesajları ayrı ölçmek yalan söylerdi).
//
// İki aşama:
//  1. mesaj sayısı > maxMsgs → EN ESKİLER düşer (son 40 kalır);
//  2. JSON hâlâ maxBytes'ı aşıyorsa en eski mesaj tek tek düşer.
//
// (2) neden var: 40 uzun cevap (ör. 2 KB × 40 = 80 KB) tavanı dürüstçe
// aşabilir. Sabit bir 413, o konuşmanın bir daha ASLA kaydedilmemesi
// demek olurdu — kalıcılık sessizce ölürdü. Duvar yine gerçek: TEK
// mesaj bile sığmıyorsa hata döner (çağıran 413 basar).
func fitChatBlob(blob aiChatBlob, maxMsgs, maxBytes int) (aiChatBlob, string, error) {
	if maxMsgs > 0 && len(blob.Messages) > maxMsgs {
		blob.Messages = blob.Messages[len(blob.Messages)-maxMsgs:]
	}
	for {
		raw, err := json.Marshal(blob)
		if err != nil {
			return blob, "", err
		}
		if len(raw) <= maxBytes {
			return blob, string(raw), nil
		}
		if len(blob.Messages) <= 1 {
			return blob, "", fmt.Errorf(
				"conversation too large: %d bytes > %d KB limit even after trimming to the last message",
				len(raw), maxBytes>>10)
		}
		blob.Messages = blob.Messages[1:]
	}
}

// metaToSummary (v0.9.1192) — CH-tarafı projeksiyon → liste öğesi.
//
// SAHİPLİK, TOMBSTONE, SIRA ve TAVAN artık SQL'de (ListSavedViewMeta:
// owner_id TAM eşitlik — takım kovası yok; name != ''; created_at DESC;
// LIMIT). Eski ownAIConversations bu dört garantiyi Go'da veriyordu ve
// bunun bedeli 50 thread için 64 KB'a kadar blob × 200 satırı CH'den
// taşımaktı — liste yalnız başlık+sayı gösterirken. Garantiler yer
// değiştirdi, kaybolmadı: chstore/saved_view_meta_test.go SQL şeklini,
// buradaki testler dönüşümü pinler.
//
// "Bozuk blob listeyi boşaltmaz" sözleşmesi de taşındı: JSON olmayan /
// eski bir gövdede CH'nin JSONExtract*'ı hata değil varsayılan üretir
// (0 / boş) — satır 0 mesajla, created_at zamanıyla görünür.
func metaToSummary(m chstore.SavedViewMeta) aiConversationSummary {
	sum := aiConversationSummary{
		ID: m.ID, Title: m.Name, UpdatedAt: m.CreatedAt,
		Messages: m.BlobMessages, Subject: m.BlobSubject,
	}
	if m.BlobUpdatedAt > 0 {
		sum.UpdatedAt = m.BlobUpdatedAt
	}
	return sum
}

// ── HTTP katmanı ──

// aiChatOwner — istek sahibinin kullanıcı kimliği; boşsa 401.
//
// NEDEN 401 ve neden "" ile devam ETMİYORUZ: saved_views'ta owner_id=”
// TAKIM-PAYLAŞIMLI demektir (listSavedViews orada bilinçle boş kullanır).
// Kimliksiz bir yazım, kişisel bir sohbeti herkesin görebileceği kovaya
// koyardı. Global auth middleware zaten /api/* için JWT şart koşuyor,
// yani bu dal pratikte ölü — ama ölü kalmasını TİP değil bu kontrol
// garanti eder.
func (s *Server) aiChatOwner(w http.ResponseWriter, r *http.Request) (string, bool) {
	c := auth.FromContext(r.Context())
	if c == nil || strings.TrimSpace(c.UserID) == "" {
		writeJSONError(w, http.StatusUnauthorized, "authentication required")
		return "", false
	}
	return c.UserID, true
}

// aiChatRow — id ile satırı getirir ve ÜÇ koşulu birden doğrular:
// satır var, page='ai-chat', sahibi ben. Aksi hâlde (nil, false) döner
// ve çağıran 404 basar.
//
// page kontrolü GÜVENLİK, kozmetik değil: bu uçlar saved_views'ın TAMAMI
// üzerinde çalışıyor. Kontrol olmasa DELETE /api/ai/conversations/{id}
// operatörün kayıtlı bir /traces GÖRÜNÜMÜNÜ silebilirdi — hem yanlış
// yüzeyden, hem saved_view.delete denetim kaydı olmadan.
func (s *Server) aiChatRow(r *http.Request, id, ownerID string) (*chstore.SavedView, error) {
	cur, err := s.store.GetSavedView(r.Context(), id)
	if err != nil {
		return nil, err
	}
	if cur == nil || cur.Page != aiChatPage || cur.OwnerID != ownerID {
		return nil, nil
	}
	return cur, nil
}

// listAIConversations — GET /api/ai/conversations.
//
// Not (maliyet): ListSavedViews query_string'i de SELECT ediyor, yani
// gövdeler CH'den geliyor ama tele KONMUYOR (özet çıkarılır). Store'a
// projeksiyonlu ikinci bir metot eklemek invariant #5'in "yüzey başına
// şema yok" ruhuna daha yakın olmadığı için tercih edilmedi; kişisel
// thread sayısı düzinelerle ölçülür ve blob 64 KB ile sınırlı.
func (s *Server) listAIConversations(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := s.aiChatOwner(w, r)
	if !ok {
		return
	}
	metas, err := s.store.ListSavedViewMeta(r.Context(), ownerID, aiChatPage, aiChatListLimit)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]aiConversationSummary, 0, len(metas))
	for _, m := range metas {
		out = append(out, metaToSummary(m))
	}
	writeJSON(w, out)
}

// getAIConversation — GET /api/ai/conversations/{id}.
func (s *Server) getAIConversation(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := s.aiChatOwner(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "id required")
		return
	}
	cur, err := s.aiChatRow(r, id, ownerID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if cur == nil {
		// 403 DEĞİL 404: başkasının thread'i için "yasak" demek onun
		// VARLIĞINI doğrulardı. Kişisel bir ad alanında görülemeyen
		// satırın dürüst cevabı "yok".
		writeJSONError(w, http.StatusNotFound, "conversation not found")
		return
	}
	var blob aiChatBlob
	if err := json.Unmarshal([]byte(cur.QueryString), &blob); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "stored conversation is unreadable: "+err.Error())
		return
	}
	writeJSON(w, aiConversation{
		ID: cur.ID, Title: cur.Name, Subject: blob.Subject,
		UpdatedAt: conversationStamp(blob.UpdatedAt, cur.CreatedAt),
		Messages:  blob.Messages,
	})
}

// conversationStamp — blob damgası varsa o, yoksa satırın created_at'i
// (blob'suz/eski satırlar için dürüst geri düşüş).
func conversationStamp(blobStamp, rowStamp int64) int64 {
	if blobStamp > 0 {
		return blobStamp
	}
	return rowStamp
}

// upsertAIConversation — POST /api/ai/conversations.
//
// İSTEMCİ SÜRÜCÜLÜ: chat her tamamlanan alışverişten sonra çağırır
// (SSE handler'ının içinde sunucu-tarafı otomatik kayıt YOK —
// copilot_chat.go'ya bu dilimde tek satır dokunulmadı; akış yolunda
// yazım, iptal edilen/hata veren bir akışı da kalıcılaştırırdı ve
// kesilen SSE'de yarım tur arşive girerdi).
//
// Kimlik: SUNUCU basar. Gövdedeki `id` boşsa yeni satır, doluysa aynı
// satır güncellenir. Doluyken satır BULUNAMAZSA (başka sekmede
// silinmiş) yeni kimlikle YENİ satır açılır ve yanıt onu döner —
// istemci kimliği yanıttan devralır. Silinmiş bir thread'in kaydetmeyi
// kalıcı olarak wedge etmesi kabul edilemez.
func (s *Server) upsertAIConversation(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := s.aiChatOwner(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, aiChatMaxRequestBytes)
	var body struct {
		ID       string          `json:"id"`
		Title    string          `json:"title"`
		Subject  string          `json:"subject"`
		Messages []aiChatMessage `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("invalid body (or > %d KB): %v", aiChatMaxRequestBytes>>10, err))
		return
	}
	msgs := sanitizeChatMessages(body.Messages)
	if len(msgs) == 0 {
		writeJSONError(w, http.StatusBadRequest, "messages required")
		return
	}

	// Mevcut satır (varsa) — başlığı ve pinned'ı ileriye taşımak için.
	// invariant #4: ReplacingMergeTree tam-satır değiştirir, taşımadığın
	// alan SIFIRLANIR.
	var cur *chstore.SavedView
	id := strings.TrimSpace(body.ID)
	if id != "" {
		found, err := s.store.GetSavedView(r.Context(), id)
		if err != nil {
			writeErr(w, err)
			return
		}
		switch {
		case found == nil:
			id = "" // silinmiş → yeni kimlik basılır
		case found.Page != aiChatPage || found.OwnerID != ownerID:
			// Başkasının satırına (ya da bir saved VIEW'a) yazma girişimi.
			writeJSONError(w, http.StatusNotFound, "conversation not found")
			return
		default:
			cur = found
		}
	}
	if id == "" {
		// Store'un kendi minter'ı da 8 byte hex (newSavedViewID); burada
		// basıyoruz çünkü yanıtın kimliği taşıması gerekiyor.
		id = newRandID(8)
	}

	now := time.Now().UnixNano()
	existingTitle := ""
	pinned := false
	if cur != nil {
		existingTitle, pinned = cur.Name, cur.Pinned
	}
	blob := aiChatBlob{
		Messages:  msgs,
		Subject:   clampChatRunes(body.Subject, aiChatMaxSubjectRunes),
		UpdatedAt: now,
	}
	fitted, raw, err := fitChatBlob(blob, aiChatMaxMessages, aiChatMaxBlobBytes)
	if err != nil {
		writeJSONError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	title := resolveChatTitle(body.Title, existingTitle, fitted.Messages)
	if title == "" {
		// name='' tombstone demek — başlıksız satır kendini silinmiş
		// gösterirdi. Pratikte ulaşılmaz (mesaj var = kullanıcı turu var
		// olabilir; ilk tur asistan ise başlık düşer), yine de dürüst
		// bir yedek ad basıyoruz.
		title = "Konuşma"
	}

	v := chstore.SavedView{
		ID: id, OwnerID: ownerID, Name: title, Page: aiChatPage,
		QueryString: raw, Pinned: pinned,
		// created_at = SON ETKİNLİK (dosya başındaki gerekçe): liste
		// sıralaması buna dayanıyor.
		CreatedAt: now,
	}
	if err := s.store.UpsertSavedView(r.Context(), v); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, aiConversation{
		ID: id, Title: title, Subject: fitted.Subject,
		UpdatedAt: now, Messages: fitted.Messages,
	})
}

// deleteAIConversation — DELETE /api/ai/conversations/{id}.
// Tombstone (name=”) yazar — DeleteSavedView deseni.
func (s *Server) deleteAIConversation(w http.ResponseWriter, r *http.Request) {
	ownerID, ok := s.aiChatOwner(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "id required")
		return
	}
	cur, err := s.aiChatRow(r, id, ownerID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if cur == nil {
		// Zaten yok / benim değil → idempotent 204 DEĞİL 404: istemci
		// "sildim" sanıp listeden düşürmesin diye değil, varlık sızdırma
		// yüzeyi olmasın diye tek tip cevap (getAIConversation ile aynı).
		writeJSONError(w, http.StatusNotFound, "conversation not found")
		return
	}
	if err := s.store.DeleteSavedView(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
