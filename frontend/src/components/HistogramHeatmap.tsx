import { useEffect, useRef, useState } from 'react';
import type { HistogramResult } from '@/lib/types';
import { fmtSmart } from '@/lib/chartFmt';

// HistogramHeatmap — an explicit OTel histogram rendered as a time × latency-
// bucket density heatmap with p50/p95/p99 bands overlaid (v0.6.56). The avg
// line on /metrics throws the distribution away; this shows it. Canvas (not
// SVG) for the same reason as LatencyHeatmap — hundreds of cells paint in
// <1ms vs hundreds of <rect> nodes per render.
//
//   mode='heatmap'    — density cells + percentile lines on top
//   mode='percentile' — just the three percentile bands on a clean axis

const PALETTE = [
  'rgba(0,0,0,0)', // 0 — empty cell
  'rgba(63,140,253,0.18)',
  'rgba(63,140,253,0.40)',
  'rgba(56,113,213,0.65)',
  'rgba(220,164,82,0.80)',
  'rgba(232,78,78,0.90)',
];

const PCTL = [
  { key: 'p50' as const, color: 'rgba(63,140,253,0.95)', label: 'p50' },
  { key: 'p95' as const, color: 'rgba(250,204,21,0.95)', label: 'p95' },
  { key: 'p99' as const, color: 'rgba(232,78,78,0.98)', label: 'p99' },
];

// valueToRow maps a latency value onto the fractional bucket-row axis
// (0 = bottom of the lowest bucket). Percentile lines share the cells' band
// layout so "the p99 line sits in the red band" reads correctly.
function valueToRow(v: number, bounds: number[]): number {
  let k = 0;
  while (k < bounds.length && bounds[k] < v) k++;
  const lo = k === 0 ? 0 : bounds[k - 1];
  const hi = k < bounds.length ? bounds[k] : bounds[bounds.length - 1];
  const frac = hi > lo ? Math.min(1, Math.max(0, (v - lo) / (hi - lo))) : 0;
  return k + frac;
}

export function HistogramHeatmap({ data, mode = 'heatmap', unit = 'ms', height = 240 }: {
  data: HistogramResult;
  mode?: 'heatmap' | 'percentile';
  unit?: string;
  height?: number;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const [hover, setHover] = useState<{ x: number; y: number; tNs: number; label: string; count: number } | null>(null);

  // rows = N finite buckets + 1 overflow (+Inf). maxCount drives the log
  // colour scale (span counts on a wide window range over decades).
  const rows = data.bounds.length + 1;
  let maxCount = 1;
  for (const col of data.counts) for (const c of col) if (c > maxCount) maxCount = c;

  useEffect(() => {
    const canvas = canvasRef.current, wrap = containerRef.current;
    if (!canvas || !wrap) return;
    const draw = () => {
      const w = wrap.clientWidth;
      if (!w) return;
      const dpr = window.devicePixelRatio || 1;
      canvas.style.width = w + 'px';
      canvas.style.height = height + 'px';
      canvas.width = Math.round(w * dpr);
      canvas.height = Math.round(height * dpr);
      const ctx = canvas.getContext('2d');
      if (!ctx) return;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.clearRect(0, 0, w, height);

      const cols = data.times.length;
      if (cols === 0 || rows === 0) return;
      const padL = 64, padB = 22, padT = 6, padR = 8;
      const plotW = Math.max(1, w - padL - padR);
      const plotH = Math.max(1, height - padT - padB);
      const cellW = plotW / cols;
      const bandH = plotH / rows;
      const rowToY = (row: number) => padT + plotH * (1 - row / rows);
      const xOf = (i: number) => padL + i * cellW + cellW / 2;

      // 1) density cells (heatmap mode only). Row 0 (smallest latency) at
      //    the bottom; +Inf overflow at the top.
      if (mode === 'heatmap') {
        const lmax = Math.log(maxCount + 1);
        for (let i = 0; i < cols; i++) {
          const col = data.counts[i];
          if (!col) continue;
          for (let j = 0; j < rows; j++) {
            const c = col[j] ?? 0;
            if (c === 0) continue;
            const t = Math.log(c + 1) / lmax;
            const stop = Math.min(PALETTE.length - 1, Math.max(1, Math.floor(t * (PALETTE.length - 1)) + 1));
            ctx.fillStyle = PALETTE[stop];
            const x = padL + i * cellW;
            const y = padT + (rows - 1 - j) * bandH;
            ctx.fillRect(x, y, Math.ceil(cellW) + 0.5, Math.ceil(bandH) + 0.5);
          }
        }
      }

      // 2) percentile overlay lines. Gaps (empty buckets → 0) break the
      //    line rather than drawing a misleading drop to the axis.
      for (const p of PCTL) {
        const vals = data[p.key] ?? [];
        ctx.strokeStyle = p.color;
        ctx.lineWidth = 1.6;
        ctx.beginPath();
        let started = false;
        for (let i = 0; i < cols; i++) {
          const v = vals[i] ?? 0;
          if (v <= 0) { started = false; continue; }
          const y = rowToY(valueToRow(v, data.bounds));
          if (!started) { ctx.moveTo(xOf(i), y); started = true; } else ctx.lineTo(xOf(i), y);
        }
        ctx.stroke();
      }

      // 3) y-axis labels (bucket upper bounds)
      const css = getComputedStyle(document.documentElement);
      ctx.fillStyle = css.getPropertyValue('--text2').trim() || '#7d8693';
      ctx.font = '10px ui-monospace, SFMono-Regular, monospace';
      ctx.textAlign = 'right';
      ctx.textBaseline = 'middle';
      const yLabels = Math.min(5, data.bounds.length);
      for (let i = 0; i < yLabels; i++) {
        const k = Math.floor((data.bounds.length - 1) * (i / Math.max(1, yLabels - 1)));
        const y = padT + (rows - 1 - k) * bandH + bandH / 2;
        ctx.fillText(fmtSmart(data.bounds[k], unit), padL - 6, y);
      }

      // 4) x-axis labels (first / mid / last)
      ctx.textAlign = 'center';
      ctx.textBaseline = 'top';
      const tFmt = (ns: number) => {
        const d = new Date(ns / 1e6);
        return `${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}`;
      };
      for (const i of [0, Math.floor(cols / 2), cols - 1]) {
        if (i < 0 || i >= cols) continue;
        ctx.fillText(tFmt(data.times[i]), padL + i * cellW + cellW / 2, height - padB + 4);
      }
    };
    draw();
    const ro = new ResizeObserver(draw);
    ro.observe(wrap);
    return () => ro.disconnect();
  }, [data, mode, unit, height, rows, maxCount]);

  const onMouseMove = (e: React.MouseEvent<HTMLCanvasElement>) => {
    const rect = e.currentTarget.getBoundingClientRect();
    const w = rect.width;
    const cols = data.times.length;
    const padL = 64, padB = 22, padT = 6, padR = 8;
    const plotW = Math.max(1, w - padL - padR);
    const plotH = Math.max(1, height - padT - padB);
    const cellW = plotW / cols;
    const bandH = plotH / rows;
    const x = e.clientX - rect.left, y = e.clientY - rect.top;
    if (cols === 0 || x < padL || y < padT || y > height - padB) { setHover(null); return; }
    const col = Math.floor((x - padL) / cellW);
    const rowFromTop = Math.floor((y - padT) / bandH);
    const j = (rows - 1) - rowFromTop;
    if (col < 0 || col >= cols || j < 0 || j >= rows) { setHover(null); return; }
    const count = data.counts[col]?.[j] ?? 0;
    const lo = j === 0 ? 0 : data.bounds[j - 1];
    const hi = j < data.bounds.length ? data.bounds[j] : Infinity;
    const label = hi === Infinity ? `> ${fmtSmart(lo, unit)}` : `${fmtSmart(lo, unit)} – ${fmtSmart(hi, unit)}`;
    setHover({ x, y, tNs: data.times[col], label, count });
  };

  return (
    <div ref={containerRef} style={{ position: 'relative', width: '100%' }}
         onMouseLeave={() => setHover(null)}>
      <div style={{
        position: 'absolute', top: 6, right: 8, zIndex: 4, display: 'flex', gap: 10,
        fontSize: 10, fontFamily: 'ui-monospace, monospace', pointerEvents: 'none',
      }}>
        {PCTL.map(p => (
          <span key={p.key} style={{ color: p.color, display: 'inline-flex', alignItems: 'center', gap: 4 }}>
            <span style={{ width: 10, height: 2, background: p.color, display: 'inline-block' }} />{p.label}
          </span>
        ))}
      </div>
      {data.skipped > 0 && (
        <div style={{
          position: 'absolute', bottom: 6, right: 8, zIndex: 4, fontSize: 10,
          color: 'var(--warn, #facc15)', fontFamily: 'ui-monospace, monospace', pointerEvents: 'none',
        }} title="Series whose bucket layout differs from the canonical one were skipped to avoid mis-summing into the wrong latency band.">
          {data.skipped} series skipped
        </div>
      )}
      <canvas ref={canvasRef} style={{ display: 'block', cursor: 'crosshair' }} onMouseMove={onMouseMove} />
      {hover && (
        <div style={{
          position: 'absolute', pointerEvents: 'none',
          left: Math.min(hover.x + 10, (containerRef.current?.clientWidth ?? 800) - 200),
          top: Math.max(0, hover.y - 40),
          background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 4,
          padding: '6px 9px', fontSize: 11, color: 'var(--text)', whiteSpace: 'nowrap',
          zIndex: 5, fontFamily: 'ui-monospace, monospace', boxShadow: '0 4px 14px rgba(0,0,0,0.35)',
        }}>
          <div style={{ fontWeight: 600 }}>{new Date(hover.tNs / 1e6).toLocaleTimeString()}</div>
          <div style={{ color: 'var(--text2)' }}>{hover.label} · {hover.count.toLocaleString()}</div>
        </div>
      )}
    </div>
  );
}
