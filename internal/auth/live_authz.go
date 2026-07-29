package auth

import (
	"context"
	"strings"
	"sync"
	"time"
)

// live_authz.go — v0.9.352. Authorization is resolved from the STORE on every
// request, not read out of the token.
//
// The bug: sessions are stateless HS256 JWTs carrying `role`, with a 24-hour
// TTL, and Parse only checks the signature and the expiry. Nothing ever asks
// the database whether that user still exists or still has that role. So
// deleting an account, disabling it, or demoting an admin to viewer took
// effect up to 24 HOURS later — the operator does the thing, the audit log
// records it, and the person keeps their access for the rest of the day.
// Surfaced by the v1.0 readiness audit; this runs inside a bank.
//
// WHY NOT A REVOCATION LIST: the obvious fix is to blacklist tokens on
// delete/demote. That fixes deletion but not demotion — a demoted admin's
// token is still valid, it just shouldn't carry `admin` any more, and
// blacklisting would sign them out instead of downgrading them. Resolving the
// role live handles both, and handles PROMOTION too (a user granted admin
// gets it immediately rather than after a re-login).
//
// The token therefore asserts AUTHENTICATION ("this is who you are", signed).
// Authorization is looked up fresh. That is the same split the cmk_ API
// tokens already use — they hold no role at all and are resolved through an
// in-memory cache on every request (v0.8.444).
//
// COST: one map read on the hot path. The store is consulted at most once per
// user per liveAuthzTTL; everything else is served from memory.

// liveAuthzTTL bounds how stale a role can be across pods.
//
// A mutation invalidates the entry on the pod that served it immediately, so
// the operator who just demoted someone sees it at once. Other pods pick it
// up within this window. Ten seconds turns a 24-hour exposure into a
// ten-second one; shortening it further trades real CH reads for a difference
// nobody can perceive.
const liveAuthzTTL = 10 * time.Second

// AuthzLookup resolves the CURRENT authorization state of a user id.
//
// ok=false means "this user must not be let in" — deleted, or disabled. The
// distinction doesn't matter to the caller and deliberately isn't exposed:
// both end in 401, and telling them apart would leak whether an account
// exists.
type AuthzLookup interface {
	LiveAuthz(ctx context.Context, userID string) (role string, ok bool, err error)
}

type authzEntry struct {
	role string
	ok   bool
	at   time.Time
}

type liveAuthzCache struct {
	mu     sync.RWMutex
	byUser map[string]authzEntry
	lookup AuthzLookup
}

func newLiveAuthzCache(l AuthzLookup) *liveAuthzCache {
	return &liveAuthzCache{byUser: map[string]authzEntry{}, lookup: l}
}

// resolve returns the live role for a user id.
//
// FAILS OPEN on a store error, and that is a deliberate trade: ClickHouse
// being briefly unreachable must not log every operator out of the tool they
// use to find out WHY it is unreachable. The window is bounded by the token's
// own expiry, and the alternative — failing closed — turns a CH blip into a
// total outage of the product. A deleted user surviving a database outage is
// the lesser harm.
func (c *liveAuthzCache) resolve(ctx context.Context, uid, tokenRole string) (string, bool) {
	if c == nil || c.lookup == nil || strings.TrimSpace(uid) == "" {
		return tokenRole, true // not wired (tests, dev) — behave as before
	}
	now := time.Now()

	c.mu.RLock()
	e, hit := c.byUser[uid]
	c.mu.RUnlock()
	if hit && now.Sub(e.at) < liveAuthzTTL {
		return e.role, e.ok
	}

	role, ok, err := c.lookup.LiveAuthz(ctx, uid)
	if err != nil {
		// Serve a stale entry if we have one — it is better evidence than
		// the token, which can be a full day old.
		if hit {
			return e.role, e.ok
		}
		return tokenRole, true
	}
	c.mu.Lock()
	c.byUser[uid] = authzEntry{role: role, ok: ok, at: now}
	c.mu.Unlock()
	return role, ok
}

// invalidate drops one user's cached authorization. Called by the mutation
// handlers so the pod that performed the change stops honouring the old role
// on its very next request, instead of waiting out liveAuthzTTL.
func (c *liveAuthzCache) invalidate(uid string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.byUser, uid)
	c.mu.Unlock()
}

// SetAuthzLookup wires the live resolver. Called once from main(); when it is
// never called the middleware keeps the pre-v0.9.352 behaviour (trust the
// token), so tests and dev builds are unaffected.
func (s *Service) SetAuthzLookup(l AuthzLookup) { s.liveAuthz = newLiveAuthzCache(l) }

// InvalidateAuthz forgets a user's cached role. Every user mutation calls it.
func (s *Service) InvalidateAuthz(userID string) { s.liveAuthz.invalidate(userID) }
