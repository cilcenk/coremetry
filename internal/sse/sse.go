// Package sse hosts a tiny in-process event bus + HTTP handler
// that browsers consume via the EventSource API. Replaces the
// 30-second client polling loop on /problems and /anomalies for
// state-change events: a Problem opens / resolves, an anomaly
// fires / clears, and the page updates immediately instead of
// waiting up to 30s for the next poll.
//
// v0.6.3 — optional Redis pub/sub bridge. In a distributed
// deployment (COREMETRY_MODE=worker on one pod, mode=api on
// another) the worker pod fires problem.open via Publish, but
// browsers are connected to api pods' SSE endpoints. Without a
// cross-pod fan-out the event vanishes locally. Set a Bridge via
// SetBridge before any Publish call — every event then also
// rides a Redis PUBLISH so api pods' Subscribe loops re-deliver
// them to local subscribers.
//
// Loops: each pod stamps its own podID into the bridged
// envelope; an inbound event with a matching podID is discarded
// before local fanout. Single-pod deployments without a bridge
// keep the pre-v0.6.3 zero-cost behaviour.
//
// Wire format: standard SSE (text/event-stream). Each message
// is a JSON object { kind, payload } so the client can tell
// "problem opened" from "anomaly cleared" without one endpoint
// per event type.
package sse

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// Bridge is the small slice of cache.Cache the SSE broker needs
// for cross-pod fan-out. Defined here as an interface so the sse
// package doesn't take a dependency on internal/cache (which
// already imports nothing from sse — keeps the layering one-way).
//
// cache.Cache satisfies this naturally because it already
// implements Publish/Subscribe for the L1 invalidation channel.
type Bridge interface {
	Publish(ctx context.Context, channel string, msg []byte) error
	Subscribe(ctx context.Context, channel string) (<-chan []byte, error)
}

// bridgeChannel is the Redis pub/sub key. Fixed name so every
// pod in a distributed deploy lands on the same channel; if an
// operator runs two Coremetry instances against one Redis they
// have to use separate Redis URLs (or DB indices), same as
// every other cache.Publish channel.
const bridgeChannel = "coremetry-events"

// bridgedEvent is the envelope that travels over Redis. PodID
// stamps the publisher so the subscriber can discard its own
// re-delivered events without re-fanout (loop prevention).
type bridgedEvent struct {
	PodID   string          `json:"podId"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Event is the wire envelope. Kind is short ("problem.open",
// "problem.resolve", "anomaly.open", "anomaly.clear") so the
// client switch is one comparison. Payload is opaque JSON the
// receiver decodes if it cares about the details (e.g. the
// problem's service for badge counts).
type Event struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Broker is the in-process pub/sub bus. Producers (evaluator,
// anomaly detector) call Publish; consumers (HTTP handler) call
// Subscribe to get a channel that receives every future event
// until the request context cancels.
//
// Channels are buffered (32) so a slow client doesn't block the
// producer. If the buffer fills (operator opens 100 tabs and
// pauses one) we drop events for that subscriber rather than
// stalling — the client's React Query polling will pick up the
// state on its next refetch.
type Broker struct {
	mu   sync.RWMutex
	subs map[chan<- Event]struct{}

	// v0.6.3 — Redis pub/sub bridge. nil = single-pod behaviour
	// (no cross-pod fan-out, zero overhead). Set via SetBridge
	// before StartBridge.
	bridge Bridge
	podID  string
}

func NewBroker() *Broker {
	return &Broker{
		subs:  map[chan<- Event]struct{}{},
		podID: randomPodID(),
	}
}

// randomPodID generates a short opaque identifier used to stamp
// outgoing bridged events so the bridge's Subscribe loop can
// drop its own re-delivered messages. 8 random bytes = 16 hex
// chars; cluster of ≤ 10 pods has ~0 collision risk over the
// process lifetime. crypto/rand because math/rand was reported
// "predictable" in a past audit and we already pay the syscall
// here exactly once at boot.
func randomPodID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Vanishingly rare — log and fall back to a timestamp-
		// derived id so the bridge still functions (collisions
		// on this path just mean a self-event might be locally
		// re-fanned, harmless aside from a duplicate React Query
		// refetch).
		log.Printf("[sse] randomPodID: %v — falling back to timestamp", err)
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// SetBridge attaches a cross-pod fan-out transport. Pass nil to
// disable (default — single-pod behaviour). Call before
// StartBridge; safe to call once at boot.
func (b *Broker) SetBridge(br Bridge) {
	b.mu.Lock()
	b.bridge = br
	b.mu.Unlock()
}

// StartBridge spins the inbound Redis pub/sub goroutine. Inbound
// messages are decoded and locally fanned out, skipping our own
// pod's re-delivered events.
//
// Outbound publishing is handled inside Publish itself (so the
// caller's single Publish() call still reaches every pod). The
// inbound side is the only thing that needs its own goroutine.
//
// Subscribe failure is non-fatal: we log and stay single-pod.
// The local fanout path keeps working; only cross-pod delivery
// is degraded, and the operator sees the failure in the boot
// log.
func (b *Broker) StartBridge(ctx context.Context) {
	b.mu.RLock()
	br := b.bridge
	b.mu.RUnlock()
	if br == nil {
		return
	}
	in, err := br.Subscribe(ctx, bridgeChannel)
	if err != nil {
		log.Printf("[sse] redis bridge subscribe: %v — cross-pod events disabled", err)
		return
	}
	log.Printf("[sse] redis bridge active (pod=%s channel=%s)", b.podID, bridgeChannel)
	go func() {
		for raw := range in {
			var env bridgedEvent
			if err := json.Unmarshal(raw, &env); err != nil {
				log.Printf("[sse] bridge decode: %v", err)
				continue
			}
			if env.PodID == b.podID {
				// Echo of our own Publish — already locally fanned
				// out at the producer side. Drop to prevent loops.
				continue
			}
			b.localFanout(Event{Kind: env.Kind, Payload: env.Payload})
		}
	}()
}

// localFanout delivers an event to the in-process subscribers
// only (no bridge republish). Used by both Publish (after
// optionally going over the bridge) and the bridge's inbound
// goroutine (when relaying a peer's event).
func (b *Broker) localFanout(ev Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
			// Subscriber buffer full — drop. The eventual
			// React Query refetch (10-30s) covers the gap.
		}
	}
}

// Subscribe registers a channel for events. Returns a function
// that removes the subscription — caller defers it.
func (b *Broker) Subscribe(ch chan<- Event) func() {
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// Publish fans the event out to local subscribers, and (when a
// bridge is attached) PUBLISHes it on Redis so peer pods can do
// the same. Non-blocking on both paths; a slow consumer drops
// the event rather than back-pressuring the producer (which
// would block the entire alert evaluator tick).
func (b *Broker) Publish(kind string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[sse] marshal payload: %v", err)
		return
	}
	ev := Event{Kind: kind, Payload: raw}
	b.localFanout(ev)

	// v0.6.3 — cross-pod fan-out. Bridge is read under the same
	// mu the subscribers use; safe because SetBridge is a one-
	// shot at boot.
	b.mu.RLock()
	br := b.bridge
	b.mu.RUnlock()
	if br == nil {
		return
	}
	env := bridgedEvent{PodID: b.podID, Kind: kind, Payload: raw}
	envRaw, err := json.Marshal(env)
	if err != nil {
		log.Printf("[sse] bridge marshal: %v", err)
		return
	}
	// 200ms cap so a wedged Redis (rare; the cache.Cache health
	// would already be flagging it) doesn't stall the producer
	// goroutine on every publish. Cross-pod delivery is best-
	// effort by design — clients still poll as the safety net.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := br.Publish(ctx, bridgeChannel, envRaw); err != nil {
		log.Printf("[sse] bridge publish: %v", err)
	}
}

// Handler returns an http.Handler that streams events to the
// client over text/event-stream. Sends a comment heartbeat
// every 15s so intermediate proxies (NGINX, Cloudflare) don't
// time out the connection — many default to 60s idle.
//
// Auth flow: the auth.Middleware in front of the mux already
// enforces JWT/cookie. By the time we hit Handler the user is
// authenticated; we don't need to re-check.
func Handler(b *Broker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("X-Accel-Buffering", "no")  // disable NGINX buffering
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		ch := make(chan Event, 32)
		unsub := b.Subscribe(ch)
		defer unsub()

		// Initial comment so EventSource considers the
		// connection open and the client's onopen fires
		// immediately, even if no events have been published
		// yet. Without this the operator sees "connecting…"
		// until the first real event lands, which on a quiet
		// Sunday could be hours.
		fmt.Fprint(w, ": ok\n\n")
		flusher.Flush()

		heartbeat := time.NewTicker(15 * time.Second)
		defer heartbeat.Stop()

		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case <-heartbeat.C:
				fmt.Fprint(w, ": ping\n\n")
				flusher.Flush()
			case ev := <-ch:
				if data, err := json.Marshal(ev); err == nil {
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Kind, data)
					flusher.Flush()
				}
			}
		}
	})
}
