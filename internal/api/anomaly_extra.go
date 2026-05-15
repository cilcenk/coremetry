package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

// ── Anomaly silences ─────────────────────────────────────────────

func (s *Server) listAnomalySilences(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.ListActiveSilences(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) createAnomalySilence(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Fingerprint string `json:"fingerprint"`
		Kind        string `json:"kind"`
		Pattern     string `json:"pattern"`
		Service     string `json:"service"`
		Reason      string `json:"reason"`
		DurationSec int    `json:"durationSec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Fingerprint == "" || body.Kind == "" {
		http.Error(w, "fingerprint and kind required", http.StatusBadRequest)
		return
	}
	if body.DurationSec <= 0 {
		body.DurationSec = 60 * 60 // default 1h
	}
	now := time.Now()
	until := now.Add(time.Duration(body.DurationSec) * time.Second)

	claims := auth.FromContext(r.Context())
	id := newRandID(8)
	sil := chstore.AnomalySilence{
		ID:          id,
		Fingerprint: body.Fingerprint,
		Kind:        body.Kind,
		Pattern:     body.Pattern,
		Service:     body.Service,
		CreatedBy:   claimEmail(claims),
		CreatedAt:   now.UnixNano(),
		UntilAt:     until.UnixNano(),
		Reason:      body.Reason,
	}
	if err := s.store.UpsertAnomalySilence(r.Context(), sil); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "anomaly_silence.create", "anomaly_silence", id,
		fmt.Sprintf(`{"fp":%q,"kind":%q,"service":%q,"durationSec":%d,"reason":%q}`,
			body.Fingerprint, body.Kind, body.Service, body.DurationSec, body.Reason))
	sil.Active = true
	writeJSON(w, sil)
}

func (s *Server) deleteAnomalySilence(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if err := s.store.DeleteAnomalySilence(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "anomaly_silence.delete", "anomaly_silence", id, "")
	w.WriteHeader(http.StatusNoContent)
}

// bulkDeleteAnomalySilences mirrors the v0.5.83 problem bulk-ack
// shape: POST a body of ids, get back a count. Lets an operator
// clean up an overnight storm of silences in one click instead of
// hitting × on each chip. One audit entry per call with the id
// list in details.
func (s *Server) bulkDeleteAnomalySilences(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body.IDs) == 0 {
		http.Error(w, "ids list required", http.StatusBadRequest)
		return
	}
	if len(body.IDs) > 200 {
		http.Error(w, "max 200 ids per bulk delete", http.StatusBadRequest)
		return
	}
	n, err := s.store.DeleteAnomalySilences(r.Context(), body.IDs)
	if err != nil {
		writeErr(w, err)
		return
	}
	details, _ := json.Marshal(map[string]any{"ids": body.IDs, "deleted": n})
	s.audit(r, "anomaly_silence.bulk_delete", "anomaly_silence", "", string(details))
	writeJSON(w, map[string]any{"deleted": n})
}

// ── Audit log ─────────────────────────────────────────────────────

func (s *Server) listAuditLog(w http.ResponseWriter, r *http.Request) {
	claims := auth.FromContext(r.Context())
	if claims == nil || claims.Role != auth.RoleAdmin {
		http.Error(w, "admin only", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	since := parseDuration(q.Get("since"), 24*time.Hour)
	rows, err := s.store.ListAuditLog(r.Context(), chstore.AuditFilter{
		SinceNs:    time.Now().Add(-since).UnixNano(),
		Actor:      strings.TrimSpace(q.Get("actor")),
		Action:     strings.TrimSpace(q.Get("action")),
		TargetKind: strings.TrimSpace(q.Get("target")),
		Limit:      parseInt(q.Get("limit"), 200),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rows)
}

// audit is a thin helper called from mutation handlers. Best-effort:
// errors are logged inside the store layer, never block the caller's
// response.
func (s *Server) audit(r *http.Request, action, kind, targetID, details string) {
	claims := auth.FromContext(r.Context())
	if claims == nil {
		return
	}
	entry := chstore.AuditEntry{
		Time:       time.Now().UnixNano(),
		ActorID:    claims.UserID,
		ActorEmail: claims.Email,
		ActorRole:  claims.Role,
		Action:     action,
		TargetKind: kind,
		TargetID:   targetID,
		IP:         clientIP(r),
		Details:    details,
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.store.AppendAudit(ctx, entry)
	}()
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		// Trust the first hop only — anything past that is
		// caller-controlled and useful for forensics but not
		// authoritative.
		if i := strings.IndexByte(v, ','); i > 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return v
	}
	return r.RemoteAddr
}

func claimEmail(c *auth.Claims) string {
	if c == nil {
		return ""
	}
	return c.Email
}

// ── Saved views ──────────────────────────────────────────────────

func (s *Server) listSavedViews(w http.ResponseWriter, r *http.Request) {
	claims := auth.FromContext(r.Context())
	page := strings.TrimSpace(r.URL.Query().Get("page"))
	owner := ""
	if claims != nil {
		owner = claims.UserID
	}
	out, err := s.store.ListSavedViews(r.Context(), owner, page)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) createSavedView(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string `json:"name"`
		Page        string `json:"page"`
		QueryString string `json:"queryString"`
		Pinned      bool   `json:"pinned"`
		Shared      bool   `json:"shared"` // admin-only knob
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	body.Page = strings.TrimSpace(body.Page)
	if body.Name == "" || body.Page == "" {
		http.Error(w, "name and page required", http.StatusBadRequest)
		return
	}
	claims := auth.FromContext(r.Context())
	owner := ""
	if claims != nil {
		owner = claims.UserID
	}
	// Team-shared views are admin-gated to keep accidental
	// "pin everyone's quirky filter" out of the topbar.
	if body.Shared {
		if claims == nil || claims.Role != auth.RoleAdmin {
			http.Error(w, "admin only for shared views", http.StatusForbidden)
			return
		}
		owner = ""
	}

	v := chstore.SavedView{
		ID:          newRandID(6),
		OwnerID:     owner,
		Name:        body.Name,
		Page:        body.Page,
		QueryString: body.QueryString,
		Pinned:      body.Pinned,
		CreatedAt:   time.Now().UnixNano(),
	}
	if err := s.store.UpsertSavedView(r.Context(), v); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "saved_view.create", "saved_view", v.ID,
		fmt.Sprintf(`{"page":%q,"name":%q,"shared":%t}`, v.Page, v.Name, body.Shared))
	writeJSON(w, v)
}

func (s *Server) deleteSavedView(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	// Authorisation: you can only delete your own non-shared
	// views unless you're admin.
	cur, err := s.store.GetSavedView(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if cur == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	claims := auth.FromContext(r.Context())
	switch {
	case claims != nil && claims.Role == auth.RoleAdmin:
		// admins can delete anything
	case cur.OwnerID == "" || (claims != nil && claims.UserID != cur.OwnerID):
		http.Error(w, "not your view", http.StatusForbidden)
		return
	}
	if err := s.store.DeleteSavedView(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "saved_view.delete", "saved_view", id, "")
	w.WriteHeader(http.StatusNoContent)
}

func newRandID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
