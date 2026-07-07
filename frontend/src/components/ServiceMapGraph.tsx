import { useEffect, useMemo, useRef, useState } from 'react';
import type { ServiceMap, ServiceMapNode } from '@/lib/types';
import { Button } from '@/components/ui/Button';
import { hashColor, isMessagingDep } from '@/lib/utils';

// ServiceMapGraph renders {nodes, edges} as an SVG graph. Two
// layout modes:
//
//   • Global (focus=null): a tiny Verlet-style force simulation
//     runs once on mount, settles, freezes — no main-thread
//     load after the initial layout.
//   • Focused (focus=<service>): closed-form radial layout. The
//     focused service sits at the centre, callers fan out on
//     the left arc, callees on the right arc. No physics needed
//     — the geometry is decided in O(N) — so the swap is
//     instant when the user picks a new service.
//
// Click-to-focus: clicking a node fires onSelectNode, letting
// the page swap the focus without leaving the map. The "View
// service detail →" button on the page handles navigation when
// the operator actually wants to leave.
//
// Hover-highlight: hovering a node dims everything outside its
// own 1-hop neighbourhood, mirroring Datadog / Honeycomb.
export function ServiceMapGraph({
  data: rawData, focus, hoverNode, onHoverNode, onSelectNode, height = 560,
}: {
  data: ServiceMap;
  focus: string | null;
  hoverNode: string | null;
  onHoverNode: (s: string | null) => void;
  onSelectNode: (s: string) => void;
  height?: number;
}) {
  const wrapRef = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(900);

  // Topology shows SYNCHRONOUS call edges only — service→service
  // (grpc / http / rest / soap), service→external, and service→db
  // (redis / postgres / oracle …). Messaging broker nodes are excluded:
  // a single broadcast topic fans in/out to dozens of unrelated
  // producers + consumers, which the force sim can't separate, so the
  // whole graph collapses into an unreadable hairball ("kafka message
  // devreye girince topoloji saçmalıyor"). The broker may arrive as a
  // messaging.system node (kind:"queue") OR — as in the demo — as a
  // peer.service'd external (`ext:kafka`, kind:"external"/subkind:
  // "kafka"); isMessagingDep() catches both while keeping real external
  // HTTP deps (ext:auth-service, ext:email-service). Drop the broker
  // node AND every edge touching it so no dangling stub arrows remain.
  // No-op (same identity) when the sample has no broker — memo stays
  // stable. Messaging keeps its own /messaging surface.
  const data = useMemo<ServiceMap>(() => {
    if (!rawData.nodes.some(n => isMessagingDep(n.kind, n.subkind))) return rawData;
    const drop = new Set(
      rawData.nodes.filter(n => isMessagingDep(n.kind, n.subkind)).map(n => n.service),
    );
    return {
      ...rawData,
      nodes: rawData.nodes.filter(n => !drop.has(n.service)),
      edges: rawData.edges.filter(e => !drop.has(e.caller) && !drop.has(e.callee)),
    };
  }, [rawData]);

  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const ro = new ResizeObserver(entries => {
      for (const e of entries) setWidth(Math.max(400, e.contentRect.width));
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // Pre-compute neighbourhood adjacency for hover dimming.
  const neighbours = useMemo(() => {
    const m = new Map<string, Set<string>>();
    for (const n of data.nodes) m.set(n.service, new Set([n.service]));
    for (const e of data.edges) {
      m.get(e.caller)?.add(e.callee);
      m.get(e.callee)?.add(e.caller);
    }
    return m;
  }, [data]);

  // Both layouts are computed every render (force-layout
  // memoised on data/dimensions; radial is closed-form O(N))
  // so we can switch instantly without re-running physics.
  // Calling the hook unconditionally keeps Rules-of-Hooks happy.
  const forcePositioned = useForceLayout(data, width, height);
  const positioned = focus
    ? radialLayout(data, focus, width, height)
    : forcePositioned;

  const edgeMaxTraces = useMemo(
    () => data.edges.reduce((m, e) => Math.max(m, e.traceCount), 0),
    [data.edges]
  );

  // Collapse bidirectional edges (A→B + B→A) into a single
  // rendered line with arrowheads on both ends. Pre-v0.5.0 the
  // two directions drew as separate overlapping lines, which
  // visually read as one but lost the "this is bidirectional"
  // signal. Mutual-RPC patterns (A serves B + B serves A in
  // the same trace) are now distinguished from one-way edges
  // by the double-headed arrow.
  type RenderedEdge = {
    caller: string; callee: string;
    forward: import('@/lib/types').ServiceMapEdge;
    reverse?: import('@/lib/types').ServiceMapEdge;
  };
  const renderedEdges = useMemo<RenderedEdge[]>(() => {
    const byKey = new Map<string, RenderedEdge>();
    for (const e of data.edges) {
      const canon = e.caller < e.callee
        ? `${e.caller}|${e.callee}`
        : `${e.callee}|${e.caller}`;
      const ex = byKey.get(canon);
      if (!ex) {
        byKey.set(canon, { caller: e.caller, callee: e.callee, forward: e });
      } else {
        // Two edges with the same canonical key → bidirectional.
        // Mark the second one as `reverse` of the first; the
        // renderer adds a markerStart arrow.
        ex.reverse = e;
      }
    }
    return Array.from(byKey.values());
  }, [data.edges]);

  // Which nodes count as "active" right now — a hovered node's
  // own neighbourhood, otherwise everything. Used to dim the
  // off-path edges/nodes.
  const active = hoverNode ? (neighbours.get(hoverNode) ?? new Set([hoverNode])) : null;

  // Pan/zoom state. Identity = full graph in view. Scale > 1
  // zooms in, scale < 1 zooms out. (vx, vy) is the upper-left
  // corner of the visible region in graph coordinates.
  // Operators on banking-scale clusters (100+ services,
  // 500+ edges) need to read individual labels in the dense
  // areas without losing global context; the closed-form
  // radial layout still snaps to focus mode for the
  // hierarchical view, but plain pan+zoom is the fast way to
  // explore everything else.
  const [view, setView] = useState({ vx: 0, vy: 0, scale: 1 });
  // Reset on data identity change so a new fetch doesn't
  // strand the operator on an off-screen viewport.
  useEffect(() => { setView({ vx: 0, vy: 0, scale: 1 }); }, [data, focus]);
  const svgRef = useRef<SVGSVGElement>(null);
  const panRef = useRef<{ startX: number; startY: number; vx: number; vy: number } | null>(null);

  const onWheel = (e: React.WheelEvent<SVGSVGElement>) => {
    e.preventDefault();
    const rect = svgRef.current?.getBoundingClientRect();
    if (!rect) return;
    // Anchor zoom around the cursor so the point under the
    // operator's eye stays put — Figma / Miro convention.
    const cursorX = ((e.clientX - rect.left) / rect.width)  * (width  / view.scale) + view.vx;
    const cursorY = ((e.clientY - rect.top)  / rect.height) * (height / view.scale) + view.vy;
    // Logarithmic zoom rate — matches typical trackpads.
    const delta = -e.deltaY * 0.0015;
    const nextScale = Math.max(0.25, Math.min(6, view.scale * (1 + delta)));
    // Re-compute upper-left so cursor stays anchored.
    const nextVx = cursorX - ((e.clientX - rect.left) / rect.width)  * (width  / nextScale);
    const nextVy = cursorY - ((e.clientY - rect.top)  / rect.height) * (height / nextScale);
    setView({ vx: nextVx, vy: nextVy, scale: nextScale });
  };
  const onMouseDownPan = (e: React.MouseEvent<SVGSVGElement>) => {
    // Only middle-click or space-modified drag pans; left-click
    // remains for node hit-testing. Right-click ignored so the
    // browser context menu stays available.
    if (e.button !== 1 && !(e.button === 0 && (e.shiftKey || e.altKey))) return;
    e.preventDefault();
    panRef.current = { startX: e.clientX, startY: e.clientY, vx: view.vx, vy: view.vy };
  };
  useEffect(() => {
    const onMove = (e: MouseEvent) => {
      if (!panRef.current) return;
      const dx = (e.clientX - panRef.current.startX) * (width  / view.scale) / Math.max(1, svgRef.current?.clientWidth  ?? width);
      const dy = (e.clientY - panRef.current.startY) * (height / view.scale) / Math.max(1, svgRef.current?.clientHeight ?? height);
      setView(v => ({ ...v, vx: panRef.current!.vx - dx, vy: panRef.current!.vy - dy }));
    };
    const onUp = () => { panRef.current = null; };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup',   onUp);
    return () => {
      window.removeEventListener('mousemove', onMove);
      window.removeEventListener('mouseup',   onUp);
    };
  }, [width, height, view.scale]);

  const resetView = () => setView({ vx: 0, vy: 0, scale: 1 });
  const canReset = view.scale !== 1 || view.vx !== 0 || view.vy !== 0;

  return (
    <div ref={wrapRef} style={{ width: '100%', position: 'relative' }}>
      {canReset && (
        <Button variant="secondary" size="sm" onClick={resetView}
          title="Reset zoom + pan"
          style={{ position: 'absolute', top: 8, right: 8, zIndex: 2 }}>
          Reset · {Math.round(view.scale * 100)}%
        </Button>
      )}
      <svg ref={svgRef}
           viewBox={`${view.vx} ${view.vy} ${width / view.scale} ${height / view.scale}`}
           onWheel={onWheel}
           onMouseDown={onMouseDownPan}
           style={{
             width: '100%', height, display: 'block',
             background: 'var(--bg1)',
             border: '1px solid var(--border)',
             borderRadius: 8,
             cursor: panRef.current ? 'grabbing' : 'default',
           }}>
        <defs>
          <marker id="svc-arrow" viewBox="0 0 10 10"
                  refX="10" refY="5" markerWidth="6" markerHeight="6"
                  orient="auto-start-reverse">
            <path d="M0,0 L10,5 L0,10 z" fill="var(--text3)" />
          </marker>
          <marker id="svc-arrow-err" viewBox="0 0 10 10"
                  refX="10" refY="5" markerWidth="6" markerHeight="6"
                  orient="auto-start-reverse">
            <path d="M0,0 L10,5 L0,10 z" fill="var(--err)" />
          </marker>
          {/* Soft glow under nodes — restrained, just enough to
              lift them off the background. SVG filter is GPU-
              composited so the cost is per-pixel-once, not per
              re-render. */}
          <filter id="svc-shadow" x="-30%" y="-30%" width="160%" height="160%">
            <feGaussianBlur in="SourceAlpha" stdDeviation="2.5" />
            <feOffset dx="0" dy="1" result="off" />
            <feComponentTransfer>
              <feFuncA type="linear" slope="0.35" />
            </feComponentTransfer>
            <feMerge>
              <feMergeNode />
              <feMergeNode in="SourceGraphic" />
            </feMerge>
          </filter>
        </defs>

        {/* Edges first so nodes draw on top. Bidirectional
            pairs render as one line with arrowheads on both
            ends; the title tooltip lists both directions. */}
        {renderedEdges.map((re, i) => {
          const e = re.forward;
          const rev = re.reverse;
          const a = positioned[e.caller], b = positioned[e.callee];
          if (!a || !b) return null;
          // Combined call volume for the stroke width when
          // bidirectional — one fat line carries the merged
          // weight instead of two thin overlapping ones.
          const totalTraces = e.traceCount + (rev?.traceCount ?? 0);
          const w = 0.6 + 2.6 * (totalTraces / Math.max(1, edgeMaxTraces));
          const errorish = e.errorCount > 0 || (rev?.errorCount ?? 0) > 0;
          const isNew = e.isNew || rev?.isNew;
          const dimmed = active && (!active.has(e.caller) || !active.has(e.callee));
          const ra = nodeRadius(a.n);
          const rb = nodeRadius(b.n);
          const dx = b.x - a.x, dy = b.y - a.y;
          const d  = Math.max(1, Math.hypot(dx, dy));
          const x1 = a.x + dx * ra / d, y1 = a.y + dy * ra / d;
          const x2 = b.x - dx * rb / d, y2 = b.y - dy * rb / d;
          const strokeColor = isNew ? 'var(--ok)'
                            : errorish ? 'var(--err)'
                            : 'var(--text3)';
          const strokeOp = isNew ? 0.9 : errorish ? 0.85 : 0.55;
          const arrow = isNew ? 'url(#svc-arrow)' :
                        errorish ? 'url(#svc-arrow-err)' : 'url(#svc-arrow)';
          return (
            <g key={i} style={{ opacity: dimmed ? 0.12 : 1, transition: 'opacity 120ms' }}>
              <line x1={x1} y1={y1} x2={x2} y2={y2}
                    stroke={strokeColor}
                    strokeWidth={isNew ? w + 0.8 : w}
                    strokeDasharray={isNew ? '4 3' : undefined}
                    opacity={strokeOp}
                    markerEnd={arrow}
                    /* Reverse direction → arrowhead on the start
                       too. SVG marker-start needs a marker that
                       points the opposite way; the existing
                       `svc-arrow` uses orient="auto-start-reverse"
                       so it auto-flips for markerStart. */
                    markerStart={rev ? arrow : undefined}>
                <title>
                  {`${e.caller} → ${e.callee}\n` +
                   `${e.traceCount} traces · ${e.spanCount} spans` +
                   (e.errorCount > 0 ? ` · ${e.errorCount} errors` : '') +
                   (rev ? `\n\n${rev.caller} → ${rev.callee}\n` +
                          `${rev.traceCount} traces · ${rev.spanCount} spans` +
                          ((rev.errorCount ?? 0) > 0 ? ` · ${rev.errorCount} errors` : '')
                        : '') +
                   (isNew ? '\n[NEW since baseline]' : '')}
                </title>
              </line>
            </g>
          );
        })}

        {Object.values(positioned).map(p => {
          const dim = active && !active.has(p.n.service);
          const isFocus = focus === p.n.service;
          const isHover = hoverNode === p.n.service;
          return (
            <NodeMark
              key={p.n.service}
              x={p.x} y={p.y}
              n={p.n}
              isFocus={isFocus}
              isHover={isHover}
              dim={!!dim}
              onHover={onHoverNode}
              onSelect={onSelectNode}
            />
          );
        })}
      </svg>

      <div style={{
        marginTop: 6, display: 'flex', gap: 16, alignItems: 'center',
        fontSize: 11, color: 'var(--text3)', flexWrap: 'wrap',
      }}>
        <span>
          {data.nodes.filter(n => !n.kind).length} services · {data.nodes.filter(n => n.kind).length} deps
          {' · '}{data.edges.length} edges
          {' · '}sampled {data.sampledFrom} traces ({data.totalSpans} spans)
        </span>
        <span style={{ marginLeft: 'auto', display: 'inline-flex', alignItems: 'center', gap: 12 }}>
          <span style={{ display: 'inline-flex', gap: 4, alignItems: 'center' }}>
            ○ service · ▢ db · ⬡ queue · ◇ external
          </span>
          <span style={{ display: 'inline-flex', gap: 6, alignItems: 'center' }}>
            <Dot color="var(--ok)" />   healthy
            <Dot color="var(--warn)" /> &gt;1% err
            <Dot color="var(--err)" />  &gt;5% err
          </span>
        </span>
      </div>
    </div>
  );
}

function Dot({ color }: { color: string }) {
  return <span style={{ width: 9, height: 9, borderRadius: '50%', background: color, display: 'inline-block', marginLeft: 8 }} />;
}

function NodeMark({
  x, y, n, isFocus, isHover, dim, onHover, onSelect,
}: {
  x: number; y: number; n: ServiceMapNode;
  isFocus: boolean; isHover: boolean; dim: boolean;
  onHover: (s: string | null) => void;
  onSelect: (s: string) => void;
}) {
  const r = nodeRadius(n);
  const baseFill = n.errorRate > 0.05 ? 'var(--err)'
                 : n.errorRate > 0.01 ? 'var(--warn)'
                 : n.kind ? 'var(--text3)'   // dep nodes use neutral grey when healthy
                          : 'var(--ok)';
  const ringW = isFocus ? 2.6 : isHover ? 2.0 : 1.4;
  const fillOp = isFocus ? 0.30 : isHover ? 0.24 : 0.18;
  // Dep nodes (db / queue / external) get a distinct shape so
  // an operator can tell at a glance whether a node is "your
  // code" or "your dependency". Datadog uses similar shape
  // discrimination — circle = service, rounded square = db,
  // hexagon = queue, diamond = external.
  const label = displayLabel(n);
  return (
    <g style={{
         cursor: 'pointer',
         opacity: dim ? 0.22 : 1,
         transition: 'opacity 120ms',
       }}
       onMouseEnter={() => onHover(n.service)}
       onMouseLeave={() => onHover(null)}
       onClick={() => onSelect(n.service)}>
      {/* Pulse ring on freshly-appeared nodes — backend marks
          isNew when topology diff is enabled. The ring sits
          outside the regular outline and animates so the eye
          drops to it before reading the rest of the map. */}
      {n.isNew && (
        <circle cx={x} cy={y} r={r + 6}
                fill="none" stroke="var(--ok)" strokeWidth={1.5}
                strokeOpacity={0.55}>
          <animate attributeName="r" from={r + 2} to={r + 10} dur="1.6s" repeatCount="indefinite" />
          <animate attributeName="stroke-opacity" from={0.7} to={0} dur="1.6s" repeatCount="indefinite" />
        </circle>
      )}
      {/* Cluster ring — thin coloured outline keyed by the
          node's enriched cluster name. Stable hashColor on the
          cluster string so every node in prod-eu-west picks
          the same hue regardless of service. Multi-cluster
          services get a neutral dashed ring so the operator
          spots them at a glance. Dep nodes (db / queue / ext)
          skip the ring — they're synthesised, not in a cluster
          themselves. */}
      {!n.kind && n.cluster && n.cluster !== 'multi' && (
        <circle cx={x} cy={y} r={r + 3}
                fill="none"
                stroke={hashColor(n.cluster)}
                strokeWidth={1.5}
                strokeOpacity={0.85} />
      )}
      {!n.kind && n.cluster === 'multi' && (
        <circle cx={x} cy={y} r={r + 3}
                fill="none"
                stroke="var(--text3)"
                strokeWidth={1.5}
                strokeDasharray="3 2"
                strokeOpacity={0.75} />
      )}
      <DepShape kind={n.kind} cx={x} cy={y} r={r}
                fill={baseFill} fillOpacity={fillOp}
                stroke={baseFill} strokeWidth={ringW} />
      {isFocus && !n.kind && (
        <circle cx={x} cy={y} r={3.2}
                fill={baseFill} stroke="var(--bg1)" strokeWidth={1} />
      )}
      {/* Kind glyph at centre of dep nodes — tiny visual cue
          that supplements the shape: 𝕊 for db, ⌬ for queue,
          ⇗ for external. */}
      {n.kind && (
        <text x={x} y={y + 4}
              textAnchor="middle" fontSize={11} fontWeight={700}
              fill={baseFill} style={{ pointerEvents: 'none' }}>
          {kindGlyph(n.kind)}
        </text>
      )}
      <text x={x} y={y - r - 7}
            textAnchor="middle"
            fontSize={isFocus ? 12 : 11}
            fontWeight={isFocus || isHover ? 700 : 600}
            fill="var(--text)" style={{ pointerEvents: 'none' }}>
        {label}
      </text>
      {/* Dep nodes already display the kind glyph centrally;
          showing both the glyph and the span count overlaps.
          Real services keep the span count under their name. */}
      {!n.kind && (
        <text x={x} y={y + 4}
              textAnchor="middle"
              fontSize={10}
              fill="var(--text2)"
              fontFamily="monospace"
              style={{ pointerEvents: 'none' }}>
          {n.spanCount.toLocaleString()}
        </text>
      )}
      <title>
        {`${label}${n.kind ? ` · ${kindLabel(n.kind)}` : ''}\n` +
         `${n.spanCount.toLocaleString()} spans · ${(n.errorRate * 100).toFixed(2)}% error\n` +
         (n.cluster ? `cluster: ${n.cluster}\n` : '') +
         (n.kind ? 'dependency synthesised from db.system / peer.service' : 'click to focus')}
      </title>
    </g>
  );
}

// displayLabel — strip the synthetic prefix from dep node
// names so the operator sees "redis" not "db:redis".
function displayLabel(n: ServiceMapNode): string {
  return n.subkind || n.service.replace(/^(db|queue|ext):/, '');
}

function kindGlyph(kind: string): string {
  switch (kind) {
    case 'db':       return '𝕊';   // database
    case 'queue':    return '⌬';   // hexagonal — queues
    case 'external': return '⇗';   // outbound arrow
    default:         return '';
  }
}

function kindLabel(kind: string): string {
  switch (kind) {
    case 'db':       return 'database';
    case 'queue':    return 'queue';
    case 'external': return 'external dependency';
    default:         return kind;
  }
}

// DepShape — picks the SVG primitive matching the node's kind.
// Real services are circles. Dep nodes use distinct shapes so
// the topology is readable at a glance.
function DepShape({ kind, cx, cy, r, fill, fillOpacity, stroke, strokeWidth }: {
  kind?: string;
  cx: number; cy: number; r: number;
  fill: string; fillOpacity: number;
  stroke: string; strokeWidth: number;
}) {
  const common = {
    fill, fillOpacity, stroke, strokeWidth,
    filter: 'url(#svc-shadow)',
  };
  if (kind === 'db') {
    // Rounded square — the canonical "datastore" glyph in
    // architecture diagrams.
    return (
      <rect x={cx - r} y={cy - r} width={r * 2} height={r * 2}
            rx={r * 0.32} ry={r * 0.32} {...common} />
    );
  }
  if (kind === 'queue') {
    // Hexagon — convention for messaging in tooling like Kiali.
    const a = r * 1.05;
    const pts = [
      `${cx - a},${cy}`,
      `${cx - a / 2},${cy - r * 0.92}`,
      `${cx + a / 2},${cy - r * 0.92}`,
      `${cx + a},${cy}`,
      `${cx + a / 2},${cy + r * 0.92}`,
      `${cx - a / 2},${cy + r * 0.92}`,
    ].join(' ');
    return <polygon points={pts} {...common} />;
  }
  if (kind === 'external') {
    // Diamond — distinguishes "outside the OTel mesh" calls
    // from local services and infra deps.
    const pts = [
      `${cx},${cy - r}`,
      `${cx + r},${cy}`,
      `${cx},${cy + r}`,
      `${cx - r},${cy}`,
    ].join(' ');
    return <polygon points={pts} {...common} />;
  }
  return <circle cx={cx} cy={cy} r={r} {...common} />;
}

function nodeRadius(n: ServiceMapNode): number {
  // log scale so a 100k-span service doesn't drown out a 100-span one.
  // Dep nodes are slightly smaller so they don't visually
  // dominate the actual services.
  const log = Math.log10(Math.max(10, n.spanCount));
  const base = n.kind ? 7 + 4 * log : 9 + 5 * log;
  return Math.min(34, Math.max(13, base));
}

interface PositionedNode {
  n: ServiceMapNode;
  x: number; y: number;
  vx: number; vy: number;
}

// ── Focused (radial) layout ─────────────────────────────────
//
// Closed-form geometry — no physics. Focused service sits at
// the centre; callers and callees split the circle into left
// and right halves so the "who calls me" and "who do I call"
// directions are visually separated. Within each half, peers
// are sorted by traffic volume (desc) so the heavy-hitter
// edges land top-of-the-arc.
function radialLayout(
  data: ServiceMap, focus: string, width: number, height: number,
): Record<string, PositionedNode> {
  const cx = width / 2, cy = height / 2;
  const out: Record<string, PositionedNode> = {};
  const focusNode = data.nodes.find(n => n.service === focus);
  if (!focusNode) return out;
  out[focus] = { n: focusNode, x: cx, y: cy, vx: 0, vy: 0 };

  const callers: { svc: string; volume: number }[] = [];
  const callees: { svc: string; volume: number }[] = [];
  for (const e of data.edges) {
    if (e.callee === focus) callers.push({ svc: e.caller, volume: e.traceCount });
    if (e.caller === focus) callees.push({ svc: e.callee, volume: e.traceCount });
  }
  callers.sort((a, b) => b.volume - a.volume);
  callees.sort((a, b) => b.volume - a.volume);

  const radius = Math.min(width, height) * 0.34;

  // Left arc: π/2 (top) sweeping through π (left) to 3π/2 (bottom).
  placeArc(out, data, callers.map(c => c.svc), cx, cy, radius, Math.PI / 2, 3 * Math.PI / 2);
  // Right arc: 3π/2 (bottom) sweeping back through 2π (right) to π/2 (top).
  // Use the same direction (so single-callee lands at the right)
  placeArc(out, data, callees.map(c => c.svc), cx, cy, radius, -Math.PI / 2, Math.PI / 2);

  return out;
}

function placeArc(
  out: Record<string, PositionedNode>,
  data: ServiceMap,
  services: string[],
  cx: number, cy: number, r: number,
  startAngle: number, endAngle: number,
) {
  if (services.length === 0) return;
  const span = endAngle - startAngle;
  for (let i = 0; i < services.length; i++) {
    const svc = services[i];
    const node = data.nodes.find(n => n.service === svc);
    if (!node) continue;
    // Even spread, with a single peer landing at the arc midpoint.
    const t = services.length === 1 ? 0.5 : i / (services.length - 1);
    const angle = startAngle + span * t;
    out[svc] = {
      n: node,
      x: cx + Math.cos(angle) * r,
      y: cy + Math.sin(angle) * r,
      vx: 0, vy: 0,
    };
  }
}

// ── Global (force) layout ───────────────────────────────────
//
// Verlet pairwise: ≤200 nodes settles in <50ms on a laptop.
// useMemo'd by (data, width, height) so panning the view (or
// re-renders from hover state) doesn't re-run the simulation.
function useForceLayout(
  data: ServiceMap, width: number, height: number,
): Record<string, PositionedNode> {
  return useMemo(() => {
    const cx = width / 2, cy = height / 2;
    const nodes: Record<string, PositionedNode> = {};
    data.nodes.forEach((n, i) => {
      const seed = (hash(n.service) >>> 0);
      const angle = (seed / 0xffffffff) * Math.PI * 2;
      const ring  = Math.min(1, 0.4 + 0.6 * (i / Math.max(1, data.nodes.length)));
      nodes[n.service] = {
        n,
        x: cx + Math.cos(angle) * ring * Math.min(width, height) * 0.32,
        y: cy + Math.sin(angle) * ring * Math.min(width, height) * 0.32,
        vx: 0, vy: 0,
      };
    });
    if (data.nodes.length === 0) return nodes;

    const linkLen = 110;
    const k_repel = 5800;
    const k_link  = 0.045;
    const k_centre = 0.012;
    const damping = 0.86;
    const iters = 220;

    const arr = Object.values(nodes);
    for (let it = 0; it < iters; it++) {
      for (let i = 0; i < arr.length; i++) {
        for (let j = i + 1; j < arr.length; j++) {
          const a = arr[i], b = arr[j];
          let dx = a.x - b.x, dy = a.y - b.y;
          let d2 = dx*dx + dy*dy;
          if (d2 < 1) { dx = (Math.random() - 0.5); dy = (Math.random() - 0.5); d2 = 1; }
          const f = k_repel / d2;
          const d = Math.sqrt(d2);
          a.vx += f * dx / d; a.vy += f * dy / d;
          b.vx -= f * dx / d; b.vy -= f * dy / d;
        }
      }
      for (const e of data.edges) {
        const a = nodes[e.caller], b = nodes[e.callee];
        if (!a || !b) continue;
        const dx = b.x - a.x, dy = b.y - a.y;
        const d = Math.max(0.01, Math.hypot(dx, dy));
        const f = k_link * (d - linkLen);
        a.vx += f * dx / d; a.vy += f * dy / d;
        b.vx -= f * dx / d; b.vy -= f * dy / d;
      }
      for (const n of arr) {
        n.vx += (cx - n.x) * k_centre;
        n.vy += (cy - n.y) * k_centre;
      }
      for (const n of arr) {
        n.vx *= damping; n.vy *= damping;
        n.x += n.vx;     n.y += n.vy;
        n.x = Math.max(46, Math.min(width  - 46, n.x));
        n.y = Math.max(46, Math.min(height - 46, n.y));
      }
    }
    return nodes;
  }, [data, width, height]);
}

function hash(s: string): number {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h;
}
