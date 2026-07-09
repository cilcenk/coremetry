// v0.8.354 — HA audit 🟡#2 regression tests: the ForSec sustain clock
// (breachSince) and CooldownSec gate (lastResolved) lived only in
// per-pod memory, so every leader failover / rolling deploy restarted
// the sustain clock from zero (alert delayed another full ForSec) and
// punched the cooldown (flap re-open + duplicate page). The stamps are
// now write-through mirrored to Redis and lazily hydrated on an
// in-memory miss; a fresh Evaluator instance sharing the same cache
// stands in for the newly-elected leader.
package evaluator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
)

// fakeStampCache is an in-memory cache.Cache standing in for Redis.
// Records the TTL of every Set and counts Dels so the tests can assert
// the 2×window/10m-floor sizing and the mirror-delete IO discipline.
type fakeStampCache struct {
	mu   sync.Mutex
	vals map[string][]byte
	ttls map[string]time.Duration
	dels int
}

func newFakeStampCache() *fakeStampCache {
	return &fakeStampCache{vals: map[string][]byte{}, ttls: map[string]time.Duration{}}
}

func (f *fakeStampCache) Get(_ context.Context, key string) ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.vals[key]
	return v, ok, nil
}
func (f *fakeStampCache) Set(_ context.Context, key string, v []byte, ttl time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vals[key] = v
	f.ttls[key] = ttl
	return nil
}
func (f *fakeStampCache) SetNX(_ context.Context, key string, v []byte, ttl time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.vals[key]; ok {
		return false, nil
	}
	f.vals[key] = v
	f.ttls[key] = ttl
	return true, nil
}
func (f *fakeStampCache) Del(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.vals, key)
	delete(f.ttls, key)
	f.dels++
	return nil
}
func (f *fakeStampCache) MGet(_ context.Context, keys []string) ([][]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(keys))
	for i, k := range keys {
		out[i] = f.vals[k]
	}
	return out, nil
}
func (f *fakeStampCache) ScanPrefix(context.Context, string) ([][]byte, error) { return nil, nil }
func (f *fakeStampCache) DelPrefix(context.Context, string) error              { return nil }
func (f *fakeStampCache) Ping(context.Context) error                           { return nil }
func (f *fakeStampCache) Stats(context.Context) (cache.RedisStats, error) {
	return cache.RedisStats{}, nil
}
func (f *fakeStampCache) Publish(context.Context, string, []byte) error { return nil }
func (f *fakeStampCache) Subscribe(ctx context.Context, _ string) (<-chan []byte, error) {
	ch := make(chan []byte)
	go func() { <-ctx.Done(); close(ch) }()
	return ch, nil
}

func (f *fakeStampCache) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.vals[key]
	return ok
}
func (f *fakeStampCache) ttl(key string) (time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.ttls[key]
	return d, ok
}
func (f *fakeStampCache) delCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dels
}

// errStampCache fails every IO op — the "Redis is down mid-lease"
// degrade case. Embeds the fake so the interface stays satisfied.
type errStampCache struct{ fakeStampCache }

func (e *errStampCache) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, errors.New("redis down")
}
func (e *errStampCache) Set(context.Context, string, []byte, time.Duration) error {
	return errors.New("redis down")
}
func (e *errStampCache) Del(context.Context, string) error { return errors.New("redis down") }

// newStampEvaluator builds an Evaluator the way a fresh pod would —
// empty in-memory maps — and wires the given cache like main.go's
// SetStampCache call. Store/notifier stay nil: these tests exercise
// only the stamp helpers.
func newStampEvaluator(t *testing.T, c cache.Cache) *Evaluator {
	t.Helper()
	_, lock := cache.NewNoop()
	e := New(nil, time.Minute, lock, nil)
	if c != nil {
		e.SetStampCache(c)
	}
	return e
}

// TestStampKeyDelimiterSafe — ruleID is free-form and can contain ':'
// (the anomaly promoter's rule_id literally does), so a naive colon
// join would alias different (ruleID, service) pairs. The
// length-prefix join must keep the mapping injective.
func TestStampKeyDelimiterSafe(t *testing.T) {
	a := stampKey(stampKindBreach, breachKey{RuleID: "a:b", Service: "c"})
	b := stampKey(stampKindBreach, breachKey{RuleID: "a", Service: "b:c"})
	if a == b {
		t.Fatalf("colliding pairs map to one key %q — naive join regression", a)
	}
	// Same pair → same key (stability across ticks/leaders).
	if a != stampKey(stampKindBreach, breachKey{RuleID: "a:b", Service: "c"}) {
		t.Fatal("stampKey not deterministic for identical input")
	}
	// Breach and resolved namespaces never overlap.
	k := breachKey{RuleID: "r", Service: "s"}
	if stampKey(stampKindBreach, k) == stampKey(stampKindResolved, k) {
		t.Fatal("breach and resolved stamps share a key")
	}
	if got, want := stampKey(stampKindBreach, k), "coremetry:eval:breach:1:r:s"; got != want {
		t.Fatalf("stampKey = %q, want %q", got, want)
	}
}

// TestStampTTL — 2× the gate window, floored at 10 minutes.
func TestStampTTL(t *testing.T) {
	cases := []struct {
		windowSec uint32
		want      time.Duration
	}{
		{0, 10 * time.Minute},    // CooldownSec unset → floor
		{120, 10 * time.Minute},  // builtin critical ForSec → 4m < floor
		{180, 10 * time.Minute},  // builtin warning ForSec → 6m < floor
		{300, 10 * time.Minute},  // 2×300s = exactly the floor
		{600, 20 * time.Minute},  // builtin warning CooldownSec
		{3600, 2 * time.Hour},    // long custom window
	}
	for _, c := range cases {
		if got := stampTTL(c.windowSec); got != c.want {
			t.Errorf("stampTTL(%d) = %v, want %v", c.windowSec, got, c.want)
		}
	}
}

// TestBreachStampSurvivesFailover — the headline fix: a breach 9
// minutes into a 10-minute ForSec window must keep its clock on the
// newly-elected leader instead of restarting from zero.
func TestBreachStampSurvivesFailover(t *testing.T) {
	ctx := context.Background()
	fc := newFakeStampCache()
	key := breachKey{RuleID: "builtin-warn-http-p99-3s", Service: "checkout"}
	const forSec = 600 // 10-minute sustain

	leaderA := newStampEvaluator(t, fc)
	t0 := time.Now().Add(-9 * time.Minute) // breach began 9 min ago
	if _, existing := leaderA.breachStart(ctx, key, t0, forSec); existing {
		t.Fatal("first sighting reported an existing stamp")
	}

	// Leader failover: brand-new Evaluator (empty maps), same Redis.
	leaderB := newStampEvaluator(t, fc)
	now := time.Now()
	first, existing := leaderB.breachStart(ctx, key, now, forSec)
	if !existing {
		t.Fatal("stamp lost across failover — sustain clock restarted from zero")
	}
	if first.UnixNano() != t0.UnixNano() {
		t.Fatalf("hydrated stamp = %v, want the original first-breach %v", first, t0)
	}
	if now.Sub(first) < 8*time.Minute {
		t.Fatalf("sustain elapsed %v — clock did not continue from the persisted stamp", now.Sub(first))
	}

	// Hydration seeds the map: even if Redis loses the key now, the
	// new leader keeps the clock in memory (read ONCE semantics).
	_ = fc.Del(ctx, stampKey(stampKindBreach, key))
	if first2, ok := leaderB.breachStart(ctx, key, time.Now(), forSec); !ok || first2.UnixNano() != t0.UnixNano() {
		t.Fatal("hydrated stamp was not seeded into the in-memory map")
	}
}

// TestCooldownSurvivesFailover — a problem that auto-resolved 1 minute
// before the failover must stay suppressed on the new leader for the
// rest of its CooldownSec, not re-open immediately (flap + dup page).
func TestCooldownSurvivesFailover(t *testing.T) {
	ctx := context.Background()
	fc := newFakeStampCache()
	key := breachKey{RuleID: "builtin-error-rate-15pct", Service: "payments"}

	leaderA := newStampEvaluator(t, fc)
	resolved := time.Now().Add(-1 * time.Minute)
	leaderA.stampResolved(ctx, key, resolved, 300)

	leaderB := newStampEvaluator(t, fc)
	rt, seen := leaderB.resolvedAt(ctx, key)
	if !seen {
		t.Fatal("cooldown stamp lost across failover — immediate re-open possible")
	}
	if rt.UnixNano() != resolved.UnixNano() {
		t.Fatalf("hydrated resolve stamp = %v, want %v", rt, resolved)
	}
	// The gate math the caller applies: 1 min < 300s cooldown ⇒ suppress.
	if since := time.Since(rt); since >= 300*time.Second {
		t.Fatalf("stamp %v old — test setup broken", since)
	}

	// Seeded into memory: Redis can lose the key, the gate holds.
	_ = fc.Del(ctx, stampKey(stampKindResolved, key))
	if _, seen := leaderB.resolvedAt(ctx, key); !seen {
		t.Fatal("hydrated resolve stamp was not seeded into the in-memory map")
	}
}

// TestStampsDegradeToInMemory — Redis miss, Noop cache (SwitchableCache
// pre-reconnect), IO errors, and a never-wired nil cache must all
// reproduce the pre-v0.8.354 in-memory-only semantics exactly.
func TestStampsDegradeToInMemory(t *testing.T) {
	noop, _ := cache.NewNoop()
	caches := map[string]cache.Cache{
		"nil (never wired)": nil,
		"noop (no redis)":   noop,
		"empty redis":       newFakeStampCache(),
		"erroring redis":    &errStampCache{},
	}
	for name, c := range caches {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			e := newStampEvaluator(t, c)
			key := breachKey{RuleID: "r1", Service: "svc"}
			now := time.Now()

			// First sighting: no stamp anywhere → fresh clock.
			if _, existing := e.breachStart(ctx, key, now, 120); existing {
				t.Fatal("fresh clock expected on first sighting")
			}
			// Same instance, second look: memory serves it (old behavior).
			if first, existing := e.breachStart(ctx, key, time.Now(), 120); !existing || first.UnixNano() != now.UnixNano() {
				t.Fatal("in-memory stamp lost between calls")
			}
			// Cooldown: never resolved → no suppression.
			if _, seen := e.resolvedAt(ctx, key); seen {
				t.Fatal("phantom resolve stamp")
			}
			// Stamp + read back from memory despite dead mirror.
			e.stampResolved(ctx, key, now, 300)
			if rt, seen := e.resolvedAt(ctx, key); !seen || rt.UnixNano() != now.UnixNano() {
				t.Fatal("in-memory resolve stamp lost")
			}
			// Clears never error/block.
			e.clearBreach(ctx, key)
			if _, existing := e.breachStart(ctx, key, time.Now(), 120); existing {
				t.Fatal("clearBreach left the in-memory stamp")
			}
			e.clearResolved(ctx, key)
			if _, seen := e.resolvedAt(ctx, key); seen {
				t.Fatal("clearResolved left the in-memory stamp")
			}
		})
	}
}

// TestClearDeletesMirrorKey — clearing a stamp this leader holds must
// delete the Redis mirror too; a stamp only in Redis (previous
// leader's) is NOT swept by clearBreach (no per-pair DEL storm — the
// !breached branch runs for every healthy pair every tick), while
// clearResolved deletes unconditionally (bounded stale sweep).
func TestClearDeletesMirrorKey(t *testing.T) {
	ctx := context.Background()
	fc := newFakeStampCache()
	e := newStampEvaluator(t, fc)
	key := breachKey{RuleID: "r1", Service: "svc"}
	bKey := stampKey(stampKindBreach, key)
	rKey := stampKey(stampKindResolved, key)

	e.breachStart(ctx, key, time.Now(), 120)
	if !fc.has(bKey) {
		t.Fatal("breach stamp not mirrored")
	}
	e.clearBreach(ctx, key)
	if fc.has(bKey) {
		t.Fatal("clearBreach left the mirror key")
	}

	e.stampResolved(ctx, key, time.Now(), 300)
	if !fc.has(rKey) {
		t.Fatal("resolve stamp not mirrored")
	}
	e.clearResolved(ctx, key)
	if fc.has(rKey) {
		t.Fatal("clearResolved left the mirror key")
	}

	// IO discipline: a fresh leader clearing a pair it never saw
	// breached must NOT issue a mirror DEL per healthy pair.
	fresh := newStampEvaluator(t, fc)
	other := breachKey{RuleID: "r2", Service: "svc2"}
	_ = fc.Set(ctx, stampKey(stampKindBreach, other), []byte("1"), time.Minute) // previous leader's stamp
	before := fc.delCount()
	fresh.clearBreach(ctx, other)
	if fc.delCount() != before {
		t.Fatal("clearBreach issued a mirror DEL for a pair not in memory — per-tick DEL storm regression")
	}
	// …but clearResolved is unconditional by design.
	_ = fc.Set(ctx, stampKey(stampKindResolved, other), []byte("1"), time.Minute)
	fresh.clearResolved(ctx, other)
	if fc.has(stampKey(stampKindResolved, other)) {
		t.Fatal("clearResolved skipped the mirror key for a pair not in memory")
	}
}

// TestStampMirrorTTLArgs — mirror keys must carry 2×window (min 10m)
// so orphaned stamps self-reap instead of living forever in Redis.
func TestStampMirrorTTLArgs(t *testing.T) {
	ctx := context.Background()
	fc := newFakeStampCache()
	e := newStampEvaluator(t, fc)

	longKey := breachKey{RuleID: "long", Service: "svc"}
	e.breachStart(ctx, longKey, time.Now(), 600) // ForSec 10m → TTL 20m
	if ttl, ok := fc.ttl(stampKey(stampKindBreach, longKey)); !ok || ttl != 20*time.Minute {
		t.Fatalf("breach TTL = %v, want 20m (2×ForSec)", ttl)
	}

	shortKey := breachKey{RuleID: "short", Service: "svc"}
	e.breachStart(ctx, shortKey, time.Now(), 120) // 2×120s = 4m → floored
	if ttl, ok := fc.ttl(stampKey(stampKindBreach, shortKey)); !ok || ttl != 10*time.Minute {
		t.Fatalf("breach TTL = %v, want the 10m floor", ttl)
	}

	e.stampResolved(ctx, longKey, time.Now(), 600) // CooldownSec 10m → 20m
	if ttl, ok := fc.ttl(stampKey(stampKindResolved, longKey)); !ok || ttl != 20*time.Minute {
		t.Fatalf("resolve TTL = %v, want 20m (2×CooldownSec)", ttl)
	}
	e.stampResolved(ctx, shortKey, time.Now(), 0) // unset cooldown → floor
	if ttl, ok := fc.ttl(stampKey(stampKindResolved, shortKey)); !ok || ttl != 10*time.Minute {
		t.Fatalf("resolve TTL = %v, want the 10m floor", ttl)
	}
}
