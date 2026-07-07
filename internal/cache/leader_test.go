package cache

// v0.8.341 — H4(A) regression tests: a leader that loses Redis must DEMOTE
// once its refresh failures outlive the lease TTL. Pre-fix, the refresh loop
// `log; continue`d on every Refresh error, keeping held=true indefinitely —
// the Redis lease expired, a peer legitimately acquired it, and TWO
// evaluators ran concurrently (duplicate Problem rows + duplicate pages).

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// scriptLock is a scriptable in-memory Lock for driving LeaderHolder through
// acquire / refresh-failure / recovery scenarios without Redis.
type scriptLock struct {
	mu         sync.Mutex
	acquireOK  bool
	acquireErr error
	refreshOK  bool
	refreshErr error
	acquires   int
	refreshes  int
}

func (s *scriptLock) TryAcquire(context.Context, string, time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquires++
	return s.acquireOK, s.acquireErr
}

func (s *scriptLock) Refresh(context.Context, string, time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshes++
	return s.refreshOK, s.refreshErr
}

func (s *scriptLock) Release(context.Context, string) error { return nil }

func (s *scriptLock) set(f func(*scriptLock)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

// waitFor polls cond every 5ms until it holds or the deadline passes.
func waitFor(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s: %s", d, msg)
}

// TestRefreshOutlivedLease pins the pure demote decision — v0.8.341.
// Boundary matters: at exactly ttl the Redis lease is GONE (PEXPIRE from the
// last successful refresh has elapsed), so >= not >.
func TestRefreshOutlivedLease(t *testing.T) {
	ttl := 30 * time.Second
	cases := []struct {
		name        string
		sinceLastOK time.Duration
		want        bool
	}{
		{"just refreshed", 0, false},
		{"one missed heartbeat", ttl / 3, false},
		{"two missed heartbeats", 2 * ttl / 3, false},
		{"1ms before lease expiry", ttl - time.Millisecond, false},
		{"exactly at lease expiry", ttl, true},
		{"past lease expiry", ttl + time.Second, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := refreshOutlivedLease(c.sinceLastOK, ttl); got != c.want {
				t.Fatalf("refreshOutlivedLease(%s, %s) = %v, want %v", c.sinceLastOK, ttl, got, c.want)
			}
		})
	}
}

// TestLeaderDemotesWhenRefreshFailuresOutliveLease — the H4(A) split-brain
// scenario: leader acquires, then Redis partitions (every Refresh errors).
// IsLeader() must flip false within ~ttl; pre-fix it stayed true forever.
func TestLeaderDemotesWhenRefreshFailuresOutliveLease(t *testing.T) {
	sl := &scriptLock{acquireOK: true, refreshErr: errors.New("redis: connection refused")}
	h := &LeaderHolder{lock: sl, key: "test-demote", ttl: 240 * time.Millisecond, refresh: 80 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Start(ctx)

	waitFor(t, 2*time.Second, h.IsLeader, "initial acquire")
	// Redis stays down: post-demote re-acquire attempts must also fail so we
	// can observe the demoted state (otherwise the stub would instantly
	// re-grant leadership).
	sl.set(func(s *scriptLock) { s.acquireOK = false; s.acquireErr = errors.New("redis: connection refused") })

	waitFor(t, 2*time.Second, func() bool { return !h.IsLeader() },
		"demote: refresh failures outlived the lease but IsLeader stayed true (H4 split-brain)")

	sl.mu.Lock()
	refreshes := sl.refreshes
	sl.mu.Unlock()
	if refreshes < 3 {
		t.Fatalf("demoted after only %d refresh attempts — should tolerate blips up to the lease TTL (~3 heartbeats)", refreshes)
	}
}

// TestLeaderSurvivesBlipsShorterThanLease — refresh errors that recover
// BEFORE the lease expires must never drop leadership (that was the intent
// of the pre-fix `continue`; the fix must not over-correct into demoting on
// the first blip).
func TestLeaderSurvivesBlipsShorterThanLease(t *testing.T) {
	sl := &scriptLock{acquireOK: true, refreshErr: errors.New("redis: i/o timeout")}
	h := &LeaderHolder{lock: sl, key: "test-blip", ttl: 900 * time.Millisecond, refresh: 300 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Start(ctx)

	waitFor(t, 2*time.Second, h.IsLeader, "initial acquire")
	// Let ~one heartbeat fail, then recover — well inside the 900ms lease.
	time.Sleep(350 * time.Millisecond)
	sl.set(func(s *scriptLock) { s.refreshErr = nil; s.refreshOK = true })

	// Sample across two more lease windows: leadership must hold throughout.
	deadline := time.Now().Add(700 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !h.IsLeader() {
			t.Fatal("demoted on a blip shorter than the lease TTL — demote threshold too aggressive")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestLeaderReacquiresAfterDemote — after a demote the holder must fall back
// into the acquire loop and regain leadership once Redis recovers (or the
// peer releases). Pre-fix this path didn't exist: held never flipped.
func TestLeaderReacquiresAfterDemote(t *testing.T) {
	sl := &scriptLock{acquireOK: true, refreshErr: errors.New("redis: connection refused")}
	h := &LeaderHolder{lock: sl, key: "test-reacquire", ttl: 240 * time.Millisecond, refresh: 80 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.Start(ctx)

	waitFor(t, 2*time.Second, h.IsLeader, "initial acquire")
	sl.set(func(s *scriptLock) { s.acquireOK = false }) // peer holds it during our outage
	waitFor(t, 2*time.Second, func() bool { return !h.IsLeader() }, "demote")

	// Recovery: lock becomes acquirable again (lease freed / Redis back).
	sl.set(func(s *scriptLock) { s.acquireOK = true })
	waitFor(t, 2*time.Second, h.IsLeader, "re-acquire after demote")
}
