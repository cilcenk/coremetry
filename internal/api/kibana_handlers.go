package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// getKibanaSettings — public read (every signed-in user). The
// Logs page renders an "Open in Kibana" link when Enabled +
// BaseURL are set; viewers need to read this just like the
// branding overlay. No secret in the payload.
func (s *Server) getKibanaSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GetKibana(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, cfg)
}

// putKibanaSettings — admin only (route-gated). Empty BaseURL +
// Enabled=false disables the integration cleanly.
func (s *Server) putKibanaSettings(w http.ResponseWriter, r *http.Request) {
	var in chstore.KibanaSettings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	in.DataView = strings.TrimSpace(in.DataView)
	if in.Enabled && in.BaseURL == "" {
		http.Error(w, "baseUrl required when enabled", http.StatusBadRequest)
		return
	}
	if err := s.store.PutKibana(r.Context(), in); err != nil {
		writeErr(w, err)
		return
	}
	details, _ := json.Marshal(in)
	s.audit(r, "settings.kibana.update", "settings", "kibana", string(details))
	writeJSON(w, in)
}
