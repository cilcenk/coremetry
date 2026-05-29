import { useEffect, useRef, useState } from 'react';
import type { HistogramResult } from '@/lib/types';
import { fmtSmart } from '@/lib/chartFmt';

// HistogramHeatmap — an explicit OTel histogram rendered three ways (v0.6.56,
// v0.6.57). The avg line on /metrics throws the distribution away; these
// show it. Canvas (not SVG) for the same reason as LatencyHeatmap —
// hundreds of cells/bars paint in <1ms vs hundreds of nodes per render.
//
//   mode='heatmap'    — time × bucket density cells + percentile lines
//   mode='percentile' — just the three percentile bands on the bucket axis
//   mode='volume'     — Dynatrace-style: per-time span-COUNT bars (right
//                       axis) behind a DURATION line (left latency axis).
//                       bars = how many, line = how slow.

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

const BAR_FILL = 'rgba(99,140,253,0.22)';

// valueToRow maps a latency value onto the fractional bucket-row axis
// (0 = bottom of the lowest bucket). Used by heatmap/percentile modes so
// the percentile line "sits in the red band" — same layout as the cells.
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
  mode?: 'heatmap' | 'percentile' | 'volume';
  unit?: string;
  height?: number;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const [hover, setHover] = useState<{ x: number; y: number; html: string } | null>(null);

  const rows = data.bounds.length + 1;
  // Per-time totals (volume) + the colour-scale / axis maxima.
  const volume = data.times.map((_, i) => (data.counts[i] ?? []).reduce((a, b) => a + b, 0));
  const maxVolume = Math.max(1, ...volume);
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
      // Volume mode reserves a right gutter for the count axis.
      const padL = 64, padB = 22, padT = 6, padR = mode === 'volume' ? 52 : 8;
      const plotW = Math.max(1, w - padL - padR);
      const plotH = Math.max(1, height - padT - padB);
      const cellW = plotW / cols;
      const bandH = plotH / rows;
      const xOf = (i: number) => padL + i * cellW + cellW / 2;
      const css = getComputedStyle(document.documentElement);
      const axisCol = css.getPropertyValue('--text2').trim() || '#7d8693';

      // Percentile line: ONE polyline through the non-zero points (connect
      // across empty buckets) + a dot at each point. v0.6.62 — at fine
      // auto-step a sparse histogram exports into single buckets with empty
      // neighbours, so every percentile point was isolated; the old
      // gap-breaking loop did moveTo with no lineTo and stroked an empty
      // path → the operator saw bars but no süre line. Connecting across
      // gaps + dots makes even a lone sample visible.
      const drawPctl = (vals: number[], color: string, yOf: (v: number) => number) => {
        const pts: [number, number][] = [];
        for (let i = 0; i < cols; i++) {
          const v = vals[i] ?? 0;
          if (v > 0) pts.push([xOf(i), yOf(v)]);
        }
        if (pts.length === 0) return;
        ctx.strokeStyle = color;
        ctx.fillStyle = color;
        ctx.lineWidth = 1.6;
        ctx.beginPath();
        pts.forEach(([x, y], k) => (k === 0 ? ctx.moveTo(x, y) : ctx.lineTo(x, y)));
        ctx.stroke();
        for (const [x, y] of pts) {
          ctx.beginPath();
          ctx.arc(x, y, 1.8, 0, Math.PI * 2);
          ctx.fill();
        }
      };

      if (mode === 'volume') {
        // span-COUNT bars (right axis).
        ctx.fillStyle = BAR_FILL;
        const bw = Math.max(1, cellW * 0.7);
        for (let i = 0; i < cols; i++) {
          const h = plotH * (volume[i] / maxVolume);
          if (h <= 0) continue;
          ctx.fillRect(padL + i * cellW + (cellW - bw) / 2, padT + plotH - h, bw, h);
        }
        // DURATION lines on the bucket-band axis — SAME layout as the
        // heatmap mode. The bounds are exponentially spaced (0,5,…,2500,
        // 10000), so the band axis reads like a log scale and a multi-second
        // p99 outlier no longer squashes the 180ms p50 flat against the
        // x-axis. v0.6.63: a linear latency axis collapsed every normal-range
        // percentile under one spike — the operator saw bars but no süre line.
        for (const p of PCTL) drawPctl(data[p.key] ?? [], p.color, (v) => padT + plotH * (1 - valueToRow(v, data.bounds) / rows));
        // left axis = latency bucket bounds (matches heatmap), right = count.
        ctx.fillStyle = axisCol;
        ctx.font = '10px ui-monospace, SFMono-Regular, monospace';
        ctx.textBaseline = 'middle';
        const yLabels = Math.min(5, data.bounds.length);
        for (let i = 0; i < yLabels; i++) {
          const k = Math.floor((data.bounds.length - 1) * (i / Math.max(1, yLabels - 1)));
          ctx.textAlign = 'right';
          ctx.fillText(fmtSmart(data.bounds[k], unit), padL - 6, padT + (rows - 1 - k) * bandH + bandH / 2);
        }
        for (let i = 0; i <= 4; i++) {
          const frac = i / 4;
          ctx.textAlign = 'left';
          ctx.fillText(fmtCount(maxVolume * frac), w - padR + 6, padT + plotH * (1 - frac));
        }
      } else {
        // heatmap density cells (heatmap mode only)
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
              ctx.fillRect(padL + i * cellW, padT + (rows - 1 - j) * bandH, Math.ceil(cellW) + 0.5, Math.ceil(bandH) + 0.5);
            }
          }
        }
        // percentile lines on the bucket-band axis (connect across gaps + dots)
        for (const p of PCTL) drawPctl(data[p.key] ?? [], p.color, (v) => padT + plotH * (1 - valueToRow(v, data.bounds) / rows));
        // y-axis labels = bucket upper bounds
        ctx.fillStyle = axisCol;
        ctx.font = '10px ui-monospace, SFMono-Regular, monospace';
        ctx.textAlign = 'right';
        ctx.textBaseline = 'middle';
        const yLabels = Math.min(5, data.bounds.length);
        for (let i = 0; i < yLabels; i++) {
          const k = Math.floor((data.bounds.length - 1) * (i / Math.max(1, yLabels - 1)));
          const y = padT + (rows - 1 - k) * bandH + bandH / 2;
          ctx.fillText(fmtSmart(data.bounds[k], unit), padL - 6, y);
        }
      }

      // x-axis labels (first / mid / last) — shared by all modes.
      ctx.fillStyle = axisCol;
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
  }, [data, mode, unit, height]);

  const onMouseMove = (e: React.MouseEvent<HTMLCanvasElement>) => {
    const rect = e.currentTarget.getBoundingClientRect();
    const w = rect.width;
    const cols = data.times.length;
    const padL = 64, padB = 22, padT = 6, padR = mode === 'volume' ? 52 : 8;
    const plotW = Math.max(1, w - padL - padR);
    const plotH = Math.max(1, height - padT - padB);
    const cellW = plotW / cols;
    const bandH = plotH / rows;
    const x = e.clientX - rect.left, y = e.clientY - rect.top;
    if (cols === 0 || x < padL || x > w - padR || y < padT || y > height - padB) { setHover(null); return; }
    const col = Math.floor((x - padL) / cellW);
    if (col < 0 || col >= cols) { setHover(null); return; }
    const t = new Date(data.times[col] / 1e6).toLocaleTimeString();
    if (mode === 'volume') {
      const html = `${t}|${fmtCount(volume[col])} spans · p50 ${fmtSmart(data.p50[col] ?? 0, unit)} · p99 ${fmtSmart(data.p99[col] ?? 0, unit)}`;
      setHover({ x, y, html });
      return;
    }
    const rowFromTop = Math.floor((y - padT) / bandH);
    const j = (rows - 1) - rowFromTop;
    if (j < 0 || j >= rows) { setHover(null); return; }
    const count = data.counts[col]?.[j] ?? 0;
    const lo = j === 0 ? 0 : data.bounds[j - 1];
    const hi = j < data.bounds.length ? data.bounds[j] : Infinity;
    const band = hi === Infinity ? `> ${fmtSmart(lo, unit)}` : `${fmtSmart(lo, unit)} – ${fmtSmart(hi, unit)}`;
    setHover({ x, y, html: `${t}|${band} · ${count.toLocaleString()}` });
  };

  return (
    <div ref={containerRef} style={{ position: 'relative', width: '100%' }}
         onMouseLeave={() => setHover(null)}>
      <div style={{
        position: 'absolute', top: 6, left: 70, zIndex: 4, display: 'flex', gap: 10,
        fontSize: 10, fontFamily: 'ui-monospace, monospace', pointerEvents: 'none',
      }}>
        {PCTL.map(p => (
          <span key={p.key} style={{ color: p.color, display: 'inline-flex', alignItems: 'center', gap: 4 }}>
            <span style={{ width: 10, height: 2, background: p.color, display: 'inline-block' }} />{p.label}
          </span>
        ))}
        {mode === 'volume' && (
          <span style={{ color: 'var(--text3)', display: 'inline-flex', alignItems: 'center', gap: 4 }}>
            <span style={{ width: 10, height: 8, background: BAR_FILL, display: 'inline-block' }} />count
          </span>
        )}
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
          left: Math.min(hover.x + 10, (containerRef.current?.clientWidth ?? 800) - 240),
          top: Math.max(0, hover.y - 40),
          background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 4,
          padding: '6px 9px', fontSize: 11, color: 'var(--text)', whiteSpace: 'nowrap',
          zIndex: 5, fontFamily: 'ui-monospace, monospace', boxShadow: '0 4px 14px rgba(0,0,0,0.35)',
        }}>
          <div style={{ fontWeight: 600 }}>{hover.html.split('|')[0]}</div>
          <div style={{ color: 'var(--text2)' }}>{hover.html.split('|')[1]}</div>
        </div>
      )}
    </div>
  );
}

function fmtCount(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M';
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'k';
  return String(Math.round(n));
}
