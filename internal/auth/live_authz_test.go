package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

// live_authz_test.go — v0.9.352.
//
// Sessions were stateless JWTs carrying `role` with a 24-hour TTL, and Parse
// only checked signature + expiry. Deleting an account, disabling it, or
// demoting an admin took effect UP TO 24 HOURS LATER: the operator does the
// thing, the audit log records it, and the person keeps their access for the
// rest of the day. This runs inside a bank.

type fakeLookup struct {
	role  string
	ok    bool
	err   error
	calls int
}

func (f *fakeLookup) LiveAuthz(context.Context, string) (string, bool, error) {
	f.calls++
	return f.role, f.ok, f.err
}

func TestLiveAuthzOverridesTokenRole(t *testing.T) {
	// The token still says admin; the store says viewer. The store wins —
	// and the user is NOT signed out, they are downgraded. A revocation list
	// could not express this: the token is perfectly valid, it just must not
	// carry admin any more.
	c := newLiveAuthzCache(&fakeLookup{role: "viewer", ok: true})
	role, ok := c.resolve(context.Background(), "u1", "admin")
	if !ok {
		t.Fatal("a demoted user must stay logged in, not be rejected")
	}
	if role != "viewer" {
		t.Errorf("role = %q, want viewer (the live value, not the token's)", role)
	}

	// Promotion works the same way, immediately — no re-login needed.
	c2 := newLiveAuthzCache(&fakeLookup{role: "admin", ok: true})
	if role, _ := c2.resolve(context.Background(), "u1", "viewer"); role != "admin" {
		t.Errorf("promotion not picked up: role = %q, want admin", role)
	}
}

func TestLiveAuthzRejectsDeletedOrDisabled(t *testing.T) {
	c := newLiveAuthzCache(&fakeLookup{ok: false})
	if _, ok := c.resolve(context.Background(), "u1", "admin"); ok {
		t.Error("a deleted/disabled user must be rejected even with a valid token")
	}
}

// Fail OPEN on a store error: ClickHouse being briefly unreachable must not
// log every operator out of the tool they use to find out why. The exposure
// stays bounded by the token's own expiry; failing closed would turn a CH
// blip into a total outage.
func TestLiveAuthzFailsOpenOnError(t *testing.T) {
	c := newLiveAuthzCache(&fakeLookup{err: errors.New("ch down")})
	role, ok := c.resolve(context.Background(), "u1", "editor")
	if !ok || role != "editor" {
		t.Errorf("store error should fall back to the token: got (%q,%v), want (editor,true)", role, ok)
	}
}

// A stale entry is better evidence than the token — it is at most 10s old,
// the token can be a day old.
func TestLiveAuthzPrefersStaleEntryOverToken(t *testing.T) {
	f := &fakeLookup{role: "viewer", ok: true}
	c := newLiveAuthzCache(f)
	c.resolve(context.Background(), "u1", "admin") // warms the cache
	f.err = errors.New("ch down")
	c.byUser["u1"] = authzEntry{role: "viewer", ok: true, at: time.Now().Add(-time.Hour)}
	if role, _ := c.resolve(context.Background(), "u1", "admin"); role != "viewer" {
		t.Errorf("stale entry ignored: role = %q, want viewer", role)
	}
}

// The store must be consulted at most once per user per TTL — this sits on
// every authenticated request.
func TestLiveAuthzCachesWithinTTL(t *testing.T) {
	f := &fakeLookup{role: "admin", ok: true}
	c := newLiveAuthzCache(f)
	for i := 0; i < 50; i++ {
		c.resolve(context.Background(), "u1", "admin")
	}
	if f.calls != 1 {
		t.Errorf("lookup called %d times, want 1 — the cache is not holding", f.calls)
	}
}

// invalidate is what makes a demotion take effect on the acting pod
// IMMEDIATELY rather than after the TTL.
func TestInvalidateForcesRefetch(t *testing.T) {
	f := &fakeLookup{role: "admin", ok: true}
	c := newLiveAuthzCache(f)
	c.resolve(context.Background(), "u1", "admin")
	f.role = "viewer"
	c.invalidate("u1")
	if role, _ := c.resolve(context.Background(), "u1", "admin"); role != "viewer" {
		t.Errorf("after invalidate the new role must be read: got %q", role)
	}
}

// Not wired (tests, dev builds) → behave exactly as before, never lock anyone
// out. A nil cache must be safe on every method.
func TestLiveAuthzUnwiredIsTransparent(t *testing.T) {
	var c *liveAuthzCache
	role, ok := c.resolve(context.Background(), "u1", "admin")
	if !ok || role != "admin" {
		t.Errorf("unwired resolver changed behaviour: (%q,%v)", role, ok)
	}
	c.invalidate("u1") // must not panic
	empty := newLiveAuthzCache(nil)
	if role, ok := empty.resolve(context.Background(), "u1", "editor"); !ok || role != "editor" {
		t.Errorf("nil lookup changed behaviour: (%q,%v)", role, ok)
	}
}
