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
	bucketSeconds = 300 // 5-minute buckets
	historyHours  = 24  // window used to learn the baseline
	// v0.8.220 — operator-reported too many anomalies + transient spikes that
	// don't clear. Davis-style asymmetry: HARD to open (3.5σ AND 3 sustained
	// 5-min buckets = 15 min, so an instant blip never opens), FAST to resolve
	// (the most-recent bucket back inside the band clears it — see the detector;
	// a single-bucket dip can't re-open thanks to the 3-bucket dwell, so it
	// doesn't flap).
	openZ        = 3.5    // |z| above this opens an anomaly
	resolveZ     = 1.5    // and below this clears it
	criticalZ    = 5.0    // |z| above this escalates warning → critical
	dwellBuckets = 3      // consecutive buckets that must all fire to open (anti-flap)
	minSamples   = 12     // need at least this many baseline buckets
	madScale     = 0.6745 // scales MAD to a normal-dist stdev (modified z-score)
	magnitudeEps = 1e-9   // denominator guard for the relative-change floor

	seasonalBaseline = true // baseline from same-time-of-day history, not a flat 24h window
	// v0.8.250 — operator-reported diurnal false positives on off-peak/night
	// slots (a bank: some ops finish fast by day but slow + thin out at night).
	// Root cause was SAMPLE SCARCITY: the old baseline matched the EXACT 5-min
	// slot on the SAME weekday/weekend class over 7 days, so a Saturday night
	// slot had only ~2 candidate samples — below seasonalMinSamples — and fell
	// back to the flat 24h window, which conflates day peak with night trough
	// and fires. Three widenings feed the SAME slot more samples:
	//   • seasonalDays 7→14 (twice the history: 2 Saturdays instead of 1)
	//   • ±seasonalNeighborBuckets same-class neighbour slots join the baseline
	//     (a ±15-min window ⇒ 7 candidate buckets/day instead of 1)
	//   • the weekend class splits into saturday / sunday (a bank runs a
	//     different profile Sat vs Sun; cmt ≠ paz) — dayClass() below.
	seasonalDays            = 14 // days of same-slot history for the seasonal baseline
	seasonalMinSamples      = 4  // min same-slot samples before the seasonal baseline is trusted
	seasonalNeighborBuckets = 3  // ± same-class neighbour buckets (±15 min) folded into the baseline
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
	// Read the seasonal-baseline knobs fresh each sweep (same
	// LoadPersisted-per-tick pattern the evaluator uses for the
	// promotion config) so an operator tune takes effect on the next
	// scan without a redeploy. They ride the existing anomaly_promotion
	// blob — one anomaly settings surface, not two. v0.8.250.
	days, minSamples, neighbor := seasonalParams(d.store.GetAnomalyPromotion(ctx))
	for _, svc := range services {
		for _, m := range trackedMetrics {
			d.checkOne(ctx, svc.Name, m, days, minSamples, neighbor)
		}
	}
}

func (d *Detector) checkOne(ctx context.Context, service, metric string, seasonalDays, seasonalMinSamples, neighborBuckets int) {
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
		if s, err := d.fetchSeasonalBaseline(ctx, service, metric, time.Now(), seasonalDays, neighborBuckets); err == nil {
			seasonal = s
		}
	}
	baseline := chooseBaseline(seasonal, consecutive, seasonalMinSamples)

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
	now := time.Now()
	cutoff := now.Add(-time.Duration(historyHours) * time.Hour)
	vexpr, err := metricValueExpr(metric)
	if err != nil {
		return nil, err
	}
	// v0.8.316 — read COMPLETE buckets only. The still-filling bucket made
	// request_rate (÷ fixed 300s) read ~elapsed/300 of the true rate, so a
	// live spike looked baseline one minute into each bucket and the
	// fast-resolve closed the open anomaly mid-incident (a flap per
	// bucket). Complete-buckets-only costs ≤5 min detection lag; every
	// sample now truly spans the 300s it's divided by.
	sql := fmt.Sprintf(`
		SELECT toUnixTimestamp(time_bucket) AS t, %s AS v
		FROM service_summary_5m
		WHERE service_name = ? AND time_bucket >= ? AND time_bucket < ?
		GROUP BY t
		ORDER BY t
		LIMIT 1000
		SETTINGS max_execution_time = 10`, vexpr)

	rows, err := d.store.Conn().Query(ctx, sql, service, cutoff, lastCompleteBucketStart(now))
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

// lastCompleteBucketStart is the exclusive upper bound for MV series reads
// (v0.8.316): the current bucket's START on the UTC 5-minute grid — the
// same grid toStartOfInterval uses — so `time_bucket < bound` keeps only
// buckets whose full 300s have elapsed. Pure + table-tested
// (complete_bucket_test.go).
func lastCompleteBucketStart(now time.Time) time.Time {
	return now.Truncate(5 * time.Minute)
}

// dayClass buckets a timestamp into the day-of-week traffic profile the
// seasonal baseline matches on: "saturday" / "sunday" / "weekday". Split into
// THREE classes (was a weekday/weekend binary) because a bank runs a distinct
// profile on Saturday vs Sunday — cmt ≠ paz — so blending them poisoned the
// baseline with the wrong day's shape. Pure so it's table-testable across all
// seven weekdays. Mirrored in SQL by the multiIf on toDayOfWeek below.
func dayClass(t time.Time) string {
	switch t.Weekday() {
	case time.Saturday:
		return "saturday"
	case time.Sunday:
		return "sunday"
	default:
		return "weekday"
	}
}

// slotSecondsOfDay returns t's seconds-since-midnight aligned DOWN to the
// 5-min bucket grid (0 … 86340). This is the CENTRE of the neighbour window
// the seasonal query matches; the MV's toHour*3600+toMinute*60 is the same
// grid, so the circular distance below is a whole number of buckets.
func slotSecondsOfDay(t time.Time) int {
	return t.Hour()*3600 + (t.Minute()/5)*5*60
}

// seasonalParams resolves the operator-tunable seasonal knobs off the shared
// anomaly_promotion blob, clamping each to a sane range and falling back to
// the compile-time default when a field is zero/absent or out of bounds. The
// clamp keeps the CH read bounded regardless of a hand-crafted API PUT (days
// caps the cutoff lookback; neighbourBuckets caps the ±window). Pure so the
// default/clamp table is unit-tested. v0.8.250.
func seasonalParams(cfg chstore.AnomalyPromotionConfig) (days, minSamples, neighborBuckets int) {
	days = cfg.SeasonalDays
	if days < 1 || days > 90 {
		days = seasonalDays
	}
	minSamples = cfg.SeasonalMinSamples
	if minSamples < 1 || minSamples > 500 {
		minSamples = seasonalMinSamples
	}
	neighborBuckets = cfg.SeasonalNeighborBuckets
	if neighborBuckets < 1 || neighborBuckets > 24 {
		neighborBuckets = seasonalNeighborBuckets
	}
	return days, minSamples, neighborBuckets
}

// seasonalBaselineSQL builds the seasonal-baseline query for a metric's
// pre-computed SELECT expression. Extracted pure (no store, no binds) so the
// SQL SHAPE is unit-tested — the circular midnight-wrap distance, the LIMIT +
// max_execution_time bounds, the time-bounded WHERE, the three-way day class,
// and that it reads the MV (never raw spans). The six `?` placeholders bind,
// in order: service, cutoff, dayClass, targetSecondsOfDay (twice), radius.
func seasonalBaselineSQL(vexpr string) string {
	// sodExpr — the bucket's seconds-of-day on the same 5-min grid as targetSod
	// (buckets are 5-min aligned, so toSecond is 0; included for correctness).
	const sodExpr = "(toHour(time_bucket) * 3600 + toMinute(time_bucket) * 60)"
	// classExpr — three-way bank day class. toDayOfWeek: 1=Mon … 6=Sat, 7=Sun.
	const classExpr = "multiIf(toDayOfWeek(time_bucket) = 6, 'saturday', toDayOfWeek(time_bucket) = 7, 'sunday', 'weekday')"

	// least(|sod-target|, 86400-|sod-target|) is the circular (midnight-wrap)
	// distance in seconds; <= radius keeps the ±neighborBuckets slots of the
	// matching day class. time_bucket >= cutoff prunes daily partitions first.
	return fmt.Sprintf(`
		SELECT toUnixTimestamp(time_bucket) AS t, %[1]s AS v
		FROM service_summary_5m
		WHERE service_name = ?
		  AND time_bucket >= ?
		  AND %[3]s = ?
		  AND least(abs(%[2]s - ?), 86400 - abs(%[2]s - ?)) <= ?
		GROUP BY t
		ORDER BY t
		LIMIT 700
		SETTINGS max_execution_time = 10`, vexpr, sodExpr, classExpr)
}

// fetchSeasonalBaseline returns the metric's values at the same time-of-day
// slot as `at` — PLUS its ±neighborBuckets neighbours (a ±15-min window at the
// default radius) — across the last `days` days, matched on `at`'s dayClass
// (weekday / saturday / sunday). Widening the slot into a neighbour window +
// splitting saturday/sunday + 14 days of history is what feeds the baseline
// enough samples on thin off-peak/night slots so it clears seasonalMinSamples
// instead of falling back to the flat 24h window (the diurnal-false-positive
// root cause — v0.8.250).
//
// The neighbour match is a CIRCULAR seconds-of-day distance, least(d,86400-d),
// so the window wraps correctly across midnight (23:50's neighbours include
// 00:00) instead of clipping at the day boundary. Reads service_summary_5m
// (MV, never raw spans) with a partition-pruning time-bound WHERE + LIMIT +
// max_execution_time. Returns fewer samples for new/sparse services; the
// caller (chooseBaseline) falls back to the consecutive window.
func (d *Detector) fetchSeasonalBaseline(ctx context.Context, service, metric string, at time.Time, days, neighborBuckets int) ([]float64, error) {
	vexpr, err := metricValueExpr(metric)
	if err != nil {
		return nil, err
	}
	cutoff := at.Add(-time.Duration(days) * 24 * time.Hour)
	targetSod := slotSecondsOfDay(at)         // 5-min-aligned centre of the window
	radius := neighborBuckets * bucketSeconds // ±window half-width in seconds
	class := dayClass(at)
	sql := seasonalBaselineSQL(vexpr)

	rows, err := d.store.Conn().Query(ctx, sql, service, cutoff, class, targetSod, targetSod, radius)
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
// on AND there are at least minSamples of them; otherwise it falls back to the
// 24h consecutive baseline (new / sparse service, or seasonal disabled).
func chooseBaseline(seasonal, consecutive []float64, minSamples int) []float64 {
	if seasonalBaseline && len(seasonal) >= minSamples {
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
	case "p99_ms":
		return "P99 latency"
	case "error_rate":
		return "Error rate"
	case "request_rate":
		return "Request rate"
	}
	return m
}
func unitOf(m string) string {
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
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
