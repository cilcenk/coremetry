import { useEffect, useMemo, useRef, useState } from 'react';

// TopologyPillGraph — the prototype's tiered pill-card node-link layout
// (design_handoff workspaces.jsx → TopologyView). Presentational only:
// the caller pre-computes nodes / edges / columns from whatever data
// shape it owns (AggSpanNode tree for the Structure tab, ServiceMap for
// the Service "Topology" tab), so a single renderer drives both and the
// operator's eye never recalibrates between them.
//
// Layout: tiered left→right. Column x = tier fraction across
// [0.085 … 0.88] of the measured width; node y = (index+1)/(count+1) of
// the height, spreading each tier evenly on the vertical axis. Nodes are
// rounded pill cards (.topo-node) positioned absolutely in px; a single
// absolutely-positioned SVG overlay draws cubic-bezier edges behind them
// (z-index 1 vs 2). Hover a node → its edges accent + thicken, everything
// else dims. All colour comes from globals.css tokens via the .topo-*
// classes — no raw hex.

export type PillLevel = 'green' | 'amber' | 'red';

export interface PillNode {
  id: string;            // stable key + edge endpoint match
  name: string;          // display label (already cleaned)
  level: PillLevel;      // status dot colour by error rate
  sub: string;           // mono sub-line, e.g. "0.85% · 518ms"
  title?: string;        // hover tooltip
}

export interface PillEdge {
  from: string;          // PillNode.id
  to: string;            // PillNode.id
  level?: 'warn' | 'err'; // degraded path colour; undefined = healthy
}

export function TopologyPillGraph({
  nodes, edges, columns, focus, onSelect, height = 480,
}: {
  nodes: PillNode[];
  edges: PillEdge[];
  columns: string[][];   // node ids per tier, left→right
  focus: string;
  onSelect?: (id: string) => void;
  height?: number;
}) {
  const wrapRef = useRef<HTMLDivElement>(null);
  const [W, setW] = useState(900);
  const [hot, setHot] = useState<string | null>(null);
  const H = height;

  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const ro = new ResizeObserver(es => { for (const e of es) setW(Math.max(420, e.contentRect.width)); });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // Tiered positions in px: x from tier index (left→right across
  // [0.085 … 0.88]), y evenly distributed within the tier.
  const nCols = columns.length;
  const pos = useMemo(() => {
    const p = new Map<string, { x: number; y: number }>();
    columns.forEach((col, ci) => {
      const x = nCols <= 1 ? W / 2 : (0.085 + (ci / (nCols - 1)) * 0.795) * W;
      col.forEach((id, ri) => p.set(id, { x, y: ((ri + 1) / (col.length + 1)) * H }));
    });
    return p;
  }, [columns, W, nCols, H]);

  // Undirected adjacency for hover dimming.
  const adj = useMemo(() => {
    const m = new Map<string, Set<string>>();
    for (const n of nodes) m.set(n.id, new Set([n.id]));
    for (const e of edges) { m.get(e.from)?.add(e.to); m.get(e.to)?.add(e.from); }
    return m;
  }, [nodes, edges]);

  const near = (id: string) => !hot || hot === id || (adj.get(hot)?.has(id) ?? false);

  return (
    <div className="topo" ref={wrapRef} style={{ height: H }}>
      <svg className="topo-edges" viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none">
        <defs>
          <marker id="tpg-arw" markerWidth="8" markerHeight="8" refX="6" refY="3" orient="auto"><path d="M0,0 L6,3 L0,6 Z" fill="var(--border-strong)" /></marker>
          <marker id="tpg-arwH" markerWidth="8" markerHeight="8" refX="6" refY="3" orient="auto"><path d="M0,0 L6,3 L0,6 Z" fill="var(--accent)" /></marker>
        </defs>
        {edges.map((e, i) => {
          const a = pos.get(e.from), b = pos.get(e.to);
          if (!a || !b) return null;
          const hov = !!hot && (hot === e.from || hot === e.to);
          const deg = e.level === 'err' ? 'var(--err)' : e.level === 'warn' ? 'var(--warn)' : null;
          const mx = (a.x + b.x) / 2;
          return (
            <path key={i} d={`M${a.x},${a.y} C${mx},${a.y} ${mx},${b.y} ${b.x},${b.y}`} fill="none"
              stroke={hov ? 'var(--accent)' : (deg ?? 'var(--border-strong)')}
              strokeWidth={hov ? 2.2 : 1.4} opacity={hot && !hov ? 0.25 : (deg && !hov ? 0.85 : 1)}
              markerEnd={hov ? 'url(#tpg-arwH)' : 'url(#tpg-arw)'} vectorEffect="non-scaling-stroke" />
          );
        })}
      </svg>
      {nodes.map(n => {
        const p = pos.get(n.id);
        if (!p) return null;
        return (
          <div key={n.id}
            className={'topo-node' + (n.id === focus ? ' focus' : '') + (!near(n.id) ? ' dim' : '')}
            style={{ left: p.x, top: p.y, cursor: onSelect ? 'pointer' : 'default' }}
            onMouseEnter={() => setHot(n.id)} onMouseLeave={() => setHot(null)}
            onClick={onSelect ? () => onSelect(n.id) : undefined}
            title={n.title ?? n.name}>
            <span className={`topo-dot ${n.level}`} />
            <div style={{ minWidth: 0 }}>
              <div className="topo-name">{n.name}</div>
              <div className="topo-sub">{n.sub}</div>
            </div>
          </div>
        );
      })}
    </div>
  );
}
