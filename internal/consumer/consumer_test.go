package consumer

import (
	"context"
	"errors"
	"sync/atomic"
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
	// FlushRetryBase tiny: v0.8.340 retries each batch 3× before counting
	// writeFailed — same contract, just not at production backoff speed.
	c := New[int]("test-fail", Options{
		BatchSize: 1, BufferSize: 100, FlushInterval: 5 * time.Millisecond, Workers: 1,
		FlushRetryBase: time.Millisecond,
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

// v0.8.336 (HA audit H1) — regression: shutdown-drain data loss. The drain
// path's dispatch raced `flushQ <- b` against the ALREADY-CANCELLED
// ctx.Done() — Go's select picks uniformly among ready cases, so every
// final batch had ~50% odds of being discarded (uncounted!) on every
// deploy, 100% when flushQ was momentarily full. Contract now:
//   - every item accepted before cancellation reaches flushFn OR is
//     counted in Dropped() — never silent;
//   - a wedged flusher can't hang shutdown forever (bounded drain send).
func TestDrainDeliversAllOnShutdown(t *testing.T) {
	for round := 0; round < 30; round++ {
		var got atomic.Int64
		c := New[int]("drain-test", Options{
			BatchSize: 10, BufferSize: 1000, FlushInterval: time.Hour, Workers: 2,
		}, func(_ context.Context, b []int) error {
			got.Add(int64(len(b)))
			return nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		c.Start(ctx)
		const n = 137 // deliberately not a batch multiple → forces a partial final batch
		for i := 0; i < n; i++ {
			if !c.Add(i) {
				t.Fatalf("round %d: Add rejected with room to spare", round)
			}
		}
		cancel()
		c.Stop()
		if got.Load()+c.Dropped() != int64(n) {
			t.Fatalf("round %d: flushed %d + dropped %d != accepted %d (silent loss)",
				round, got.Load(), c.Dropped(), n)
		}
		if got.Load() != int64(n) {
			t.Fatalf("round %d: healthy flushers must receive ALL items, flushed only %d/%d",
				round, got.Load(), n)
		}
	}
}

// A fully wedged flush stage (all workers blocked) must not hang Stop()
// forever: the bounded drain send gives up, COUNTS the loss, and shutdown
// completes.
func TestDrainBoundedWhenFlushersWedged(t *testing.T) {
	block := make(chan struct{})
	// The fn honors ctx like the real CH driver does — the consumer's
	// FlushTimeout (v0.8.336) is what bounds a server that accepts TCP
	// and never answers.
	c := New[int]("wedge-test", Options{
		BatchSize: 5, BufferSize: 100, FlushInterval: time.Hour, Workers: 1,
		FlushTimeout: time.Second, FlushRetryBase: time.Millisecond,
	}, func(ctx context.Context, b []int) error {
		select {
		case <-block:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	c.Start(ctx)
	for i := 0; i < 40; i++ { // enough to fill flushQ (2×workers) + wedge the worker
		c.Add(i)
	}
	cancel()
	done := make(chan struct{})
	go func() { c.Stop(); close(done) }()
	select {
	case <-done:
		// bounded — good. EVERY item must be accounted for: either counted
		// as a timed-out write (writeFailed) or abandoned at the drain
		// hand-off (dropped) — never silent.
		if c.Dropped()+c.WriteFailed() != 40 {
			t.Fatalf("accounting hole: dropped %d + writeFailed %d != 40 accepted",
				c.Dropped(), c.WriteFailed())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("Stop() hung >20s with a wedged flusher — shutdown must be bounded")
	}
	close(block)
}

// v0.8.340 (HA audit H2) — regression: fail-fast CH outage drained the
// ENTIRE 500k buffer into writeFailed at wire speed. A flush error was
// logged and the batch discarded immediately, so a 30s ClickHouse restart
// (connection refused answers in ms) lost every batch the workers could
// pull — the buffer never buffered. Contract now: flusher retries the SAME
// batch with backoff (bounded attempts) before declaring writeFailed; a
// transient error therefore loses NOTHING, and while retrying, the worker
// slot stays occupied so backpressure propagates naturally.
func TestFlushRetriesTransientErrors(t *testing.T) {
	var calls atomic.Int64
	var delivered atomic.Int64
	c := New[int]("retry-test", Options{
		BatchSize: 10, BufferSize: 100, FlushInterval: 10 * time.Millisecond,
		Workers: 1, FlushTimeout: time.Second, FlushRetryBase: 10 * time.Millisecond,
	}, func(_ context.Context, b []int) error {
		if calls.Add(1) <= 2 {
			return errors.New("transient: connection refused")
		}
		delivered.Add(int64(len(b)))
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	c.Start(ctx)
	for i := 0; i < 10; i++ {
		c.Add(i)
	}
	deadline := time.After(5 * time.Second)
	for delivered.Load() != 10 {
		select {
		case <-deadline:
			t.Fatalf("batch not delivered after transient errors: calls=%d delivered=%d writeFailed=%d",
				calls.Load(), delivered.Load(), c.WriteFailed())
		case <-time.After(20 * time.Millisecond):
		}
	}
	if c.WriteFailed() != 0 {
		t.Fatalf("transient errors must not count writeFailed, got %d", c.WriteFailed())
	}
	cancel()
	c.Stop()
}

// Persistent failure still gives up — after the bounded attempts the batch
// is counted (writeFailed) and the worker moves on, so a poison batch or a
// long outage degrades to counted drop-mode instead of blocking forever.
func TestFlushGivesUpAfterBoundedRetries(t *testing.T) {
	var calls atomic.Int64
	c := New[int]("giveup-test", Options{
		BatchSize: 10, BufferSize: 100, FlushInterval: 10 * time.Millisecond,
		Workers: 1, FlushTimeout: time.Second, FlushRetryBase: 5 * time.Millisecond,
	}, func(_ context.Context, b []int) error {
		calls.Add(1)
		return errors.New("permanent")
	})
	ctx, cancel := context.WithCancel(context.Background())
	c.Start(ctx)
	for i := 0; i < 10; i++ {
		c.Add(i)
	}
	deadline := time.After(5 * time.Second)
	for c.WriteFailed() != 10 {
		select {
		case <-deadline:
			t.Fatalf("writeFailed=%d after %d calls, want 10", c.WriteFailed(), calls.Load())
		case <-time.After(20 * time.Millisecond):
		}
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("attempts = %d, want exactly 3 (1 + 2 retries)", got)
	}
	cancel()
	c.Stop()
}
