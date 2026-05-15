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
)

// Aggregator wakes up every `interval` (5m by default) and runs
// WriteTopologyBucket for the most-recently-completed 5-min
// window. On boot it backfills `backfill` worth of buckets so the
// API has data from minute one.
//
// HA-safe: a Redis lock elects a single writer per tick. Lock TTL
// is generous (2× interval) so a slow CH doesn't kill liveness.
type Aggregator struct {
	store    *chstore.Store
	interval time.Duration
	backfill time.Duration
	lock     cache.Lock
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
	return &Aggregator{store: store, interval: interval, backfill: backfill, lock: lock}
}

func (a *Aggregator) Start(ctx context.Context) {
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
}

func (a *Aggregator) tick(ctx context.Context, bootstrap bool) {
	got, err := a.lock.TryAcquire(ctx, lockKey, 2*a.interval)
	if err != nil || !got {
		return
	}
	defer a.lock.Release(ctx, lockKey)

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
