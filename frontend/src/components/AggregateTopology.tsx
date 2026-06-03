import { useMemo } from 'react';
import { Link } from 'react-router-dom';
import type { AggSpanNode } from '@/lib/types';
import { fmtNum, isMessagingDep } from '@/lib/utils';
import { TopologyPillGraph, type PillNode, type PillEdge, type PillLevel } from './TopologyPillGraph';

// Strip the topology node prefix ("db:h2" → "h2", "queue:orders" → "orders").
function cleanName(raw: string): string {
  const i = raw.indexOf(':');
  return i > 0 && i < 8 ? raw.slice(i + 1) : raw;
}
function kindTag(kind: GraphService['kind']): string {
  return kind === 'db' ? 'database' : kind === 'queue' ? 'queue' : kind === 'cache' ? 'cache' : 'service';
}

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
  const focus = roots[0]?.service ?? '';

  // Project the service-level graph onto the shared pill renderer's
  // shape. Status dot + degraded-edge colour both key off error rate.
  const pill = useMemo(() => {
    const errRate = (s?: GraphService) => (s && s.totalCalls > 0 ? (s.totalErrors / s.totalCalls) * 100 : 0);
    const dotLevel = (er: number): PillLevel => (er > 5 ? 'red' : er > 1 ? 'amber' : 'green');
    const svcOf = (n: string) => graph.services.find(s => s.name === n);
    const nodes: PillNode[] = graph.services.map(s => {
      const er = errRate(s);
      return {
        id: s.name,
        name: cleanName(s.name),
        level: dotLevel(er),
        sub: `${er.toFixed(1)}% · ${fmtNum(s.totalCalls)} · ${kindTag(s.kind)}`,
        title: `${s.name} · ${fmtNum(s.totalCalls)} calls · ${er.toFixed(1)}% err`,
      };
    });
    const edges: PillEdge[] = graph.edges.map(e => {
      const er = Math.max(errRate(svcOf(e.from)), errRate(svcOf(e.to)));
      return { from: e.from, to: e.to, level: er > 5 ? 'err' : er > 1 ? 'warn' : undefined };
    });
    return { nodes, edges };
  }, [graph]);

  if (graph.services.length <= 1) {
    return (
      <div style={{ fontSize: 12, color: 'var(--text3)', fontStyle: 'italic', padding: '20px 4px' }}>
        Not enough cross-service spans in the sample to draw a topology. Either this
        service runs everything in-process, or the sample window is too short.
      </div>
    );
  }

  const totalDeps = graph.services.length - (graph.columns[0]?.length ?? 0);
  const hops = Math.max(1, graph.columns.length - 1);

  return (
    <div>
      {/* Header — mirrors the Service "Topology" tab chrome so the
          operator's eye doesn't recalibrate between the two views. */}
      <div style={{ fontSize: 12, marginBottom: 8, display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
        <span style={{ fontWeight: 600, color: 'var(--text)' }}>{cleanName(focus)}</span>
        <span style={{ color: 'var(--text3)' }}>neighborhood · {hops} hop{hops === 1 ? '' : 's'}</span>
        <span style={{ marginLeft: 'auto', display: 'inline-flex', alignItems: 'center', gap: 14 }}>
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, fontSize: 11, color: 'var(--text3)' }}>
            <span className="topo-dot green" />healthy
            <span className="topo-dot amber" style={{ marginLeft: 8 }} />degraded
          </span>
          <Link to={`/topology?focus=${encodeURIComponent(focus)}`}
            style={{ fontSize: 11, color: 'var(--accent)', textDecoration: 'none', whiteSpace: 'nowrap' }}>
            Open full Topology →
          </Link>
        </span>
      </div>
      <TopologyPillGraph nodes={pill.nodes} edges={pill.edges} columns={graph.columns} focus={focus} />
      {/* Footer stats — service / dep / edge counts + sample note. */}
      <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 8 }}>
        {graph.services.length} service{graph.services.length === 1 ? '' : 's'} · {totalDeps} dep{totalDeps === 1 ? '' : 's'} · {graph.edges.length} edge{graph.edges.length === 1 ? '' : 's'} · sampled traces
      </div>
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

  // Drop messaging-broker nodes (kafka / rabbitmq / sqs …) and any edge
  // touching one. A broadcast topic links dozens of unrelated producers
  // + consumers, which explodes the BFS-depth columns into noise
  // ("topoloji saçmalıyor"). The broker may be inferKind→"queue" or a
  // peer.service'd node like "ext:kafka" (cleanName → "kafka"), so match
  // both. Topology stays synchronous-call only: service→service +
  // service→db/redis.
  const dropped = new Set(
    Array.from(svcAgg.values())
      .filter(s => s.kind === 'queue' || isMessagingDep(undefined, cleanName(s.name)))
      .map(s => s.name),
  );
  const services = Array.from(svcAgg.values())
    .filter(s => !dropped.has(s.name))
    .sort((a, b) => a.name.localeCompare(b.name));
  const edges: Edge[] = [];
  for (const [k, v] of edgeAgg.entries()) {
    const sep = k.indexOf('→');
    const from = k.slice(0, sep), to = k.slice(sep + 1);
    if (dropped.has(from) || dropped.has(to)) continue;
    edges.push({
      from,
      to,
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
