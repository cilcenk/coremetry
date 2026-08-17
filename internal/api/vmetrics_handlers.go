package api

// Settings → Metrics backend (v0.9.1150, VictoriaMetrics Faz 1).
//
// Follows the tempo_handlers.go / logstore_es_handlers.go template
// exactly: secret-free GET snapshot, empty-token-preserves PUT, and a
// Test endpoint that probes the SUBMITTED form without saving or
// swapping — so the operator can find out the URL is wrong before making
// it the store every metric surface reads from.
//
// Admin-only, all three. A read backend swap changes what every operator
// on the install sees on the Metrics page; editor is not enough. (Compare
// /api/settings/failure-slo, v0.9.1036, whose GET is ungated because it
// is a reference LINE on a chart, not a routing decision.)

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/cilcenk/coremetry/internal/vmetrics"
)

// vmSettingsInput is the shared PUT/test body. Empty `token` = keep the
// stored one (rotate by pasting a new value) — same contract as the Tempo
// token and the ES apiKey.
type vmSettingsInput struct {
	Enabled            bool   `json:"enabled"`
	BaseURL            string `json:"baseUrl"`
	AuthType           string `json:"authType"`
	Token              string `json:"token"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify"`
	// v0.9.1164 — the two query-shaping knobs. Both are plain values with no
	// preserve-on-empty rule: 0 and false are MEANINGFUL here ("use the
	// default floor", "keep the guard on"), which is the opposite of the
	// token's contract and the reason they are not folded into it.
	RateWindowFloorS           int  `json:"rateWindowFloorS"`
	AllowUnfilteredPercentiles bool `json:"allowUnfilteredPercentiles"`
}

// mergeVMSettings validates the input and folds it over the CURRENT full
// settings (token preservation). Returns a 400-able message rather than
// an error type so the handler writes it straight through. Pure —
// table-tested in vmetrics_handlers_test.go, which is where the
// empty-token-preserves rule is pinned; that rule is invisible in review
// and blanking a token silently disables auth on every subsequent read.
func mergeVMSettings(in vmSettingsInput, cur vmetrics.Settings) (vmetrics.Settings, string) {
	in.BaseURL = strings.TrimSpace(in.BaseURL)
	in.AuthType = strings.TrimSpace(in.AuthType)
	if in.Enabled && in.BaseURL == "" {
		return vmetrics.Settings{}, "baseUrl required when enabled"
	}
	switch in.AuthType {
	case "", "none", "bearer":
		// ok — VM itself has no auth; bearer covers vmauth / ingress-JWT.
	default:
		return vmetrics.Settings{}, "authType must be one of: none, bearer"
	}
	// v0.9.1164 — the floor is REJECTED rather than clamped, and the predicate
	// is vmetrics' own so the form can never accept a value the query layer
	// would then ignore (resolveRateWindowFloor falls back to the default for
	// anything out of bounds). Clamping here would be the worse failure: the
	// operator types 5, the chart uses 10, and nothing ever says so.
	//
	// The message quotes the BOUNDS from vmetrics and deliberately does not
	// print the default value: that number would be a third copy of a constant
	// that already lives in vmetrics and on the form's placeholder, and the one
	// thing worse than an unhelpful message is a message confidently naming a
	// default the engine stopped using.
	if !vmetrics.ValidRateWindowFloor(in.RateWindowFloorS) {
		return vmetrics.Settings{}, fmt.Sprintf(
			"rateWindowFloorS must be 0 (use the built-in default) or between %d and %d seconds",
			vmetrics.MinRateWindowFloorSec, vmetrics.MaxRateWindowFloorSec)
	}
	cfg := vmetrics.Settings{
		Enabled:                    in.Enabled,
		BaseURL:                    in.BaseURL,
		AuthType:                   in.AuthType,
		Token:                      in.Token,
		InsecureSkipVerify:         in.InsecureSkipVerify,
		RateWindowFloorS:           in.RateWindowFloorS,
		AllowUnfilteredPercentiles: in.AllowUnfilteredPercentiles,
	}
	if cfg.Token == "" {
		cfg.Token = cur.Token
	}
	return cfg, ""
}

// getVMSettings returns the saved config minus the token. The token never
// round-trips; hasToken is the only secret-adjacent bit the UI gets.
func (s *Server) getVMSettings(w http.ResponseWriter, r *http.Request) {
	if s.vmetrics == nil {
		// Defensive — main() always wires the service. An empty snapshot
		// renders the form in its "not configured" state, which is the
		// truth, rather than crashing the Settings tab.
		writeJSON(w, vmetrics.Snapshot{})
		return
	}
	writeJSON(w, s.vmetrics.Snapshot())
}

// putVMSettings persists a new config and swaps the live one.
//
// Unlike the logstore PUT this does NOT probe before saving. Reason: the
// operator has a dedicated Test button here, and a save-time probe would
// make "disable VM" fail whenever VM is already down — the exact moment
// they most need to turn it off. Enabling a broken URL surfaces as a 502
// on the metric surfaces with VM's own error text.
func (s *Server) putVMSettings(w http.ResponseWriter, r *http.Request) {
	if s.vmetrics == nil {
		http.Error(w, "victoriametrics backend not available", http.StatusServiceUnavailable)
		return
	}
	var in vmSettingsInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	cfg, badReq := mergeVMSettings(in, s.vmetrics.CurrentSettings())
	if badReq != "" {
		http.Error(w, badReq, http.StatusBadRequest)
		return
	}
	if err := s.vmetrics.SavePersisted(r.Context(), s.store, cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishConfigReload(r.Context(), "victoria-metrics")
	snap := s.vmetrics.Snapshot()
	// The token never enters audit_log — hasToken is the only
	// secret-adjacent field, and it is already in the GET shape.
	details, _ := json.Marshal(map[string]any{
		"enabled":            snap.Enabled,
		"baseUrl":            snap.BaseURL,
		"authType":           snap.AuthType,
		"hasToken":           snap.HasToken,
		"insecureSkipVerify": snap.InsecureSkipVerify,
		// v0.9.1164 — both knobs are audited, and the guard one is the reason
		// this list had to grow rather than stay a connection summary. Lifting
		// the bucket-scan guard is the kind of change that shows up later as
		// "vmselect fell over on Tuesday", and the audit entry is the only place
		// that can answer who lifted it and when.
		"rateWindowFloorS":           snap.RateWindowFloorS,
		"allowUnfilteredPercentiles": snap.AllowUnfilteredPercentiles,
	})
	// Action name follows the external-backend siblings
	// (settings.tempo.update / settings.thanos.update /
	// settings.logstore.update / settings.devops.update) rather than the
	// generic "settings.update" the in-app knobs use: this is the group an
	// operator filters the audit log by when asking "who changed where our
	// metrics come from".
	s.audit(r, "settings.victoria_metrics.update", "settings", "victoria_metrics", string(details))
	writeJSON(w, snap)
}

// testVMSettings probes the submitted form (with the stored-token merge)
// WITHOUT saving or swapping. Answers 200 with {ok:false,error} on a
// failed probe: a failed connection is a SUCCESSFUL answer to the
// operator's question, not an HTTP error (the devops/logstore precedent).
func (s *Server) testVMSettings(w http.ResponseWriter, r *http.Request) {
	if s.vmetrics == nil {
		http.Error(w, "victoriametrics backend not available", http.StatusServiceUnavailable)
		return
	}
	var in vmSettingsInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	// Probe the URL the operator TYPED even when the Enabled box is off —
	// the normal flow is "paste URL, test, then tick Enabled", and
	// requiring Enabled first would mean saving a config you have not
	// verified.
	in.Enabled = true
	cfg, badReq := mergeVMSettings(in, s.vmetrics.CurrentSettings())
	if badReq != "" {
		http.Error(w, badReq, http.StatusBadRequest)
		return
	}
	if err := s.vmetrics.Test(r.Context(), cfg); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
