package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/copilot"
	"github.com/cilcenk/coremetry/internal/rag"
)

// rag.go — v0.8.438 doküman RAG yüzeyi. Registrar deseni (pivot.go
// şablonu): api.go yalnız registerRAGRoutes çağrısıyla büyür; handler
// mantığı burada, depo chstore/rag.go'da, embed/chunk internal/rag'da.
//
// Chat entegrasyonu ragChatAnswer ile: guided telemetri router'ından
// SONRA, serbest tool döngüsünden ÖNCE denenir — telemetri sorularını
// gasp etmez, doküman sorusuna döküman cevabı verir. Embedding
// endpoint'i yapılandırılmamışsa tüm yol sessizce kapalıdır.

const (
	ragMaxUploadBytes = 5 << 20 // 5MB/doc
	ragMaxDocs        = 200
	// ragScoreFloor — en iyi chunk bu kosinüs benzerliğinin altındaysa
	// soru doküman sorusu DEĞİLDİR; serbest döngüye düşülür.
	ragScoreFloor = 0.35
)

func (s *Server) registerRAGRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET    /api/rag/config", auth.RequireRole(auth.RoleAdmin, s.getRAGConfig))
	mux.HandleFunc("PUT    /api/rag/config", auth.RequireRole(auth.RoleAdmin, s.putRAGConfig))
	mux.HandleFunc("GET    /api/rag/documents", s.listRAGDocuments)
	mux.HandleFunc("POST   /api/rag/documents", auth.RequireAnyRole(editorRoles, s.uploadRAGDocument))
	mux.HandleFunc("DELETE /api/rag/documents/{id}", auth.RequireAnyRole(editorRoles, s.deleteRAGDocument))
}

// maskedRAGConfig — APIKey asla geri dönmez (secrets kuralı).
func maskedRAGConfig(c rag.Config) map[string]any {
	return map[string]any{
		"endpoint": c.Endpoint, "model": c.Model, "enabled": c.Enabled,
		"topK": c.TopK, "hasKey": c.APIKey != "",
	}
}

func (s *Server) getRAGConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, maskedRAGConfig(s.rag.Snapshot()))
}

func (s *Server) putRAGConfig(w http.ResponseWriter, r *http.Request) {
	var body rag.Config
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	// Boş / sentinel key = mevcut korunur (SMTP deseni).
	if body.APIKey == "" || body.APIKey == "********" {
		body.APIKey = s.rag.Snapshot().APIKey
	}
	if err := s.rag.SavePersisted(r.Context(), s.store, body); err != nil {
		writeErr(w, err)
		return
	}
	details, _ := json.Marshal(map[string]any{
		"endpoint": body.Endpoint, "model": body.Model,
		"enabled": body.Enabled, "topK": body.TopK,
	})
	s.audit(r, "settings.rag.update", "settings", "rag_embedding", string(details))
	writeJSON(w, maskedRAGConfig(body))
}

func (s *Server) listRAGDocuments(w http.ResponseWriter, r *http.Request) {
	docs, err := s.store.ListRagDocuments(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if docs == nil {
		docs = []chstore.RagDocument{}
	}
	writeJSON(w, map[string]any{"documents": docs, "ready": s.rag.Ready()})
}

// uploadRAGDocument — multipart (file alanı, md/txt) veya JSON
// {name, text}. Chunk + embed + replace tek istekte; embedding
// endpoint'i hazır değilse 503 döner (yükleme yarım kalmaz).
func (s *Server) uploadRAGDocument(w http.ResponseWriter, r *http.Request) {
	if !s.rag.Ready() {
		http.Error(w, "embedding endpoint yapılandırılmamış (Settings → AI → RAG)", http.StatusServiceUnavailable)
		return
	}
	name, text, err := readRAGUpload(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	docs, err := s.store.ListRagDocuments(r.Context())
	if err == nil && len(docs) >= ragMaxDocs {
		http.Error(w, fmt.Sprintf("doküman tavanı (%d) dolu — önce silin", ragMaxDocs), http.StatusBadRequest)
		return
	}
	c := auth.FromContext(r.Context())
	email := ""
	if c != nil {
		email = c.Email
	}
	docID := ragDocID(name)
	n, err := s.ragIngestDocument(r.Context(), docID, name, "upload", "", email, text)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "rag.upload", "rag_document", docID,
		fmt.Sprintf(`{"name":%q,"chunks":%d,"bytes":%d}`, name, n, len(text)))
	writeJSON(w, map[string]any{"docId": docID, "chunks": n})
}

// ragIngestDocument — chunk → embed → replace. Crawler (v2) da aynı
// yolu kullanır; source/sourceRef ayrımı buradan geçer.
func (s *Server) ragIngestDocument(ctx context.Context, docID, name, source, sourceRef, uploadedBy, text string) (int, error) {
	pieces := rag.ChunkText(text)
	if len(pieces) == 0 {
		return 0, fmt.Errorf("dokümandan metin çıkarılamadı")
	}
	embs, err := s.rag.Embed(ctx, pieces)
	if err != nil {
		return 0, err
	}
	chunks := make([]chstore.RagChunk, len(pieces))
	for i := range pieces {
		chunks[i] = chstore.RagChunk{
			DocID: docID, DocName: name, Source: source, SourceRef: sourceRef,
			UploadedBy: uploadedBy, ChunkIdx: uint32(i),
			Content: pieces[i], Embedding: embs[i],
		}
	}
	if err := s.store.ReplaceDocumentChunks(ctx, chunks); err != nil {
		return 0, err
	}
	return len(chunks), nil
}

func (s *Server) deleteRAGDocument(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteRagDocument(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "rag.delete", "rag_document", id, "{}")
	writeJSON(w, map[string]bool{"ok": true})
}

func readRAGUpload(r *http.Request) (name, text string, err error) {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		var body struct{ Name, Text string }
		if err := json.NewDecoder(io.LimitReader(r.Body, ragMaxUploadBytes)).Decode(&body); err != nil {
			return "", "", fmt.Errorf("invalid body: %w", err)
		}
		if strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.Text) == "" {
			return "", "", fmt.Errorf("name ve text zorunlu")
		}
		return strings.TrimSpace(body.Name), body.Text, nil
	}
	if err := r.ParseMultipartForm(ragMaxUploadBytes); err != nil {
		return "", "", fmt.Errorf("upload %dMB tavanını aşıyor", ragMaxUploadBytes>>20)
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		return "", "", fmt.Errorf("file alanı zorunlu")
	}
	defer f.Close()
	low := strings.ToLower(hdr.Filename)
	if !strings.HasSuffix(low, ".md") && !strings.HasSuffix(low, ".txt") {
		return "", "", fmt.Errorf("v1 yalnız .md / .txt kabul eder (%s)", hdr.Filename)
	}
	b, err := io.ReadAll(io.LimitReader(f, ragMaxUploadBytes+1))
	if err != nil {
		return "", "", err
	}
	if len(b) > ragMaxUploadBytes {
		return "", "", fmt.Errorf("dosya %dMB tavanını aşıyor", ragMaxUploadBytes>>20)
	}
	return hdr.Filename, string(b), nil
}

// ragDocID — ad-türevli deterministik id: aynı adla yeniden yükleme
// aynı doc_id'ye düşer → ReplacingMergeTree güncellemeyi devralır.
func ragDocID(name string) string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(name))))
	return hex.EncodeToString(h[:8])
}

// ── Chat entegrasyonu ───────────────────────────────────────────────

// ragSystemPrompt — 2B hedefe uygun kısa, katı talimat: yalnız verilen
// bağlamdan cevapla; bağlamda yoksa uydurma.
const ragSystemPrompt = `Sen Coremetry'nin doküman asistanısın. SADECE sana verilen BAĞLAM parçalarındaki bilgiyle, Türkçe ve öz cevap ver. Cevap bağlamda yoksa "Yüklü dokümanlarda bu bilgi yok." de — asla tahmin etme, asla bağlam dışı bilgi ekleme.`

// ragChatAnswer — guided telemetri router'ı eşleşmediğinde, serbest
// tool döngüsünden önce denenen doküman yolu. handled=false → RAG
// kapalı / doküman yok / soru dokümanlarla ilgisiz (skor tabanı) —
// akış aynen devam eder.
func (s *Server) ragChatAnswer(ctx context.Context, emit func(string, any), msgs []copilot.ChatMessage) (handled, ok bool) {
	if s.rag == nil || !s.rag.Ready() {
		return false, false
	}
	question := lastUserText(msgs)
	if strings.TrimSpace(question) == "" {
		return false, false
	}
	qEmb, err := s.rag.Embed(ctx, []string{question})
	if err != nil || len(qEmb) != 1 {
		log.Printf("[rag] soru embed: %v", err)
		return false, false // embedding düşükse chat'i kilitleme
	}
	hits, err := s.store.TopKRagChunks(ctx, qEmb[0], s.rag.EffectiveTopK())
	if err != nil || len(hits) == 0 || hits[0].Score < ragScoreFloor {
		return false, false
	}

	var b strings.Builder
	type src struct {
		Doc   string  `json:"doc"`
		Ref   string  `json:"ref,omitempty"`
		Chunk uint32  `json:"chunk"`
		Score float64 `json:"score"`
	}
	sources := make([]src, 0, len(hits))
	for i, h := range hits {
		fmt.Fprintf(&b, "[%d] (%s §%d)\n%s\n\n", i+1, h.DocName, h.ChunkIdx+1, h.Content)
		sources = append(sources, src{Doc: h.DocName, Ref: h.SourceRef, Chunk: h.ChunkIdx + 1, Score: h.Score})
	}

	user := "SORU: " + question + "\n\nBAĞLAM:\n" + b.String()
	raw, exErr := s.copilotStreamSurface(ctx, "rag-chat", ragSystemPrompt, user, func(delta string) {
		emit("delta", map[string]string{"text": delta})
	})
	if exErr != nil {
		emit("error", map[string]string{"error": exErr.Error()})
		return true, false
	}
	emit("answer", map[string]any{
		"text":       strings.TrimSpace(raw),
		"exchangeId": copilot.MetaFromContext(ctx).ExchangeID,
		"sources":    sources,
	})
	return true, true
}
