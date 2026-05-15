import { useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { ServicePicker } from '@/components/ServicePicker';
import { fmtNum, hashColor, timeRangeToNs } from '@/lib/utils';
import { api } from '@/lib/api';
import type {
  ServiceTopologyResponse, ServiceTopologyNode, ServiceTopologyEdge,
  TopologyResponse, TopologyNode,
  RootFlow,
  TimeRange,
} from '@/lib/types';

// /topology has three complementary views; URL ?view= picks one:
//   • service   — full backend interaction graph (v0.5.102)
//   • operation — depth-bounded op-level deep dive from one
//                 root service (v0.5.100/101)
//   • flows     — top business flows (root entry points) +
//                 click-to-focus subgraph (v0.5.103)
// Each view has its own controls; the topbar time range is
// shared across all three.
type View = 'service' | 'operation' | 'flows';

export default function TopologyPage() {
  const [params, setParams] = useSearchParams();
  const view = (params.get('view') as View) || 'service';
  const preset = params.get('preset') || '1h';
  const [range, setRange] = useState<TimeRange>({ preset });

  useEffect(() => {
    if (range.preset && range.preset !== preset) {
      setParams(prev => {
        const p = new URLSearchParams(prev);
        p.set('preset', range.preset);
        return p;
      }, { replace: true });
    }
  }, [range, preset, setParams]);

  const setView = (v: View) => setParams(prev => {
    const p = new URLSearchParams(prev);
    p.set('view', v);
    return p;
  }, { replace: true });

  return (
    <>
      <Topbar title="Topology" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ display: 'flex', gap: 4, marginBottom: 12 }}>
          {(['service', 'operation', 'flows'] as View[]).map(v => (
            <button key={v} type="button" onClick={() => setView(v)}
              className={view === v ? '' : 'sec'}
              style={{ fontSize: 12, padding: '5px 14px' }}>
              {v === 'service' ? 'Service topology'
                : v === 'operation' ? 'Operation deep-dive'
                : 'Business flows'}
            </button>
          ))}
        </div>
        {view === 'service'   && <ServiceView range={range} />}
        {view === 'operation' && <OperationView params={params} setParams={setParams} range={range} />}
        {view === 'flows'     && <FlowsView range={range} />}
      </div>
    </>
  );
}

// ── View 1: Service topology ─────────────────────────────────

function ServiceView({ range }: { range: TimeRange }) {
  const [data, setData] = useState<ServiceTopologyResponse | null | undefined>(undefined);
  const [selectedEdge, setSelectedEdge] = useState<ServiceTopologyEdge | null>(null);
  // Top-N + focus controls. In production a single full render is
  // unusable past ~50 services; default to top 30 by total call
  // volume so the page loads with a readable overview. "Focus on"
  // overrides the top-N pick — it shows only the focused service
  // + its 1-hop neighbors so an operator can pivot from the
  // overview to a specific service without losing the time range.
  const [topN, setTopN] = useState(30);
  const [focus, setFocus] = useState('');
  useEffect(() => {
    setData(undefined);
    const { from, to } = timeRangeToNs(range);
    api.serviceTopology({ from, to }).then(setData).catch(() => setData(null));
  }, [range]);

  // Compute the visible subgraph from the raw response based on
  // the two controls. All filtering is client-side because the
  // server already caps edges at 5k; computing here lets the
  // operator slide topN / pick focus without a round-trip.
  const visible = useMemo(() => {
    if (!data) return null;
    if (focus) {
      const keepNodes = new Set<string>([focus]);
      const keepEdges: ServiceTopologyEdge[] = [];
      data.edges.forEach(e => {
        if (e.parentService === focus || e.childNode === focus) {
          keepNodes.add(e.parentService);
          keepNodes.add(e.childNode);
          keepEdges.push(e);
        }
      });
      return {
        nodes: data.nodes.filter(n => keepNodes.has(n.id)),
        edges: keepEdges,
      };
    }
    // Top-N pick: rank nodes by total in+out call volume,
    // keep the heaviest N, then drop edges whose endpoints aren't
    // both in the kept set.
    const score = new Map<string, number>();
    const bump = (id: string, calls: number) => {
      score.set(id, (score.get(id) ?? 0) + calls);
    };
    data.edges.forEach(e => {
      bump(e.parentService, e.calls);
      bump(e.childNode, e.calls);
    });
    const ranked = data.nodes.slice().sort((a, b) =>
      (score.get(b.id) ?? 0) - (score.get(a.id) ?? 0));
    const keptSet = new Set(ranked.slice(0, topN).map(n => n.id));
    const keepEdges = data.edges.filter(e =>
      keptSet.has(e.parentService) && keptSet.has(e.childNode));
    return {
      nodes: data.nodes.filter(n => keptSet.has(n.id)),
      edges: keepEdges,
    };
  }, [data, topN, focus]);

  const layout = useMemo(
    () => layerServices(visible ? { ...data!, nodes: visible.nodes, edges: visible.edges } : null),
    [visible, data]
  );

  if (data === undefined) return <Spinner />;
  if (data === null) return <Empty icon="✗" title="Failed to load topology" />;
  if (data.nodes.length === 0) {
    return <Empty icon="◇" title="No interactions in this window">Pick a wider time range or wait for traces to flow.</Empty>;
  }
  const totalNodes = data.nodes.length;
  const totalEdges = data.edges.length;
  const showingNodes = visible?.nodes.length ?? 0;
  const showingEdges = visible?.edges.length ?? 0;
  // Build the focus-picker options from the full node list so the
  // operator can pivot to any service even when it's not in the
  // current top-N slice.
  const focusOptions = data.nodes
    .filter(n => n.kind === 'service')
    .map(n => n.name)
    .sort();
  return (
    <>
      <div className="controls" style={{ marginBottom: 12, gap: 12, flexWrap: 'wrap' }}>
        <label style={{ fontSize: 12, color: 'var(--text2)' }}>Focus on</label>
        <select value={focus} onChange={e => setFocus(e.target.value)}
          style={{ fontSize: 12, padding: '3px 6px', minWidth: 180 }}>
          <option value="">— top services —</option>
          {focusOptions.map(s => <option key={s} value={s}>{s}</option>)}
        </select>
        {!focus && (
          <>
            <label style={{ fontSize: 12, color: 'var(--text2)' }}>Top services</label>
            <input type="range" min={10} max={Math.min(200, totalNodes)} value={topN}
              onChange={e => setTopN(parseInt(e.target.value, 10))}
              style={{ width: 140 }} />
            <span style={{ fontFamily: 'monospace', fontSize: 12, color: 'var(--text)' }}>{topN}</span>
          </>
        )}
        <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--text3)' }}>
          {focus
            ? `Focused on ${focus}: ${showingNodes} nodes / ${showingEdges} edges`
            : `${showingNodes}/${totalNodes} nodes · ${showingEdges}/${totalEdges} edges`}
        </span>
      </div>
      {data.truncated && (
        <div style={{
          background: 'rgba(212,165,55,0.12)', border: '1px solid rgba(212,165,55,0.4)',
          borderRadius: 4, padding: '6px 10px', marginBottom: 10,
          color: 'var(--text2)', fontSize: 11,
        }}>
          Edge query hit its 5k cap — heaviest strands only. Narrow the time range for full coverage.
        </div>
      )}
      {visible && visible.nodes.length === 0 && (
        <Empty icon="◇" title={focus ? `No interactions for ${focus} in this window` : 'No matches'} />
      )}
      {visible && visible.nodes.length > 0 && (
        <>
          <ServiceTopologySVG
            nodes={visible.nodes} edges={visible.edges} layout={layout}
            onEdgeClick={setSelectedEdge}
          />
          {selectedEdge && (
            <EdgeDetailPanel edge={selectedEdge} onClose={() => setSelectedEdge(null)} />
          )}
        </>
      )}
    </>
  );
}

// ── View 2: Operation deep-dive ──────────────────────────────

function OperationView({ params, setParams, range }: {
  params: URLSearchParams;
  setParams: (n: URLSearchParams | ((p: URLSearchParams) => URLSearchParams), opts?: { replace?: boolean }) => void;
  range: TimeRange;
}) {
  const root  = params.get('root')  || '';
  const depth = Math.max(1, Math.min(6, parseInt(params.get('depth') || '3', 10) || 3));
  const [data, setData] = useState<TopologyResponse | null | undefined>(undefined);

  useEffect(() => {
    if (!root) { setData(null); return; }
    setData(undefined);
    const { from, to } = timeRangeToNs(range);
    api.topology({ root, depth, from, to }).then(setData).catch(() => setData(null));
  }, [root, depth, range]);

  const setRoot = (v: string) => setParams(prev => {
    const p = new URLSearchParams(prev);
    if (v) p.set('root', v); else p.delete('root');
    return p;
  }, { replace: true });
  const setDepth = (v: number) => setParams(prev => {
    const p = new URLSearchParams(prev);
    p.set('depth', String(v));
    return p;
  }, { replace: true });

  const layers = useMemo(() => layerOpNodes(data, root), [data, root]);
  const drawioHref = data && root
    ? api.topologyDrawIOURL({ root, depth, from: data.from, to: data.to }) : '';

  return (
    <>
      <div className="controls" style={{ marginBottom: 12, gap: 12 }}>
        <label style={{ fontSize: 12, color: 'var(--text2)' }}>Root service</label>
        <ServicePicker value={root} onChange={setRoot} placeholder="Pick a service…" width={220} />
        <label style={{ fontSize: 12, color: 'var(--text2)' }}>Depth</label>
        <input type="range" min={1} max={6} value={depth}
               onChange={e => setDepth(parseInt(e.target.value, 10))}
               style={{ width: 120 }} />
        <span style={{ fontFamily: 'monospace', fontSize: 12, color: 'var(--text)' }}>{depth}</span>
        {drawioHref && (
          <a href={drawioHref} className="sec"
             style={{ fontSize: 11, padding: '4px 10px', textDecoration: 'none', marginLeft: 'auto' }}
             title="Download as draw.io diagram">
            ↓ draw.io
          </a>
        )}
      </div>
      {!root && (
        <Empty icon="⋔" title="Pick a root service">
          Operation deep-dive expands the op-level call graph downstream from one service.
          Slide depth to widen the view.
        </Empty>
      )}
      {root && data === undefined && <Spinner />}
      {root && data === null && <Empty icon="✗" title="Failed to load topology" />}
      {root && data && data.nodes.length === 0 && (
        <Empty icon="◇" title="No outgoing calls in this window">
          Pick a wider time range or a different root service.
        </Empty>
      )}
      {root && data && data.nodes.length > 0 && (
        <>
          {data.truncated && (
            <div style={{
              background: 'rgba(212,165,55,0.12)', border: '1px solid rgba(212,165,55,0.4)',
              borderRadius: 4, padding: '6px 10px', marginBottom: 10,
              color: 'var(--text2)', fontSize: 11,
            }}>
              Edge query hit its 50k cap — view shows the heaviest edges only.
            </div>
          )}
          <div style={{ fontSize: 12, color: 'var(--text3)', marginBottom: 8 }}>
            {data.nodes.length} nodes · {data.edges.length} edges · depth {data.depth}
          </div>
          <OpTopologySVG layers={layers} edges={data.edges} />
        </>
      )}
    </>
  );
}

// ── View 3: Business flows ───────────────────────────────────

function FlowsView({ range }: { range: TimeRange }) {
  const [flows, setFlows] = useState<RootFlow[] | null | undefined>(undefined);
  const [picked, setPicked] = useState<RootFlow | null>(null);
  const [pickedData, setPickedData] = useState<ServiceTopologyResponse | null | undefined>(undefined);
  const [selectedEdge, setSelectedEdge] = useState<ServiceTopologyEdge | null>(null);

  useEffect(() => {
    setFlows(undefined);
    setPicked(null);
    const { from, to } = timeRangeToNs(range);
    api.topologyFlows({ top: 20, from, to }).then(d => setFlows(d.flows ?? []))
      .catch(() => setFlows(null));
  }, [range]);

  useEffect(() => {
    if (!picked) { setPickedData(undefined); return; }
    setPickedData(undefined);
    const { from, to } = timeRangeToNs(range);
    api.topologyFlow({
      root_service: picked.rootService, root_op: picked.rootOp, from, to,
    }).then(setPickedData).catch(() => setPickedData(null));
  }, [picked, range]);

  if (flows === undefined) return <Spinner />;
  if (flows === null) return <Empty icon="✗" title="Failed to load flows" />;
  if (flows.length === 0) {
    return <Empty icon="◇" title="No root flows in this window">
      Pick a wider time range — flows are anchored on root spans which need at least one trace.
    </Empty>;
  }

  return (
    <>
      {!picked && (
        <div style={{ display: 'grid', gap: 10, gridTemplateColumns: 'repeat(auto-fill, minmax(360px, 1fr))' }}>
          {flows.map((f, i) => (
            <button key={i} type="button"
              onClick={() => setPicked(f)}
              style={{
                textAlign: 'left', padding: 12, borderRadius: 6,
                background: 'var(--bg2)', border: '1px solid var(--border)',
                cursor: 'pointer', color: 'var(--text)',
              }}>
              <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
                <span style={{ fontWeight: 700, fontSize: 13 }}>{f.rootOp}</span>
                <span style={{ fontSize: 11, color: 'var(--text3)' }}>{f.rootService}</span>
                <span style={{ marginLeft: 'auto', fontSize: 11, color: 'var(--text3)' }}>
                  {fmtNum(f.traceCount)} traces
                </span>
              </div>
              <div style={{ marginTop: 8, display: 'flex', flexWrap: 'wrap', gap: 4 }}>
                {f.services.slice(0, 10).map(s => (
                  <span key={s} style={{
                    fontSize: 10, padding: '2px 6px', borderRadius: 3,
                    background: 'var(--bg3)', border: '1px solid var(--border)',
                    fontFamily: 'monospace',
                  }}>{s}</span>
                ))}
                {f.services.length > 10 && (
                  <span style={{ fontSize: 10, color: 'var(--text3)' }}>+{f.services.length - 10}</span>
                )}
              </div>
            </button>
          ))}
        </div>
      )}
      {picked && (
        <>
          <div style={{ display: 'flex', alignItems: 'baseline', gap: 10, marginBottom: 12 }}>
            <button type="button" className="sec"
              onClick={() => setPicked(null)}
              style={{ fontSize: 11, padding: '3px 10px' }}>
              ← All flows
            </button>
            <span style={{ fontWeight: 700, fontSize: 13 }}>{picked.rootOp}</span>
            <span style={{ fontSize: 11, color: 'var(--text3)' }}>
              {picked.rootService} · {fmtNum(picked.traceCount)} traces
            </span>
          </div>
          {pickedData === undefined && <Spinner />}
          {pickedData === null && <Empty icon="✗" title="Failed to load flow" />}
          {pickedData && pickedData.nodes.length === 0 && (
            <Empty icon="◇" title="Single-service flow — no outgoing calls" />
          )}
          {pickedData && pickedData.nodes.length > 0 && (
            <>
              <div style={{ fontSize: 12, color: 'var(--text3)', marginBottom: 8 }}>
                {pickedData.nodes.length} nodes · {pickedData.edges.length} edges
              </div>
              <ServiceTopologySVG
                nodes={pickedData.nodes} edges={pickedData.edges}
                layout={layerServices(pickedData)}
                onEdgeClick={setSelectedEdge}
              />
              {selectedEdge && (
                <EdgeDetailPanel edge={selectedEdge} onClose={() => setSelectedEdge(null)} />
              )}
            </>
          )}
        </>
      )}
    </>
  );
}

// ── Layout helpers + SVG renderers ────────────────────────────

function layerServices(data: ServiceTopologyResponse | null | undefined): Map<string, number> {
  const layer = new Map<string, number>();
  if (!data) return layer;
  const incoming = new Map<string, number>();
  data.nodes.forEach(n => incoming.set(n.id, 0));
  data.edges.forEach(e => incoming.set(e.childNode, (incoming.get(e.childNode) ?? 0) + 1));

  const roots = data.nodes.filter(n => (incoming.get(n.id) ?? 0) === 0 && n.kind === 'service');
  let queue: string[];
  if (roots.length > 0) queue = roots.map(r => r.id);
  else {
    const out = new Map<string, number>();
    data.edges.forEach(e => out.set(e.parentService, (out.get(e.parentService) ?? 0) + 1));
    let max = -1, pick = data.nodes[0]?.id;
    out.forEach((v, k) => { if (v > max) { max = v; pick = k; } });
    queue = pick ? [pick] : [];
  }
  queue.forEach(id => layer.set(id, 0));
  while (queue.length > 0) {
    const id = queue.shift()!;
    const h = layer.get(id)!;
    data.edges.filter(e => e.parentService === id).forEach(e => {
      if (!layer.has(e.childNode)) {
        layer.set(e.childNode, h + 1);
        queue.push(e.childNode);
      }
    });
  }
  let maxH = 0;
  layer.forEach(v => { if (v > maxH) maxH = v; });
  data.nodes.forEach(n => { if (!layer.has(n.id)) layer.set(n.id, maxH + 1); });
  return layer;
}

function layerOpNodes(data: TopologyResponse | null | undefined, root: string): TopologyNode[][] {
  if (!data || !root) return [];
  const outgoing = new Map<string, string[]>();
  data.edges.forEach(e => {
    const src = `${e.parentService}|${e.parentOp}`;
    const dst = `${e.childService}|${e.childOp}`;
    if (!outgoing.has(src)) outgoing.set(src, []);
    outgoing.get(src)!.push(dst);
  });
  const hop = new Map<string, number>();
  data.edges.forEach(e => {
    if (e.parentService === root) {
      const id = `${e.parentService}|${e.parentOp}`;
      if (!hop.has(id)) hop.set(id, 0);
    }
  });
  if (hop.size === 0) data.nodes.forEach(n => hop.set(n.id, 0));
  let frontier = new Set(hop.keys());
  while (frontier.size > 0) {
    const next = new Set<string>();
    frontier.forEach(id => {
      const h = hop.get(id)!;
      (outgoing.get(id) || []).forEach(childID => {
        if (!hop.has(childID)) {
          hop.set(childID, h + 1);
          next.add(childID);
        }
      });
    });
    frontier = next;
  }
  data.nodes.forEach(n => { if (!hop.has(n.id)) hop.set(n.id, 0); });
  const maxHop = Math.max(...Array.from(hop.values()));
  const layers: TopologyNode[][] = Array.from({ length: maxHop + 1 }, () => []);
  data.nodes.forEach(n => layers[hop.get(n.id) ?? 0].push(n));
  layers.forEach(layer => layer.sort((a, b) =>
    a.service.localeCompare(b.service) || a.op.localeCompare(b.op)));
  return layers;
}

const NODE_W = 200, NODE_H = 56, COL_W = 280, ROW_H = 80;

function nodeColors(node: ServiceTopologyNode): { fill: string; stroke: string } {
  switch (node.kind) {
    case 'db':       return { fill: '#3b5a73', stroke: '#6c8ebf' };
    case 'queue':    return { fill: '#7a5e1d', stroke: '#d6b656' };
    case 'external': return { fill: '#6a3a3a', stroke: '#b85450' };
    default: {
      const c = hashColor(node.name);
      return { fill: c, stroke: c };
    }
  }
}
function protoColor(proto: string): string {
  switch (proto) {
    case 'http':  return '#4A90D9';
    case 'rpc':   return '#8A6FB5';
    case 'db':    return '#6c8ebf';
    case 'kafka': return '#d6b656';
    default:      return '#888';
  }
}

function ServiceTopologySVG({ nodes, edges, layout, onEdgeClick }: {
  nodes: ServiceTopologyNode[];
  edges: ServiceTopologyEdge[];
  layout: Map<string, number>;
  onEdgeClick: (e: ServiceTopologyEdge) => void;
}) {
  const layered: ServiceTopologyNode[][] = [];
  nodes.forEach(n => {
    const h = layout.get(n.id) ?? 0;
    while (layered.length <= h) layered.push([]);
    layered[h].push(n);
  });
  layered.forEach(col => col.sort((a, b) => a.name.localeCompare(b.name)));
  const pos = new Map<string, { x: number; y: number }>();
  layered.forEach((col, hop) => col.forEach((n, i) => pos.set(n.id, { x: hop * COL_W, y: i * ROW_H })));
  const maxRows = Math.max(1, ...layered.map(c => c.length));
  const width = Math.max(1, layered.length) * COL_W;
  const height = maxRows * ROW_H + 40;
  const maxCalls = Math.max(1, ...edges.map(e => Number(e.calls) || 0));
  const truncate = (s: string, n: number) => s.length > n ? s.slice(0, n - 1) + '…' : s;
  const sorted = [...edges].map(e => e.calls).sort((a, b) => b - a);
  const callThreshold = sorted[Math.floor(sorted.length / 3)] ?? 0;
  return (
    <div style={{
      overflow: 'auto', maxHeight: '65vh',
      border: '1px solid var(--border)', borderRadius: 6,
      background: 'var(--bg2)', padding: 12, marginBottom: 16,
    }}>
      <svg width={width} height={height}
        viewBox={`-10 -10 ${width + 40} ${height + 40}`}
        xmlns="http://www.w3.org/2000/svg" style={{ display: 'block' }}>
        <defs>
          {(['http', 'rpc', 'db', 'kafka', 'internal'] as const).map(p => (
            <marker key={p} id={`arrow-${p}`} viewBox="0 0 10 10" refX="9" refY="5"
              markerWidth="7" markerHeight="7" orient="auto">
              <path d="M 0 0 L 10 5 L 0 10 z" fill={protoColor(p)} />
            </marker>
          ))}
        </defs>
        {edges.map((e, i) => {
          const src = pos.get(e.parentService);
          const dst = pos.get(e.childNode);
          if (!src || !dst) return null;
          const x1 = src.x + NODE_W, y1 = src.y + NODE_H / 2;
          const x2 = dst.x,          y2 = dst.y + NODE_H / 2;
          const mx = (x1 + x2) / 2;
          const sw = 1 + (Number(e.calls) / maxCalls) * 3;
          const color = protoColor(e.protocol);
          const showLabel = e.calls >= callThreshold;
          const proto = e.protocol.toUpperCase();
          const top = e.topLabels[0] || '';
          const more = e.distinctLabels > 1 ? ` (+${e.distinctLabels - 1})` : '';
          return (
            <g key={i} style={{ cursor: 'pointer' }} onClick={() => onEdgeClick(e)}>
              <path d={`M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`}
                stroke={color} strokeWidth={sw} fill="none"
                markerEnd={`url(#arrow-${e.protocol})`} opacity={0.7}>
                <title>{`${e.parentService} → ${e.childNode}\n${proto} · ${fmtNum(e.calls)} calls · ${e.distinctLabels} endpoint(s)\n\n${e.topLabels.join('\n')}`}</title>
              </path>
              {showLabel && (
                <text x={(x1 + x2) / 2} y={(y1 + y2) / 2 - 4}
                  fontSize={10} fill={color} textAnchor="middle"
                  style={{ pointerEvents: 'none' }}>
                  {`${proto} ${truncate(top, 28)}${more}`}
                </text>
              )}
              {showLabel && (
                <text x={(x1 + x2) / 2} y={(y1 + y2) / 2 + 9}
                  fontSize={9} fill="var(--text3)" textAnchor="middle"
                  style={{ pointerEvents: 'none' }}>
                  {fmtNum(e.calls)} calls
                </text>
              )}
            </g>
          );
        })}
        {nodes.map(n => {
          const p = pos.get(n.id);
          if (!p) return null;
          const { fill, stroke } = nodeColors(n);
          const kindIcon = n.kind === 'db' ? '⛁' : n.kind === 'queue' ? '⌬' : n.kind === 'external' ? '↗' : '';
          return (
            <g key={n.id} transform={`translate(${p.x}, ${p.y})`}>
              <rect width={NODE_W} height={NODE_H} rx={8} ry={8}
                fill={fill} fillOpacity={0.18} stroke={stroke} strokeWidth={1.6}>
                <title>{`${n.name} (${n.kind})`}</title>
              </rect>
              <text x={10} y={22} fontSize={13} fontWeight={600} fill="var(--text)">
                {truncate(n.name, 24)}
              </text>
              <text x={10} y={40} fontSize={10} fill="var(--text3)">
                {n.kind.toUpperCase()}
              </text>
              {kindIcon && (
                <text x={NODE_W - 18} y={22} fontSize={14} fill={stroke}>{kindIcon}</text>
              )}
            </g>
          );
        })}
      </svg>
    </div>
  );
}

function OpTopologySVG({ layers, edges }: {
  layers: TopologyNode[][];
  edges: TopologyResponse['edges'];
}) {
  const op_node_w = 200, op_node_h = 48, op_col_w = 280, op_row_h = 64;
  const pos = new Map<string, { x: number; y: number }>();
  layers.forEach((layer, hop) => layer.forEach((n, i) => pos.set(n.id, { x: hop * op_col_w, y: i * op_row_h })));
  const maxRows = Math.max(1, ...layers.map(l => l.length));
  const width = Math.max(1, layers.length) * op_col_w;
  const height = maxRows * op_row_h + 20;
  const maxCalls = Math.max(1, ...edges.map(e => Number(e.calls) || 0));
  const truncate = (s: string, n: number) => s.length > n ? s.slice(0, n - 1) + '…' : s;
  return (
    <div style={{
      overflow: 'auto', maxHeight: '60vh',
      border: '1px solid var(--border)', borderRadius: 6,
      background: 'var(--bg2)', padding: 12, marginBottom: 16,
    }}>
      <svg width={width} height={height}
        viewBox={`-10 -10 ${width + 40} ${height + 40}`}
        xmlns="http://www.w3.org/2000/svg" style={{ display: 'block' }}>
        <defs>
          <marker id="op-arrow" viewBox="0 0 10 10" refX="9" refY="5"
            markerWidth="7" markerHeight="7" orient="auto">
            <path d="M 0 0 L 10 5 L 0 10 z" fill="var(--text3)" />
          </marker>
        </defs>
        {edges.map((e, i) => {
          const src = pos.get(`${e.parentService}|${e.parentOp}`);
          const dst = pos.get(`${e.childService}|${e.childOp}`);
          if (!src || !dst) return null;
          const x1 = src.x + op_node_w, y1 = src.y + op_node_h / 2;
          const x2 = dst.x,             y2 = dst.y + op_node_h / 2;
          const mx = (x1 + x2) / 2;
          const sw = 1 + (Number(e.calls) / maxCalls) * 3;
          return (
            <path key={i}
              d={`M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`}
              stroke="var(--text3)" strokeWidth={sw} fill="none"
              markerEnd="url(#op-arrow)" opacity={0.55}>
              <title>{`${e.parentService}.${e.parentOp} → ${e.childService}.${e.childOp} · ${fmtNum(e.calls)} calls`}</title>
            </path>
          );
        })}
        {layers.flatMap(layer => layer.map(n => {
          const p = pos.get(n.id)!;
          const color = hashColor(n.service);
          return (
            <g key={n.id} transform={`translate(${p.x}, ${p.y})`}>
              <rect width={op_node_w} height={op_node_h} rx={6} ry={6}
                fill={color} fillOpacity={0.16}
                stroke={color} strokeWidth={1.5}>
                <title>{`${n.service}.${n.op}`}</title>
              </rect>
              <text x={10} y={19} fontSize={12} fontWeight={600} fill="var(--text)">
                {truncate(n.service, 26)}
              </text>
              <text x={10} y={36} fontSize={11} fill="var(--text3)"
                fontFamily="ui-monospace, SFMono-Regular, Menlo, monospace">
                {truncate(n.op, 28)}
              </text>
            </g>
          );
        }))}
      </svg>
    </div>
  );
}

function EdgeDetailPanel({ edge, onClose }: {
  edge: ServiceTopologyEdge;
  onClose: () => void;
}) {
  return (
    <div style={{
      background: 'var(--bg2)', border: '1px solid var(--border)',
      borderRadius: 6, padding: 12, marginTop: 12,
    }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 8 }}>
        <div style={{ fontSize: 13, fontWeight: 700 }}>
          {edge.parentService} → {edge.childNode}
        </div>
        <div style={{ fontSize: 11, color: 'var(--text3)' }}>
          {edge.protocol.toUpperCase()} · {fmtNum(edge.calls)} calls · {edge.distinctLabels} endpoint{edge.distinctLabels === 1 ? '' : 's'}
        </div>
        <button type="button" onClick={onClose} className="sec"
          style={{ marginLeft: 'auto', fontSize: 11, padding: '2px 8px' }}>
          Close
        </button>
      </div>
      <ul style={{ margin: 0, padding: '0 0 0 16px', fontSize: 12, lineHeight: 1.6, fontFamily: 'monospace' }}>
        {edge.topLabels.map((label, i) => <li key={i}>{label}</li>)}
      </ul>
      {edge.distinctLabels > edge.topLabels.length && (
        <div style={{ marginTop: 6, fontSize: 11, color: 'var(--text3)' }}>
          Showing top {edge.topLabels.length} of {edge.distinctLabels} distinct endpoints.
        </div>
      )}
    </div>
  );
}
