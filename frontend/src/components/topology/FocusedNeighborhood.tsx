import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { timeRangeToNs, fmtNum } from '@/lib/utils';
import { healthToken } from '@/lib/health';
import { Spinner } from '@/components/Spinner';
import { TopologyFlowGraph } from '@/components/TopologyFlowGraph';
import type { TimeRange, ServiceGraphResponse, GraphNode, GraphEdge, ServiceMap } from '@/lib/types';
import { Button } from '@/components/ui/Button';

// FocusedNeighborhood — the focused topology graph (service-detail Topology
// tab). Since v0.8.294 the neighborhood is walked SERVER-side
// (/api/servicegraph?scope=neighborhood&hops=N — same direction-separated
// BFS contract, see neighborhoodKeepSet) so this component no longer
// downloads the entire global graph (20k edges at 1000+ services) for a
// ≤40-node view. The client keeps assignFocusColumns on the returned
// subgraph for the signed caller/dependency columns that drive the
// nearest-first CAP, and renders via TopologyFlowGraph (v0.8.108): pill
// nodes + flow-animated bezier edges + hover-blue direct-edge emphasis.
// Toolbar (hops / errors-only) + hover inspector stay.

const CAP = 40;

function kindLabel(n: GraphNode): string {
  if (n.kind === 'database') return n.system ? `db · ${n.system}` : 'database';
  if (n.kind === 'queue')    return n.system ? `mq · ${n.system}` : 'queue';
  if (n.kind === 'external') return 'external';
  return 'service';
}
function fmtMs(ms: number): string {
  if (!ms) return '—';
  if (ms >= 1000) return (ms / 1000).toFixed(ms >= 10_000 ? 0 : 1) + 's';
  return Math.round(ms) + 'ms';
}

// assignFocusColumns — the signed-column assignment for the focused graph.
// Returns id → column where 0 = focus, NEGATIVE = callers (upstream, reached
// by walking IN edges) and POSITIVE = dependencies (downstream, OUT edges).
//
// The two directions are walked SEPARATELY and each step keeps its sign. A
// single bidirectional BFS (the pre-v0.8.39 code) summed the ±1 steps along
// the path, so a node reached as caller(-1) → that caller's OTHER dependency
// (+1) landed at column 0 — piling every "sibling" of the focus into the
// focus's own column. Against real data a 2-hop view dumped ~26 nodes at 0
// (operator-reported "the graph won't branch at 2 hops"). Walking upstream
// via IN-only and downstream via OUT-only keeps the fan: callers strictly
// left, deps strictly right, and nothing but the focus at 0. A node reachable
// both ways (a cycle) takes the side with the smaller hop distance.
//
// Since v0.8.108 the columns aren't used for x-placement (TopologyFlowGraph
// lays out with its own BFS) — they still drive the nearest-first node CAP.
export function assignFocusColumns(edges: GraphEdge[], focus: string, hops: number): Map<string, number> {
  const out = new Map<string, GraphEdge[]>(); // source → edges (downstream)
  const inc = new Map<string, GraphEdge[]>(); // target → edges (upstream)
  for (const e of edges) {
    (out.get(e.source) ?? out.set(e.source, []).get(e.source)!).push(e);
    (inc.get(e.target) ?? inc.set(e.target, []).get(e.target)!).push(e);
  }
  const col = new Map<string, number>([[focus, 0]]);
  const setCloser = (id: string, v: number) => {
    const c = col.get(id);
    if (c === undefined || Math.abs(v) < Math.abs(c)) col.set(id, v);
  };
  // dir 0 = downstream (OUT edges, +hop); dir 1 = upstream (IN edges, -hop).
  for (let dir = 0; dir < 2; dir++) {
    const adj = dir === 0 ? out : inc;
    const sign = dir === 0 ? 1 : -1;
    const neighbour = dir === 0 ? (e: GraphEdge) => e.target : (e: GraphEdge) => e.source;
    let frontier = [focus];
    const seen = new Set([focus]); // per-direction so a cycle can reach both sides
    for (let h = 1; h <= hops; h++) {
      const next: string[] = [];
      for (const id of frontier) {
        for (const e of adj.get(id) ?? []) {
          const nb = neighbour(e);
          if (!seen.has(nb)) { seen.add(nb); setCloser(nb, sign * h); next.push(nb); }
        }
      }
      frontier = next;
    }
  }
  return col;
}

export function FocusedNeighborhood({ range, focus, hops, errorsOnly, onHops, onErrorsOnly, onRecenter, onClear }: {
  range: TimeRange;
  focus: string;
  hops: number;
  errorsOnly: boolean;
  onHops: (h: number) => void;
  onErrorsOnly: (v: boolean) => void;
  onRecenter: (svc: string) => void;
  onClear: () => void;
}) {
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  // hops rides the query key (bounded: server clamps to 1..3, UI offers 1|2)
  // so widening the radius refetches the wider subgraph; 30s server cache.
  const graph = useQuery<ServiceGraphResponse>({
    queryKey: ['servicegraph', 'neighborhood', focus, hops, from, to],
    queryFn: () => api.serviceGraph({ focus, scope: 'neighborhood', hops, from, to }),
    staleTime: 30_000,
  });

  // ── signed columns + nearest-first cap over the server-walked subgraph ──
  const nb = useMemo(() => {
    const allNodes = new Map<string, GraphNode>();
    for (const n of graph.data?.nodes ?? []) allNodes.set(n.id, n);
    const edges = (graph.data?.edges ?? []).filter(e => !errorsOnly || e.errorRate > 1);
    // Signed-column assignment — extracted + unit-tested (assignFocusColumns
    // above). Walks callers (upstream) and deps (downstream) as SEPARATE
    // directional BFS so a caller's other dependency no longer collapses onto
    // the focus column (the pre-v0.8.39 "won't branch at 2 hops" bug).
    const col = assignFocusColumns(edges, focus, hops);
    // cap: keep the nearest CAP nodes (lowest |col|, focus always kept).
    let ids = [...col.keys()];
    let collapsed = 0;
    if (ids.length > CAP) {
      ids.sort((a, b) => Math.abs(col.get(a)!) - Math.abs(col.get(b)!));
      collapsed = ids.length - CAP;
      ids = ids.slice(0, CAP);
    }
    const keep = new Set(ids);
    const nodes = ids.map(id => allNodes.get(id)).filter((n): n is GraphNode => !!n);
    const shown = edges.filter(e => keep.has(e.source) && keep.has(e.target));
    // node p99 ≈ max p99 of its incoming edges (fall back to outgoing).
    const p99Of = (id: string) => {
      let m = 0;
      for (const e of shown) if (e.target === id && e.p99Ms > m) m = e.p99Ms;
      if (!m) for (const e of shown) if (e.source === id && e.p99Ms > m) m = e.p99Ms;
      return m;
    };
    return { nodes, edges: shown, col, collapsed, p99Of };
  }, [graph.data, focus, hops, errorsOnly]);

  const [hover, setHover] = useState<string | null>(null);

  // GraphNode/GraphEdge → ServiceMap adapter (TopologyFlowGraph's contract).
  // errorRate: /api/servicegraph returns PERCENT, ServiceMapNode is a
  // FRACTION (the component thresholds at 0.05 / 0.01). subkind carries the
  // prefix-decoded display name so dep pills read "h2", not "db:h2".
  const mapData = useMemo<ServiceMap>(() => ({
    // sampledFrom/totalSpans are /api/service-map sampling metadata —
    // required by the type, not consumed by TopologyFlowGraph.
    sampledFrom: 0,
    totalSpans: 0,
    nodes: nb.nodes.map(n => ({
      service: n.id,
      spanCount: n.calls,
      errorRate: n.errorRate / 100,
      kind: n.kind === 'database' ? 'db'
        : n.kind === 'queue' ? 'queue'
        : n.kind === 'external' || n.kind === 'internal' ? 'external'
        : undefined,
      // v0.8.297 — dep pill'in ana satırı motor adını okur ("oracle",
      // "kafka"); instance/db.name alt satıra iner (depInstanceLabel).
      subkind: (n.kind === 'database' || n.kind === 'queue')
        ? (n.system || n.name)
        : (n.name !== n.id ? n.name : undefined),
      dbName: n.dbName || undefined,
    })),
    edges: nb.edges.map(e => ({
      caller: e.source,
      callee: e.target,
      traceCount: e.calls,
      spanCount: e.calls,
      errorCount: e.errors,
      // v0.8.322 — carry the MV's per-edge RED (the v0.8.281 chip
      // contract). This inline adapter dropped them, so the service
      // Topology tab never showed the "N/dk · p99 · err%" chips its
      // /service-map twin renders (TopologyFlowGraph gates the chip on
      // p99Ms != null). errorRate rescales to ServiceMap's 0..1.
      rate: e.rate,
      errorRate: (e.errorRate ?? 0) / 100,
      avgMs: e.avgMs,
      p99Ms: e.p99Ms,
    })),
  }), [nb]);

  if (graph.isLoading) return <div style={{ padding: 60, display: 'grid', placeItems: 'center' }}><Spinner /></div>;

  const hoverNode = hover ? nb.nodes.find(n => n.id === hover) : null;
  const height = Math.round(window.innerHeight * 0.74);

  return (
    <div style={{ position: 'relative' }}>
      {/* ── toolbar ─────────────────────────────────────────────────────── */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', marginBottom: 10 }}>
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, padding: '4px 8px', borderRadius: 6, background: 'var(--bg2)', border: '1px solid var(--border)', fontSize: 12, fontWeight: 600 }}>
          <span style={{ width: 8, height: 8, borderRadius: '50%', background: healthToken(focusErr(nb.nodes, focus)) }} />
          {focus}
          <Button variant="ghost" size="sm" onClick={onClear}
            title="Back to the service picker" style={{ marginLeft: 2 }}>✕</Button>
        </span>
        <div className="seg">
          <button className={hops === 1 ? 'on' : ''} onClick={() => onHops(1)}>1 hop</button>
          <button className={hops === 2 ? 'on' : ''} onClick={() => onHops(2)}>2 hops</button>
        </div>
        <label style={{ display: 'inline-flex', alignItems: 'center', gap: 5, fontSize: 12, color: 'var(--text2)', cursor: 'pointer' }}>
          <input type="checkbox" checked={errorsOnly} onChange={e => onErrorsOnly(e.target.checked)} /> Errors only
        </label>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          {nb.nodes.length} of {graph.data?.nodes.length ?? 0} nodes · {nb.edges.length} edges
          {nb.collapsed > 0 && <strong style={{ color: 'var(--warn)' }}> · +{nb.collapsed} more collapsed</strong>}
        </span>
      </div>

      {/* ── flow graph canvas ───────────────────────────────────────────── */}
      <TopologyFlowGraph
        data={mapData}
        focus={focus}
        hoverNode={hover}
        onHoverNode={setHover}
        onSelectNode={id => {
          if (id === focus) return;
          const n = nb.nodes.find(x => x.id === id);
          onRecenter(n?.name ?? id);
        }}
        height={height}
        dropMessaging={false}
      />

      {/* ── hover inspector ─────────────────────────────────────────────── */}
      {hoverNode && (
        <div style={{ position: 'absolute', left: 10, bottom: 44, zIndex: 5, width: 240, padding: 12, borderRadius: 8, background: 'var(--bg2)', border: '1px solid var(--border)', boxShadow: '0 6px 20px rgba(0,0,0,.28)', fontSize: 12 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 7, marginBottom: 6 }}>
            <span style={{ width: 9, height: 9, borderRadius: '50%', background: healthToken(hoverNode.errorRate) }} />
            <span style={{ fontWeight: 700, color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{hoverNode.name}</span>
            <Button variant="secondary" size="sm" onClick={() => onRecenter(hoverNode.name)} style={{ marginLeft: 'auto' }}>Recenter</Button>
          </div>
          <div style={{ fontSize: 10, color: 'var(--text3)', fontFamily: 'ui-monospace, monospace', marginBottom: 6 }}>
            {kindLabel(hoverNode)} · {hoverNode.kind === 'service' ? 'service.name' : hoverNode.system ? (hoverNode.kind === 'database' ? 'db.system' : 'messaging.system') : hoverNode.kind}
            {hoverNode.kind === 'database' && hoverNode.dbName ? ` · db.name=${hoverNode.dbName}` : ''}
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3,1fr)', gap: 6, marginBottom: 8 }}>
            <Stat l="CALLS" v={fmtNum(hoverNode.calls)} />
            <Stat l="P99" v={fmtMs(nb.p99Of(hoverNode.id))} />
            <Stat l="ERR" v={`${hoverNode.errorRate.toFixed(1)}%`} tone={healthToken(hoverNode.errorRate)} />
          </div>
          {hoverNode.kind === 'service' && (
            <Link to={`/service?name=${encodeURIComponent(hoverNode.name)}`} style={{ fontSize: 11, color: 'var(--accent)', textDecoration: 'none' }}>Open service →</Link>
          )}
        </div>
      )}

      {/* ── health legend (bottom-left) ─────────────────────────────────── */}
      <div style={{ position: 'absolute', left: 10, bottom: 10, zIndex: 3, display: 'inline-flex', gap: 12, fontSize: 10, color: 'var(--text3)', background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 6, padding: '4px 8px' }}>
        <Kind c="var(--ok)" l="healthy" /><Kind c="var(--warn)" l=">1% err" /><Kind c="var(--err)" l=">5% err" />
      </div>

      {/* ── footer caption ──────────────────────────────────────────────── */}
      <div style={{ position: 'absolute', right: 10, bottom: 10, zIndex: 3, maxWidth: '62%', fontSize: 9.5, color: 'var(--text3)', textAlign: 'right', lineHeight: 1.4 }}>
        Built from OTel span semantics — nodes by service.name, type from db.system/messaging.system, edges from CLIENT→SERVER spans.
        Click a node to recenter · hover for direct edges · dashed pill = external dependency.
      </div>
    </div>
  );
}

function focusErr(nodes: GraphNode[], focus: string): number {
  return nodes.find(n => n.id === focus)?.errorRate ?? 0;
}
function Kind({ c, l }: { c: string; l: string }) {
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
      <span style={{ width: 8, height: 8, borderRadius: '50%', background: c }} />{l}
    </span>
  );
}
function Stat({ l, v, tone }: { l: string; v: string; tone?: string }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column' }}>
      <span style={{ fontSize: 12, fontWeight: 700, color: tone ?? 'var(--text)', fontFamily: 'ui-monospace, monospace' }}>{v}</span>
      <span style={{ fontSize: 8.5, color: 'var(--text3)', letterSpacing: '0.4px' }}>{l}</span>
    </div>
  );
}
