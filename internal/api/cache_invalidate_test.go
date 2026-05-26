package api

// v0.6.11 regression test — cacheInvalidatePrefix used to call
// ScanPrefix and discard the result, never actually deleting
// the matching L2 keys. The L1 + the cross-pod broadcast still
// happened, so the failure mode was operator-reported staleness
// (mute/exclude settings updated → topology view still serves
// the old data) rather than a crash.
//
// This test guards against re-regression: it instruments a fake
// Cache, calls cacheInvalidatePrefix, and asserts that
// (a) DelPrefix was invoked with the exact prefix, and
// (b) Publish broadcast the canonical "prefix:<P>" payload to
//     the invalidation channel.
//
// Original bug: the pre-fix code path read
//
//   if keys, err := s.cache.ScanPrefix(ctx, prefix); err == nil {
//       _ = keys // not used in this path; placeholder
//   }
//
// — i.e. SCAN ran but DEL never did.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/cache"
)

// fakeCache records every method invocation so the test can
// assert what cacheInvalidatePrefix did. Implements just enough
// of cache.Cache to satisfy the interface; unused methods
// no-op.
type fakeCache struct {
	mu             sync.Mutex
	delPrefixCalls []string
	publishCalls   []publishCall
	scanPrefixHits int // pre-fix path; should stay 0 after v0.6.11
}

type publishCall struct {
	channel string
	msg     string
}

func (f *fakeCache) Get(context.Context, string) ([]byte, bool, error)         { return nil, false, nil }
func (f *fakeCache) Set(context.Context, string, []byte, time.Duration) error  { return nil }
func (f *fakeCache) Del(context.Context, string) error                         { return nil }
func (f *fakeCache) ScanPrefix(_ context.Context, _ string) ([][]byte, error) {
	f.mu.Lock()
	f.scanPrefixHits++
	f.mu.Unlock()
	return nil, nil
}
func (f *fakeCache) DelPrefix(_ context.Context, prefix string) error {
	f.mu.Lock()
	f.delPrefixCalls = append(f.delPrefixCalls, prefix)
	f.mu.Unlock()
	return nil
}
func (f *fakeCache) Ping(context.Context) error                  { return nil }
func (f *fakeCache) Stats(context.Context) (cache.RedisStats, error) {
	return cache.RedisStats{}, nil
}
func (f *fakeCache) Publish(_ context.Context, channel string, msg []byte) error {
	f.mu.Lock()
	f.publishCalls = append(f.publishCalls, publishCall{channel, string(msg)})
	f.mu.Unlock()
	return nil
}
func (f *fakeCache) Subscribe(ctx context.Context, _ string) (<-chan []byte, error) {
	ch := make(chan []byte)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

func TestCacheInvalidatePrefix_DelPrefixWired(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
	}{
		{"topology edges", "topology-edges:"},
		{"topology services", "topology-service:"},
		{"db summary", "db-summary:"},
		{"empty-string prefix is a no-op-ish call but still goes through", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeCache{}
			srv := &Server{
				cache: fc,
				l1:    newL1Cache(8),
				stats: newCacheStats(),
			}
			srv.cacheInvalidatePrefix(context.Background(), tc.prefix)

			fc.mu.Lock()
			defer fc.mu.Unlock()

			if len(fc.delPrefixCalls) != 1 {
				t.Fatalf("DelPrefix should be called exactly once, got %d", len(fc.delPrefixCalls))
			}
			if fc.delPrefixCalls[0] != tc.prefix {
				t.Errorf("DelPrefix called with %q, want %q", fc.delPrefixCalls[0], tc.prefix)
			}
			if fc.scanPrefixHits != 0 {
				t.Errorf("ScanPrefix MUST NOT be called from the invalidate path (pre-fix bug); got %d hits",
					fc.scanPrefixHits)
			}
			// Broadcast must still happen so peer pods drain
			// their L1.
			if len(fc.publishCalls) != 1 {
				t.Fatalf("Publish should be called exactly once, got %d", len(fc.publishCalls))
			}
			want := "prefix:" + tc.prefix
			if fc.publishCalls[0].msg != want {
				t.Errorf("Publish payload = %q, want %q", fc.publishCalls[0].msg, want)
			}
		})
	}
}
