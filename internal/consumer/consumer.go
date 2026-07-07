package consumer

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// drainHandoffTimeout bounds the shutdown-drain hand-off to the flush
// stage (v0.8.336): long enough for a slow-but-alive ClickHouse insert
// to free a slot, short enough that a wedged flush stage can't hold the
// pod past terminationGracePeriod.
const drainHandoffTimeout = 5 * time.Second

type Options struct {
	BatchSize     int
	BufferSize    int
	FlushInterval time.Duration
	// Workers is the number of parallel flushers consuming the
	// dispatch channel. Each worker calls flushFn independently so
	// a slow ClickHouse insert no longer back-pressures item
	// accumulation. Defaults to 1 when unset for back-compat.
	Workers int
	// FlushTimeout bounds each flushFn call (v0.8.336). The old
	// context.Background() assumed CH's server-side
	// max_execution_time=60 bounds the insert — but a wedged server
	// that ACCEPTS the connection and never answers is bounded only
	// by the driver's 300s default ReadTimeout, and shutdown (Stop
	// waits on flushers) inherited whichever was worse. Defaults 60s.
	FlushTimeout time.Duration
	// FlushRetryBase is the first retry backoff (v0.8.340, HA audit
	// H2); the second retry waits 4×. Zero = the 2s production
	// default. Exposed mainly so tests run in milliseconds.
	FlushRetryBase time.Duration
	// ByteBudget caps the APPROXIMATE bytes this consumer may hold
	// across its whole pipeline — channel backlog + accumulating
	// batch + dispatched batches + batches in-flight in flushFn
	// (v0.8.355, HA audit 🟡#1). BufferSize alone bounds ITEMS: a
	// Java fleet emitting 15-25KB stack-trace log bodies could park
	// multi-GB behind a stalled-but-alive ClickHouse (batches occupy
	// workers for minutes under the v0.8.340 retry semantics) and get
	// the pod OOMKilled — destroying ALL buffered signals, worse than
	// the counted drops this budget produces instead. 0 = disabled.
	// Only enforced when the consumer was built with NewSized; a nil
	// sizer leaves behavior byte-identical to pre-v0.8.355.
	ByteBudget int64
}

// flushAttempts is the total tries per batch (1 + 2 retries,
// v0.8.340). A fail-fast CH outage used to drain the ENTIRE buffer
// into writeFailed at wire speed — the flusher logged and discarded
// each batch in milliseconds, so the 500k-item buffer never buffered.
// Retrying the SAME batch keeps the worker slot occupied, which is
// exactly the backpressure that lets the buffer absorb a short outage;
// a persistent failure still degrades to COUNTED drop-mode.
const flushAttempts = 3

// Consumer is a generic, channel-based batch consumer with
// backpressure plus a parallel flush stage. Producers call Add
// concurrently → single reader loop accumulates batches → those
// batches are handed off to a pool of `Workers` flushers via a
// dispatch channel. Decoupling accumulation from CH insert latency
// is critical at 1B spans/day: a 200ms CH stall must not stall the
// goroutine reading from the OTLP receiver.
// sizedBatch carries a dispatched batch together with its byte sum so
// the flusher can release the byte budget without re-sizing every item
// (v0.8.355). bytes is 0 when byte accounting is disabled.
type sizedBatch[T any] struct {
	items []T
	bytes int64
}

type Consumer[T any] struct {
	name    string
	opts    Options
	ch      chan T
	flushQ  chan sizedBatch[T] // dispatched batches awaiting a flusher
	flushFn func(ctx context.Context, batch []T) error
	// sizeOf estimates one item's in-memory bytes (v0.8.355). Set via
	// NewSized; nil disables byte accounting entirely (New keeps the
	// count-only contract). Must be cheap and allocation-free — it runs
	// on the Add hot path AND once more per item in the batch loop.
	sizeOf func(T) int
	wg     sync.WaitGroup
	// bufBytes is the approximate byte total currently inside the
	// pipeline (channel + accumulating batch + flushQ + in-flight
	// flushes). Incremented by Add on acceptance, decremented when a
	// batch LEAVES — flush completion (success or writeFailed) and
	// drain-abandon. Approximate by design: concurrent Adds may
	// overshoot ByteBudget by a few items; the budget is an OOM safety
	// valve, not a metering system.
	bufBytes atomic.Int64
	dropped  atomic.Int64
	// writeFailed counts items lost because flushFn (the ClickHouse insert)
	// errored — the batch is logged and discarded, never retried, so this is
	// silent data loss the operator otherwise can't see. Surfaced on
	// /admin/stats (v0.8.x). Distinct from `dropped` (receiver buffer full).
	writeFailed atomic.Int64
	// draining flips when shutdown-drain begins (v0.8.340): flushers
	// then make a SINGLE attempt per remaining batch — retry-with-
	// backoff against a wedged CH would multiply the teardown time per
	// batch and blow the pod's terminationGracePeriod (getting
	// SIGKILLed mid-drain loses more than skipping retries does).
	draining atomic.Bool
	// accepted is a monotonic counter of items the consumer received
	// (including ones it later dropped from the channel-full path —
	// well, actually NO, dropped items never enter; this counts only
	// queued items). Status page samples this to compute ingest rate.
	accepted atomic.Int64
}

func New[T any](name string, opts Options, flushFn func(context.Context, []T) error) *Consumer[T] {
	return &Consumer[T]{
		name:    name,
		opts:    opts,
		ch:      make(chan T, opts.BufferSize),
		flushFn: flushFn,
	}
}

// NewSized is New plus a per-item byte sizer, enabling the
// Options.ByteBudget cap (v0.8.355, HA audit 🟡#1). sizeOf estimates
// one item's in-memory footprint — cheap string-length sums, never
// allocations (it runs on the Add hot path). A nil sizeOf or a zero
// ByteBudget leaves the consumer byte-identical to New.
func NewSized[T any](name string, opts Options, sizeOf func(T) int, flushFn func(context.Context, []T) error) *Consumer[T] {
	c := New(name, opts, flushFn)
	c.sizeOf = sizeOf
	return c
}

// sizingEnabled reports whether byte accounting is active — both a
// sizer AND a positive budget are required.
func (c *Consumer[T]) sizingEnabled() bool {
	return c.sizeOf != nil && c.opts.ByteBudget > 0
}

// Add enqueues an item. Returns false if the buffer is full — by item
// count OR byte budget (v0.8.355) — and counts the drop either way.
func (c *Consumer[T]) Add(item T) bool {
	var sz int64
	if c.sizingEnabled() {
		// Reserve-then-send: the atomic Add is the reservation, so
		// concurrent producers can't all pass a stale Load() check.
		sz = int64(c.sizeOf(item))
		if c.bufBytes.Add(sz) > c.opts.ByteBudget {
			c.bufBytes.Add(-sz)
			c.dropped.Add(1)
			return false
		}
	}
	select {
	case c.ch <- item:
		c.accepted.Add(1)
		return true
	default:
		if sz != 0 {
			c.bufBytes.Add(-sz)
		}
		c.dropped.Add(1)
		return false
	}
}

func (c *Consumer[T]) Start(ctx context.Context) {
	workers := c.opts.Workers
	if workers < 1 {
		workers = 1
	}
	// Dispatch buffer of 2× workers so the loop can stage one batch
	// per worker plus an in-flight one without blocking on a slow
	// flusher; deeper buffering would just delay backpressure
	// without helping throughput.
	c.flushQ = make(chan sizedBatch[T], workers*2)

	c.wg.Add(1)
	go c.loop(ctx)
	for i := 0; i < workers; i++ {
		c.wg.Add(1)
		go c.flusher()
	}
}

// loop drains ch into batches and dispatches each to flushQ. Runs
// in its own goroutine; never calls flushFn directly so insert
// latency cannot back-pressure the read side.
func (c *Consumer[T]) loop(ctx context.Context) {
	defer c.wg.Done()
	ticker := time.NewTicker(c.opts.FlushInterval)
	defer ticker.Stop()

	batch := make([]T, 0, c.opts.BatchSize)
	// batchBytes mirrors the sizer sum of `batch` (v0.8.355) — computed
	// here (single-threaded, no contention) by re-running the cheap
	// sizer per item, and carried through flushQ so the flusher can
	// release the budget without touching item contents again.
	var batchBytes int64
	sized := c.sizingEnabled()

	// send hands one batch to the flush stage. v0.8.336 (HA audit H1):
	// the old single `select { flushQ | ctx.Done }` coin-flipped healthy
	// final batches into the void during shutdown — with ctx ALREADY
	// cancelled in the drain path, Go picks uniformly among ready cases,
	// so ~50% of last batches were silently discarded on every deploy
	// (uncounted). Two-stage shape instead: the first select is the
	// normal backpressure point; once ctx is done we fall through to a
	// BOUNDED blocking hand-off — healthy flushers always get the batch,
	// and a fully wedged flush stage can't hang shutdown (loss COUNTED).
	send := func(b sizedBatch[T]) {
		select {
		case c.flushQ <- b:
			return
		case <-ctx.Done():
		}
		select {
		case c.flushQ <- b:
		case <-time.After(drainHandoffTimeout):
			c.dropped.Add(int64(len(b.items)))
			// Abandoned items LEAVE the pipeline here — release their
			// byte reservation (v0.8.355) or the budget leaks shut.
			c.bufBytes.Add(-b.bytes)
			log.Printf("[consumer/%s] drain: flush stage wedged — abandoned %d items after %s",
				c.name, len(b.items), drainHandoffTimeout)
		}
	}
	dispatch := func() {
		if len(batch) == 0 {
			return
		}
		b := sizedBatch[T]{items: batch, bytes: batchBytes}
		batch = make([]T, 0, c.opts.BatchSize)
		batchBytes = 0
		send(b)
	}
	accumulate := func(item T) {
		batch = append(batch, item)
		if sized {
			batchBytes += int64(c.sizeOf(item))
		}
	}

	for {
		select {
		case item := <-c.ch:
			accumulate(item)
			if len(batch) >= c.opts.BatchSize {
				dispatch()
			}
		case <-ticker.C:
			dispatch()
		case <-ctx.Done():
			// Single-attempt mode BEFORE the drain dispatches below —
			// misplacing this after the loop re-introduces 3×-retry
			// teardown times against a wedged CH (v0.8.340).
			c.draining.Store(true)
			// drain any remaining items, then close so flushers exit.
		drain:
			for {
				select {
				case item := <-c.ch:
					accumulate(item)
					if len(batch) >= c.opts.BatchSize {
						dispatch()
					}
				default:
					break drain
				}
			}
			dispatch()
			close(c.flushQ)
			return
		}
	}
}

// flusher reads dispatched batches and runs flushFn. Detached from
// the consumer's context so a shutdown-triggered drain still gets the
// final batches written — but BOUNDED per call (v0.8.336): a wedged
// ClickHouse that accepts TCP and never answers used to hold flushers
// (and therefore Stop()) for the driver's 300s default ReadTimeout.
func (c *Consumer[T]) flusher() {
	defer c.wg.Done()
	timeout := c.opts.FlushTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	retryBase := c.opts.FlushRetryBase
	if retryBase <= 0 {
		retryBase = 2 * time.Second
	}
	for b := range c.flushQ {
		attempts := flushAttempts
		if c.draining.Load() {
			attempts = 1
		}
		var err error
		for attempt := 1; attempt <= attempts; attempt++ {
			// Fresh bounded ctx per attempt — a timed-out first try must
			// not eat the retries' budget.
			fctx, cancel := context.WithTimeout(context.Background(), timeout)
			err = c.flushFn(fctx, b.items)
			cancel()
			if err == nil {
				break
			}
			if attempt < attempts {
				backoff := retryBase * time.Duration(1<<(2*(attempt-1))) // base, 4×base
				log.Printf("[consumer/%s] flush attempt %d/%d failed (%v) — retrying in %s",
					c.name, attempt, attempts, err, backoff)
				time.Sleep(backoff)
			}
		}
		if err != nil {
			c.writeFailed.Add(int64(len(b.items)))
			log.Printf("[consumer/%s] flush error after %d attempts (%d items lost): %v",
				c.name, attempts, len(b.items), err)
		}
		// The batch has LEFT the pipeline — written or counted lost —
		// so release its byte reservation (v0.8.355). Deliberately
		// AFTER the retry loop: a batch occupying a worker for minutes
		// against a stalled CH is exactly the memory the budget exists
		// to bound. No-op (bytes==0) when accounting is disabled.
		c.bufBytes.Add(-b.bytes)
	}
}

// Stop waits for the consumer loop and all flushers to finish after
// context cancellation.
func (c *Consumer[T]) Stop() {
	c.wg.Wait()
	if n := c.dropped.Load(); n > 0 {
		log.Printf("[consumer/%s] dropped %d items (buffer was full)", c.name, n)
	}
	if n := c.writeFailed.Load(); n > 0 {
		log.Printf("[consumer/%s] lost %d items to flush errors", c.name, n)
	}
	// Non-zero after a full drain means items raced into the channel
	// after the drain loop emptied it (their bytes were reserved but
	// never dispatched) — harmless at exit, but worth a line so an
	// accounting leak would be visible (v0.8.355).
	if c.sizingEnabled() {
		if n := c.bufBytes.Load(); n != 0 {
			log.Printf("[consumer/%s] %d bytes still accounted at stop", c.name, n)
		}
	}
}

func (c *Consumer[T]) QueueLen() int  { return len(c.ch) }
func (c *Consumer[T]) Capacity() int  { return cap(c.ch) }
func (c *Consumer[T]) Dropped() int64 { return c.dropped.Load() }

// BufferedBytes returns the approximate bytes currently held across the
// consumer's pipeline (channel + batches + in-flight flushes), per the
// NewSized sizer. Always 0 when byte accounting is disabled (v0.8.355).
func (c *Consumer[T]) BufferedBytes() int64 { return c.bufBytes.Load() }

// WriteFailed returns the cumulative count of items lost because the
// ClickHouse insert (flushFn) errored — the batch was discarded, not
// retried. Surfaced on /admin/stats as the "write-failed" data-loss class.
func (c *Consumer[T]) WriteFailed() int64 { return c.writeFailed.Load() }

// Accepted returns the cumulative count of items that were successfully
// queued. Sampled twice over a known interval to compute an ingest rate.
func (c *Consumer[T]) Accepted() int64 { return c.accepted.Load() }
