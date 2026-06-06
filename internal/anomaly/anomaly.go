// Package anomaly runs a Watchdog/Lookout-style baseline check on a few
// key signals (error_rate, p99 latency, request_rate). For each (service,
// metric) it builds a 24h baseline of 5-minute buckets, then compares the
// most-recent bucket against that distribution. Significant deviations
// (|z-score| > openZ) are surfaced as Problems with rule_id="anomaly:*",
// auto-resolved when the value returns inside resolveZ.
//
// This is intentionally simple — no seasonality, no trend removal. It
// catches sudden spikes well; slow drifts are better handled by SLO burn.
package anomaly

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chstore"
	"github.com/cilcenk/coremetry/internal/notify"
)

const lockKey = "coremetry:lock:anomaly"

// Tunables — exposed as constants so it's obvious what to fiddle with.
const (
	bucketSeconds = 300            // 5-minute buckets
	historyHours  = 24             // window used to learn the baseline
	openZ         = 3.0            // |z| above this opens an anomaly
	resolveZ      = 1.5            // and below this clears it
	minSamples    = 12             // need at least this many baseline buckets
	minMagnitude  = 0.5            // ignore micro-deltas (units depend on metric)
	madScale      = 0.6745         // scales MAD to a normal-dist stdev (modified z-score)
)

// trackedMetrics is intentionally small: cardinality stays bounded
// (services × len(trackedMetrics) checks per tick).
var trackedMetrics = []string{"error_rate", "p99_ms", "request_rate"}

type Detector struct {
	store    *chstore.Store
	interval time.Duration
	lock     cache.Lock
	leader   *cache.LeaderHolder // v0.5.429 — long-lived leader designation
	notifier *notify.Notifier
}

// New takes a cache.Lock so multiple replicas don't all open the same
// anomaly, and a notifier so PROBLEM OPENED transitions email/slack out.
func New(store *chstore.Store, interval time.Duration, lock cache.Lock, notifier *notify.Notifier) *Detector {
	if interval == 0 {
		interval = 2 * time.Minute
	}
	return &Detector{
		store: store, interval: interval,
		lock:     lock,
		leader:   cache.NewLeaderHolder(lock, lockKey, cache.LeaderTTL(interval)),
		notifier: notifier,
	}
}

func (d *Detector) Start(ctx context.Context) {
	d.leader.Start(ctx)
	t := time.NewTicker(d.interval)
	defer t.Stop()
	d.runIfLeader(ctx) // run once immediately
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.runIfLeader(ctx)
		}
	}
}

func (d *Detector) runIfLeader(ctx context.Context) {
	if !d.leader.IsLeader() {
		return
	}
	d.scan(ctx)
}

func (d *Detector) scan(ctx context.Context) {
	services, err := d.store.GetServices(ctx, 24*time.Hour, time.Time{}, time.Time{})
	if err != nil {
		log.Printf("[anomaly] list services: %v", err)
		return
	}
	for _, svc := range services {
		for _, m := range trackedMetrics {
			d.checkOne(ctx, svc.Name, m)
		}
	}
}

func (d *Detector) checkOne(ctx context.Context, service, metric string) {
	buckets, err := d.fetchBuckets(ctx, service, metric)
	if err != nil {
		log.Printf("[anomaly] %s/%s fetch: %v", service, metric, err)
		return
	}
	if len(buckets) < minSamples+1 {
		return // not enough history yet
	}
	// Last bucket is the "current"; everything before it is the baseline.
	current := buckets[len(buckets)-1]
	baseline := buckets[:len(buckets)-1]

	// Modified z-score (median + MAD) instead of mean + population stdev:
	// both the mean and the population stdev are dragged by their OWN
	// outliers, so a single contaminated baseline bucket (e.g. yesterday's
	// spike) inflates the stdev and masks today's spike (z shrinks). Median
	// + MAD are outlier-robust, so the baseline isn't poisoned by its own
	// anomalies. madScale rescales MAD to a normal-dist sigma so openZ /
	// resolveZ keep their σ meaning.
	median, mad := medianMAD(baseline)
	if mad < 1e-9 {
		return // flat baseline → modified z-score undefined; skip
	}
	z := madScale * (current - median) / mad
	abs := math.Abs(z)

	ruleID := "anomaly:" + service + ":" + metric
	open, _ := d.store.FindOpenProblem(ctx, ruleID, service)
	hasOpen := open != nil && open.ID != ""

	if abs >= openZ && math.Abs(current-median) >= minMagnitude {
		severity := "warning"
		if abs >= 5 {
			severity = "critical"
		}
		desc := fmt.Sprintf("%s anomaly on %s — current %.2f%s vs baseline %.2f%s (%.1fσ).",
			displayMetric(metric), service, current, unitOf(metric), median, unitOf(metric), z)
		if hasOpen {
			open.Value = current
			open.Description = desc
			if err := d.store.UpsertProblem(ctx, *open); err != nil {
				log.Printf("[anomaly] refresh %s: %v", ruleID, err)
			}
			return
		}
		p := chstore.Problem{
			ID:          newID(),
			RuleID:      ruleID,
			RuleName:    fmt.Sprintf("Anomaly · %s", displayMetric(metric)),
			Severity:    severity,
			Service:     service,
			Metric:      metric,
			Value:       current,
			Threshold:   median,
			Status:      "open",
			Description: desc,
			StartedAt:   time.Now().UnixNano(),
		}
		if err := d.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[anomaly] open %s: %v", ruleID, err)
			return
		}
		log.Printf("[anomaly] OPENED %s · %s = %.2f%s (med=%.2f mad=%.2f z=%.1f)",
			service, metric, current, unitOf(metric), median, mad, z)
		// Auto-attach to the active incident for this service+severity
		// (same convention as evaluator-opened problems).
		if _, err := d.store.AttachProblemToIncident(ctx, p); err != nil {
			log.Printf("[anomaly] incident attach: %v", err)
		}
		if d.notifier != nil {
			go d.notifier.SendProblemAlert(context.Background(), p)
		}
	} else if abs <= resolveZ && hasOpen {
		now := time.Now().UnixNano()
		open.Status = "resolved"
		open.ResolvedAt = &now
		open.Value = current
		if err := d.store.UpsertProblem(ctx, *open); err != nil {
			log.Printf("[anomaly] resolve %s: %v", ruleID, err)
			return
		}
		log.Printf("[anomaly] RESOLVED %s · %s (back to z=%.1f)", service, metric, z)
	}
}

// fetchBuckets returns the requested metric in 5-minute buckets over the
// configured history window, ascending in time. The most recent bucket is
// the "current" sample.
//
// v0.5.296 — Scale-audit critical: previously these three queries scanned
// raw `spans` with a GROUP BY over the focal service's 24h history. At
// billion-span scale + 100s of services × 3 metrics × every detector tick,
// that's the heaviest hot path in the system. Switched to
// service_summary_5m which already carries the aggregate states we need
// (count, error_count, quantile TDigest). Each query now reads ~288 rows
// per service (24h × 12 buckets/h) and merges in-memory — sub-millisecond
// regardless of underlying span volume. LIMIT + SETTINGS still added as
// belt-and-braces in case the MV grows past our expectations.
func (d *Detector) fetchBuckets(ctx context.Context, service, metric string) ([]float64, error) {
	cutoff := time.Now().Add(-time.Duration(historyHours) * time.Hour)
	conn := d.store.Conn()

	var sql string
	switch metric {
	case "error_rate":
		sql = `
			SELECT toUnixTimestamp(time_bucket)                                              AS t,
			       countMerge(error_count_state) / nullIf(countMerge(span_count_state), 0) * 100 AS v
			FROM service_summary_5m
			WHERE service_name = ? AND time_bucket >= ?
			GROUP BY t
			ORDER BY t
			LIMIT 1000
			SETTINGS max_execution_time = 10`
	case "request_rate":
		sql = `
			SELECT toUnixTimestamp(time_bucket)            AS t,
			       countMerge(span_count_state) / 300.0    AS v
			FROM service_summary_5m
			WHERE service_name = ? AND time_bucket >= ?
			GROUP BY t
			ORDER BY t
			LIMIT 1000
			SETTINGS max_execution_time = 10`
	case "p99_ms":
		sql = `
			SELECT toUnixTimestamp(time_bucket)                              AS t,
			       quantilesMerge(0.5, 0.95, 0.99)(duration_q_state)[3] / 1e6 AS v
			FROM service_summary_5m
			WHERE service_name = ? AND time_bucket >= ?
			GROUP BY t
			ORDER BY t
			LIMIT 1000
			SETTINGS max_execution_time = 10`
	default:
		return nil, fmt.Errorf("unknown metric %q", metric)
	}

	rows, err := conn.Query(ctx, sql, service, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []float64
	for rows.Next() {
		var t uint32
		var v float64
		if err := rows.Scan(&t, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// medianMAD returns the median and the Median Absolute Deviation
// (median of |x_i - median|) of xs — the outlier-robust analogue of
// mean+stdev that the modified z-score in checkOne uses. MAD=0 when the
// sample is empty or (near-)constant, mirroring meanStdev's stdev=0 case.
func medianMAD(xs []float64) (median, mad float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	median = medianOf(xs)
	dev := make([]float64, len(xs))
	for i, v := range xs {
		dev[i] = math.Abs(v - median)
	}
	return median, medianOf(dev)
}

// medianOf returns the median of xs without mutating the caller's slice.
func medianOf(xs []float64) float64 {
	n := len(xs)
	if n == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// meanStdev — population standard deviation. Stdev=0 when n<2.
// Retained for callers other than checkOne (which now uses medianMAD).
func meanStdev(xs []float64) (mean, stdev float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	for _, v := range xs {
		mean += v
	}
	mean /= float64(len(xs))
	if len(xs) < 2 {
		return mean, 0
	}
	var ss float64
	for _, v := range xs {
		d := v - mean
		ss += d * d
	}
	return mean, math.Sqrt(ss / float64(len(xs)))
}

func displayMetric(m string) string {
	switch m {
	case "p99_ms":       return "P99 latency"
	case "error_rate":   return "Error rate"
	case "request_rate": return "Request rate"
	}
	return m
}
func unitOf(m string) string {
	if strings.HasSuffix(m, "_ms") { return "ms" }
	if m == "error_rate"           { return "%" }
	if m == "request_rate"         { return "/s" }
	return ""
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
