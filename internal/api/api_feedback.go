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

func (s *Server) listFeedbacks(w http.ResponseWriter, r *http.Request) {
	limit := parseInt(r.URL.Query().Get("limit"), 20)
	offset := parseInt(r.URL.Query().Get("offset"), 0)
	key := fmt.Sprintf("feedbacks:limit=%d:offset=%d", limit, offset)
	s.serveCached(w, r, key, 30*time.Second, func() (any, error) {
		items, hasMore, err := s.store.ListFeedbacks(r.Context(), limit, offset)
		if err != nil {
			return nil, err
		}
		if items == nil {
			items = []chstore.Feedback{}
		}
		return map[string]any{"feedbacks": items, "hasMore": hasMore}, nil
	})
}

func (s *Server) submitFeedback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	body.Message = strings.TrimSpace(body.Message)
	if body.Message == "" {
		http.Error(w, "message required", http.StatusBadRequest)
		return
	}
	if len(body.Message) > 2000 {
		http.Error(w, "message too long (max 2000 chars)", http.StatusBadRequest)
		return
	}

	claims := auth.FromContext(r.Context())
	userID, userEmail := "", ""
	if claims != nil {
		userID = claims.UserID
		userEmail = claims.Email
	}

	saved, err := s.store.InsertFeedback(r.Context(), chstore.Feedback{
		UserID:    userID,
		UserEmail: userEmail,
		Message:   body.Message,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "feedback.submit", "feedback", saved.ID,
		fmt.Sprintf(`{"email":%q}`, userEmail))
	writeJSON(w, saved)
}
