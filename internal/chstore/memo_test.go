package chstore

// v0.10.156 — ttlMemo sözleşmesi (memo.go başlığı). Sahte saat: TTL kararı
// duvar saatine değil enjekte edilen saate bağlı, test deterministik.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTTLMemo_ServesWithinTTLAndRefetchesAfter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := newTTLMemo[int](5 * time.Second)
	m.now = func() time.Time { return now }
	var calls atomic.Int32
	fn := func(ctx context.Context) (int, error) { return int(calls.Add(1)), nil }
	ctx := context.Background()
	if v, _ := m.Get(ctx, fn); v != 1 {
		t.Fatalf("first Get: %d", v)
	}
	now = now.Add(4 * time.Second)
	if v, _ := m.Get(ctx, fn); v != 1 || calls.Load() != 1 {
		t.Fatalf("within TTL must not refetch: v=%d calls=%d", v, calls.Load())
	}
	now = now.Add(2 * time.Second) // 6 s > TTL
	if v, _ := m.Get(ctx, fn); v != 2 || calls.Load() != 2 {
		t.Fatalf("past TTL must refetch: v=%d calls=%d", v, calls.Load())
	}
}

func TestTTLMemo_InvalidateForcesRefetch(t *testing.T) {
	m := newTTLMemo[string](time.Minute)
	var calls atomic.Int32
	fn := func(ctx context.Context) (string, error) { calls.Add(1); return "v", nil }
	ctx := context.Background()
	_, _ = m.Get(ctx, fn)
	_, _ = m.Get(ctx, fn)
	m.Invalidate()
	_, _ = m.Get(ctx, fn)
	if calls.Load() != 2 {
		t.Fatalf("expected refetch after Invalidate, calls=%d", calls.Load())
	}
}

func TestTTLMemo_ErrorIsNotCached(t *testing.T) {
	m := newTTLMemo[int](time.Minute)
	var calls atomic.Int32
	fn := func(ctx context.Context) (int, error) {
		if calls.Add(1) == 1 {
			return 0, errors.New("boom")
		}
		return 7, nil
	}
	ctx := context.Background()
	if _, err := m.Get(ctx, fn); err == nil {
		t.Fatal("first Get must surface the error")
	}
	if v, err := m.Get(ctx, fn); err != nil || v != 7 {
		t.Fatalf("second Get must retry: v=%d err=%v", v, err)
	}
	if v, _ := m.Get(ctx, fn); v != 7 || calls.Load() != 2 {
		t.Fatalf("third Get must be served from memo: v=%d calls=%d", v, calls.Load())
	}
}

func TestTTLMemo_ConcurrentGetsShareOneFlight(t *testing.T) {
	m := newTTLMemo[int](time.Minute)
	var calls atomic.Int32
	fn := func(ctx context.Context) (int, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return 42, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v, _ := m.Get(context.Background(), fn); v != 42 {
				t.Errorf("got %d", v)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent Gets must collapse into one fetch, got %d", calls.Load())
	}
}

// v0.10.156 inceleme D2 — Invalidate uçuştaki Get'i BEKLEMEZ ve o uçuşun
// sonucu saklanmaz (yazım sonrası bayat servis yok).
func TestTTLMemo_InvalidateDoesNotBlockAndDropsInFlightResult(t *testing.T) {
	m := newTTLMemo[int](time.Minute)
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	fn := func(ctx context.Context) (int, error) {
		n := int(calls.Add(1))
		if n == 1 {
			close(started)
			<-release
		}
		return n, nil
	}
	done := make(chan int, 1)
	go func() { v, _ := m.Get(context.Background(), fn); done <- v }()
	<-started // fn uçuşta, mutex tutuluyor
	inv := make(chan struct{})
	go func() { m.Invalidate(); close(inv) }()
	select {
	case <-inv:
	case <-time.After(2 * time.Second):
		t.Fatal("Invalidate blocked behind an in-flight Get")
	}
	close(release)
	if v := <-done; v != 1 {
		t.Fatalf("in-flight caller must still get its own result, got %d", v)
	}
	if v, _ := m.Get(context.Background(), fn); v != 2 || calls.Load() != 2 {
		t.Fatalf("result computed before the Invalidate must not be served: v=%d calls=%d", v, calls.Load())
	}
	if v, _ := m.Get(context.Background(), fn); v != 2 || calls.Load() != 2 {
		t.Fatalf("fresh result must be memoised: v=%d calls=%d", v, calls.Load())
	}
}
