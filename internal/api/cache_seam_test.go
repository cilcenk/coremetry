package api

// v0.10.146 — cachedJSON: serveCached'in HTTP'siz çekirdeği. Katman
// sözleşmesi (MISS → HIT-L1 → HIT(L2) → STALE + arka plan tazeleme →
// BYPASS) burada pinlenir; serveCached artık bunun ince sarmalayıcısı,
// bu yüzden bu test her iki tüketiciyi (HTTP uçları + dashboard bundle)
// birden korur.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// seamMemCache — L2'yi gerçekten saklayan fake (fakeCache'in Get'i hep miss).
type seamMemCache struct {
	fakeCache
	mu sync.Mutex
	m  map[string][]byte
}

func (c *seamMemCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[key]
	return v, ok, nil
}

func (c *seamMemCache) Set(_ context.Context, key string, val []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string][]byte{}
	}
	c.m[key] = append([]byte(nil), val...)
	return nil
}

func (c *seamMemCache) has(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.m[key]
	return ok
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestCachedJSON_TierLadder(t *testing.T) {
	mc := &seamMemCache{}
	s := &Server{cache: mc, l1: newL1Cache(8), stats: newCacheStats()}
	const key = "seam-key"
	const ttl = 400 * time.Millisecond
	var calls atomic.Int32
	fn := func(ctx context.Context) (any, error) {
		return map[string]any{"n": calls.Add(1)}, nil
	}
	ctx := context.Background()

	// 1. Soğuk → MISS, fn bir kez.
	body, tier, err := s.cachedJSON(ctx, key, ttl, false, fn)
	if err != nil || tier != "MISS" || string(body) != `{"n":1}` {
		t.Fatalf("cold: tier=%s body=%s err=%v", tier, body, err)
	}
	// 2. Hemen ardından → HIT-L1, fn çağrılmaz.
	body, tier, _ = s.cachedJSON(ctx, key, ttl, false, fn)
	if tier != "HIT-L1" || string(body) != `{"n":1}` || calls.Load() != 1 {
		t.Fatalf("warm: tier=%s body=%s calls=%d", tier, body, calls.Load())
	}
	// 3. L2 yazımı ayrık (v0.8.350) — gelmesini bekle, L1'i düşür → HIT (L2, taze).
	waitFor(t, "async L2 set", func() bool { return mc.has(key) })
	s.l1.del(key)
	body, tier, _ = s.cachedJSON(ctx, key, ttl, false, fn)
	if tier != "HIT" || string(body) != `{"n":1}` || calls.Load() != 1 {
		t.Fatalf("l2 fresh: tier=%s body=%s calls=%d", tier, body, calls.Load())
	}
	// 4. TTL geçti, SWR penceresi içinde → STALE: eski gövde hemen döner,
	//    fn arka planda bir kez daha koşar ve L2 yenilenir.
	time.Sleep(ttl + 50*time.Millisecond)
	s.l1.del(key)
	body, tier, _ = s.cachedJSON(ctx, key, ttl, false, fn)
	if tier != "STALE" || string(body) != `{"n":1}` {
		t.Fatalf("stale: tier=%s body=%s", tier, body)
	}
	waitFor(t, "background refresh", func() bool { return calls.Load() == 2 })
	waitFor(t, "refreshed L2 body", func() bool {
		raw, ok, _ := mc.Get(ctx, key)
		if !ok {
			return false
		}
		_, b, envOK := unwrapEnvelope(raw)
		return envOK && string(b) == `{"n":2}`
	})
	// 5. bypass (?refresh=1) → okuma katmanları atlanır, fn koşar, tier BYPASS.
	body, tier, _ = s.cachedJSON(ctx, key, ttl, true, fn)
	if tier != "BYPASS" || string(body) != `{"n":3}` {
		t.Fatalf("bypass: tier=%s body=%s", tier, body)
	}
	// bypass sonucu da yazılır: sıradaki okuma L1'den yeni gövdeyi görür.
	body, tier, _ = s.cachedJSON(ctx, key, ttl, false, fn)
	if tier != "HIT-L1" || string(body) != `{"n":3}` {
		t.Fatalf("after bypass: tier=%s body=%s", tier, body)
	}
}

func TestCachedJSON_ErrorIsNotCached(t *testing.T) {
	mc := &seamMemCache{}
	s := &Server{cache: mc, l1: newL1Cache(8), stats: newCacheStats()}
	const key = "seam-err"
	var calls atomic.Int32
	fn := func(ctx context.Context) (any, error) {
		if calls.Add(1) == 1 {
			return nil, context.DeadlineExceeded
		}
		return map[string]any{"ok": true}, nil
	}
	if _, _, err := s.cachedJSON(context.Background(), key, time.Second, false, fn); err == nil {
		t.Fatal("first call must surface the upstream error")
	}
	if _, ok := s.l1.get(key); ok {
		t.Fatal("an error must not populate L1")
	}
	body, tier, err := s.cachedJSON(context.Background(), key, time.Second, false, fn)
	if err != nil || tier != "MISS" || string(body) != `{"ok":true}` {
		t.Fatalf("retry after error: tier=%s body=%s err=%v", tier, body, err)
	}
}
