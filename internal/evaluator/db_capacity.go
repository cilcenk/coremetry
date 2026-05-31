package evaluator

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// db_capacity.go — capacity / saturation alerting off DB-receiver gauges
// (feature #5). The /databases dashboards already colour their tiles when a
// tablespace / session / connection pool nears its cap, but that tone was
// cosmetic — nothing opened a Problem, so nobody got PAGED. This pass reads
// the same receiver saturation gauges on the existing leader-locked
// evaluator tick and opens/resolves Problems against them, deduped per
// (instance, check) exactly like every other Problem.
//
// Why the evaluator (not anomaly): the evaluator OWNS auto-created
// threshold Problems — leader lock, FindOpenProblem/UpsertProblem dedup,
// the ReplacingMergeTree(version) problems table, notify fan-out,
// incident-attach. internal/anomaly/ is unsupervised span/log shape
// detection (Drain, significant_text, ratio recorder); a fixed-threshold
// gauge breach is not that. So this rides evaluateAll like evaluateSLOs.
//
// Dedup model — match the existing callers EXACTLY:
//   • rule_id  = "db-capacity:<check>"        (stable per check)
//   • service  = the receiver instance         (so FindOpenProblem(rule,svc)
//                                                returns the one open row)
//   • id       = "db-capacity:<check>:<instance>[:<subkey>]" (stable, so the
//                ReplacingMergeTree collapses re-fires onto the same row)
// One open Problem per (instance, check[, subkey]); refreshed not
// duplicated while breached; auto-resolved when back under threshold.

// Capacity thresholds. Crit at 90%, warn at 85% — the bar Datadog/Dynatrace
// use for "disk/tablespace/connection-pool nearly full". The Redis eviction
// check is a raw rate (no cap), so any positive eviction rate is the signal.
const (
	capacityCritPct = 90.0
	capacityWarnPct = 85.0
)

// capacityCheck describes one saturation check: how to read it and how to
// label the resulting Problem. read() returns the per-instance samples;
// the evaluator turns each into open/resolve. rate=true means the sample's
// Usage is a per-second rate with no Limit (>0 breaches at crit).
type capacityCheck struct {
	id    string // rule_id suffix → "db-capacity:<id>"
	label string // human label in the reason string ("tablespace", "sessions")
	dbsys string // "ORACLE" / "POSTGRES" / … for the reason string
	rate  bool   // raw-rate check (no usage/limit pair)
	// probe gates a DEFENSIVE check: the check reads only when this
	// metric is currently being published (so an install without that
	// receiver never sees spurious Problems). Empty = always run (the
	// Oracle checks — the primary banking-DB integration).
	probe string
	read  func(ctx context.Context, st *chstore.Store) ([]chstore.CapacitySample, error)
}

// capacityChecks is the full catalogue. The Oracle checks always run (the
// oracledb receiver is the primary banking-DB integration). The
// Postgres/MySQL/Redis checks are DEFENSIVE — they read only when their
// receiver is actually publishing (see evaluateDBCapacity's metricExists
// gate), so an install without that receiver never sees spurious Problems.
var capacityChecks = []capacityCheck{
	// Oracle tablespace usage — dimensioned by tablespace_name. The #1
	// reason an Oracle instance falls over.
	{id: "oracle-tablespace", label: "tablespace", dbsys: "ORACLE",
		read: func(ctx context.Context, st *chstore.Store) ([]chstore.CapacitySample, error) {
			return st.DimensionedUsageLimit(ctx,
				"oracledb.tablespace_size.usage", "oracledb.tablespace_size.limit", "tablespace_name")
		}},
	// Oracle sessions usage/limit.
	{id: "oracle-sessions", label: "sessions", dbsys: "ORACLE",
		read: func(ctx context.Context, st *chstore.Store) ([]chstore.CapacitySample, error) {
			return st.UsageLimit(ctx, "oracledb.sessions.usage", "oracledb.sessions.limit")
		}},
	// Oracle processes usage/limit.
	{id: "oracle-processes", label: "processes", dbsys: "ORACLE",
		read: func(ctx context.Context, st *chstore.Store) ([]chstore.CapacitySample, error) {
			return st.UsageLimit(ctx, "oracledb.processes.usage", "oracledb.processes.limit")
		}},

	// ── Defensive (only fire when the receiver is present) ──────────────
	// Postgres backends / max_connections.
	{id: "postgres-connections", label: "connections", dbsys: "POSTGRES", probe: "postgresql.backends",
		read: func(ctx context.Context, st *chstore.Store) ([]chstore.CapacitySample, error) {
			return st.UsageLimit(ctx, "postgresql.backends", "postgresql.connection.max")
		}},
	// MySQL connections / max_used_connections.
	{id: "mysql-connections", label: "connections", dbsys: "MYSQL", probe: "mysql.connection.count",
		read: func(ctx context.Context, st *chstore.Store) ([]chstore.CapacitySample, error) {
			return st.UsageLimit(ctx, "mysql.connection.count", "mysql.max_used_connections")
		}},
	// Redis eviction rate — raw rate, breaches when > 0 (maxmemory-policy
	// is actively evicting keys → memory pressure).
	{id: "redis-evictions", label: "key evictions", dbsys: "REDIS", rate: true, probe: "redis.keys.evicted",
		read: func(ctx context.Context, st *chstore.Store) ([]chstore.CapacitySample, error) {
			return st.RateGauge(ctx, "redis.keys.evicted")
		}},
}

// capacityDecision is the PURE threshold core (CLAUDE.md #11 — unit-tested
// in db_capacity_test.go). Given a sample's usage/limit (or a raw rate for
// rate checks) it returns whether a Problem should be OPEN and at what
// severity, plus the percentage for the reason string.
//
//   - usage/limit pair: pct = usage/limit*100; crit ≥ 90, warn ≥ 85,
//     otherwise resolve. A non-positive limit yields (false, "", 0) — the
//     read layer already drops those, this is belt-and-suspenders.
//   - rate check (limit == 0 via the rate flag): any rate > 0 is critical
//     (active eviction); pct carries the rate itself for the reason.
func capacityDecision(usage, limit float64, rate bool) (open bool, severity string, pct float64) {
	if rate {
		if usage > 0 {
			return true, "critical", usage
		}
		return false, "", usage
	}
	if limit <= 0 {
		return false, "", 0
	}
	pct = usage / limit * 100
	switch {
	case pct >= capacityCritPct:
		return true, "critical", pct
	case pct >= capacityWarnPct:
		return true, "warning", pct
	default:
		return false, "", pct
	}
}

// capacityReason builds the operator-facing reason string, e.g.
// "ORACLE tablespace SYSAUX at 92% on corebank-scan.prod" or
// "REDIS key evictions at 14.3/s on cache-01".
func capacityReason(c capacityCheck, instance, subkey string, pct float64) string {
	var b strings.Builder
	b.WriteString(c.dbsys)
	b.WriteString(" ")
	b.WriteString(c.label)
	if subkey != "" {
		b.WriteString(" ")
		b.WriteString(subkey)
	}
	if c.rate {
		fmt.Fprintf(&b, " at %.1f/s on %s", pct, instance)
	} else {
		fmt.Fprintf(&b, " at %.0f%% on %s", pct, instance)
	}
	return b.String()
}

// capacityRuleID / capacityProblemID build the stable dedup keys. rule_id
// is per-check; the Problem id additionally carries instance + subkey so a
// re-fire on the next tick collapses onto the same ReplacingMergeTree row.
func capacityRuleID(checkID string) string { return "db-capacity:" + checkID }
func capacityProblemID(checkID, instance, subkey string) string {
	id := "db-capacity:" + checkID + ":" + instance
	if subkey != "" {
		id += ":" + subkey
	}
	return id
}

// capacityService is the value stored in the Problem.service column — the
// receiver instance, optionally qualified by the dimension subkey so a
// per-tablespace Problem shows "corebank-scan.prod·SYSAUX" in the inbox and
// FindOpenProblem(rule, service) dedups to exactly one open row per
// (instance, check, subkey).
func capacityService(instance, subkey string) string {
	if subkey != "" {
		return instance + "·" + subkey
	}
	return instance
}

// evaluateDBCapacity is the new evaluateAll pass. One bounded read per
// check (each grouped by instance, so no per-instance fan-out), then
// open/refresh/resolve each sample exactly like evaluateOne. Runs on the
// leader tick — no new goroutine, no new route.
func (e *Evaluator) evaluateDBCapacity(ctx context.Context) {
	for _, c := range capacityChecks {
		// Defensive checks: skip cleanly unless the receiver is
		// actually publishing. Oracle checks have no probe → always run.
		if c.probe != "" {
			present, err := e.store.MetricExists(ctx, c.probe)
			if err != nil {
				log.Printf("[evaluator] db-capacity probe %s: %v", c.probe, err)
				continue
			}
			if !present {
				continue
			}
		}
		samples, err := c.read(ctx, e.store)
		if err != nil {
			log.Printf("[evaluator] db-capacity read %s: %v", c.id, err)
			continue
		}
		for _, s := range samples {
			e.reconcileCapacity(ctx, c, s)
		}
	}
}

// reconcileCapacity opens / refreshes / resolves the Problem for one
// sample, mirroring evaluateOne's open/refresh/resolve switch. Dedup is by
// (rule_id, service) via FindOpenProblem + a stable Problem id.
func (e *Evaluator) reconcileCapacity(ctx context.Context, c capacityCheck, s chstore.CapacitySample) {
	open, sev, pct := capacityDecision(s.Usage, s.Limit, c.rate)
	ruleID := capacityRuleID(c.id)
	service := capacityService(s.Instance, s.Subkey)

	existing, err := e.store.FindOpenProblem(ctx, ruleID, service)
	hasOpen := err == nil && existing != nil && existing.ID != ""

	switch {
	case open && !hasOpen:
		now := time.Now()
		p := chstore.Problem{
			ID:          capacityProblemID(c.id, s.Instance, s.Subkey),
			RuleID:      ruleID,
			RuleName:    "DB capacity · " + c.dbsys + " " + c.label,
			Severity:    sev,
			Service:     service,
			Metric:      "db.capacity",
			Value:       pct,
			Threshold:   capacityThreshold(c, sev),
			Status:      "open",
			Description: capacityReason(c, s.Instance, s.Subkey, pct),
			StartedAt:   now.UnixNano(),
		}
		if err := e.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[evaluator] db-capacity open %s/%s: %v", ruleID, service, err)
			return
		}
		log.Printf("[evaluator] PROBLEM OPENED (db.capacity): %s", p.Description)
		if _, err := e.store.AttachProblemToIncident(ctx, p); err != nil {
			log.Printf("[evaluator] db-capacity incident attach: %v", err)
		}
		if e.notifier != nil {
			go e.notifier.SendProblemAlert(context.Background(), p)
		}

	case open && hasOpen:
		// Refresh live value + severity (a warning can worsen into a
		// critical without re-opening). Keep the original StartedAt.
		existing.Value = pct
		existing.Severity = sev
		existing.Threshold = capacityThreshold(c, sev)
		existing.Description = capacityReason(c, s.Instance, s.Subkey, pct)
		if err := e.store.UpsertProblem(ctx, *existing); err != nil {
			log.Printf("[evaluator] db-capacity refresh %s/%s: %v", ruleID, service, err)
		}

	case !open && hasOpen:
		resolvedAt := time.Now().UnixNano()
		existing.Status = "resolved"
		existing.ResolvedAt = &resolvedAt
		existing.Value = pct
		if err := e.store.UpsertProblem(ctx, *existing); err != nil {
			log.Printf("[evaluator] db-capacity resolve %s/%s: %v", ruleID, service, err)
		} else {
			log.Printf("[evaluator] PROBLEM RESOLVED (db.capacity): %s %s on %s",
				c.dbsys, c.label, s.Instance)
		}
	}
}

// capacityThreshold reports the % threshold that the firing severity
// crossed, so the Problem row carries a meaningful threshold for the
// P1/P2/P3 breach-ratio logic. Rate checks use 0 (any rate breaches).
func capacityThreshold(c capacityCheck, severity string) float64 {
	if c.rate {
		return 0
	}
	if severity == "critical" {
		return capacityCritPct
	}
	return capacityWarnPct
}
