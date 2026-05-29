package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

// Runbooks CRUD (v0.7.0 — see docs/runbooks-agent-design.md). GET list +
// detail are ungated so viewers see runbooks read-only (invariant #7);
// every write is gated to editor+ at the mux (api.go) and audited here.
// Executions + the coremetry-agent dispatch land in later increments.

func (s *Server) listRunbooks(w http.ResponseWriter, r *http.Request) {
	rbs, err := s.store.ListRunbooks(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, rbs)
}

func (s *Server) getRunbook(w http.ResponseWriter, r *http.Request) {
	rb, err := s.store.GetRunbook(r.Context(), r.PathValue("id"))
	if err != nil || rb == nil {
		http.Error(w, `{"error":"runbook not found"}`, http.StatusNotFound)
		return
	}
	writeJSON(w, rb)
}

func (s *Server) createRunbook(w http.ResponseWriter, r *http.Request) {
	var rb chstore.Runbook
	if err := json.NewDecoder(r.Body).Decode(&rb); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if rb.ID == "" {
		rb.ID = newID(8)
	}
	now := time.Now().UnixNano()
	rb.CreatedAt = now
	rb.UpdatedAt = now
	if c := auth.FromContext(r.Context()); c != nil {
		rb.CreatedBy = c.Email
	}
	if err := s.store.UpsertRunbook(r.Context(), rb); err != nil {
		writeErr(w, err)
		return
	}
	details, _ := json.Marshal(map[string]any{"title": rb.Title, "steps": len(rb.Steps)})
	s.audit(r, "runbook.create", "runbook", rb.ID, string(details))
	writeJSON(w, rb)
}

// updateRunbook edits an existing runbook. Server-owned fields (createdAt,
// createdBy) are preserved from the stored row so a client edit can't reset
// them; updatedAt is stamped fresh.
func (s *Server) updateRunbook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := s.store.GetRunbook(r.Context(), id)
	if err != nil || existing == nil {
		http.Error(w, `{"error":"runbook not found"}`, http.StatusNotFound)
		return
	}
	var rb chstore.Runbook
	if err := json.NewDecoder(r.Body).Decode(&rb); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rb.ID = existing.ID
	rb.CreatedAt = existing.CreatedAt
	rb.CreatedBy = existing.CreatedBy
	rb.UpdatedAt = time.Now().UnixNano()
	if err := s.store.UpsertRunbook(r.Context(), rb); err != nil {
		writeErr(w, err)
		return
	}
	details, _ := json.Marshal(map[string]any{"title": rb.Title, "steps": len(rb.Steps)})
	s.audit(r, "runbook.update", "runbook", rb.ID, string(details))
	writeJSON(w, rb)
}

func (s *Server) deleteRunbook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteRunbook(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "runbook.delete", "runbook", id, "")
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) enableRunbook(w http.ResponseWriter, r *http.Request)  { s.setRunbookEnabled(w, r, true) }
func (s *Server) disableRunbook(w http.ResponseWriter, r *http.Request) { s.setRunbookEnabled(w, r, false) }

func (s *Server) setRunbookEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	id := r.PathValue("id")
	if err := s.store.SetRunbookEnabled(r.Context(), id, enabled); err != nil {
		writeErr(w, err)
		return
	}
	action := "runbook.enable"
	if !enabled {
		action = "runbook.disable"
	}
	s.audit(r, action, "runbook", id, "")
	writeJSON(w, map[string]string{"status": "ok"})
}
