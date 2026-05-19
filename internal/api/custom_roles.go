package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/cilcenk/coremetry/internal/auth"
)

// Custom-role admin handlers (v0.5.251). Custom roles are operator-
// defined subsets of viewer's page access — a "readonly-3" role that
// only surfaces traces/metrics/logs, etc. They live in the
// system_settings "custom_roles" key as a JSON blob; auth.Service
// owns the in-memory catalog and the marshal/unmarshal contract.
//
// Page IDs come from the backend's availablePages registry — see
// pages.go. The upsert handler rejects unknown IDs so a typo (or a
// stale frontend) can't silently strand a user on a non-existent
// route.

func (s *Server) listCustomRoles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"roles": s.auth.CustomRoles(),
	})
}

// upsertCustomRole creates or replaces a role by name. Idempotent —
// the catalog is a single blob so partial writes aren't possible.
func (s *Server) upsertCustomRole(w http.ResponseWriter, r *http.Request) {
	var body auth.CustomRole
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid JSON: `+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		http.Error(w, `{"error":"name required"}`, http.StatusBadRequest)
		return
	}
	// Reject unknown page IDs. The frontend's grid is sourced from
	// /api/admin/pages so a typo here means the client is out of
	// sync — surface the error rather than silently dropping the
	// page from the role.
	known := availablePageIDs()
	for _, p := range body.Pages {
		if _, ok := known[p]; !ok {
			http.Error(w,
				fmt.Sprintf(`{"error":"unknown page %q — pick from /api/admin/pages"}`, p),
				http.StatusBadRequest)
			return
		}
	}
	if err := s.auth.UpsertCustomRole(r.Context(), s.store, body); err != nil {
		writeErr(w, err)
		return
	}
	details, _ := json.Marshal(map[string]any{"name": body.Name, "pages": body.Pages})
	s.audit(r, "role.upsert", "custom_role", body.Name, string(details))
	writeJSON(w, body)
}

// deleteCustomRole removes a role + clears the custom_role field on
// every user currently assigned to it (so the user falls back to
// unrestricted viewer rather than being stranded on a dangling
// pointer).
func (s *Server) deleteCustomRole(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		http.Error(w, `{"error":"name required"}`, http.StatusBadRequest)
		return
	}
	if err := s.auth.DeleteCustomRole(r.Context(), s.store, name); err != nil {
		writeErr(w, err)
		return
	}
	// Clear the column on every user assigned to this role. Walk
	// the user list once — for the install scale we operate at
	// (low thousands of users) this is cheap; at hyper-scale, a
	// per-name index would help, but that's premature here.
	users, err := s.store.ListUsers(r.Context())
	if err == nil {
		for i := range users {
			if users[i].CustomRole != name {
				continue
			}
			u := users[i]
			u.CustomRole = ""
			_ = s.store.UpsertUser(r.Context(), u)
		}
	}
	s.audit(r, "role.delete", "custom_role", name, "")
	writeJSON(w, map[string]string{"status": "ok"})
}

// listAvailablePages returns the canonical sidebar-page registry —
// drives the checkbox grid in Settings → Roles. Read-only; no
// caching needed because the slice is process-local.
func (s *Server) listAvailablePages(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"pages": availablePages,
	})
}

// setUserCustomRole assigns (or clears) a custom-role pointer on a
// user. Only valid when the user's base role is viewer — admin /
// editor get rejected so a stale pointer can't survive a role
// promotion. The handler also rejects unknown role names so a typo
// can't strand the user.
func (s *Server) setUserCustomRole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		CustomRole string `json:"customRole"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	body.CustomRole = strings.TrimSpace(body.CustomRole)
	target, err := s.store.GetUserByID(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if target == nil {
		http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
		return
	}
	if target.Role != auth.RoleViewer && body.CustomRole != "" {
		http.Error(w, `{"error":"custom roles only apply to viewer base role"}`, http.StatusBadRequest)
		return
	}
	if body.CustomRole != "" {
		// Unknown custom role → reject. Same rationale as the
		// page-ID validation in upsertCustomRole.
		if s.auth.CustomRolePages(body.CustomRole) == nil {
			http.Error(w,
				fmt.Sprintf(`{"error":"unknown custom role %q"}`, body.CustomRole),
				http.StatusBadRequest)
			return
		}
	}
	prev := target.CustomRole
	if prev == body.CustomRole {
		writeJSON(w, map[string]any{"id": target.ID, "email": target.Email, "customRole": prev})
		return
	}
	target.CustomRole = body.CustomRole
	if err := s.store.UpsertUser(r.Context(), *target); err != nil {
		writeErr(w, err)
		return
	}
	details, _ := json.Marshal(map[string]any{"email": target.Email, "from": prev, "to": body.CustomRole})
	s.audit(r, "user.set_custom_role", "user", target.ID, string(details))
	writeJSON(w, map[string]any{"id": target.ID, "email": target.Email, "customRole": target.CustomRole})
}
