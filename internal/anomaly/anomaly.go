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
	"strings"
	"time"

	"github.com/cenk/qmetry/internal/cache"
	"github.com/cenk/qmetry/internal/chstore"
)

const lockKey = "qmetry:lock:anomaly"

// Tunables — exposed as constants so it's obvious what to fiddle with.
const (
	bucketSeconds = 300            // 5-minute buckets
	historyHours  = 24             // window used to learn the baseline
	openZ         = 3.0            // |z| above this opens an anomaly
	resolveZ      = 1.5            // and below this clears it
	minSamples    = 12             // need at least this many baseline buckets
	minMagnitude  = 0.5            // ignore micro-deltas (units depend on metric)
)

// trackedMetrics is intentionally small: cardinality stays bounded
// (services × len(trackedMetrics) checks per tick).
var trackedMetrics = []string{"error_rate", "p99_ms", "request_rate"}

type Detector struct {
	store    *chstore.Store
	interval time.Duration
	lock     cache.Lock
}

// New takes a cache.Lock so multiple replicas don't all open the same
// anomaly. Pass cache.NewNoop()'s lock for single-instance.
func New(store *chstore.Store, interval time.Duration, lock cache.Lock) *Detector {
	if interval == 0 {
		interval = 2 * time.Minute
	}
	return &Detector{store: store, interval: interval, lock: lock}
}

func (d *Detector) Start(ctx context.Context) {
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
	ok, err := d.lock.TryAcquire(ctx, lockKey, 2*d.interval)
	if err != nil {
		log.Printf("[anomaly] lock: %v — running anyway", err)
		d.scan(ctx)
		return
	}
	if !ok {
		return
	}
	defer d.lock.Release(ctx, lockKey)
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

	mean, stdev := meanStdev(baseline)
	if stdev < 1e-9 {
		return // flat baseline → z-score undefined; skip
	}
	z := (current - mean) / stdev
	abs := math.Abs(z)

	ruleID := "anomaly:" + service + ":" + metric
	open, _ := d.store.FindOpenProblem(ctx, ruleID, service)
	hasOpen := open != nil && open.ID != ""

	if abs >= openZ && math.Abs(current-mean) >= minMagnitude {
		severity := "warning"
		if abs >= 5 {
			severity = "critical"
		}
		desc := fmt.Sprintf("%s anomaly on %s — current %.2f%s vs baseline %.2f%s (%.1fσ).",
			displayMetric(metric), service, current, unitOf(metric), mean, unitOf(metric), z)
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
			Threshold:   mean,
			Status:      "open",
			Description: desc,
			StartedAt:   time.Now().UnixNano(),
		}
		if err := d.store.UpsertProblem(ctx, p); err != nil {
			log.Printf("[anomaly] open %s: %v", ruleID, err)
			return
		}
		log.Printf("[anomaly] OPENED %s · %s = %.2f%s (μ=%.2f σ=%.2f z=%.1f)",
			service, metric, current, unitOf(metric), mean, stdev, z)
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
func (d *Detector) fetchBuckets(ctx context.Context, service, metric string) ([]float64, error) {
	cutoff := time.Now().Add(-time.Duration(historyHours) * time.Hour)
	conn := d.store.Conn()

	var sql string
	switch metric {
	case "error_rate":
		sql = `
			SELECT toUnixTimestamp(toStartOfInterval(time, INTERVAL 5 MINUTE)) AS t,
			       countIf(status_code='error') / nullIf(count(),0) * 100 AS v
			FROM spans WHERE service_name = ? AND time >= ?
			GROUP BY t ORDER BY t`
	case "request_rate":
		sql = `
			SELECT toUnixTimestamp(toStartOfInterval(time, INTERVAL 5 MINUTE)) AS t,
			       count() / 300.0 AS v
			FROM spans WHERE service_name = ? AND time >= ?
			GROUP BY t ORDER BY t`
	case "p99_ms":
		sql = `
			SELECT toUnixTimestamp(toStartOfInterval(time, INTERVAL 5 MINUTE)) AS t,
			       quantile(0.99)(duration) / 1e6 AS v
			FROM spans WHERE service_name = ? AND time >= ?
			GROUP BY t ORDER BY t`
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

// meanStdev — population standard deviation. Stdev=0 when n<2.
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
