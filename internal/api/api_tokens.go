package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/cilcenk/coremetry/internal/auth"
)

// api_tokens.go — v0.8.444 servis token yönetimi (registrar deseni).
// Harici agent platformlarının (GenAI Studio) MCP/REST erişimi için
// admin'in ürettiği, rol bağlı, iptal edilebilir kimlikler. Düz token
// YALNIZ create yanıtında döner; liste hash taşımaz.

func (s *Server) registerAPITokenRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET    /api/admin/api-tokens", auth.RequireRole(auth.RoleAdmin, s.listAPITokens))
	mux.HandleFunc("POST   /api/admin/api-tokens", auth.RequireRole(auth.RoleAdmin, s.createAPIToken))
	mux.HandleFunc("DELETE /api/admin/api-tokens/{id}", auth.RequireRole(auth.RoleAdmin, s.revokeAPIToken))
}

func (s *Server) listAPITokens(w http.ResponseWriter, r *http.Request) {
	toks, err := s.store.ListAPITokens(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"tokens": toks})
}

func (s *Server) createAPIToken(w http.ResponseWriter, r *http.Request) {
	var body struct{ Name, Role string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	switch body.Role {
	case auth.RoleAdmin, auth.RoleEditor, auth.RoleViewer:
	default:
		http.Error(w, "role must be admin | editor | viewer", http.StatusBadRequest)
		return
	}
	c := auth.FromContext(r.Context())
	createdBy := ""
	if c != nil {
		createdBy = c.Email
	}
	plain, tok, err := s.store.CreateAPIToken(r.Context(), body.Name, body.Role, createdBy)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Yeni token'ın ANINDA çalışması için cache'i tazele (30 sn'lik
	// tick'i bekletme).
	s.auth.RefreshAPITokens(r.Context())
	s.audit(r, "apitoken.create", "api_token", tok.ID,
		fmt.Sprintf(`{"name":%q,"role":%q}`, tok.Name, tok.Role))
	// Düz token yalnız BURADA döner — bir daha asla gösterilmez.
	writeJSON(w, map[string]any{"token": plain, "record": tok})
}

func (s *Server) revokeAPIToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if err := s.store.RevokeAPIToken(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	s.auth.RefreshAPITokens(r.Context())
	s.audit(r, "apitoken.revoke", "api_token", id, "{}")
	writeJSON(w, map[string]bool{"ok": true})
}
