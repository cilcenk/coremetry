// Package cache provides a thin abstraction over Redis (or none) for two
// scaling primitives the rest of Qmetry depends on:
//
//   - Cache: short-TTL hot read cache, used by API handlers in front of
//     ClickHouse for endpoints that get polled at high frequency.
//   - Lock: distributed lock with token-based release, used by background
//     workers (evaluator, anomaly detector) so multiple Qmetry replicas
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
}

// Lock is a best-effort distributed lock. TryAcquire returns ok=false
// (without error) when the lock is already held — callers should skip
// their work, not retry. Release is safe to call from any code path
// (defer-friendly).
type Lock interface {
	TryAcquire(ctx context.Context, key string, ttl time.Duration) (ok bool, err error)
	Release(ctx context.Context, key string) error
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

// Noop lock = always-leader. Correct for single-instance deployments
// because there's no one to contend with.
func (noopLock) TryAcquire(context.Context, string, time.Duration) (bool, error) { return true, nil }
func (noopLock) Release(context.Context, string) error                          { return nil }
