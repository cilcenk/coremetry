package evaluator

// Per-tick batched measurement prefetch (v0.8.352, perf Phase 2 fix P2-A).
//
// MEASURED PROBLEM (live query_log, 1h, 100-service install): evaluateOne
// issued its measure()/measureCount() queries once per (rule, service) per
// tick — ~70k queries/hour (~32k MinSamples countMerge singles, ~23k raw
// kind-scoped quantiles, ~15k per-service merge reads), max spikes 8.5s.
//
// FIX: evaluateAll collects the DISTINCT (metric, windowSec) pairs from the
// enabled span-metric rules (plus the distinct windows the MinSamples gate
// needs) and prefetches ONE map[service]value per pair via
// chstore.MeasureAllServices / MeasureCountAllServices. evaluateOne then
// evaluates every rule×service from memory. 8 builtin rules × 100 services
// × ~2 reads ≈ 1600 queries/tick collapse to ≤ 10.
//
// Only windows the 5m MV grid serves (chstore.UseSummaryMV) are batched —
// sub-5m custom windows keep the per-service raw path because the MV can't
// reconstruct them (rare by construction: builtins are 5m/10m).

import (
	"context"
	"log"
	"math"
	"sort"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// measureKey identifies one batched read: a metric over one rule window.
type measureKey struct {
	Metric    string
	WindowSec int
}

// batchMeasurer is the slice of *chstore.Store the prefetch consumes —
// an interface so the plumbing is testable with a stub (no CH).
type batchMeasurer interface {
	MeasureAllServices(ctx context.Context, metric string, window time.Duration, now time.Time) (map[string]float64, error)
	MeasureCountAllServices(ctx context.Context, window time.Duration, now time.Time) (map[string]uint64, error)
}

// tickMeasures is the per-tick prefetch cache handed to evaluateOne.
// A key present in failed/countsFailed means the batched read errored
// this tick: every rule riding it is skipped (logged once at prefetch),
// matching the old per-service behavior where a measure() error logged
// and skipped that (rule, service).
type tickMeasures struct {
	values       map[measureKey]map[string]float64
	failed       map[measureKey]bool
	counts       map[int]map[string]uint64 // keyed by windowSec
	countsFailed map[int]bool
}

// measureFor returns the prefetched per-service values for one
// (metric, window). failed=true → the batched read errored this tick.
// ok=false (and !failed) → the pair was never prefetched; the caller
// falls back to the per-service path (defensive — collectMeasureKeys
// covers every rule evaluateAll iterates).
func (t *tickMeasures) measureFor(metric string, windowSec int) (vals map[string]float64, failed, ok bool) {
	k := measureKey{Metric: metric, WindowSec: windowSec}
	if t.failed[k] {
		return nil, true, false
	}
	vals, ok = t.values[k]
	return vals, false, ok
}

// countFor is measureFor's twin for the MinSamples span counts.
func (t *tickMeasures) countFor(windowSec int) (counts map[string]uint64, failed, ok bool) {
	if t.countsFailed[windowSec] {
		return nil, true, false
	}
	counts, ok = t.counts[windowSec]
	return counts, false, ok
}

// collectMeasureKeys extracts the DISTINCT (metric, windowSec) pairs the
// enabled span-metric rules need, plus the distinct windows the
// MinSamples gate counts over. Log-query rules never measure spans;
// sub-5m windows stay on the per-service raw path; the count windows only
// include rules whose gate actually runs (MinSamples > 0 AND a
// sample-floor metric — v0.8.314 exempts absolutes). Sorted so the tick's
// query order is deterministic (stable query_log, stable tests).
func collectMeasureKeys(rules []chstore.AlertRule) (measures []measureKey, countWindows []int) {
	seen := make(map[measureKey]bool)
	seenW := make(map[int]bool)
	for _, r := range rules {
		if !r.Enabled || r.LogQuery != "" {
			continue
		}
		if !useSummaryMV(time.Duration(r.WindowSec) * time.Second) {
			continue
		}
		k := measureKey{Metric: r.Metric, WindowSec: int(r.WindowSec)}
		if !seen[k] {
			seen[k] = true
			measures = append(measures, k)
		}
		if r.MinSamples > 0 && metricNeedsSampleFloor(r.Metric) && !seenW[int(r.WindowSec)] {
			seenW[int(r.WindowSec)] = true
			countWindows = append(countWindows, int(r.WindowSec))
		}
	}
	sort.Slice(measures, func(i, j int) bool {
		if measures[i].Metric != measures[j].Metric {
			return measures[i].Metric < measures[j].Metric
		}
		return measures[i].WindowSec < measures[j].WindowSec
	})
	sort.Ints(countWindows)
	return measures, countWindows
}

// prefetchMeasures runs the batched reads ONCE per tick. A failed pair is
// logged and marked so evaluateOne skips the rules riding it this tick —
// one bad metric/window never crashes or stalls the whole tick (same
// blast radius as the old per-service error handling).
func prefetchMeasures(ctx context.Context, m batchMeasurer, rules []chstore.AlertRule, now time.Time) *tickMeasures {
	keys, countWindows := collectMeasureKeys(rules)
	t := &tickMeasures{
		values:       make(map[measureKey]map[string]float64, len(keys)),
		failed:       make(map[measureKey]bool),
		counts:       make(map[int]map[string]uint64, len(countWindows)),
		countsFailed: make(map[int]bool),
	}
	for _, k := range keys {
		vals, err := m.MeasureAllServices(ctx, k.Metric, time.Duration(k.WindowSec)*time.Second, now)
		if err != nil {
			log.Printf("[evaluator] prefetch %s/%ds: %v — skipping rules on this metric/window this tick", k.Metric, k.WindowSec, err)
			t.failed[k] = true
			continue
		}
		t.values[k] = vals
	}
	for _, w := range countWindows {
		counts, err := m.MeasureCountAllServices(ctx, time.Duration(w)*time.Second, now)
		if err != nil {
			log.Printf("[evaluator] prefetch counts/%ds: %v — skipping MinSamples-gated rules on this window this tick", w, err)
			t.countsFailed[w] = true
			continue
		}
		t.counts[w] = counts
	}
	return t
}

// absentMeasure translates "service missing from the batched map" (zero
// spans in the window) into EXACTLY what the per-service measure() query
// produced over an empty result, per metric class:
//
//   - error_count / request_rate / transport count → countMerge/count()
//     over nothing = 0 → evaluate with 0 (this is how traffic-drop
//     `request_rate <` rules fire on a silent service — must keep working).
//   - error_rate / avg_ms (MV) / transport error_rate → x/nullIf(0,0) =
//     NULL → the old Scan into float64 errored → the rule was SKIPPED for
//     that service. evaluate=false reproduces the skip (minus the log spam).
//   - percentiles + transport avg (raw) → quantile()/avg() over an empty
//     set = NaN → evaluated (compare() on NaN is always false, so open
//     problems hit the resolve branch). Faithful, not pretty.
func absentMeasure(metric string) (value float64, evaluate bool) {
	switch metric {
	case "error_count", "request_rate":
		return 0, true
	case "error_rate", "avg_ms":
		return 0, false
	case "p50_ms", "p95_ms", "p99_ms":
		return math.NaN(), true
	}
	if _, _, ok := transportFilter(metric); ok {
		switch transportOp(metric) {
		case "count":
			return 0, true
		case "error_rate":
			return 0, false
		case "p50_ms", "p95_ms", "p99_ms", "avg_ms":
			return math.NaN(), true
		}
	}
	// Unknown metric — unreachable via the prefetch (the plan resolver
	// already failed the pair), kept as a skip for safety.
	return 0, false
}
