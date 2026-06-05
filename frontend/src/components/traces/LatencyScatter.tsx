// LatencyScatter.tsx — duration-vs-time scatter (Tempo/Honeycomb-grade).
//
// A <canvas> scatter built from the LIVE trace rows (no fabricated data):
//   x = start time, y = duration on a LOG scale, colour = status
//   (ok = accent, error = err). Hover → tooltip, click → open the trace,
//   drag → brush a time window that narrows the table.
//
// Canvas (not SVG) because a billion-span install can surface thousands of
// points per page; the DOM-node-per-point SVG approach janks past ~2k. We also
// downsample through the Phase-0 lttb transform so the plotted set is bounded
// regardless of how many rows the fetch returns. Hit-testing is done against
// the (already small) plotted point list, so hover/click stay O(points).

import { useEffect, useMemo, useRef, useState } from 'react';
import { lttb, type Point } from '@/lib/perf/lttb';
import { tsShort } from '@/lib/utils';
import type { TraceRow } from '@/lib/types';
import { fmtDur } from './shared';

const PAD = { l: 12, r: 12, t: 12, b: 16 };
const MAX_POINTS = 2000;

interface PlotPoint {
  px: number;       // device-independent canvas x
  py: number;       // device-independent canvas y
  row: TraceRow;
}

export function LatencyScatter({
  rows, height = 168, onOpen, onBrush,
}: {
  rows: TraceRow[];
  height?: number;
  onOpen: (t: TraceRow) => void;
  onBrush: (fromMs: number, toMs: number) => void;
}) {
  const wrapRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const [width, setWidth] = useState(900);
  const [hover, setHover] = useState<{ p: PlotPoint } | null>(null);
  // Brush drag state in canvas-x px. dragStart=null means no active drag.
  const dragStart = useRef<number | null>(null);
  const [brush, setBrush] = useState<{ a: number; b: number } | null>(null);
  // Latest plotted points, kept in a ref so the imperative pointer handlers
  // can hit-test without forcing the effect to re-bind on every hover.
  const pointsRef = useRef<PlotPoint[]>([]);

  // Track container width so the canvas is crisp + responsive.
  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth || 900));
    ro.observe(el);
    setWidth(el.clientWidth || 900);
    return () => ro.disconnect();
  }, []);

  // Domain (time + duration) + downsampled, plottable point set. Errors are
  // never dropped by the downsampler — they're the operator's signal — so we
  // LTTB the OK points by visual shape and keep every error point.
  const model = useMemo(() => {
    if (rows.length === 0) return null;
    let t0 = Infinity, t1 = -Infinity, maxDur = 0;
    for (const r of rows) {
      const tms = r.startTime / 1e6;
      if (tms < t0) t0 = tms;
      if (tms > t1) t1 = tms;
      if (r.durationMs > maxDur) maxDur = r.durationMs;
    }
    if (t1 === t0) t1 = t0 + 1;
    maxDur = Math.max(maxDur, 1);

    const errors = rows.filter(r => r.hasError);
    const oks = rows.filter(r => !r.hasError);
    // Downsample the OK cloud by (time, duration) shape so the scatter stays
    // bounded; errors are always kept.
    const okBudget = Math.max(3, MAX_POINTS - errors.length);
    let okKept: TraceRow[] = oks;
    if (oks.length > okBudget) {
      const sorted = [...oks].sort((a, b) => a.startTime - b.startTime);
      const pts: Point[] = sorted.map(r => ({ x: r.startTime / 1e6, y: r.durationMs }));
      const reduced = lttb(pts, okBudget);
      // Map reduced (x,y) anchors back to rows by nearest start time. The
      // anchors come straight from `pts`, so identity-match on x is exact.
      const byX = new Map<number, TraceRow>();
      sorted.forEach((r, i) => byX.set(pts[i].x, r));
      okKept = reduced.map(p => byX.get(p.x)).filter((r): r is TraceRow => !!r);
    }
    return { t0, t1, maxDur, plotted: [...okKept, ...errors] };
  }, [rows]);

  // Draw the scatter on canvas. Re-runs on data / size change.
  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    canvas.width = Math.round(width * dpr);
    canvas.height = Math.round(height * dpr);
    const ctx = canvas.getContext('2d');
    if (!ctx) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, width, height);

    pointsRef.current = [];
    if (!model) return;

    const { t0, t1, maxDur } = model;
    const plotW = width - PAD.l - PAD.r;
    const plotH = height - PAD.t - PAD.b;
    const sx = (tms: number) => PAD.l + ((tms - t0) / (t1 - t0)) * plotW;
    const logMax = Math.log10(maxDur + 1) || 1;
    const sy = (d: number) => PAD.t + plotH - (Math.log10(d + 1) / logMax) * plotH;

    // Read tokenised colours from CSS variables (theme-aware).
    const cs = getComputedStyle(canvas);
    const cBorder = cs.getPropertyValue('--border').trim() || '#3338';
    const cAccent = cs.getPropertyValue('--accent').trim() || '#3b82f6';
    const cErr = cs.getPropertyValue('--err').trim() || '#dc2626';

    // Log gridlines at 25/50/75/100% of max duration.
    ctx.strokeStyle = cBorder;
    ctx.lineWidth = 1;
    ctx.setLineDash([3, 4]);
    for (const f of [0.25, 0.5, 0.75, 1]) {
      const y = sy(maxDur * f);
      ctx.beginPath();
      ctx.moveTo(PAD.l, y);
      ctx.lineTo(width - PAD.r, y);
      ctx.stroke();
    }
    ctx.setLineDash([]);

    // Plot points (OK first, errors on top so they're never occluded).
    const pts: PlotPoint[] = [];
    for (const row of model.plotted) {
      const px = sx(row.startTime / 1e6);
      const py = sy(row.durationMs);
      pts.push({ px, py, row });
      ctx.beginPath();
      ctx.arc(px, py, row.hasError ? 3.4 : 2.6, 0, Math.PI * 2);
      ctx.fillStyle = row.hasError ? cErr : cAccent;
      ctx.globalAlpha = row.hasError ? 0.95 : 0.5;
      ctx.fill();
    }
    ctx.globalAlpha = 1;
    pointsRef.current = pts;
  }, [model, width, height]);

  // Pointer helpers convert clientX/Y → canvas-local coords.
  const localCoords = (e: React.PointerEvent | React.MouseEvent) => {
    const r = canvasRef.current?.getBoundingClientRect();
    if (!r) return { x: 0, y: 0 };
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  };

  const hitTest = (x: number, y: number): PlotPoint | null => {
    let best: PlotPoint | null = null;
    let bestD = 36; // squared px radius
    for (const p of pointsRef.current) {
      const dx = p.px - x, dy = p.py - y;
      const d = dx * dx + dy * dy;
      if (d < bestD) { bestD = d; best = p; }
    }
    return best;
  };

  const onPointerDown = (e: React.PointerEvent) => {
    const { x } = localCoords(e);
    dragStart.current = x;
    setBrush({ a: x, b: x });
  };
  const onPointerMove = (e: React.PointerEvent) => {
    const { x, y } = localCoords(e);
    if (dragStart.current != null) {
      setBrush({ a: dragStart.current, b: x });
      setHover(null);
      return;
    }
    const hit = hitTest(x, y);
    setHover(hit ? { p: hit } : null);
  };
  const finishDrag = (x: number) => {
    const a = dragStart.current;
    dragStart.current = null;
    setBrush(null);
    if (a == null || !model) return;
    if (Math.abs(x - a) < 6) {
      // A click (not a drag) → open the nearest point if any.
      const hit = hitTest(x, hover?.p.py ?? x);
      if (hit) onOpen(hit.row);
      return;
    }
    const plotW = width - PAD.l - PAD.r;
    const { t0, t1 } = model;
    const toTime = (cx: number) => t0 + ((Math.max(PAD.l, Math.min(width - PAD.r, cx)) - PAD.l) / plotW) * (t1 - t0);
    const lo = Math.round(toTime(Math.min(a, x)));
    const hi = Math.round(toTime(Math.max(a, x)));
    if (hi - lo >= 1) onBrush(lo, hi);
  };
  const onPointerUp = (e: React.PointerEvent) => {
    if (dragStart.current == null) {
      // Plain click without an active drag (e.g. pointer never moved).
      const { x, y } = localCoords(e);
      const hit = hitTest(x, y);
      if (hit) onOpen(hit.row);
      return;
    }
    finishDrag(localCoords(e).x);
  };

  if (rows.length === 0) {
    return (
      <div ref={wrapRef} style={{ height, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--text3)', fontSize: 12 }}>
        No traces in view to plot.
      </div>
    );
  }

  // y-axis duration ticks (log scale) — render only the ticks within range.
  const yTicks = model
    ? [1000, 100, 10].filter(v => v <= model.maxDur * 1.4)
    : [];
  const tickTop = (v: number) => {
    if (!model) return 0;
    const plotH = height - PAD.t - PAD.b;
    const logMax = Math.log10(model.maxDur + 1) || 1;
    return PAD.t + plotH - (Math.log10(v + 1) / logMax) * plotH;
  };

  return (
    <div ref={wrapRef} style={{ position: 'relative', height }}>
      <canvas
        ref={canvasRef}
        style={{ width: '100%', height, display: 'block', cursor: dragStart.current != null ? 'crosshair' : 'pointer', touchAction: 'none' }}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerLeave={() => { setHover(null); if (dragStart.current != null) { dragStart.current = null; setBrush(null); } }}
      />
      {/* Brush rectangle overlay */}
      {brush && Math.abs(brush.b - brush.a) > 1 && (
        <div style={{
          position: 'absolute', top: 0, height,
          left: Math.min(brush.a, brush.b),
          width: Math.abs(brush.b - brush.a),
          background: 'color-mix(in srgb, var(--accent) 12%, transparent)',
          border: '1px solid var(--accent)',
          pointerEvents: 'none',
        }} />
      )}
      {/* y-axis duration ticks */}
      {yTicks.map((v) => (
        <div key={v} className="mono" style={{
          position: 'absolute', right: 4, top: tickTop(v),
          transform: 'translateY(-50%)', fontSize: 9, color: 'var(--text3)',
          background: 'var(--bg2)', padding: '0 3px', pointerEvents: 'none',
        }}>{v >= 1000 ? '1s' : `${v}ms`}</div>
      ))}
      {/* hover tooltip */}
      {hover && (
        <div style={{
          position: 'absolute', pointerEvents: 'none', zIndex: 5,
          left: `min(${hover.p.px}px, calc(100% - 220px))`,
          top: hover.p.py, transform: 'translate(10px, -50%)',
          background: 'var(--bg2)', border: '1px solid var(--border)',
          borderRadius: 4, padding: '6px 9px', fontSize: 11, color: 'var(--text)',
          whiteSpace: 'nowrap', boxShadow: '0 4px 14px rgba(0,0,0,0.25)',
        }}>
          <div style={{ fontWeight: 600, marginBottom: 2 }}>{hover.p.row.rootName || '—'}</div>
          <div style={{ color: 'var(--text2)' }}>{hover.p.row.serviceName}</div>
          <div className="mono">{fmtDur(hover.p.row.durationMs)} · {tsShort(hover.p.row.startTime)}</div>
          <div style={{ marginTop: 2 }}>
            <span className={`badge ${hover.p.row.hasError ? 'b-err' : 'b-ok'}`} style={{ fontSize: 9 }}>
              {hover.p.row.hasError ? 'ERROR' : 'OK'}
            </span>
          </div>
        </div>
      )}
    </div>
  );
}
