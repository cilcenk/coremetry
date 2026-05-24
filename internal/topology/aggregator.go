// Package topology runs the background aggregator that pre-
// computes service-level topology edges into the topology_edges_5m
// table. The /api/topology/service endpoint reads from that
// aggregated table instead of hammering spans with a self-join
// on every request — at billions-of-spans-per-day scale the live
// path is unworkable. See chstore.WriteTopologyBucket for the
// actual aggregation query.
package topology

import (
	"context"
	"log"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
	"github.com/cilcenk/coremetry/internal/chstore"
)

const (
	lockKey       = "topology-aggregator-leader"
	bucketSize    = 5 * time.Minute
	settleDelay   = 30 * time.Second // wait this long past bucket end before processing
	// v0.5.379 — "live" tick rewrites the in-progress current
	// bucket every minute so the /topology + /backtrace pages
	// see fresh edge data within ~1 min of ingest instead of
	// waiting out the 5-min bucket + settle delay. The
	// ReplacingMergeTree(version) engine dedupes when newer
	// writes overlap the same (bucket, parent, child) tuple.
	liveTickInterval = 1 * time.Minute
	liveLockKey      = "topology-aggregator-live-leader"
)

// Aggregator wakes up every `interval` (5m by default) and runs
// WriteTopologyBucket for the most-recently-completed 5-min
// window. On boot it backfills `backfill` worth of buckets so the
// API has data from minute one.
//
// HA-safe: a Redis lock elects a single writer per tick. Lock TTL
// is generous (2× interval) so a slow CH doesn't kill liveness.
type Aggregator struct {
	store      *chstore.Store
	interval   time.Duration
	backfill   time.Duration
	lock       cache.Lock
	leader     *cache.LeaderHolder // v0.5.429 — main (closed-bucket) tick
	liveLeader *cache.LeaderHolder // v0.5.429 — live (in-progress bucket) tick
}

func New(store *chstore.Store, interval, backfill time.Duration, lock cache.Lock) *Aggregator {
	if interval <= 0 {
		interval = bucketSize
	}
	if backfill < 0 {
		backfill = 0
	}
	if lock == nil {
		_, lock = cache.NewNoop()
	}
	return &Aggregator{
		store: store, interval: interval, backfill: backfill,
		lock:       lock,
		leader:     cache.NewLeaderHolder(lock, lockKey, cache.LeaderTTL(interval)),
		liveLeader: cache.NewLeaderHolder(lock, liveLockKey, cache.LeaderTTL(liveTickInterval)),
	}
}

func (a *Aggregator) Start(ctx context.Context) {
	a.leader.Start(ctx)
	a.liveLeader.Start(ctx)
	go func() {
		// Stagger initial run so coremetry doesn't pile its first
		// heavy aggregation query onto a still-warming CH right at
		// boot. 45s past startup is enough that ingestion has
		// caught up but well within the operator's "is it working
		// yet" patience window.
		select {
		case <-ctx.Done():
			return
		case <-time.After(45 * time.Second):
		}
		a.tick(ctx, true)

		// Align subsequent ticks to bucket boundaries + a settle
		// delay so each run processes the just-completed bucket.
		// Without alignment, ticks could land mid-bucket and miss
		// a fraction of the rows.
		next := nextAlignedTick(time.Now(), a.interval, settleDelay)
		timer := time.NewTimer(time.Until(next))
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				a.tick(ctx, false)
				next = nextAlignedTick(time.Now(), a.interval, settleDelay)
				timer.Reset(time.Until(next))
			}
		}
	}()
	// v0.5.379 — live tick. Re-writes the in-progress (current)
	// bucket every minute so the topology / backtrace MV reads
	// surface fresh edge data without waiting 5 min + settle.
	// The 5-min-aligned tick above remains the source of truth
	// for closed buckets; ReplacingMergeTree(version) ensures
	// the most recent write per (bucket, parent, child) wins on
	// FINAL read.
	go a.liveLoop(ctx)
}

// liveLoop writes the IN-PROGRESS bucket (the one [now.Truncate(5m),
// now.Truncate(5m)+5m] currently being filled by ingest) every
// liveTickInterval. Different lock from the main tick so a slow
// closed-bucket job doesn't block the live refresh and vice
// versa.
func (a *Aggregator) liveLoop(ctx context.Context) {
	// Initial wait — let the bootstrap tick finish before we
	// start writing the live bucket, otherwise the two collide
	// on the same partition on cold start.
	select {
	case <-ctx.Done():
		return
	case <-time.After(60 * time.Second):
	}
	t := time.NewTicker(liveTickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.liveTick(ctx)
		}
	}
}

func (a *Aggregator) liveTick(ctx context.Context) {
	if !a.liveLeader.IsLeader() {
		return
	}

	// Current in-progress bucket — start aligned to the bucket
	// size. WriteTopologyBucket selects spans where
	// time IN [bucketStart, bucketStart + 5m), so writing this
	// bucket mid-window captures everything ingested so far.
	bucketStart := time.Now().Truncate(a.interval)
	if err := a.store.WriteTopologyBucket(ctx, bucketStart); err != nil {
		log.Printf("[topology-agg] live service bucket %s: %v", bucketStart.Format(time.RFC3339), err)
		return
	}
	if err := a.store.WriteTopologyOpBucket(ctx, bucketStart); err != nil {
		log.Printf("[topology-agg] live op bucket %s: %v", bucketStart.Format(time.RFC3339), err)
		return
	}
	if err := a.store.WriteServiceCallersBucket(ctx, bucketStart); err != nil {
		log.Printf("[topology-agg] live service-callers %s: %v", bucketStart.Format(time.RFC3339), err)
		return
	}
	// Skip RootFlows on live ticks — it's a higher-cost JOIN
	// query and operators are less sensitive to fresh data
	// there (business-flow visibility lags by a window
	// naturally). Re-evaluate if operator-reported.
}

func (a *Aggregator) tick(ctx context.Context, bootstrap bool) {
	if !a.leader.IsLeader() {
		return
	}

	// Target the just-completed bucket. e.g. now=14:23 → bucket
	// 14:15-14:20. Skipping the live bucket avoids partial-data
	// reads; the cache layer (v0.5.107) handles "what does the
	// last 5 minutes look like" via live query for now.
	now := time.Now()
	end := now.Add(-settleDelay).Truncate(a.interval)
	buckets := []time.Time{end.Add(-a.interval)}
	if bootstrap && a.backfill > 0 {
		n := int(a.backfill / a.interval)
		for i := 2; i <= n; i++ {
			buckets = append(buckets, end.Add(-time.Duration(i)*a.interval))
		}
	}
	for _, b := range buckets {
		if err := a.store.WriteTopologyBucket(ctx, b); err != nil {
			log.Printf("[topology-agg] service bucket %s: %v", b.Format(time.RFC3339), err)
			continue
		}
		if err := a.store.WriteTopologyOpBucket(ctx, b); err != nil {
			log.Printf("[topology-agg] op bucket %s: %v", b.Format(time.RFC3339), err)
			continue
		}
		if err := a.store.WriteRootFlowsBucket(ctx, b); err != nil {
			log.Printf("[topology-agg] root flows bucket %s: %v", b.Format(time.RFC3339), err)
			continue
		}
		// v0.5.368 — populate service_callers_5m alongside the
		// other 5-min rollups. Same settle-delay + retry shape
		// since it's the same JOIN scope.
		if err := a.store.WriteServiceCallersBucket(ctx, b); err != nil {
			log.Printf("[topology-agg] service-callers bucket %s: %v", b.Format(time.RFC3339), err)
			continue
		}
	}
	if bootstrap {
		log.Printf("[topology-agg] backfilled %d buckets through %s", len(buckets), end.Format(time.RFC3339))
	}
}

// nextAlignedTick returns the next instant at which we should
// run the aggregator: the next bucket boundary past now + the
// settle delay. e.g. now=14:23:15, interval=5m, settle=30s →
// next bucket = 14:25, next tick = 14:25:30.
func nextAlignedTick(now time.Time, interval, settle time.Duration) time.Time {
	bucket := now.Truncate(interval).Add(interval)
	return bucket.Add(settle)
}
