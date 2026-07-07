package cache

import (
	"context"
	"log"
	"math/rand"
	"sync/atomic"
	"time"
)

// SwitchableCache / SwitchableLock wrap the live Cache/Lock behind an
// atomic.Pointer so main.go can swap the Noop boot-fallback for the real
// Redis impl at runtime without a pod restart — v0.8.341 (H4). Mirrors
// logstore.Switchable (internal/logstore/switchable.go), which does the
// same for the logs read backend.
//
// Why: pre-v0.8.341, cache.New pinged Redis ONCE at boot (3s); on failure
// main wired the Noop cache + Noop always-leader lock for the pod's
// LIFETIME. After Redis recovered, N pods all stayed "leader" forever —
// duplicate evaluators / notifications / retention sweeps — and the L2
// cache + cross-pod SSE bridge stayed dead. Every consumer (LeaderHolders,
// api.Server, sse bridge, cluster membership) captures its Cache/Lock at
// construction, so the fix is a stable wrapper they can all hold while the
// inner impl is hot-swapped by the background re-probe (StartRedisReprobe).
//
// Wired ALWAYS — healthy boots wrap the real impl from the start, so the
// only cost on the happy path is one atomic pointer load per call.
type SwitchableCache struct {
	inner atomic.Pointer[Cache]
}

func NewSwitchableCache(c Cache) *SwitchableCache {
	s := &SwitchableCache{}
	s.inner.Store(&c)
	return s
}

// Current returns the live inner cache. Callers must not hold it across
// requests — that would defeat the swap.
func (s *SwitchableCache) Current() Cache { return *s.inner.Load() }

// Swap atomically replaces the inner cache. In-flight calls finish on the
// impl they started with; nil is ignored (a failed probe must never leave
// consumers with a nil cache — same rule as logstore.Switchable.Swap).
func (s *SwitchableCache) Swap(c Cache) {
	if c == nil {
		return
	}
	s.inner.Store(&c)
}

// ── Cache interface forwarding ──────────────────────────────────────

func (s *SwitchableCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	return s.Current().Get(ctx, key)
}

func (s *SwitchableCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return s.Current().Set(ctx, key, value, ttl)
}

func (s *SwitchableCache) SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	return s.Current().SetNX(ctx, key, value, ttl)
}

func (s *SwitchableCache) Del(ctx context.Context, key string) error {
	return s.Current().Del(ctx, key)
}

func (s *SwitchableCache) ScanPrefix(ctx context.Context, prefix string) ([][]byte, error) {
	return s.Current().ScanPrefix(ctx, prefix)
}

func (s *SwitchableCache) DelPrefix(ctx context.Context, prefix string) error {
	return s.Current().DelPrefix(ctx, prefix)
}

func (s *SwitchableCache) Ping(ctx context.Context) error {
	return s.Current().Ping(ctx)
}

func (s *SwitchableCache) Stats(ctx context.Context) (RedisStats, error) {
	return s.Current().Stats(ctx)
}

func (s *SwitchableCache) Publish(ctx context.Context, channel string, msg []byte) error {
	return s.Current().Publish(ctx, channel, msg)
}

// Subscribe binds to the inner impl live at CALL time — a subscription
// opened while the inner is Noop stays on the Noop channel after a swap
// (the Noop chan only closes on ctx cancel). main.go's recovery callback
// therefore re-kicks the two boot-time subscribers (SSE bridge + L1
// invalidation) so they re-subscribe against the real Redis.
func (s *SwitchableCache) Subscribe(ctx context.Context, channel string) (<-chan []byte, error) {
	return s.Current().Subscribe(ctx, channel)
}

// SwitchableLock — see SwitchableCache. LeaderHolder binds its Lock at
// construction (NewLeaderHolder captures it), so holders wired over a
// SwitchableLock transparently start hitting real Redis on their next
// heartbeat after a Swap — no reconstruction, no restart.
type SwitchableLock struct {
	inner atomic.Pointer[Lock]
}

func NewSwitchableLock(l Lock) *SwitchableLock {
	s := &SwitchableLock{}
	s.inner.Store(&l)
	return s
}

// Current returns the live inner lock.
func (s *SwitchableLock) Current() Lock { return *s.inner.Load() }

// Swap atomically replaces the inner lock; nil is ignored.
//
// Convergence window — v0.8.341 (H4): while degraded, every pod's Noop
// lock said "always leader" (unchanged single-instance semantics). After
// the swap, each holder's next heartbeat calls Refresh on the REAL
// redisLock, which has no token registered for the key → returns
// (false, nil) → the holder drops held and races TryAcquire (SetNX)
// against its peers. Exactly one pod wins; the fleet converges from
// N leaders to 1 within one refresh cadence (ttl/3, ≤ ~3min worst case
// for the 10min TTL cap).
func (s *SwitchableLock) Swap(l Lock) {
	if l == nil {
		return
	}
	s.inner.Store(&l)
}

// ── Lock interface forwarding ───────────────────────────────────────

func (s *SwitchableLock) TryAcquire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	return s.Current().TryAcquire(ctx, key, ttl)
}

func (s *SwitchableLock) Release(ctx context.Context, key string) error {
	return s.Current().Release(ctx, key)
}

func (s *SwitchableLock) Refresh(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	return s.Current().Refresh(ctx, key, ttl)
}

// StartRedisReprobe launches the boot-failure recovery goroutine —
// v0.8.341 (H4). Started from main.go ONLY when Redis was configured but
// the boot ping failed (the lockDegraded state, v0.8.212): retries
// `connect` every `interval` (jittered ±20% so a fleet that lost Redis
// together doesn't stampede it on recovery); on the first success it swaps
// the real impls into cs/ls, fires onRecovered (clears the /admin/stats
// lockDegraded flag + re-kicks pub/sub subscribers), and STOPS — the probe
// exists to end the degraded boot state once, not to health-check Redis
// forever (steady-state failures surface per-call as before).
//
// connect is injected (rather than calling New(url) directly) so the
// swap-once semantics are unit-testable without a Redis.
func StartRedisReprobe(ctx context.Context, interval time.Duration, connect func() (Cache, Lock, error), cs *SwitchableCache, ls *SwitchableLock, onRecovered func()) {
	go func() {
		for attempt := 1; ; attempt++ {
			// Jitter ±20%: interval*(0.8 .. 1.2).
			d := time.Duration(float64(interval) * (0.8 + 0.4*rand.Float64()))
			select {
			case <-ctx.Done():
				return
			case <-time.After(d):
			}
			c, l, err := connect()
			if err != nil {
				// Keep the incident timeline readable without spamming:
				// one line every 20 attempts (~5min at the 15s cadence).
				if attempt%20 == 1 {
					log.Printf("[cache] redis re-probe attempt %d failed (still degraded, retrying every ~%s): %v",
						attempt, interval, err)
				}
				continue
			}
			cs.Swap(c)
			ls.Swap(l)
			log.Printf("[cache] REDIS RECOVERED after %d probe attempts — real cache + distributed "+
				"leader lock swapped in. Leader convergence: pods that ran always-leader during the "+
				"outage will demote on their next heartbeat and race for the real lock; expect "+
				"exactly one leader per worker within one refresh cadence.", attempt)
			if onRecovered != nil {
				onRecovered()
			}
			return
		}
	}()
}
