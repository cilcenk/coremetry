// entrySpans — the single definition of "this service's own work".
//
// v0.9.240. A service's response time must be measured over the spans where
// the service ANSWERS something, not over every span it emits. The two
// populations differ by orders of magnitude and volume: a request handler
// runs for hundreds of milliseconds and happens once, while the DB calls it
// makes run for a fraction of a millisecond and happen dozens of times. Mix
// them and the client spans win the median by sheer count.
//
// Measured on one service, one hour, our own demo data:
//   all spans              6167 spans   p50  60ms
//   server + consumer       929 spans   p50 807ms
// A 13× difference in the number the operator reads as "how slow is this
// service". In a production service that fans out to a database the ratio is
// far worse — an operator-reported case showed a 1ms median against a 99ms
// p99, which is not a tail, it is two populations in one chart.
//
// server  — an inbound request this service handled.
// consumer — a queue message this service processed. Also an entry point:
//   the work is genuinely this service's, so a Kafka-consumer service must
//   not read as having no traffic. This is why the earlier, narrower fix
//   (v0.9.129, excluding messaging.system = kafka) was not enough: it
//   removed producer noise but left every DB client span in, and it removed
//   consumer work that legitimately belongs to the service.
//
// client / producer / internal are deliberately OUT: they are calls this
// service MAKES. They belong in the dependency and database views, which
// already break them out per operation.
export const ENTRY_SPAN_KINDS = ['server', 'consumer'] as const;

/** DSL fragment (the parser supports `AND` + `IN [a, b]`, not `OR`). */
export const ENTRY_SPAN_DSL = `kind in [${ENTRY_SPAN_KINDS.join(', ')}]`;

/** Escape a service name for embedding in a double-quoted DSL literal. */
export function dslService(service: string): string {
  return `service.name = "${service.replace(/"/g, '\\"')}"`;
}

/**
 * Latency DSL for a service: its own work only.
 * Callers MUST handle the empty case — a pure batch/producer service has no
 * entry spans at all, and silently showing nothing would be worse than the
 * distortion this replaces. See Overview's fallback to the all-span series.
 */
export function entryLatencyDSL(service: string): string {
  return `${dslService(service)} AND ${ENTRY_SPAN_DSL}`;
}
