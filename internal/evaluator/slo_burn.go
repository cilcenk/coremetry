package evaluator

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// 2-window burn-rate alarm thresholds (Google SRE Workbook §5).
// The fast window catches a sudden burst that would exhaust the
// month's budget in a couple of days; the slow window catches
// the steady drip that takes a week to do the same. Both must
// agree before we open a problem — that's the whole point of
// the dual-window pattern: it suppresses single-bucket
// anomalies that would otherwise wake the oncall every other
// minute.
//
// Defaults are the SRE-book values for a 30-day SLO window:
//   • Fast: 1h burn > 14.4 → exhausts a 30d budget in ~2 days
//   • Slow: 6h burn >  6   → exhausts a 30d budget in ~5 days
// Critical severity needs both fast AND slow ≥ critical band.
// Warning band is half: 6 / 1 over the same windows.
type burnPolicy struct {
	severity   string
	fastWindow time.Duration
	fastRate   float64
	slowWindow time.Duration
	slowRate   float64
}

var burnPolicies = []burnPolicy{
	{severity: "critical",
		fastWindow: 1 * time.Hour,  fastRate: 14.4,
		slowWindow: 6 * time.Hour,  slowRate: 6.0},
	{severity: "warning",
		fastWindow: 6 * time.Hour,  fastRate: 6.0,
		slowWindow: 24 * time.Hour, slowRate: 3.0},
}

// evaluateSLOs runs the burn-rate alarm pass — fired by the
// main evaluator tick. Single-leader gating is already in place
// at the caller; this just walks SLOs and opens / closes
// Problems as the burn rate crosses thresholds.
func (e *Evaluator) evaluateSLOs(ctx context.Context) {
	slos, err := e.store.ListSLOs(ctx)
	if err != nil {
		log.Printf("[evaluator/slo] list: %v", err)
		return
	}
	for _, slo := range slos {
		for _, pol := range burnPolicies {
			e.evaluateSLOBurn(ctx, slo, pol)
		}
	}
}

// evaluateSLOBurn computes the fast + slow burn rates for one
// (slo, policy) pair and opens / refreshes / resolves a
// Problem accordingly. The Problem's rule_id is
// "slo:<id>:<severity>" so each (SLO × severity band) maps to
// at most one open Problem at a time.
func (e *Evaluator) evaluateSLOBurn(ctx context.Context, slo chstore.SLO, pol burnPolicy) {
	fastRate, fastTotal, err := e.store.ComputeSLOBurnRate(ctx, slo, pol.fastWindow)
	if err != nil {
		log.Printf("[evaluator/slo] %s fast burn: %v", slo.ID, err)
		return
	}
	slowRate, slowTotal, err := e.store.ComputeSLOBurnRate(ctx, slo, pol.slowWindow)
	if err != nil {
		log.Printf("[evaluator/slo] %s slow burn: %v", slo.ID, err)
		return
	}

	// Need traffic on BOTH windows to make a meaningful
	// statement. A service that emitted no spans in the last
	// hour is silent, not necessarily "healthy" — and the
	// burn-rate division by total=0 isn't sane anyway.
	const minSpans = 50
	hasTraffic := fastTotal >= minSpans && slowTotal >= minSpans
	breached := hasTraffic && fastRate >= pol.fastRate && slowRate >= pol.slowRate

	ruleID := fmt.Sprintf("slo:%s:%s", slo.ID, pol.severity)
	open, _ := e.store.FindOpenProblem(ctx, ruleID, slo.Service)
	hasOpen := open != nil && open.ID != ""

	switch {
	case breached && !hasOpen:
		p := chstore.Problem{
			ID:       newID(),
			RuleID:   ruleID,
			RuleName: fmt.Sprintf("SLO burn-rate %s — %s", pol.severity, slo.Name),
			Severity: pol.severity,
			Service:  slo.Service,
			Metric:   fmt.Sprintf("burn_rate_%dm", int(pol.fastWindow.Minutes())),
			Value:    fastRate,
			Threshold: pol.fastRate,
			Status:   "open",
			Description: fmt.Sprintf(
				"Burn rate above %s threshold for SLO %q (target %.2f%%). "+
					"Last %s: %.1fx — %s: %.1fx. At this rate the error budget "+
					"would be exhausted in days, not the SLO's %d-day window.",
				pol.severity, slo.Name, slo.Target*100,
				pol.fastWindow, fastRate,
				pol.slowWindow, slowRate,
				slo.WindowDays),
			StartedAt: time.Now().UnixNano(),
		}
		if err := e.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[evaluator/slo] open: %v", err)
			return
		}
		e.countOpened() // v0.9.550 — kalp atışı sayacı
		log.Printf("[evaluator/slo] PROBLEM OPENED: %s %s burn=%.1fx/%.1fx",
			slo.Service, pol.severity, fastRate, slowRate)
		if _, err := e.store.AttachProblemToIncident(ctx, p); err != nil {
			log.Printf("[evaluator/slo] incident attach: %v", err)
		}
		if e.notifier != nil {
			go e.notifier.SendProblemAlert(context.Background(), p)
		}

	case breached && hasOpen:
		open.Value = fastRate
		_ = e.store.UpsertProblem(ctx, *open)

	case !breached && hasOpen:
		// Resolve when burn drops back. The SRE-book
		// recommendation is to require BOTH fast and slow
		// to be under-threshold to clear; we already require
		// that for breached, so a single failed condition is
		// enough for resolve.
		// v0.9.977 — yanma hızı ihlal anındaki değerinde kalır.
		chstore.MarkResolved(open, time.Now().UnixNano())
		_ = e.store.UpsertProblem(ctx, *open)
		e.countResolved() // v0.9.550 — kalp atışı sayacı
		log.Printf("[evaluator/slo] PROBLEM RESOLVED: %s %s burn=%.1fx/%.1fx",
			slo.Service, pol.severity, fastRate, slowRate)
	}
}
