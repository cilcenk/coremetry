import { useEffect, useMemo, useRef, useState, useCallback } from 'react';
import { Link } from 'react-router-dom';
import dagre from 'dagre';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { timeRangeToNs, fmtNum } from '@/lib/utils';
import { Spinner, Empty } from '@/components/Spinner';
import { Button } from '@/components/ui/Button';
import { healthToken } from '@/lib/health';
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

// fmtMs — compact latency label (mirrors FocusedNeighborhood.fmtMs so the
// canvas edge label and the /topology inspector read identically).
function fmtMs(ms: number): string {
  if (!ms) return '—';
  if (ms >= 1000) return (ms / 1000).toFixed(ms >= 10_000 ? 0 : 1) + 's';
  return Math.round(ms) + 'ms';
}

// edgeLabel — the canvas chip text for one edge under the SELECTED size metric
// (slice 4). Rate → calls/min (fmtNum is the codebase's compact int format);
// Duration → p99Ms (Coremetry HAS per-edge p99, the operator-meaningful tail —
// prefer it over avg for the on-canvas label).
function edgeLabel(e: GraphEdge, metric: NodeSizeMetric): string {
  return metric === 'rate' ? `${fmtNum(Math.round(e.rate))}/min` : fmtMs(e.p99Ms);
}

// Selected — additive single-click focus (slice 5). A NODE drives the ego-graph
// dim (everything not incident to it fades) + a detail panel; an EDGE highlights
// the spline + a per-edge detail panel. null = nothing selected (default).
type Selected =
  | { type: 'node'; id: string }
  | { type: 'edge'; source: string; target: string }
  | null;

function sameEdge(s: Selected, e: GraphEdge): boolean {
  return s?.type === 'edge' && s.source === e.source && s.target === e.target;
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
  // Additive click-to-focus (slice 5). A selected NODE masks the canvas to its
  // ego-graph (incident edges + their endpoints) via the existing dim path; a
  // selected edge highlights that spline. Render-time only — NO refetch.
  const [selected, setSelected] = useState<Selected>(null);
  const drag = useRef<{ px: number; py: number; tx: number; ty: number } | null>(null);

  // Clear a stale selection if the payload changes underneath it (range/scope
  // flip). Guards against a panel pointing at a node/edge no longer in the graph.
  useEffect(() => { setSelected(null); }, [q.data]);

  // ego set: the selected node + its direct neighbors. Non-ego nodes/edges dim.
  const egoSet = useMemo(() => {
    if (selected?.type !== 'node') return null;
    const s = new Set<string>([selected.id]);
    for (const nb of neighbors.get(selected.id) ?? []) s.add(nb);
    return s;
  }, [selected, neighbors]);

  const selectedNode = selected?.type === 'node' ? nodeById.get(selected.id) ?? null : null;
  // The selected edge's payload (looked up from the live layout so its RED stats
  // come from the SAME data the canvas drew — no second fetch).
  const selectedEdge = useMemo(() => {
    if (selected?.type !== 'edge') return null;
    return q.data?.edges.find(e => e.source === selected.source && e.target === selected.target) ?? null;
  }, [selected, q.data]);

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
    // A node is LIT if: nothing focused, OR it's the hovered node / a hover
    // neighbor, OR — when a NODE is selected — it's in the ego set (slice 5
    // ego-graph mask reuses the SAME dim path as hover). The two combine: hover
    // can still spotlight within a selected ego-graph.
    const isLit = (id: string) =>
      (!hover || id === hover || (hoverSet?.has(id) ?? false)) &&
      (!egoSet || egoSet.has(id));
    // An edge is LIT if either endpoint is hovered, OR (node selected) both
    // endpoints sit in the ego set, OR it IS the selected edge.
    const edgeLit = (e: GraphEdge) =>
      (!hover || e.source === hover || e.target === hover) &&
      (!egoSet || (egoSet.has(e.source) && egoSet.has(e.target))) ||
      sameEdge(selected, e);

    // viewport bounds in world coords for culling. Pad by MAX_W so the widest
    // size-encoded cards aren't culled a frame early at the viewport edges.
    const minX = -view.tx / view.scale - MAX_W;
    const minY = -view.ty / view.scale - NODE_H;
    const maxX = (vw - view.tx) / view.scale + MAX_W;
    const maxY = (vh - view.ty) / view.scale + NODE_H;

    // Density gate (slice 4 — Uptrace's load-bearing rule). On a billing-grade
    // graph, always-on edge labels become spaghetti; gate by edge count:
    //   < 8  → label always   |   8–15 → label only on hover/highlight
    //   ≥ 16 → label only on hover/highlight.
    // (8–15 and ≥16 collapse to the same rule today — kept as separate bands so
    // the thresholds read as the documented gate and a future "dim label" tier
    // for the mid band is a one-line change.)
    const edgeCount = layout.edges.length;
    const labelAlways = edgeCount < 8;

    // edges first
    ctx.font = '600 10px -apple-system, "Segoe UI", sans-serif';
    ctx.textBaseline = 'middle';
    for (const { e, pts } of layout.edges) {
      if (pts.length < 2) continue;
      const lit = edgeLit(e);
      const col = healthColor(e.errorRate, pal);
      const isSel = sameEdge(selected, e);
      ctx.globalAlpha = lit ? 0.85 : 0.12;
      ctx.strokeStyle = isSel ? pal.accent : col;
      // KEEP edge thickness = log10(calls) (Coremetry's instinct > Uptrace's
      // flat width); the selected edge gets a +1px bump for affordance.
      ctx.lineWidth = Math.max(0.6, Math.min(6, Math.log10(e.calls + 1))) + (isSel ? 1 : 0);
      ctx.beginPath();
      ctx.moveTo(pts[0].x, pts[0].y);
      for (let i = 1; i < pts.length; i++) ctx.lineTo(pts[i].x, pts[i].y);
      ctx.stroke();
      // arrowhead at the target end (always kept — slice 4)
      const a = pts[pts.length - 2], b = pts[pts.length - 1];
      const ang = Math.atan2(b.y - a.y, b.x - a.x);
      ctx.beginPath();
      ctx.moveTo(b.x, b.y);
      ctx.lineTo(b.x - 7 * Math.cos(ang - 0.4), b.y - 7 * Math.sin(ang - 0.4));
      ctx.lineTo(b.x - 7 * Math.cos(ang + 0.4), b.y - 7 * Math.sin(ang + 0.4));
      ctx.closePath();
      ctx.fillStyle = isSel ? pal.accent : col;
      ctx.fill();

      // density-gated metric chip at the spline midpoint (slice 4). Always when
      // the graph is small; otherwise only when this edge is lit (hover/select).
      if (labelAlways || lit) {
        const mid = midpointOf(pts);
        const text = edgeLabel(e, sizeMetric);
        const padX = 4, padY = 2;
        const tw = ctx.measureText(text).width;
        const chipW = tw + padX * 2, chipH = 10 + padY * 2;
        ctx.globalAlpha = lit ? 1 : 0.55;
        ctx.fillStyle = pal.bg1;
        ctx.strokeStyle = pal.border;
        ctx.lineWidth = 1;
        roundRect(ctx, mid.x - chipW / 2, mid.y - chipH / 2, chipW, chipH, 4);
        ctx.fill();
        ctx.stroke();
        ctx.fillStyle = pal.text2;
        ctx.fillText(text, mid.x - tw / 2, mid.y);
      }
    }
    ctx.globalAlpha = 1;

    // nodes
    const selId = selected?.type === 'node' ? selected.id : null;
    ctx.font = '600 12px -apple-system, "Segoe UI", sans-serif';
    ctx.textBaseline = 'middle';
    for (const [id, p] of layout.pos) {
      if (p.x + p.w / 2 < minX || p.x - p.w / 2 > maxX || p.y + p.h / 2 < minY || p.y - p.h / 2 > maxY) continue; // cull
      const n = nodeById.get(id);
      if (!n) continue;
      const lit = isLit(id);
      const focused = id === hover || id === selId;
      const x = p.x - p.w / 2, y = p.y - p.h / 2;
      ctx.globalAlpha = lit ? 1 : 0.12;
      // card
      ctx.fillStyle = pal.bg1;
      ctx.strokeStyle = focused ? pal.accent : pal.border;
      ctx.lineWidth = focused ? 2 : 1;
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
  }, [layout, view, hover, pal, nodeById, neighbors, height, selected, egoSet, sizeMetric]);

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

  // hitTestEdge — nearest spline within a screen-space tolerance, for click-to-
  // select-an-edge (slice 5). Tolerance is divided by the live scale so the grab
  // zone stays ~6px on screen regardless of zoom.
  const hitTestEdge = useCallback((sx: number, sy: number): GraphEdge | null => {
    if (!layout) return null;
    const wx = (sx - view.tx) / view.scale;
    const wy = (sy - view.ty) / view.scale;
    const tol = 6 / view.scale;
    let best: GraphEdge | null = null, bestD = tol;
    for (const { e, pts } of layout.edges) {
      for (let i = 1; i < pts.length; i++) {
        const d = distToSeg(wx, wy, pts[i - 1], pts[i]);
        if (d < bestD) { bestD = d; best = e; }
      }
    }
    return best;
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
  // Single click is ADDITIVE (slice 5): select a node (→ ego-graph + detail
  // panel) or an edge (→ edge detail panel) instead of navigating. Navigation
  // stays reachable via the panel's "Open service →" button and double-click.
  // A click on empty canvas clears the selection.
  const onClick = (ev: React.MouseEvent) => {
    if (drag.current && (Math.abs(ev.clientX - drag.current.px) > 3 || Math.abs(ev.clientY - drag.current.py) > 3)) return; // a pan, not a click
    const rect = canvasRef.current!.getBoundingClientRect();
    const sx = ev.clientX - rect.left, sy = ev.clientY - rect.top;
    const id = hitTest(sx, sy);
    if (id) { setSelected({ type: 'node', id }); return; }
    const e = hitTestEdge(sx, sy);
    if (e) { setSelected({ type: 'edge', source: e.source, target: e.target }); return; }
    setSelected(null);
  };
  // Double-click a service node = navigate (no muscle-memory regression).
  const onDoubleClick = (ev: React.MouseEvent) => {
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
        onDoubleClick={onDoubleClick}
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
      <Button variant="secondary" size="sm" onClick={fit}
        style={{ position: 'absolute', right: 10, top: 10 }}>
        Fit
      </Button>
      {/* HoverCard suppressed while the detail panel is open — the panel owns
          the right rail and a hover card behind it just flickers. */}
      {!selected && hover && nodeById.get(hover) && <HoverCard node={nodeById.get(hover)!} />}

      {/* ── detail panel (slice 5) — additive click-to-focus inspector. Ports
          the FocusedNeighborhood inspector markup (health dot + RED Stat rows
          + Recenter) into a right-rail panel. No refetch for the node/edge
          stats (read from the live payload); only the edge-instances drill
          fetches, lazily. ───────────────────────────────────────────────── */}
      {selectedNode && (
        <NodeDetail
          node={selectedNode}
          onClose={() => setSelected(null)}
          onRecenter={() => { fit(); }}
          onOpen={() => { if (selectedNode.kind === 'service' && onSelectService) onSelectService(selectedNode.name); }}
        />
      )}
      {selected?.type === 'edge' && (
        <EdgeDetail
          edge={selectedEdge}
          source={selected.source}
          target={selected.target}
          sourceNode={nodeById.get(selected.source) ?? null}
          targetNode={nodeById.get(selected.target) ?? null}
          from={from}
          to={to}
          onClose={() => setSelected(null)}
        />
      )}
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

// ── detail panel (slice 5) ───────────────────────────────────────────────────
// Shared right-rail shell. Ports the FocusedNeighborhood inspector look (bg2
// card, health-dot header, mono kind line, RED Stat grid) but as a sticky panel
// instead of a hover popover. Uses the shared <Button> atom — NO hand-rolled
// button CSS (v0.7.54 one-design-language rule).
function PanelShell({ title, dot, sub, onClose, children }: {
  title: React.ReactNode; dot?: string; sub?: React.ReactNode;
  onClose: () => void; children: React.ReactNode;
}) {
  return (
    <div style={{ position: 'absolute', right: 10, top: 10, bottom: 10, width: 264, zIndex: 6, display: 'flex', flexDirection: 'column', gap: 10, padding: 12, borderRadius: 8, background: 'var(--bg2)', border: '1px solid var(--border)', boxShadow: '0 6px 20px rgba(0,0,0,.28)', fontSize: 12, overflowY: 'auto' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 7 }}>
        {dot && <span style={{ width: 9, height: 9, borderRadius: '50%', background: dot, flex: '0 0 auto' }} />}
        <span style={{ fontWeight: 700, color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{title}</span>
        <Button variant="ghost" size="sm" onClick={onClose} title="Close (clears the ego-graph focus)" style={{ marginLeft: 'auto', lineHeight: 1 }}>✕</Button>
      </div>
      {sub && (
        <div style={{ fontSize: 10, color: 'var(--text3)', fontFamily: 'ui-monospace, monospace', marginTop: -4 }}>{sub}</div>
      )}
      {children}
    </div>
  );
}

// NodeDetail — the selected-node inspector. Health/kind/RED + deep links
// (Open service → existing nav; Explore traces → /traces?service=…). No
// monitors link: /alerts has no service-scoped filter today, so shipping
// /alerts?service= would be a dead no-op query. Stats read from the live
// payload — no refetch.
function NodeDetail({ node, onClose, onRecenter, onOpen }: {
  node: GraphNode;
  onClose: () => void; onRecenter: () => void; onOpen: () => void;
}) {
  const isService = node.kind === 'service';
  return (
    <PanelShell
      title={node.name}
      dot={healthToken(node.errorRate)}
      sub={`${node.kind}${node.system ? ` · ${node.system}` : ''}${node.kind === 'database' && node.dbName ? ` · db.name=${node.dbName}` : ''}`}
      onClose={onClose}
    >
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3,1fr)', gap: 6 }}>
        <Stat l="CALLS" v={fmtNum(node.calls)} />
        <Stat l="RATE" v={`${fmtNum(Math.round(node.rate))}/min`} />
        <Stat l="ERR" v={`${node.errorRate.toFixed(1)}%`} tone={healthToken(node.errorRate)} />
      </div>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
        <Button variant="secondary" size="sm" onClick={onRecenter} title="Re-frame the graph (Fit)">Recenter</Button>
        {isService && <Button variant="primary" size="sm" onClick={onOpen}>Open service →</Button>}
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        {isService && (
          <Link to={`/traces?service=${encodeURIComponent(node.name)}`} style={linkStyle}>Explore traces →</Link>
        )}
      </div>
    </PanelShell>
  );
}

// EdgeDetail — the selected-edge inspector. "client → server" title, the RED
// rows (calls / errRate / avgMs / p99Ms), a pair-filtered trace link, and the
// "Edge instances" drill that REUSES the existing GET /api/topology/edge/
// instances endpoint (infra edges only — service→db/queue, the only shape that
// endpoint groups by peer_service). The drill fetches lazily on demand.
function EdgeDetail({ edge, source, target, sourceNode, targetNode, from, to, onClose }: {
  edge: GraphEdge | null; source: string; target: string;
  sourceNode: GraphNode | null; targetNode: GraphNode | null;
  from: number; to: number; onClose: () => void;
}) {
  const [showInstances, setShowInstances] = useState(false);
  const srcName = sourceNode?.name ?? source;
  const tgtName = targetNode?.name ?? target;
  // The edge-instances endpoint groups a parent SERVICE's spans by peer_service
  // filtered to a db/queue system — so it only applies when the target is a
  // db/queue node and the source is a service. Otherwise the affordance hides.
  const infraKind: 'db' | 'queue' | null =
    targetNode?.kind === 'database' ? 'db' : targetNode?.kind === 'queue' ? 'queue' : null;
  const canDrill = infraKind !== null && targetNode?.system != null && sourceNode?.kind === 'service';

  const inst = useQuery({
    queryKey: ['edge-instances', source, targetNode?.system ?? '', infraKind ?? '', from, to],
    queryFn: () => api.topologyEdgeInstances({
      parent: source, system: targetNode!.system!, kind: infraKind!, from, to,
    }),
    enabled: showInstances && canDrill,
    staleTime: 30_000,
  });

  const errRate = edge?.errorRate ?? 0;
  return (
    <PanelShell
      title={<span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 11 }}>{srcName} <span style={{ color: 'var(--text3)' }}>→</span> {tgtName}</span>}
      dot={healthToken(errRate)}
      sub="CLIENT → SERVER edge"
      onClose={onClose}
    >
      {!edge ? (
        <Empty icon="⋔" title="Edge no longer in window" />
      ) : (
        <>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2,1fr)', gap: 6 }}>
            <Stat l="CALLS" v={fmtNum(edge.calls)} />
            <Stat l="ERR" v={`${errRate.toFixed(1)}%`} tone={healthToken(errRate)} />
            <Stat l="AVG" v={fmtMs(edge.avgMs)} />
            <Stat l="P99" v={fmtMs(edge.p99Ms)} />
          </div>
          <Link to={`/traces?services=${encodeURIComponent(source)},${encodeURIComponent(target)}`} style={linkStyle}>View traces (this pair) →</Link>
          {canDrill && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
              {!showInstances ? (
                <Button variant="secondary" size="sm" onClick={() => setShowInstances(true)}>
                  Edge instances ▾
                </Button>
              ) : (
                <>
                  <div style={{ fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '.04em' }}>
                    {targetNode?.system} instances
                  </div>
                  {inst.isLoading && <Spinner />}
                  {!inst.isLoading && (inst.data?.instances.length ?? 0) === 0 && (
                    <Empty icon="⋔" title="No instances in this window" />
                  )}
                  {(inst.data?.instances ?? []).map(i => (
                    <div key={i.instance} style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '4px 6px', borderRadius: 5, background: 'var(--bg1)', border: '1px solid var(--border)' }}>
                      <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', fontFamily: 'ui-monospace, monospace', fontSize: 10.5 }} title={i.instance}>{i.instance}</span>
                      <span style={{ fontVariantNumeric: 'tabular-nums', color: 'var(--text2)' }}>{fmtNum(i.calls)}</span>
                      <span style={{ fontVariantNumeric: 'tabular-nums', color: 'var(--text3)' }}>{fmtMs(i.p99Ms)}</span>
                    </div>
                  ))}
                </>
              )}
            </div>
          )}
        </>
      )}
    </PanelShell>
  );
}

const linkStyle: React.CSSProperties = { fontSize: 11, color: 'var(--accent)', textDecoration: 'none' };

function Stat({ l, v, tone }: { l: string; v: string; tone?: string }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column' }}>
      <span style={{ fontSize: 12, fontWeight: 700, color: tone ?? 'var(--text)', fontFamily: 'ui-monospace, monospace' }}>{v}</span>
      <span style={{ fontSize: 8.5, color: 'var(--text3)', letterSpacing: '0.4px' }}>{l}</span>
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

// midpointOf — the geometric midpoint of an edge polyline (the dagre spline
// points), by arc length, so the metric chip (slice 4) sits at the visual
// centre of the curve rather than the centre of its bounding box.
function midpointOf(pts: Array<{ x: number; y: number }>): { x: number; y: number } {
  let total = 0;
  for (let i = 1; i < pts.length; i++) total += Math.hypot(pts[i].x - pts[i - 1].x, pts[i].y - pts[i - 1].y);
  let half = total / 2;
  for (let i = 1; i < pts.length; i++) {
    const seg = Math.hypot(pts[i].x - pts[i - 1].x, pts[i].y - pts[i - 1].y);
    if (half <= seg || i === pts.length - 1) {
      const t = seg === 0 ? 0 : half / seg;
      return { x: pts[i - 1].x + (pts[i].x - pts[i - 1].x) * t, y: pts[i - 1].y + (pts[i].y - pts[i - 1].y) * t };
    }
    half -= seg;
  }
  return pts[Math.floor(pts.length / 2)];
}

// distToSeg — point→segment distance (world coords) for edge hit-testing.
function distToSeg(px: number, py: number, a: { x: number; y: number }, b: { x: number; y: number }): number {
  const dx = b.x - a.x, dy = b.y - a.y;
  const len2 = dx * dx + dy * dy;
  if (len2 === 0) return Math.hypot(px - a.x, py - a.y);
  let t = ((px - a.x) * dx + (py - a.y) * dy) / len2;
  t = Math.max(0, Math.min(1, t));
  return Math.hypot(px - (a.x + t * dx), py - (a.y + t * dy));
}
