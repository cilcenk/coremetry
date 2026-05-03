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
	"strings"
	"time"

	"github.com/cenk/qmetry/internal/cache"
	"github.com/cenk/qmetry/internal/chstore"
)

const lockKey = "qmetry:lock:evaluator"

type Evaluator struct {
	store    *chstore.Store
	interval time.Duration
	lock     cache.Lock
}

// New takes a cache.Lock so multiple Qmetry replicas only run the
// evaluation loop once per tick. Pass cache.NewNoop()'s lock for
// single-instance deployments.
func New(store *chstore.Store, interval time.Duration, lock cache.Lock) *Evaluator {
	if interval == 0 {
		interval = time.Minute
	}
	return &Evaluator{store: store, interval: interval, lock: lock}
}

// Start runs the evaluation loop until ctx is cancelled. Built-in rules
// are seeded by every replica — that's safe (UpsertAlertRule is idempotent
// on id). Only the actual evaluation pass is leader-gated.
func (e *Evaluator) Start(ctx context.Context) {
	if err := e.seedBuiltinRules(ctx); err != nil {
		log.Printf("[evaluator] seed built-in rules: %v", err)
	}

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

// runIfLeader skips the tick when another replica holds the lock. Lease
// is 2× the tick interval so a crashed leader is recovered quickly while
// still leaving headroom for slow runs.
func (e *Evaluator) runIfLeader(ctx context.Context) {
	ok, err := e.lock.TryAcquire(ctx, lockKey, 2*e.interval)
	if err != nil {
		log.Printf("[evaluator] lock: %v — running anyway", err)
		e.evaluateAll(ctx)
		return
	}
	if !ok {
		return // another replica is running this tick
	}
	defer e.lock.Release(ctx, lockKey)
	e.evaluateAll(ctx)
}

// ── Built-in rules ───────────────────────────────────────────────────────────
//
// These ship out of the box — auto-applied to every service detected in
// the last 24 hours. Users can disable them via the UI.

var builtins = []chstore.AlertRule{
	{
		ID:         "builtin-error-rate-5pct",
		Name:       "High error rate (>5% over 5 min)",
		Service:    "", // wildcard — evaluated per service
		Metric:     "error_rate",
		Comparator: ">",
		Threshold:  5,
		WindowSec:  5 * 60,
		Severity:   "warning",
		Enabled:    true,
		BuiltIn:    true,
	},
	{
		ID:         "builtin-error-rate-15pct",
		Name:       "Critical error rate (>15% over 5 min)",
		Service:    "",
		Metric:     "error_rate",
		Comparator: ">",
		Threshold:  15,
		WindowSec:  5 * 60,
		Severity:   "critical",
		Enabled:    true,
		BuiltIn:    true,
	},
	{
		ID:         "builtin-p99-2s",
		Name:       "High P99 latency (>2s over 5 min)",
		Service:    "",
		Metric:     "p99_ms",
		Comparator: ">",
		Threshold:  2000,
		WindowSec:  5 * 60,
		Severity:   "warning",
		Enabled:    true,
		BuiltIn:    true,
	},
}

func (e *Evaluator) seedBuiltinRules(ctx context.Context) error {
	existing, err := e.store.ListAlertRules(ctx)
	if err != nil {
		return err
	}
	have := make(map[string]bool)
	for _, r := range existing {
		have[r.ID] = true
	}
	for _, r := range builtins {
		if have[r.ID] {
			continue
		}
		r.CreatedAt = time.Now().UnixNano()
		if err := e.store.UpsertAlertRule(ctx, r); err != nil {
			log.Printf("[evaluator] seed %s: %v", r.ID, err)
		}
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
}

func (e *Evaluator) evaluateOne(ctx context.Context, r chstore.AlertRule, service string) {
	if service == "" {
		return
	}
	value, err := e.measure(ctx, service, r.Metric, time.Duration(r.WindowSec)*time.Second)
	if err != nil {
		log.Printf("[evaluator] measure %s/%s: %v", r.ID, service, err)
		return
	}

	breached := compare(value, r.Comparator, r.Threshold)

	open, err := e.store.FindOpenProblem(ctx, r.ID, service)
	hasOpen := err == nil && open != nil && open.ID != ""

	switch {
	case breached && !hasOpen:
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
			StartedAt:   time.Now().UnixNano(),
		}
		if err := e.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[evaluator] open problem: %v", err)
		} else {
			log.Printf("[evaluator] PROBLEM OPENED: %s · %s = %.2f (threshold %.2f)",
				service, r.Metric, value, r.Threshold)
		}

	case breached && hasOpen:
		// Refresh the live value on the existing problem
		open.Value = value
		if err := e.store.UpsertProblem(ctx, *open); err != nil {
			log.Printf("[evaluator] refresh problem: %v", err)
		}

	case !breached && hasOpen:
		// Auto-resolve
		now := time.Now().UnixNano()
		open.Status = "resolved"
		open.ResolvedAt = &now
		open.Value = value
		if err := e.store.UpsertProblem(ctx, *open); err != nil {
			log.Printf("[evaluator] resolve problem: %v", err)
		} else {
			log.Printf("[evaluator] PROBLEM RESOLVED: %s · %s", service, r.Metric)
		}
	}
}

// measure runs the per-service metric query for the given window.
func (e *Evaluator) measure(ctx context.Context, service, metric string, window time.Duration) (float64, error) {
	cutoff := time.Now().Add(-window)
	conn := e.store.Conn()

	switch metric {
	case "error_rate":
		var v float64
		err := conn.QueryRow(ctx, `
			SELECT countIf(status_code='error') / nullIf(count(),0) * 100
			FROM spans WHERE service_name = ? AND time >= ?`,
			service, cutoff).Scan(&v)
		if err != nil {
			return 0, err
		}
		return v, nil
	case "error_count":
		var n uint64
		err := conn.QueryRow(ctx, `
			SELECT countIf(status_code='error')
			FROM spans WHERE service_name = ? AND time >= ?`,
			service, cutoff).Scan(&n)
		if err != nil {
			return 0, err
		}
		return float64(n), nil
	case "request_rate":
		var n uint64
		err := conn.QueryRow(ctx, `
			SELECT count() FROM spans WHERE service_name = ? AND time >= ?`,
			service, cutoff).Scan(&n)
		if err != nil {
			return 0, err
		}
		return float64(n) / window.Seconds(), nil
	case "p50_ms", "p95_ms", "p99_ms", "avg_ms":
		var sql string
		switch metric {
		case "avg_ms":
			sql = `SELECT avg(duration) / 1e6 FROM spans WHERE service_name=? AND time>=?`
		default:
			q := metric[1 : len(metric)-3] // "50" / "95" / "99"
			sql = fmt.Sprintf(`SELECT quantile(0.%s)(duration) / 1e6 FROM spans WHERE service_name=? AND time>=?`, q)
		}
		var v float64
		err := conn.QueryRow(ctx, sql, service, cutoff).Scan(&v)
		return v, err
	}
	return 0, fmt.Errorf("unknown metric %q", metric)
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
	if m == "error_rate" {
		return "%"
	}
	if m == "request_rate" {
		return "/s"
	}
	return ""
}

func newID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}
