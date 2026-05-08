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

// FR_THRESHOLD: above this node count we abandon the O(n²) force-
// directed layout for a deterministic O(n) circular layout. With
// 300+ services FR takes 2-5s on a desktop browser and routinely
// hangs the main thread on lower-spec laptops; the circular layout
// renders instantly and is honest about what it's showing — a
// "service inventory" rather than a topology diagram.
const FR_THRESHOLD = 200;

// frIterations scales the FR iteration count to the input size so
// total work stays bounded. Per-iteration cost is dominated by the
// O(n²) repulsion pass; total work ≈ ITER × n² capped to ~4M ops.
function frIterations(n: number): number {
  if (n <= 30)  return 160;
  if (n <= 80)  return 100;
  if (n <= 150) return 60;
  return 40;
}

// circularLayout positions nodes on a single concentric ring, sized
// to the viewport. Used for FR_THRESHOLD-busting graphs (200+
// services) where the eye can't trace edges anyway — the value of
// the canvas at that scale is "scan the ring, click the service you
// need", and that works regardless of geometry.
function circularLayout(names: string[], w: number, h: number) {
  const n = names.length;
  if (n === 0) return new Map<string, { x: number; y: number }>();
  const cx = w / 2, cy = h / 2;
  const r = Math.min(w, h) * 0.42;
  const out = new Map<string, { x: number; y: number }>();
  for (let i = 0; i < n; i++) {
    const a = (i / n) * Math.PI * 2 - Math.PI / 2;
    out.set(names[i], { x: cx + Math.cos(a) * r, y: cy + Math.sin(a) * r });
  }
  return out;
}

function layoutFR(names: string[], edges: ServiceEdge[], w: number, h: number) {
  const n = names.length;
  if (n === 0) return new Map<string, { x: number; y: number }>();

  // Big-graph short-circuit. Honest about not running FR — caller
  // sees a circular ring which is far more useful at scale than a
  // half-converged FR mess.
  if (n > FR_THRESHOLD) {
    return circularLayout(names, w, h);
  }

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

  const ITER = frIterations(n);
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
  // Per-node drag offset relative to the layout's resting position.
  // Survives re-layout (keyed by name), so a user-arranged graph
  // doesn't snap back when the data refreshes.
  const [nodeOffsets, setNodeOffsets] = useState<Record<string, { dx: number; dy: number }>>({});
  // Hovered node — drives edge / neighbor highlight. `null` = nothing hovered.
  const [hovered, setHovered] = useState<string | null>(null);

  // Drag state machine refs:
  //   panRef     — when set, the user grabbed empty canvas and is panning.
  //   nodeDrag   — when set, the user grabbed a specific node.
  // Tracking moved=true prevents a click handler firing after a real drag.
  const panRef = useRef<{ x: number; y: number; vx: number; vy: number; moved: boolean } | null>(null);
  const nodeDrag = useRef<{ name: string; startX: number; startY: number; ox: number; oy: number; moved: boolean } | null>(null);

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
    const off = nodeOffsets[name] ?? { dx: 0, dy: 0 };
    const svc = services.find(s => s.name === name);
    const inc = incoming.get(name);
    return {
      name,
      kind: classify(name),
      x: p.x + off.dx, y: p.y + off.dy,
      avgMs: svc?.avgDurationMs ?? (inc && inc.n > 0 ? inc.sum / inc.n : 0),
      errorRate: svc?.errorRate ?? inc?.err ?? 0,
    };
  }), [names, positions, services, incoming, w, nodeOffsets]);

  const nodeMap = new Map(nodes.map(n => [n.name, n]));
  // Reduce-loop instead of `Math.max(...arr)` — the spread form
  // throws "Maximum call stack size exceeded" past ~50k args, and
  // a multi-thousand-edge graph can hit that ceiling on
  // service-saturated environments.
  const maxCalls = edges.reduce((m, e) => e.callCount > m ? e.callCount : m, 1);

  // Neighbour set of the hovered node — used to dim non-related edges
  // and nodes the same way Uptrace does on hover. Empty when nothing
  // is hovered.
  const neighborhood = useMemo(() => {
    if (!hovered) return null;
    const ns = new Set<string>([hovered]);
    edges.forEach(e => {
      if (e.source === hovered) ns.add(e.target);
      if (e.target === hovered) ns.add(e.source);
    });
    return ns;
  }, [hovered, edges]);

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

  // Mouse-down anywhere outside a node starts a pan; node-specific
  // mousedown (see <g onMouseDown> below) intercepts before this fires.
  const onCanvasMouseDown = (ev: React.MouseEvent) => {
    if ((ev.target as Element).closest('.graph-node')) return;
    panRef.current = { x: ev.clientX, y: ev.clientY, vx: view.x, vy: view.y, moved: false };
  };

  const onMouseMove = (ev: React.MouseEvent) => {
    // Node drag wins over canvas pan when both could fire.
    if (nodeDrag.current) {
      const drag = nodeDrag.current;
      // Translate screen-pixel delta into graph-space (account for zoom).
      const sdx = (ev.clientX - drag.startX) / view.k;
      const sdy = (ev.clientY - drag.startY) / view.k;
      if (Math.abs(sdx) > 1 || Math.abs(sdy) > 1) drag.moved = true;
      setNodeOffsets(prev => ({
        ...prev,
        [drag.name]: { dx: drag.ox + sdx, dy: drag.oy + sdy },
      }));
      return;
    }
    if (panRef.current) {
      const pan = panRef.current;
      const dx = ev.clientX - pan.x, dy = ev.clientY - pan.y;
      if (Math.abs(dx) > 2 || Math.abs(dy) > 2) pan.moved = true;
      setView(v => ({ ...v, x: pan.vx + dx, y: pan.vy + dy }));
    }
  };

  const onMouseUp = () => {
    nodeDrag.current = null;
    panRef.current = null;
  };

  // Per-node mouse-down — captures the node by name, records its
  // current offset, and prevents the canvas pan handler from firing.
  const onNodeMouseDown = (ev: React.MouseEvent, name: string) => {
    ev.stopPropagation();
    const off = nodeOffsets[name] ?? { dx: 0, dy: 0 };
    nodeDrag.current = {
      name, startX: ev.clientX, startY: ev.clientY,
      ox: off.dx, oy: off.dy, moved: false,
    };
  };

  // Click only counts when the mouse-up fires on the same node and
  // didn't move enough to qualify as a drag. Without this guard a
  // drag would also navigate the user to /service?name=X.
  const onNodeClickGuarded = (ev: React.MouseEvent, name: string) => {
    if (nodeDrag.current && nodeDrag.current.moved) {
      ev.stopPropagation();
      return;
    }
    onNodeClick(name);
  };

  return (
    <div ref={containerRef} id="graph-wrap"
         style={{ height: VIEW_H, position: 'relative', cursor: panRef.current ? 'grabbing' : 'default' }}>
      <svg id="graph-svg" viewBox={`0 0 ${w} ${VIEW_H}`}
           onWheel={onWheel}
           onMouseDown={onCanvasMouseDown}
           onMouseMove={onMouseMove}
           onMouseUp={onMouseUp}
           onMouseLeave={onMouseUp}>
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
          {/* Highlighted variants — rendered when an edge belongs to
              the hovered node's neighborhood. Brighter, no dash, fatter. */}
          <marker id="arr-ok-hl"  markerWidth="11" markerHeight="9" refX="9" refY="4.5" orient="auto">
            <path d="M0,0 L11,4.5 L0,9 Z" fill="#56d364" />
          </marker>
          <marker id="arr-err-hl" markerWidth="11" markerHeight="9" refX="9" refY="4.5" orient="auto">
            <path d="M0,0 L11,4.5 L0,9 Z" fill="#ff7b72" />
          </marker>
        </defs>

        <rect x="0" y="0" width={w} height={VIEW_H} fill="url(#dot-grid)" />

        <g transform={`translate(${view.x},${view.y}) scale(${view.k})`}>
          {/* Edges first so nodes paint on top */}
          {edges.map((e, i) => {
            const src = nodeMap.get(e.source); const tgt = nodeMap.get(e.target);
            if (!src || !tgt) return null;
            const errored = e.errorRate > 0;
            // An edge is "active" when one of its endpoints is hovered.
            // Active edges paint brighter; inactive edges dim (opacity)
            // when there's any hover state at all.
            const inNeighborhood = !neighborhood || neighborhood.has(e.source) && neighborhood.has(e.target);
            const isHovered = hovered != null && (e.source === hovered || e.target === hovered);
            let color = errored ? '#f85149' : '#3fb950';
            let opacity = errored ? 0.85 : 0.7;
            if (isHovered) {
              color = errored ? '#ff7b72' : '#56d364';
              opacity = 1;
            } else if (hovered && !inNeighborhood) {
              opacity = 0.18;
            }
            // Endpoint = node border, not center, so arrow tip lands cleanly
            const pad = 4;
            const dx = tgt.x - src.x, dy = tgt.y - src.y;
            const len = Math.sqrt(dx * dx + dy * dy) || 1;
            const tx = tgt.x - (dx / len) * (NODE_W / 2 + pad);
            const ty = tgt.y - (dy / len) * (NODE_H / 2 + pad);
            const sx = src.x + (dx / len) * (NODE_W / 2 + pad);
            const sy = src.y + (dy / len) * (NODE_H / 2 + pad);
            const baseW = Math.max(1.2, Math.log1p((e.callCount / maxCalls) * 8) * 1.4);
            const strokeW = isHovered ? baseW + 1.4 : baseW;
            const marker = isHovered
              ? (errored ? 'url(#arr-err-hl)' : 'url(#arr-ok-hl)')
              : (errored ? 'url(#arr-err)' : 'url(#arr-ok)');
            return (
              <path key={i}
                d={`M ${sx} ${sy} L ${tx} ${ty}`}
                stroke={color} strokeWidth={strokeW} fill="none"
                strokeOpacity={opacity}
                strokeDasharray={errored && !isHovered ? '6 4' : undefined}
                markerEnd={marker}
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
            const isExplicit = highlightService === n.name;
            const isHover    = hovered === n.name;
            // Dim a node if there's any active focus (explicit highlight
            // OR hover) and this node isn't part of it.
            const focused = !!(highlightService || hovered);
            const inFocus = isExplicit || isHover ||
                            (highlightService && highlightService === n.name) ||
                            (neighborhood && neighborhood.has(n.name));
            const dim = focused && !inFocus;
            const subtitle = `${style.subtitle} · ${formatMs(n.avgMs)}`;
            const isDragging = nodeDrag.current?.name === n.name;
            return (
              <g key={n.name} className="graph-node"
                 transform={`translate(${n.x - NODE_W / 2},${n.y - NODE_H / 2})`}
                 opacity={dim ? 0.30 : 1}
                 style={{ cursor: isDragging ? 'grabbing' : 'grab' }}
                 onMouseDown={ev => onNodeMouseDown(ev, n.name)}
                 onClick={ev => onNodeClickGuarded(ev, n.name)}
                 onMouseEnter={ev => {
                   setHovered(n.name);
                   setTooltip({
                     x: ev.clientX, y: ev.clientY,
                     text: `${n.name}\n${style.subtitle}\nAvg: ${formatMs(n.avgMs)}\nErr rate: ${n.errorRate.toFixed(1)}%`,
                   });
                 }}
                 onMouseLeave={() => {
                   if (!nodeDrag.current) setHovered(null);
                   setTooltip(null);
                 }}>
                {/* Soft glow halo when this node is hovered or pinned. */}
                {(isHover || isExplicit) && (
                  <rect x={-4} y={-4} width={NODE_W + 8} height={NODE_H + 8} rx="9" ry="9"
                        fill="none" stroke={isExplicit ? '#f0883e' : '#58a6ff'}
                        strokeOpacity={0.28} strokeWidth={6} />
                )}
                <rect width={NODE_W} height={NODE_H} rx="6" ry="6"
                  fill={style.fill}
                  stroke={isExplicit ? '#f0883e' : isHover ? '#58a6ff' : style.stroke}
                  strokeWidth={isExplicit || isHover ? 2 : 1} />
                {/* Icon */}
                <g transform="translate(8, 14)">
                  <NodeIcon kind={n.kind} color={style.fg} />
                </g>
                {/* Name (top line) */}
                <text x={32} y={18} fill={style.fg} fontSize="12" fontWeight="700"
                      style={{ pointerEvents: 'none' }}>
                  {truncate(n.name, 18)}
                </text>
                {/* Subtitle (bottom line) */}
                <text x={32} y={34} fill="#8b949e" fontSize="9" fontWeight="600"
                      letterSpacing="0.5" style={{ pointerEvents: 'none' }}>
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

      {/* Zoom controls + reset layout */}
      <div style={{
        position: 'absolute', left: 8, bottom: 8, display: 'flex', flexDirection: 'column',
        gap: 4, background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 4,
      }}>
        <ZoomBtn onClick={() => setView(v => ({ ...v, k: Math.min(2.5, v.k * 1.25) }))}>+</ZoomBtn>
        <ZoomBtn onClick={() => setView(v => ({ ...v, k: Math.max(0.4, v.k / 1.25) }))}>−</ZoomBtn>
        <ZoomBtn onClick={() => { setView({ x: 0, y: 0, k: 1 }); setNodeOffsets({}); }}
                 title="Reset zoom + node positions">⌂</ZoomBtn>
      </div>

      {/* Hint strip — explain interactions to a first-time user. */}
      <div style={{
        position: 'absolute', right: 8, bottom: 8, padding: '4px 8px',
        background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 4,
        color: 'var(--text3)', fontSize: 10, lineHeight: 1.4, pointerEvents: 'none',
      }}>
        <b style={{ color: 'var(--text2)' }}>Drag</b> a node to move it ·
        {' '}<b style={{ color: 'var(--text2)' }}>Hover</b> to highlight neighbours ·
        {' '}<b style={{ color: 'var(--text2)' }}>Wheel</b> to zoom
      </div>

      {tooltip && (
        <div className="graph-tooltip" style={{ left: tooltip.x + 14, top: tooltip.y - 10 }}>
          {tooltip.text.split('\n').map((l, i) => <div key={i}>{l}</div>)}
        </div>
      )}
    </div>
  );
}

function ZoomBtn({ children, onClick, title }: { children: React.ReactNode; onClick: () => void; title?: string }) {
  return (
    <button onClick={onClick} title={title} style={{
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
