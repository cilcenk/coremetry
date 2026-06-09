package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

func (s *Server) listFeedbacks(w http.ResponseWriter, r *http.Request) {
	// Clamp BEFORE building the cache key so raw query values can't
	// mint distinct cache entries for identical clamped results.
	limit := parseInt(r.URL.Query().Get("limit"), 20)
	if limit <= 0 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}
	offset := parseInt(r.URL.Query().Get("offset"), 0)
	if offset < 0 {
		offset = 0
	}
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

// validateFeedbackMessage trims and bounds a feedback submission.
// Counts RUNES, not bytes — the frontend's maxLength={2000} counts
// characters, so multibyte text (Turkish, emoji) must not be
// rejected for its UTF-8 byte length.
func validateFeedbackMessage(msg string) (string, error) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "", errors.New("message required")
	}
	if utf8.RuneCountInString(msg) > 2000 {
		return "", errors.New("message too long (max 2000 chars)")
	}
	return msg, nil
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
	msg, err := validateFeedbackMessage(body.Message)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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
		Message:   msg,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "feedback.submit", "feedback", saved.ID,
		fmt.Sprintf(`{"email":%q}`, userEmail))
	writeJSON(w, saved)
}
