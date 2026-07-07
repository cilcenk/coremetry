package cache

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// LeaderHolder — v0.5.426. Designates ONE pod as the leader for
// a given background worker via a heartbeat-refreshed Redis
// lock. Replaces the prior per-tick TryAcquire/Release pattern,
// which at N pods caused each worker to alternate execution
// across all N (correct but log-noisy + N× CH load on workers
// that don't actually need parallel execution).
//
// Lifecycle:
//
//  1. NewLeaderHolder(lock, key, ttl) returns a non-running
//     holder. ttl is the lease TTL; refresh runs at ttl/3.
//  2. Start(ctx) launches the heartbeat goroutine. The
//     goroutine immediately attempts TryAcquire, then enters
//     the refresh loop. ctx.Done() cleanly releases the lock
//     and exits.
//  3. IsLeader() reports the current state. Background workers
//     check this at the top of each tick and skip when false:
//
//         if !leader.IsLeader() { return }
//         // ... do work ...
//
//  4. The held state flips back to false when Refresh reports
//     the lease is definitively lost (ok=false) OR — v0.8.341 —
//     when Refresh ERRORS have gone on longer than the lease
//     TTL (Redis partition / AUTH flap: we can no longer prove
//     we hold it, and the lease HAS expired server-side, so a
//     peer may legitimately be leader). The workers stop
//     running; the holder falls back into the acquire loop and
//     leadership picks back up when Redis returns.
//
// Bounded behaviour at N pods: exactly one pod is leader at any
// moment (subject to lease TTL crossover during failover — same
// guarantee Kubernetes leader election provides). When the
// leader pod dies, another acquires within ttl of the next poll.
//
// Per-worker key: each background worker (errors-inbox,
// anomaly recorder, topology aggregator, …) holds its OWN
// LeaderHolder with a unique key. Different pods can lead
// different workers; this matches the prior per-worker
// lockKey structure + keeps failover granular.
type LeaderHolder struct {
	lock    Lock
	key     string
	ttl     time.Duration
	refresh time.Duration

	held atomic.Bool

	startOnce sync.Once
}

// LeaderTTL picks a sensible lease TTL given a worker's tick
// interval. Bounded so very short ticks (10s) still get a 30s
// floor (avoids thrashing on Redis blips) and very long ticks
// (hourly retention sweep) don't get an hour-long lease (failover
// must stay bounded even when the worker itself runs rarely).
//
// Rule: TTL = clamp(3×interval, 30s, 10min). Refresh fires at
// TTL/3, so a 30s TTL refreshes every 10s; a 10min TTL refreshes
// every ~3min.
func LeaderTTL(interval time.Duration) time.Duration {
	ttl := 3 * interval
	if ttl < 30*time.Second {
		ttl = 30 * time.Second
	}
	if ttl > 10*time.Minute {
		ttl = 10 * time.Minute
	}
	return ttl
}

// NewLeaderHolder returns a holder for the given lock + key.
// ttl is the Redis key TTL while held — the holder refreshes
// at ttl/3. Pick ttl long enough that a pod restart doesn't
// thrash leadership (30-60s is typical) but short enough that
// a crashed pod doesn't block leadership for too long. Use
// LeaderTTL(interval) for a sensible per-worker default.
func NewLeaderHolder(lock Lock, key string, ttl time.Duration) *LeaderHolder {
	if ttl < 3*time.Second {
		ttl = 30 * time.Second
	}
	return &LeaderHolder{
		lock:    lock,
		key:     key,
		ttl:     ttl,
		refresh: ttl / 3,
	}
}

// Start launches the heartbeat goroutine. Safe to call once;
// repeated calls are a no-op (sync.Once). Caller is expected
// to hold the goroutine open via ctx — typically the same ctx
// driving the rest of the background workers.
func (h *LeaderHolder) Start(ctx context.Context) {
	h.startOnce.Do(func() {
		go h.run(ctx)
	})
}

// IsLeader returns true when this pod currently holds the lock.
// Cheap (atomic load) — safe to call in tick hot paths.
func (h *LeaderHolder) IsLeader() bool {
	return h.held.Load()
}

// refreshOutlivedLease is the pure demote decision — v0.8.341 (H4). True
// once the time since the last SUCCESSFUL refresh reaches the lease TTL:
// the PEXPIRE from that refresh has elapsed server-side, the key is gone,
// and a peer may have legitimately acquired it. `>=` not `>` — at exactly
// ttl the lease is already expired. Below the TTL we tolerate errors (the
// lock might still be ours; demoting early would thrash leadership on
// every Redis blip, the exact failure the 30s TTL floor exists to absorb).
func refreshOutlivedLease(sinceLastOK, ttl time.Duration) bool {
	return sinceLastOK >= ttl
}

func (h *LeaderHolder) run(ctx context.Context) {
	// On exit, release the lock so the next pod can become
	// leader without waiting for TTL expiry.
	defer func() {
		if h.held.Load() {
			// Best-effort — ctx may be done so use a short
			// fresh context for the release call.
			rctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = h.lock.Release(rctx, h.key)
			h.held.Store(false)
		}
	}()

	// Outer loop: acquire → hold → (demoted | lost) → acquire again.
	// v0.8.341 restructure: pre-fix, the refresh loop `continue`d on EVERY
	// Refresh error, so a leader that lost Redis (partition / AUTH flap)
	// while ClickHouse stayed up kept held=true FOREVER — its lease expired,
	// a peer acquired it, and two evaluators ran concurrently (duplicate
	// Problem rows + duplicate pages). Now error streaks are bounded by the
	// lease TTL, after which we demote and drop back here.
	for {
		// Acquire loop — retry until success or ctx done. Non-leader
		// pods sit here until the current leader dies or demotes.
		for {
			ok, err := h.lock.TryAcquire(ctx, h.key, h.ttl)
			if err == nil && ok {
				h.held.Store(true)
				log.Printf("[leader] became leader for %s (ttl=%s)", h.key, h.ttl)
				break
			}
			// Either another pod holds it (ok=false) OR network
			// blip (err != nil). Wait + retry.
			select {
			case <-ctx.Done():
				return
			case <-time.After(h.refresh):
			}
		}

		// Refresh loop — extend the TTL every `refresh`. lastOK is the
		// last instant we KNOW the lease was extended (acquire counts:
		// SetNX set the TTL). A successful refresh resets the clock.
		lastOK := time.Now()
	hold:
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(h.refresh):
			}
			ok, err := h.lock.Refresh(ctx, h.key, h.ttl)
			switch {
			case err == nil && ok:
				lastOK = time.Now()
			case err != nil:
				// Refresh at ttl/3 → up to ~3 consecutive failures
				// before the streak reaches the TTL and we demote —
				// short blips never drop leadership.
				since := time.Since(lastOK)
				if refreshOutlivedLease(since, h.ttl) {
					// v0.8.341 (H4) — DEMOTE. Our lease is expired in
					// Redis; a peer may already be leader. Keeping
					// held=true here is the split-brain: two pods
					// running the same worker.
					h.held.Store(false)
					log.Printf("[leader] DEMOTED %s — refresh failures outlived the lease "+
						"(ttl=%s, last successful refresh %s ago; last error: %v). "+
						"A peer may hold leadership now; falling back to acquire loop.",
						h.key, h.ttl, since.Round(time.Second), err)
					break hold
				}
				log.Printf("[leader] refresh %s failed (%v) — still within lease, "+
					"demoting in ≤%s unless a refresh lands",
					h.key, err, (h.ttl - since).Round(time.Second))
			default:
				// ok=false, err=nil: definitive answer from Redis — our
				// token no longer owns the key (lease expired before the
				// refresh landed, or someone else acquired). Drop
				// IsLeader and re-enter the acquire loop.
				h.held.Store(false)
				log.Printf("[leader] lost leadership for %s, re-entering acquire loop", h.key)
				break hold
			}
		}
	}
}
