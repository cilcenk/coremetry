'use client';
import { useEffect, useMemo, useRef, useState } from 'react';
import type { Service, ServiceEdge } from '@/lib/types';
import { fmtNum } from '@/lib/utils';

// ── Node visual contract ────────────────────────────────────────────────────

type NodeKind = 'service' | 'database' | 'external';

interface LaidOutNode {
  name: string;
  kind: NodeKind;
  x: number;
  y: number;
  avgMs: number;       // for the "TYPE · Xms" subtitle
  errorRate: number;   // drives the red corner dot
}

const NODE_W = 160;
const NODE_H = 48;
const VIEW_H = 560;

// Heuristic — backend doesn't tag node types yet. Tightens visually so a
// screenshot demo looks correct with zero schema work.
function classify(name: string): NodeKind {
  const lc = name.toLowerCase();
  if (name.startsWith('<') && name.endsWith('>')) return 'external';
  if (lc.startsWith('db:') ||
      /(redis|postgres|mysql|mongo|cassandra|elasticsearch|kafka|rabbitmq|memcache)/.test(lc)) {
    return 'database';
  }
  return 'service';
}

const KIND_STYLE: Record<NodeKind, { fill: string; stroke: string; subtitle: string; fg: string }> = {
  service:  { fill: '#1c2541', stroke: '#3b4a78', subtitle: 'SERVICE',  fg: '#cbd5f5' },
  database: { fill: '#0f3b3a', stroke: '#1f6b6a', subtitle: 'DATABASE', fg: '#a4dadc' },
  external: { fill: '#2a2e36', stroke: '#454952', subtitle: 'EXTERNAL', fg: '#c9d1d9' },
};

// ── Force-directed layout (Fruchterman–Reingold lite) ───────────────────────
//
// Pure JS, no external libs. ~150 iterations is enough to settle a typical
// service graph (<30 nodes). Memoized on the (sorted) names + edges signature
// so re-renders don't re-shuffle the diagram.

function layoutFR(names: string[], edges: ServiceEdge[], w: number, h: number) {
  const n = names.length;
  if (n === 0) return new Map<string, { x: number; y: number }>();

  const k = Math.sqrt((w * h) / Math.max(n, 1)) * 0.9; // ideal edge length
  const idx = new Map(names.map((nm, i) => [nm, i]));
  // Seed deterministically (two independent hashes per name) so the same
  // input always yields the same starting layout — avoids the diagram
  // jumping around between page navigations.
  const pos: { x: number; y: number }[] = new Array(n);
  for (let i = 0; i < n; i++) {
    const nm = names[i];
    let h1 = 2166136261;
    for (let j = 0; j < nm.length; j++) h1 = ((h1 ^ nm.charCodeAt(j)) * 16777619) >>> 0;
    let h2 = 5381;
    for (let j = 0; j < nm.length; j++) h2 = ((h2 * 33) ^ nm.charCodeAt(j)) >>> 0;
    pos[i] = { x: (h1 % 1000) / 1000 * w, y: (h2 % 1000) / 1000 * h };
  }
  const disp = pos.map(() => ({ x: 0, y: 0 }));

  const ITER = 160;
  let temp = w / 8;       // simulated annealing — cools each step
  for (let it = 0; it < ITER; it++) {
    // Repulsion between every pair
    for (let i = 0; i < n; i++) disp[i].x = disp[i].y = 0;
    for (let i = 0; i < n; i++) {
      for (let j = i + 1; j < n; j++) {
        const dx = pos[i].x - pos[j].x;
        const dy = pos[i].y - pos[j].y;
        const d2 = dx * dx + dy * dy + 0.01;
        const f = (k * k) / d2;
        const fx = dx * f, fy = dy * f;
        disp[i].x += fx; disp[i].y += fy;
        disp[j].x -= fx; disp[j].y -= fy;
      }
    }
    // Attraction along edges
    for (const e of edges) {
      const a = idx.get(e.source); const b = idx.get(e.target);
      if (a === undefined || b === undefined) continue;
      const dx = pos[a].x - pos[b].x;
      const dy = pos[a].y - pos[b].y;
      const d  = Math.sqrt(dx * dx + dy * dy) + 0.01;
      const f = (d * d) / k;
      const fx = (dx / d) * f, fy = (dy / d) * f;
      disp[a].x -= fx; disp[a].y -= fy;
      disp[b].x += fx; disp[b].y += fy;
    }
    // Apply with cap, keep inside frame
    for (let i = 0; i < n; i++) {
      const d = Math.sqrt(disp[i].x * disp[i].x + disp[i].y * disp[i].y) + 0.01;
      pos[i].x += (disp[i].x / d) * Math.min(d, temp);
      pos[i].y += (disp[i].y / d) * Math.min(d, temp);
      pos[i].x = Math.max(NODE_W / 2 + 8, Math.min(w - NODE_W / 2 - 8, pos[i].x));
      pos[i].y = Math.max(NODE_H / 2 + 8, Math.min(h - NODE_H / 2 - 8, pos[i].y));
    }
    temp = Math.max(temp * 0.96, 0.5); // cooling
  }
  return new Map(names.map((nm, i) => [nm, pos[i]]));
}

// ── Component ───────────────────────────────────────────────────────────────

export function ServiceGraphSVG({ services, edges, onNodeClick, highlightService }: {
  services: Service[];
  edges: ServiceEdge[];
  onNodeClick: (name: string) => void;
  highlightService?: string;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const [tooltip, setTooltip] = useState<{ x: number; y: number; text: string } | null>(null);
  const [w, setW] = useState(900);
  // Pan/zoom — applied as a single SVG-group transform.
  const [view, setView] = useState({ x: 0, y: 0, k: 1 });
  const dragRef = useRef<{ x: number; y: number; vx: number; vy: number } | null>(null);

  useEffect(() => {
    const update = () => setW(containerRef.current?.clientWidth ?? 900);
    update();
    window.addEventListener('resize', update);
    return () => window.removeEventListener('resize', update);
  }, []);

  // Aggregate per-node avgMs by averaging incoming-edge avgMs (so "external"
  // and DB nodes that never produce spans still get a sensible duration).
  const incoming = useMemo(() => {
    const m = new Map<string, { sum: number; n: number; err: number }>();
    edges.forEach(e => {
      const cur = m.get(e.target) ?? { sum: 0, n: 0, err: 0 };
      cur.sum += e.avgMs; cur.n += 1;
      cur.err = Math.max(cur.err, e.errorRate);
      m.set(e.target, cur);
    });
    return m;
  }, [edges]);

  const names = useMemo(() => {
    const set = new Set<string>();
    services.forEach(s => set.add(s.name));
    edges.forEach(e => { set.add(e.source); set.add(e.target); });
    return [...set].sort();
  }, [services, edges]);

  const positions = useMemo(
    () => layoutFR(names, edges, w, VIEW_H),
    [names, edges, w]
  );

  const nodes: LaidOutNode[] = useMemo(() => names.map(name => {
    const p = positions.get(name) ?? { x: w / 2, y: VIEW_H / 2 };
    const svc = services.find(s => s.name === name);
    const inc = incoming.get(name);
    return {
      name,
      kind: classify(name),
      x: p.x, y: p.y,
      avgMs: svc?.avgDurationMs ?? (inc && inc.n > 0 ? inc.sum / inc.n : 0),
      errorRate: svc?.errorRate ?? inc?.err ?? 0,
    };
  }), [names, positions, services, incoming, w]);

  const nodeMap = new Map(nodes.map(n => [n.name, n]));
  const maxCalls = Math.max(...edges.map(e => e.callCount), 1);

  // Pan/zoom handlers
  const onWheel = (ev: React.WheelEvent) => {
    ev.preventDefault();
    const factor = Math.exp(-ev.deltaY * 0.0015);
    setView(v => {
      const k = Math.max(0.4, Math.min(2.5, v.k * factor));
      // zoom around cursor
      const rect = containerRef.current!.getBoundingClientRect();
      const mx = ev.clientX - rect.left, my = ev.clientY - rect.top;
      const ratio = k / v.k;
      return { x: mx - (mx - v.x) * ratio, y: my - (my - v.y) * ratio, k };
    });
  };
  const onMouseDown = (ev: React.MouseEvent) => {
    if ((ev.target as Element).closest('.graph-node')) return; // don't pan when grabbing a node
    dragRef.current = { x: ev.clientX, y: ev.clientY, vx: view.x, vy: view.y };
  };
  const onMouseMove = (ev: React.MouseEvent) => {
    if (!dragRef.current) return;
    setView(v => ({ ...v, x: dragRef.current!.vx + (ev.clientX - dragRef.current!.x),
                          y: dragRef.current!.vy + (ev.clientY - dragRef.current!.y) }));
  };
  const onMouseUp = () => { dragRef.current = null; };

  return (
    <div ref={containerRef} id="graph-wrap" style={{ height: VIEW_H, position: 'relative' }}>
      <svg id="graph-svg" viewBox={`0 0 ${w} ${VIEW_H}`}
           onWheel={onWheel} onMouseDown={onMouseDown}
           onMouseMove={onMouseMove} onMouseUp={onMouseUp} onMouseLeave={onMouseUp}>
        <defs>
          {/* Background dot grid — graph paper feel */}
          <pattern id="dot-grid" x="0" y="0" width="18" height="18" patternUnits="userSpaceOnUse">
            <circle cx="1" cy="1" r="1" fill="#21262d" />
          </pattern>
          {/* Two arrowheads, one per edge color */}
          <marker id="arr-ok"  markerWidth="10" markerHeight="8" refX="9" refY="4" orient="auto">
            <path d="M0,0 L10,4 L0,8 Z" fill="#3fb950" />
          </marker>
          <marker id="arr-err" markerWidth="10" markerHeight="8" refX="9" refY="4" orient="auto">
            <path d="M0,0 L10,4 L0,8 Z" fill="#f85149" />
          </marker>
        </defs>

        <rect x="0" y="0" width={w} height={VIEW_H} fill="url(#dot-grid)" />

        <g transform={`translate(${view.x},${view.y}) scale(${view.k})`}>
          {/* Edges first so nodes paint on top */}
          {edges.map((e, i) => {
            const src = nodeMap.get(e.source); const tgt = nodeMap.get(e.target);
            if (!src || !tgt) return null;
            const errored = e.errorRate > 0;
            const color = errored ? '#f85149' : '#3fb950';
            // Endpoint = node border, not center, so arrow tip lands cleanly
            const pad = 4;
            const dx = tgt.x - src.x, dy = tgt.y - src.y;
            const len = Math.sqrt(dx * dx + dy * dy) || 1;
            const tx = tgt.x - (dx / len) * (NODE_W / 2 + pad);
            const ty = tgt.y - (dy / len) * (NODE_H / 2 + pad);
            const sx = src.x + (dx / len) * (NODE_W / 2 + pad);
            const sy = src.y + (dy / len) * (NODE_H / 2 + pad);
            const strokeW = Math.max(1.2, Math.log1p((e.callCount / maxCalls) * 8) * 1.4);
            return (
              <path key={i}
                d={`M ${sx} ${sy} L ${tx} ${ty}`}
                stroke={color} strokeWidth={strokeW} fill="none"
                strokeOpacity={errored ? 0.85 : 0.7}
                strokeDasharray={errored ? '6 4' : undefined}
                markerEnd={errored ? 'url(#arr-err)' : 'url(#arr-ok)'}
                onMouseEnter={ev => setTooltip({
                  x: ev.clientX, y: ev.clientY,
                  text: `${e.source} → ${e.target}\nCalls: ${fmtNum(e.callCount)}\nErrors: ${e.errorRate.toFixed(1)}%\nAvg: ${e.avgMs.toFixed(2)}ms`,
                })}
                onMouseLeave={() => setTooltip(null)} />
            );
          })}

          {/* Nodes */}
          {nodes.map(n => {
            const style = KIND_STYLE[n.kind];
            const isHL = highlightService === n.name;
            const dim  = highlightService && !isHL;
            const subtitle = `${style.subtitle} · ${formatMs(n.avgMs)}`;
            return (
              <g key={n.name} className="graph-node"
                 transform={`translate(${n.x - NODE_W / 2},${n.y - NODE_H / 2})`}
                 opacity={dim ? 0.35 : 1}
                 onClick={() => onNodeClick(n.name)}
                 onMouseEnter={ev => setTooltip({
                   x: ev.clientX, y: ev.clientY,
                   text: `${n.name}\n${style.subtitle}\nAvg: ${formatMs(n.avgMs)}\nErr rate: ${n.errorRate.toFixed(1)}%`,
                 })}
                 onMouseLeave={() => setTooltip(null)}>
                <rect width={NODE_W} height={NODE_H} rx="6" ry="6"
                  fill={style.fill}
                  stroke={isHL ? '#f0883e' : style.stroke}
                  strokeWidth={isHL ? 2 : 1} />
                {/* Icon */}
                <g transform="translate(8, 14)">
                  <NodeIcon kind={n.kind} color={style.fg} />
                </g>
                {/* Name (top line) */}
                <text x={32} y={18} fill={style.fg} fontSize="12" fontWeight="700">
                  {truncate(n.name, 18)}
                </text>
                {/* Subtitle (bottom line) */}
                <text x={32} y={34} fill="#8b949e" fontSize="9" fontWeight="600"
                      letterSpacing="0.5">
                  {subtitle}
                </text>
                {n.errorRate > 0 && (
                  <circle cx={NODE_W - 6} cy={6} r="3.5" fill="#f85149" />
                )}
              </g>
            );
          })}
        </g>
      </svg>

      {/* Zoom controls */}
      <div style={{
        position: 'absolute', left: 8, bottom: 8, display: 'flex', flexDirection: 'column',
        gap: 4, background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 4,
      }}>
        <ZoomBtn onClick={() => setView(v => ({ ...v, k: Math.min(2.5, v.k * 1.25) }))}>+</ZoomBtn>
        <ZoomBtn onClick={() => setView(v => ({ ...v, k: Math.max(0.4, v.k / 1.25) }))}>−</ZoomBtn>
        <ZoomBtn onClick={() => setView({ x: 0, y: 0, k: 1 })}>⌂</ZoomBtn>
      </div>

      {tooltip && (
        <div className="graph-tooltip" style={{ left: tooltip.x + 14, top: tooltip.y - 10 }}>
          {tooltip.text.split('\n').map((l, i) => <div key={i}>{l}</div>)}
        </div>
      )}
    </div>
  );
}

function ZoomBtn({ children, onClick }: { children: React.ReactNode; onClick: () => void }) {
  return (
    <button onClick={onClick} style={{
      width: 28, height: 24, padding: 0, background: 'transparent', color: 'var(--text2)',
      border: 'none', cursor: 'pointer', fontSize: 14,
    }}>{children}</button>
  );
}

// 16x16 inline icons — rendered at the left edge of each node.
function NodeIcon({ kind, color }: { kind: NodeKind; color: string }) {
  if (kind === 'database') {
    // Cylinder (3 ellipses + 2 vertical lines)
    return (
      <g stroke={color} strokeWidth="1.2" fill="none">
        <ellipse cx="8" cy="3"  rx="5.5" ry="1.8" />
        <path d="M2.5,3 V11 a5.5,1.8 0 0 0 11,0 V3" />
        <path d="M2.5,7 a5.5,1.8 0 0 0 11,0" />
      </g>
    );
  }
  if (kind === 'external') {
    // Two arrows in opposite directions (request/response)
    return (
      <g stroke={color} strokeWidth="1.4" fill="none" strokeLinecap="round" strokeLinejoin="round">
        <path d="M2,5 H12 L9,2.5 M14,11 H4 L7,13.5" />
      </g>
    );
  }
  // Service — gear silhouette
  return (
    <g fill={color}>
      <circle cx="8" cy="8" r="2.2" fill="none" stroke={color} strokeWidth="1.2" />
      <path d="M8,1.2 v2.2 M8,12.6 v2.2 M1.2,8 h2.2 M12.6,8 h2.2
               M3.4,3.4 l1.5,1.5 M11.1,11.1 l1.5,1.5
               M12.6,3.4 l-1.5,1.5 M4.9,11.1 l-1.5,1.5"
            stroke={color} strokeWidth="1.2" strokeLinecap="round" />
    </g>
  );
}

function truncate(s: string, n: number): string {
  return s.length > n ? s.slice(0, n - 1) + '…' : s;
}

function formatMs(ms: number): string {
  if (ms === 0 || !isFinite(ms)) return '0';
  if (ms < 1)   return `${(ms * 1000).toFixed(0)}us`;
  if (ms < 100) return `${ms.toFixed(1)}ms`;
  return `${ms.toFixed(0)}ms`;
}
