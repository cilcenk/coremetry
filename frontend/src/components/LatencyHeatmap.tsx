import { useEffect, useRef, useState } from 'react';
import type { LatencyHeatmap as Heatmap } from '@/lib/types';
import { fmtSmart } from '@/lib/chartFmt';

// LatencyHeatmap — Honeycomb-style 2D density visualisation.
// X = time (left → right), Y = log-scale latency
// (bottom → top, slowest at top), cell colour = count.
// Same time axis as the metric line chart so an operator
// can flip between the two views and read the same window.
//
// Why canvas rather than SVG: at 60 × 28 = 1680 cells the
// canvas paints in <1 ms; the SVG equivalent would build
// 1680 <rect> nodes every render. Hover detection is hand-
// rolled against the cell grid (constant-time lookup; no
// React event listener per cell).

const PALETTE = [
  // Cool → warm gradient. First entry is the empty-cell
  // background; last is the peak. uPlot's accent palette
  // wouldn't read as "density" — these stops are picked from
  // viridis-tail so the eye reads "dim → bright" as count.
  'rgba(0,0,0,0)',          // 0 — invisible (no cell)
  'rgba(63,140,253,0.18)',
  'rgba(63,140,253,0.40)',
  'rgba(56,113,213,0.65)',
  'rgba(220,164,82,0.80)',
  'rgba(232,78,78,0.90)',
];

// Z-score outlier detection (v0.5.256). For each cell, z =
// (count - μ) / σ where μ + σ are taken over non-zero cells in
// the whole grid. Cells with z ≥ OUTLIER_Z get a contrasting
// outline so the eye snaps to "this latency band is unusually
// busy for this window". 2.5σ covers the top ~0.6% of cells
// under a normal distribution — empirically the right cut for
// span heatmaps where the bulk of cells are quiet and the
// interesting ones spike.
const OUTLIER_Z = 2.5;

interface HeatmapStats {
  mean: number;
  stddev: number;
  // outliers[col][row] = true when that cell's z-score ≥ OUTLIER_Z.
  // Stored as a flat Set of "col,row" strings so the tooltip can
  // O(1)-check the hover cell without re-deriving z on every move.
  outliers: Set<string>;
}

function computeHeatmapStats(data: Heatmap): HeatmapStats {
  // Stats over NON-ZERO cells only — empty cells would drag the
  // mean to ~0 and inflate every other cell's z-score. The
  // intuition matches the operator's: outlier = "this filled cell
  // is way busier than the other filled cells", not "this cell
  // exists at all".
  let sum = 0, n = 0;
  for (let i = 0; i < data.counts.length; i++) {
    const col = data.counts[i];
    if (!col) continue;
    for (let j = 0; j < col.length; j++) {
      const c = col[j];
      if (c > 0) { sum += c; n++; }
    }
  }
  if (n === 0) {
    return { mean: 0, stddev: 0, outliers: new Set() };
  }
  const mean = sum / n;
  let sqSum = 0;
  for (let i = 0; i < data.counts.length; i++) {
    const col = data.counts[i];
    if (!col) continue;
    for (let j = 0; j < col.length; j++) {
      const c = col[j];
      if (c > 0) { sqSum += (c - mean) * (c - mean); }
    }
  }
  const stddev = Math.sqrt(sqSum / Math.max(1, n));
  const outliers = new Set<string>();
  if (stddev > 0) {
    for (let i = 0; i < data.counts.length; i++) {
      const col = data.counts[i];
      if (!col) continue;
      for (let j = 0; j < col.length; j++) {
        const c = col[j];
        if (c > 0 && (c - mean) / stddev >= OUTLIER_Z) {
          outliers.add(i + ',' + j);
        }
      }
    }
  }
  return { mean, stddev, outliers };
}

export function LatencyHeatmap({ data, height = 220 }: {
  data: Heatmap;
  height?: number;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const [hover, setHover] = useState<{
    x: number; y: number;
    time: number; durMs: number; count: number;
    z: number; isOutlier: boolean;
  } | null>(null);
  // Stats are recomputed when `data` changes; cheap (O(N*M)
  // single pass) and avoids a useMemo deopt + dep churn.
  const statsRef = useRef<HeatmapStats>({ mean: 0, stddev: 0, outliers: new Set() });
  statsRef.current = computeHeatmapStats(data);

  // Re-paint on data / dimension change. We don't memo the
  // result — paint is fast and React re-renders when hover
  // updates anyway.
  useEffect(() => {
    const canvas = canvasRef.current;
    const wrap = containerRef.current;
    if (!canvas || !wrap) return;

    const draw = () => {
      const w = wrap.clientWidth;
      if (!w) return;
      // High-DPR sharp canvas: backing store is dpr× the
      // CSS size; we draw in CSS units after a context
      // scale. Skips the blurry render on retina screens.
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
      const rows = data.durationBins.length;
      if (cols === 0 || rows === 0) return;

      // Reserve 56 px on the left for the y-axis labels
      // and 22 px on the bottom for the x-axis labels.
      const padL = 56, padB = 22, padT = 4, padR = 4;
      const plotW = Math.max(1, w - padL - padR);
      const plotH = Math.max(1, height - padT - padB);
      const cellW = plotW / cols;
      const cellH = plotH / rows;

      const max = Math.max(1, data.maxCount);
      // Logarithmic colour scale — span counts on a 24h chart
      // can range over 4 decades; linear mapping makes the
      // mode invisible. log(count+1)/log(max+1) → [0,1].
      const lmax = Math.log(max + 1);
      const stats = statsRef.current;
      for (let i = 0; i < cols; i++) {
        for (let j = 0; j < rows; j++) {
          const c = data.counts[i]?.[j] ?? 0;
          if (c === 0) continue;
          const t = Math.log(c + 1) / lmax;
          const stop = Math.min(PALETTE.length - 1,
            Math.max(1, Math.floor(t * (PALETTE.length - 1)) + 1));
          ctx.fillStyle = PALETTE[stop];
          // Y-axis is inverted: row 0 (smallest latency) at the bottom.
          const x = padL + i * cellW;
          const y = padT + (rows - 1 - j) * cellH;
          ctx.fillRect(x, y, Math.ceil(cellW) + 0.5, Math.ceil(cellH) + 0.5);
        }
      }
      // Outlier highlight pass (v0.5.256). Painted AFTER the
      // base fill so the outline sits on top of the cell colour.
      // Bright amber stroke makes outliers visually pop without
      // changing the underlying density palette — the operator's
      // colour intuition for "warm = busy" is preserved.
      if (stats.outliers.size > 0) {
        ctx.strokeStyle = 'rgba(250,204,21,0.95)';
        ctx.lineWidth = 1.5;
        for (const key of stats.outliers) {
          const [iStr, jStr] = key.split(',');
          const i = +iStr, j = +jStr;
          const x = padL + i * cellW;
          const y = padT + (rows - 1 - j) * cellH;
          ctx.strokeRect(x + 0.5, y + 0.5, Math.max(1, cellW - 1), Math.max(1, cellH - 1));
        }
      }

      // Y-axis labels — pick 4 evenly-spaced rows so the
      // axis isn't a smear of overlapping numbers.
      const css = getComputedStyle(document.documentElement);
      ctx.fillStyle = css.getPropertyValue('--text2').trim() || '#7d8693';
      ctx.font = '10px ui-monospace, SFMono-Regular, monospace';
      ctx.textAlign = 'right';
      ctx.textBaseline = 'middle';
      const yLabels = 4;
      for (let i = 0; i <= yLabels; i++) {
        const j = Math.floor((rows - 1) * (i / yLabels));
        const ms = data.durationBins[j];
        const y = padT + (rows - 1 - j) * cellH + cellH / 2;
        ctx.fillText(fmtSmart(ms, 'ms'), padL - 4, y);
      }

      // X-axis labels — first, last, and a midpoint
      // timestamp.
      ctx.textAlign = 'center';
      ctx.textBaseline = 'top';
      const tFmt = (ns: number) => {
        const d = new Date(ns / 1e6);
        return `${d.getHours().toString().padStart(2,'0')}:${d.getMinutes().toString().padStart(2,'0')}`;
      };
      const xPos = [0, Math.floor(cols / 2), cols - 1];
      for (const i of xPos) {
        const x = padL + i * cellW + cellW / 2;
        ctx.fillText(tFmt(data.times[i]), x, height - padB + 4);
      }
    };

    draw();
    const ro = new ResizeObserver(draw);
    ro.observe(wrap);
    return () => ro.disconnect();
  }, [data, height]);

  // Mouse hover → look up the cell under the cursor and
  // surface (time, latency band, count) in the floating
  // tooltip. Cell math mirrors the draw loop so positions
  // line up exactly.
  const onMouseMove = (e: React.MouseEvent<HTMLCanvasElement>) => {
    const wrap = containerRef.current;
    if (!wrap) return;
    const rect = (e.currentTarget as HTMLCanvasElement).getBoundingClientRect();
    const w = rect.width;
    const cols = data.times.length;
    const rows = data.durationBins.length;
    const padL = 56, padB = 22, padT = 4, padR = 4;
    const plotW = Math.max(1, w - padL - padR);
    const plotH = Math.max(1, height - padT - padB);
    const cellW = plotW / cols;
    const cellH = plotH / rows;
    const x = e.clientX - rect.left;
    const y = e.clientY - rect.top;
    if (x < padL || y < padT || y > height - padB) {
      setHover(null); return;
    }
    const col = Math.floor((x - padL) / cellW);
    const rowFromTop = Math.floor((y - padT) / cellH);
    const row = (rows - 1) - rowFromTop;
    if (col < 0 || col >= cols || row < 0 || row >= rows) {
      setHover(null); return;
    }
    const c = data.counts[col]?.[row] ?? 0;
    const stats = statsRef.current;
    const z = c > 0 && stats.stddev > 0 ? (c - stats.mean) / stats.stddev : 0;
    setHover({
      x, y,
      time: data.times[col],
      durMs: data.durationBins[row],
      count: c,
      z,
      isOutlier: stats.outliers.has(col + ',' + row),
    });
  };

  // Sampling indicator (v0.5.238) — when the backend ran a
  // hash-sample to keep wide windows under the execution cap,
  // surface a small tag so the operator knows the cell counts
  // are extrapolated (×1/samplingRate). Shape stays accurate.
  const samplingRate = data.samplingRate ?? 1;
  const sampledTag = samplingRate < 1 ? `Sampled at ${(samplingRate * 100).toFixed(0)}%` : null;

  return (
    <div ref={containerRef}
         style={{ position: 'relative', width: '100%' }}
         onMouseLeave={() => setHover(null)}>
      {sampledTag && (
        <div style={{
          position: 'absolute', top: 6, right: 6, zIndex: 4,
          fontSize: 10, padding: '2px 6px', borderRadius: 10,
          background: 'rgba(250,204,21,0.12)',
          border: '1px solid rgba(250,204,21,0.40)',
          color: 'var(--warn, #facc15)',
          pointerEvents: 'none',
          fontFamily: 'ui-monospace, monospace',
        }} title="Wide windows are hash-sampled by trace_id to keep the query under the execution cap; cell counts are estimated by multiplying back up.">
          {sampledTag}
        </div>
      )}
      <canvas ref={canvasRef}
              style={{ display: 'block', cursor: 'crosshair' }}
              onMouseMove={onMouseMove} />
      {hover && (
        <div style={{
          position: 'absolute', pointerEvents: 'none',
          left: Math.min(hover.x + 10, (containerRef.current?.clientWidth ?? 800) - 200),
          top: Math.max(0, hover.y - 36),
          background: 'var(--bg2)',
          border: '1px solid var(--border)',
          borderRadius: 4, padding: '6px 9px',
          fontSize: 11, color: 'var(--text)',
          whiteSpace: 'nowrap', zIndex: 5,
          fontFamily: 'ui-monospace, monospace',
          boxShadow: '0 4px 14px rgba(0,0,0,0.35)',
        }}>
          <div style={{ fontWeight: 600 }}>
            {new Date(hover.time / 1e6).toLocaleTimeString()}
          </div>
          <div style={{ color: 'var(--text2)' }}>
            ≤ {fmtSmart(hover.durMs, 'ms')} · {hover.count.toLocaleString()} spans
          </div>
          {hover.count > 0 && (
            <div style={{
              color: hover.isOutlier ? 'var(--warn, #facc15)' : 'var(--text3)',
              fontSize: 10, marginTop: 2,
            }}>
              z = {hover.z.toFixed(2)}{hover.isOutlier && ' · outlier'}
            </div>
          )}
        </div>
      )}
      {/* Outlier legend — only renders when at least one outlier
          is painted, so quiet heatmaps don't carry visual noise.
          Sits bottom-right; mirrors the sampledTag's top-right slot. */}
      {statsRef.current.outliers.size > 0 && (
        <div style={{
          position: 'absolute', bottom: 6, right: 6, zIndex: 4,
          fontSize: 10, padding: '2px 6px', borderRadius: 10,
          background: 'rgba(250,204,21,0.10)',
          border: '1px solid rgba(250,204,21,0.40)',
          color: 'var(--warn, #facc15)',
          pointerEvents: 'none',
          fontFamily: 'ui-monospace, monospace',
        }} title={`Cells with z-score ≥ ${OUTLIER_Z} (count > mean + ${OUTLIER_Z}σ over non-empty cells)`}>
          {statsRef.current.outliers.size} outlier{statsRef.current.outliers.size === 1 ? '' : 's'}
        </div>
      )}
    </div>
  );
}
