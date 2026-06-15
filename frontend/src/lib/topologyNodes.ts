// Infra topology nodes are encoded as strings in `childNode` / node ids:
//   db:<system>            | db:<system>@<host>
//   queue:<system>         | queue:<system>@<host> | queue:<system>:<topic>
//   ext:<service>
//
// v0.7.31 — the queue form gained a `:<topic>` segment so each Kafka topic is a
// distinct node (bsa.kafka.core.cache.refresh stops collapsing every topic on a
// broker into one queue:kafka hairball). These pure helpers parse + label the
// strings; everything else treats childNode as an opaque id.

// infraNodeSystem returns the messaging/db SYSTEM (kafka, postgresql, …) from a
// node name, ignoring any @host or :topic suffix. Per-instance breakdowns are
// system-scoped, so the edge-instances panel queries on this.
//
// Regression guard: pre-v0.7.31 the caller did
// `childNode.slice(childNode.indexOf(':') + 1)`, which for the new
// `queue:kafka:topic` form wrongly yielded "kafka:topic" and broke the
// instances query. This extracts just the system.
export function infraNodeSystem(childNode: string): string {
  const colon = childNode.indexOf(':');
  if (colon < 0) return '';
  const rest = childNode.slice(colon + 1); // "kafka:topic" | "kafka@host" | "postgresql"
  return rest.split(':')[0].split('@')[0]; // → "kafka" | "postgresql"
}

// infraNodeLabel returns the human-readable label shown in the diagram for an
// infra node: the topic for a topic-scoped queue (the operationally meaningful
// identity), else the system (+host). The node's kind icon already conveys
// db/queue/external, so the prefix is dropped.
export function infraNodeLabel(name: string): string {
  if (name.startsWith('queue:')) {
    const rest = name.slice('queue:'.length); // "kafka:topic" | "kafka@host" | "kafka"
    const c = rest.indexOf(':');
    return c >= 0 ? rest.slice(c + 1) : rest; // topic, else system(+host)
  }
  if (name.startsWith('db:')) return name.slice('db:'.length);
  if (name.startsWith('ext:')) return name.slice('ext:'.length);
  return name;
}

// ── node-size encoding (v0.8.x — Uptrace service-graph adapt, slice 2) ──────
//
// mapNumber linearly maps `value` from [inMin,inMax] onto [outMin,outMax] and
// CLAMPS the result to [outMin,outMax]. A degenerate input range (inMax<=inMin,
// e.g. every node has the same metric, or max=0) collapses to outMin so a flat
// graph renders at the minimum size rather than dividing by zero. NaN → outMin.
export function mapNumber(
  value: number, inMin: number, inMax: number, outMin: number, outMax: number,
): number {
  if (!Number.isFinite(value) || inMax <= inMin) return outMin;
  const t = (value - inMin) / (inMax - inMin);
  const out = outMin + t * (outMax - outMin);
  return Math.max(outMin, Math.min(outMax, out));
}

// NodeSizeMode / NodeSizeMetric — the two user-controllable axes of the
// node-size encoding (slice 3). Direction picks WHICH edges of a node roll up;
// metric picks the QUANTITY rolled.
export type NodeSizeMode = 'incoming' | 'outgoing';
export type NodeSizeMetric = 'rate' | 'duration';

// NodeSizeEdge — the per-edge fields the rollup reads. `rate` is calls/min;
// `avgMs` + `calls` drive the call-weighted duration mean. All optional/NaN
// values coerce to 0 so a partial payload renders flat rather than throwing.
export interface NodeSizeEdge {
  source: string;
  target: string;
  rate: number;
  avgMs: number;
  calls: number;
}

// nodeSizeMetric rolls each node's per-direction edge stats into ONE number per
// node — Uptrace's node cue (nodes summarise their edges; size = throughput).
// Slice 3 makes both axes user-controllable:
//
//   mode 'outgoing' → roll edges where the node is the SOURCE (what it calls);
//   mode 'incoming' → roll edges where the node is the TARGET (who calls it).
//
//   metric 'rate'     → SUM the edge `rate` (calls/min) over the directional
//                       edges. This is the slice-2 behaviour at outgoing+rate.
//   metric 'duration' → a representative latency per node: the CALL-WEIGHTED
//                       average of edge `avgMs`, i.e. Σ(avgMs·calls)/Σ(calls)
//                       over the directional edges (Σcalls=0 → 0). We weight by
//                       calls so a chatty fast edge can't be averaged up by one
//                       rare slow edge — it's a true-ish mean of the latency the
//                       node actually experiences. (Edges also carry p99Ms; avg
//                       is the calmer, less jumpy size cue — a node shouldn't
//                       balloon because one request tail-latency'd. The picker
//                       can surface p99 elsewhere; size stays steady.)
//
// Returns a typed { metric, max } so the dagre layout and the canvas draw read
// the IDENTICAL per-node value. The width math (mapNumber [MIN_W,MAX_W]) is
// metric-agnostic: it normalises against `max` of WHATEVER metric is selected,
// so switching rate↔duration just re-scales — no per-metric width tuning.
//
// Edges whose relevant endpoint isn't in the node set are skipped (defensive —
// the layout already drops them). max is the largest per-node metric (0 if
// none), the denominator mapNumber uses to scale widths.
export function nodeSizeMetric(
  nodes: ReadonlyArray<{ id: string }>,
  edges: ReadonlyArray<NodeSizeEdge>,
  mode: NodeSizeMode = 'outgoing',
  metric: NodeSizeMetric = 'rate',
): { metric: Map<string, number>; max: number } {
  const ids = new Set(nodes.map(n => n.id));
  // The node an edge attributes to depends on the direction: outgoing → source,
  // incoming → target.
  const endpointOf = (e: NodeSizeEdge) => (mode === 'outgoing' ? e.source : e.target);

  const out = new Map<string, number>();
  for (const n of nodes) out.set(n.id, 0); // every node present → 0 default

  if (metric === 'rate') {
    for (const e of edges) {
      const id = endpointOf(e);
      if (!ids.has(id)) continue;
      const r = Number.isFinite(e.rate) ? e.rate : 0;
      out.set(id, (out.get(id) ?? 0) + r);
    }
  } else {
    // duration → call-weighted mean: accumulate Σ(avgMs·calls) and Σ(calls)
    // per node, then divide (guarding Σcalls=0 so a node with only zero-call
    // edges stays at 0 rather than NaN).
    const wSum = new Map<string, number>(); // Σ(avgMs·calls)
    const cSum = new Map<string, number>(); // Σ(calls)
    for (const e of edges) {
      const id = endpointOf(e);
      if (!ids.has(id)) continue;
      const calls = Number.isFinite(e.calls) ? e.calls : 0;
      const avgMs = Number.isFinite(e.avgMs) ? e.avgMs : 0;
      if (calls <= 0) continue; // zero-call edge carries no weight
      wSum.set(id, (wSum.get(id) ?? 0) + avgMs * calls);
      cSum.set(id, (cSum.get(id) ?? 0) + calls);
    }
    for (const id of out.keys()) {
      const c = cSum.get(id) ?? 0;
      out.set(id, c > 0 ? (wSum.get(id) ?? 0) / c : 0);
    }
  }

  let max = 0;
  for (const v of out.values()) if (v > max) max = v;
  return { metric: out, max };
}
