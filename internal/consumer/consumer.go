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
}

// Consumer is a generic, channel-based batch consumer with
// backpressure plus a parallel flush stage. Producers call Add
// concurrently → single reader loop accumulates batches → those
// batches are handed off to a pool of `Workers` flushers via a
// dispatch channel. Decoupling accumulation from CH insert latency
// is critical at 1B spans/day: a 200ms CH stall must not stall the
// goroutine reading from the OTLP receiver.
type Consumer[T any] struct {
	name    string
	opts    Options
	ch      chan T
	flushQ  chan []T // dispatched batches awaiting a flusher
	flushFn func(ctx context.Context, batch []T) error
	wg      sync.WaitGroup
	dropped atomic.Int64
	// writeFailed counts items lost because flushFn (the ClickHouse insert)
	// errored — the batch is logged and discarded, never retried, so this is
	// silent data loss the operator otherwise can't see. Surfaced on
	// /admin/stats (v0.8.x). Distinct from `dropped` (receiver buffer full).
	writeFailed atomic.Int64
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

// Add enqueues an item. Returns false if the buffer is full (item is dropped).
func (c *Consumer[T]) Add(item T) bool {
	select {
	case c.ch <- item:
		c.accepted.Add(1)
		return true
	default:
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
	c.flushQ = make(chan []T, workers*2)

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

	// send hands one batch to the flush stage. v0.8.336 (HA audit H1):
	// the old single `select { flushQ | ctx.Done }` coin-flipped healthy
	// final batches into the void during shutdown — with ctx ALREADY
	// cancelled in the drain path, Go picks uniformly among ready cases,
	// so ~50% of last batches were silently discarded on every deploy
	// (uncounted). Two-stage shape instead: the first select is the
	// normal backpressure point; once ctx is done we fall through to a
	// BOUNDED blocking hand-off — healthy flushers always get the batch,
	// and a fully wedged flush stage can't hang shutdown (loss COUNTED).
	send := func(b []T) {
		select {
		case c.flushQ <- b:
			return
		case <-ctx.Done():
		}
		select {
		case c.flushQ <- b:
		case <-time.After(drainHandoffTimeout):
			c.dropped.Add(int64(len(b)))
			log.Printf("[consumer/%s] drain: flush stage wedged — abandoned %d items after %s",
				c.name, len(b), drainHandoffTimeout)
		}
	}
	dispatch := func() {
		if len(batch) == 0 {
			return
		}
		b := batch
		batch = make([]T, 0, c.opts.BatchSize)
		send(b)
	}

	for {
		select {
		case item := <-c.ch:
			batch = append(batch, item)
			if len(batch) >= c.opts.BatchSize {
				dispatch()
			}
		case <-ticker.C:
			dispatch()
		case <-ctx.Done():
			// drain any remaining items, then close so flushers exit.
		drain:
			for {
				select {
				case item := <-c.ch:
					batch = append(batch, item)
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
	for batch := range c.flushQ {
		fctx, cancel := context.WithTimeout(context.Background(), timeout)
		err := c.flushFn(fctx, batch)
		cancel()
		if err != nil {
			c.writeFailed.Add(int64(len(batch)))
			log.Printf("[consumer/%s] flush error (%d items lost): %v", c.name, len(batch), err)
		}
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
}

func (c *Consumer[T]) QueueLen() int  { return len(c.ch) }
func (c *Consumer[T]) Capacity() int  { return cap(c.ch) }
func (c *Consumer[T]) Dropped() int64 { return c.dropped.Load() }

// WriteFailed returns the cumulative count of items lost because the
// ClickHouse insert (flushFn) errored — the batch was discarded, not
// retried. Surfaced on /admin/stats as the "write-failed" data-loss class.
func (c *Consumer[T]) WriteFailed() int64 { return c.writeFailed.Load() }

// Accepted returns the cumulative count of items that were successfully
// queued. Sampled twice over a known interval to compute an ingest rate.
func (c *Consumer[T]) Accepted() int64 { return c.accepted.Load() }
