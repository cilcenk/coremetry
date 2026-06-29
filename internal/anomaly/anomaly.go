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
	// v0.8.220 — operator-reported too many anomalies + transient spikes that
	// don't clear. Davis-style asymmetry: HARD to open (3.5σ AND 3 sustained
	// 5-min buckets = 15 min, so an instant blip never opens), FAST to resolve
	// (the most-recent bucket back inside the band clears it — see the detector;
	// a single-bucket dip can't re-open thanks to the 3-bucket dwell, so it
	// doesn't flap).
	openZ         = 3.5            // |z| above this opens an anomaly
	resolveZ      = 1.5            // and below this clears it
	criticalZ     = 5.0            // |z| above this escalates warning → critical
	dwellBuckets  = 3              // consecutive buckets that must all fire to open (anti-flap)
	minSamples    = 12             // need at least this many baseline buckets
	madScale      = 0.6745         // scales MAD to a normal-dist stdev (modified z-score)
	magnitudeEps  = 1e-9           // denominator guard for the relative-change floor

	seasonalBaseline   = true // baseline from same-time-of-day history, not a flat 24h window
	seasonalDays       = 7    // days of same-slot history for the seasonal baseline
	seasonalMinSamples = 4    // min same-slot samples before the seasonal baseline is trusted
)

// trackedMetrics is intentionally small: cardinality stays bounded
// (services × len(trackedMetrics) checks per tick).
var trackedMetrics = []string{"error_rate", "p99_ms", "request_rate"}

// metricPolicy makes detection metric-aware: which DIRECTION of deviation
// matters, and the relative-change floor that filters statistically-
// significant-but-tiny moves. A symmetric |z| would open a "critical"
// anomaly on a 3σ p99 DROP (good news), and one absolute magnitude floor
// can't fit error_rate(%), p99(ms) and rps at once.
type metricPolicy struct {
	direction string  // "up" | "down" | "both" — which side opens an anomaly
	floorPct  float64 // relative-change floor: |current-median|/max(|median|,eps)
}

var metricPolicies = map[string]metricPolicy{
	"error_rate":   {direction: "up", floorPct: 0.10},   // only rising errors matter
	"p99_ms":       {direction: "up", floorPct: 0.10},   // only rising latency matters
	"request_rate": {direction: "both", floorPct: 0.15}, // drop AND spike both matter
}

// policyFor returns a metric's policy, defaulting to a symmetric 10% floor.
func policyFor(metric string) metricPolicy {
	if p, ok := metricPolicies[metric]; ok {
		return p
	}
	return metricPolicy{direction: "both", floorPct: 0.10}
}

// anomalyDecision is the pure open/severity/direction verdict for one sample.
type anomalyDecision struct {
	open      bool
	severity  string // "warning" | "critical" (meaningful when open)
	direction string // "spiked" | "dropped"
}

// decideAnomaly applies the metric's directional gate + relative-change floor
// + direction-aware severity to a single (z, current, median) sample. Pure +
// store-free so the policy is unit-testable without a Detector. A 3σ p99 DROP
// returns open=false ("up" only); a request_rate DROP escalates to critical
// (traffic loss is worse than a spike).
func decideAnomaly(metric string, z, current, median float64) anomalyDecision {
	pol := policyFor(metric)
	dirOpen := false
	switch pol.direction {
	case "up":
		dirOpen = z >= openZ
	case "down":
		dirOpen = z <= -openZ
	default: // "both"
		dirOpen = math.Abs(z) >= openZ
	}
	relChange := math.Abs(current-median) / math.Max(math.Abs(median), magnitudeEps)
	if !dirOpen || relChange < pol.floorPct {
		return anomalyDecision{}
	}
	dropped := z < 0
	dir := "spiked"
	if dropped {
		dir = "dropped"
	}
	severity := "warning"
	if math.Abs(z) >= criticalZ {
		severity = "critical"
	}
	if metric == "request_rate" && dropped {
		severity = "critical" // traffic loss is more serious than a spike
	}
	return anomalyDecision{open: true, severity: severity, direction: dir}
}

// resolvedFor reports whether the metric has returned inside the resolve band
// for its policy direction (the directional counterpart of |z| <= resolveZ).
func resolvedFor(metric string, z float64) bool {
	switch policyFor(metric).direction {
	case "up":
		return z <= resolveZ
	case "down":
		return z >= -resolveZ
	default:
		return math.Abs(z) <= resolveZ
	}
}

// evalWindow scores every bucket in the dwell window against the baseline's
// median/MAD. It reports allOpen (every bucket fires AND in the SAME
// direction — so a flapping spike→drop doesn't open), allResolved (every
// bucket back inside the resolve band), and cur (the most-recent bucket's
// verdict, which drives the reported severity/direction). Pure + store-free
// so the dwell/M-of-N policy is unit-testable, and stateless so a leader
// handoff loses no in-memory streak counter.
func evalWindow(metric string, median, mad float64, window []float64) (allOpen, allResolved bool, cur anomalyDecision) {
	if len(window) == 0 {
		return false, false, anomalyDecision{}
	}
	allOpen, allResolved = true, true
	dir := ""
	for i, v := range window {
		zv := madScale * (v - median) / mad
		dv := decideAnomaly(metric, zv, v, median)
		cur = dv
		if i == 0 {
			dir = dv.direction
		}
		if !dv.open || dv.direction != dir {
			allOpen = false
		}
		if !resolvedFor(metric, zv) {
			allResolved = false
		}
	}
	return allOpen, allResolved, cur
}

// anomalyAction is the pure open/resolve/none decision for one (service, metric)
// check: open/refresh when ALL dwell buckets fire (allOpen), else RESOLVE an
// already-open problem the moment the MOST-RECENT bucket is back inside the band
// (resolvedFor(metric, latestZ)). Extracted from checkOne so the v0.8.220
// asymmetry (hard open / fast resolve) is unit-tested — a silent revert to the
// old all-dwell-buckets `allResolved` resolve condition changes this function
// and fails TestAnomalyAction. Returns "open" | "resolve" | "none".
func anomalyAction(hasOpen, allOpen bool, metric string, latestZ float64) string {
	switch {
	case allOpen:
		return "open"
	case hasOpen && resolvedFor(metric, latestZ):
		return "resolve"
	default:
		return "none"
	}
}

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
	if len(buckets) < minSamples+dwellBuckets {
		return // not enough history + a full dwell window yet
	}
	// Dwell / M-of-N anti-flap: judge the LAST dwellBuckets, not just the most
	// recent one, so a single transient bucket can't flap a problem open/
	// closed around the z threshold. The window is derived entirely from the
	// fetched series → stateless, so a leader handoff loses no streak counter.
	split := len(buckets) - dwellBuckets
	window := buckets[split:]
	current := buckets[len(buckets)-1]

	// Baseline = same-time-of-day history (seasonal) when available, else the
	// 24h-consecutive window. Seasonal kills the diurnal false positives — the
	// morning ramp looks normal against the same slot on prior days — and
	// surfaces real off-peak dips. Best-effort: a seasonal read error or too
	// few same-slot samples falls back to the consecutive window.
	consecutive := buckets[:split]
	var seasonal []float64
	if seasonalBaseline {
		if s, err := d.fetchSeasonalBaseline(ctx, service, metric, time.Now()); err == nil {
			seasonal = s
		}
	}
	baseline := chooseBaseline(seasonal, consecutive)

	// Modified z-score (median + MAD) instead of mean + population stdev:
	// both are dragged by their OWN outliers, so a single contaminated
	// baseline bucket inflates the stdev and masks today's spike. Median +
	// MAD are outlier-robust; madScale rescales MAD to a normal-dist sigma so
	// openZ / resolveZ keep their σ meaning.
	median, mad := medianMAD(baseline)
	if mad < 1e-9 {
		return // flat baseline → modified z-score undefined; skip
	}
	z := madScale * (current - median) / mad

	ruleID := "anomaly:" + service + ":" + metric
	open, _ := d.store.FindOpenProblem(ctx, ruleID, service)
	hasOpen := open != nil && open.ID != ""

	// Open only when ALL dwell buckets fire (same direction); resolve as soon as
	// the most-recent bucket is back inside the band (v0.8.220 fast-resolve). cur
	// is the most-recent verdict; the pure anomalyAction decides open/resolve/none.
	allOpen, _, cur := evalWindow(metric, median, mad, window)
	action := anomalyAction(hasOpen, allOpen, metric, z)
	if action == "open" {
		severity := cur.severity
		desc := fmt.Sprintf("%s %s on %s — current %.2f%s vs baseline %.2f%s (%.1fσ, sustained %d buckets).",
			displayMetric(metric), cur.direction, service, current, unitOf(metric), median, unitOf(metric), z, dwellBuckets)
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
	} else if action == "resolve" {
		// v0.8.220 — FAST resolve (anomalyAction): the most-recent bucket is back
		// inside the band, so clear a recovered problem immediately (incl. the
		// "transient spike that opened then never resolved" report) instead of
		// waiting for ALL dwell buckets to align — which left problems stuck open
		// on gradual recovery / silent sources. The 3-bucket open dwell still
		// prevents re-open flapping.
		now := time.Now().UnixNano()
		open.Status = "resolved"
		open.ResolvedAt = &now
		open.Value = current
		if err := d.store.UpsertProblem(ctx, *open); err != nil {
			log.Printf("[anomaly] resolve %s: %v", ruleID, err)
			return
		}
		log.Printf("[anomaly] RESOLVED %s · %s (recovered, z=%.1f)", service, metric, z)
	}
}

// metricValueExpr returns the service_summary_5m SELECT expression that
// derives one tracked metric from the MV's aggregate states. Shared by the
// consecutive (fetchBuckets) and seasonal (fetchSeasonalBaseline) reads so
// both baselines are computed identically.
func metricValueExpr(metric string) (string, error) {
	switch metric {
	case "error_rate":
		return "countMerge(error_count_state) / nullIf(countMerge(span_count_state), 0) * 100", nil
	case "request_rate":
		return "countMerge(span_count_state) / 300.0", nil
	case "p99_ms":
		return "quantilesTDigestMerge(0.5, 0.95, 0.99)(duration_q_state)[3] / 1e6", nil
	}
	return "", fmt.Errorf("unknown metric %q", metric)
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
	vexpr, err := metricValueExpr(metric)
	if err != nil {
		return nil, err
	}
	sql := fmt.Sprintf(`
		SELECT toUnixTimestamp(time_bucket) AS t, %s AS v
		FROM service_summary_5m
		WHERE service_name = ? AND time_bucket >= ?
		GROUP BY t
		ORDER BY t
		LIMIT 1000
		SETTINGS max_execution_time = 10`, vexpr)

	rows, err := d.store.Conn().Query(ctx, sql, service, cutoff)
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

// fetchSeasonalBaseline returns the metric's value at the SAME time-of-day
// slot as `at` across the last seasonalDays days, weekday/weekend-matched —
// so a diurnal/weekly traffic pattern is the baseline instead of a flat 24h
// window (which makes every morning ramp a false positive and hides real
// off-peak dips). The slot is the bucket's (hour, 5-min minute); weekend is
// grouped separately because traffic profiles differ. Reads service_summary_5m
// (MV) with time-bound WHERE + LIMIT + max_execution_time. Returns fewer than
// seasonalDays samples for new/sparse services; the caller falls back.
func (d *Detector) fetchSeasonalBaseline(ctx context.Context, service, metric string, at time.Time) ([]float64, error) {
	vexpr, err := metricValueExpr(metric)
	if err != nil {
		return nil, err
	}
	cutoff := at.Add(-time.Duration(seasonalDays) * 24 * time.Hour)
	hour := at.Hour()
	minute := (at.Minute() / 5) * 5 // align to the 5-min bucket grid
	weekend := 0
	if wd := at.Weekday(); wd == time.Saturday || wd == time.Sunday {
		weekend = 1
	}
	// toDayOfWeek: 1=Mon … 7=Sun in ClickHouse; weekend = (dow >= 6).
	sql := fmt.Sprintf(`
		SELECT toUnixTimestamp(time_bucket) AS t, %s AS v
		FROM service_summary_5m
		WHERE service_name = ?
		  AND time_bucket >= ?
		  AND toHour(time_bucket) = ?
		  AND toMinute(time_bucket) = ?
		  AND if(toDayOfWeek(time_bucket) >= 6, 1, 0) = ?
		GROUP BY t
		ORDER BY t
		LIMIT 100
		SETTINGS max_execution_time = 10`, vexpr)

	rows, err := d.store.Conn().Query(ctx, sql, service, cutoff, hour, minute, weekend)
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

// chooseBaseline prefers the seasonal same-slot samples when seasonal mode is
// on AND there are enough of them; otherwise it falls back to the 24h
// consecutive baseline (new / sparse service, or seasonal disabled).
func chooseBaseline(seasonal, consecutive []float64) []float64 {
	if seasonalBaseline && len(seasonal) >= seasonalMinSamples {
		return seasonal
	}
	return consecutive
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
