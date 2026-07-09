package api

import (
	"context"
	"hash/fnv"
	"log"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cilcenk/coremetry/internal/logstore"
)

// v0.8.236 — shared live-tail broadcaster. Previously EVERY connected
// /logs live tab drove its own 10s forward-read against the logs
// backend, so N tabs on the same filter = N× identical ES/CH queries
// (O(tabs) backend load — against the operator's standing "keep ES
// request volume low" preference). The broker runs ONE poll loop per
// DISTINCT filter set and fans the rows out to every subscriber:
// backend load becomes O(distinct filters) per pod, and the common
// case (several engineers / a NOC wall on the same view) collapses to
// a single query per tick.
//
// Pod-local by design: cross-pod dedup would need a shared bus for
// marginal gain — per-pod-per-filter is already the right order.

// tailEvent is one broadcast unit: a batch of new rows and/or a gap
// marker ("some rows were skipped — you fell behind").
type tailEvent struct {
	logs []*logstore.LogRecord
	gap  bool
}

// tailSub is one SSE connection's mailbox. The channel is bounded so a
// stalled connection can never block the group's broadcast; when a
// send would block we drop the batch for THAT subscriber and deliver a
// gap marker with the next event instead (no silent loss).
type tailSub struct {
	ch         chan tailEvent
	overflowed bool // guarded by the owning group's mu
}

// tailSubBuf is the per-subscriber event buffer. Each event is a whole
// tick batch, so 8 buffered events ≈ 80s of backlog tolerance before a
// slow client starts seeing gap markers.
const tailSubBuf = 8

// tailFilterKey identifies a poll group. Hashes ALL the filter inputs
// the tail endpoint accepts (the v0.5.187 rule: never a partial/length
// digest — two different filters must never share a group). SinceNs is
// deliberately NOT part of the key: the cursor belongs to the group,
// per-client catch-up is handled at subscribe time.
func tailFilterKey(f logstore.Filter) string {
	h := fnv.New64a()
	for _, s := range []string{
		f.Service, f.Cluster, f.Env, // Env: v0.8.400 — env-separation Phase 4
		f.Search,
		strconv.Itoa(int(f.SeverityMin)),
		f.TraceID, f.SpanID,
		strconv.FormatBool(f.HasTrace), // v0.8.406 — trace-only filter
	} {
		h.Write([]byte(s))
		h.Write([]byte{0}) // field separator so ("ab","c") != ("a","bc")
	}
	return strconv.FormatUint(h.Sum64(), 16)
}

type tailGroup struct {
	base   logstore.Filter
	cancel context.CancelFunc
	// cursor mirrors the poll loop's sinceNs so subscribe-time catch-up
	// reads know where the shared stream will resume. Atomic because
	// the poll goroutine writes it while subscribers read it.
	cursor atomic.Int64

	mu   sync.Mutex
	subs map[*tailSub]struct{}
	refs int
}

type tailBroker struct {
	logs logstore.Store // the Switchable — backend swaps propagate per tick

	mu     sync.Mutex
	groups map[string]*tailGroup
}

func newTailBroker(logs logstore.Store) *tailBroker {
	return &tailBroker{logs: logs, groups: map[string]*tailGroup{}}
}

// subscribe attaches a new SSE connection to the (possibly freshly
// created) poll group for f. Returns the subscriber mailbox, the
// group's current cursor (for catch-up bounding), and the detach
// function — ALWAYS call it; the last detach stops the poll goroutine.
func (b *tailBroker) subscribe(f logstore.Filter) (*tailSub, int64, func()) {
	key := tailFilterKey(f)
	b.mu.Lock()
	g := b.groups[key]
	if g == nil {
		ctx, cancel := context.WithCancel(context.Background())
		g = &tailGroup{base: f, cancel: cancel, subs: map[*tailSub]struct{}{}}
		g.cursor.Store(time.Now().UnixNano())
		b.groups[key] = g
		go b.run(ctx, g)
	}
	sub := &tailSub{ch: make(chan tailEvent, tailSubBuf)}
	g.mu.Lock()
	g.subs[sub] = struct{}{}
	g.refs++
	g.mu.Unlock()
	b.mu.Unlock()

	var once sync.Once
	detach := func() {
		once.Do(func() {
			b.mu.Lock()
			g.mu.Lock()
			delete(g.subs, sub)
			g.refs--
			last := g.refs == 0
			g.mu.Unlock()
			if last {
				g.cancel()
				delete(b.groups, key)
			}
			b.mu.Unlock()
		})
	}
	return sub, g.cursor.Load(), detach
}

// run is the group's poll loop — the exact per-connection loop
// streamLogs used to run, now shared. Cursor semantics (tailStep:
// inclusive >= re-read, boundary-id dedup, gap on saturation) are
// unchanged and stay pinned by the tailStep unit tests.
func (b *tailBroker) run(ctx context.Context, g *tailGroup) {
	sinceNs := g.cursor.Load()
	boundary := map[int64]struct{}{}
	tick := time.NewTicker(logsTailCadence)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			tickCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
			f := g.base
			f.SinceNs = sinceNs
			page, err := b.logs.Search(tickCtx, f)
			cancel()
			if err != nil {
				// Transient backend error — keep the group alive, retry
				// next tick. One log line per group per tick, not per tab.
				log.Printf("[logs-stream] shared tail failed (backend=%s): %v", b.logs.Backend(), err)
				continue
			}
			emit, nextSince, nextBoundary, gap := tailStep(sinceNs, boundary, page.Logs, logstore.LogsTailMax)
			sinceNs, boundary = nextSince, nextBoundary
			g.cursor.Store(sinceNs)
			if len(emit) == 0 && !gap {
				continue
			}
			g.broadcast(tailEvent{logs: emit, gap: gap})
		}
	}
}

// broadcast fans one event to every subscriber without ever blocking:
// a full mailbox marks the subscriber overflowed, and its next
// delivered event carries gap=true so the client shows "fell behind"
// instead of silently missing lines.
func (g *tailGroup) broadcast(ev tailEvent) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for sub := range g.subs {
		out := ev
		if sub.overflowed {
			out.gap = true
		}
		select {
		case sub.ch <- out:
			sub.overflowed = false
		default:
			sub.overflowed = true
		}
	}
}
