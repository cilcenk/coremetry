package api

// v0.8.403 regression tests — user presence ("logged-in users show as
// ONLINE" on the admin Users page).
//
// Two properties matter enough to pin:
//
//  1. THROTTLE: the UI's pollers fire every 10-30s per open tab; the
//     in-proc stamp-skip window (60s) is what keeps presence from
//     multiplying Redis writes. If the window logic regresses, every
//     poll becomes a Redis SET across the fleet.
//  2. ENRICH MAPPING: stamp bytes → (online, lastSeenAt) drives the
//     operator-visible badge. Fresh stamp = online; stale/absent/
//     garbage must degrade to offline + "never/unknown", never error.

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
)

// TestPresenceShouldStamp_ThrottleWindow — table-driven sequence
// against a fixed clock. Each step is (user, offset from t0) and the
// expected stamp/skip decision given every step before it.
func TestPresenceShouldStamp_ThrottleWindow(t *testing.T) {
	t0 := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	steps := []struct {
		name string
		user string
		at   time.Duration
		want bool
	}{
		{"first request stamps", "u1", 0, true},
		{"poller at 10s skips", "u1", 10 * time.Second, false},
		{"poller at 30s skips", "u1", 30 * time.Second, false},
		{"59s — still inside window", "u1", 59 * time.Second, false},
		{"60s — window elapsed, stamps", "u1", 60 * time.Second, true},
		{"61s — new window just opened", "u1", 61 * time.Second, false},
		{"second user is independent", "u2", 61 * time.Second, true},
		{"first user again at 119s skips", "u1", 119 * time.Second, false},
		{"first user at 120s stamps", "u1", 120 * time.Second, true},
		{"empty userID never stamps", "", 200 * time.Second, false},
	}

	p := newPresenceTracker(cacheOnly(cache.NewNoop()))
	for _, st := range steps {
		if got := p.shouldStamp(st.user, t0.Add(st.at)); got != st.want {
			t.Errorf("%s: shouldStamp(%q, t0+%s) = %v, want %v",
				st.name, st.user, st.at, got, st.want)
		}
	}
}

// TestPresenceShouldStamp_MapCapResets — the throttle map must never
// grow unbounded on a long-lived pod; hitting the cap resets it (one
// extra stamp per user is the only cost).
func TestPresenceShouldStamp_MapCapResets(t *testing.T) {
	p := newPresenceTracker(cacheOnly(cache.NewNoop()))
	now := time.Now()
	for i := 0; i < presenceMaxTracked; i++ {
		p.shouldStamp("user-"+time.Duration(i).String(), now)
	}
	p.shouldStamp("overflow-user", now)
	p.mu.Lock()
	n := len(p.last)
	p.mu.Unlock()
	if n > presenceMaxTracked {
		t.Fatalf("throttle map grew past the cap: %d entries", n)
	}
}

// TestPresenceOnline_Mapping — pure stamp→flag mapping (the enrich
// path in listUsers applies exactly this to each MGET slot).
func TestPresenceOnline_Mapping(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC).UnixNano()
	cases := []struct {
		name    string
		stampNs int64
		want    bool
	}{
		{"fresh stamp (1m ago) = online", now - int64(time.Minute), true},
		{"just under the window (4m59s) = online", now - int64(5*time.Minute) + int64(time.Second), true},
		{"exactly at the window = offline", now - int64(5*time.Minute), false},
		{"stale stamp (6m) = offline", now - int64(6*time.Minute), false},
		{"zero stamp (never seen) = offline", 0, false},
		{"negative stamp (garbage) = offline", -1, false},
		{"future stamp (clock skew) = online", now + int64(30*time.Second), true},
	}
	for _, c := range cases {
		if got := presenceOnline(c.stampNs, now); got != c.want {
			t.Errorf("%s: presenceOnline(%d, %d) = %v, want %v",
				c.name, c.stampNs, now, got, c.want)
		}
	}
}

// presenceStubCache — noop base with a canned MGET keyspace, so
// lastSeen's parse/skip behaviour is testable without Redis.
type presenceStubCache struct {
	cache.Cache // noop base (embedding the interface value)
	vals        map[string][]byte
	mgetErr     error
}

func (p presenceStubCache) MGet(_ context.Context, keys []string) ([][]byte, error) {
	if p.mgetErr != nil {
		return nil, p.mgetErr
	}
	out := make([][]byte, len(keys))
	for i, k := range keys {
		out[i] = p.vals[k]
	}
	return out, nil
}

// TestPresenceLastSeen_EnrichMapping — batched read: valid stamps map
// to their user, missing keys are absent, garbage values are skipped
// (never a partial parse or an error surfaced to the handler).
func TestPresenceLastSeen_EnrichMapping(t *testing.T) {
	freshNs := time.Now().Add(-90 * time.Second).UnixNano()
	stub := presenceStubCache{
		Cache: cacheOnly(cache.NewNoop()),
		vals: map[string][]byte{
			presenceKeyPrefix + "alice": []byte(strconv.FormatInt(freshNs, 10)),
			presenceKeyPrefix + "mallory": []byte("not-a-number"),
			presenceKeyPrefix + "zero":    []byte("0"),
		},
	}
	p := newPresenceTracker(stub)

	got := p.lastSeen(context.Background(), []string{"alice", "bob", "mallory", "zero"})
	if len(got) != 1 {
		t.Fatalf("lastSeen returned %d entries, want 1 (only alice has a valid stamp): %v", len(got), got)
	}
	if got["alice"] != freshNs {
		t.Errorf("alice stamp = %d, want %d", got["alice"], freshNs)
	}
	if _, ok := got["bob"]; ok {
		t.Error("bob has no stamp but appeared in the result")
	}

	// Degraded Redis: MGET errors must yield an empty map, not an error
	// path — presence silently unavailable is the contract.
	p2 := newPresenceTracker(presenceStubCache{
		Cache:   cacheOnly(cache.NewNoop()),
		mgetErr: context.DeadlineExceeded,
	})
	if got := p2.lastSeen(context.Background(), []string{"alice"}); len(got) != 0 {
		t.Fatalf("lastSeen on erroring cache = %v, want empty", got)
	}
}

// cacheOnly discards the Lock half of cache.NewNoop() so tests read
// naturally at the call site.
func cacheOnly(c cache.Cache, _ cache.Lock) cache.Cache { return c }
