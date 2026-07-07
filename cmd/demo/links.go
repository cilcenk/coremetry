// Coremetry demo — cross-trace Kafka span links (v0.8.335).
//
// The cross-signal "Linked traces" pivot (v0.8.329-333) reads the
// span_links table, but until now the demo never emitted OTLP
// Span.Links, so the pivot had nothing real to show. This file gives
// it data the way a real Kafka instrumentation would: every producer
// span records itself into a per-topic ring, and consumer spans link
// back to RECENT producer spans from OTHER traces on the same topic —
// exactly the batch-consume shape (one poll processes messages whose
// producers live in earlier, separate traces) that makes span links
// exist as a concept.
//
// The ring is deliberately process-global and survives across traces —
// that is the point: links cross trace boundaries. Memory is strictly
// bounded: 64 refs per topic, and the topic set is the small fixed set
// of kafka-pub hops in the scenario tables.
package main

import (
	mrand "math/rand/v2"
	"sync"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// linkRingCap bounds each topic's ring — old producer refs are evicted
// FIFO. 64 × ~40 bytes × a dozen topics is negligible.
const linkRingCap = 64

// spanRef is one recorded producer span: just enough identity to build
// an OTLP link (the ingester's invalid-link gate only needs the ids to
// be non-zero, which randID guarantees).
type spanRef struct {
	traceID []byte
	spanID  []byte
}

// linkRing is a mutex-guarded per-topic ring buffer of recent producer
// spans. One shared instance (kafkaLinks) serves the table-driven mesh
// hops and the hand-wired scenarios alike.
type linkRing struct {
	mu      sync.Mutex
	byTopic map[string][]spanRef
}

// kafkaLinks is the process-global recorder: every kafka.publish span
// records into it, every kafka.consume span pulls link targets from it.
var kafkaLinks = &linkRing{byTopic: map[string][]spanRef{}}

// record remembers a producer span for `topic`, evicting the oldest
// entry beyond linkRingCap.
func (r *linkRing) record(topic string, traceID, spanID []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	q := append(r.byTopic[topic], spanRef{traceID: traceID, spanID: spanID})
	if len(q) > linkRingCap {
		q = q[len(q)-linkRingCap:]
	}
	r.byTopic[topic] = q
}

// pick returns up to max OTLP links to the newest producer spans on
// `topic`, skipping entries from the consumer's own trace (self) —
// self-links are dropped by the UI anyway, so emitting them would just
// be noise. Newest-first matches Kafka reality: a consumer poll
// processes the messages published most recently.
func (r *linkRing) pick(topic string, self []byte, max int) []*tracepb.Span_Link {
	r.mu.Lock()
	defer r.mu.Unlock()
	q := r.byTopic[topic]
	var out []*tracepb.Span_Link
	for i := len(q) - 1; i >= 0 && len(out) < max; i-- {
		if string(q[i].traceID) == string(self) {
			continue
		}
		out = append(out, &tracepb.Span_Link{
			TraceId: q[i].traceID,
			SpanId:  q[i].spanID,
			Attributes: mapToKVs(kv(
				"messaging.system", "kafka",
				"messaging.destination.name", topic,
			)),
		})
	}
	return out
}

// maybe rolls the ~70% odds that a consumer span carries links at all
// (so the pivot data isn't uniform), then picks 1-2 targets. Plain
// mrand, NOT rollFail: links are correlation metadata, not latency or
// failure, so the DEMO-REALISM load-model contract (dur/rollFail)
// deliberately does not apply here.
func (r *linkRing) maybe(topic string, self []byte) []*tracepb.Span_Link {
	if mrand.IntN(100) >= 70 {
		return nil
	}
	return r.pick(topic, self, 1+mrand.IntN(2))
}
