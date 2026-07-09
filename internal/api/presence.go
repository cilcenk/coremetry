package api

// User presence — "who's online" for the admin Users page (v0.8.403).
//
// SEMANTICS: a user is ONLINE when any authenticated API request carried
// their identity within the last 5 minutes. Every open Coremetry tab
// polls (health 5s, everything else 10-30s), so "logged in with a tab
// open" ≈ online — which is exactly the operator's ask. Closing the tab
// or logging out ages the stamp out within 5 minutes (the Redis key TTL
// equals the online window, so a present stamp ⇔ online; lastSeenAt is
// therefore only available while the user is/was recently online —
// absent means "not seen in the last 5 minutes" a.k.a. never/unknown).
//
// WRITE PATH — stamped from the middleware chain INSIDE
// auth.Middleware (see Start(): s.presence.middleware(mux)), i.e. after
// claims resolve, covering both the JWT and trusted-header paths.
// Fire-and-forget and throttled:
//
//   - THROTTLE: an in-proc per-pod map remembers the last stamp per
//     user; requests within 60s of the previous stamp skip Redis
//     entirely. The UI's pollers fire every 10-30s — without the
//     throttle presence would multiply Redis writes per open tab.
//     Per-pod (not shared): a fleet of N api pods writes at most N
//     stamps/user/minute — bounded and irrelevant next to cache traffic.
//   - FIRE-AND-FORGET: the Redis SET runs in a goroutine with its own
//     bounded ctx. Zero added latency on the request path, and a
//     degraded Redis means presence is silently unavailable — NEVER an
//     auth-path error (same posture as the L2 cache: best-effort).
//
// READ PATH — listUsers batch-reads every page user's stamp with ONE
// MGET (chunked at 256 keys so a pathological user count stays bounded)
// and enriches each row with online + lastSeenAt (unix ns).

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
	"github.com/cilcenk/coremetry/internal/cache"
)

const (
	// presenceKeyPrefix + userID → decimal unix-ns of the last stamp.
	presenceKeyPrefix = "coremetry:presence:"
	// presenceTTL is the Redis key lifetime — how long "last seen"
	// survives after the user goes quiet. 24h so the Users page can
	// say "2h ago" instead of dropping to "—" five minutes after
	// logout; presenceOnlineWindow below (5m) is what actually
	// decides ONLINE. (v0.8.403 integration call on the agent's
	// design flag.)
	presenceTTL = 24 * time.Hour
	// presenceOnlineWindow — stamp fresher than this = online.
	presenceOnlineWindow = 5 * time.Minute
	// presenceStampEvery — in-proc throttle: at most one Redis write
	// per user per pod per minute. Must stay well under presenceTTL
	// (5m) or an active user's key could expire between stamps.
	presenceStampEvery = 60 * time.Second
	// presenceMGetChunk bounds a single MGET's argument list.
	presenceMGetChunk = 256
	// presenceMaxTracked caps the throttle map. Real deployments have
	// tens-to-hundreds of users; hitting the cap resets the map, which
	// only costs one extra stamp per user — self-healing, never grows
	// unbounded on a long-lived pod.
	presenceMaxTracked = 8192
)

// presenceTracker owns the throttle state + cache handle. One per
// Server, constructed in NewServer.
type presenceTracker struct {
	cache cache.Cache
	now   func() time.Time // injectable for tests

	mu   sync.Mutex
	last map[string]time.Time // userID → last Redis stamp (this pod)
}

func newPresenceTracker(c cache.Cache) *presenceTracker {
	return &presenceTracker{
		cache: c,
		now:   time.Now,
		last:  make(map[string]time.Time),
	}
}

// shouldStamp is the pure throttle decision: true when this request
// must write a Redis stamp for userID, false when the previous stamp
// is younger than presenceStampEvery. Records the stamp time on a true
// return (decide + mark are one atomic step so concurrent requests for
// the same user can't both win).
func (p *presenceTracker) shouldStamp(userID string, now time.Time) bool {
	if userID == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if last, ok := p.last[userID]; ok && now.Sub(last) < presenceStampEvery {
		return false
	}
	if len(p.last) >= presenceMaxTracked {
		p.last = make(map[string]time.Time)
	}
	p.last[userID] = now
	return true
}

// stamp records "userID is active now" — throttled, fire-and-forget.
// Never returns an error and never blocks the caller on Redis: the
// write happens in a goroutine with its own bounded ctx (same pattern
// as copilot's RecordUsage goroutine — the response must not wait for
// bookkeeping).
func (p *presenceTracker) stamp(userID string) {
	now := p.now()
	if !p.shouldStamp(userID, now) {
		return
	}
	val := []byte(strconv.FormatInt(now.UnixNano(), 10))
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// Error deliberately dropped: degraded Redis = presence
		// silently unavailable, never an auth-path failure or log spam
		// (the cache layer already logs its own connectivity story).
		_ = p.cache.Set(ctx, presenceKeyPrefix+userID, val, presenceTTL)
	}()
}

// middleware sits INSIDE auth.Middleware (claims already resolved) and
// stamps presence for every authenticated request. Unauthenticated
// SkipPath traffic flows through with nil claims — no stamp.
func (p *presenceTracker) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c := auth.FromContext(r.Context()); c != nil {
			p.stamp(c.UserID)
		}
		next.ServeHTTP(w, r)
	})
}

// lastSeen batch-reads the presence stamps for userIDs. One MGET per
// 256-key chunk. Returns userID → stamp unix-ns; users without a live
// stamp are simply absent. Best-effort: any Redis error drops that
// chunk silently (presence unavailable ≠ users page broken).
func (p *presenceTracker) lastSeen(ctx context.Context, userIDs []string) map[string]int64 {
	out := make(map[string]int64, len(userIDs))
	for start := 0; start < len(userIDs); start += presenceMGetChunk {
		chunk := userIDs[start:min(start+presenceMGetChunk, len(userIDs))]
		keys := make([]string, len(chunk))
		for i, id := range chunk {
			keys[i] = presenceKeyPrefix + id
		}
		vals, err := p.cache.MGet(ctx, keys)
		if err != nil || len(vals) != len(keys) {
			continue
		}
		for i, v := range vals {
			if len(v) == 0 {
				continue
			}
			if ns, perr := strconv.ParseInt(string(v), 10, 64); perr == nil && ns > 0 {
				out[chunk[i]] = ns
			}
		}
	}
	return out
}

// presenceOnline is the pure stamp→flag mapping: a positive stamp
// younger than the online window = online. A stamp slightly in the
// future (cross-pod clock skew) also reads as online — the diff is
// negative, comfortably under the window.
func presenceOnline(stampNs, nowNs int64) bool {
	return stampNs > 0 && nowNs-stampNs < presenceOnlineWindow.Nanoseconds()
}
