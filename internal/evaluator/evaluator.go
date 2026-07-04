// Package evaluator runs alert rules on a fixed interval, opens problems
// when their condition is breached, and resolves problems whose breach is
// no longer present. Built-in rules cover the typical APM signals
// (error rate, P99 latency, request-rate drops).
package evaluator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/logstore"
	"github.com/cilcenk/coremetry/internal/notify"
)

const lockKey = "coremetry:lock:evaluator"

type Evaluator struct {
	store    *chstore.Store
	logs     logstore.Store // v0.5.242 — drives the saved-search log alert path
	interval time.Duration
	lock     cache.Lock
	leader   *cache.LeaderHolder // v0.5.429
	notifier *notify.Notifier

	// breachSince tracks when a (rule, service) tuple first
	// breached its threshold. Used by the v0.5.127 sustained-
	// breach gate (AlertRule.ForSec): a problem only opens once
	// the breach has persisted ForSec seconds. Reset to zero
	// time the moment the breach clears. Map mutex'd via
	// breachMu — the evaluator runs single-leader per tick but
	// the seed call + tests can touch it concurrently.
	breachSince map[breachKey]time.Time
	// lastResolved stamps when a problem on (rule, service) last
	// auto-resolved. Used by the v0.5.129 cooldown gate
	// (AlertRule.CooldownSec): re-opens within the cooldown
	// window are suppressed to absorb threshold-jitter flap.
	lastResolved map[breachKey]time.Time
	breachMu     sync.Mutex
}

type breachKey struct {
	RuleID  string
	Service string
}

// New takes a cache.Lock so multiple Coremetry replicas only run the
// evaluation loop once per tick, and a notifier so PROBLEM OPENED
// transitions fan out to email/slack/etc.
func New(store *chstore.Store, interval time.Duration, lock cache.Lock, notifier *notify.Notifier) *Evaluator {
	if interval == 0 {
		interval = time.Minute
	}
	return &Evaluator{
		store:        store,
		interval:     interval,
		lock:         lock,
		leader:       cache.NewLeaderHolder(lock, lockKey, cache.LeaderTTL(interval)),
		notifier:     notifier,
		breachSince:  make(map[breachKey]time.Time),
		lastResolved: make(map[breachKey]time.Time),
	}
}

// SetLogs wires the log backend so the saved-search alert path
// (rules with LogQuery != "") can count matches via logstore.
// Called from main() once buildLogStore has resolved the
// backend — keeps the New() constructor lean + avoids
// reordering boot-time wiring around the evaluator.
func (e *Evaluator) SetLogs(logs logstore.Store) { e.logs = logs }

// Start runs the evaluation loop until ctx is cancelled. Built-in rules
// are seeded by every replica — that's safe (UpsertAlertRule is idempotent
// on id). Only the actual evaluation pass is leader-gated.
func (e *Evaluator) Start(ctx context.Context) {
	if err := e.seedBuiltinRules(ctx); err != nil {
		log.Printf("[evaluator] seed built-in rules: %v", err)
	}

	e.leader.Start(ctx)
	t := time.NewTicker(e.interval)
	defer t.Stop()

	e.runIfLeader(ctx) // run once immediately

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.runIfLeader(ctx)
		}
	}
}

// runIfLeader skips the tick when another replica holds leadership.
// v0.5.429 — long-lived LeaderHolder; one pod owns the worker for
// its lifetime + refreshes the lease in the background.
func (e *Evaluator) runIfLeader(ctx context.Context) {
	if !e.leader.IsLeader() {
		return
	}
	e.evaluateAll(ctx)
}

// ── Built-in rules ───────────────────────────────────────────────────────────
//
// Curated for banking-realistic baseline workloads — Coremetry's primary
// target. Pre-v0.4.87 we shipped 15 rules with sub-second thresholds
// (DB P99 >500ms, HTTP P99 >1s, etc.) which fired constantly on a real
// banking stack where Oracle calls + multi-hop transaction services
// routinely run 800ms-2s in steady state. That alarm fatigue erodes
// trust in every other alert.
//
// Two-tier default set (v0.8.262 — operator request: "built-in alert
// rule'larında hardening yap, gerçek alertler oluşsun, default
// gelsin"):
//
//   CRITICAL floor (5-min windows) — "really wrong", pages someone:
//     error rate >15% · HTTP P99 >5s · DB P99 >5s · MQ consume P99 >2m
//   WARNING tier (10-min sustained) — real degradation forming, catch
//     it before the floor trips:
//     error rate >5% · HTTP P99 >3s · DB P99 >2.5s · MQ consume P99 >30s
//
// Every rule now carries the full anti-noise kit the engine already
// had but the defaults never used: MinSamples (v0.5.128 — no verdicts
// from a 3-span window), ForSec (v0.5.126 — breach must SUSTAIN, one
// spiky bucket doesn't open a problem), CooldownSec (v0.5.129 — no
// flap-reopen at the threshold boundary).
//
// Threshold history: pre-v0.4.87 shipped 15 sub-second rules that
// fired constantly on banking steady state (Oracle + multi-hop chains
// run 800ms–2s P99 normally) — alarm fatigue erased trust. v0.5.67
// slimmed near-duplicate transport error-rate rules into the service-
// wide error_rate. The warning tier deliberately sits ABOVE those
// documented steady-state ceilings (3s HTTP / 2.5s DB) with longer
// windows + sustain gates, so it flags true degradation, not morning
// warm-up.
var builtins = []chstore.AlertRule{
	// ── Critical floor ──────────────────────────────────────────
	// Service-wide error rate. 15% is the "something is clearly
	// failing" floor — normal failed-card-transactions noise
	// stays well below. Subsumes HTTP-5xx, DB-error, and
	// RPC-error sub-rates (all flow into this metric).
	{ID: "builtin-error-rate-15pct", Name: "Critical error rate (>15% over 5 min)",
		Metric: "error_rate", Comparator: ">", Threshold: 15, WindowSec: 5 * 60,
		Severity: "critical", Enabled: true, BuiltIn: true,
		MinSamples: 50, ForSec: 120, CooldownSec: 300},

	// HTTP latency. P99 >5s in a banking call chain is SLO-
	// violating territory regardless of which service.
	{ID: "builtin-http-p99-5s", Name: "HTTP P99 latency >5s (5 min)",
		Metric: "http_p99_ms", Comparator: ">", Threshold: 5000, WindowSec: 5 * 60,
		Severity: "critical", Enabled: true, BuiltIn: true,
		MinSamples: 50, ForSec: 120, CooldownSec: 300},

	// Database latency. 5s is when the DB is actually broken
	// (lock storm, undersized, network blip) — not warm-up.
	{ID: "builtin-db-p99-5s", Name: "DB P99 latency >5s (5 min)",
		Metric: "db_p99_ms", Comparator: ">", Threshold: 5000, WindowSec: 5 * 60,
		Severity: "critical", Enabled: true, BuiltIn: true,
		MinSamples: 30, ForSec: 120, CooldownSec: 300},

	// Message-queue consumer lag. 2 minutes processing P99 on a
	// Kafka / IBM MQ consumer is real back-pressure. Producer
	// errors fold into error_rate so we don't double-page.
	{ID: "builtin-mq-consume-p99-2m", Name: "MQ consume P99 >2 min — consumer lag (5 min)",
		Metric: "mq_consume_p99_ms", Comparator: ">", Threshold: 120000, WindowSec: 5 * 60,
		Severity: "critical", Enabled: true, BuiltIn: true,
		MinSamples: 20, ForSec: 120, CooldownSec: 300},

	// ── Warning tier (sustained degradation) ────────────────────
	// 5% sustained error rate for 10+ minutes is genuinely
	// abnormal against a 1-2% banking steady state; 3-min
	// sustain + 100-sample floor keep single bad buckets and
	// low-traffic services quiet.
	{ID: "builtin-warn-error-rate-5pct", Name: "Elevated error rate (>5% sustained 10 min)",
		Metric: "error_rate", Comparator: ">", Threshold: 5, WindowSec: 10 * 60,
		Severity: "warning", Enabled: true, BuiltIn: true,
		MinSamples: 100, ForSec: 180, CooldownSec: 600},

	// 3s HTTP P99 sits above the documented 800ms–2s multi-hop
	// steady-state ceiling — sustained 10 min means real
	// degradation, with headroom before the 5s critical floor.
	{ID: "builtin-warn-http-p99-3s", Name: "HTTP P99 latency >3s (sustained 10 min)",
		Metric: "http_p99_ms", Comparator: ">", Threshold: 3000, WindowSec: 10 * 60,
		Severity: "warning", Enabled: true, BuiltIn: true,
		MinSamples: 100, ForSec: 180, CooldownSec: 600},

	// 2.5s DB P99 sustained — above the 500ms–1s warm steady
	// state banking datastores run, below the 5s "actually
	// broken" floor. Catches lock contention building up.
	{ID: "builtin-warn-db-p99-2500ms", Name: "DB P99 latency >2.5s (sustained 10 min)",
		Metric: "db_p99_ms", Comparator: ">", Threshold: 2500, WindowSec: 10 * 60,
		Severity: "warning", Enabled: true, BuiltIn: true,
		MinSamples: 60, ForSec: 180, CooldownSec: 600},

	// 30s consume P99 sustained = back-pressure forming well
	// before the 2-minute critical lag floor.
	{ID: "builtin-warn-mq-consume-p99-30s", Name: "MQ consume P99 >30s — back-pressure (sustained 10 min)",
		Metric: "mq_consume_p99_ms", Comparator: ">", Threshold: 30000, WindowSec: 10 * 60,
		Severity: "warning", Enabled: true, BuiltIn: true,
		MinSamples: 20, ForSec: 180, CooldownSec: 600},
}

// deprecatedBuiltinIDs lists IDs that USED TO be in the
// builtins slice. On boot we silently auto-disable any of
// them still enabled in the operator's alert_rules table so
// they stop generating noise on upgrade. Operator can
// re-enable from the UI if they actually want the rule;
// nothing is deleted (preserves any custom edits like
// runbookURL on the rule itself).
var deprecatedBuiltinIDs = []string{
	"builtin-http-5xx-5pct",  // subsumed by error_rate
	"builtin-db-error-5pct",  // subsumed by error_rate
	"builtin-rpc-error-10pct", // subsumed by error_rate
}

func (e *Evaluator) seedBuiltinRules(ctx context.Context) error {
	existing, err := e.store.ListAlertRules(ctx)
	if err != nil {
		return err
	}
	have := make(map[string]bool)
	byID := make(map[string]chstore.AlertRule, len(existing))
	for _, r := range existing {
		have[r.ID] = true
		byID[r.ID] = r
	}
	// Seed any new builtins that aren't in the table yet.
	for _, r := range builtins {
		if have[r.ID] {
			continue
		}
		r.CreatedAt = time.Now().UnixNano()
		if err := e.store.UpsertAlertRule(ctx, r); err != nil {
			log.Printf("[evaluator] seed %s: %v", r.ID, err)
		}
	}
	// Backfill the anti-noise kit onto builtin rows seeded by older
	// releases (v0.8.262): pre-hardening builtins carried
	// MinSamples/ForSec/CooldownSec = 0. Only zero fields are
	// filled — an operator's explicit non-zero setting is never
	// touched, and thresholds/windows/names are left alone entirely
	// (they may be customised).
	for _, def := range builtins {
		cur, ok := byID[def.ID]
		if !ok || !cur.BuiltIn {
			continue
		}
		next := cur
		if next.MinSamples == 0 {
			next.MinSamples = def.MinSamples
		}
		if next.ForSec == 0 {
			next.ForSec = def.ForSec
		}
		if next.CooldownSec == 0 {
			next.CooldownSec = def.CooldownSec
		}
		if next == cur {
			continue
		}
		if err := e.store.UpsertAlertRule(ctx, next); err != nil {
			log.Printf("[evaluator] harden builtin %s: %v", def.ID, err)
			continue
		}
		log.Printf("[evaluator] hardened builtin %s (minSamples=%d forSec=%d cooldownSec=%d)",
			def.ID, next.MinSamples, next.ForSec, next.CooldownSec)
	}
	// Auto-disable rules that USED TO be builtins but were
	// removed in a later release. Skips rules the operator
	// already disabled (idempotent) and never deletes, so any
	// runbookURL / threshold customisation survives the upgrade.
	for _, id := range deprecatedBuiltinIDs {
		r, ok := byID[id]
		if !ok || !r.Enabled {
			continue
		}
		if err := e.store.SetAlertRuleEnabled(ctx, id, false); err != nil {
			log.Printf("[evaluator] deprecate %s: %v", id, err)
			continue
		}
		log.Printf("[evaluator] disabled deprecated builtin %s (subsumed by error_rate)", id)
	}
	return nil
}

// ── Evaluation loop ──────────────────────────────────────────────────────────

func (e *Evaluator) evaluateAll(ctx context.Context) {
	rules, err := e.store.ListAlertRules(ctx)
	if err != nil {
		log.Printf("[evaluator] list rules: %v", err)
		return
	}

	// Cache the recent service set so wildcard rules know what to evaluate.
	services, err := e.store.GetServices(ctx, 24*time.Hour, time.Time{}, time.Time{})
	if err != nil {
		log.Printf("[evaluator] services: %v", err)
		return
	}

	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		targets := []string{r.Service}
		if r.Service == "" {
			targets = make([]string, 0, len(services))
			for _, s := range services {
				targets = append(targets, s.Name)
			}
		}
		for _, svc := range targets {
			e.evaluateOne(ctx, r, svc)
		}
	}

	// SLO burn-rate alarms — independent of the user-defined
	// alert rules above. Each configured SLO gets two passes
	// (warning + critical) using the 2-window burn-rate
	// pattern from the Google SRE Workbook. Fires Problems on
	// the same pipeline as everything else, so the existing
	// notify / incident-attach / SSE wiring all picks up
	// burn-rate breaches without additional plumbing.
	e.evaluateSLOs(ctx)

	// DB capacity / saturation alarms (feature #5) — page off the
	// DB-receiver gauges (Oracle tablespace / sessions / processes,
	// defensively Postgres / MySQL connections + Redis evictions) that
	// the /databases dashboards only coloured cosmetically. Opens /
	// resolves Problems deduped per (instance, check) on this same
	// leader-locked tick, riding the existing notify / incident-attach
	// pipeline like every other Problem.
	e.evaluateDBCapacity(ctx)

	// Escalation sweep — bump severity on problems that have
	// been open past the configured threshold without
	// acknowledgement. Refires SendProblemAlert with the new
	// severity so the next-tier channels (typically
	// "critical-only" oncall pages) light up. Run after the
	// main evaluate pass so a problem freshly opened this
	// tick doesn't get checked twice.
	e.escalateStaleProblems(ctx)

	// v0.5.352 — silent-source sweep. Operator-reported:
	// problems for services that have stopped emitting stay
	// open forever because the eval path's measure() returns
	// no data and the resolve branch never runs. Sweep here:
	// any open/acknowledged problem whose updated_at is older
	// than 3× the evaluator interval (no recent refresh →
	// source went silent) gets auto-resolved with a marker
	// reason. 3× is the same trade-off the evaluator's
	// escalation sweep uses: tolerant of a single missed tick
	// (network glitch, leader transition), strict enough that
	// a truly decommissioned service closes within ~minutes.
	e.sweepStaleProblems(ctx)

	// v0.7.33 — cascade incident resolution. Operator-reported: Problems
	// auto-resolve but Incidents stayed open forever (CH ground truth: 214
	// problems resolved / 0 open, yet 57 incidents open / 0 resolved). An
	// incident is a container for its attached problems; once they've ALL
	// cleared the incident is over, so auto-resolve it with resolved_at = the
	// last problem's clear time (the started→resolved interval then reflects the
	// real impact window). Operators still resolve manually for a postmortem
	// note; this just stops the inbox filling with stale containers.
	e.cascadeResolveIncidents(ctx)

	// Anomaly auto-promotion — convert strong, sustained
	// anomaly events into first-class Problems so the
	// existing alert pipeline picks them up. Threshold-driven
	// so noise stays in /anomalies; only the patterns the
	// detector keeps re-firing with a real ratio become
	// pageable.
	e.promoteStrongAnomalies(ctx)
}

// incidentCascadeDecision decides whether an OPEN incident should auto-resolve
// because every problem attached to it has cleared, and at what end time. It
// resolves only when the incident has at least one attached problem and NONE
// remain unresolved. endedAt is the latest problem-clear time so the resolved
// incident's started→ended interval reflects the real impact window; it falls
// back to `now` when no clear timestamp is recorded. Pure for unit testing.
func incidentCascadeDecision(problemCount, unresolved int, maxResolvedAt, now int64) (resolve bool, endedAt int64) {
	if problemCount == 0 || unresolved > 0 {
		return false, 0
	}
	if maxResolvedAt > 0 {
		return true, maxResolvedAt
	}
	return true, now
}

// cascadeResolveIncidents auto-resolves every open incident whose attached
// problems have ALL cleared (the operator-reported gap — see the evaluateAll
// call site). One bounded rollup query, then a per-settled-incident upsert +
// timeline event.
func (e *Evaluator) cascadeResolveIncidents(ctx context.Context) {
	rollups, err := e.store.OpenIncidentRollups(ctx)
	if err != nil {
		log.Printf("[evaluator] incident cascade rollup: %v", err)
		return
	}
	now := time.Now().UnixNano()
	for _, ro := range rollups {
		resolve, endedAt := incidentCascadeDecision(ro.ProblemCount, ro.Unresolved, ro.MaxResolvedAt, now)
		if !resolve {
			continue
		}
		inc, err := e.store.GetIncident(ctx, ro.ID)
		if err != nil || inc == nil || inc.Status == "resolved" {
			continue
		}
		inc.Status = "resolved"
		inc.ResolvedAt = &endedAt
		if err := e.store.UpsertIncident(ctx, inc); err != nil {
			log.Printf("[evaluator] incident cascade upsert %s: %v", ro.ID, err)
			continue
		}
		_ = e.store.AppendIncidentEvent(ctx, chstore.IncidentEvent{
			IncidentID: inc.ID,
			Time:       endedAt,
			Kind:       "resolved",
			Actor:      "system",
			Body:       "auto-resolved: all attached problems cleared",
		})
		log.Printf("[evaluator] INCIDENT AUTO-RESOLVED: %s (%d problems, all cleared)", inc.ID, ro.ProblemCount)
	}
}

func (e *Evaluator) evaluateOne(ctx context.Context, r chstore.AlertRule, service string) {
	// Saved-search log alerts (v0.5.242) bypass the per-service
	// span-metric path entirely. The KQL itself carries any
	// service / pod / level filter the operator wants; the
	// evaluator just counts matches in the window and compares
	// to the threshold. We special-case BEFORE the service
	// guard because log_query rules use service="".
	if r.LogQuery != "" {
		e.evaluateLogQuery(ctx, r)
		return
	}
	if service == "" {
		return
	}
	// Sample floor (v0.5.128). For sample-dependent metrics
	// (error_rate / percentiles / avg_ms) a single bad request
	// in a low-traffic window pushes the value to scary levels
	// without any real signal — skip the eval entirely below
	// MinSamples. request_rate / error_count are absolute and
	// inherently sample-aware, so the gate doesn't apply.
	if r.MinSamples > 0 && metricNeedsSampleFloor(r.Metric) {
		count, err := e.measureCount(ctx, service, time.Duration(r.WindowSec)*time.Second)
		if err != nil {
			log.Printf("[evaluator] measure-count %s/%s: %v", r.ID, service, err)
			return
		}
		if count < uint64(r.MinSamples) {
			// Also clear any stamped breach so a sustain gate
			// doesn't carry over once the service warms up.
			key := breachKey{RuleID: r.ID, Service: service}
			e.breachMu.Lock()
			delete(e.breachSince, key)
			e.breachMu.Unlock()
			return
		}
	}

	value, err := e.measure(ctx, service, r.Metric, time.Duration(r.WindowSec)*time.Second)
	if err != nil {
		log.Printf("[evaluator] measure %s/%s: %v", r.ID, service, err)
		return
	}

	breached := compare(value, r.Comparator, r.Threshold)

	// Sustained-breach gate (v0.5.127): when r.ForSec > 0 we
	// stamp the first-breach time and only open a problem after
	// the breach has persisted that long. Clearing the breach
	// resets the stamp so a re-breach restarts the clock — same
	// semantics as Prometheus' `for:` directive. Open problems
	// don't pass through here; the breach has already been
	// promoted, refresh continues via the existing path.
	key := breachKey{RuleID: r.ID, Service: service}
	now := time.Now()
	if breached && r.ForSec > 0 {
		e.breachMu.Lock()
		first, seen := e.breachSince[key]
		if !seen {
			e.breachSince[key] = now
			e.breachMu.Unlock()
			return // first sighting — wait for sustain
		}
		e.breachMu.Unlock()
		if now.Sub(first) < time.Duration(r.ForSec)*time.Second {
			return // still inside the sustain window
		}
		// Past the sustain — fall through to open.
	}
	if !breached {
		e.breachMu.Lock()
		delete(e.breachSince, key)
		e.breachMu.Unlock()
	}

	open, err := e.store.FindOpenProblem(ctx, r.ID, service)
	hasOpen := err == nil && open != nil && open.ID != ""

	switch {
	case breached && !hasOpen:
		// Cooldown gate (v0.5.129): if this (rule, service)
		// just auto-resolved, hold off re-opening until the
		// cooldown window passes. Threshold-jitter near the
		// boundary stops producing OPEN/RESOLVED churn.
		if r.CooldownSec > 0 {
			e.breachMu.Lock()
			rt, seen := e.lastResolved[key]
			e.breachMu.Unlock()
			if seen && now.Sub(rt) < time.Duration(r.CooldownSec)*time.Second {
				return
			}
		}
		// Open a new problem
		p := chstore.Problem{
			ID:          newID(),
			RuleID:      r.ID,
			RuleName:    r.Name,
			Severity:    r.Severity,
			Service:     service,
			Metric:      r.Metric,
			Value:       value,
			Threshold:   r.Threshold,
			Status:      "open",
			Description: describeProblem(r, service, value),
			Assignee:    e.defaultAssignee(ctx, service),
			StartedAt:   time.Now().UnixNano(),
		}
		if err := e.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[evaluator] open problem: %v", err)
		} else {
			log.Printf("[evaluator] PROBLEM OPENED: %s · %s = %.2f (threshold %.2f)",
				service, r.Metric, value, r.Threshold)
			// Auto-group into an Incident — same-service same-severity
			// problems within 30min get folded under one declared
			// incident so the oncall has a single place to drive
			// response from. Best-effort; failure here doesn't block
			// alerting.
			if _, err := e.store.AttachProblemToIncident(ctx, p); err != nil {
				log.Printf("[evaluator] incident attach: %v", err)
			}
			// Fan out to user channels (email/slack/etc). Fire-and-forget
			// so a flaky SMTP doesn't block the eval loop. When a
			// maintenance window is active for this (service,
			// severity) at firing time, skip the notification —
			// the Problem itself still opens + auto-resolves so
			// the post-window timeline review is intact, only
			// the live channel spam is suppressed.
			// Notifier internally consults the maintenance-windows
			// table and skips fan-out when an active window matches
			// (service, severity). Problem itself still opens +
			// resolves normally — only the live channel spam is
			// suppressed.
			if e.notifier != nil {
				go e.notifier.SendProblemAlert(context.Background(), p)
			}
		}

	case breached && hasOpen:
		// Refresh the live value on the existing problem
		open.Value = value
		if err := e.store.UpsertProblem(ctx, *open); err != nil {
			log.Printf("[evaluator] refresh problem: %v", err)
		}

	case !breached && hasOpen:
		// Auto-resolve
		resolvedAt := time.Now().UnixNano()
		open.Status = "resolved"
		open.ResolvedAt = &resolvedAt
		open.Value = value
		if err := e.store.UpsertProblem(ctx, *open); err != nil {
			log.Printf("[evaluator] resolve problem: %v", err)
		} else {
			// Stamp the resolution so v0.5.129's CooldownSec
			// gate can suppress immediate re-opens.
			e.breachMu.Lock()
			e.lastResolved[key] = time.Now()
			e.breachMu.Unlock()
			log.Printf("[evaluator] PROBLEM RESOLVED: %s · %s", service, r.Metric)
		}
	}
}

// evaluateLogQuery handles a saved-search alert (v0.5.242). The
// rule's LogQuery is a KQL clause; we count matches in the
// window via the logstore and compare to Threshold. Open /
// resolve mirrors the metric path so the existing
// notification + incident-attach + cooldown machinery still
// applies. Service is left empty on the resulting Problem —
// the KQL itself can scope to one service via service.name:"X".
func (e *Evaluator) evaluateLogQuery(ctx context.Context, r chstore.AlertRule) {
	if e.logs == nil {
		log.Printf("[evaluator] log_query %s: logs backend not wired", r.ID)
		return
	}
	window := time.Duration(r.WindowSec) * time.Second
	if window == 0 {
		window = time.Minute
	}
	// v0.8.3 — per-evaluation deadline so a slow ES Search for a
	// log_query alert rule can't hang the evaluator goroutine against
	// the process-lifetime ctx (no-op on CH; bounds the ES hang).
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	now := time.Now()
	page, err := e.logs.Search(ctx, logstore.Filter{
		Search: r.LogQuery,
		From:   now.Add(-window),
		To:     now,
		Limit:  1, // hits the cap but we only need .Total
	})
	if err != nil {
		log.Printf("[evaluator] log_query measure %s: %v", r.ID, err)
		return
	}
	value := float64(page.Total)
	breached := compare(value, r.Comparator, r.Threshold)

	// Sustained-breach + cooldown machinery same as the metric
	// path — key on (rule, "" service) so the maps stay
	// segregated from per-service rules.
	key := breachKey{RuleID: r.ID, Service: ""}
	if breached && r.ForSec > 0 {
		e.breachMu.Lock()
		first, seen := e.breachSince[key]
		if !seen {
			e.breachSince[key] = now
			e.breachMu.Unlock()
			return
		}
		e.breachMu.Unlock()
		if now.Sub(first) < time.Duration(r.ForSec)*time.Second {
			return
		}
	}
	if !breached {
		e.breachMu.Lock()
		delete(e.breachSince, key)
		e.breachMu.Unlock()
	}

	open, err := e.store.FindOpenProblem(ctx, r.ID, "")
	hasOpen := err == nil && open != nil && open.ID != ""

	switch {
	case breached && !hasOpen:
		if r.CooldownSec > 0 {
			e.breachMu.Lock()
			rt, seen := e.lastResolved[key]
			e.breachMu.Unlock()
			if seen && now.Sub(rt) < time.Duration(r.CooldownSec)*time.Second {
				return
			}
		}
		p := chstore.Problem{
			ID:          newID(),
			RuleID:      r.ID,
			RuleName:    r.Name,
			Severity:    r.Severity,
			Service:     "",
			Metric:      "log_query",
			Value:       value,
			Threshold:   r.Threshold,
			Status:      "open",
			Description: fmt.Sprintf("log_query matched %d in last %s (threshold %s %.0f) — query: %s",
				page.Total, window, r.Comparator, r.Threshold, r.LogQuery),
			StartedAt:   now.UnixNano(),
		}
		if err := e.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[evaluator] open log-query problem: %v", err)
			return
		}
		log.Printf("[evaluator] PROBLEM OPENED (log_query): %s = %d (threshold %s %.0f)",
			r.Name, page.Total, r.Comparator, r.Threshold)
		if _, err := e.store.AttachProblemToIncident(ctx, p); err != nil {
			log.Printf("[evaluator] log-query incident attach: %v", err)
		}
		if e.notifier != nil {
			go e.notifier.SendProblemAlert(context.Background(), p)
		}
	case breached && hasOpen:
		open.Value = value
		if err := e.store.UpsertProblem(ctx, *open); err != nil {
			log.Printf("[evaluator] refresh log-query problem: %v", err)
		}
	case !breached && hasOpen:
		resolvedAt := now.UnixNano()
		open.Status = "resolved"
		open.ResolvedAt = &resolvedAt
		open.Value = value
		if err := e.store.UpsertProblem(ctx, *open); err != nil {
			log.Printf("[evaluator] resolve log-query problem: %v", err)
		} else {
			e.breachMu.Lock()
			e.lastResolved[key] = now
			e.breachMu.Unlock()
			log.Printf("[evaluator] PROBLEM RESOLVED (log_query): %s", r.Name)
		}
	}
}

// Escalation thresholds — how long a problem can stay open at
// each severity before the sweep bumps it up a tier. Chosen
// for the bank-oncall flow:
//
//   • info → warning after 15 min — gives a heads-up that
//     should have been triaged but wasn't.
//   • warning → critical after 30 min — paging-grade signal
//     that no human has acknowledged in half an hour.
//   • critical stays critical (no further tier).
//
// Measured from started_at, so a freshly-opened critical
// stays at critical naturally (it has nowhere higher to go),
// and an info opened 45 min ago lands at critical on the
// first sweep after restart.
const (
	escalateInfoToWarningAfter     = 15 * time.Minute
	escalateWarningToCriticalAfter = 30 * time.Minute
)

// escalateStaleProblems walks every open problem and bumps
// severity when it's lingered past the threshold. Per-tier
// guards mean each problem escalates at most twice end-to-end
// (info → warning → critical); the comparison is against the
// original started_at so an "info opened 45 min ago" goes
// straight to critical on the first sweep.
//
// Refires SendProblemAlert after writing the new severity so
// the next-tier channels (typically severity-gated to
// "critical only" for the on-call pager) light up.
// Acknowledged / resolved problems are skipped — only "open"
// status enters the sweep.
// sweepStaleProblems auto-resolves open/acknowledged problems
// whose source signal has gone silent. Detection: updated_at
// older than 3× the evaluator interval — the evaluator
// upserts the row every tick when the metric still produces
// a value (even at 0, that's a no-error refresh), so a
// frozen updated_at means the eval path bailed (MinSamples
// gate, measure() returned no data for a decommissioned
// service, the source service stopped emitting, etc.).
//
// 3× threshold tolerates one missed tick (leader transition,
// network blip) without false-resolves. Default eval
// interval = 1 min, so cutoff = ~3 min.
//
// v0.5.352 — operator-reported: problem for "svc-1" stayed
// open from 04.05.2026 17:00 onward even though no traces
// came in from that service. The eval path's measure()
// returned no data, so neither the breached nor the
// !breached branch ran, and the problem was orphaned.
func (e *Evaluator) sweepStaleProblems(ctx context.Context) {
	cutoff := time.Now().Add(-3 * e.interval)
	stale, err := e.store.ListStaleOpenProblems(ctx, cutoff)
	if err != nil {
		log.Printf("[evaluator] stale sweep: list: %v", err)
		return
	}
	resolved := 0
	for i := range stale {
		p := stale[i]
		resolvedAt := time.Now().UnixNano()
		p.Status = "resolved"
		p.ResolvedAt = &resolvedAt
		// Mark the resolution reason inline so an operator
		// auditing /problems sees why this row closed without
		// a corresponding threshold-crossing event.
		p.Description = appendStaleSuffix(p.Description)
		if err := e.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[evaluator] stale sweep: resolve %s: %v", p.ID, err)
			continue
		}
		// Clear the cooldown stamp so the next time the source
		// emits and breaches again, a fresh problem can open.
		e.breachMu.Lock()
		delete(e.lastResolved, breachKey{RuleID: p.RuleID, Service: p.Service})
		e.breachMu.Unlock()
		resolved++
		log.Printf("[evaluator] PROBLEM AUTO-RESOLVED (source silent): %s · %s", p.Service, p.Metric)
	}
	if resolved > 0 {
		log.Printf("[evaluator] stale sweep: resolved %d problem(s) with silent sources", resolved)
	}
}

// appendStaleSuffix tags the resolution reason onto the
// problem's description so /problems history makes the
// silent-source close obvious. Idempotent — if the suffix is
// already present (the row got resolved-then-reopened-then-
// silently-closed once before) we don't double-tag.
func appendStaleSuffix(desc string) string {
	const suffix = " · auto-resolved: source silent"
	if strings.Contains(desc, "source silent") {
		return desc
	}
	return desc + suffix
}

func (e *Evaluator) escalateStaleProblems(ctx context.Context) {
	problems, err := e.store.ListProblems(ctx, chstore.ProblemFilter{
		Status: "open",
		Limit:  500,
	})
	if err != nil {
		log.Printf("[evaluator] escalation sweep: list problems: %v", err)
		return
	}
	now := time.Now()
	for i := range problems {
		p := problems[i]
		openFor := now.Sub(time.Unix(0, p.StartedAt))
		next := nextSeverity(p.Severity, openFor)
		if next == "" || next == p.Severity {
			continue
		}
		old := p.Severity
		p.Severity = next
		if err := e.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[evaluator] escalate %s/%s: %v", p.Service, p.RuleID, err)
			continue
		}
		log.Printf("[evaluator] ESCALATED %s · %s · open %s · %s → %s",
			p.Service, p.RuleName, openFor.Round(time.Minute), old, next)
		// Refire so severity-gated channels (e.g. an oncall
		// pager that only subscribes to critical) get the
		// notification. SendProblemAlert publishes the SSE
		// event too, so live operator UIs pick up the
		// severity bump immediately.
		if e.notifier != nil {
			go e.notifier.SendProblemAlert(context.Background(), p)
		}
	}
}

// Promoted problems get rule_id = "anomaly-auto:<fingerprint>"
// so the same anomaly re-promoted on a later tick lands on
// the same Problem row (no duplicate noise). When the
// anomaly clears (last_seen ages out of the "active" window)
// the row stays "open" — operators ack it the same way as
// any other Problem.
//
// Promotion thresholds were hard-coded until v0.5.71; they
// now come from chstore.AnomalyPromotionConfig saved under
// system_settings. Defaults match the legacy constants so
// installs that never visit the settings page keep the
// v0.5.59 behaviour.
const promoteAnomalyRuleID = "anomaly-auto:"

// promoteStrongAnomalies converts strong, sustained
// AnomalyEvents into first-class Problems so the existing
// notify / incident-attach / SSE wiring picks them up. The
// thresholds above gate which events qualify. Idempotent —
// a re-promotion on a later sweep hits the same problem id
// and just bumps its last-seen via UpsertProblem.
//
// Severity ladder:
//   • peakRatio ≥ 10  → warning  (the standard auto-promote tier)
//   • peakRatio ≥ 20  → critical (genuine "wake someone")
//
// The escalation sweep above can still bump these higher
// over time if the operator doesn't ack.
func (e *Evaluator) promoteStrongAnomalies(ctx context.Context) {
	// Pull config from the store. Soft-fails to defaults
	// internally so a CH blip never accidentally disables
	// promotion in a long-running evaluator.
	cfg := e.store.GetAnomalyPromotion(ctx)
	if !cfg.Enabled {
		return
	}
	minSustained := time.Duration(cfg.MinSustainedSec) * time.Second

	events, err := e.store.ListAnomalyEvents(ctx, chstore.ListAnomalyEventsFilter{
		// Last hour is enough — the detector re-emits every
		// tick, so anything that should be a Problem will be
		// in this window. Wider lookback would just re-touch
		// already-promoted rows.
		SinceNs: time.Now().Add(-1 * time.Hour).UnixNano(),
		Limit:   500,
	})
	if err != nil {
		log.Printf("[evaluator] anomaly promotion: list events: %v", err)
		return
	}
	now := time.Now()
	for _, ev := range events {
		if ev.Status != "active" {
			continue
		}
		if ev.PeakRatio < cfg.MinPeakRatio {
			continue
		}
		if ev.CurrentCount < cfg.MinCount {
			continue
		}
		if now.Sub(time.Unix(0, ev.StartedAt)) < minSustained {
			continue
		}
		sev := "warning"
		if ev.PeakRatio >= 20 {
			sev = "critical"
		}
		ruleID := promoteAnomalyRuleID + ev.ID
		// Re-use the existing open Problem row when one is
		// in flight for the same fingerprint — keeps the
		// notify channel from refiring on every sweep.
		isNew := false
		open, _ := e.store.FindOpenProblem(ctx, ruleID, ev.Service)
		if open == nil {
			isNew = true
		}
		desc := truncate(
			"Auto-promoted from anomaly: "+ev.Kind+" / "+ev.Pattern+
				" (peak ratio "+formatFloat(ev.PeakRatio, 1)+
				"×, count "+formatUint(ev.CurrentCount)+")",
			480,
		)
		var startedAt int64
		var id string
		if open != nil {
			id = open.ID
			startedAt = open.StartedAt
		} else {
			id = ruleID + ":" + ev.Service
			startedAt = ev.StartedAt
		}
		p := chstore.Problem{
			ID:          id,
			RuleID:      ruleID,
			RuleName:    "Anomaly · " + ev.Pattern,
			Severity:    sev,
			Service:     ev.Service,
			Metric:      "anomaly_ratio",
			Value:       ev.PeakRatio,
			Threshold:   cfg.MinPeakRatio,
			Status:      "open",
			Description: desc,
			StartedAt:   startedAt,
		}
		if err := e.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[evaluator] anomaly promote %s/%s: %v",
				ev.Service, ev.ID, err)
			continue
		}
		if isNew {
			log.Printf("[evaluator] AUTO-PROMOTED anomaly %s · %s · ratio %.1fx → %s",
				ev.Service, ev.Pattern, ev.PeakRatio, sev)
			if e.notifier != nil {
				go e.notifier.SendProblemAlert(context.Background(), p)
			}
		}
	}
}

// formatFloat / formatUint — tiny helpers used by promotion
// description so we don't pull fmt.Sprintf into the hot path
// (the rest of the file works in strconv-style for the same
// reason). One alloc each, no formatting locale weirdness.
func formatFloat(v float64, prec int) string {
	return strconv.FormatFloat(v, 'f', prec, 64)
}
func formatUint(v uint64) string { return strconv.FormatUint(v, 10) }

// truncate caps a description at n bytes so a runaway pattern
// label (e.g. a 30KB log message captured as the anomaly
// pattern) doesn't bloat the problems table row.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// nextSeverity returns the new severity for a problem that's
// been open for `openFor`, or "" / unchanged when no
// escalation applies. The check is one-directional and
// idempotent: a problem already at critical never returns
// a higher tier; one that hasn't crossed any threshold yet
// returns "".
func nextSeverity(cur string, openFor time.Duration) string {
	switch strings.ToLower(cur) {
	case "info":
		if openFor >= escalateWarningToCriticalAfter {
			return "critical"
		}
		if openFor >= escalateInfoToWarningAfter {
			return "warning"
		}
	case "warning":
		if openFor >= escalateWarningToCriticalAfter {
			return "critical"
		}
	}
	return ""
}

// measure runs the per-service metric query for the given window.
// metricNeedsSampleFloor reports whether a metric's value is
// distorted by low sample counts (1 of 1 = 100% error rate)
// versus an absolute count whose value carries the signal
// directly. v0.5.128's MinSamples gate only applies to the
// former.
func metricNeedsSampleFloor(metric string) bool {
	if strings.HasSuffix(metric, "_rate") || strings.HasSuffix(metric, "_ms") {
		return true
	}
	switch metric {
	case "error_rate", "avg_ms", "p50_ms", "p95_ms", "p99_ms":
		return true
	}
	return false
}

// measureCount returns the total span count for a service over
// the window. Used by the MinSamples gate; one extra round-trip
// per rule per tick (skipped when MinSamples == 0).
// defaultAssignee resolves the team that should see a freshly-
// opened Problem before anyone manually claims it. Looks at the
// service's catalog metadata: owner_team wins (the team that
// builds + ships the service), sre_team is the fallback for
// services with no listed owner. Empty when no catalog row
// exists yet — the operator can still claim manually.
func (e *Evaluator) defaultAssignee(ctx context.Context, service string) string {
	if service == "" {
		return ""
	}
	md, err := e.store.GetServiceMetadata(ctx, service)
	if err != nil || md == nil {
		return ""
	}
	if md.OwnerTeam != "" {
		return md.OwnerTeam
	}
	return md.SRETeam
}

// useSummaryMV decides whether a measure() / measureCount() call
// for the given alert window can ride the 5-minute MV instead of
// scanning raw spans. v0.6.12 — every basic RED metric the
// evaluator measures (count, error_rate, error_count, request_rate,
// avg/p50/p95/p99 latency) has an aggregating-state counterpart in
// service_summary_5m, so windows >= 5min are served from the MV at
// constant cost. Sub-5min windows fall back to raw spans (with
// bounds) because the MV's 5-min granularity can't reconstruct
// them faithfully — single eval tick over a smaller window is fine
// to spans-scan since the WHERE is already (service_name, time)
// indexed-bound.
func useSummaryMV(window time.Duration) bool {
	return window >= 5*time.Minute
}

func (e *Evaluator) measureCount(ctx context.Context, service string, window time.Duration) (uint64, error) {
	cutoff := time.Now().Add(-window)
	var n uint64
	if useSummaryMV(window) {
		err := e.store.Conn().QueryRow(ctx, `
			SELECT countMerge(span_count_state) FROM service_summary_5m
			WHERE service_name = ? AND time_bucket >= ?
			SETTINGS max_execution_time = 10`,
			service, cutoff).Scan(&n)
		return n, err
	}
	err := e.store.Conn().QueryRow(ctx, `
		SELECT count() FROM spans WHERE service_name = ? AND time >= ?
		SETTINGS max_execution_time = 10`,
		service, cutoff).Scan(&n)
	return n, err
}

func (e *Evaluator) measure(ctx context.Context, service, metric string, window time.Duration) (float64, error) {
	cutoff := time.Now().Add(-window)
	conn := e.store.Conn()
	mv := useSummaryMV(window)

	switch metric {
	case "error_rate":
		var v float64
		var sql string
		if mv {
			sql = `SELECT toFloat64(countMerge(error_count_state)) /
			              nullIf(toFloat64(countMerge(span_count_state)),0) * 100
			       FROM service_summary_5m
			       WHERE service_name=? AND time_bucket>=?
			       SETTINGS max_execution_time = 10`
		} else {
			sql = `SELECT countIf(status_code='error') / nullIf(count(),0) * 100
			       FROM spans WHERE service_name=? AND time>=?
			       SETTINGS max_execution_time = 10`
		}
		err := conn.QueryRow(ctx, sql, service, cutoff).Scan(&v)
		if err != nil {
			return 0, err
		}
		return v, nil
	case "error_count":
		var n uint64
		var sql string
		if mv {
			sql = `SELECT countMerge(error_count_state) FROM service_summary_5m
			       WHERE service_name=? AND time_bucket>=?
			       SETTINGS max_execution_time = 10`
		} else {
			sql = `SELECT countIf(status_code='error') FROM spans
			       WHERE service_name=? AND time>=?
			       SETTINGS max_execution_time = 10`
		}
		err := conn.QueryRow(ctx, sql, service, cutoff).Scan(&n)
		if err != nil {
			return 0, err
		}
		return float64(n), nil
	case "request_rate":
		var n uint64
		var sql string
		if mv {
			sql = `SELECT countMerge(span_count_state) FROM service_summary_5m
			       WHERE service_name=? AND time_bucket>=?
			       SETTINGS max_execution_time = 10`
		} else {
			sql = `SELECT count() FROM spans WHERE service_name=? AND time>=?
			       SETTINGS max_execution_time = 10`
		}
		err := conn.QueryRow(ctx, sql, service, cutoff).Scan(&n)
		if err != nil {
			return 0, err
		}
		return float64(n) / window.Seconds(), nil
	case "p50_ms", "p95_ms", "p99_ms", "avg_ms":
		var sql string
		if mv {
			// quantilesMerge returns the full tuple; arrayElement
			// picks the requested index. Indices match the MV's
			// quantilesState(0.5, 0.95, 0.99) ordering: 1=p50,
			// 2=p95, 3=p99. duration_q_state is the merge target.
			switch metric {
			case "avg_ms":
				sql = `SELECT toFloat64(sumMerge(duration_sum_state)) /
				              nullIf(toFloat64(countMerge(span_count_state)),0) / 1e6
				       FROM service_summary_5m
				       WHERE service_name=? AND time_bucket>=?
				       SETTINGS max_execution_time = 10`
			case "p50_ms":
				sql = `SELECT arrayElement(quantilesTDigestMerge(0.5,0.95,0.99)(duration_q_state), 1) / 1e6
				       FROM service_summary_5m
				       WHERE service_name=? AND time_bucket>=?
				       SETTINGS max_execution_time = 10`
			case "p95_ms":
				sql = `SELECT arrayElement(quantilesTDigestMerge(0.5,0.95,0.99)(duration_q_state), 2) / 1e6
				       FROM service_summary_5m
				       WHERE service_name=? AND time_bucket>=?
				       SETTINGS max_execution_time = 10`
			case "p99_ms":
				sql = `SELECT arrayElement(quantilesTDigestMerge(0.5,0.95,0.99)(duration_q_state), 3) / 1e6
				       FROM service_summary_5m
				       WHERE service_name=? AND time_bucket>=?
				       SETTINGS max_execution_time = 10`
			}
		} else {
			switch metric {
			case "avg_ms":
				sql = `SELECT avg(duration) / 1e6 FROM spans
				       WHERE service_name=? AND time>=?
				       SETTINGS max_execution_time = 10`
			default:
				q := metric[1 : len(metric)-3] // "50" / "95" / "99"
				sql = fmt.Sprintf(`SELECT quantile(0.%s)(duration) / 1e6
				                   FROM spans WHERE service_name=? AND time>=?
				                   SETTINGS max_execution_time = 10`, q)
			}
		}
		var v float64
		err := conn.QueryRow(ctx, sql, service, cutoff).Scan(&v)
		return v, err
	}

	// Transport-scoped metrics — narrow each query by an indexed
	// LowCardinality column (db_system / rpc_system / kind /
	// http_method) so the (service_name, time) primary key still
	// drives the scan and only relevant rows are aggregated. These
	// power the production-grade DB / RPC / HTTP / MQ alert
	// categories.
	//
	// For *_rate metrics the WHERE narrows the *denominator* (the
	// span population we're measuring against, e.g. all HTTP server
	// spans), and the *_rate's numerator condition counts within
	// that population (e.g. http_status >= 500). Conflating the two
	// — narrowing WHERE by 5xx — would produce 100% trivially.
	if where, numerator, ok := transportFilter(metric); ok {
		op := transportOp(metric)
		// v0.6.12 — every transport-scoped query stays on raw
		// spans because service_summary_5m has no breakdown by
		// http_method / db_system / rpc_system / kind. The
		// indexed (service_name, time) prefix still drives the
		// scan, but we now cap wall-clock at 10s so a CH lock
		// fight can't pin the evaluator tick.
		const settings = ` SETTINGS max_execution_time = 10`
		var sql string
		switch op {
		case "error_rate":
			sql = `SELECT countIf(` + numerator + `) * 100.0 / nullIf(count(),0)
				FROM spans WHERE service_name=? AND time>=? AND ` + where + settings
		case "p50_ms", "p95_ms", "p99_ms", "avg_ms":
			if op == "avg_ms" {
				sql = `SELECT avg(duration) / 1e6
					FROM spans WHERE service_name=? AND time>=? AND ` + where + settings
			} else {
				q := op[1 : len(op)-3]
				sql = fmt.Sprintf(`SELECT quantile(0.%s)(duration) / 1e6
					FROM spans WHERE service_name=? AND time>=? AND `, q) + where + settings
			}
		case "count":
			sql = `SELECT count() FROM spans WHERE service_name=? AND time>=? AND ` + where + settings
		default:
			return 0, fmt.Errorf("unknown transport op %q in %q", op, metric)
		}
		var v float64
		err := conn.QueryRow(ctx, sql, service, cutoff).Scan(&v)
		return v, err
	}
	return 0, fmt.Errorf("unknown metric %q", metric)
}

// transportFilter returns:
//   - where:     denominator population predicate (WHERE narrows
//     the span set we're measuring against)
//   - numerator: numerator predicate for *_rate metrics (counts the
//     "bad" rows within the population). Unused for latency/count
//     metrics.
//
// All fragments are literal SQL — no user input — so they're safe
// to concatenate.
func transportFilter(metric string) (where, numerator string, ok bool) {
	switch {
	case strings.HasPrefix(metric, "http_5xx_"):
		return "kind='server' AND http_method != ''",
			"http_status >= 500", true
	case strings.HasPrefix(metric, "http_4xx_"):
		return "kind='server' AND http_method != ''",
			"http_status >= 400 AND http_status < 500", true
	case strings.HasPrefix(metric, "http_"):
		return "kind='server' AND http_method != ''",
			"status_code='error'", true
	case strings.HasPrefix(metric, "db_"):
		return "db_system != ''",
			"status_code='error'", true
	case strings.HasPrefix(metric, "rpc_"):
		return "rpc_system != ''",
			"status_code='error'", true
	case strings.HasPrefix(metric, "mq_publish_"):
		return "kind='producer'",
			"status_code='error'", true
	case strings.HasPrefix(metric, "mq_consume_"):
		return "kind='consumer'",
			"status_code='error'", true
	}
	return "", "", false
}

// transportOp pulls the aggregate suffix off a transport metric:
//
//	http_5xx_rate          → error_rate (5xx-narrowed by transportFilter)
//	http_p99_ms            → p99_ms
//	db_error_rate          → error_rate
//	mq_publish_error_rate  → error_rate
func transportOp(metric string) string {
	switch {
	case strings.HasSuffix(metric, "_rate"):
		return "error_rate"
	case strings.HasSuffix(metric, "_p99_ms"):
		return "p99_ms"
	case strings.HasSuffix(metric, "_p95_ms"):
		return "p95_ms"
	case strings.HasSuffix(metric, "_p50_ms"):
		return "p50_ms"
	case strings.HasSuffix(metric, "_avg_ms"):
		return "avg_ms"
	case strings.HasSuffix(metric, "_count"):
		return "count"
	}
	return ""
}

func compare(value float64, op string, threshold float64) bool {
	switch op {
	case ">":  return value >  threshold
	case ">=": return value >= threshold
	case "<":  return value <  threshold
	case "<=": return value <= threshold
	}
	return false
}

func describeProblem(r chstore.AlertRule, service string, value float64) string {
	unit := metricUnit(r.Metric)
	return fmt.Sprintf("%s on %s — observed %.2f%s, threshold %s %.2f%s over %ds window.",
		r.Name, service, value, unit, r.Comparator, r.Threshold, unit, r.WindowSec)
}

func metricUnit(m string) string {
	if strings.HasSuffix(m, "_ms") {
		return "ms"
	}
	if strings.HasSuffix(m, "_rate") {
		// http_5xx_rate, db_error_rate, rpc_error_rate, … all
		// percent — request_rate is the one exception.
		if m == "request_rate" {
			return "/s"
		}
		return "%"
	}
	return ""
}

func newID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}
