package chstore

import (
	"context"
	"fmt"
	"time"
)

// AlertRule defines an evaluator condition. metric is one of:
//   error_rate    — % of error spans  (operand: percentage)
//   p99_ms / p95_ms / avg_ms / p50_ms — latency in ms
//   request_rate  — spans per second  (typically used with `<` to detect drops)
//   error_count   — number of error spans (absolute)
type AlertRule struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Service    string  `json:"service"`     // empty = applies to all services
	Metric     string  `json:"metric"`      // error_rate | p99_ms | request_rate | …
	Comparator string  `json:"comparator"`  // > | >= | < | <=
	Threshold  float64 `json:"threshold"`
	WindowSec  uint32  `json:"windowSec"`   // sliding window size
	Severity   string  `json:"severity"`    // info | warning | critical
	Enabled    bool    `json:"enabled"`
	BuiltIn    bool    `json:"builtIn"`
	// ForSec is the sustained-breach gate (v0.5.126): the
	// threshold must stay breached for this long before a
	// problem opens. Prometheus-style `for:` — kills single-
	// sample spike noise without changing the threshold. 0 =
	// open immediately (current behaviour).
	ForSec uint32 `json:"forSec"`
	// MinSamples is the sample-count floor (v0.5.128). When > 0
	// the evaluator only fires if the window saw at least this
	// many requests — kills low-traffic flapping on rate /
	// percentile metrics (a 1-request window with 1 error = 100%
	// error_rate, which is meaningless). 0 = no floor.
	MinSamples uint32 `json:"minSamples"`
	// CooldownSec is the post-resolution silence window (v0.5.129).
	// After a problem auto-resolves the evaluator suppresses re-
	// opens on the same (rule, service) for this many seconds —
	// kills threshold-jitter flapping where the value oscillates
	// at the boundary. 0 = re-open immediately.
	CooldownSec uint32 `json:"cooldownSec"`
	// RunbookURL — optional link an oncall reaches when the
	// rule fires. Surfaces on Problem detail + alert
	// notifications. Empty = no runbook configured.
	RunbookURL string  `json:"runbookUrl,omitempty"`
	// LogQuery (v0.5.242) — KQL/Lucene clause that defines a
	// "saved-search alert". When set, the evaluator counts log
	// matches via the logstore in the rule's window and compares
	// to Threshold via Comparator instead of running the
	// span-derived Metric path. Service/Metric are still set
	// (service="" + metric="log_query" by convention) so the
	// rules table renders consistently. The OTel-canonical
	// shorthand (level:error, pod:my-pod) is rewritten by the
	// ES backend's expandShorthand so the same query works
	// against any shipping pipeline.
	LogQuery   string  `json:"logQuery,omitempty"`
	CreatedAt  int64   `json:"createdAt"`   // unix nanoseconds
}

type Problem struct {
	ID          string  `json:"id"`
	RuleID      string  `json:"ruleId"`
	RuleName    string  `json:"ruleName"`
	Severity    string  `json:"severity"`
	Service     string  `json:"service"`
	Metric      string  `json:"metric"`
	Value       float64 `json:"value"`
	Threshold   float64 `json:"threshold"`
	Status      string  `json:"status"`     // open | resolved
	Description string  `json:"description"`
	// Assignee (v0.5.209) — free-form string, two flavours:
	//   • team name auto-set from service_metadata.owner_team
	//     when the problem opens (so "payments" surfaces without
	//     an operator action)
	//   • email of a specific operator after manual claim/assign
	// Empty = unassigned. Operator-editable via PATCH
	// /api/problems/{id}/assignee.
	Assignee    string  `json:"assignee,omitempty"`
	StartedAt   int64   `json:"startedAt"`  // unix ns
	ResolvedAt  *int64  `json:"resolvedAt,omitempty"`
	// RunbookURL — composed at read time from the firing
	// alert rule (preferred) or the service catalog metadata
	// (fallback). Not stored on the problems table; the URL
	// is operator-curated and likely to change between when
	// the problem opened and when an oncall reads it. NEVER
	// scanned from CH — populated by EnrichProblems.
	RunbookURL  string  `json:"runbookUrl,omitempty"`
	// Clusters — k8s/openshift cluster names this problem's
	// service was active in around the time of the alert.
	// Populated at READ time from recent span activity (NOT
	// stored on the problems table). Empty when the service
	// hasn't carried a cluster attribute. Multi-cluster
	// services typically list 2-3 names; the UI renders
	// chips so the oncall sees "this fires on eu-west AND
	// eu-central" at a glance.
	Clusters []string `json:"clusters,omitempty"`
	// RecentDeploy — most recent observed service.version
	// transition for this service in the window leading up
	// to the problem firing, or nil. The AI explain /
	// runbook prompts use this signal to ask "did a deploy
	// just happen?", and the UI surfaces it as a small
	// "deployed v1.2.3 · 6 min before" tag next to the
	// problem row. Populated at READ time from spans (NOT
	// stored on the problems table) — the deploy might be
	// confirmed retroactively after the row was written.
	RecentDeploy *RecentDeploy `json:"recentDeploy,omitempty"`
	// Priority (v0.5.210) — computed at read time from severity +
	// breach magnitude + deploy proximity. Three buckets:
	//   • P1 — handle now (critical + significant overshoot
	//     OR critical + just deployed). Top of the triage queue.
	//   • P2 — handle today (criticals at minor overshoot, OR
	//     warnings hit by recent deploy / 2x threshold breach).
	//   • P3 — handle when convenient (steady warnings, info-
	//     level rules). Filterable out of the default view.
	// Not stored on the problems table — recomputed every read
	// so a fresh deploy or a worsening value flips the bucket
	// without requiring a re-write of the row.
	Priority     string        `json:"priority,omitempty"`
	// PriorityReason — short human string explaining the bucket
	// pick ("critical + deploy 4m before", "2.5x threshold").
	// Driven by the same logic that sets Priority; surfaces in
	// the UI tooltip so the rule is auditable, not magic.
	PriorityReason string      `json:"priorityReason,omitempty"`
	// AISummary (v0.5.254) — short LLM-generated context blurb
	// answering "why did this fire + what to look at first". Filled
	// asynchronously by the problemExplainer goroutine within ~30s
	// of problem open (critical severity only by default). Empty
	// when the explainer hasn't run yet OR the AI Copilot isn't
	// configured. AISummaryAt is the unix-ns timestamp of the last
	// generation; lets the UI show "AI insight · 12s ago".
	AISummary    string `json:"aiSummary,omitempty"`
	AISummaryAt  int64  `json:"aiSummaryAt,omitempty"`
}

// RecentDeploy is the compact deploy signal attached to a
// firing Problem. AgeSeconds = problem.StartedAt - deploy.time,
// rounded — positive means deploy was BEFORE the problem
// (the typical correlate-with-incident case).
type RecentDeploy struct {
	Version    string `json:"version"`
	TimeUnixNs int64  `json:"timeUnixNs"`
	AgeSeconds int64  `json:"ageSeconds"`
}

// EnrichProblemsWithPriority computes the P1/P2/P3 triage bucket
// for every problem in the slice. Pure function over already-
// loaded fields — no CH round-trip — so this is the last step
// in the enrichment chain and runs against the post-runbook /
// cluster / deploy values. Doesn't mutate stored rows; the
// bucket lives only on the wire so a worsening metric or a
// fresh deploy flips the rank on the next read.
//
// Blend formula (transparent on purpose — operator sees it in
// PriorityReason and can argue with it):
//
//   P1 (drop-everything) when ANY of:
//     • severity = critical AND value ≥ 2x threshold     (significant breach)
//     • severity = critical AND deploy ≤ 5min ago        (post-deploy critical)
//     • severity = critical AND open ≥ 4h                (stale critical)
//
//   P2 (today) when ANY of:
//     • severity = critical                              (criticals default to P2)
//     • severity = warning  AND value ≥ 2x threshold     (significant warning)
//     • severity = warning  AND deploy ≤ 5min ago        (post-deploy warning)
//
//   P3 otherwise — steady warnings, info-level rules.
//
// "Value above threshold" only makes sense when comparator is
// >= / >; for < comparators we use the inverse ratio. info
// severity always pins to P3.
//
// Reason string is the FIRST trigger that fired — so the
// tooltip surfaces the most relevant signal (e.g. "deploy 4m
// ago" beats "1.8x threshold" when both apply, because the
// former is the more actionable correlate).
func EnrichProblemsWithPriority(problems []Problem) []Problem {
	now := time.Now().UnixNano()
	for i := range problems {
		p := &problems[i]
		p.Priority, p.PriorityReason = computePriority(*p, now)
	}
	return problems
}

func computePriority(p Problem, nowNs int64) (string, string) {
	sev := p.Severity
	if sev == "info" {
		return "P3", "info"
	}

	// Breach magnitude. If threshold is 0 we can't compute a
	// ratio — fall back to severity alone.
	bigBreach := false
	if p.Threshold != 0 {
		ratio := p.Value / p.Threshold
		// For "<" or "<=" rules (e.g. uptime fell below 99%),
		// the value drops as things get worse — flip the
		// ratio.
		if ratio < 1 && ratio > 0 {
			ratio = 1 / ratio
		}
		if ratio >= 2 {
			bigBreach = true
		}
	}

	postDeploy := p.RecentDeploy != nil && p.RecentDeploy.AgeSeconds > 0 && p.RecentDeploy.AgeSeconds <= 5*60

	// Stale-critical: still open after 4h of operator inactivity.
	openHours := float64(nowNs-p.StartedAt) / float64(time.Hour)
	staleCritical := p.Status != "resolved" && openHours >= 4

	if sev == "critical" {
		switch {
		case postDeploy:
			return "P1", fmt.Sprintf("critical + deploy %ds before", p.RecentDeploy.AgeSeconds)
		case bigBreach:
			return "P1", fmt.Sprintf("critical + %.1fx threshold", p.Value/p.Threshold)
		case staleCritical:
			return "P1", fmt.Sprintf("critical open %.1fh", openHours)
		default:
			return "P2", "critical"
		}
	}

	// severity = warning
	switch {
	case postDeploy:
		return "P2", fmt.Sprintf("warning + deploy %ds before", p.RecentDeploy.AgeSeconds)
	case bigBreach:
		return "P2", fmt.Sprintf("warning + %.1fx threshold", p.Value/p.Threshold)
	default:
		return "P3", "warning steady"
	}
}

// EnrichProblemsWithClusters fills each problem's Clusters
// field from the recent service-to-cluster map. One batch
// query covers every problem in the slice. Soft-fails: if
// the lookup errors we return the slice unchanged rather
// than blocking the page on a transient CH blip.
func (s *Store) EnrichProblemsWithClusters(ctx context.Context, problems []Problem, since time.Duration) []Problem {
	if len(problems) == 0 {
		return problems
	}
	m, err := s.GetServiceClusterMap(ctx, since)
	if err != nil {
		return problems
	}
	for i := range problems {
		if cs, ok := m[problems[i].Service]; ok && len(cs) > 0 {
			problems[i].Clusters = cs
		}
	}
	return problems
}

// EnrichIncidentsWithClusters mirrors EnrichProblemsWithClusters
// for the Incidents list. Same single-batch lookup, same soft-
// fail behaviour.
func (s *Store) EnrichIncidentsWithClusters(ctx context.Context, incidents []Incident, since time.Duration) []Incident {
	if len(incidents) == 0 {
		return incidents
	}
	m, err := s.GetServiceClusterMap(ctx, since)
	if err != nil {
		return incidents
	}
	for i := range incidents {
		if cs, ok := m[incidents[i].Service]; ok && len(cs) > 0 {
			incidents[i].Clusters = cs
		}
	}
	return incidents
}

// EnrichAnomaliesWithClusters mirrors the pair above for
// AnomalyEvent. Same one-shot map lookup keyed by service.
func (s *Store) EnrichAnomaliesWithClusters(ctx context.Context, events []AnomalyEvent, since time.Duration) []AnomalyEvent {
	if len(events) == 0 {
		return events
	}
	m, err := s.GetServiceClusterMap(ctx, since)
	if err != nil {
		return events
	}
	for i := range events {
		if cs, ok := m[events[i].Service]; ok && len(cs) > 0 {
			events[i].Clusters = cs
		}
	}
	return events
}

// EnrichProblemsWithDeploys attaches the most recent
// observed service.version deploy that happened up to
// `lookback` before each problem's started_at. Single bulk
// CH query covers every service across every problem in the
// slice — N+1 free regardless of problem count. Soft-fails:
// CH error returns the slice unchanged rather than blocking
// the page render.
//
// Mechanism: one GROUP BY over spans for the union of
// involved services in [min(started)-lookback, max(started)],
// then per-problem in-memory match against the highest
// first_seen time ≤ that problem's started_at.
func (s *Store) EnrichProblemsWithDeploys(ctx context.Context, problems []Problem, lookback time.Duration) []Problem {
	if len(problems) == 0 {
		return problems
	}
	// Distinct services + global time window across the page.
	services := map[string]struct{}{}
	var minStarted, maxStarted int64
	for i, p := range problems {
		if p.Service == "" {
			continue
		}
		services[p.Service] = struct{}{}
		if i == 0 || p.StartedAt < minStarted {
			minStarted = p.StartedAt
		}
		if p.StartedAt > maxStarted {
			maxStarted = p.StartedAt
		}
	}
	if len(services) == 0 {
		return problems
	}
	svcList := make([]any, 0, len(services))
	for s := range services {
		svcList = append(svcList, s)
	}
	from := time.Unix(0, minStarted).Add(-lookback)
	to := time.Unix(0, maxStarted)

	// Build positional placeholders for the IN clause.
	holders := ""
	for i := range svcList {
		if i > 0 {
			holders += ","
		}
		holders += "?"
	}
	sql := `
		SELECT service_name,
		       res_values[indexOf(res_keys, 'service.version')] AS version,
		       toUnixTimestamp64Nano(min(time))                 AS first_seen_ns
		FROM spans
		WHERE service_name IN (` + holders + `)
		  AND time >= ? AND time <= ?
		  AND has(res_keys, 'service.version')
		GROUP BY service_name, version
		HAVING version != ''
		ORDER BY service_name, first_seen_ns ASC
		SETTINGS max_execution_time = 10`
	args := append([]any{}, svcList...)
	args = append(args, from, to)
	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return problems
	}
	defer rows.Close()
	// Map: service → []deploy{version, ns} ordered ascending.
	type d struct {
		version string
		ns      int64
	}
	byService := map[string][]d{}
	for rows.Next() {
		var svc, ver string
		var ns int64
		if err := rows.Scan(&svc, &ver, &ns); err != nil {
			return problems
		}
		byService[svc] = append(byService[svc], d{ver, ns})
	}
	if err := rows.Err(); err != nil {
		return problems
	}
	lookbackNs := int64(lookback)
	for i := range problems {
		list := byService[problems[i].Service]
		if len(list) == 0 {
			continue
		}
		// Find latest deploy with ns ≤ problem.StartedAt and
		// ns ≥ problem.StartedAt-lookback. List is asc, so
		// walk from the end.
		var pick *d
		for j := len(list) - 1; j >= 0; j-- {
			if list[j].ns > problems[i].StartedAt {
				continue
			}
			if list[j].ns < problems[i].StartedAt-lookbackNs {
				break
			}
			pick = &list[j]
			break
		}
		if pick != nil {
			problems[i].RecentDeploy = &RecentDeploy{
				Version:    pick.version,
				TimeUnixNs: pick.ns,
				AgeSeconds: (problems[i].StartedAt - pick.ns) / 1e9,
			}
		}
	}
	return problems
}

// EnrichAnomaliesWithDeploys is the AnomalyEvent twin of
// EnrichProblemsWithDeploys. v0.5.286 — same one-shot
// bulk-query pattern (one round-trip regardless of how many
// services / events are in the slice). Each event's
// RecentDeploy points at the most recent deploy of that
// service whose first_seen falls in
// [event.startedAt-lookback, event.startedAt]. Uses the
// effectiveVersionExpr chain (v0.5.283) so Helm-only
// installs (app.kubernetes.io/version label) and image-tag
// fallbacks correlate too, not just bare service.version.
func (s *Store) EnrichAnomaliesWithDeploys(ctx context.Context, events []AnomalyEvent, lookback time.Duration) []AnomalyEvent {
	if len(events) == 0 {
		return events
	}
	services := map[string]struct{}{}
	var minStarted, maxStarted int64
	for i, e := range events {
		if e.Service == "" {
			continue
		}
		services[e.Service] = struct{}{}
		if i == 0 || e.StartedAt < minStarted {
			minStarted = e.StartedAt
		}
		if e.StartedAt > maxStarted {
			maxStarted = e.StartedAt
		}
	}
	if len(services) == 0 {
		return events
	}
	svcList := make([]any, 0, len(services))
	for s := range services {
		svcList = append(svcList, s)
	}
	from := time.Unix(0, minStarted).Add(-lookback)
	to := time.Unix(0, maxStarted)

	holders := ""
	for i := range svcList {
		if i > 0 {
			holders += ","
		}
		holders += "?"
	}
	// v0.5.286 — uses effectiveVersionExpr (the same Helm /
	// image-tag / placeholder-filtered chain GetRecentDeploys
	// uses) so the correlation finds deploys even when
	// service.version stays at "0.0.1-SNAPSHOT" or the
	// pipeline only ships labels via Helm.
	sql := `
		SELECT service_name,
		       ` + effectiveVersionExpr + ` AS version,
		       toUnixTimestamp64Nano(min(time))                 AS first_seen_ns
		FROM spans
		WHERE service_name IN (` + holders + `)
		  AND time >= ? AND time <= ?
		  AND (has(res_keys, 'service.version')
		    OR has(res_keys, 'container.image.tag')
		    OR has(res_keys, 'k8s.container.image.tag')
		    OR has(res_keys, 'k8s.deployment.labels.app_kubernetes_io_version')
		    OR has(res_keys, 'k8s.pod.labels.app_kubernetes_io_version')
		    OR has(res_keys, 'k8s.deployment.labels.version')
		    OR has(res_keys, 'helm.chart.version'))
		GROUP BY service_name, version
		HAVING version != ''
		ORDER BY service_name, first_seen_ns ASC
		SETTINGS max_execution_time = 10`
	args := append([]any{}, svcList...)
	args = append(args, from, to)
	rows, err := s.conn.Query(ctx, sql, args...)
	if err != nil {
		return events
	}
	defer rows.Close()
	type d struct {
		version string
		ns      int64
	}
	byService := map[string][]d{}
	for rows.Next() {
		var svc, ver string
		var ns int64
		if err := rows.Scan(&svc, &ver, &ns); err != nil {
			return events
		}
		byService[svc] = append(byService[svc], d{ver, ns})
	}
	if err := rows.Err(); err != nil {
		return events
	}
	lookbackNs := int64(lookback)
	for i := range events {
		list := byService[events[i].Service]
		if len(list) == 0 {
			continue
		}
		var pick *d
		for j := len(list) - 1; j >= 0; j-- {
			if list[j].ns > events[i].StartedAt {
				continue
			}
			if list[j].ns < events[i].StartedAt-lookbackNs {
				break
			}
			pick = &list[j]
			break
		}
		if pick != nil {
			events[i].RecentDeploy = &RecentDeploy{
				Version:    pick.version,
				TimeUnixNs: pick.ns,
				AgeSeconds: (events[i].StartedAt - pick.ns) / 1e9,
			}
		}
	}
	return events
}

// EnrichProblemsWithRunbooks resolves each problem's
// RunbookURL from (a) the alert rule that fired or (b) the
// service catalog metadata as a fallback. Two single-shot
// queries (alert_rules + service_metadata) joined in-memory
// against the problems slice — N+1 free regardless of the
// problem count. Safe to call on resolved problems too;
// the URL is contextual to the rule / service, not the
// status.
func (s *Store) EnrichProblemsWithRunbooks(ctx context.Context, problems []Problem) []Problem {
	if len(problems) == 0 {
		return problems
	}
	// Alert rule runbooks — keyed by rule id.
	ruleBooks := map[string]string{}
	if rules, err := s.ListAlertRules(ctx); err == nil {
		for _, r := range rules {
			if r.RunbookURL != "" {
				ruleBooks[r.ID] = r.RunbookURL
			}
		}
	}
	// Service catalog runbooks — keyed by service name.
	svcBooks := map[string]string{}
	if mds, err := s.ListServiceMetadata(ctx); err == nil {
		for svc, md := range mds {
			if md.RunbookURL != "" {
				svcBooks[svc] = md.RunbookURL
			}
		}
	}
	for i := range problems {
		if u, ok := ruleBooks[problems[i].RuleID]; ok {
			problems[i].RunbookURL = u
			continue
		}
		if u, ok := svcBooks[problems[i].Service]; ok {
			problems[i].RunbookURL = u
		}
	}
	return problems
}

// ── Alert rules ───────────────────────────────────────────────────────────────

func (s *Store) ListAlertRules(ctx context.Context) ([]AlertRule, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT id, name, service, metric, comparator, threshold, window_sec,
		       severity, enabled, built_in, runbook_url, for_sec, min_samples,
		       cooldown_sec, log_query, toUnixTimestamp64Nano(created_at)
		FROM alert_rules FINAL
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertRule
	for rows.Next() {
		var r AlertRule
		var enabled, builtIn uint8
		if err := rows.Scan(&r.ID, &r.Name, &r.Service, &r.Metric, &r.Comparator,
			&r.Threshold, &r.WindowSec, &r.Severity, &enabled, &builtIn,
			&r.RunbookURL, &r.ForSec, &r.MinSamples, &r.CooldownSec,
			&r.LogQuery, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Enabled = enabled == 1
		r.BuiltIn = builtIn == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) UpsertAlertRule(ctx context.Context, r AlertRule) error {
	enabled := uint8(0); if r.Enabled { enabled = 1 }
	builtIn := uint8(0); if r.BuiltIn { builtIn = 1 }
	// Explicit column list — alert_rules grew runbook_url in
	// a later migration. Without naming the columns, the
	// ordinal arg shape would mismatch the table layout on
	// upgraded installs.
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO alert_rules
		(id, name, service, metric, comparator, threshold, window_sec,
		 severity, enabled, built_in, runbook_url, for_sec, min_samples,
		 cooldown_sec, log_query, created_at, version)`)
	if err != nil { return err }
	if err := batch.Append(r.ID, r.Name, r.Service, r.Metric, r.Comparator,
		r.Threshold, r.WindowSec, r.Severity, enabled, builtIn,
		r.RunbookURL, r.ForSec, r.MinSamples, r.CooldownSec, r.LogQuery,
		time.Now().UTC(), uint64(time.Now().UnixNano())); err != nil {
		return err
	}
	return batch.Send()
}

// DeleteAlertRule removes the row entirely (ALTER … DELETE)
// rather than the previous soft-disable pattern — operators
// hitting Delete expect the rule to go AWAY from the list, not
// stay around as a disabled tombstone. Matches the precedent
// already established by monitors / status-page components /
// notification channels. Built-in rules are still resurrected
// on next boot from the seed list, so deleting a preset gives
// the operator a clean slate without a permanent "ghost" row.
// Disable-without-delete remains available through
// SetAlertRuleEnabled (toggled by the noisy-rules bulk path).
func (s *Store) DeleteAlertRule(ctx context.Context, id string) error {
	return s.conn.Exec(ctx, `ALTER TABLE alert_rules DELETE WHERE id = ?`, id)
}

// SetAlertRuleEnabled flips a rule's enabled flag. Used by both the
// disable (DELETE) and re-enable endpoints.
func (s *Store) SetAlertRuleEnabled(ctx context.Context, id string, enabled bool) error {
	r, err := s.GetAlertRule(ctx, id)
	if err != nil {
		return err
	}
	r.Enabled = enabled
	return s.UpsertAlertRule(ctx, *r)
}

func (s *Store) GetAlertRule(ctx context.Context, id string) (*AlertRule, error) {
	var r AlertRule
	var enabled, builtIn uint8
	err := s.conn.QueryRow(ctx, `
		SELECT id, name, service, metric, comparator, threshold, window_sec,
		       severity, enabled, built_in, runbook_url, for_sec, min_samples,
		       cooldown_sec, log_query, toUnixTimestamp64Nano(created_at)
		FROM alert_rules FINAL WHERE id = ? LIMIT 1`, id).
		Scan(&r.ID, &r.Name, &r.Service, &r.Metric, &r.Comparator, &r.Threshold,
			&r.WindowSec, &r.Severity, &enabled, &builtIn,
			&r.RunbookURL, &r.ForSec, &r.MinSamples, &r.CooldownSec,
			&r.LogQuery, &r.CreatedAt)
	if err != nil { return nil, err }
	r.Enabled = enabled == 1
	r.BuiltIn = builtIn == 1
	return &r, nil
}

// ── Problems ─────────────────────────────────────────────────────────────────

type ProblemFilter struct {
	Status   string // "open" | "resolved" | ""
	Service  string
	Severity string
	// RuleIDPrefix narrows to rules whose id starts with the given
	// string — used by the Anomalies page to surface only the
	// anomaly-detector entries (rule_id = "anomaly:…") and skip
	// the rule-driven Problems.
	RuleIDPrefix string
	Limit        int
}

// CountProblems returns the row count matching the same filter
// shape ListProblems uses, without materialising the actual rows.
// Drives the sidebar badge so the count stays truthful when there
// are >200 open problems — the badge previously fetched 200 rows
// and counted the array, capping the displayed value at 200.
// FINAL on the spans is the same as the list path so the merged
// dedup result is what counts; using a plain count() would
// double-count rows mid-merge.
func (s *Store) CountProblems(ctx context.Context, f ProblemFilter) (uint64, error) {
	var wc whereClause
	if f.Status       != "" { wc.add("status = ?", f.Status) }
	if f.Service      != "" { wc.add("service = ?", f.Service) }
	if f.Severity     != "" { wc.add("severity = ?", f.Severity) }
	if f.RuleIDPrefix != "" { wc.add("startsWith(rule_id, ?)", f.RuleIDPrefix) }
	row := s.conn.QueryRow(ctx, `
		SELECT count()
		FROM problems FINAL `+wc.sql()+`
		SETTINGS max_execution_time = 5`, wc.args...)
	var n uint64
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) ListProblems(ctx context.Context, f ProblemFilter) ([]Problem, error) {
	var wc whereClause
	if f.Status       != "" { wc.add("status = ?", f.Status) }
	if f.Service      != "" { wc.add("service = ?", f.Service) }
	if f.Severity     != "" { wc.add("severity = ?", f.Severity) }
	if f.RuleIDPrefix != "" { wc.add("startsWith(rule_id, ?)", f.RuleIDPrefix) }
	if f.Limit == 0 { f.Limit = 100 }

	// v0.5.406 — bound the query at 8s. Without this CH could run
	// the FINAL-merge + ORDER BY started_at scan to completion no
	// matter how long it took; on installs with a deep problems
	// table the read regularly tipped past the browser's 60s
	// fetch timeout, the browser sent an AbortController cancel,
	// and the upstream context cancel surfaced as repeated
	// "context deadline cancelled" lines in coremetry logs.
	// 8s is well under the browser ceiling but plenty for a
	// healthy install — and bails out clearly with a CH error
	// instead of stacking up doomed requests.
	rows, err := s.conn.Query(ctx, `
		SELECT id, rule_id, rule_name, severity, service, metric,
		       value, threshold, status, description, assignee,
		       toUnixTimestamp64Nano(started_at),
		       resolved_at,
		       ai_summary, toUnixTimestamp64Nano(ai_summary_at)
		FROM problems FINAL `+wc.sql()+`
		ORDER BY started_at DESC
		LIMIT ?
		SETTINGS max_execution_time = 8`, append(wc.args, f.Limit)...)
	if err != nil { return nil, err }
	defer rows.Close()

	var out []Problem
	for rows.Next() {
		var p Problem
		var resolvedAt *time.Time
		if err := rows.Scan(&p.ID, &p.RuleID, &p.RuleName, &p.Severity, &p.Service,
			&p.Metric, &p.Value, &p.Threshold, &p.Status, &p.Description, &p.Assignee,
			&p.StartedAt, &resolvedAt, &p.AISummary, &p.AISummaryAt); err != nil {
			return nil, err
		}
		if resolvedAt != nil {
			ns := resolvedAt.UnixNano()
			p.ResolvedAt = &ns
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// FindSimilarResolvedProblems returns up to `limit` resolved
// problems matching the given service + rule_id, ordered most
// recent first. Used by the runbook AI to anchor LLM
// suggestions in actually-resolved-before incidents rather
// than generic SRE wisdom. We require status='resolved' so the
// model sees only outcomes it can learn from — open problems
// have no time-to-resolve signal yet.
func (s *Store) FindSimilarResolvedProblems(ctx context.Context, service, ruleID string, limit int) ([]Problem, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := s.conn.Query(ctx, `
		SELECT id, rule_id, rule_name, severity, service, metric,
		       value, threshold, status, description, assignee,
		       toUnixTimestamp64Nano(started_at),
		       resolved_at,
		       ai_summary, toUnixTimestamp64Nano(ai_summary_at)
		FROM problems FINAL
		WHERE service = ? AND rule_id = ? AND status = 'resolved'
		ORDER BY started_at DESC
		LIMIT ?`, service, ruleID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Problem
	for rows.Next() {
		var p Problem
		var resolvedAt *time.Time
		if err := rows.Scan(&p.ID, &p.RuleID, &p.RuleName, &p.Severity, &p.Service,
			&p.Metric, &p.Value, &p.Threshold, &p.Status, &p.Description, &p.Assignee,
			&p.StartedAt, &resolvedAt, &p.AISummary, &p.AISummaryAt); err != nil {
			return nil, err
		}
		if resolvedAt != nil {
			ns := resolvedAt.UnixNano()
			p.ResolvedAt = &ns
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListStaleOpenProblems returns open/acknowledged problems
// whose updated_at is older than `staleCutoff`. v0.5.352 —
// operator-reported: when a service stops emitting, the
// evaluator's measure() returns no data, the resolve path
// is never taken, and the problem stays open forever. This
// list feeds the periodic stale-sweep that auto-closes them.
//
// FINAL on the read so a recently-resolved-but-not-yet-merged
// row doesn't leak into the sweep.
func (s *Store) ListStaleOpenProblems(ctx context.Context, staleCutoff time.Time) ([]Problem, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT id, rule_id, rule_name, severity, service, metric,
		       value, threshold, status, description, assignee,
		       toUnixTimestamp64Nano(started_at),
		       resolved_at,
		       ai_summary, toUnixTimestamp64Nano(ai_summary_at)
		FROM problems FINAL
		WHERE status IN ('open', 'acknowledged')
		  AND updated_at < ?
		ORDER BY updated_at ASC
		LIMIT 500
		SETTINGS max_execution_time = 10`, staleCutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Problem
	for rows.Next() {
		var p Problem
		var resolvedAt *time.Time
		if err := rows.Scan(&p.ID, &p.RuleID, &p.RuleName, &p.Severity, &p.Service,
			&p.Metric, &p.Value, &p.Threshold, &p.Status, &p.Description, &p.Assignee,
			&p.StartedAt, &resolvedAt, &p.AISummary, &p.AISummaryAt); err != nil {
			return nil, err
		}
		if resolvedAt != nil {
			ns := resolvedAt.UnixNano()
			p.ResolvedAt = &ns
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// FindOpenProblem returns the latest unresolved problem for
// (rule, service). "Unresolved" covers both `open` (default
// pageable state) and `acknowledged` (operator saw it, muted
// notifications, problem still in flight) — so the v0.5.83
// bulk-ack flow doesn't accidentally make the evaluator open
// a duplicate Problem row on the next tick.
func (s *Store) FindOpenProblem(ctx context.Context, ruleID, service string) (*Problem, error) {
	var p Problem
	var resolvedAt *time.Time
	err := s.conn.QueryRow(ctx, `
		SELECT id, rule_id, rule_name, severity, service, metric,
		       value, threshold, status, description, assignee,
		       toUnixTimestamp64Nano(started_at),
		       resolved_at,
		       ai_summary, toUnixTimestamp64Nano(ai_summary_at)
		FROM problems FINAL
		WHERE rule_id = ? AND service = ? AND status IN ('open', 'acknowledged')
		ORDER BY started_at DESC LIMIT 1`, ruleID, service).
		Scan(&p.ID, &p.RuleID, &p.RuleName, &p.Severity, &p.Service,
			&p.Metric, &p.Value, &p.Threshold, &p.Status, &p.Description, &p.Assignee,
			&p.StartedAt, &resolvedAt, &p.AISummary, &p.AISummaryAt)
	if err != nil { return nil, err }
	if resolvedAt != nil {
		ns := resolvedAt.UnixNano()
		p.ResolvedAt = &ns
	}
	return &p, nil
}

// AcknowledgeProblems flips a batch of problems to status=
// "acknowledged". Idempotent — already-resolved problems
// are silently skipped (you can't ack a resolved row,
// nothing to mute). The evaluator's auto-resolve path
// still flips them to "resolved" once the threshold
// stops firing, which is the right answer for an ack'd
// problem too.
func (s *Store) AcknowledgeProblems(ctx context.Context, ids []string, actor string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	// Fetch the rows we want to flip. ListProblems with a
	// bounded Limit is the cheap path; the ids list is
	// bounded by the bulk-action UI cap (50). Filter to
	// non-resolved here so we don't accidentally re-open a
	// resolved row by upsert.
	all, err := s.ListProblems(ctx, ProblemFilter{Limit: 1000})
	if err != nil {
		return 0, err
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	count := 0
	for i := range all {
		p := all[i]
		if !want[p.ID] || p.Status == "resolved" || p.Status == "acknowledged" {
			continue
		}
		p.Status = "acknowledged"
		if err := s.UpsertProblem(ctx, p); err != nil {
			return count, err
		}
		count++
	}
	_ = actor // future: audit log entry per acknowledge
	return count, nil
}

// OpenProblemCounts is the per-service open-problem tally
// (v0.5.274). Powers the /services health badge. Counts the
// max severity per service so the badge can flip yellow on a
// warning AND red on a critical without a second query.
type OpenProblemCounts struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}

// GetOpenProblemCountsByService returns per-service tallies of
// currently-open problems grouped by severity. Single FINAL
// scan over the problems table — bounded by `status =
// 'open'` so the row count is tiny even on a busy install
// (open problems are a triage state, not a historical archive).
func (s *Store) GetOpenProblemCountsByService(ctx context.Context) (map[string]OpenProblemCounts, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT service, severity, count() AS c
		FROM problems FINAL
		WHERE status IN ('open', 'acknowledged')
		GROUP BY service, severity
		SETTINGS max_execution_time = 5`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]OpenProblemCounts{}
	for rows.Next() {
		var service, severity string
		var c uint64
		if err := rows.Scan(&service, &severity, &c); err != nil {
			return nil, err
		}
		entry := out[service]
		switch severity {
		case "critical":
			entry.Critical += int(c)
		case "warning":
			entry.Warning += int(c)
		default:
			entry.Info += int(c)
		}
		out[service] = entry
	}
	return out, rows.Err()
}

// GetProblem fetches a single problem by id, or nil when no
// row matches. Lighter than ListProblems for the patch-one path.
func (s *Store) GetProblem(ctx context.Context, id string) (*Problem, error) {
	var p Problem
	var resolvedAt *time.Time
	err := s.conn.QueryRow(ctx, `
		SELECT id, rule_id, rule_name, severity, service, metric,
		       value, threshold, status, description, assignee,
		       toUnixTimestamp64Nano(started_at),
		       resolved_at,
		       ai_summary, toUnixTimestamp64Nano(ai_summary_at)
		FROM problems FINAL
		WHERE id = ?
		LIMIT 1`, id).
		Scan(&p.ID, &p.RuleID, &p.RuleName, &p.Severity, &p.Service,
			&p.Metric, &p.Value, &p.Threshold, &p.Status, &p.Description, &p.Assignee,
			&p.StartedAt, &resolvedAt, &p.AISummary, &p.AISummaryAt)
	if err != nil {
		return nil, err
	}
	if resolvedAt != nil {
		ns := resolvedAt.UnixNano()
		p.ResolvedAt = &ns
	}
	return &p, nil
}

// SetProblemAssignee overwrites the assignee on a single problem.
// Empty string clears the assignee — operator's explicit "take it
// back to unassigned" action. ReplacingMergeTree handles the
// dedupe; the upsert path is the same as every other write.
func (s *Store) SetProblemAssignee(ctx context.Context, id, assignee string) error {
	cur, err := s.GetProblem(ctx, id)
	if err != nil {
		return err
	}
	if cur == nil {
		return fmt.Errorf("problem %q not found", id)
	}
	cur.Assignee = assignee
	return s.UpsertProblem(ctx, *cur)
}

func (s *Store) UpsertProblem(ctx context.Context, p Problem) error {
	// Explicit column list (v0.5.254 — was: column-order INSERT).
	// The ai_summary / ai_summary_at columns are populated
	// asynchronously by the problemExplainer goroutine, so the
	// evaluator's upsert must NOT clobber them on every poll. The
	// explicit list excludes them — CH falls back to the existing
	// stored value via ReplacingMergeTree's version-merge once the
	// explainer writes a row with the summary set.
	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO problems
		(id, rule_id, rule_name, severity, service, metric, value,
		 threshold, status, description, assignee, started_at,
		 resolved_at, updated_at, version)`)
	if err != nil { return err }
	startedAt := time.Unix(0, p.StartedAt).UTC()
	var resolvedAt *time.Time
	if p.ResolvedAt != nil {
		t := time.Unix(0, *p.ResolvedAt).UTC()
		resolvedAt = &t
	}
	if err := batch.Append(p.ID, p.RuleID, p.RuleName, p.Severity, p.Service,
		p.Metric, p.Value, p.Threshold, p.Status, p.Description, p.Assignee,
		startedAt, resolvedAt, time.Now().UTC(), uint64(time.Now().UnixNano())); err != nil {
		return fmt.Errorf("append problem: %w", err)
	}
	return batch.Send()
}

// UpsertProblemAISummary writes just the AI-explain blurb without
// touching any of the evaluator-owned fields. ReplacingMergeTree
// keeps the highest-version row at read time; this insert wins
// over a same-tick evaluator upsert because the explainer always
// runs after the problem has been opened (later wall-clock = newer
// version).
//
// Reads other fields back from the existing row first so the
// resulting full row is consistent — otherwise the merge'd row
// would have value/threshold/etc collapsing to defaults on the
// "summary-only" version.
func (s *Store) UpsertProblemAISummary(ctx context.Context, problemID, summary string) error {
	row, err := s.GetProblem(ctx, problemID)
	if err != nil || row == nil {
		return err // problem disappeared (resolved + GC'd) — drop silently
	}
	row.AISummary = summary
	row.AISummaryAt = time.Now().UnixNano()

	batch, err := s.conn.PrepareBatch(ctx, `INSERT INTO problems
		(id, rule_id, rule_name, severity, service, metric, value,
		 threshold, status, description, assignee, started_at,
		 resolved_at, updated_at, version, ai_summary, ai_summary_at)`)
	if err != nil {
		return err
	}
	startedAt := time.Unix(0, row.StartedAt).UTC()
	var resolvedAt *time.Time
	if row.ResolvedAt != nil {
		t := time.Unix(0, *row.ResolvedAt).UTC()
		resolvedAt = &t
	}
	summaryAt := time.Unix(0, row.AISummaryAt).UTC()
	if err := batch.Append(row.ID, row.RuleID, row.RuleName, row.Severity, row.Service,
		row.Metric, row.Value, row.Threshold, row.Status, row.Description, row.Assignee,
		startedAt, resolvedAt, time.Now().UTC(), uint64(time.Now().UnixNano()),
		row.AISummary, summaryAt); err != nil {
		return fmt.Errorf("append problem ai-summary: %w", err)
	}
	return batch.Send()
}

// ── Service backtrace queries ────────────────────────────────────────────────

type ServiceEdgeStats struct {
	Service   string  `json:"service"`
	Calls     uint64  `json:"calls"`
	ErrorRate float64 `json:"errorRate"`
	AvgMs     float64 `json:"avgMs"`
	P99Ms     float64 `json:"p99Ms"`
}

// CallersOf returns services that called `service` (incoming dependency view).
func (s *Store) CallersOf(ctx context.Context, service string, since time.Duration) ([]ServiceEdgeStats, error) {
	cutoff := time.Now().Add(-since)
	rows, err := s.conn.Query(ctx, `
		SELECT service_name AS caller,
		       count() AS calls,
		       countIf(status_code = 'error') / count() * 100 AS error_rate,
		       avg(duration) / 1e6 AS avg_ms,
		       quantile(0.99)(duration) / 1e6 AS p99_ms
		FROM spans
		WHERE time >= ?
		  AND peer_service = ?
		  AND kind IN ('client', 'producer')
		GROUP BY caller
		ORDER BY calls DESC`, cutoff, service)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []ServiceEdgeStats
	for rows.Next() {
		var e ServiceEdgeStats
		if err := rows.Scan(&e.Service, &e.Calls, &e.ErrorRate, &e.AvgMs, &e.P99Ms); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CalleesOf returns services that `service` calls (outgoing dependency view).
func (s *Store) CalleesOf(ctx context.Context, service string, since time.Duration) ([]ServiceEdgeStats, error) {
	cutoff := time.Now().Add(-since)
	rows, err := s.conn.Query(ctx, `
		SELECT peer_service AS callee,
		       count() AS calls,
		       countIf(status_code = 'error') / count() * 100 AS error_rate,
		       avg(duration) / 1e6 AS avg_ms,
		       quantile(0.99)(duration) / 1e6 AS p99_ms
		FROM spans
		WHERE time >= ?
		  AND service_name = ?
		  AND peer_service != ''
		  AND kind IN ('client', 'producer')
		GROUP BY callee
		ORDER BY calls DESC`, cutoff, service)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []ServiceEdgeStats
	for rows.Next() {
		var e ServiceEdgeStats
		if err := rows.Scan(&e.Service, &e.Calls, &e.ErrorRate, &e.AvgMs, &e.P99Ms); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
