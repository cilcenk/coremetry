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
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/cache"
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
	// ragKeywordFloor (v0.9.162 review) — BM25 köprüsünde en iyi chunk sorgu
	// terimlerinin bu kesrinden azını içeriyorsa alaka zayıf; grounding yapma,
	// serbest tool döngüsüne bırak (tek terimlik tesadüfi eşleşme chat'i
	// kaçırmasın). 0.5 = terimlerin en az yarısı whole-token eşleşmeli.
	ragKeywordFloor = 0.5
)

func (s *Server) registerRAGRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET    /api/rag/config", auth.RequireRole(auth.RoleAdmin, s.getRAGConfig))
	mux.HandleFunc("PUT    /api/rag/config", auth.RequireRole(auth.RoleAdmin, s.putRAGConfig))
	mux.HandleFunc("GET    /api/rag/documents", s.listRAGDocuments)
	mux.HandleFunc("POST   /api/rag/documents", auth.RequireAnyRole(editorRoles, s.uploadRAGDocument))
	mux.HandleFunc("DELETE /api/rag/documents/{id}", auth.RequireAnyRole(editorRoles, s.deleteRAGDocument))
	mux.HandleFunc("POST   /api/rag/sync", auth.RequireAnyRole(editorRoles, s.syncRAGSources))
}

// maskedRAGConfig — APIKey asla geri dönmez (secrets kuralı).
func maskedRAGConfig(c rag.Config) map[string]any {
	srcs := make([]map[string]string, 0, len(c.Sources))
	for _, sc := range c.Sources {
		m := map[string]string{"url": sc.URL}
		if sc.AuthHeader != "" {
			m["authHeader"] = "********" // asla geri echo edilmez
		}
		// v0.8.451 — Basic auth (on-prem Azure DevOps). Kullanıcı adı
		// sır değil, aynen döner; şifre yalnız varlık sentineliyle.
		if sc.Username != "" {
			m["username"] = sc.Username
		}
		if sc.Password != "" {
			m["password"] = "********"
		}
		srcs = append(srcs, m)
	}
	return map[string]any{
		"endpoint": c.Endpoint, "model": c.Model, "enabled": c.Enabled,
		"topK": c.TopK, "hasKey": c.APIKey != "", "sources": srcs,
		"insecureSkipVerify": c.InsecureSkipVerify, // sır değil, aynen döner
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
	// Boş / sentinel key = mevcut korunur (SMTP deseni). Kaynak auth
	// header'ları da aynı sözleşme: "********" gelen kaynak, mevcut
	// config'te aynı URL varsa onun header'ını devralır.
	cur := s.rag.Snapshot()
	if body.APIKey == "" || body.APIKey == "********" {
		body.APIKey = cur.APIKey
	}
	for i, src := range body.Sources {
		if src.AuthHeader == "********" {
			body.Sources[i].AuthHeader = ""
			for _, old := range cur.Sources {
				if old.URL == src.URL {
					body.Sources[i].AuthHeader = old.AuthHeader
					break
				}
			}
		}
		// v0.8.451 — Basic şifresi aynı sözleşme: "********" (veya
		// kayıtlıyken boş bırakma) mevcut değeri devralır.
		if src.Password == "********" || src.Password == "" {
			body.Sources[i].Password = ""
			for _, old := range cur.Sources {
				if old.URL == src.URL {
					body.Sources[i].Password = old.Password
					break
				}
			}
		}
	}
	if err := s.rag.SavePersisted(r.Context(), s.store, body); err != nil {
		writeErr(w, err)
		return
	}
	details, _ := json.Marshal(map[string]any{
		"endpoint": body.Endpoint, "model": body.Model,
		"enabled": body.Enabled, "topK": body.TopK, "sources": len(body.Sources),
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
	// ready = semantik (embed) hazır; enabled = RAG açık (text-only keyword
	// modu dahil, v0.9.162). Frontend enabled && !ready → "keyword modu" hint'i.
	writeJSON(w, map[string]any{"documents": docs, "ready": s.rag.Ready(), "enabled": s.rag.Enabled()})
}

// uploadRAGDocument — multipart (file alanı, md/txt) veya JSON
// {name, text}. Chunk + embed + replace tek istekte; embedding
// RAG etkin değilse 503. v0.9.162: kapı Ready() DEĞİL Enabled() — embedding
// endpoint'i yoksa doküman text-only girer (keyword retrieval), yükleme
// bloklanmaz (review: eski Ready() kapısı text-only ingest'i erişilmez yapmıştı).
func (s *Server) uploadRAGDocument(w http.ResponseWriter, r *http.Request) {
	if !s.rag.Enabled() {
		http.Error(w, "RAG etkin değil (Settings → AI → RAG)", http.StatusServiceUnavailable)
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
	return s.ragIngestDocumentHashed(ctx, docID, name, source, sourceRef, uploadedBy, text, "")
}

// embedOrTextOnly (v0.9.173) — per-chunk embeddings when the embed endpoint is
// Ready AND responds; otherwise nil embeddings (text-only keyword ingest). A
// down / misconfigured bge-m3 must NOT block upload (operator-reported: upload
// HARD-FAIL'ed when the endpoint was set but unreachable). The document still
// lands in keyword mode; a later re-upload fills embeddings (same doc_id,
// ReplacingMergeTree). len(result) == len(pieces) always; result[i] may be nil.
func embedOrTextOnly(pieces []string, ready bool, embed func([]string) ([][]float32, error)) [][]float32 {
	if ready {
		if e, err := embed(pieces); err == nil {
			return e
		} else {
			log.Printf("[rag] embed failed — ingesting %d chunk(s) text-only (keyword mode): %v", len(pieces), err)
		}
	}
	return make([][]float32, len(pieces)) // nil per chunk = text-only
}

func (s *Server) ragIngestDocumentHashed(ctx context.Context, docID, name, source, sourceRef, uploadedBy, text, srcHash string) (int, error) {
	pieces := rag.ChunkText(text)
	if len(pieces) == 0 {
		return 0, fmt.Errorf("dokümandan metin çıkarılamadı")
	}
	// Embedding OPSİYONEL (v0.9.161 BM25 köprüsü + v0.9.173 dayanıklılık): embed
	// endpoint (bge-m3) yapılandırılmamışsa VEYA erişilemiyorsa doküman
	// METİN-ONLY girer — keyword retrieval (TopKRagChunksByContent) çalışır.
	// Embed başarısızlığı upload'ı ARTIK bloklamaz (operatör-bildirimi). bge-m3
	// gelince yeniden yükleme embedding'i doldurur (aynı doc_id → RMT devralır).
	embs := embedOrTextOnly(pieces,
		s.rag != nil && s.rag.Ready(),
		func(p []string) ([][]float32, error) { return s.rag.Embed(ctx, p) })
	chunks := make([]chstore.RagChunk, len(pieces))
	for i := range pieces {
		chunks[i] = chstore.RagChunk{
			DocID: docID, DocName: name, Source: source, SourceRef: sourceRef,
			UploadedBy: uploadedBy, ChunkIdx: uint32(i),
			Content: pieces[i], Embedding: embs[i], SourceHash: srcHash,
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
	if s.rag == nil {
		return false, false
	}
	question := lastUserText(msgs)
	if strings.TrimSpace(question) == "" {
		return false, false
	}
	topK := s.rag.EffectiveTopK()

	// Retrieval: bge-m3 yapılandırılmışsa SEMANTİK (cosine, floor'lu); değilse
	// veya boşsa BM25 KÖPRÜSÜ (keyword, v0.9.161) — doküman grounding
	// embedding'süz de çalışsın. Keyword hit'leri lexical skorlu (cosine floor
	// uygulanmaz; TopKRagChunksByContent zaten ≥1 terim eşleşenleri döner =
	// alaka kapısı, alakasız soru 0 hit → chat serbest-tool döngüsüne düşer).
	var hits []chstore.RagHit
	if s.rag.Ready() {
		if qEmb, err := s.rag.Embed(ctx, []string{question}); err == nil && len(qEmb) == 1 {
			if h, e := s.store.TopKRagChunks(ctx, qEmb[0], topK); e == nil &&
				len(h) > 0 && h[0].Score >= ragScoreFloor {
				hits = h
			}
		} else if err != nil {
			log.Printf("[rag] soru embed: %v — keyword'e düşülüyor", err)
		}
	}
	if len(hits) == 0 {
		// Keyword floor (v0.9.162 review): en iyi hit terimlerin ≥yarısını
		// whole-token içermiyorsa alaka zayıf → grounding yapma, serbest
		// döngüye bırak (tesadüfi tek-terim eşleşmesi chat'i kaçırmasın).
		if h, e := s.store.TopKRagChunksByContent(ctx, question, topK); e == nil &&
			len(h) > 0 && h[0].Score >= ragKeywordFloor {
			hits = h
		}
	}
	if len(hits) == 0 {
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

// ── URL/wiki senkronu (v0.8.442) ────────────────────────────────────

// syncRAGSources — POST /api/rag/sync: yapılandırılmış tüm URL
// kaynaklarını bir kez tarar. Manuel buton + 30 dk'lık leader-gated
// tick aynı yolu kullanır. Sonuç özetini döner; audit'li.
func (s *Server) syncRAGSources(w http.ResponseWriter, r *http.Request) {
	if !s.rag.Enabled() {
		http.Error(w, "RAG etkin değil", http.StatusServiceUnavailable)
		return
	}
	res := s.ragSyncPass(r.Context())
	b, _ := json.Marshal(res)
	s.audit(r, "rag.sync", "rag_sources", "manual", string(b))
	writeJSON(w, res)
}

type ragSyncResult struct {
	Sources int      `json:"sources"`
	Pages   int      `json:"pages"`
	Indexed int      `json:"indexed"` // yeni/değişen (yeniden embed edilen)
	Skipped int      `json:"skipped"` // hash değişmemiş
	Pruned  int      `json:"pruned"`  // kaynakta artık olmayan
	Errors  []string `json:"errors,omitempty"`
}

// ragSyncPass — tek senkron geçişi. Hash-diff: sayfa içeriğinin
// sha256'sı mevcut dokümanın source_hash'iyle aynıysa embedding
// çağrısı hiç yapılmaz (maliyet kontrolünün kalbi). Kaybolan sayfalar
// (bu geçişte görülmeyen, aynı kaynak prefix'li url dokümanları)
// budanır.
func (s *Server) ragSyncPass(ctx context.Context) ragSyncResult {
	cfg := s.rag.Snapshot()
	res := ragSyncResult{Sources: len(cfg.Sources)}
	if len(cfg.Sources) == 0 {
		return res
	}
	existing := map[string]chstore.RagDocument{}
	if docs, err := s.store.ListRagDocuments(ctx); err == nil {
		for _, d := range docs {
			if d.Source == "url" {
				existing[d.DocID] = d
			}
		}
	}
	httpc := rag.NewHTTPClient(20*time.Second, cfg.InsecureSkipVerify) // v0.9.23 — crawler da bayrağa uyar
	seen := map[string]bool{}
	for _, src := range cfg.Sources {
		pages, err := rag.Crawl(ctx, httpc, src)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", src.URL, err))
		}
		res.Pages += len(pages)
		for _, pg := range pages {
			docID := ragDocID(pg.URL)
			seen[docID] = true
			if old, ok := existing[docID]; ok && old.SourceHash == pg.Hash {
				res.Skipped++
				continue
			}
			if _, err := s.ragIngestDocumentHashed(ctx, docID, pg.Title, "url", pg.URL, "wiki-sync", pg.Text, pg.Hash); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", pg.URL, err))
				continue
			}
			res.Indexed++
		}
	}
	// Budama: url kaynaklı olup bu geçişte görülmeyenler. Kaynak listesi
	// boşaltıldıysa budama YAPILMAZ (operatör kaynağı kaldırdı diye tüm
	// indeksin silinmesi sürpriz olur — dokümanlar panelden silinebilir).
	if res.Pages > 0 {
		for id := range existing {
			if !seen[id] {
				if err := s.store.DeleteRagDocument(ctx, id); err == nil {
					res.Pruned++
				}
			}
		}
	}
	log.Printf("[rag-sync] sources=%d pages=%d indexed=%d skipped=%d pruned=%d errs=%d",
		res.Sources, res.Pages, res.Indexed, res.Skipped, res.Pruned, len(res.Errors))
	return res
}

// StartRAGSync — 30 dk'lık leader-gated arka plan senkronu (deriver
// deseni). api rolündeki pod'larda main.go'dan başlatılır.
func (s *Server) StartRAGSync(ctx context.Context, lock cache.Lock) {
	const interval = 30 * time.Minute
	leader := cache.NewLeaderHolder(lock, "coremetry:lock:rag-sync", cache.LeaderTTL(interval))
	leader.Start(ctx)
	tick := func() {
		if !leader.IsLeader() || !s.rag.Enabled() || len(s.rag.Snapshot().Sources) == 0 {
			return
		}
		s.ragSyncPass(ctx)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}
