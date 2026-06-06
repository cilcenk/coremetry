// VolumeChart.tsx — time-bucketed span-volume histogram (Tempo/Datadog-grade).
//
// ~30 equal time buckets across the visible window. Each bucket is ONE vertical
// bar whose total height ∝ (bucket total count / max bucket count). The bar is
// vertically STACKED: the bottom slice is ERRORS (solid var(--err)), the top
// slice is OK (var(--text3) @~0.5). Hot buckets (errRate > ~3%) get a red-tinted
// column background AND a redder OK slice so error-heavy ranges read red at a
// glance while healthy ranges stay grey. A P99 duration line (var(--warn)) rides
// across the bucket centers with a "p99 <max>ms" / "0" axis label on the right.
//
// Buckets derive from the CURRENTLY-fetched + filtered trace rows (count + p99
// per bucket) — so the chart always matches the table. Canvas (not uPlot)
// because the stacked bar + per-bucket hot tint + overlaid p99 line compose in a
// single paint that uPlot's bars plugin doesn't express cleanly.

import { useEffect, useMemo, useRef, useState } from 'react';
import type { TraceRow } from '@/lib/types';
import { fmtDur } from './shared';

const PAD = { l: 8, r: 8, t: 14, b: 6 };
const BUCKETS = 30;          // ~30 equal time buckets across the window
const HOT_RATE = 0.03;       // errRate above this tints the whole bar red
const GAP = 3;               // px gap between bars
const TOP_RADIUS = 3;        // rounded top corners only

interface Bucket {
  t: number;       // bucket start (ms)
  ok: number;
  err: number;
  total: number;
  p99: number;     // ms
  errRate: number; // 0..1
}

export function VolumeChart({
  rows, height = 120,
}: {
  rows: TraceRow[];
  height?: number;
}) {
  const wrapRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const [width, setWidth] = useState(900);
  const [hover, setHover] = useState<{ b: Bucket; x: number } | null>(null);
  const barsRef = useRef<{ b: Bucket; x0: number; x1: number }[]>([]);

  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth || 900));
    ro.observe(el);
    setWidth(el.clientWidth || 900);
    return () => ro.disconnect();
  }, []);

  // Bucket the live rows into a DENSE 30-cell grid over [lo, hi]. Empty cells
  // are kept (rendered as gaps) so the time axis is uniform and the p99 line
  // spans real bucket centers. p99 per bucket is computed from each bucket's own
  // duration samples.
  const { buckets, totals } = useMemo(() => {
    if (rows.length === 0) {
      return { buckets: [] as Bucket[], totals: { total: 0, err: 0, p99Max: 0, maxCount: 0, maxP99: 0 } };
    }
    let lo = Infinity, hi = -Infinity;
    for (const r of rows) {
      const t = r.startTime / 1e6;
      if (t < lo) lo = t;
      if (t > hi) hi = t;
    }
    if (hi <= lo) hi = lo + 1;
    const span = hi - lo;
    const bucketMs = span / BUCKETS;

    const cells: { ok: number; err: number; durs: number[] }[] =
      Array.from({ length: BUCKETS }, () => ({ ok: 0, err: 0, durs: [] }));
    for (const r of rows) {
      const t = r.startTime / 1e6;
      let idx = Math.floor((t - lo) / bucketMs);
      if (idx < 0) idx = 0;
      if (idx >= BUCKETS) idx = BUCKETS - 1;
      const c = cells[idx];
      if (r.hasError) c.err++; else c.ok++;
      c.durs.push(r.durationMs);
    }

    const out: Bucket[] = cells.map((c, i) => {
      const total = c.ok + c.err;
      let p99 = 0;
      if (c.durs.length) {
        c.durs.sort((a, b) => a - b);
        const rank = 0.99 * (c.durs.length - 1);
        const loI = Math.floor(rank), hiI = Math.ceil(rank);
        p99 = c.durs[loI] + (c.durs[hiI] - c.durs[loI]) * (rank - loI);
      }
      return { t: lo + i * bucketMs, ok: c.ok, err: c.err, total, p99, errRate: total ? c.err / total : 0 };
    });

    let total = 0, err = 0, p99Max = 0, maxCount = 0, maxP99 = 0;
    for (const b of out) {
      total += b.total;
      err += b.err;
      p99Max = Math.max(p99Max, b.p99);
      maxCount = Math.max(maxCount, b.total);
      maxP99 = Math.max(maxP99, b.p99);
    }
    return { buckets: out, totals: { total, err, p99Max, maxCount, maxP99 } };
  }, [rows]);

  // Paint.
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
    barsRef.current = [];
    if (buckets.length === 0) return;

    const cs = getComputedStyle(canvas);
    const cOk = cs.getPropertyValue('--text3').trim() || '#888';
    const cErr = cs.getPropertyValue('--err').trim() || '#dc2626';
    const cP99 = cs.getPropertyValue('--warn').trim() || '#d97706';
    const cBg3 = cs.getPropertyValue('--bg3').trim() || '#222';

    // Tints derived from tokens via color-mix (theme-safe, no new hex).
    const hotBg = `color-mix(in srgb, ${cErr} 20%, ${cBg3})`;
    const hotOk = `color-mix(in srgb, ${cErr} 55%, ${cOk})`;

    const plotW = width - PAD.l - PAD.r;
    const plotH = height - PAD.t - PAD.b;
    const n = buckets.length;
    const slot = plotW / n;
    const barW = Math.max(1, slot - GAP);
    const maxCount = totals.maxCount || 1;
    const maxP99 = totals.maxP99 || 1;
    const baseY = PAD.t + plotH;

    const roundedTopRect = (x: number, y: number, w: number, h: number, r: number) => {
      const rr = Math.min(r, w / 2, h);
      ctx.beginPath();
      ctx.moveTo(x, y + h);
      ctx.lineTo(x, y + rr);
      ctx.arcTo(x, y, x + rr, y, rr);
      ctx.lineTo(x + w - rr, y);
      ctx.arcTo(x + w, y, x + w, y + rr, rr);
      ctx.lineTo(x + w, y + h);
      ctx.closePath();
      ctx.fill();
    };

    buckets.forEach((b, i) => {
      const x = PAD.l + i * slot + GAP / 2;
      const x1 = x + barW;
      barsRef.current.push({ b, x0: x, x1 });
      const hot = b.errRate > HOT_RATE;

      // Hot bucket: tint the WHOLE bar background column red.
      if (hot) {
        ctx.fillStyle = hotBg;
        ctx.globalAlpha = 1;
        ctx.fillRect(x, PAD.t, barW, plotH);
      }

      if (b.total <= 0) return;
      const barH = (b.total / maxCount) * plotH;
      const errH = barH * b.errRate;       // bottom slice ∝ error share
      const okH = barH - errH;             // top slice = remainder
      const top = baseY - barH;

      // Bottom slice: ERRORS, solid var(--err).
      if (errH > 0) {
        ctx.fillStyle = cErr;
        ctx.globalAlpha = 1;
        // square-bottom rect (no rounding on the baseline)
        if (okH <= 0.5) {
          // error-only bar: round its top
          roundedTopRect(x, top, barW, barH, TOP_RADIUS);
        } else {
          ctx.fillRect(x, baseY - errH, barW, errH);
        }
      }
      // Top slice: OK. Healthy → grey @0.5; hot → redder grey @0.85.
      if (okH > 0.5) {
        ctx.fillStyle = hot ? hotOk : cOk;
        ctx.globalAlpha = hot ? 0.85 : 0.5;
        roundedTopRect(x, top, barW, okH, TOP_RADIUS);
        ctx.globalAlpha = 1;
      }
    });
    ctx.globalAlpha = 1;

    // p99 line overlay across bucket centers (warn, 2px) + small circles.
    ctx.strokeStyle = cP99;
    ctx.lineWidth = 2;
    ctx.lineJoin = 'round';
    const pts: { cx: number; cy: number }[] = [];
    buckets.forEach((b, i) => {
      if (b.p99 <= 0) return;
      const cx = PAD.l + i * slot + slot / 2;
      const cy = baseY - (b.p99 / maxP99) * plotH;
      pts.push({ cx, cy });
    });
    if (pts.length) {
      ctx.beginPath();
      pts.forEach((p, i) => (i === 0 ? ctx.moveTo(p.cx, p.cy) : ctx.lineTo(p.cx, p.cy)));
      ctx.stroke();
      ctx.fillStyle = cP99;
      for (const p of pts) {
        ctx.beginPath();
        ctx.arc(p.cx, p.cy, 2, 0, Math.PI * 2);
        ctx.fill();
      }
    }
  }, [buckets, totals, width, height]);

  const onMove = (e: React.MouseEvent) => {
    const r = canvasRef.current?.getBoundingClientRect();
    if (!r) return;
    const x = e.clientX - r.left;
    const hit = barsRef.current.find(bk => x >= bk.x0 - 1 && x <= bk.x1 + 1 && bk.b.total > 0);
    setHover(hit ? { b: hit.b, x: (hit.x0 + hit.x1) / 2 } : null);
  };

  return (
    <div style={{
      background: 'var(--bg2)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 12, marginBottom: 10,
    }}>
      <div ref={wrapRef} style={{ position: 'relative', height }}>
        {buckets.length === 0 ? (
          <div style={{ height, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--text3)', fontSize: 12 }}>
            No traces in view to bucket.
          </div>
        ) : (
          <>
            <canvas ref={canvasRef}
              style={{ width: '100%', height, display: 'block' }}
              onMouseMove={onMove}
              onMouseLeave={() => setHover(null)} />

            {/* Legend top-left of the plot. */}
            <div style={{
              position: 'absolute', top: 0, left: 0, display: 'inline-flex',
              alignItems: 'center', gap: 5, fontSize: 10, color: 'var(--text3)',
              pointerEvents: 'none',
            }}>
              <span style={{ width: 8, height: 8, background: 'var(--text3)', borderRadius: 2, opacity: 0.5 }} /> ok
              <span style={{ width: 8, height: 8, background: 'var(--err)', borderRadius: 2, marginLeft: 6 }} /> error
              <span style={{ width: 12, height: 2, background: 'var(--warn)', marginLeft: 6 }} /> p99
            </div>

            {/* Right-edge p99 axis labels: max at top, 0 at bottom. */}
            <div className="mono" style={{
              position: 'absolute', top: 0, right: 0, fontSize: 9,
              color: 'var(--warn)', pointerEvents: 'none',
            }}>
              p99 {totals.p99Max ? fmtDur(totals.p99Max) : '—'}
            </div>
            <div className="mono" style={{
              position: 'absolute', bottom: 0, right: 0, fontSize: 9,
              color: 'var(--text3)', pointerEvents: 'none',
            }}>
              0
            </div>
          </>
        )}

        {hover && (
          <div style={{
            position: 'absolute', pointerEvents: 'none', zIndex: 5, top: 14,
            left: `min(${hover.x}px, calc(100% - 200px))`, transform: 'translateX(8px)',
            background: 'var(--bg2)', border: '1px solid var(--border)',
            borderRadius: 4, padding: '6px 9px', fontSize: 11, color: 'var(--text)',
            whiteSpace: 'nowrap', boxShadow: '0 4px 14px rgba(0,0,0,0.25)',
          }}>
            <div className="mono" style={{ color: 'var(--text2)', marginBottom: 2 }}>
              {new Date(hover.b.t).toLocaleTimeString()}
            </div>
            <div>
              <b>{hover.b.total}</b> spans · <b style={{ color: 'var(--err)' }}>{(hover.b.errRate * 100).toFixed(1)}%</b> err · {' '}
              <span style={{ color: 'var(--warn)' }}>p99 {hover.b.p99 ? fmtDur(hover.b.p99) : '—'}</span>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
