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
	// v0.8.506: yalnız isim listesi gerekiyor — MV'den (bkz.
	// ListActiveServiceNames); ham spans 24h taraması kalktı.
	services, err := d.store.ListActiveServiceNames(ctx, 24*time.Hour)
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

	// v0.8.507 — batch the per-(service,metric) MV reads into ONE
	// GROUP BY service_name pass PER metric (×2: consecutive + seasonal),
	// replacing the old services × trackedMetrics × 2 per-service queries.
	// At prod scale that loop was ~1400 svc × 3 metrics × 2 reads ≈ 8400
	// queries / 2-min tick, each re-reading ~the whole window's granules
	// (query_log: 46-65K read_rows apiece, ~708M rows/hr re-scanning the
	// same window). The batched form reads those rows ONCE — 6 queries /
	// tick — then distributes the per-service series to checkOne. Same
	// pattern the evaluator adopted in v0.8.352. One `now` for the whole
	// tick keeps the window (and the seasonal slot) consistent across
	// every service, instead of the old per-checkOne time.Now() drift.
	now := time.Now()
	bucketsByMetric := make(map[string]map[string][]float64, len(trackedMetrics))
	seasonalByMetric := make(map[string]map[string][]float64, len(trackedMetrics))
	for _, m := range trackedMetrics {
		all, err := d.fetchAllBuckets(ctx, m, now)
		if err != nil {
			// Whole-metric batch read errored this tick → skip the metric
			// (every service's series is absent below → checkOne skips it),
			// matching the old per-service "fetch error → skip" behavior.
			log.Printf("[anomaly] batch buckets %s: %v", m, err)
			continue
		}
		bucketsByMetric[m] = all
		if seasonalBaseline {
			// Best-effort: a seasonal read error leaves the metric absent
			// from seasonalByMetric → seriesFor returns nil → chooseBaseline
			// falls back to the consecutive window (unchanged behavior).
			if s, err := d.fetchAllSeasonal(ctx, m, now, days, neighbor); err == nil {
				seasonalByMetric[m] = s
			} else {
				log.Printf("[anomaly] batch seasonal %s: %v", m, err)
			}
		}
	}

	for _, svc := range services {
		for _, m := range trackedMetrics {
			buckets := seriesFor(bucketsByMetric[m], svc)
			seasonal := seriesFor(seasonalByMetric[m], svc)
			d.checkOne(ctx, svc, m, buckets, seasonal, minSamples)
		}
	}
}

// checkOne runs the anomaly verdict for one (service, metric) from PRE-BATCHED
// series (v0.8.507): buckets = the consecutive 5-min series, seasonal = the
// same-slot history — both handed in by scan()'s per-metric batch reads rather
// than fetched here per service. A nil/short `buckets` (service absent from the
// batch, or the metric's batch read errored this tick) is skipped by the
// enoughHistory guard — identical to the old per-service fetch returning empty.
func (d *Detector) checkOne(ctx context.Context, service, metric string, buckets, seasonal []float64, seasonalMinSamples int) {
	if !enoughHistory(len(buckets)) {
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
	// few same-slot samples falls back to the consecutive window (chooseBaseline
	// gets a nil seasonal when the batch read errored → consecutive).
	consecutive := buckets[:split]
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

// enoughHistory reports whether a fetched bucket series has enough samples to
// run the dwell-windowed check: minSamples baseline buckets PLUS a full dwell
// window. A service the batch query returned no (or too few) rows for is
// skipped here — the same guard the old per-service fetchBuckets + len check
// applied. Pure so the "empty/sparse service is skipped" contract is testable
// without a live ClickHouse. v0.8.507.
func enoughHistory(n int) bool { return n >= minSamples+dwellBuckets }

// seriesFor returns the batched series for a service, or nil when the metric's
// batch query returned no rows for it (new/sparse service, OR the batch read
// errored this tick and the whole metric map is absent). A nil series makes
// checkOne's enoughHistory guard skip the service — identical to the old
// per-service fetch returning an empty result set. Pure. v0.8.507.
func seriesFor(byService map[string][]float64, service string) []float64 {
	return byService[service]
}

// accumulateSeries folds one scanned (service, value) row into the per-service
// series map, preserving arrival order. The batch queries ORDER BY
// service_name, t so each service's slice comes out ascending in time — the
// same order the old per-service `ORDER BY t` produced, which the dwell window
// (buckets[len-dwellBuckets:]) and the current sample (buckets[len-1]) depend
// on. Pure so the batch distribution is unit-tested without a live CH. v0.8.507.
func accumulateSeries(byService map[string][]float64, service string, v float64) {
	byService[service] = append(byService[service], v)
}

// buildAllBucketsQuery is the batched twin of the old per-service fetchBuckets
// read: ONE `GROUP BY service_name, t` pass over service_summary_5m for a
// metric, instead of one `WHERE service_name = ?` query PER service. The metric
// SELECT expression (metricValueExpr) is byte-identical to the per-service read
// so every baseline value is computed the same way. Extracted pure so the SQL
// SHAPE — no service filter, GROUP BY service_name, time-bounded WHERE (for
// partition pruning + the v0.8.316 complete-buckets-only upper bound), a
// per-service + overall LIMIT safety cap, max_execution_time, MV (never raw
// spans) — is table-tested without a CH connection. Two `?` binds, in order:
// cutoff (historyHours ago), lastCompleteBucketStart(now).
//
// v0.5.296 — the reads that this replaces already moved OFF raw spans onto
// service_summary_5m (scale-audit critical). v0.8.507 collapses the remaining
// per-service N+1 fan-out into one pass: prod was ~1400 svc × 3 metrics × 2
// reads ≈ 8400 queries / 2-min tick, each re-scanning ~the whole window's
// granules; the batch reads those rows ONCE.
func buildAllBucketsQuery(vexpr string) string {
	// v0.8.316 — complete buckets only (time_bucket < lastCompleteBucketStart):
	// the still-filling bucket made request_rate (÷ fixed 300s) read
	// ~elapsed/300 of the true rate, so a live spike looked baseline one minute
	// into each bucket and the fast-resolve closed the open anomaly mid-incident.
	return fmt.Sprintf(`
		SELECT service_name, toUnixTimestamp(time_bucket) AS t, %s AS v
		FROM service_summary_5m
		WHERE time_bucket >= ? AND time_bucket < ?
		GROUP BY service_name, t
		ORDER BY service_name, t
		LIMIT 1000 BY service_name
		LIMIT 20000000
		SETTINGS max_execution_time = 30`, vexpr)
}

// fetchAllBuckets runs buildAllBucketsQuery once for a metric and returns the
// per-service 5-minute series (ascending in time), keyed by service_name. A
// service absent from the map had no complete buckets in the window; checkOne's
// enoughHistory guard skips it. `now` is fixed by the caller for the whole tick
// so every service shares one window. v0.8.507.
func (d *Detector) fetchAllBuckets(ctx context.Context, metric string, now time.Time) (map[string][]float64, error) {
	vexpr, err := metricValueExpr(metric)
	if err != nil {
		return nil, err
	}
	cutoff := now.Add(-time.Duration(historyHours) * time.Hour)
	rows, err := d.store.Conn().Query(ctx, buildAllBucketsQuery(vexpr), cutoff, lastCompleteBucketStart(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]float64)
	for rows.Next() {
		var svc string
		var t uint32
		var v float64
		if err := rows.Scan(&svc, &t, &v); err != nil {
			return nil, err
		}
		accumulateSeries(out, svc, v)
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

// buildAllSeasonalQuery is the batched twin of the old per-service seasonal
// read: ONE `GROUP BY service_name, t` pass matching the same time-of-day slot
// (± neighbour buckets, circular midnight-wrap) and day class across ALL
// services, instead of one `WHERE service_name = ?` query per service. The
// slot / class / radius binds are sweep constants — identical for every service
// in a tick — so the ONLY shape change from the per-service query is dropping
// the service filter and grouping by service_name (v0.8.507). Extracted pure so
// the SQL SHAPE is unit-tested — the circular midnight-wrap distance, the LIMIT
// + max_execution_time bounds, the time-bounded WHERE, the three-way day class,
// GROUP BY service_name, and that it reads the MV (never raw spans). The five
// `?` placeholders bind, in order: cutoff, dayClass, targetSecondsOfDay (twice),
// radius.
func buildAllSeasonalQuery(vexpr string) string {
	// sodExpr — the bucket's seconds-of-day on the same 5-min grid as targetSod
	// (buckets are 5-min aligned, so toSecond is 0; included for correctness).
	// v0.8.323 — pinned to UTC: the Go side derives slot/class from at.UTC(),
	// so the SQL must resolve hour/weekday on the SAME clock no matter what
	// the CH server's default timezone is. A TZ delta (app Europe/Istanbul,
	// CH UTC) silently matched the wrong time-of-day slot — day-peak history
	// against a night "now" — reintroducing the diurnal false positives this
	// seasonal feature exists to kill.
	const sodExpr = "(toHour(time_bucket, 'UTC') * 3600 + toMinute(time_bucket, 'UTC') * 60)"
	// classExpr — three-way bank day class. toDayOfWeek mode 0: 1=Mon … 6=Sat, 7=Sun.
	const classExpr = "multiIf(toDayOfWeek(time_bucket, 0, 'UTC') = 6, 'saturday', toDayOfWeek(time_bucket, 0, 'UTC') = 7, 'sunday', 'weekday')"

	// least(|sod-target|, 86400-|sod-target|) is the circular (midnight-wrap)
	// distance in seconds; <= radius keeps the ±neighborBuckets slots of the
	// matching day class. time_bucket >= cutoff prunes daily partitions first.
	return fmt.Sprintf(`
		SELECT service_name, toUnixTimestamp(time_bucket) AS t, %[1]s AS v
		FROM service_summary_5m
		WHERE time_bucket >= ?
		  AND %[3]s = ?
		  AND least(abs(%[2]s - ?), 86400 - abs(%[2]s - ?)) <= ?
		GROUP BY service_name, t
		ORDER BY service_name, t
		LIMIT 700 BY service_name
		LIMIT 14000000
		SETTINGS max_execution_time = 30`, vexpr, sodExpr, classExpr)
}

// fetchAllSeasonal runs buildAllSeasonalQuery once for a metric and returns the
// per-service seasonal samples (the same time-of-day slot as `at` PLUS its
// ±neighborBuckets neighbours, across the last `days` days, matched on `at`'s
// dayClass), keyed by service_name. Widening the slot into a neighbour window +
// splitting saturday/sunday + 14 days of history is what feeds the baseline
// enough samples on thin off-peak/night slots so it clears seasonalMinSamples
// instead of falling back to the flat 24h window (the diurnal-false-positive
// root cause — v0.8.250). The neighbour match is a CIRCULAR seconds-of-day
// distance so the window wraps correctly across midnight. Returns fewer (or no)
// samples for new/sparse services; chooseBaseline falls back to the consecutive
// window. `at` is fixed by the caller for the whole tick so the slot is
// consistent across every service. v0.8.507.
func (d *Detector) fetchAllSeasonal(ctx context.Context, metric string, at time.Time, days, neighborBuckets int) (map[string][]float64, error) {
	vexpr, err := metricValueExpr(metric)
	if err != nil {
		return nil, err
	}
	// v0.8.323 — slot + day class derive from UTC so they match the SQL's
	// UTC-pinned toHour/toDayOfWeek (see buildAllSeasonalQuery). With no DST
	// in TR, a constant offset keeps slot-matching consistent either way —
	// but only when BOTH sides share one clock.
	at = at.UTC()
	cutoff := at.Add(-time.Duration(days) * 24 * time.Hour)
	targetSod := slotSecondsOfDay(at)         // 5-min-aligned centre of the window
	radius := neighborBuckets * bucketSeconds // ±window half-width in seconds
	class := dayClass(at)

	rows, err := d.store.Conn().Query(ctx, buildAllSeasonalQuery(vexpr), cutoff, class, targetSod, targetSod, radius)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]float64)
	for rows.Next() {
		var svc string
		var t uint32
		var v float64
		if err := rows.Scan(&svc, &t, &v); err != nil {
			return nil, err
		}
		accumulateSeries(out, svc, v)
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
