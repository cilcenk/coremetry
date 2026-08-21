package api

// rag_curate.go — KB TERFİ KUYRUĞU (v0.9.1196, AI Faz 5.2, onaylı plan).
//
// Döngünün kapanan halkası: operatör 👍 verdi (ai_feedback) → cevap bu
// kuyruğa aday düştü → admin Knowledge sekmesinde inceleyip "KB'ye ekle"
// dedi → Soru+Cevap, rag_chunks'a source='curated' olarak girdi → RAG bir
// dahaki benzer soruda onu bulur. KB operatör ONAYIYLA büyür — 👍'lı her
// şeyi otomatik almak, tek yanlış-pozitif oyla bilgi tabanını
// zehirlemek olurdu; onay listesi tam bu yüzden var (plan kabulü).
//
// Terfi işareti = rag_chunks'taki source_ref (exchange_id). Ayrı bir
// "promoted" tablosu YOK: chunk'ın varlığı işaretin kendisi, silinirse
// aday listeye geri düşer — ki bu doğru davranış (yanlışlıkla silinen
// bir küratörlük yeniden terfi edilebilir kalmalı).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

// curatedDocTitle — adayın Knowledge listesindeki adı: prompt'un tek
// satıra indirilmiş, 60 RUNE'a kırpılmış başı. SAF, tablo-testli.
// Rune, bayt değil: Türkçe bir soru bayt kesmesiyle karakter ortasından
// bölünürdü (aiChatTitleMaxRunes ile aynı gerekçe ve aynı tavan).
func curatedDocTitle(prompt string) string {
	t := strings.Join(strings.Fields(prompt), " ") // satır sonları + çoklu boşluk tek boşluğa
	if t == "" {
		return "KB: (sorusuz cevap)"
	}
	r := []rune(t)
	if len(r) > 60 {
		t = string(r[:60]) + "…"
	}
	return "KB: " + t
}

// curatedDocText — chunk'lanacak gövde. Soru ve cevap AYRI başlıklarla:
// retrieval'da chunk tek başına okunur ve hangi yarının soru olduğu
// içerikten anlaşılmalı; yüzey etiketi de bağlam taşır ("trace açıklaması"
// ile "log deseni" cevabı farklı türden bilgidir).
func curatedDocText(surface, prompt, response string) string {
	var b strings.Builder
	b.WriteString("Soru:\n")
	b.WriteString(strings.TrimSpace(prompt))
	b.WriteString("\n\nCevap (operatör onaylı")
	if surface != "" {
		b.WriteString(", yüzey: " + surface)
	}
	b.WriteString("):\n")
	b.WriteString(strings.TrimSpace(response))
	return b.String()
}

// listKBCandidates — GET /api/rag/candidates?rangeS=. Admin paneli
// okuması; negatif ikizle (listNegativeAIFeedback) aynı cache disiplini.
func (s *Server) listKBCandidates(w http.ResponseWriter, r *http.Request) {
	rangeS := int64(30 * 86400)
	if v := r.URL.Query().Get("rangeS"); v != "" {
		if n := parseInt(v, 0); n > 0 && n <= 90*86400 {
			rangeS = int64(n)
		}
	}
	key := fmt.Sprintf("rag:kbcand:v1:range=%d", rangeS)
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		to := time.Now()
		rows, err := s.store.ListKBCandidates(ctx, to.Add(-time.Duration(rangeS)*time.Second), to, 100)
		if err != nil {
			return nil, err
		}
		if rows == nil {
			rows = []chstore.KBCandidate{}
		}
		return map[string]any{"rows": rows, "rangeS": rangeS}, nil
	})
}

// curateKBCandidate — POST /api/rag/curate {exchangeId}.
//
// İçerik LİSTEDEN DEĞİL kaynaktan okunur (AICallSampleByExchange):
// 60 sn'lik liste cache'i bayat olabilir ve terfi eden şey her zaman
// ai_calls'taki gerçek örnek olmalı. Upload'la aynı kapılar: RAG etkin
// değilse 503, doküman tavanı doluysa 400 — curated dokümanlar da aynı
// katalogda yaşıyor ve tavanı sessizce delemez.
func (s *Server) curateKBCandidate(w http.ResponseWriter, r *http.Request) {
	if !s.rag.Enabled() {
		http.Error(w, "RAG etkin değil (Settings → AI → RAG)", http.StatusServiceUnavailable)
		return
	}
	var in struct {
		ExchangeID string `json:"exchangeId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	xid := strings.TrimSpace(in.ExchangeID)
	if xid == "" || len(xid) > aiFeedbackMaxIDLen {
		writeJSONError(w, http.StatusBadRequest, "exchangeId required")
		return
	}
	surface, prompt, response, err := s.store.AICallSampleByExchange(r.Context(), xid)
	if err != nil {
		writeErr(w, err)
		return
	}
	if strings.TrimSpace(response) == "" {
		// Aday listesi response'suz satırları zaten elemişti; buraya
		// düşmek ya bayat bir liste ya uydurma bir id demek — ikisinde de
		// boş chunk yazmak yerine açık cevap.
		writeJSONError(w, http.StatusNotFound, "bu exchangeId için kayıtlı cevap örneği yok")
		return
	}
	docs, err := s.store.ListRagDocuments(r.Context())
	if err == nil && len(docs) >= ragMaxDocs {
		writeJSONError(w, http.StatusBadRequest,
			fmt.Sprintf("doküman tavanı (%d) dolu — önce silin", ragMaxDocs))
		return
	}
	email := ""
	if c := auth.FromContext(r.Context()); c != nil {
		email = c.Email
	}
	// docID exchange'e ÇAKILI: aynı adayı iki kez terfi etmek yeni doküman
	// açmaz, aynısını tazeler (ReplacingMergeTree devralır) — çift tık /
	// iki sekme yarışı katalogda kopya üretemez.
	docID := "curated-" + xid
	n, err := s.ragIngestDocument(r.Context(), docID, curatedDocTitle(prompt),
		"curated", xid, email, curatedDocText(surface, prompt, response))
	if err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "rag.curate", "rag_document", docID,
		fmt.Sprintf(`{"exchangeId":%q,"surface":%q,"chunks":%d}`, xid, surface, n))
	writeJSON(w, map[string]any{"docId": docID, "chunks": n})
}
