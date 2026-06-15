import { useEffect, useMemo, useRef, useState, useCallback } from 'react';
import dagre from 'dagre';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { timeRangeToNs } from '@/lib/utils';
import { Spinner, Empty } from '@/components/Spinner';
import { mapNumber, nodeSizeMetric } from '@/lib/topologyNodes';
import type { NodeSizeMode, NodeSizeMetric } from '@/lib/topologyNodes';
import type { TimeRange } from '@/lib/types';
import type { GraphNode, GraphEdge, GraphNodeKind, ServiceGraphResponse } from '@/lib/types';

// ServiceGraph (v0.8.12 — topology rebuild Stage 2) — the ONE canonical
// OpenTelemetry-native service map. Consumes the compact {nodes,edges} payload
// from /api/servicegraph (built off the topology_edges_5m MV; no client span
// scan), lays it out with dagre (a real layered-DAG engine — handles cycles,
// brokers and arbitrary fan-out with ZERO special-case code), and renders on
// <canvas> for 60fps on large graphs (off-screen nodes are culled).
//
// Node color = health (error rate green/amber/red); edge thickness = log(calls);
// edge color = error rate. Messaging brokers + databases are first-class nodes.
// Interaction is deliberately minimal: hover → highlight neighbors + dim rest;
// click → navigate to the service; pan/zoom + Fit + a health legend.
//
// The SAME component serves the Service "Topology" tab (scope=neighborhood +
// focus) and the full /topology page (scope=global) via props.

const NODE_W = 156; // fallback card width when no size metric is supplied
const NODE_H = 38;
// Node-size encoding (v0.8.x — Uptrace adapt, slice 2): card WIDTH encodes
// outgoing throughput. MIN_W keeps the labelled name readable (Uptrace's 10-40px
// dots are too small for Coremetry's named cards); MAX_W caps the widest node so
// one hot service can't dwarf the canvas. Height stays fixed at NODE_H.
const MIN_W = 140;
const MAX_W = 220;

interface Pal {
  bg0: string; bg1: string; bg2: string;
  text: string; text2: string; text3: string;
  border: string; ok: string; warn: string; err: string; accent: string;
}

function readPalette(): Pal {
  const cs = getComputedStyle(document.documentElement);
  const v = (n: string) => cs.getPropertyValue(n).trim() || '#888';
  return {
    bg0: v('--bg0'), bg1: v('--bg1'), bg2: v('--bg2'),
    text: v('--text'), text2: v('--text2'), text3: v('--text3'),
    border: v('--border'), ok: v('--ok'), warn: v('--warn'), err: v('--err'), accent: v('--accent'),
  };
}

// usePalette resolves the CSS-variable tokens to concrete colors the canvas can
// use, and re-resolves when the theme flips (light/dark) via data-theme.
function usePalette(): Pal {
  const [pal, setPal] = useState<Pal>(readPalette);
  useEffect(() => {
    const obs = new MutationObserver(() => setPal(readPalette()));
    obs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
    return () => obs.disconnect();
  }, []);
  return pal;
}

// healthColor maps a node/edge error rate to the green/amber/red token.
function healthColor(errorRate: number, pal: Pal): string {
  if (errorRate >= 5) return pal.err;
  if (errorRate >= 1) return pal.warn;
  return pal.ok;
}

const KIND_BADGE: Record<GraphNodeKind, string> = {
  service: '', database: 'DB', queue: 'Q', external: 'EXT', internal: '·',
};

interface Layout {
  pos: Map<string, { x: number; y: number; w: number; h: number }>;
  edges: Array<{ e: GraphEdge; pts: Array<{ x: number; y: number }> }>;
  width: number;
  height: number;
}

// layoutGraph lays the graph out with dagre. `widthOf` returns the per-node card
// width (the size-encoding map from nodeWidths); dagre packs ranks by the ACTUAL
// width so nodesep/ranksep account for variable-width cards (no overlap), and the
// bbox below uses each node's real width so Fit frames variable-width graphs.
function layoutGraph(data: ServiceGraphResponse, widthOf: (id: string) => number): Layout {
  const g = new dagre.graphlib.Graph();
  g.setGraph({ rankdir: 'LR', nodesep: 22, ranksep: 64, marginx: 28, marginy: 28 });
  g.setDefaultEdgeLabel(() => ({}));
  const ids = new Set(data.nodes.map(n => n.id));
  for (const n of data.nodes) g.setNode(n.id, { width: widthOf(n.id), height: NODE_H });
  for (const e of data.edges) {
    if (ids.has(e.source) && ids.has(e.target) && e.source !== e.target) g.setEdge(e.source, e.target);
  }
  dagre.layout(g);
  const pos = new Map<string, { x: number; y: number; w: number; h: number }>();
  let width = 0, height = 0;
  for (const id of g.nodes()) {
    const n = g.node(id);
    if (!n) continue;
    pos.set(id, { x: n.x, y: n.y, w: n.width, h: n.height });
    width = Math.max(width, n.x + n.width);
    height = Math.max(height, n.y + n.height);
  }
  const edges = data.edges.map(e => {
    const ge = ids.has(e.source) && ids.has(e.target) ? g.edge(e.source, e.target) : undefined;
    return { e, pts: (ge?.points as Array<{ x: number; y: number }>) ?? [] };
  });
  return { pos, edges, width: width + 28, height: height + 28 };
}

export function ServiceGraph({
  scope, focus, range, height = 560, onSelectService,
  nodeSizeMode: nodeSizeModeProp, nodeSizeMetric: nodeSizeMetricProp, onNodeSizeChange,
}: {
  scope: 'global' | 'neighborhood';
  focus?: string;
  range: TimeRange;
  height?: number;
  onSelectService?: (service: string) => void;
  // Node-size encoding axes (v0.8.x — Uptrace adapt, slice 3). Optional so the
  // standalone service-detail Topology tab works with no wiring (defaults
  // outgoing/rate). When a controlled value + onNodeSizeChange callback are
  // supplied (the /topology page lifts these to the URL), the toggles are
  // fully controlled; otherwise they fall back to LOCAL state below.
  nodeSizeMode?: NodeSizeMode;
  nodeSizeMetric?: NodeSizeMetric;
  onNodeSizeChange?: (mode: NodeSizeMode, metric: NodeSizeMetric) => void;
}) {
  const pal = usePalette();
  const wrapRef = useRef<HTMLDivElement | null>(null);
  const canvasRef = useRef<HTMLCanvasElement | null>(null);

  // Controlled-or-local toggle state. If the parent passes a value, that wins;
  // otherwise we own a local useState so the component is self-sufficient. The
  // setter always re-roll's client-side — it NEVER touches the query key.
  const [localMode, setLocalMode] = useState<NodeSizeMode>('outgoing');
  const [localMetric, setLocalMetric] = useState<NodeSizeMetric>('rate');
  const sizeMode = nodeSizeModeProp ?? localMode;
  const sizeMetric = nodeSizeMetricProp ?? localMetric;
  const applySize = useCallback((mode: NodeSizeMode, metric: NodeSizeMetric) => {
    if (onNodeSizeChange) onNodeSizeChange(mode, metric);
    else { setLocalMode(mode); setLocalMetric(metric); }
  }, [onNodeSizeChange]);

  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);
  const q = useQuery<ServiceGraphResponse>({
    // NOTE: the query key deliberately does NOT include sizeMode/sizeMetric —
    // toggling them re-rolls the SAME fetched payload client-side (the whole
    // point of slice 3, like Uptrace). A key change here would refetch.
    queryKey: ['servicegraph', scope, focus ?? '', from, to],
    queryFn: () => api.serviceGraph({ scope, focus: focus || undefined, from, to }),
    staleTime: 30_000,
  });

  // Node-size encoding (v0.8.x — Uptrace adapt, slice 3): width encodes the
  // selected mode (incoming|outgoing) × metric (rate|duration). Pure
  // client-side reduce over the already-fetched payload (no new fetch). The
  // metric → width map drives BOTH the dagre layout and the canvas draw via
  // layout.pos[*].w, so packing and rendering agree exactly. mapNumber's
  // [MIN_W,MAX_W] mapping is metric-agnostic — it normalises against the max of
  // whatever metric is selected, so toggling just re-scales the widths.
  const nodeWidths = useMemo(() => {
    const m = new Map<string, number>();
    if (!q.data) return m;
    const { metric, max } = nodeSizeMetric(q.data.nodes, q.data.edges, sizeMode, sizeMetric);
    for (const n of q.data.nodes) {
      m.set(n.id, mapNumber(metric.get(n.id) ?? 0, 0, max, MIN_W, MAX_W));
    }
    return m;
  }, [q.data, sizeMode, sizeMetric]);

  const layout = useMemo(
    () => (q.data && q.data.nodes.length
      ? layoutGraph(q.data, (id) => nodeWidths.get(id) ?? MIN_W)
      : null),
    [q.data, nodeWidths],
  );
  const nodeById = useMemo(() => {
    const m = new Map<string, GraphNode>();
    for (const n of q.data?.nodes ?? []) m.set(n.id, n);
    return m;
  }, [q.data]);
  // adjacency for hover highlight (undirected neighbor set per node).
  const neighbors = useMemo(() => {
    const m = new Map<string, Set<string>>();
    for (const e of q.data?.edges ?? []) {
      (m.get(e.source) ?? m.set(e.source, new Set()).get(e.source)!).add(e.target);
      (m.get(e.target) ?? m.set(e.target, new Set()).get(e.target)!).add(e.source);
    }
    return m;
  }, [q.data]);

  // view transform: scale + translate (world → screen).
  const [view, setView] = useState({ scale: 1, tx: 0, ty: 0 });
  const [hover, setHover] = useState<string | null>(null);
  const drag = useRef<{ px: number; py: number; tx: number; ty: number } | null>(null);

  const fit = useCallback(() => {
    const el = wrapRef.current;
    if (!el || !layout) return;
    const vw = el.clientWidth, vh = height;
    const scale = Math.min(vw / layout.width, vh / layout.height, 1.5) * 0.92;
    const tx = (vw - layout.width * scale) / 2;
    const ty = (vh - layout.height * scale) / 2;
    setView({ scale, tx, ty });
  }, [layout, height]);

  // Fit when a new layout lands. rAF so the canvas/container has settled its
  // width first (fixing the first-paint off-center), + a ResizeObserver so the
  // graph re-frames when the panel resizes (v0.8.14).
  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const raf = requestAnimationFrame(fit);
    const ro = new ResizeObserver(() => fit());
    ro.observe(el);
    return () => { cancelAnimationFrame(raf); ro.disconnect(); };
  }, [fit]);

  // ── draw ───────────────────────────────────────────────────────────────────
  useEffect(() => {
    const canvas = canvasRef.current;
    const el = wrapRef.current;
    if (!canvas || !el || !layout) return;
    const dpr = window.devicePixelRatio || 1;
    const vw = el.clientWidth, vh = height;
    canvas.width = Math.round(vw * dpr);
    canvas.height = Math.round(vh * dpr);
    canvas.style.width = vw + 'px';
    canvas.style.height = vh + 'px';
    const ctx = canvas.getContext('2d');
    if (!ctx) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, vw, vh);
    ctx.save();
    ctx.translate(view.tx, view.ty);
    ctx.scale(view.scale, view.scale);

    const hoverSet = hover ? (neighbors.get(hover) ?? new Set<string>()) : null;
    const isLit = (id: string) => !hover || id === hover || (hoverSet?.has(id) ?? false);

    // viewport bounds in world coords for culling. Pad by MAX_W so the widest
    // size-encoded cards aren't culled a frame early at the viewport edges.
    const minX = -view.tx / view.scale - MAX_W;
    const minY = -view.ty / view.scale - NODE_H;
    const maxX = (vw - view.tx) / view.scale + MAX_W;
    const maxY = (vh - view.ty) / view.scale + NODE_H;

    // edges first
    for (const { e, pts } of layout.edges) {
      if (pts.length < 2) continue;
      const lit = !hover || e.source === hover || e.target === hover;
      const col = healthColor(e.errorRate, pal);
      ctx.globalAlpha = lit ? 0.85 : 0.12;
      ctx.strokeStyle = col;
      ctx.lineWidth = Math.max(0.6, Math.min(6, Math.log10(e.calls + 1)));
      ctx.beginPath();
      ctx.moveTo(pts[0].x, pts[0].y);
      for (let i = 1; i < pts.length; i++) ctx.lineTo(pts[i].x, pts[i].y);
      ctx.stroke();
      // arrowhead at the target end
      const a = pts[pts.length - 2], b = pts[pts.length - 1];
      const ang = Math.atan2(b.y - a.y, b.x - a.x);
      ctx.beginPath();
      ctx.moveTo(b.x, b.y);
      ctx.lineTo(b.x - 7 * Math.cos(ang - 0.4), b.y - 7 * Math.sin(ang - 0.4));
      ctx.lineTo(b.x - 7 * Math.cos(ang + 0.4), b.y - 7 * Math.sin(ang + 0.4));
      ctx.closePath();
      ctx.fillStyle = col;
      ctx.fill();
    }
    ctx.globalAlpha = 1;

    // nodes
    ctx.font = '600 12px -apple-system, "Segoe UI", sans-serif';
    ctx.textBaseline = 'middle';
    for (const [id, p] of layout.pos) {
      if (p.x + p.w / 2 < minX || p.x - p.w / 2 > maxX || p.y + p.h / 2 < minY || p.y - p.h / 2 > maxY) continue; // cull
      const n = nodeById.get(id);
      if (!n) continue;
      const lit = isLit(id);
      const x = p.x - p.w / 2, y = p.y - p.h / 2;
      ctx.globalAlpha = lit ? 1 : 0.22;
      // card
      ctx.fillStyle = pal.bg1;
      ctx.strokeStyle = id === hover ? pal.accent : pal.border;
      ctx.lineWidth = id === hover ? 2 : 1;
      roundRect(ctx, x, y, p.w, p.h, 7);
      ctx.fill();
      ctx.stroke();
      // health stripe (left)
      ctx.fillStyle = healthColor(n.errorRate, pal);
      roundRect(ctx, x, y, 4, p.h, 7);
      ctx.fill();
      // kind badge
      const badge = KIND_BADGE[n.kind];
      let labelX = x + 12;
      if (badge) {
        ctx.font = '700 9px ui-monospace, monospace';
        ctx.fillStyle = pal.text3;
        ctx.fillText(badge, x + 12, p.y);
        labelX = x + 12 + ctx.measureText(badge).width + 7;
        ctx.font = '600 12px -apple-system, "Segoe UI", sans-serif';
      }
      // name (clipped)
      ctx.fillStyle = pal.text;
      const maxW = p.w - (labelX - x) - 10;
      ctx.fillText(clip(ctx, n.name, maxW), labelX, p.y);
    }
    ctx.restore();
    ctx.globalAlpha = 1;
  }, [layout, view, hover, pal, nodeById, neighbors, height]);

  // ── interaction ──────────────────────────────────────────────────────────
  const hitTest = useCallback((sx: number, sy: number): string | null => {
    if (!layout) return null;
    const wx = (sx - view.tx) / view.scale;
    const wy = (sy - view.ty) / view.scale;
    for (const [id, p] of layout.pos) {
      if (wx >= p.x - p.w / 2 && wx <= p.x + p.w / 2 && wy >= p.y - p.h / 2 && wy <= p.y + p.h / 2) return id;
    }
    return null;
  }, [layout, view]);

  const onPointerMove = (ev: React.PointerEvent) => {
    const rect = canvasRef.current!.getBoundingClientRect();
    const sx = ev.clientX - rect.left, sy = ev.clientY - rect.top;
    if (drag.current) {
      setView(v => ({ ...v, tx: drag.current!.tx + (ev.clientX - drag.current!.px), ty: drag.current!.ty + (ev.clientY - drag.current!.py) }));
      return;
    }
    const id = hitTest(sx, sy);
    if (id !== hover) setHover(id);
  };
  const onPointerDown = (ev: React.PointerEvent) => {
    drag.current = { px: ev.clientX, py: ev.clientY, tx: view.tx, ty: view.ty };
    (ev.target as HTMLElement).setPointerCapture?.(ev.pointerId);
  };
  const endDrag = () => { drag.current = null; };
  const onClick = (ev: React.MouseEvent) => {
    const rect = canvasRef.current!.getBoundingClientRect();
    const id = hitTest(ev.clientX - rect.left, ev.clientY - rect.top);
    if (!id) return;
    const n = nodeById.get(id);
    if (n && n.kind === 'service' && onSelectService) onSelectService(n.name);
  };
  // wheel zoom via a non-passive native listener (preventDefault — v0.8.2 lesson).
  useEffect(() => {
    const el = canvasRef.current;
    if (!el) return;
    const onWheel = (ev: WheelEvent) => {
      ev.preventDefault();
      const rect = el.getBoundingClientRect();
      const sx = ev.clientX - rect.left, sy = ev.clientY - rect.top;
      setView(v => {
        const factor = ev.deltaY < 0 ? 1.1 : 1 / 1.1;
        const ns = Math.max(0.15, Math.min(3, v.scale * factor));
        // keep the cursor point fixed
        const wx = (sx - v.tx) / v.scale, wy = (sy - v.ty) / v.scale;
        return { scale: ns, tx: sx - wx * ns, ty: sy - wy * ns };
      });
    };
    el.addEventListener('wheel', onWheel, { passive: false });
    return () => el.removeEventListener('wheel', onWheel);
  }, []);

  if (q.isLoading) return <div style={{ height, display: 'grid', placeItems: 'center' }}><Spinner /></div>;
  if (q.isError) return <Empty icon="⋔" title="Service graph unavailable">Couldn't load the topology edges.</Empty>;
  if (!q.data || q.data.nodes.length === 0) return <Empty icon="⋔" title="No service interactions in this window" />;

  return (
    <div ref={wrapRef} style={{ position: 'relative', height, border: '1px solid var(--border)', borderRadius: 8, overflow: 'hidden', background: 'var(--bg0)' }}>
      <canvas
        ref={canvasRef}
        style={{ display: 'block', cursor: hover ? 'pointer' : 'grab', touchAction: 'none' }}
        onPointerMove={onPointerMove}
        onPointerDown={onPointerDown}
        onPointerUp={endDrag}
        onPointerLeave={() => { endDrag(); setHover(null); }}
        onClick={onClick}
      />
      {/* health legend + fit */}
      <div style={{ position: 'absolute', left: 10, bottom: 10, display: 'flex', gap: 12, alignItems: 'center', background: 'var(--bg1)', border: '1px solid var(--border)', borderRadius: 6, padding: '5px 9px', fontSize: 11, color: 'var(--text2)' }}>
        <LegendDot color="var(--ok)" label="healthy" />
        <LegendDot color="var(--warn)" label="1–5% err" />
        <LegendDot color="var(--err)" label="≥5% err" />
        <span style={{ color: 'var(--text3)' }}>{q.data.nodes.length} nodes · {q.data.edges.length} edges</span>
      </div>
      {/* Node-size encoding toggles (v0.8.x — Uptrace adapt, slice 3). Two
          compact segmented controls re-roll the SAME payload client-side: no
          refetch. Reuses the shared .segmented primitive (v0.7.54 one-design-
          language rule) — no hand-rolled button styles. */}
      <div style={{ position: 'absolute', left: 10, top: 10, display: 'flex', gap: 8, alignItems: 'center' }}>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>Size by</span>
        <div className="segmented" style={{ fontSize: 11 }}>
          <button type="button" className={sizeMode === 'incoming' ? 'active' : ''}
            onClick={() => applySize('incoming', sizeMetric)}
            title="Size each node by the edges where it is the TARGET (who calls it)">
            Incoming
          </button>
          <button type="button" className={sizeMode === 'outgoing' ? 'active' : ''}
            onClick={() => applySize('outgoing', sizeMetric)}
            title="Size each node by the edges where it is the SOURCE (what it calls)">
            Outgoing
          </button>
        </div>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>Metric</span>
        <div className="segmented" style={{ fontSize: 11 }}>
          <button type="button" className={sizeMetric === 'rate' ? 'active' : ''}
            onClick={() => applySize(sizeMode, 'rate')}
            title="Sum of edge call rate (calls/min)">
            Rate
          </button>
          <button type="button" className={sizeMetric === 'duration' ? 'active' : ''}
            onClick={() => applySize(sizeMode, 'duration')}
            title="Call-weighted average edge latency (avg ms)">
            Duration
          </button>
        </div>
      </div>
      <button type="button" onClick={fit}
        style={{ position: 'absolute', right: 10, top: 10, fontSize: 12, padding: '4px 10px', background: 'var(--bg1)', border: '1px solid var(--border)', borderRadius: 6, color: 'var(--text)', cursor: 'pointer' }}>
        Fit
      </button>
      {hover && nodeById.get(hover) && <HoverCard node={nodeById.get(hover)!} />}
    </div>
  );
}

function LegendDot({ color, label }: { color: string; label: string }) {
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
      <span style={{ width: 9, height: 9, borderRadius: 2, background: color }} />{label}
    </span>
  );
}

function HoverCard({ node }: { node: GraphNode }) {
  return (
    <div style={{ position: 'absolute', right: 10, bottom: 10, background: 'var(--bg1)', border: '1px solid var(--border)', borderRadius: 6, padding: '7px 10px', fontSize: 11.5, color: 'var(--text2)', minWidth: 140 }}>
      <div style={{ color: 'var(--text)', fontWeight: 700 }}>{node.name}</div>
      <div style={{ color: 'var(--text3)', fontSize: 10, textTransform: 'uppercase', letterSpacing: '.04em' }}>{node.kind}{node.system ? ` · ${node.system}` : ''}</div>
      <div style={{ marginTop: 4, display: 'flex', gap: 10, fontVariantNumeric: 'tabular-nums' }}>
        <span>{node.calls.toLocaleString()} calls</span>
        <span style={{ color: node.errorRate >= 5 ? 'var(--err)' : node.errorRate >= 1 ? 'var(--warn)' : 'var(--ok)' }}>{node.errorRate.toFixed(1)}% err</span>
      </div>
    </div>
  );
}

// ── canvas helpers ───────────────────────────────────────────────────────────
function roundRect(ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, r: number) {
  const rr = Math.min(r, w / 2, h / 2);
  ctx.beginPath();
  ctx.moveTo(x + rr, y);
  ctx.arcTo(x + w, y, x + w, y + h, rr);
  ctx.arcTo(x + w, y + h, x, y + h, rr);
  ctx.arcTo(x, y + h, x, y, rr);
  ctx.arcTo(x, y, x + w, y, rr);
  ctx.closePath();
}
function clip(ctx: CanvasRenderingContext2D, s: string, maxW: number): string {
  if (ctx.measureText(s).width <= maxW) return s;
  let lo = 0, hi = s.length;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (ctx.measureText(s.slice(0, mid) + '…').width <= maxW) lo = mid; else hi = mid - 1;
  }
  return s.slice(0, lo) + '…';
}
