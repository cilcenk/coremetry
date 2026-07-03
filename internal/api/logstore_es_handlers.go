package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// Settings → Elasticsearch handlers (v0.8.232, operator-requested:
// configure the logs backend from the UI and SEE the error when it's
// wrong). Mirrors the tempo_handlers.go template: secret-free GET
// snapshot, empty-secret-preserves PUT, and a Test endpoint that
// surfaces the real connection error without touching the live store.

// esSettingsInput is the shared PUT/test body. Password/APIKey empty =
// keep the stored value (rotate by pasting a new one) — same contract
// as the tempo token.
type esSettingsInput struct {
	Backend            string              `json:"backend"`
	Addresses          []string            `json:"addresses"`
	Username           string              `json:"username"`
	Password           string              `json:"password"`
	APIKey             string              `json:"apiKey"`
	InsecureSkipVerify bool                `json:"insecureSkipVerify"`
	Index              string              `json:"index"`
	IndexTemplate      string              `json:"indexTemplate"`
	Fields             logstore.ESFieldMap `json:"fields"`
}

// mergeESSettings validates the input and folds it over the current
// full settings (secret preservation). Returns a 400-able error string
// instead of an error type — handler writes it straight.
func mergeESSettings(in esSettingsInput, cur logstore.ESSettings) (logstore.ESSettings, string) {
	in.Backend = strings.TrimSpace(in.Backend)
	if in.Backend == "" {
		in.Backend = "clickhouse"
	}
	switch in.Backend {
	case "clickhouse", "elasticsearch":
	default:
		return logstore.ESSettings{}, "backend must be one of: clickhouse, elasticsearch"
	}
	addrs := make([]string, 0, len(in.Addresses))
	for _, a := range in.Addresses {
		if a = strings.TrimSpace(a); a != "" {
			addrs = append(addrs, a)
		}
	}
	if in.Backend == "elasticsearch" && len(addrs) == 0 {
		return logstore.ESSettings{}, "at least one address required for the elasticsearch backend"
	}
	cfg := logstore.ESSettings{
		Backend:            in.Backend,
		Addresses:          addrs,
		Username:           strings.TrimSpace(in.Username),
		Password:           in.Password,
		APIKey:             strings.TrimSpace(in.APIKey),
		InsecureSkipVerify: in.InsecureSkipVerify,
		Index:              strings.TrimSpace(in.Index),
		IndexTemplate:      strings.TrimSpace(in.IndexTemplate),
		Fields:             in.Fields,
	}
	if cfg.Password == "" {
		cfg.Password = cur.Password
	}
	if cfg.APIKey == "" {
		cfg.APIKey = cur.APIKey
	}
	return cfg, ""
}

func (s *Server) getLogstoreESSettings(w http.ResponseWriter, r *http.Request) {
	if s.logsMgr == nil {
		http.Error(w, "logstore settings not available", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, s.logsMgr.Snapshot())
}

// putLogstoreESSettings applies + persists a new logs-backend config.
// Apply-first (build + eager ping): a config that can't connect comes
// back as the REAL ES error in the response body and nothing is saved
// — the live backend keeps serving.
func (s *Server) putLogstoreESSettings(w http.ResponseWriter, r *http.Request) {
	if s.logsMgr == nil {
		http.Error(w, "logstore settings not available", http.StatusServiceUnavailable)
		return
	}
	var in esSettingsInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	cfg, badReq := mergeESSettings(in, s.logsMgr.CurrentSettings())
	if badReq != "" {
		http.Error(w, badReq, http.StatusBadRequest)
		return
	}
	if err := s.logsMgr.SavePersisted(r.Context(), s.store, cfg); err != nil {
		// 502: the config was well-formed but the backend rejected it
		// (bad address / credential / TLS). Body carries the real error
		// — the whole point of the UI flow.
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.publishConfigReload(r.Context(), "logstore")
	snap := s.logsMgr.Snapshot()
	// Secrets never enter audit_log — hasPassword/hasApiKey only.
	details, _ := json.Marshal(map[string]any{
		"backend":            snap.Backend,
		"addresses":          snap.Addresses,
		"hasPassword":        snap.HasPassword,
		"hasApiKey":          snap.HasAPIKey,
		"index":              snap.Index,
		"indexTemplate":      snap.IndexTemplate,
		"insecureSkipVerify": snap.InsecureSkipVerify,
	})
	s.audit(r, "settings.logstore.update", "settings", "logstore_es", string(details))
	writeJSON(w, snap)
}

// testLogstoreESSettings builds a candidate backend from the submitted
// form (with stored-secret merge) and pings it — WITHOUT swapping or
// saving. The Settings tab's "Test" button.
func (s *Server) testLogstoreESSettings(w http.ResponseWriter, r *http.Request) {
	if s.logsMgr == nil {
		http.Error(w, "logstore settings not available", http.StatusServiceUnavailable)
		return
	}
	var in esSettingsInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	cfg, badReq := mergeESSettings(in, s.logsMgr.CurrentSettings())
	if badReq != "" {
		http.Error(w, badReq, http.StatusBadRequest)
		return
	}
	if err := s.logsMgr.Test(r.Context(), cfg); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
