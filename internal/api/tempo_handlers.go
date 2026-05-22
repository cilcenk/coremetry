package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cilcenk/coremetry/internal/tempo"
)

// getTempoSettings returns the saved external-Tempo config minus
// the token. UI uses {enabled, baseUrl, authType, hasToken,
// username, orgId} to render the editable form. The token never
// round-trips — the operator pastes a new one to rotate.
func (s *Server) getTempoSettings(w http.ResponseWriter, r *http.Request) {
	if s.tempo == nil {
		// Defensive — main() always wires the service, but a unit
		// test or partial init could leave it nil; render an
		// empty snapshot rather than crashing.
		writeJSON(w, tempo.Snapshot{})
		return
	}
	writeJSON(w, s.tempo.Snapshot())
}

// putTempoSettings saves a new config + updates the live client.
// An empty `token` in the payload preserves the previously stored
// token (so a partial update from the Settings UI doesn't blank
// it by accident). The Disable path is `enabled:false` — the
// operator can untick the box without re-typing the URL / token
// they may want to flip back on later.
func (s *Server) putTempoSettings(w http.ResponseWriter, r *http.Request) {
	if s.tempo == nil {
		http.Error(w, "tempo backend not available", http.StatusServiceUnavailable)
		return
	}
	var in struct {
		Enabled            bool   `json:"enabled"`
		BaseURL            string `json:"baseUrl"`
		AuthType           string `json:"authType"`
		Token              string `json:"token"`
		Username           string `json:"username"`
		OrgID              string `json:"orgId"`
		InsecureSkipVerify bool   `json:"insecureSkipVerify"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	in.BaseURL = strings.TrimSpace(in.BaseURL)
	in.AuthType = strings.TrimSpace(in.AuthType)
	in.Username = strings.TrimSpace(in.Username)
	in.OrgID = strings.TrimSpace(in.OrgID)
	if in.Enabled && in.BaseURL == "" {
		http.Error(w, "baseUrl required when enabled", http.StatusBadRequest)
		return
	}
	switch in.AuthType {
	case "", "none", "bearer", "basic":
		// ok
	default:
		http.Error(w, "authType must be one of: none, bearer, basic", http.StatusBadRequest)
		return
	}
	// Preserve existing token when payload omits one — the UI
	// only sends a token when the operator is actively rotating
	// it. Compare on the bytes rather than HasToken so a "" wipe
	// stays distinguishable from "no change". We use a sentinel
	// `\x00` literal would be invasive; instead the contract is:
	// empty `token` = keep prior; non-empty = replace.
	cur := s.tempo.CurrentSettings()
	cfg := tempo.Settings{
		Enabled:            in.Enabled,
		BaseURL:            in.BaseURL,
		AuthType:           in.AuthType,
		Token:              in.Token,
		Username:           in.Username,
		OrgID:              in.OrgID,
		InsecureSkipVerify: in.InsecureSkipVerify,
	}
	if cfg.Token == "" {
		cfg.Token = cur.Token
	}
	if err := s.tempo.SavePersisted(r.Context(), s.store, cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishConfigReload(r.Context(), "tempo")
	snap := s.tempo.Snapshot()
	// Token + Username + OrgID never enter audit_log. HasToken is
	// the only secret-adjacent bit and it's already part of the
	// GET response shape.
	details, _ := json.Marshal(map[string]any{
		"enabled":            snap.Enabled,
		"baseUrl":            snap.BaseURL,
		"authType":           snap.AuthType,
		"hasToken":           snap.HasToken,
		"orgId":              snap.OrgID,
		"insecureSkipVerify": snap.InsecureSkipVerify,
	})
	s.audit(r, "settings.tempo.update", "settings", "tempo", string(details))
	writeJSON(w, snap)
}
