import type { ServiceMapEdge } from './types';

// edgeWeights — the per-graph traffic weight used for topology edge
// thickness + flow-animation speed (v0.8.281).
//
// The sampled global /api/service-map carries per-edge traceCount, and
// thickness has always scaled on it. The MV-backed focus path
// (serviceGraphAdapter) has NO per-trace attribution — every edge arrives
// with traceCount=0, which made t = 0/max for ALL focus edges, so the
// whole neighborhood rendered at minimum width and the operator couldn't
// read traffic at a glance.
//
// Rule: if ANY edge in the graph carries a traceCount, the graph is
// sample-derived → weigh by traceCount exactly as before (byte-identical
// global behaviour). Otherwise the graph is MV-derived → weigh by
// spanCount (calls). max is floored at 1 so callers can divide safely.
// Pure so the mode switch is unit-tested.
export function edgeWeights(edges: ServiceMapEdge[]): {
  weightOf: (e: ServiceMapEdge | undefined) => number;
  max: number;
} {
  let maxTrace = 0;
  for (const e of edges) maxTrace = Math.max(maxTrace, e.traceCount);
  const weightOf = (e: ServiceMapEdge | undefined): number =>
    !e ? 0 : maxTrace > 0 ? e.traceCount : e.spanCount;
  let max = 0;
  for (const e of edges) max = Math.max(max, weightOf(e));
  return { weightOf, max: Math.max(1, max) };
}
