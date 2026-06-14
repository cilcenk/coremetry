package consumer

import (
	"context"
	"errors"
	"testing"
	"time"
)

// v0.8.x — before this, a flushFn (ClickHouse insert) error in flusher() was
// logged and the entire batch silently discarded with no counter, so the
// operator had no way to see write-path data loss on /admin/stats. The
// writeFailed counter closes that gap. This test pins the contract: every
// item in a batch whose flush errors is counted in WriteFailed(), and a
// healthy flush leaves it at zero (no false positives).
// waitFor polls cond until true or the deadline, sleeping briefly between
// checks. Deterministic substitute for a fixed sleep — flush is async via the
// loop+flusher goroutines, so we wait for the counter to settle.
func waitFor(cond func() bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

func TestConsumer_WriteFailedCountsFlushErrors(t *testing.T) {
	boom := errors.New("ch insert boom")
	// BatchSize 1 → each item dispatches + flushes immediately, so the counter
	// reaches 10 without depending on the flush-interval tick.
	c := New[int]("test-fail", Options{
		BatchSize: 1, BufferSize: 100, FlushInterval: 5 * time.Millisecond, Workers: 1,
	}, func(_ context.Context, batch []int) error { return boom })

	ctx, cancel := context.WithCancel(context.Background())
	c.Start(ctx)
	for i := 0; i < 10; i++ {
		if !c.Add(i) {
			t.Fatalf("Add(%d) returned false — buffer should not be full at cap 100", i)
		}
	}
	if !waitFor(func() bool { return c.WriteFailed() == 10 }, 2*time.Second) {
		t.Fatalf("WriteFailed() = %d; want 10 (every item in a failing-flush batch is lost)", c.WriteFailed())
	}
	if got := c.Dropped(); got != 0 {
		t.Fatalf("Dropped() = %d; want 0 — the buffer never overflowed; write-loss must not be conflated with queue-full", got)
	}
	cancel()
	c.Stop()
}

func TestConsumer_WriteFailedZeroOnHealthyFlush(t *testing.T) {
	flushed := make(chan int, 16)
	c := New[int]("test-ok", Options{
		BatchSize: 1, BufferSize: 100, FlushInterval: 5 * time.Millisecond, Workers: 1,
	}, func(_ context.Context, batch []int) error { flushed <- len(batch); return nil })

	ctx, cancel := context.WithCancel(context.Background())
	c.Start(ctx)
	for i := 0; i < 10; i++ {
		c.Add(i)
	}
	// wait until 10 items have flushed successfully
	got := 0
	if !waitFor(func() bool {
		for {
			select {
			case n := <-flushed:
				got += n
			default:
				return got >= 10
			}
		}
	}, 2*time.Second) {
		t.Fatalf("only %d items flushed; want 10", got)
	}
	if wf := c.WriteFailed(); wf != 0 {
		t.Fatalf("WriteFailed() = %d; want 0 — a successful flush must not increment the loss counter", wf)
	}
	cancel()
	c.Stop()
}
