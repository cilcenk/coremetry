package consumer

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

type Options struct {
	BatchSize     int
	BufferSize    int
	FlushInterval time.Duration
	Workers       int
}

// Consumer is a generic, channel-based batch consumer with backpressure.
// Multiple goroutines can call Add concurrently; a single reader loop
// accumulates items and flushes when batchSize is reached or FlushInterval fires.
type Consumer[T any] struct {
	name    string
	opts    Options
	ch      chan T
	flushFn func(ctx context.Context, batch []T) error
	wg      sync.WaitGroup
	dropped atomic.Int64
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
		return true
	default:
		c.dropped.Add(1)
		return false
	}
}

func (c *Consumer[T]) Start(ctx context.Context) {
	c.wg.Add(1)
	go c.loop(ctx)
}

func (c *Consumer[T]) loop(ctx context.Context) {
	defer c.wg.Done()
	ticker := time.NewTicker(c.opts.FlushInterval)
	defer ticker.Stop()

	batch := make([]T, 0, c.opts.BatchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		b := batch
		batch = make([]T, 0, c.opts.BatchSize)
		if err := c.flushFn(ctx, b); err != nil {
			log.Printf("[consumer/%s] flush error: %v", c.name, err)
		}
	}

	for {
		select {
		case item := <-c.ch:
			batch = append(batch, item)
			if len(batch) >= c.opts.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			// drain remaining
		drain:
			for {
				select {
				case item := <-c.ch:
					batch = append(batch, item)
					if len(batch) >= c.opts.BatchSize {
						flush()
					}
				default:
					break drain
				}
			}
			flush()
			return
		}
	}
}

// Stop waits for the consumer loop to finish after context cancellation.
func (c *Consumer[T]) Stop() {
	c.wg.Wait()
	if n := c.dropped.Load(); n > 0 {
		log.Printf("[consumer/%s] dropped %d items (buffer was full)", c.name, n)
	}
}

func (c *Consumer[T]) QueueLen() int  { return len(c.ch) }
func (c *Consumer[T]) Dropped() int64 { return c.dropped.Load() }
