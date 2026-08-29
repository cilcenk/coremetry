package chstore

// v0.10.158 — LazySnapshot sözleşmesi (lazy_snapshot.go başlığı).

import (
	"context"
	"errors"
	"testing"
)

func TestLazySnapshot_NoFetchWithoutGet(t *testing.T) {
	calls := 0
	l := NewLazySnapshot(func(ctx context.Context) (*OpenProblems, error) { calls++; return &OpenProblems{}, nil })
	if calls != 0 || l.Fetched() {
		t.Fatalf("fetch must not run before Get: calls=%d", calls)
	}
}

func TestLazySnapshot_FetchesOnceAcrossGets(t *testing.T) {
	calls := 0
	want := &OpenProblems{}
	l := NewLazySnapshot(func(ctx context.Context) (*OpenProblems, error) { calls++; return want, nil })
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		got, err := l.Get(ctx)
		if err != nil || got != want {
			t.Fatalf("Get %d: got=%p err=%v", i, got, err)
		}
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 fetch, got %d", calls)
	}
}

func TestLazySnapshot_ErrorIsStableWithinInstance(t *testing.T) {
	calls := 0
	boom := errors.New("boom")
	l := NewLazySnapshot(func(ctx context.Context) (*OpenProblems, error) { calls++; return nil, boom })
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if snap, err := l.Get(ctx); snap != nil || !errors.Is(err, boom) {
			t.Fatalf("Get %d: snap=%v err=%v", i, snap, err)
		}
	}
	if calls != 1 {
		t.Fatalf("a failed tick must not retry within the instance, calls=%d", calls)
	}
	if l.Fetched() != true {
		t.Fatal("Fetched must report the attempt")
	}
}

func TestLazySnapshot_NilReceiverSafe(t *testing.T) {
	var l *LazySnapshot
	snap, err := l.Get(context.Background())
	if snap != nil || err != nil || l.Fetched() {
		t.Fatalf("nil receiver: snap=%v err=%v", snap, err)
	}
	if snap.ByKey("r", "s") != nil {
		t.Fatal("nil snapshot ByKey must be nil")
	}
}
