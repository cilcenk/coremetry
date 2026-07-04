// serviceGraphToMap — adapts the MV-backed /api/servicegraph
// response (GraphNode/GraphEdge) onto the ServiceMap shape the
// /service-map renderer consumes.
//
// v0.8.273 (operator-reported): picking a Focus on /service-map
// showed an EMPTY graph on the test install. The page filtered the
// SAMPLED global map (200 heaviest traces) down to the focus
// neighborhood — any service outside the sample had no nodes to
// keep, and on a 1200-service install that's almost every pick.
// The fix renders the focus view from /api/servicegraph
// (topology_edges_5m MV — full coverage, sample-independent,
// hidden-patterns applied per v0.8.264), which needs this shape
// adapter.

import type { ServiceGraphResponse, ServiceMap } from './types';

export function serviceGraphToMap(g: ServiceGraphResponse): ServiceMap {
  return {
    nodes: (g.nodes ?? []).map(n => ({
      service: n.id,
      spanCount: n.calls,
      // ServiceMap carries a 0..1 fraction; GraphNode carries 0..100.
      errorRate: (n.errorRate ?? 0) / 100,
      kind: n.kind === 'service' ? '' : n.kind,
      subkind: n.system || undefined,
    })),
    edges: (g.edges ?? []).map(e => ({
      caller: e.source,
      callee: e.target,
      spanCount: e.calls,
      errorCount: e.errors,
      // The MV path has no per-trace attribution; 0 renders as "—"
      // wherever trace counts surface.
      traceCount: 0,
    })),
    sampledFrom: 0, // MV-backed — not a trace sample
    totalSpans: (g.nodes ?? []).reduce((a, n) => a + (n.calls || 0), 0),
  };
}
