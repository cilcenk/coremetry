package api

// v0.8.350 (HA 🟡5) regression test — serveCached's MISS path must NOT
// wait on the L2 (Redis) Set before writing the response. Pre-fix the
// miss path called storeCached synchronously: with a blackholed Redis
// every cold read paid the client dial stall a second time AFTER the
// upstream query had already produced the body. Contract pinned here,
// timing-free via channels:
//   - serveCached returns (response fully written) while the fake L2
//     Set is still blocked
//   - L1 is populated synchronously (same-node burst coalescing)
//   - the detached L2 write still happens with the right key

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// blockingSetCache's Set parks until the test releases it, so the test
// can prove the response never waited on it.
type blockingSetCache struct {
	fakeCache
	setEntered chan string   // receives the key when Set is invoked
	release    chan struct{} // test closes it to let Set finish
	setDone    chan struct{} // closed when Set returns
}

func (c *blockingSetCache) Set(_ context.Context, key string, _ []byte, _ time.Duration) error {
	c.setEntered <- key
	<-c.release
	close(c.setDone)
	return nil
}

func TestServeCachedMiss_ResponseDoesNotWaitOnL2Set(t *testing.T) {
	fc := &blockingSetCache{
		setEntered: make(chan string, 1),
		release:    make(chan struct{}),
		setDone:    make(chan struct{}),
	}
	s := &Server{cache: fc, l1: newL1Cache(8), stats: newCacheStats()}

	const key = "async-set-key"
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/test", nil)

	served := make(chan struct{})
	go func() {
		s.serveCached(w, r, key, time.Second, func(ctx context.Context) (any, error) {
			return map[string]any{"ok": true}, nil
		})
		close(served)
	}()

	// Ordering proof: serveCached must return while release is still
	// open, i.e. while the L2 Set (if it has even started) is parked.
	// The 5s guard is liveness-only — a regression to a synchronous Set
	// deadlocks here deterministically, it can never pass by racing.
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("serveCached blocked on the L2 Set — miss-path Redis write is synchronous again (v0.8.350 regression)")
	}

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Errorf("body = %q, want the upstream payload", w.Body.String())
	}
	if w.Header().Get("X-Cache") != "MISS" {
		t.Errorf("X-Cache = %q, want MISS", w.Header().Get("X-Cache"))
	}
	// L1 must be populated synchronously — a burst of same-node reads
	// right behind this one must not stampede the upstream.
	if _, ok := s.l1.get(key); !ok {
		t.Error("L1 was not set synchronously on the miss path")
	}

	// The detached L2 write must still happen — async, not dropped.
	select {
	case got := <-fc.setEntered:
		if got != key {
			t.Errorf("L2 Set key = %q, want %q", got, key)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("async L2 Set was never invoked — warm entries would stop reaching Redis")
	}
	close(fc.release)
	select {
	case <-fc.setDone:
	case <-time.After(5 * time.Second):
		t.Fatal("L2 Set never completed after release")
	}
}
