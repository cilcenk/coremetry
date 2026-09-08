package api

// ai_chat_retention.go — v0.10.561 (CoSRE Faz 5c): sohbet arşivi saklama
// süresi (saved_views page='ai-chat' süpürücüsü). problem_priority şablonu:
// boot'ta LoadPersisted + 30 s yenileme, admin GET/PUT + audit. Rota kaydı
// ai_routes.go (AI ayağı; api.go büyümez).
//
//   GET /api/ai/chat-retention → {"days":90}
//   PUT /api/ai/chat-retention  {"days":N}  0 = süpürme kapalı, ≤3650

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func (s *Server) LoadAIChatRetention(ctx context.Context) {
	chstore.SetAIChatRetention(s.store.GetAIChatRetention(ctx))
}

func (s *Server) StartAIChatRetentionRefresh(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.LoadAIChatRetention(ctx)
		}
	}
}

func (s *Server) getAIChatRetention(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store.GetAIChatRetention(r.Context()))
}

func (s *Server) putAIChatRetention(w http.ResponseWriter, r *http.Request) {
	c := chstore.DefaultAIChatRetention()
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeJSONError(w, http.StatusBadRequest, "geçersiz JSON: "+err.Error())
		return
	}
	if c.Days < 0 || c.Days > chstore.AIChatRetentionMax {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("days 0–%d olmalı (0 = süpürme kapalı)", chstore.AIChatRetentionMax))
		return
	}
	if err := s.store.SaveAIChatRetention(r.Context(), c); err != nil {
		writeErr(w, err)
		return
	}
	chstore.SetAIChatRetention(c)
	s.audit(r, "settings.update", "ai_chat_retention", "ai_chat_retention", fmt.Sprintf(`{"days":%d}`, c.Days))
	writeJSON(w, chstore.NormalizeAIChatRetention(c))
}
