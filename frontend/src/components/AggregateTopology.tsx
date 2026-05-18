import { useMemo, useState } from 'react';
import type { AggSpanNode } from '@/lib/types';
import { fmtNum } from '@/lib/utils';

// AggregateTopology — v0.5.222. Third view inside ServiceStructure
// alongside Tree + Flame. Same input data (path-aggregated AggSpanNode
// tree from /api/services/{name}/structure), different projection:
// collapse to service-level edges so the operator sees "this
// service's actual blast-out" in one diagram.
//
// Different from /topology's Service view in two ways:
//   • Scoped: only the services that appear in the focused
//     service's traces, no global noise.
//   • Trace-derived: edges come from the same sampled traces the
//     Flame uses, so latency hot paths line up across views.
//
// Renders left→right by BFS depth. Each column packs vertically;
// edges are simple Bezier curves with thickness ∝ log(calls).

type Edge = {
  from: string;
  to: string;
  calls: number;
  avgMs: number;
  errorCount: number;
};

type GraphService = {
  name: string;
  kind: 'service' | 'db' | 'queue' | 'cache';
  totalCalls: number;
  totalErrors: number;
};

export function AggregateTopology({ roots }: { roots: AggSpanNode[] }) {
  const graph = useMemo(() => buildGraph(roots), [roots]);
  const [hover, setHover] = useState<Edge | null>(null);

  if (graph.services.length <= 1) {
    return (
      <div style={{ fontSize: 12, color: 'var(--text3)', fontStyle: 'italic', padding: '20px 4px' }}>
        Not enough cross-service spans in the sample to draw a topology.
        Either this service runs everything in-process, or the sample
        window is too short. Try Cross-service scope with a wider range.
      </div>
    );
  }

  const NODE_W = 170;
  const NODE_H = 44;
  const COL_GAP = 80;
  const ROW_GAP = 14;
  const PAD = 16;

  // Layered layout — columns indexed by BFS depth, rows packed
  // vertically. Service order within a row is stable (alphabetical)
  // so a re-render doesn't shuffle the diagram.
  const cols = graph.columns;
  const colH = cols.map(svcs =>
    svcs.length * NODE_H + Math.max(0, svcs.length - 1) * ROW_GAP);
  const maxColH = Math.max(...colH);
  const W = PAD * 2 + cols.length * NODE_W + (cols.length - 1) * COL_GAP;
  const H = PAD * 2 + maxColH;

  // Positions
  const pos = new Map<string, { x: number; y: number }>();
  cols.forEach((svcs, ci) => {
    const colOffset = (maxColH - colH[ci]) / 2;
    svcs.forEach((svc, ri) => {
      const x = PAD + ci * (NODE_W + COL_GAP);
      const y = PAD + colOffset + ri * (NODE_H + ROW_GAP);
      pos.set(svc, { x, y });
    });
  });

  const maxCalls = Math.max(1, ...graph.edges.map(e => e.calls));

  return (
    <div>
      <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 8 }}>
        Service-to-service projection of the sampled traces ·
        {' '}{graph.services.length} services
        {' · '}{graph.edges.length} edge{graph.edges.length === 1 ? '' : 's'}
        {hover && (
          <span style={{ marginLeft: 14, color: 'var(--text2)' }}>
            <b>{hover.from}</b> → <b>{hover.to}</b>
            {' · '}{fmtNum(hover.calls)} calls
            {' · '}avg {hover.avgMs.toFixed(1)} ms
            {hover.errorCount > 0 && <> · <span style={{ color: 'var(--err)' }}>{hover.errorCount} errors</span></>}
          </span>
        )}
      </div>
      <svg viewBox={`0 0 ${W} ${H}`} width="100%" height={Math.max(160, H)}
        style={{ display: 'block', background: 'var(--bg2)', borderRadius: 6 }}>
        <defs>
          <marker id="agg-topo-arrow" viewBox="0 0 10 10"
            refX="9" refY="5" markerWidth="6" markerHeight="6"
            orient="auto">
            <path d="M 0 0 L 10 5 L 0 10 z" fill="var(--text3)" />
          </marker>
        </defs>

        {/* Edges first so nodes paint on top */}
        {graph.edges.map((e, i) => {
          const a = pos.get(e.from);
          const b = pos.get(e.to);
          if (!a || !b) return null;
          const x1 = a.x + NODE_W, y1 = a.y + NODE_H / 2;
          const x2 = b.x,         y2 = b.y + NODE_H / 2;
          const mx = (x1 + x2) / 2;
          // 0.6-3.6 px stroke width scaled to log(calls).
          const sw = 0.6 + 3 * (Math.log10(e.calls + 1) / Math.log10(maxCalls + 1));
          // Error-tinted edges get a red overlay so the eye lands
          // there without reading the count.
          const errored = e.errorCount > 0;
          const colour = errored ? 'var(--err)' : 'var(--text3)';
          return (
            <g key={i}
              onMouseEnter={() => setHover(e)}
              onMouseLeave={() => setHover(null)}
              style={{ cursor: 'pointer' }}>
              <path d={`M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`}
                stroke={colour} strokeWidth={sw} fill="none"
                markerEnd="url(#agg-topo-arrow)" opacity={0.75}>
                <title>{`${e.from} → ${e.to}\n${fmtNum(e.calls)} calls · avg ${e.avgMs.toFixed(1)}ms · ${e.errorCount} errors`}</title>
              </path>
              {/* Calls label, only when this edge has visual room */}
              {sw > 1.2 && (
                <text x={mx} y={(y1 + y2) / 2 - 4}
                  fontSize={9} fill={colour} textAnchor="middle"
                  style={{ pointerEvents: 'none' }}>
                  {fmtNum(e.calls)}
                </text>
              )}
            </g>
          );
        })}

        {/* Nodes */}
        {graph.services.map((svc, i) => {
          const p = pos.get(svc.name);
          if (!p) return null;
          const fill = svc.totalErrors > 0
            ? 'rgba(239,68,68,0.10)'
            : svc.kind === 'service' ? 'var(--bg1)'
            : 'rgba(168,85,247,0.10)'; // infra
          const stroke = svc.totalErrors > 0
            ? 'var(--err)'
            : svc.kind === 'service' ? 'var(--border)'
            : 'rgba(168,85,247,0.45)';
          return (
            <g key={i}>
              <rect x={p.x} y={p.y} width={NODE_W} height={NODE_H} rx={5}
                fill={fill} stroke={stroke} strokeWidth={1.4} />
              <text x={p.x + 10} y={p.y + 17}
                fontSize={12} fontWeight={600} fill="var(--text)">
                {svc.name.length > 22 ? svc.name.slice(0, 20) + '…' : svc.name}
              </text>
              <text x={p.x + 10} y={p.y + 33}
                fontSize={10} fill="var(--text3)" fontFamily="ui-monospace, monospace">
                {fmtNum(svc.totalCalls)} call{svc.totalCalls === 1 ? '' : 's'}
                {svc.totalErrors > 0 && (
                  <tspan fill="var(--err)" dx={6}>{svc.totalErrors} err</tspan>
                )}
              </text>
            </g>
          );
        })}
      </svg>
    </div>
  );
}

// buildGraph walks the AggSpanNode tree, collapses to service-level
// edges (one entry per parent_service → child_service pair regardless
// of how many distinct operations bridge them), then layers nodes by
// BFS depth from the roots so the layout draws clean left→right.
function buildGraph(roots: AggSpanNode[]): {
  services: GraphService[];
  edges: Edge[];
  columns: string[][];
} {
  const edgeAgg = new Map<string, { calls: number; sumMs: number; errs: number }>();
  const svcAgg = new Map<string, GraphService>();

  const touchSvc = (name: string, kind: string | undefined, n: number, errs: number) => {
    const k = inferKind(name, kind);
    const cur = svcAgg.get(name) ?? { name, kind: k, totalCalls: 0, totalErrors: 0 };
    cur.totalCalls += n;
    cur.totalErrors += errs;
    cur.kind = k;
    svcAgg.set(name, cur);
  };

  const walk = (node: AggSpanNode, parentSvc: string | null) => {
    touchSvc(node.service, node.kind, node.count, node.errorCount);
    if (parentSvc && parentSvc !== node.service) {
      const key = parentSvc + '→' + node.service;
      const cur = edgeAgg.get(key) ?? { calls: 0, sumMs: 0, errs: 0 };
      cur.calls += node.count;
      cur.sumMs += node.avgMs * node.count;
      cur.errs += node.errorCount;
      edgeAgg.set(key, cur);
    }
    if (node.children) {
      for (const c of node.children) walk(c, node.service);
    }
  };
  for (const r of roots) walk(r, null);

  const services = Array.from(svcAgg.values())
    .sort((a, b) => a.name.localeCompare(b.name));
  const edges: Edge[] = [];
  for (const [k, v] of edgeAgg.entries()) {
    const sep = k.indexOf('→');
    edges.push({
      from: k.slice(0, sep),
      to: k.slice(sep + 1),
      calls: v.calls,
      avgMs: v.calls > 0 ? v.sumMs / v.calls : 0,
      errorCount: v.errs,
    });
  }

  // BFS depth from any node that has no incoming edge (root layer).
  const incoming = new Map<string, number>();
  for (const e of edges) incoming.set(e.to, (incoming.get(e.to) ?? 0) + 1);
  const rootLayer = services.filter(s => !incoming.has(s.name)).map(s => s.name);
  const depth = new Map<string, number>();
  for (const r of rootLayer) depth.set(r, 0);
  let frontier = [...rootLayer];
  let d = 0;
  while (frontier.length && d < 12) {
    d++;
    const next = new Set<string>();
    for (const f of frontier) {
      for (const e of edges) {
        if (e.from === f && !depth.has(e.to)) {
          depth.set(e.to, d);
          next.add(e.to);
        }
      }
    }
    frontier = Array.from(next);
  }
  // Unreached services (cycles, orphans) land in their own
  // rightmost column so they're visible.
  let maxDepth = 0;
  for (const v of depth.values()) if (v > maxDepth) maxDepth = v;
  for (const s of services) {
    if (!depth.has(s.name)) depth.set(s.name, maxDepth + 1);
  }
  const realMaxDepth = Math.max(...depth.values());
  const columns: string[][] = Array.from({ length: realMaxDepth + 1 }, () => []);
  for (const s of services) columns[depth.get(s.name)!].push(s.name);
  for (const col of columns) col.sort();

  return { services, edges, columns };
}

// Same kind inference shared with the global Service topology page:
// db:* / queue:* infra nodes light up purple, the rest are services.
function inferKind(name: string, kind?: string): GraphService['kind'] {
  if (name.startsWith('db:') || kind === 'client' && name.includes(':')) {
    if (name.startsWith('queue:')) return 'queue';
    if (name.startsWith('cache:')) return 'cache';
    return 'db';
  }
  if (name.startsWith('queue:')) return 'queue';
  if (name.startsWith('cache:')) return 'cache';
  return 'service';
}
