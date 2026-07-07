// v0.8.354 — HA audit 🟡#2: the ForSec sustain clock (breachSince) and
// the CooldownSec gate (lastResolved) lived ONLY in per-pod memory, so
// every leader failover and every rolling deploy (maxUnavailable:0 ⇒
// leader moves on EVERY release) reset them: a breach 9 minutes into a
// 10-minute sustain window restarted from zero on the new leader
// (alert delayed another full ForSec), and a just-resolved problem
// could re-open straight past its cooldown (flap + duplicate page).
//
// Design: write-through, read-fallback. The in-memory maps stay the
// hot path (unchanged semantics for a stable leader); every stamp
// WRITE mirrors to Redis, and an in-memory MISS (fresh leader)
// hydrates from Redis once before falling back to "no stamp". Redis
// errors, misses, and the Noop cache (SwitchableCache pre-reconnect,
// v0.8.344) all degrade to the pre-v0.8.354 in-memory-only behavior —
// stamp IO never blocks or fails a tick.
//
// Lock discipline: breachMu guards ONLY the map access; all Redis IO
// happens outside the mutex (values are copied first).
package evaluator

import (
	"context"
	"log"
	"strconv"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
)

const (
	stampKindBreach   = "breach"
	stampKindResolved = "resolved"
	// stampMinTTL floors the mirror-key lifetime so zero/short gate
	// windows still survive a slow failover. The TTL is straggler-GC
	// only — live code paths delete the key explicitly on clear.
	stampMinTTL = 10 * time.Minute
)

// stampKey builds the Redis mirror key for a (rule, service) stamp:
//
//	coremetry:eval:<kind>:<len(ruleID)>:<ruleID>:<service>
//
// ruleID is free-form (operator-defined; the anomaly promoter's
// rule_id is literally "anomaly-auto:<fp>"), so a plain colon join
// would let two different (ruleID, service) pairs alias one key
// ("a:b"+"c" vs "a"+"b:c"). Length-prefixing the ruleID keeps the
// join injective — same collision concern chstore.FingerprintAnomaly
// solves with a delimited sha1, minus the hashing so the key stays
// operator-readable in redis-cli.
func stampKey(kind string, k breachKey) string {
	return "coremetry:eval:" + kind + ":" +
		strconv.Itoa(len(k.RuleID)) + ":" + k.RuleID + ":" + k.Service
}

// stampTTL sizes a mirror key's lifetime at 2× the gate window it
// protects (ForSec for breach stamps, CooldownSec for resolve stamps),
// floored at stampMinTTL. uint32 matches the AlertRule field types.
func stampTTL(windowSec uint32) time.Duration {
	ttl := 2 * time.Duration(windowSec) * time.Second
	if ttl < stampMinTTL {
		ttl = stampMinTTL
	}
	return ttl
}

// SetStampCache wires the shared cache so sustain/cooldown stamps
// survive leader failover. Called from main() once, like SetLogs —
// keeps New()'s signature stable. nil (never wired) or a Noop inner
// keeps the evaluator on pure in-memory stamps.
func (e *Evaluator) SetStampCache(c cache.Cache) { e.stamps = c }

// ── raw mirror IO (nil-safe, error-swallowing) ───────────────────────────────

// stampGet reads a mirrored stamp. Any error, miss, or unparsable
// value degrades to "no stamp" — exactly the old in-memory behavior.
func (e *Evaluator) stampGet(ctx context.Context, key string) (time.Time, bool) {
	if e.stamps == nil {
		return time.Time{}, false
	}
	b, ok, err := e.stamps.Get(ctx, key)
	if err != nil || !ok {
		return time.Time{}, false
	}
	ns, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil || ns <= 0 {
		return time.Time{}, false
	}
	return time.Unix(0, ns), true
}

// stampSet mirrors a stamp write (value = unixnano string).
// Best-effort: an error is logged and dropped — the in-memory map
// stays authoritative for this leader.
func (e *Evaluator) stampSet(ctx context.Context, key string, t time.Time, ttl time.Duration) {
	if e.stamps == nil {
		return
	}
	if err := e.stamps.Set(ctx, key, []byte(strconv.FormatInt(t.UnixNano(), 10)), ttl); err != nil {
		log.Printf("[evaluator] stamp mirror set %s: %v", key, err)
	}
}

func (e *Evaluator) stampDel(ctx context.Context, key string) {
	if e.stamps == nil {
		return
	}
	if err := e.stamps.Del(ctx, key); err != nil {
		log.Printf("[evaluator] stamp mirror del %s: %v", key, err)
	}
}

// ── sustain clock (breachSince) ──────────────────────────────────────────────

// breachStart returns the sustain clock's first-breach stamp for key,
// recording `now` when none exists anywhere. existing=false means this
// call stamped the FIRST sighting — the caller waits for the sustain
// window. On an in-memory miss (fresh leader after failover) it
// hydrates from the Redis mirror once, so a breach 9 minutes into a
// 10-minute ForSec keeps its clock across the failover instead of
// restarting from zero.
func (e *Evaluator) breachStart(ctx context.Context, key breachKey, now time.Time, forSec uint32) (first time.Time, existing bool) {
	e.breachMu.Lock()
	first, seen := e.breachSince[key]
	e.breachMu.Unlock()
	if seen {
		return first, true
	}
	rkey := stampKey(stampKindBreach, key)
	if ts, ok := e.stampGet(ctx, rkey); ok {
		e.breachMu.Lock()
		if cur, dup := e.breachSince[key]; dup { // raced with another writer
			e.breachMu.Unlock()
			return cur, true
		}
		e.breachSince[key] = ts
		e.breachMu.Unlock()
		return ts, true
	}
	// First sighting anywhere — stamp memory, then mirror.
	e.breachMu.Lock()
	if cur, dup := e.breachSince[key]; dup {
		e.breachMu.Unlock()
		return cur, true
	}
	e.breachSince[key] = now
	e.breachMu.Unlock()
	e.stampSet(ctx, rkey, now, stampTTL(forSec))
	return now, false
}

// clearBreach drops the sustain stamp (breach cleared, or the
// MinSamples gate wiped it). The Redis mirror is deleted only when the
// in-memory map actually held the stamp: the !breached branch runs for
// EVERY healthy (rule, service) pair every tick, and an unconditional
// mirror delete would cost rules×services Redis round-trips per tick.
// Trade-off: a stamp a PREVIOUS leader wrote for a breach that cleared
// before this leader ever saw it breached outlives the clear until its
// TTL (2×ForSec, min 10m) reaps it; a re-breach inside that window
// hydrates the stale clock and can open up to ForSec early. Accepted —
// it needs a failover plus a clear-then-re-breach within minutes, and
// CooldownSec already guards the flap direction.
func (e *Evaluator) clearBreach(ctx context.Context, key breachKey) {
	e.breachMu.Lock()
	_, had := e.breachSince[key]
	delete(e.breachSince, key)
	e.breachMu.Unlock()
	if had {
		e.stampDel(ctx, stampKey(stampKindBreach, key))
	}
}

// ── cooldown gate (lastResolved) ─────────────────────────────────────────────

// resolvedAt returns the cooldown gate's last-auto-resolve stamp,
// hydrating from the Redis mirror on an in-memory miss so a
// just-resolved problem can't re-open past its CooldownSec on a
// freshly-elected leader. A hit seeds the map — one Redis read per key
// per leadership, not per tick. A miss seeds nothing, but the calling
// path then opens a Problem (no suppression), so the read doesn't
// repeat for that key either.
func (e *Evaluator) resolvedAt(ctx context.Context, key breachKey) (time.Time, bool) {
	e.breachMu.Lock()
	rt, seen := e.lastResolved[key]
	e.breachMu.Unlock()
	if seen {
		return rt, true
	}
	ts, ok := e.stampGet(ctx, stampKey(stampKindResolved, key))
	if !ok {
		return time.Time{}, false
	}
	e.breachMu.Lock()
	if cur, dup := e.lastResolved[key]; dup {
		e.breachMu.Unlock()
		return cur, true
	}
	e.lastResolved[key] = ts
	e.breachMu.Unlock()
	return ts, true
}

// stampResolved records an auto-resolve for the cooldown gate —
// write-through to the Redis mirror so the gate survives failover.
func (e *Evaluator) stampResolved(ctx context.Context, key breachKey, now time.Time, cooldownSec uint32) {
	e.breachMu.Lock()
	e.lastResolved[key] = now
	e.breachMu.Unlock()
	e.stampSet(ctx, stampKey(stampKindResolved, key), now, stampTTL(cooldownSec))
}

// clearResolved drops the cooldown stamp after the silent-source sweep
// closes a problem — the next real breach must open a fresh problem
// immediately. Unlike clearBreach the mirror delete is unconditional:
// the sweep list is small and bounded, and a fresh leader must be able
// to clear a stamp only the PREVIOUS leader held in memory.
func (e *Evaluator) clearResolved(ctx context.Context, key breachKey) {
	e.breachMu.Lock()
	delete(e.lastResolved, key)
	e.breachMu.Unlock()
	e.stampDel(ctx, stampKey(stampKindResolved, key))
}
