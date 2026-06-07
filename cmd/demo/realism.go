// realism.go — traffic shape, saturation, and incident model (v0.8.x).
//
// The base generator emits a believable banking mesh, but at a FLAT rate
// with FIXED per-hop error probabilities and UNIFORM latencies. Real
// production traffic doesn't look like that:
//
//   - it breathes with the business day (a diurnal curve: quiet at night,
//     a mid-morning peak, a smaller early-evening bump);
//   - it has organic micro-spikes minute to minute;
//   - every so often something breaks — a dependency degrades, the DB
//     contends, a GC storm hits, a noisy neighbour saturates a node — and
//     for a few minutes the affected window shows ELEVATED latency AND
//     error rates that then recover on their own.
//
// This file is the single source of truth for that shape. It exposes a
// global `L` whose three hot-path factors are read lock-free:
//
//   L.latencyFactor()  multiplies every dur() — saturation stretches the
//                      whole latency distribution at once, so the trace
//                      waterfalls and the http.server.duration histogram
//                      move TOGETHER (which is what makes the percentile
//                      charts and the anomaly correlator light up).
//   L.rateFactor()     multiplies the driver's scenarios/sec — the demo
//                      actually slows down at 03:00 and surges at 10:00.
//   L.errorBump()      extra failure probability folded into rollFail(),
//                      so failures CLUSTER during incidents instead of
//                      being sprinkled uniformly forever.
//
// Keeping all three in one model is what ties metrics + traces + logs to
// the same underlying story rather than three independent random streams.
package main

import (
	"context"
	"math"
	mrand "math/rand/v2"
	"sync"
	"sync/atomic"
	"time"
)

// loadModel holds the current traffic/saturation state. The three factors
// are stored as float64 bits in atomics so the trace hot path (dur,
// rollFail, the driver loop) never contends on a mutex; only the slow
// recompute tick and incident start/stop take the lock.
type loadModel struct {
	latencyMult atomic.Uint64 // float64 bits — multiplies dur()
	rateMult    atomic.Uint64 // float64 bits — multiplies scenarios/sec
	errBump     atomic.Uint64 // float64 bits — extra P(fail), 0..~0.25

	mu       sync.Mutex
	incident *incident
}

// incident is a transient degradation window. While active it inflates
// latency and (for most kinds) error rates until `until`, then clears.
type incident struct {
	until       time.Time
	latencyMult float64
	errBump     float64
	label       string
}

// L is the process-wide load model.
var L = &loadModel{}

func storeF(a *atomic.Uint64, v float64) { a.Store(math.Float64bits(v)) }
func loadF(a *atomic.Uint64) float64     { return math.Float64frombits(a.Load()) }

func init() {
	storeF(&L.latencyMult, 1.0)
	storeF(&L.rateMult, 1.0)
	storeF(&L.errBump, 0.0)
}

// diurnalShape returns a 0.28..1.0 business-day multiplier for the local
// wall-clock time: an overnight trough, a mid-morning main peak (~10:00),
// and a smaller early-evening bump (~19:00). Computed off the minute of
// day with raised-cosine humps so the curve is smooth rather than
// stepping on the hour.
func diurnalShape(t time.Time) float64 {
	minute := float64(t.Hour()*60 + t.Minute())
	peak := func(center, width, amp float64) float64 {
		d := math.Abs(minute - center)
		if d > width {
			return 0
		}
		return amp * 0.5 * (1 + math.Cos(math.Pi*d/width))
	}
	v := 0.28 + peak(10*60, 6*60, 0.72) + peak(19*60, 3.5*60, 0.35)
	if v > 1.0 {
		v = 1.0
	}
	return v
}

// run recomputes the diurnal rate every few seconds and randomly starts /
// stops short incidents. Returns when ctx is cancelled.
func (l *loadModel) run(ctx context.Context) {
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	l.recompute(time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			l.recompute(now)
		}
	}
}

func (l *loadModel) recompute(now time.Time) {
	// Diurnal base rate + small minute-to-minute noise.
	rate := diurnalShape(now) * (0.92 + mrand.Float64()*0.16)

	latMult := 1.0
	errBump := 0.0

	l.mu.Lock()
	if l.incident != nil && now.After(l.incident.until) {
		l.incident = nil
	}
	if l.incident == nil && mrand.Float64() < 0.02 { // ~ one every few minutes
		l.incident = newIncident(now)
	}
	if l.incident != nil {
		latMult = l.incident.latencyMult
		errBump = l.incident.errBump
		rate *= 1.15 // incidents often coincide with a load surge
	}
	l.mu.Unlock()

	// Organic micro-spike even without a full incident.
	if mrand.Float64() < 0.05 {
		rate *= 1.4 + mrand.Float64()*0.8
		latMult *= 1.2
	}

	storeF(&l.rateMult, rate)
	storeF(&l.latencyMult, latMult)
	storeF(&l.errBump, errBump)
}

// newIncident picks one of a few production-shaped failure modes and gives
// it a 1–4 minute lifetime.
func newIncident(now time.Time) *incident {
	kinds := []incident{
		{latencyMult: 2.4, errBump: 0.10, label: "oracle-row-lock-contention"},
		{latencyMult: 3.2, errBump: 0.04, label: "jvm-gc-pause-storm"},
		{latencyMult: 1.8, errBump: 0.18, label: "downstream-dependency-degraded"},
		{latencyMult: 4.0, errBump: 0.02, label: "noisy-neighbor-cpu-steal"},
	}
	in := kinds[mrand.IntN(len(kinds))]
	in.until = now.Add(time.Duration(60+mrand.IntN(180)) * time.Second)
	return &in
}

// Hot-path readers.
func (l *loadModel) latencyFactor() float64 { return loadF(&l.latencyMult) }
func (l *loadModel) rateFactor() float64    { return loadF(&l.rateMult) }
func (l *loadModel) errorBump() float64     { return loadF(&l.errBump) }

// incidentLabel returns the active incident's label (or "" if none) — used
// to stamp logs/metrics during a degradation window.
func (l *loadModel) incidentLabel() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.incident == nil {
		return ""
	}
	return l.incident.label
}

// rollFail returns true with probability basePct% PLUS the current
// load/incident error bump, so failures cluster during incidents the way
// real outages do instead of being uniform forever.
func rollFail(basePct int) bool {
	p := float64(basePct)/100.0 + L.errorBump()
	if p > 0.95 {
		p = 0.95
	}
	return mrand.Float64() < p
}

// ─── Metric-shaping helpers (read by flush) ─────────────────────────────────

// latencyBounds are the explicit histogram bucket boundaries (ms) shared by
// every duration histogram the demo emits, so the backend can compute
// p50/p90/p95/p99 from REAL bucket counts instead of only min/max/avg.
var latencyBounds = []float64{1, 2, 5, 10, 20, 50, 75, 100, 150, 200, 300, 500, 750, 1000, 2000, 5000}

// bucketIndex returns the histogram bucket a value falls into for the
// shared latencyBounds (len(latencyBounds)+1 buckets, last is the overflow).
func bucketIndex(v float64) int {
	i := 0
	for i < len(latencyBounds) && v > latencyBounds[i] {
		i++
	}
	return i
}

// redisBackedServices read/write a Redis cache, so they get a cache
// hit-ratio gauge that dips during incidents.
var redisBackedServices = map[string]bool{
	"fraud-service": true, "fraud-ml-service": true, "forex-service": true,
	"underwriting-service": true, "limits-service": true, "pricing-service": true,
}

// kafkaConsumerServices consume from Kafka, so they get a consumer-lag
// gauge that builds up under load / during incidents.
var kafkaConsumerServices = map[string]bool{
	"notification-service": true, "aml-service": true, "audit-service": true,
	"rewards-service": true, "chargeback-service": true, "datawarehouse-etl": true,
	"settlement-service": true,
}

// gcManaged reports whether a runtime has a stop-the-world-ish collector
// worth emitting a gc-pause gauge for.
func gcManaged(lang string) bool {
	switch lang {
	case "java", "go", "dotnet", ".net", "nodejs":
		return true
	}
	return false
}
