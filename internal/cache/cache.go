// Package cache provides a thin abstraction over Redis (or none) for two
// scaling primitives the rest of Coremetry depends on:
//
//   - Cache: short-TTL hot read cache, used by API handlers in front of
//     ClickHouse for endpoints that get polled at high frequency.
//   - Lock: distributed lock with token-based release, used by background
//     workers (evaluator, anomaly detector) so multiple Coremetry replicas
//     don't all run the same scheduled work.
//
// Both have a Noop fallback so the binary keeps running unchanged when
// Redis is not configured (single-instance dev / hobby deployments).
package cache

import (
	"context"
	"time"
)

// Cache is a read-through cache. Get returns ok=true on a hit; ok=false on
// a miss is NOT an error.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Del(ctx context.Context, key string) error
	// ScanPrefix returns every value whose key matches the given
	// prefix (Redis SCAN MATCH + MGET). Returned values are the
	// stored bytes; expired / missing keys silently drop out.
	// Used by the cluster membership service (v0.5.253) to list
	// every live pod's heartbeat without exposing the raw Redis
	// client through the abstraction. Noop returns (nil, nil) so
	// single-instance dev falls back to a 1-member view.
	ScanPrefix(ctx context.Context, prefix string) ([][]byte, error)
	// Ping reports liveness of the underlying cache. Noop returns nil
	// (treats cache-disabled mode as healthy — there's no remote to be
	// down). Used by the status page.
	Ping(ctx context.Context) error
	// Stats returns Redis INFO + DBSIZE for the System page admin
	// view. Noop returns a zero-valued struct so callers can render
	// "Redis not configured" without error handling.
	Stats(ctx context.Context) (RedisStats, error)
	// Publish broadcasts msg on a Redis pub/sub channel. Used by
	// the cross-pod L1 cache invalidation flow (v0.5.337):
	// a mutating endpoint DELs L2 + publishes the key, every
	// pod's Subscribe loop receives the key and evicts L1. Noop
	// returns nil — single-instance pods have no peers to notify.
	Publish(ctx context.Context, channel string, msg []byte) error
	// Subscribe returns a channel of incoming pub/sub messages
	// for the given channel name. The returned chan closes when
	// ctx is cancelled. Noop returns a chan that never delivers
	// (and closes on ctx cancellation), so the caller's
	// invalidation goroutine sits idle without busy-looping.
	Subscribe(ctx context.Context, channel string) (<-chan []byte, error)
}

// Lock is a best-effort distributed lock. TryAcquire returns ok=false
// (without error) when the lock is already held — callers should skip
// their work, not retry. Release is safe to call from any code path
// (defer-friendly).
type Lock interface {
	TryAcquire(ctx context.Context, key string, ttl time.Duration) (ok bool, err error)
	Release(ctx context.Context, key string) error
	// v0.5.426 — Refresh extends the TTL on a key WE already hold.
	// Returns false when our token no longer owns the key (i.e.
	// we lost leadership — lease expired and someone else acquired).
	// Used by LeaderHolder to keep long-lived leadership without
	// the release+reacquire race window of the prior per-tick
	// pattern.
	Refresh(ctx context.Context, key string, ttl time.Duration) (ok bool, err error)
}

// ── Noop implementations (single-instance / cache-disabled mode) ────────────

type noopCache struct{}
type noopLock struct{}

// NewNoop returns a Cache+Lock pair that does nothing for cache and always
// grants the lock. Used when Redis is not configured.
func NewNoop() (Cache, Lock) { return noopCache{}, noopLock{} }

func (noopCache) Get(context.Context, string) ([]byte, bool, error)   { return nil, false, nil }
func (noopCache) Set(context.Context, string, []byte, time.Duration) error { return nil }
func (noopCache) Del(context.Context, string) error                   { return nil }
func (noopCache) ScanPrefix(context.Context, string) ([][]byte, error) { return nil, nil }
func (noopCache) Ping(context.Context) error                          { return nil }
func (noopCache) Publish(context.Context, string, []byte) error       { return nil }
func (noopCache) Subscribe(ctx context.Context, _ string) (<-chan []byte, error) {
	// Idle channel that closes on ctx cancellation. The
	// invalidation loop sits in a select on this — without the
	// close on cancel, the goroutine would leak on graceful
	// shutdown.
	ch := make(chan []byte)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

// Noop lock = always-leader. Correct for single-instance deployments
// because there's no one to contend with.
func (noopLock) TryAcquire(context.Context, string, time.Duration) (bool, error) { return true, nil }
func (noopLock) Release(context.Context, string) error                          { return nil }
func (noopLock) Refresh(context.Context, string, time.Duration) (bool, error)    { return true, nil }
