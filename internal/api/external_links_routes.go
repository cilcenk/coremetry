package api

// external_links_routes.go — v0.10.345: trace sayfası dış link şablonları.
//   GET/PUT /api/settings/external-links  admin (audit)
//   GET     /api/external-links           kimlikli her rol (viewer düğmeyi görür)

import (
	"encoding/json"
	"net/http"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/chstore"
)

func init() { registerRoutesExtra("external-links", (*Server).registerExternalLinksRoutes) }

func (s *Server) registerExternalLinksRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings/external-links", auth.RequireRole(auth.RoleAdmin, s.getExternalLinks))
	mux.HandleFunc("PUT /api/settings/external-links", auth.RequireRole(auth.RoleAdmin, s.putExternalLinks))
	mux.HandleFunc("GET /api/external-links", auth.RequireAnyRole([]string{auth.RoleAdmin, auth.RoleEditor, auth.RoleViewer}, s.getExternalLinks))
}

func (s *Server) getExternalLinks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, chstore.ExternalLinkSettings{Links: chstore.CurrentExternalLinks()})
}

func (s *Server) putExternalLinks(w http.ResponseWriter, r *http.Request) {
	var in chstore.ExternalLinkSettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	cfg, err := s.store.SaveExternalLinks(r.Context(), in)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	details, _ := json.Marshal(cfg)
	s.audit(r, "settings.external_links.update", "settings", "external_links", string(details))
	writeJSON(w, cfg)
}
