package main

import "testing"

// v0.8.212 — multi-pod HA hazard: COREMETRY_REDIS_URL set (operator wants a
// distributed leader lock) but Redis is unreachable, so the pod falls back to
// the always-leader Noop lock → in a multi-pod deployment EVERY pod becomes
// leader and background jobs (alerts/notifications/aggregation/retention) run
// DUPLICATED. isLockDegraded is the pure gate behind the boot warning + the
// /admin/stats flag. Pin: degraded ONLY when Redis was configured AND didn't
// connect — never for the single-pod / dev case (no Redis = always-leader is
// correct), never when Redis connected.
func TestIsLockDegraded(t *testing.T) {
	cases := []struct {
		name       string
		configured bool
		connected  bool
		want       bool
	}{
		{"redis set but down => degraded", true, false, true},
		{"redis set and up => fine", true, true, false},
		{"no redis (single-pod/dev) => not degraded", false, false, false},
		{"no redis but 'connected' nonsense => not degraded", false, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isLockDegraded(c.configured, c.connected); got != c.want {
				t.Fatalf("isLockDegraded(%v, %v) = %v, want %v", c.configured, c.connected, got, c.want)
			}
		})
	}
}
