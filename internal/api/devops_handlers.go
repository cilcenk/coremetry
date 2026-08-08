package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cilcenk/coremetry/internal/devops"
)

// Settings → Azure DevOps / TFS connection (v0.9.829).
//
// DELIBERATELY NARROW SLICE: connection only. Repo mapping and
// stack-frame → source review are a later release once the
// operator's repo-naming pattern is settled, so nothing consumes
// this config yet — the tab exists so the credentials and the
// reachability question can be settled independently of the
// feature that will use them.
//
// Follows logstore_es_handlers.go / tempo_handlers.go: secret-free
// GET snapshot, empty-secret-preserves PUT via a pure merge
// helper, and a Test endpoint that probes a candidate without
// saving or swapping anything.

// secretKept is the sentinel a UI sends back when it is round-
// tripping a stored secret it never saw (the RAG sources use the
// same literal — internal/api/rag.go). Treated identically to an
// empty string: keep what is stored.
const secretKept = "********"

// devopsSettingsInput is the shared PUT / test body.
type devopsSettingsInput struct {
	BaseURL            string `json:"baseUrl"`
	Collection         string `json:"collection"`
	Project            string `json:"project"`
	Username           string `json:"username"`
	PAT                string `json:"pat"`
	Flavor             string `json:"flavor"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify"`
}

// mergeDevOpsSettings validates the input and folds it over the
// currently-stored config so the PAT survives an edit that didn't
// re-type it. Returns a 400-able message rather than an error
// value — the handler writes it straight through.
//
// Secret contract (three cases, all covered by the table test):
//   - ""          → keep stored
//   - "********"  → keep stored (UI round-trip sentinel)
//   - anything else → replace
//
// There is deliberately no "clear the PAT" input value: clearing
// happens by clearing the whole connection (empty baseUrl), which
// is the honest operator gesture. A magic wipe-sentinel is one
// typo away from silently dropping working credentials.
func mergeDevOpsSettings(in devopsSettingsInput, cur devops.Settings) (devops.Settings, string) {
	cfg := devops.Settings{
		BaseURL:            strings.TrimSpace(in.BaseURL),
		Collection:         strings.TrimSpace(in.Collection),
		Project:            strings.TrimSpace(in.Project),
		Username:           strings.TrimSpace(in.Username),
		PAT:                in.PAT,
		Flavor:             strings.TrimSpace(in.Flavor),
		InsecureSkipVerify: in.InsecureSkipVerify,
	}
	if cfg.Flavor == "" {
		cfg.Flavor = devops.FlavorAuto
	}
	switch cfg.Flavor {
	case devops.FlavorAuto, devops.FlavorServer, devops.FlavorTFS:
	default:
		return devops.Settings{}, "flavor must be one of: auto, azure-devops-server, tfs"
	}
	if cfg.BaseURL != "" {
		lower := strings.ToLower(cfg.BaseURL)
		if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
			return devops.Settings{}, "server URL must start with http:// or https://"
		}
	}
	if cfg.PAT == "" || cfg.PAT == secretKept {
		cfg.PAT = cur.PAT
	}
	// Clearing the server URL removes the integration, so the
	// credential goes with it. Otherwise "keep stored" would leave
	// an orphaned PAT sitting in system_settings that no screen
	// shows and no operator remembers granting.
	if cfg.BaseURL == "" {
		cfg.PAT = ""
	}
	return cfg, ""
}

// getDevOpsSettings returns the saved connection config minus the
// PAT. The UI renders hasPat as a "stored" indicator; the token
// itself never round-trips.
func (s *Server) getDevOpsSettings(w http.ResponseWriter, r *http.Request) {
	if s.devops == nil {
		// Defensive — main() always wires the service, but a partial
		// init (unit test) should render an empty snapshot rather
		// than crash the settings page.
		writeJSON(w, devops.Snapshot{})
		return
	}
	writeJSON(w, s.devops.Snapshot())
}

// putDevOpsSettings persists the config and swaps the live client.
// Unlike the Elasticsearch tab this does NOT apply-first: nothing
// reads this config yet, so refusing to save an unreachable server
// would only stop an operator from staging credentials ahead of
// the box being reachable. "Test connection" is the separate,
// explicit reachability gesture.
func (s *Server) putDevOpsSettings(w http.ResponseWriter, r *http.Request) {
	if s.devops == nil {
		http.Error(w, "devops connection not available", http.StatusServiceUnavailable)
		return
	}
	var in devopsSettingsInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	cfg, badReq := mergeDevOpsSettings(in, s.devops.CurrentSettings())
	if badReq != "" {
		http.Error(w, badReq, http.StatusBadRequest)
		return
	}
	if err := s.devops.SavePersisted(r.Context(), s.store, cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishConfigReload(r.Context(), "devops")
	snap := s.devops.Snapshot()
	s.audit(r, "settings.devops.update", "settings", "devops_connection",
		string(devopsAuditDetails(snap)))
	writeJSON(w, snap)
}

// devopsAuditDetails builds the audit payload. The PAT and the
// username never enter audit_log — hasPat is the only secret-
// adjacent bit, and it is already part of the GET response shape.
// The server URL is not a secret and is the single most useful
// thing to have in the trail when someone asks "who pointed this
// at the wrong collection?".
func devopsAuditDetails(snap devops.Snapshot) []byte {
	b, _ := json.Marshal(map[string]any{
		"baseUrl":            snap.BaseURL,
		"collection":         snap.Collection,
		"project":            snap.Project,
		"flavor":             snap.Flavor,
		"hasPat":             snap.HasPAT,
		"insecureSkipVerify": snap.InsecureSkipVerify,
	})
	return b
}

// testDevOpsSettings probes the submitted form values — with the
// stored PAT merged in when the field was left blank — WITHOUT
// saving. Connection failures come back as {ok:false, error} with
// 200, not an HTTP error: a failed probe is a successful answer to
// the operator's question.
func (s *Server) testDevOpsSettings(w http.ResponseWriter, r *http.Request) {
	if s.devops == nil {
		http.Error(w, "devops connection not available", http.StatusServiceUnavailable)
		return
	}
	var in devopsSettingsInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	cfg, badReq := mergeDevOpsSettings(in, s.devops.CurrentSettings())
	if badReq != "" {
		http.Error(w, badReq, http.StatusBadRequest)
		return
	}
	writeJSON(w, s.devops.Test(r.Context(), cfg))
}
