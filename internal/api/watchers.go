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
	"context"
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

// ── /watchers read surface (v0.9.196) ───────────────────────────────
//
// The dedicated /watchers page lists the imported watcher rules from
// the EXISTING /api/alert-rules endpoint (client-side metric=='watcher'
// filter — a few hundred rows). The two endpoints below add what that
// list can't know:
//
//	GET /api/watchers/summary      — per-rule problems rollup
//	                                 (lastFire / fires24h / openNow)
//	                                 + the structural disabled reason,
//	                                 ONE bounded GROUP BY query for the
//	                                 whole fleet (never per-row calls).
//	GET /api/watchers/{id}/history — one rule's fire/resolve timeline:
//	                                 problems(rule_id) joined to their
//	                                 notification_log rows.
//
// Both are read-only (no audit) and open to any signed-in role —
// viewers see watcher state read-only, same as /api/alert-rules.

// watcherSummaryEntry is one rule's row in the summary response.
type watcherSummaryEntry struct {
	LastFire int64  `json:"lastFire"` // unix ns; 0 = never fired
	Fires24h uint64 `json:"fires24h"`
	OpenNow  bool   `json:"openNow"`
	// DisabledReason — the structural reason a disabled watcher can't
	// run (script condition, no executable search, …), recomputed from
	// the stored definition. Empty for enabled rules AND for rules the
	// operator disabled by hand (the UI shows a generic tooltip then).
	DisabledReason string `json:"disabledReason,omitempty"`
}

// watcherDisabledReason recomputes the import-time disabled verdict
// from the stored definition. The reason was never persisted on the
// rule row (only shown in the import report), so the list surface
// re-derives it — ≤ a few hundred JSON parses behind a 60s cache.
// Enabled rules answer "" without parsing. Pure — table-tested.
func watcherDisabledReason(r chstore.AlertRule) string {
	if r.Enabled || r.WatcherJSON == "" {
		return ""
	}
	wt, err := watcher.Parse([]byte(r.WatcherJSON))
	if err != nil {
		return "stored definition does not parse: " + err.Error()
	}
	if _, rep, err := watcher.ToRule(wt, []byte(r.WatcherJSON)); err == nil {
		return rep.DisabledReason // "" when the operator disabled a runnable watch by hand
	}
	return ""
}

// buildWatcherSummaries zero-fills the problems rollup against the
// rule list so EVERY watcher rule answers (a watcher that never fired
// still gets a row) and non-watcher rules never leak in. Pure —
// table-tested.
func buildWatcherSummaries(rules []chstore.AlertRule, sums map[string]chstore.WatcherSummary) map[string]watcherSummaryEntry {
	out := make(map[string]watcherSummaryEntry)
	for _, r := range rules {
		if r.Metric != "watcher" {
			continue
		}
		s := sums[r.ID] // zero value when the rule never opened a problem
		out[r.ID] = watcherSummaryEntry{
			LastFire:       s.LastFire,
			Fires24h:       s.Fires24h,
			OpenNow:        s.OpenNow,
			DisabledReason: watcherDisabledReason(r),
		}
	}
	return out
}

// GET /api/watchers/summary — rule_id → rollup for the /watchers
// list. ONE problems GROUP BY + the in-process-cached rules list; no
// request inputs, so the cache key is a constant (nothing to hash).
func (s *Server) watchersSummary(w http.ResponseWriter, r *http.Request) {
	s.serveCached(w, r, "watchers:summary", 60*time.Second, func(ctx context.Context) (any, error) {
		rules, err := s.store.ListAlertRules(ctx)
		if err != nil {
			return nil, err
		}
		sums, err := s.store.WatcherProblemSummaries(ctx)
		if err != nil {
			return nil, err
		}
		return buildWatcherSummaries(rules, sums), nil
	})
}

// watcherHistoryResponse is the /api/watchers/{id}/history envelope:
// the rule's recent problems (fire = startedAt, resolve = resolvedAt)
// plus every notification recorded for those problem ids. Slices are
// always non-nil so the client never branches on null.
type watcherHistoryResponse struct {
	Problems      []chstore.Problem         `json:"problems"`
	Notifications []chstore.NotificationLog `json:"notifications"`
}

// GET /api/watchers/{id}/history — one watcher's timeline. Existence
// (+ watcher-ness) is checked against the in-process-cached rules list
// so a bad deep-link 404s without a CH round-trip.
func (s *Server) watcherHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, `{"error":"id required"}`, http.StatusBadRequest)
		return
	}
	rules, err := s.store.ListAlertRules(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	found := watcherRuleExists(rules, id)
	if !found {
		// v0.9.196 review-fix — multi-replica taze-import yarışı: import
		// başka pod'a düştüyse bu pod'un 30s'lik in-process rule cache'i
		// yeni watcher'ı henüz görmez ve taze import'a 404 döner ("import
		// bozuk" izlenimi). Miss yolunda BİR kez cache'i düşürüp yeniden
		// listele; hot path cache'ten sürmeye devam eder.
		s.store.InvalidateAlertRulesCache()
		if fresh, ferr := s.store.ListAlertRules(r.Context()); ferr == nil {
			found = watcherRuleExists(fresh, id)
		}
	}
	if !found {
		http.Error(w, `{"error":"watcher not found"}`, http.StatusNotFound)
		return
	}
	// Cache key hashes all inputs: the id (limits are fixed constants).
	s.serveCached(w, r, "watchers:history:"+id, 30*time.Second, func(ctx context.Context) (any, error) {
		problems, err := s.store.ListProblems(ctx, chstore.ProblemFilter{RuleID: id, Limit: 50})
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(problems))
		for _, p := range problems {
			ids = append(ids, p.ID)
		}
		notifs, err := s.store.ListNotificationLogByRelated(ctx, ids, 100)
		if err != nil {
			return nil, err
		}
		if problems == nil {
			problems = []chstore.Problem{}
		}
		if notifs == nil {
			notifs = []chstore.NotificationLog{}
		}
		return watcherHistoryResponse{Problems: problems, Notifications: notifs}, nil
	})
}

// watcherRuleExists reports whether id names a watcher rule in the list —
// the shared predicate of watcherHistory's cached + cache-bypassed checks.
func watcherRuleExists(rules []chstore.AlertRule, id string) bool {
	for _, rule := range rules {
		if rule.ID == id && rule.Metric == "watcher" {
			return true
		}
	}
	return false
}
