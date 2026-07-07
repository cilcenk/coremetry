package cache

// v0.8.341 — H4(B) regression tests: Redis down at boot must not condemn the
// pod to a permanent Noop (always-leader) cache+lock. SwitchableCache /
// SwitchableLock give main.go a stable pointer it can hot-swap when the
// background re-probe reconnects; every consumer that captured the wrapper
// at construction transparently starts using the real impl.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// stubCache overrides Get on the Noop base so tests can tell which inner
// impl a SwitchableCache is currently routing to.
type stubCache struct {
	noopCache
	val string
}

func (s stubCache) Get(context.Context, string) ([]byte, bool, error) {
	return []byte(s.val), true, nil
}

func TestSwitchableCacheSwapRoutesToNewImpl(t *testing.T) {
	sw := NewSwitchableCache(stubCache{val: "noop-era"})
	if b, ok, _ := sw.Get(context.Background(), "k"); !ok || string(b) != "noop-era" {
		t.Fatalf("pre-swap Get = %q ok=%v, want noop-era", b, ok)
	}
	sw.Swap(stubCache{val: "redis-era"})
	if b, ok, _ := sw.Get(context.Background(), "k"); !ok || string(b) != "redis-era" {
		t.Fatalf("post-swap Get = %q ok=%v, want redis-era — swap not visible through wrapper", b, ok)
	}
	// nil Swap must be ignored — a failed rebuild must never leave consumers
	// with a nil inner (mirrors logstore.Switchable semantics).
	sw.Swap(nil)
	if b, _, _ := sw.Get(context.Background(), "k"); string(b) != "redis-era" {
		t.Fatal("Swap(nil) replaced the inner cache")
	}
}

func TestSwitchableLockSwapRoutesToNewImpl(t *testing.T) {
	a := &scriptLock{acquireOK: false}
	b := &scriptLock{acquireOK: true}
	sw := NewSwitchableLock(a)

	if ok, _ := sw.TryAcquire(context.Background(), "k", time.Second); ok {
		t.Fatal("deny-all inner granted the lock")
	}
	sw.Swap(b)
	if ok, _ := sw.TryAcquire(context.Background(), "k", time.Second); !ok {
		t.Fatal("post-swap TryAcquire still routed to old inner")
	}
	a.mu.Lock()
	aAcquires := a.acquires
	a.mu.Unlock()
	b.mu.Lock()
	bAcquires := b.acquires
	b.mu.Unlock()
	if aAcquires != 1 || bAcquires != 1 {
		t.Fatalf("routing counts a=%d b=%d, want 1/1", aAcquires, bAcquires)
	}
	sw.Swap(nil)
	if ok, _ := sw.TryAcquire(context.Background(), "k", time.Second); !ok {
		t.Fatal("Swap(nil) replaced the inner lock")
	}
}

// TestLeaderHolderSeesSwappedLockWithoutReconstruction — the H4(B) critical
// semantic: LeaderHolder captures its Lock at construction, so wiring it over
// a SwitchableLock must make a later Swap transparent. A holder built while
// the inner is deny-all (stand-in for "peer holds it") becomes leader after
// Swap flips the inner to an acquirable lock — no reconstruction.
func TestLeaderHolderSeesSwappedLockWithoutReconstruction(t *testing.T) {
	deny := &scriptLock{acquireOK: false}
	sw := NewSwitchableLock(deny)
	h := &LeaderHolder{lock: sw, key: "test-switch", ttl: 300 * time.Millisecond, refresh: 50 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Start(ctx)

	// A few acquire cadences on the deny-all inner: never leader.
	time.Sleep(150 * time.Millisecond)
	if h.IsLeader() {
		t.Fatal("became leader on deny-all inner")
	}

	sw.Swap(&scriptLock{acquireOK: true, refreshOK: true})
	waitFor(t, 2*time.Second, h.IsLeader,
		"holder never picked up the swapped-in lock — construction-time capture defeated the swap")
}

// TestReprobeSwapsOnceThenStops — the boot-failure recovery goroutine must
// retry until one successful connect, swap exactly once, fire onRecovered
// exactly once, and then stop probing (no periodic churn after recovery).
func TestReprobeSwapsOnceThenStops(t *testing.T) {
	cs := NewSwitchableCache(noopCache{})
	ls := NewSwitchableLock(noopLock{})

	var connects atomic.Int32
	var recovered atomic.Int32
	connect := func() (Cache, Lock, error) {
		if connects.Add(1) < 3 {
			return nil, nil, errors.New("redis ping: connection refused")
		}
		return stubCache{val: "real"}, &scriptLock{acquireOK: true, refreshOK: true}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartRedisReprobe(ctx, 5*time.Millisecond, connect, cs, ls, func() { recovered.Add(1) })

	waitFor(t, 2*time.Second, func() bool { return recovered.Load() == 1 }, "onRecovered never fired")

	// Swap landed: the wrapper now routes to the real impls.
	if b, ok, _ := cs.Get(context.Background(), "k"); !ok || string(b) != "real" {
		t.Fatalf("cache not swapped after recovery: %q ok=%v", b, ok)
	}
	if ok, _ := ls.TryAcquire(context.Background(), "k", time.Second); !ok {
		t.Fatal("lock not swapped after recovery")
	}

	// Probing stopped: connect count is frozen after success.
	n := connects.Load()
	time.Sleep(60 * time.Millisecond) // ≥ 10 intervals
	if got := connects.Load(); got != n {
		t.Fatalf("probe kept running after recovery: connects %d → %d", n, got)
	}
	if recovered.Load() != 1 {
		t.Fatalf("onRecovered fired %d times, want exactly 1", recovered.Load())
	}
}
