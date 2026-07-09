package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

// AI answer feedback (v0.8.399, AI audit feedback slice). The chat
// drawer renders thumbs up/down under each assistant answer; a click
// POSTs the exchangeId the SSE answer event carried + the verdict.
// One ai_feedback row per exchange (ReplacingMergeTree — re-rating
// replaces), aggregated into the /ai per-surface breakdown as a
// thumbs-up rate. Provider-agnostic: the verdict rates the answer,
// not the model that produced it.
//
// Auth: any authenticated user (global auth middleware) — whoever can
// chat can rate the answer they got; mirrors POST /api/copilot/chat.
// Deliberately NO audit entry: this is high-frequency user-scoped
// quality signal, not an admin/config mutation (the saved_view.create
// audit precedent covers named artifacts other operators see; a
// thumb press is neither).

type aiFeedbackRequest struct {
	ExchangeID string `json:"exchangeId"`
	Verdict    int8   `json:"verdict"` // 1 | -1
}

// aiFeedbackMaxIDLen bounds the exchangeId a client can post. The
// server mints 32-char hex ids (newRandID(16)); 64 leaves headroom
// without letting a hostile client stuff a blob into the dedup key.
const aiFeedbackMaxIDLen = 64

// validateAIFeedback is the pure request gate — split out so the
// v0.8.399 regression test can drive it table-style without HTTP.
func validateAIFeedback(exchangeID string, verdict int8) error {
	if strings.TrimSpace(exchangeID) == "" {
		return errors.New("exchangeId required")
	}
	if len(exchangeID) > aiFeedbackMaxIDLen {
		return errors.New("exchangeId too long")
	}
	if verdict != 1 && verdict != -1 {
		return errors.New("verdict must be 1 or -1")
	}
	return nil
}

// postAIFeedback stores one verdict. The surface label is resolved
// server-side from the ai_calls row carrying the same exchange_id —
// the client can't mislabel a verdict, and a not-yet-flushed row
// (RecordUsage inserts on a goroutine) degrades to surface '' rather
// than rejecting the click.
func (s *Server) postAIFeedback(w http.ResponseWriter, r *http.Request) {
	var req aiFeedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	req.ExchangeID = strings.TrimSpace(req.ExchangeID)
	if err := validateAIFeedback(req.ExchangeID, req.Verdict); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	surface, err := s.store.AICallSurfaceByExchange(r.Context(), req.ExchangeID)
	if err != nil {
		writeErr(w, err)
		return
	}
	email := ""
	if c := auth.FromContext(r.Context()); c != nil {
		email = c.Email
	}
	if err := s.store.UpsertAIFeedback(r.Context(), chstore.AIFeedback{
		ExchangeID: req.ExchangeID,
		Surface:    surface,
		Verdict:    req.Verdict,
		UserEmail:  email,
	}); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
