import { useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { fmtNum, hashColor, timeRangeToNs } from '@/lib/utils';
import { api } from '@/lib/api';
import type {
  ServiceTopologyResponse, ServiceTopologyNode, ServiceTopologyEdge,
  TimeRange,
} from '@/lib/types';

// /topology — service-level interaction graph for the entire
// backend in one view. Nodes are services AND synthetic infra
// nodes (db, queue, ext); edges carry the protocol family +
// top method/endpoint label so an operator can read the call
// flow at a glance. Click an edge to expand the full per-
// endpoint breakdown for that strand.
//
// Layered layout: each node is assigned a hop index via BFS from
// services with no incoming edges (the natural entry points).
// Cyclic services fall through to a synthetic-root pass that
// picks the highest out-degree node as a layer-0 anchor.
//
// Hover on a node or edge for full tooltip; click an edge to
// open the breakdown panel.
export default function TopologyPage() {
  const [params, setParams] = useSearchParams();
  const preset = params.get('preset') || '1h';
  const [range, setRange] = useState<TimeRange>({ preset });
  const [data, setData] = useState<ServiceTopologyResponse | null | undefined>(undefined);
  const [selectedEdge, setSelectedEdge] = useState<ServiceTopologyEdge | null>(null);

  useEffect(() => {
    if (range.preset && range.preset !== preset) {
      setParams(prev => {
        const p = new URLSearchParams(prev);
        p.set('preset', range.preset);
        return p;
      }, { replace: true });
    }
  }, [range, preset, setParams]);

  useEffect(() => {
    setData(undefined);
    const { from, to } = timeRangeToNs(range);
    api.serviceTopology({ from, to })
      .then(d => setData(d))
      .catch(() => setData(null));
  }, [range]);

  const layout = useMemo(() => layerServices(data), [data]);

  return (
    <>
      <Topbar title="Topology" range={range} onRangeChange={setRange} />
      <div id="content">
        {data === undefined && <Spinner />}
        {data === null && <Empty icon="✗" title="Failed to load topology" />}
        {data && data.nodes.length === 0 && (
          <Empty icon="◇" title="No interactions in this window">
            Pick a wider time range or wait for traces to flow.
          </Empty>
        )}
        {data && data.nodes.length > 0 && (
          <>
            {data.truncated && (
              <div style={{
                background: 'rgba(212,165,55,0.12)', border: '1px solid rgba(212,165,55,0.4)',
                borderRadius: 4, padding: '6px 10px', marginBottom: 10,
                color: 'var(--text2)', fontSize: 11,
              }}>
                Edge query hit its 5k cap — view shows the heaviest strands only.
              </div>
            )}
            <div style={{ fontSize: 12, color: 'var(--text3)', marginBottom: 8 }}>
              {data.nodes.length} nodes · {data.edges.length} edges
            </div>
            <ServiceTopologySVG
              nodes={data.nodes}
              edges={data.edges}
              layout={layout}
              onEdgeClick={setSelectedEdge}
            />
            {selectedEdge && (
              <EdgeDetailPanel edge={selectedEdge} onClose={() => setSelectedEdge(null)} />
            )}
          </>
        )}
      </div>
    </>
  );
}

// Layered layout: hop index per node.
// Roots = services with no incoming edges. If everything is
// cyclic, the highest out-degree service is the synthetic root.
// Unreached nodes get appended after the deepest reached layer
// — typical for orphaned infra leaves.
function layerServices(data: ServiceTopologyResponse | null | undefined): Map<string, number> {
  const layer = new Map<string, number>();
  if (!data) return layer;
  const incoming = new Map<string, number>();
  data.nodes.forEach(n => incoming.set(n.id, 0));
  data.edges.forEach(e => incoming.set(e.childNode, (incoming.get(e.childNode) ?? 0) + 1));

  const roots = data.nodes.filter(n => (incoming.get(n.id) ?? 0) === 0 && n.kind === 'service');
  let queue: string[];
  if (roots.length > 0) {
    queue = roots.map(r => r.id);
  } else {
    // Cyclic graph: pick highest out-degree service.
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
  // Any node not reached lands one layer past the deepest reached
  // node so it still shows up.
  let maxH = 0;
  layer.forEach(v => { if (v > maxH) maxH = v; });
  data.nodes.forEach(n => { if (!layer.has(n.id)) layer.set(n.id, maxH + 1); });
  return layer;
}

const NODE_W = 200, NODE_H = 56, COL_W = 280, ROW_H = 80;

// Color + shape hint per node kind. Service nodes use the service-
// hash palette so they stay consistent with the trace waterfall;
// infra nodes get a fixed palette so DBs / queues / external APIs
// read at a glance no matter which install.
function nodeColors(node: ServiceTopologyNode): { fill: string; stroke: string } {
  switch (node.kind) {
    case 'db':
      return { fill: '#3b5a73', stroke: '#6c8ebf' };
    case 'queue':
      return { fill: '#7a5e1d', stroke: '#d6b656' };
    case 'external':
      return { fill: '#6a3a3a', stroke: '#b85450' };
    default: {
      const c = hashColor(node.name);
      return { fill: c, stroke: c };
    }
  }
}

function protoColor(proto: string): string {
  switch (proto) {
    case 'http':    return '#4A90D9';
    case 'rpc':     return '#8A6FB5';
    case 'db':      return '#6c8ebf';
    case 'kafka':   return '#d6b656';
    default:        return '#888';
  }
}

function ServiceTopologySVG({ nodes, edges, layout, onEdgeClick }: {
  nodes: ServiceTopologyNode[];
  edges: ServiceTopologyEdge[];
  layout: Map<string, number>;
  onEdgeClick: (e: ServiceTopologyEdge) => void;
}) {
  // Group nodes by layer, sort within each layer for stable order.
  const layered: ServiceTopologyNode[][] = [];
  nodes.forEach(n => {
    const h = layout.get(n.id) ?? 0;
    while (layered.length <= h) layered.push([]);
    layered[h].push(n);
  });
  layered.forEach(col => col.sort((a, b) => a.name.localeCompare(b.name)));

  const pos = new Map<string, { x: number; y: number }>();
  layered.forEach((col, hop) => {
    col.forEach((n, i) => pos.set(n.id, { x: hop * COL_W, y: i * ROW_H }));
  });
  const maxRows = Math.max(1, ...layered.map(c => c.length));
  const width = layered.length * COL_W;
  const height = maxRows * ROW_H + 40;
  const maxCalls = Math.max(1, ...edges.map(e => Number(e.calls) || 0));
  const truncate = (s: string, n: number) => s.length > n ? s.slice(0, n - 1) + '…' : s;

  // Show inline label only on the top-third heaviest edges so
  // the diagram doesn't drown in text. Lighter edges still get a
  // hover tooltip with full detail.
  const callThreshold = (() => {
    const sorted = [...edges].map(e => e.calls).sort((a, b) => b - a);
    return sorted[Math.floor(sorted.length / 3)] ?? 0;
  })();

  return (
    <div style={{
      overflow: 'auto', maxHeight: '65vh', position: 'relative',
      border: '1px solid var(--border)', borderRadius: 6,
      background: 'var(--bg2)', padding: 12, marginBottom: 16,
    }}>
      <svg width={width} height={height}
        viewBox={`-10 -10 ${width + 40} ${height + 40}`}
        xmlns="http://www.w3.org/2000/svg"
        style={{ display: 'block' }}>
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
          const labelText = `${proto} ${truncate(top, 28)}${more}`;
          return (
            <g key={i} style={{ cursor: 'pointer' }} onClick={() => onEdgeClick(e)}>
              <path
                d={`M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`}
                stroke={color} strokeWidth={sw} fill="none"
                markerEnd={`url(#arrow-${e.protocol})`} opacity={0.7}>
                <title>
                  {`${e.parentService} → ${e.childNode}\n${proto} · ${fmtNum(e.calls)} calls · ${e.distinctLabels} endpoint(s)\n\n${e.topLabels.join('\n')}`}
                </title>
              </path>
              {showLabel && (
                <text
                  x={(x1 + x2) / 2} y={(y1 + y2) / 2 - 4}
                  fontSize={10} fill={color} textAnchor="middle"
                  style={{ pointerEvents: 'none' }}>
                  {labelText}
                </text>
              )}
              {showLabel && (
                <text
                  x={(x1 + x2) / 2} y={(y1 + y2) / 2 + 9}
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

// EdgeDetailPanel surfaces the per-endpoint breakdown for one
// strand. Shows all topLabels (server returns up to 5) plus the
// distinct count so the operator can tell when the list is
// truncated. Positioned inline beneath the diagram rather than
// as a modal so the diagram remains visible while reading.
function EdgeDetailPanel({ edge, onClose }: {
  edge: ServiceTopologyEdge;
  onClose: () => void;
}) {
  return (
    <div style={{
      background: 'var(--bg2)', border: '1px solid var(--border)',
      borderRadius: 6, padding: 12, marginTop: 12,
    }}>
      <div style={{
        display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 8,
      }}>
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
        {edge.topLabels.map((label, i) => (
          <li key={i}>{label}</li>
        ))}
      </ul>
      {edge.distinctLabels > edge.topLabels.length && (
        <div style={{ marginTop: 6, fontSize: 11, color: 'var(--text3)' }}>
          Showing top {edge.topLabels.length} of {edge.distinctLabels} distinct endpoints.
        </div>
      )}
    </div>
  );
}
