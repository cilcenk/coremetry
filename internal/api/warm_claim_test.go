package api

// v0.8.350 (HA 🟡4) regression tests — warmDependenciesCache's cross-pod
// SETNX claim. On a multi-pod fleet every api pod used to run the heavy
// /databases + /messaging GROUP-BY warm set every 25s; the claim elects
// ONE warmer per window. Pinned semantics:
//   - first claimant wins, a second claim inside the TTL loses (skip cycle)
//   - Noop cache (no Redis configured) ALWAYS claims — a single-instance
//     deployment has no shared L2 and must keep warming itself
//   - Redis errors fail OPEN (claim granted) — a pod that can't reach
//     Redis has no shared L2 either, so skipping would leave it cold

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
)

// claimCache layers real SETNX first-wins semantics (TTL ignored — the
// tests never need expiry) over the no-op fakeCache from
// cache_invalidate_test.go.
type claimCache struct {
	fakeCache
	mu      sync.Mutex
	held    map[string]bool
	lastKey string
	err     error
}

func (c *claimCache) SetNX(_ context.Context, key string, _ []byte, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastKey = key
	if c.err != nil {
		return false, c.err
	}
	if c.held == nil {
		c.held = map[string]bool{}
	}
	if c.held[key] {
		return false, nil
	}
	c.held[key] = true
	return true, nil
}

func TestWarmClaim_FirstWinsSecondSkips(t *testing.T) {
	c := &claimCache{}
	if !warmClaim(context.Background(), c, 25*time.Second) {
		t.Fatal("first claim must win the warm cycle")
	}
	if warmClaim(context.Background(), c, 25*time.Second) {
		t.Fatal("second claim inside the TTL must lose — both pods would run the heavy GROUP-BY warm set")
	}
	if c.lastKey != warmDepsClaimKey {
		t.Errorf("claim key = %q, want %q", c.lastKey, warmDepsClaimKey)
	}
}

func TestWarmClaim_NoopAlwaysClaims(t *testing.T) {
	noop, _ := cache.NewNoop()
	for i := 0; i < 3; i++ {
		if !warmClaim(context.Background(), noop, 25*time.Second) {
			t.Fatalf("Noop claim #%d lost — a single-instance pod would stop warming its own L1", i+1)
		}
	}
}

func TestWarmClaim_RedisErrorFailsOpen(t *testing.T) {
	c := &claimCache{err: errors.New("dial tcp: i/o timeout")}
	if !warmClaim(context.Background(), c, 25*time.Second) {
		t.Fatal("Redis error must fail OPEN — without Redis there is no shared L2, the pod must warm its own L1")
	}
}
