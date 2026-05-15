import { useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { ServicePicker } from '@/components/ServicePicker';
import { fmtNum, hashColor, timeRangeToNs } from '@/lib/utils';
import { api } from '@/lib/api';
import type { TopologyResponse, TopologyNode } from '@/lib/types';
import type { TimeRange } from '@/lib/types';

// /topology — operation-level call graph rooted at one service,
// BFS-bounded by depth (1..6). First cut renders a layered list +
// edges table; the visual SVG renderer lands in v0.5.101. draw.io
// export lets the operator paste the diagram into design docs /
// runbooks without screenshotting.
export default function TopologyPage() {
  const [params, setParams] = useSearchParams();
  const root  = params.get('root')  || '';
  const depth = Math.max(1, Math.min(6, parseInt(params.get('depth') || '3', 10) || 3));
  const preset = params.get('preset') || '1h';
  const [range, setRange] = useState<TimeRange>({ preset });
  const [data, setData] = useState<TopologyResponse | null | undefined>(undefined);

  // Keep the URL ?preset= in sync with the topbar time-range
  // picker so a refreshed view restores the same window.
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
    if (!root) { setData(null); return; }
    setData(undefined);
    const { from, to } = timeRangeToNs(range);
    api.topology({ root, depth, from, to })
      .then(d => setData(d))
      .catch(() => setData(null));
  }, [root, depth, range]);

  // Group nodes by hop depth using a BFS over the response edges
  // — server already returned the bounded subgraph, but the
  // ordering doesn't carry hop info in the wire format. Re-doing
  // BFS on the client is cheap (O(nodes+edges)) and avoids a
  // schema change.
  const layers = useMemo(() => layerNodes(data, root), [data, root]);

  const setRoot = (v: string) => {
    setParams(prev => {
      const p = new URLSearchParams(prev);
      if (v) p.set('root', v); else p.delete('root');
      return p;
    }, { replace: true });
  };
  const setDepth = (v: number) => {
    setParams(prev => {
      const p = new URLSearchParams(prev);
      p.set('depth', String(v));
      return p;
    }, { replace: true });
  };

  const drawioHref = data && root
    ? api.topologyDrawIOURL({ root, depth, from: data.from, to: data.to })
    : '';

  return (
    <>
      <Topbar title="Topology" range={range} onRangeChange={setRange} />
      <div id="content">
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
            Topology builds a call graph starting from the picked service.
            Use the depth slider to widen how far downstream you expand.
          </Empty>
        )}
        {root && data === undefined && <Spinner />}
        {root && data === null && (
          <Empty icon="✗" title="Failed to load topology" />
        )}
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
                Narrow the time range for full coverage.
              </div>
            )}
            <div style={{ fontSize: 12, color: 'var(--text3)', marginBottom: 8 }}>
              {data.nodes.length} nodes · {data.edges.length} edges · depth {data.depth}
            </div>
            <TopologySVG layers={layers} edges={data.edges} />
            <div className="table-wrap" style={{ marginTop: 16 }}>
              <table>
                <thead><tr>
                  <th>Caller</th>
                  <th>Callee</th>
                  <th className="num">Calls</th>
                </tr></thead>
                <tbody>
                  {data.edges.map((e, i) => (
                    <tr key={i}>
                      <td>
                        <div style={{ fontWeight: 600 }}>{e.parentService}</div>
                        <div style={{ fontFamily: 'monospace', fontSize: 11, color: 'var(--text3)' }}>
                          {e.parentOp}
                        </div>
                      </td>
                      <td>
                        <div style={{ fontWeight: 600 }}>{e.childService}</div>
                        <div style={{ fontFamily: 'monospace', fontSize: 11, color: 'var(--text3)' }}>
                          {e.childOp}
                        </div>
                      </td>
                      <td className="num mono">{fmtNum(e.calls)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        )}
      </div>
    </>
  );
}

// TopologySVG renders the BFS-layered graph as an inline SVG:
// columns are hops (x = hop * COL_W), nodes stacked vertically
// inside each column, edges drawn as right-leaning Bezier curves
// with stroke width proportional to call volume so the heaviest
// paths read at a glance. Service color comes from the existing
// hashColor palette so a node's hue stays consistent with the
// service-map / trace waterfall views.
function TopologySVG({ layers, edges }: {
  layers: TopologyNode[][];
  edges: TopologyResponse['edges'];
}) {
  const NODE_W = 200, NODE_H = 48, COL_W = 280, ROW_H = 64;
  const pos = new Map<string, { x: number; y: number }>();
  layers.forEach((layer, hop) => {
    layer.forEach((n, i) => {
      pos.set(n.id, { x: hop * COL_W, y: i * ROW_H });
    });
  });
  const maxRows = Math.max(1, ...layers.map(l => l.length));
  const width = Math.max(1, layers.length) * COL_W;
  const height = maxRows * ROW_H + 20;
  const maxCalls = Math.max(1, ...edges.map(e => Number(e.calls) || 0));
  const truncate = (s: string, n: number) => s.length > n ? s.slice(0, n - 1) + '…' : s;
  return (
    <div style={{
      overflowX: 'auto', overflowY: 'auto', maxHeight: '60vh',
      border: '1px solid var(--border)', borderRadius: 6,
      background: 'var(--bg2)', padding: 12, marginBottom: 16,
    }}>
      <svg width={width} height={height}
        viewBox={`-10 -10 ${width + 40} ${height + 40}`}
        xmlns="http://www.w3.org/2000/svg"
        style={{ display: 'block' }}>
        <defs>
          <marker id="topo-arrow" viewBox="0 0 10 10" refX="9" refY="5"
            markerWidth="7" markerHeight="7" orient="auto">
            <path d="M 0 0 L 10 5 L 0 10 z" fill="var(--text3)" />
          </marker>
        </defs>
        {edges.map((e, i) => {
          const src = pos.get(`${e.parentService}|${e.parentOp}`);
          const dst = pos.get(`${e.childService}|${e.childOp}`);
          if (!src || !dst) return null;
          const x1 = src.x + NODE_W, y1 = src.y + NODE_H / 2;
          const x2 = dst.x,          y2 = dst.y + NODE_H / 2;
          const mx = (x1 + x2) / 2;
          const sw = 1 + (Number(e.calls) / maxCalls) * 3;
          return (
            <path key={i}
              d={`M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`}
              stroke="var(--text3)" strokeWidth={sw} fill="none"
              markerEnd="url(#topo-arrow)" opacity={0.55}>
              <title>{`${e.parentService}.${e.parentOp} → ${e.childService}.${e.childOp} · ${fmtNum(e.calls)} calls`}</title>
            </path>
          );
        })}
        {layers.flatMap(layer => layer.map(n => {
          const p = pos.get(n.id)!;
          const color = hashColor(n.service);
          return (
            <g key={n.id} transform={`translate(${p.x}, ${p.y})`}>
              <rect width={NODE_W} height={NODE_H} rx={6} ry={6}
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

// layerNodes re-runs BFS on the client to assign each node a hop
// index. Server's bounded subgraph already excludes nodes past
// the requested depth, so this is cheap.
function layerNodes(data: TopologyResponse | null | undefined, root: string): TopologyNode[][] {
  if (!data || !root) return [];
  const byId = new Map<string, TopologyNode>();
  data.nodes.forEach(n => byId.set(n.id, n));
  const outgoing = new Map<string, string[]>();
  data.edges.forEach(e => {
    const src = `${e.parentService}|${e.parentOp}`;
    const dst = `${e.childService}|${e.childOp}`;
    if (!outgoing.has(src)) outgoing.set(src, []);
    outgoing.get(src)!.push(dst);
  });
  const hop = new Map<string, number>();
  // Seed hop-0 with every root-service node that appears as a caller.
  data.edges.forEach(e => {
    if (e.parentService === root) {
      const id = `${e.parentService}|${e.parentOp}`;
      if (!hop.has(id)) hop.set(id, 0);
    }
  });
  if (hop.size === 0) {
    // No outgoing edges at all — show every node in hop 0.
    data.nodes.forEach(n => hop.set(n.id, 0));
  }
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
  // Unreached nodes (defensive — shouldn't happen with a connected
  // subgraph but server may return orphan child rows if the parent
  // op appears nowhere as a child of root).
  data.nodes.forEach(n => { if (!hop.has(n.id)) hop.set(n.id, 0); });

  const maxHop = Math.max(...Array.from(hop.values()));
  const layers: TopologyNode[][] = Array.from({ length: maxHop + 1 }, () => []);
  data.nodes.forEach(n => {
    const h = hop.get(n.id) ?? 0;
    layers[h].push(n);
  });
  layers.forEach(layer => layer.sort((a, b) =>
    a.service.localeCompare(b.service) || a.op.localeCompare(b.op)));
  return layers;
}
