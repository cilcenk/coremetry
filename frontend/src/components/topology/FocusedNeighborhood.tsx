import { useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { timeRangeToNs, fmtNum } from '@/lib/utils';
import { svcColor } from '@/components/traces/shared';
import { healthToken } from '@/lib/health';
import { Spinner } from '@/components/Spinner';
import type { TimeRange, ServiceGraphResponse, GraphNode, GraphEdge } from '@/lib/types';

// FocusedNeighborhood — the focused-graph half of /topology (v0.8.x). Given a
// chosen service, BFS out from it in BOTH directions (callers ← focus →
// dependencies) to `hops` levels over the GLOBAL service graph (fetched once,
// shared cache with ServicePicker), cap the node count, and lay it out
// layered left→right with DOM node-cards + an SVG bezier edge overlay. Pan /
// zoom / fit, hover-highlight + inspector, click-to-recenter. Token-only.

const NODE_W = 168;
const NODE_H = 46;
const COL_GAP = 96;   // horizontal gap between hop columns
const ROW_GAP = 20;   // vertical gap between cards in a column
const COL_W = NODE_W + COL_GAP;
const ROW_H = NODE_H + ROW_GAP;
const CAP = 40;

// kindStripe — left accent stripe per node kind. Services reuse the shared
// per-service hue; infra kinds map to existing tokens (no new hex).
function kindStripe(n: GraphNode): string {
  switch (n.kind) {
    case 'database': return 'var(--info)';
    case 'queue':    return 'var(--accent2)';
    case 'external': return 'var(--text3)';
    case 'internal': return 'var(--text3)';
    default:         return svcColor(n.name);
  }
}
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
// edge colour by error rate — faint healthy, amber >1%, red >5%.
function edgeToken(errRate: number): string {
  return errRate > 5 ? 'var(--err)' : errRate > 1 ? 'var(--warn)' : 'var(--text3)';
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

interface Placed { node: GraphNode; col: number; x: number; y: number; }

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
  const graph = useQuery<ServiceGraphResponse>({
    queryKey: ['servicegraph', 'global', '', from, to],
    queryFn: () => api.serviceGraph({ scope: 'global', from, to }),
    staleTime: 30_000,
  });

  // ── BFS the focused neighborhood from the global graph ──────────────────
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

  // ── Layered layout: group by signed column, centre each column vertically ─
  const { placed, posOf, width, height } = useMemo(() => {
    const cols = new Map<number, GraphNode[]>();
    for (const n of nb.nodes) {
      const c = nb.col.get(n.id);
      if (c === undefined) {
        // Invariant: every kept node was assigned a column by the directional
        // BFS (nb.nodes derive from col.keys()). A miss means an id key-space
        // regression — focus / edge.source / edge.target / node.id must all
        // share ONE key space, else this silently piled nodes at column 0.
        if (import.meta.env.DEV) console.error(`[topology] node "${n.id}" has no column — id key-space mismatch`);
        continue;
      }
      (cols.get(c) ?? cols.set(c, []).get(c)!).push(n);
    }
    const colKeys = [...cols.keys()].sort((a, b) => a - b);
    const maxRows = Math.max(1, ...[...cols.values()].map(v => v.length));
    const totalH = maxRows * ROW_H;
    const placedArr: Placed[] = [];
    colKeys.forEach((c, ci) => {
      const list = cols.get(c)!.sort((a, b) => b.calls - a.calls);
      const colH = list.length * ROW_H;
      const y0 = (totalH - colH) / 2;
      list.forEach((node, ri) => {
        placedArr.push({ node, col: c, x: ci * COL_W, y: y0 + ri * ROW_H });
      });
    });
    const pos = new Map<string, Placed>(placedArr.map(p => [p.node.id, p]));
    return { placed: placedArr, posOf: (id: string) => pos.get(id), width: colKeys.length * COL_W, height: totalH };
  }, [nb]);

  // ── Pan / zoom ──────────────────────────────────────────────────────────
  const wrapRef = useRef<HTMLDivElement | null>(null);
  const [view, setView] = useState({ x: 40, y: 40, k: 1 });
  const [hover, setHover] = useState<string | null>(null);
  const drag = useRef<{ x: number; y: number; vx: number; vy: number } | null>(null);

  const fit = () => {
    const el = wrapRef.current; if (!el || width === 0) return;
    const pad = 60;
    const k = Math.min(1.2, Math.max(0.3, Math.min((el.clientWidth - pad) / (width + NODE_W), (el.clientHeight - pad) / (height + NODE_H))));
    setView({ k, x: (el.clientWidth - (width + NODE_W) * k) / 2, y: (el.clientHeight - (height + NODE_H) * k) / 2 });
  };
  // fit whenever the graph shape changes.
  useEffect(() => { fit(); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [width, height]);

  // non-passive wheel-zoom to cursor (v0.8.2 pattern).
  useEffect(() => {
    const el = wrapRef.current; if (!el) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      const r = el.getBoundingClientRect();
      const mx = e.clientX - r.left, my = e.clientY - r.top;
      setView(v => {
        const k = Math.min(2.5, Math.max(0.2, v.k * (e.deltaY < 0 ? 1.1 : 1 / 1.1)));
        return { k, x: mx - (mx - v.x) * (k / v.k), y: my - (my - v.y) * (k / v.k) };
      });
    };
    el.addEventListener('wheel', onWheel, { passive: false });
    return () => el.removeEventListener('wheel', onWheel);
  }, []);

  const onDown = (e: React.MouseEvent) => { drag.current = { x: e.clientX, y: e.clientY, vx: view.x, vy: view.y }; };
  const onMove = (e: React.MouseEvent) => {
    if (!drag.current) return;
    setView(v => ({ ...v, x: drag.current!.vx + (e.clientX - drag.current!.x), y: drag.current!.vy + (e.clientY - drag.current!.y) }));
  };
  const onUp = () => { drag.current = null; };
  const zoomBy = (f: number) => setView(v => {
    const el = wrapRef.current; const cx = (el?.clientWidth ?? 0) / 2, cy = (el?.clientHeight ?? 0) / 2;
    const k = Math.min(2.5, Math.max(0.2, v.k * f));
    return { k, x: cx - (cx - v.x) * (k / v.k), y: cy - (cy - v.y) * (k / v.k) };
  });

  // ── Hover neighborhood (highlight) ──────────────────────────────────────
  const hl = useMemo(() => {
    if (!hover) return null;
    const nodes = new Set<string>([hover]);
    const edges = new Set<GraphEdge>();
    for (const e of nb.edges) if (e.source === hover || e.target === hover) { edges.add(e); nodes.add(e.source); nodes.add(e.target); }
    return { nodes, edges };
  }, [hover, nb.edges]);

  if (graph.isLoading) return <div style={{ padding: 60, display: 'grid', placeItems: 'center' }}><Spinner /></div>;

  const hoverNode = hover ? nb.nodes.find(n => n.id === hover) : null;

  return (
    <div style={{ position: 'relative', height: Math.round(window.innerHeight * 0.74), border: '1px solid var(--border)', borderRadius: 8, overflow: 'hidden', background: 'var(--bg1)' }}>
      {/* ── toolbar ─────────────────────────────────────────────────────── */}
      <div style={{ position: 'absolute', top: 10, left: 10, right: 10, zIndex: 4, display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', pointerEvents: 'none' }}>
        <span style={{ pointerEvents: 'auto', display: 'inline-flex', alignItems: 'center', gap: 6, padding: '4px 8px', borderRadius: 6, background: 'var(--bg2)', border: '1px solid var(--border)', fontSize: 12, fontWeight: 600 }}>
          <span style={{ width: 8, height: 8, borderRadius: '50%', background: healthToken(focusErr(nb.nodes, focus)) }} />
          {focus}
          <button onClick={onClear} title="Back to the service picker"
            style={{ marginLeft: 2, border: 'none', background: 'transparent', color: 'var(--text3)', cursor: 'pointer', fontSize: 13, lineHeight: 1 }}>✕</button>
        </span>
        <div className="seg" style={{ pointerEvents: 'auto' }}>
          <button className={hops === 1 ? 'on' : ''} onClick={() => onHops(1)}>1 hop</button>
          <button className={hops === 2 ? 'on' : ''} onClick={() => onHops(2)}>2 hops</button>
        </div>
        <label style={{ pointerEvents: 'auto', display: 'inline-flex', alignItems: 'center', gap: 5, fontSize: 12, color: 'var(--text2)', cursor: 'pointer' }}>
          <input type="checkbox" checked={errorsOnly} onChange={e => onErrorsOnly(e.target.checked)} /> Errors only
        </label>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          {nb.nodes.length} of {graph.data?.nodes.length ?? 0} nodes · {nb.edges.length} edges
          {nb.collapsed > 0 && <strong style={{ color: 'var(--warn)' }}> · +{nb.collapsed} more collapsed</strong>}
        </span>
        <span style={{ marginLeft: 'auto', pointerEvents: 'auto', display: 'inline-flex', alignItems: 'center', gap: 10, fontSize: 11, color: 'var(--text3)' }}>
          <span style={{ display: 'inline-flex', gap: 6 }}>
            <Kind c={svcColor(focus)} l="service" /><Kind c="var(--info)" l="db" /><Kind c="var(--accent2)" l="queue" />
          </span>
          <span className="seg">
            <button onClick={() => zoomBy(1 / 1.2)}>−</button>
            <button onClick={() => zoomBy(1.2)}>+</button>
            <button onClick={fit}>Fit</button>
          </span>
        </span>
      </div>

      {/* ── canvas ──────────────────────────────────────────────────────── */}
      <div ref={wrapRef}
        onMouseDown={onDown} onMouseMove={onMove} onMouseUp={onUp} onMouseLeave={onUp}
        style={{ position: 'absolute', inset: 0, cursor: drag.current ? 'grabbing' : 'grab' }}>
        <div style={{ position: 'absolute', left: 0, top: 0, transformOrigin: '0 0', transform: `translate(${view.x}px, ${view.y}px) scale(${view.k})` }}>
          {/* edges */}
          <svg width={width + NODE_W} height={height + NODE_H} style={{ position: 'absolute', left: 0, top: 0, overflow: 'visible' }}>
            <defs>
              {/* Open-chevron arrowhead, constant ~12px via
                  markerUnits=userSpaceOnUse — busy edges no longer grow giant
                  heads. context-stroke inherits the per-edge colour. */}
              <marker id="fn-arrow" viewBox="0 0 12 12" refX="8.5" refY="6" markerUnits="userSpaceOnUse" markerWidth="12" markerHeight="12" orient="auto">
                <path d="M2.5,2.5 L8.5,6 L2.5,9.5" fill="none" stroke="context-stroke" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
              </marker>
              {/* highlighted edges switch their stroke to --accent — the
                  chevron matches with an accent stroke. */}
              <marker id="fn-arrow-hl" viewBox="0 0 12 12" refX="8.5" refY="6" markerUnits="userSpaceOnUse" markerWidth="13" markerHeight="13" orient="auto">
                <path d="M2.5,2.5 L8.5,6 L2.5,9.5" fill="none" stroke="var(--accent)" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" />
              </marker>
            </defs>
            {nb.edges.map((e, i) => {
              const a = posOf(e.source), b = posOf(e.target);
              if (!a || !b) return null;
              // draw downstream: from the right edge of the upstream card to the
              // left edge of the downstream card (lower col = upstream).
              const [src, dst] = (a.col <= b.col) ? [a, b] : [b, a];
              const x1 = src.x + NODE_W, y1 = src.y + NODE_H / 2;
              const x2 = dst.x, y2 = dst.y + NODE_H / 2;
              const dx = Math.max(28, Math.abs(x2 - x1) / 2);
              const d = `M${x1},${y1} C${x1 + dx},${y1} ${x2 - dx},${y2} ${x2},${y2}`;
              const isHl = hl?.edges.has(e);
              const dim = hl && !isHl;
              const stroke = isHl ? 'var(--accent)' : edgeToken(e.errorRate);
              // Cap heavy edges so they stay readable (~3.4px before the
              // hover bump), independent of the now-constant arrowhead.
              const w = Math.min(3.4, Math.max(1, Math.log10(e.calls + 1) * 0.95)) * (isHl ? 1.5 : 1);
              return (
                <g key={i} opacity={dim ? 0.16 : 1}>
                  {/* soft accent glow underlay beneath the highlighted edge */}
                  {isHl && (
                    <path d={d} fill="none" stroke="var(--accent)" strokeWidth={w + 6}
                      strokeLinecap="round" opacity={0.13} />
                  )}
                  {/* main path — round caps; highlighted edge runs an animated
                      dashed traffic flow (.tf-edge-hl, reduced-motion safe) */}
                  <path className={isHl ? 'tf-edge-hl' : undefined}
                    d={d} fill="none" stroke={stroke} strokeWidth={w} strokeLinecap="round"
                    markerEnd={`url(#${isHl ? 'fn-arrow-hl' : 'fn-arrow'})`} />
                  {isHl && (
                    <text x={(x1 + x2) / 2} y={(y1 + y2) / 2 - 4} textAnchor="middle"
                      style={{ fontSize: 9, fill: 'var(--text2)', fontFamily: 'ui-monospace, monospace' }}>
                      {fmtNum(e.calls)} · p99 {fmtMs(e.p99Ms)}{e.errorRate > 0 ? ` · ${e.errorRate.toFixed(1)}% err` : ''}
                    </text>
                  )}
                </g>
              );
            })}
          </svg>
          {/* node cards */}
          {placed.map(p => {
            const n = p.node;
            const dim = hl && !hl.nodes.has(n.id);
            const isFocus = n.id === focus;
            return (
              <div key={n.id}
                onMouseEnter={() => setHover(n.id)} onMouseLeave={() => setHover(h => h === n.id ? null : h)}
                onClick={ev => { ev.stopPropagation(); if (!isFocus) onRecenter(n.name); }}
                style={{
                  position: 'absolute', left: p.x, top: p.y, width: NODE_W, height: NODE_H,
                  display: 'flex', alignItems: 'center', gap: 8, padding: '0 8px',
                  background: 'var(--bg2)', borderRadius: 6, cursor: isFocus ? 'default' : 'pointer',
                  border: `1px solid ${isFocus ? 'var(--accent)' : 'var(--border)'}`,
                  boxShadow: isFocus ? '0 0 0 1px var(--accent)' : 'none',
                  opacity: dim ? 0.28 : 1, overflow: 'hidden',
                }}>
                <span style={{ width: 3, alignSelf: 'stretch', margin: '6px 0', borderRadius: 2, background: kindStripe(n), flexShrink: 0 }} />
                <span style={{ width: 8, height: 8, borderRadius: '50%', background: healthToken(n.errorRate), flexShrink: 0 }} />
                <span style={{ minWidth: 0, display: 'flex', flexDirection: 'column', gap: 1 }}>
                  <span style={{ fontSize: 12, fontWeight: 700, color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={n.name}>{n.name}</span>
                  <span style={{ fontSize: 9.5, color: 'var(--text3)', fontFamily: 'ui-monospace, monospace', whiteSpace: 'nowrap' }}>
                    {n.kind === 'database' && n.dbName
                      ? `db.name ${n.dbName} · p99 ${fmtMs(nb.p99Of(n.id))}`
                      : `${fmtNum(n.calls)} · p99 ${fmtMs(nb.p99Of(n.id))} · ${n.errorRate.toFixed(1)}%`}
                  </span>
                </span>
              </div>
            );
          })}
        </div>
      </div>

      {/* ── hover inspector ─────────────────────────────────────────────── */}
      {hoverNode && (
        <div style={{ position: 'absolute', left: 10, bottom: 44, zIndex: 5, width: 240, padding: 12, borderRadius: 8, background: 'var(--bg2)', border: '1px solid var(--border)', boxShadow: '0 6px 20px rgba(0,0,0,.28)', fontSize: 12 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 7, marginBottom: 6 }}>
            <span style={{ width: 9, height: 9, borderRadius: '50%', background: healthToken(hoverNode.errorRate) }} />
            <span style={{ fontWeight: 700, color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{hoverNode.name}</span>
            <button onClick={() => onRecenter(hoverNode.name)} className="sec" style={{ marginLeft: 'auto', fontSize: 10, padding: '2px 7px' }}>Recenter</button>
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
        <Kind c="var(--ok)" l="healthy" dot /><Kind c="var(--warn)" l=">1% err" dot /><Kind c="var(--err)" l=">5% err" dot />
      </div>

      {/* ── footer caption ──────────────────────────────────────────────── */}
      <div style={{ position: 'absolute', right: 10, bottom: 10, zIndex: 3, maxWidth: '62%', fontSize: 9.5, color: 'var(--text3)', textAlign: 'right', lineHeight: 1.4 }}>
        Built from OTel span semantics — nodes by service.name, type from db.system/messaging.system, edges from CLIENT→SERVER spans.
        Click a node to recenter · hover for edges · scroll/drag to navigate.
      </div>
    </div>
  );
}

function focusErr(nodes: GraphNode[], focus: string): number {
  return nodes.find(n => n.id === focus)?.errorRate ?? 0;
}
function Kind({ c, l, dot }: { c: string; l: string; dot?: boolean }) {
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
      <span style={{ width: dot ? 8 : 3, height: dot ? 8 : 12, borderRadius: dot ? '50%' : 2, background: c }} />{l}
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
