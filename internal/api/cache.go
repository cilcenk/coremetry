package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Multi-tier caching for the hot-read API endpoints:
//
//   L0 in-flight dedupe (singleflight) — N concurrent callers
//     missing the same cache key collapse to one upstream
//     ClickHouse call; the rest wait for the result. Without
//     this, a cold key on a high-traffic endpoint produces a
//     thundering herd against CH on cache expiry.
//
//   L1 in-process cache — a tiny FIFO map sitting in front
//     of Redis. Catches burst traffic within a single node
//     without crossing the network. Per-entry TTL is short
//     (≤5s) so freshness expectations follow Redis, not the
//     longer in-process window.
//
//   L2 Redis cache (existing) — shared across nodes, primary
//     source of truth for "this query was answered N seconds
//     ago". Stores an envelope { written, body } so the read
//     path can compute age and decide whether to serve fresh,
//     serve stale + async-refresh (SWR), or treat as a hard
//     miss.
//
// SWR (stale-while-revalidate): every cache write stamps a
// timestamp; reads compute age. If age < softTtl → serve and
// log HIT. If softTtl ≤ age < 3*softTtl → serve immediately,
// kick a background refresh (deduped via singleflight). Past
// the 3x window we treat the entry as a hard miss and the
// caller pays the upstream cost. Net effect: most reads
// return in <50ms even when "the cache expired" because the
// stale-but-recent value is good enough for short-TTL
// dashboard queries.

// l1Cache is an in-process FIFO with per-entry TTL. Capped at
// `cap` entries; insertion order drives eviction (not true
// LRU — true LRU would need a linked list, FIFO is good
// enough for the burst-coalescing role).
type l1Cache struct {
	mu      sync.Mutex
	entries map[string]l1Entry
	order   []string
	cap     int
}

type l1Entry struct {
	data    []byte
	expires time.Time
}

func newL1Cache(cap int) *l1Cache {
	return &l1Cache{entries: map[string]l1Entry{}, cap: cap}
}

func (l *l1Cache) get(key string) ([]byte, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.data, true
}

func (l *l1Cache) set(key string, data []byte, ttl time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.entries[key]; !exists {
		l.order = append(l.order, key)
	}
	l.entries[key] = l1Entry{data: data, expires: time.Now().Add(ttl)}
	// FIFO eviction. Re-inserting an existing key doesn't bump
	// it forward — keeps the cap predictable under churn.
	for len(l.entries) > l.cap && len(l.order) > 0 {
		head := l.order[0]
		l.order = l.order[1:]
		delete(l.entries, head)
	}
}

// cacheEnvelope wraps the JSON body with a write timestamp so
// reads can compute age. Stored in Redis under the cache key;
// L1 stores the unwrapped body (already age-checked at the
// L1 set time).
type cacheEnvelope struct {
	Written int64           `json:"w"` // unix nanoseconds at write time
	Body    json.RawMessage `json:"b"`
}

func wrapEnvelope(body []byte) ([]byte, error) {
	return json.Marshal(cacheEnvelope{
		Written: time.Now().UnixNano(),
		Body:    body,
	})
}

// unwrapEnvelope returns (written, body, true) when raw matches
// the envelope shape, or (zero, nil, false) for legacy raw-body
// entries written before the envelope was introduced. Legacy
// entries age out naturally via the Redis TTL.
func unwrapEnvelope(raw []byte) (time.Time, []byte, bool) {
	var env cacheEnvelope
	if err := json.Unmarshal(raw, &env); err != nil ||
		env.Written == 0 || len(env.Body) == 0 {
		return time.Time{}, nil, false
	}
	return time.Unix(0, env.Written), env.Body, true
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// l1TTL bounds how long an entry stays in the in-process layer.
// Capped at 5s so a deploying replica doesn't serve a value
// that's behind every other node's view. Shorter than the
// minimum Redis cache TTL we use elsewhere (15s for the
// hottest endpoints).
const l1TTL = 5 * time.Second

// staleFactor caps how long past the soft TTL we'll still
// serve a stale value with an async refresh. 3x = e.g. a 30s
// TTL stays usable for 90s. Past that we fall back to a hard
// miss because the data is too out of date to call "fresh
// enough".
const staleFactor = 3

// serveCached is the read-through cache wrapper. Reads check
// L1 → L2 (Redis with SWR) → upstream fn (with singleflight
// dedupe). Writes populate both tiers. Writers also stamp the
// X-Cache response header so the operator can see what tier
// served the request from the browser network panel.
//
// `refresh=1` in the query string forces a recompute (e.g.
// when the operator just changed a setting and wants the
// dashboard to reflect it). The fresh result is still written
// so subsequent callers benefit.
//
// Failure modes:
//   - L1 corrupt entry: caller never set it, can't happen
//   - L2 Redis down: Get/Set return errors, we log + fall
//     through to the live path (same as pre-tiered behaviour)
//   - fn() error: surface to caller, do not poison the cache
func (s *Server) serveCached(w http.ResponseWriter, r *http.Request, key string, ttl time.Duration, fn func() (any, error)) {
	skipRead := r.URL.Query().Get("refresh") == "1"

	if !skipRead {
		// ── L1 ────────────────────────────────────────────────
		if data, ok := s.l1.get(key); ok {
			s.stats.record("HIT-L1", key)
			writeCacheHit(w, "HIT-L1", data)
			return
		}
		// ── L2 with SWR ───────────────────────────────────────
		if raw, ok, err := s.cache.Get(r.Context(), key); err == nil && ok {
			if written, body, envOK := unwrapEnvelope(raw); envOK {
				age := time.Since(written)
				if age < ttl {
					// Fresh hit. Populate L1 with the remaining
					// freshness window (capped) so future
					// burst reads on this node skip Redis too.
					s.l1.set(key, body, minDur(ttl-age, l1TTL))
					s.stats.record("HIT", key)
					writeCacheHit(w, "HIT", body)
					return
				}
				if age < ttl*staleFactor {
					// Stale-but-usable. Serve immediately,
					// kick a background refresh (deduped via
					// singleflight so concurrent stale hits
					// share one upstream call).
					go s.refreshKey(key, ttl, fn)
					s.stats.record("STALE", key)
					writeCacheHit(w, "STALE", body)
					return
				}
				// Past hard window → fall through to miss.
			} else {
				// Legacy entry (no envelope). Serve as-is and
				// let Redis TTL evict it; new writes go
				// through the envelope path.
				s.stats.record("HIT-LEGACY", key)
				writeCacheHit(w, "HIT-LEGACY", raw)
				return
			}
		}
	}

	// ── Miss path with singleflight dedupe ────────────────────
	v, err, _ := s.sf.Do(key, func() (any, error) {
		return fn()
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	// v0.5.303 — scrub NaN/Inf floats anywhere in the result
	// tree before json.Marshal. Defence-in-depth for the
	// "encoding/json: unsupported value NaN" 500s; complements
	// the per-Scan safeF guards from v0.5.301.
	sanitizeFloats(v)
	body, err := json.Marshal(v)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.storeCached(r.Context(), key, body, ttl)
	w.Header().Set("Content-Type", "application/json")
	if skipRead {
		s.stats.record("BYPASS", key)
		w.Header().Set("X-Cache", "BYPASS")
	} else {
		s.stats.record("MISS", key)
		w.Header().Set("X-Cache", "MISS")
	}
	w.Write(body)
}

// writeCacheHit emits the standard headers + body for a tier
// hit. Pulled into a helper so the four hit paths
// (HIT-L1 / HIT / STALE / HIT-LEGACY) stay consistent.
func writeCacheHit(w http.ResponseWriter, tier string, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache", tier)
	w.Write(body)
}

// storeCached writes the envelope to Redis and the bare body
// to L1. Redis TTL is staleFactor × softTtl so the SWR window
// has room to breathe past nominal expiry. Errors are logged
// but never fatal.
func (s *Server) storeCached(ctx context.Context, key string, body []byte, ttl time.Duration) {
	if env, err := wrapEnvelope(body); err == nil {
		if err := s.cache.Set(ctx, key, env, ttl*staleFactor); err != nil {
			log.Printf("[cache] set %s: %v", key, err)
		}
	}
	s.l1.set(key, body, minDur(ttl, l1TTL))
}

// refreshKey is the background half of SWR. Runs the upstream
// fn under a fresh context (the request that triggered the
// refresh has already returned), updates both cache tiers.
// Deduped via singleflight under the cache key so concurrent
// stale-hits don't fan out into N parallel CH queries.
func (s *Server) refreshKey(key string, ttl time.Duration, fn func() (any, error)) {
	s.sf.Do(key, func() (any, error) {
		// Defensive timeout — same as the warmer's queryBudg.
		// A refresh that hangs longer than this would block
		// the singleflight slot for new concurrent refreshes
		// in the same window.
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		v, err := fn()
		if err != nil {
			log.Printf("[cache] refresh %s: %v", key, err)
			return nil, err
		}
		sanitizeFloats(v) // v0.5.303 — same NaN scrub as the miss path
		body, err := json.Marshal(v)
		if err != nil {
			log.Printf("[cache] refresh marshal %s: %v", key, err)
			return nil, err
		}
		s.storeCached(ctx, key, body, ttl)
		return nil, nil
	})
}

// Singleflight + L1 are initialised once per Server. Both are
// goroutine-safe by design; no further wiring needed from
// callers — serveCached uses them implicitly.
type cacheTier struct {
	sf singleflight.Group
	l1 *l1Cache
}

// cacheStats records per-tier hit counts and the hottest keys
// since process start. Surfaces on /api/admin/cache-stats so
// the System page can show whether the multi-tier cache is
// actually doing useful work in production.
//
// Memory is bounded: tier counts is fixed (6 strings),
// keyHits is capped at 4096 keys with the lowest-count entry
// evicted on insertion to keep working-set-sized.
type cacheStats struct {
	mu      sync.Mutex
	counts  map[string]int64
	keyHits map[string]int64
	started time.Time
}

const cacheStatsKeyCap = 4096

func newCacheStats() *cacheStats {
	return &cacheStats{
		counts:  map[string]int64{},
		keyHits: map[string]int64{},
		started: time.Now(),
	}
}

// record bumps the tier counter and (for hit tiers only)
// the per-key counter. Misses don't update keyHits because a
// missing key isn't yet "hot" — it's a cold one.
func (cs *cacheStats) record(tier, key string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.counts[tier]++
	switch tier {
	case "HIT-L1", "HIT", "STALE":
		cs.keyHits[key]++
		if len(cs.keyHits) > cacheStatsKeyCap {
			// Evict the smallest-count entry. Linear scan;
			// O(n) but n is bounded at 4096 and this only
			// runs on the rare overflow.
			var minKey string
			var minCount int64 = -1
			for k, v := range cs.keyHits {
				if minCount < 0 || v < minCount {
					minKey, minCount = k, v
				}
			}
			delete(cs.keyHits, minKey)
		}
	}
}

// CacheStatsSnapshot is the wire shape for the admin endpoint.
// Counts is a tier → hit count map; TopKeys is a sorted slice
// of the most-frequently-served keys (capped at 20).
type CacheStatsSnapshot struct {
	SinceUnixNano int64            `json:"sinceUnixNano"`
	Counts        map[string]int64 `json:"counts"`
	TopKeys       []CacheKeyHit    `json:"topKeys"`
	L1Size        int              `json:"l1Size"`
	L1Cap         int              `json:"l1Cap"`
}

type CacheKeyHit struct {
	Key  string `json:"key"`
	Hits int64  `json:"hits"`
}

func (cs *cacheStats) snapshot(l1 *l1Cache) CacheStatsSnapshot {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	out := CacheStatsSnapshot{
		SinceUnixNano: cs.started.UnixNano(),
		Counts:        make(map[string]int64, len(cs.counts)),
	}
	for k, v := range cs.counts {
		out.Counts[k] = v
	}
	// Sort keys by hit count desc, take top 20. Cheap on the
	// bounded map; runs on admin requests not hot path.
	keys := make([]CacheKeyHit, 0, len(cs.keyHits))
	for k, v := range cs.keyHits {
		keys = append(keys, CacheKeyHit{Key: k, Hits: v})
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Hits > keys[j].Hits })
	if len(keys) > 20 {
		keys = keys[:20]
	}
	out.TopKeys = keys
	if l1 != nil {
		l1.mu.Lock()
		out.L1Size = len(l1.entries)
		out.L1Cap = l1.cap
		l1.mu.Unlock()
	}
	return out
}
