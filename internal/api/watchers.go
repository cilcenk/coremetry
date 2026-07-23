package api

// POST /api/watchers/import — ES Watcher Faz-1 AŞAMA B import
// surface. Takes the exact PUT _watcher/watch body, runs it through
// internal/watcher (Parse → Validate → ToRule) and either returns
// the field-by-field mapping report (dryRun) or persists the
// projected alert rule with the raw definition attached
// (AlertRule.WatcherJSON, Metric="watcher" — the evaluator's watcher
// path picks it up on the next tick).
//
// Contract:
//
//	{"name": "...", "watch": {…raw watch…}, "dryRun": true|false}
//
//	dryRun  → {"report": {findings, enabled, disabledReason?, rule}}
//	import  → same + {"imported": true, "ruleId": "…"}
//
// Name resolution: explicit request name wins; otherwise the watch's
// metadata.name; neither → 400. Faz-1 has NO overwrite — an existing
// rule with the same name answers 409 and the operator renames.
// Watches whose condition can't be evaluated (script / never /
// non-hits.total compare) import DISABLED with the reason in the
// report; nothing silently fires wrong.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/watcher"
)

type watcherImportRequest struct {
	Name string `json:"name"`
	// Watch is the raw ES watch body — kept as RawMessage so the
	// stored definition round-trips byte-identical (watcher package
	// design contract).
	Watch json.RawMessage `json:"watch"`
	// WatchText is the operator's LITERAL paste as a JSON string
	// (review F5): the frontend sends the textarea content untouched —
	// a browser-side JSON.parse→stringify round-trip silently rounds
	// integers above 2^53 (epoch-nanos bounds, 64-bit IDs) and
	// reformats the definition, breaking the byte-verbatim contract
	// before the request ever leaves the browser. When set it wins
	// over Watch. It may carry Kibana DevTools """triple-quoted"""
	// strings; watcher.NormalizeDevTools converts those to valid JSON
	// and THAT normalized form is what gets stored — the evaluator
	// re-parses the stored definition as JSON every run, so it must be
	// valid JSON. Bodies without `"""` normalize byte-identically.
	WatchText string `json:"watchText,omitempty"`
	DryRun    bool   `json:"dryRun"`
}

// watchSource resolves the watch bytes: the literal paste wins, the
// parsed-object form stays for API compatibility.
func (r watcherImportRequest) watchSource() []byte {
	if len(bytes.TrimSpace([]byte(r.WatchText))) > 0 {
		return []byte(r.WatchText)
	}
	return r.Watch
}

// watcherRulePreview is the projection slice shown at dry-run time —
// exactly the fields ToRule derives from the watch, so the operator
// sees what will execute before anything persists.
type watcherRulePreview struct {
	Name        string  `json:"name"`
	Comparator  string  `json:"comparator"`
	Threshold   float64 `json:"threshold"`
	WindowSec   uint32  `json:"windowSec"`
	CooldownSec uint32  `json:"cooldownSec"`
}

// watcherImportReport is the response envelope for both dry-run and
// live import: the watcher package's findings plus the rule
// projection preview.
type watcherImportReport struct {
	Findings       []watcher.Finding  `json:"findings"`
	Enabled        bool               `json:"enabled"`
	DisabledReason string             `json:"disabledReason,omitempty"`
	Rule           watcherRulePreview `json:"rule"`
}

// buildWatcherImport is the pure stage of the import handler:
// request body → (projected rule, mapping report). Errors are
// operator-input errors (400s); nothing here touches the store.
func buildWatcherImport(req watcherImportRequest) (chstore.AlertRule, watcherImportReport, error) {
	src := watcher.NormalizeDevTools(req.watchSource())
	if len(bytes.TrimSpace(src)) == 0 {
		return chstore.AlertRule{}, watcherImportReport{}, fmt.Errorf("watch: empty definition — paste the PUT _watcher/watch body")
	}
	w, err := watcher.Parse(src)
	if err != nil {
		return chstore.AlertRule{}, watcherImportReport{}, err
	}
	rule, rep, err := watcher.ToRule(w, src)
	if err != nil {
		return chstore.AlertRule{}, watcherImportReport{}, err
	}
	if name := strings.TrimSpace(req.Name); name != "" {
		rule.Name = name
	}
	if rule.Name == "" {
		return chstore.AlertRule{}, watcherImportReport{}, fmt.Errorf("name required — the watch has no metadata.name; provide one in the request")
	}
	findings := rep.Findings
	if findings == nil {
		findings = []watcher.Finding{}
	}
	return rule, watcherImportReport{
		Findings:       findings,
		Enabled:        rule.Enabled,
		DisabledReason: rep.DisabledReason,
		Rule: watcherRulePreview{
			Name:        rule.Name,
			Comparator:  rule.Comparator,
			Threshold:   rule.Threshold,
			WindowSec:   rule.WindowSec,
			CooldownSec: rule.CooldownSec,
		},
	}, nil
}

// watcherImportPlan is the post-parse flow decision: dry-run stops
// at the report; a live import refuses name collisions (Faz-1 has no
// overwrite) and otherwise proceeds.
type watcherImportPlan int

const (
	watcherPlanReportOnly watcherImportPlan = iota
	watcherPlanConflict
	watcherPlanImport
)

func planWatcherImport(dryRun bool, name string, existing []chstore.AlertRule) watcherImportPlan {
	if dryRun {
		return watcherPlanReportOnly
	}
	name = strings.TrimSpace(name)
	for _, r := range existing {
		if strings.TrimSpace(r.Name) == name {
			return watcherPlanConflict
		}
	}
	return watcherPlanImport
}

func (s *Server) importWatcher(w http.ResponseWriter, r *http.Request) {
	var req watcherImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad JSON body"}`, http.StatusBadRequest)
		return
	}
	rule, report, err := buildWatcherImport(req)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	if req.DryRun {
		writeJSON(w, map[string]any{"report": report})
		return
	}
	// Review F8 — the name-collision check below is check-then-insert.
	// This mutex closes the same-pod race (double-click, two tabs);
	// cross-pod imports inside the ~30s alert-rules cache TTL can
	// still slip past and merely produce two visibly duplicate
	// same-named rules with distinct IDs (no data corruption — the
	// native create path has no name guard at all, so this stays
	// best-effort UX by design; full closure would need the Redis
	// mutex infra, overkill for an operator-paced import flow).
	s.watcherImportMu.Lock()
	defer s.watcherImportMu.Unlock()
	existing, err := s.store.ListAlertRules(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if planWatcherImport(false, rule.Name, existing) == watcherPlanConflict {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("an alert rule named %q already exists — rename the import (Faz-1 has no overwrite)", rule.Name),
		})
		return
	}
	rule.ID = newID(8)
	rule.BuiltIn = false
	rule.CreatedAt = time.Now().UnixNano()
	if err := s.store.UpsertAlertRule(r.Context(), rule); err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, "watcher.import", "alert_rule", rule.ID, rule.Name)
	writeJSON(w, map[string]any{"imported": true, "ruleId": rule.ID, "report": report})
}
