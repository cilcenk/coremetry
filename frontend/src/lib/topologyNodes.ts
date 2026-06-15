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

// nodeSizeMetric rolls each node's per-direction edge stats into ONE number per
// node — Uptrace's node cue (nodes summarise their edges; size = throughput).
// Slice 2 hardcodes the default mode = OUTGOING + RATE: a node's metric is the
// sum of `rate` (calls/min) over the edges where it is the SOURCE. Slice 3 will
// make direction/metric a toggle; this returns a typed { metric, max } so the
// dagre layout and the canvas draw read the IDENTICAL per-node value.
//
// Edges whose endpoints aren't in the node set are skipped (defensive — the
// layout already drops them). max is the largest per-node metric (0 if none),
// the denominator mapNumber uses to scale widths.
export function nodeSizeMetric(
  nodes: ReadonlyArray<{ id: string }>,
  edges: ReadonlyArray<{ source: string; target: string; rate: number }>,
): { metric: Map<string, number>; max: number } {
  const metric = new Map<string, number>();
  const ids = new Set(nodes.map(n => n.id));
  for (const n of nodes) metric.set(n.id, 0); // every node present → 0 default
  for (const e of edges) {
    if (!ids.has(e.source)) continue; // outgoing → attribute to the source node
    const r = Number.isFinite(e.rate) ? e.rate : 0;
    metric.set(e.source, (metric.get(e.source) ?? 0) + r);
  }
  let max = 0;
  for (const v of metric.values()) if (v > max) max = v;
  return { metric, max };
}
