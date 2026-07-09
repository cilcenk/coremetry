import { useEffect, useRef } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { useThemeTick } from '@/lib/useThemeTick';
import { fmtXTicks } from '@/lib/chartFmt';

// TimeChart (v0.8.91) — the ONE time-series primitive. Generalises the proven
// OverviewChart uPlot wrapper (Canvas, so it also lands the "charts to Canvas"
// perf goal): per-series type (bar | line | area), an optional right (dual)
// axis, drag-to-brush a time range, deploy markers, a hover crosshair +
// per-series tooltip, and cross-chart cursor sync. Every time-series chart
// renders through this — no more hand-drawn axes/gridlines/hover.

export interface TimeChartSeries {
  key: string;
  label: string;
  data: number[];          // aligned to `times`
  color: string;           // a CSS var() token, resolved at draw time
  type: 'bar' | 'line' | 'area';
  axis?: 'left' | 'right'; // default 'left'
  width?: number;          // line width (line/area)
}

interface Props {
  times: number[];                  // unix seconds, ascending — shared x axis
  series: TimeChartSeries[];
  height?: number;                  // default 150
  leftUnit?: string;
  rightUnit?: string;
  deployMarkers?: number[];         // unix seconds → dashed red vlines
  onBrush?: (fromMs: number, toMs: number) => void;
  syncKey?: string;                 // uPlot.sync group for cross-chart crosshair
  fmtLeft?: (v: number) => string;  // y label formatter (left)
  fmtRight?: (v: number) => string; // y label formatter (right)
}

function cssVar(v: string): string {
  const m = /^var\((--[\w-]+)\)$/.exec(v.trim());
  if (!m) return v;
  return getComputedStyle(document.documentElement).getPropertyValue(m[1]).trim() || v;
}
const kfmt = (v: number) => (v >= 1000 ? `${(v / 1000).toFixed(1)}k` : v.toFixed(v < 10 && v % 1 !== 0 ? 1 : 0));

export function TimeChart({
  times, series, height = 150, leftUnit = '', rightUnit = '',
  deployMarkers, onBrush, syncKey, fmtLeft, fmtRight,
}: Props) {
  const hostRef = useRef<HTMLDivElement>(null);
  const ttRef = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | null>(null);
  const onBrushRef = useRef(onBrush); onBrushRef.current = onBrush;
  const themeTick = useThemeTick();

  useEffect(() => {
    const el = hostRef.current;
    if (!el || times.length < 2 || series.length === 0) return;

    const colors = series.map(s => cssVar(s.color));
    const gridc = cssVar('var(--border)');
    const text3 = cssVar('var(--text3)');
    const hasRight = series.some(s => s.axis === 'right');

    const maxOf = (axis: 'left' | 'right') => {
      let m = 0;
      series.forEach(s => { if ((s.axis ?? 'left') === axis) for (const v of s.data) if (v > m) m = v; });
      return m > 0 ? m * 1.1 : 1;
    };
    const maxL = maxOf('left'), maxR = maxOf('right');

    const data: uPlot.AlignedData = [times, ...series.map(s => s.data)] as uPlot.AlignedData;
    const barPath = uPlot.paths.bars!({ size: [0.86, Infinity], align: 0 });

    // Deploy markers — dashed red vlines under the series.
    const deployPlugin: uPlot.Plugin = {
      hooks: {
        draw: u => {
          if (!deployMarkers?.length) return;
          const ctx = u.ctx;
          ctx.save();
          ctx.strokeStyle = cssVar('var(--err)');
          ctx.globalAlpha = 0.8;
          ctx.lineWidth = 1.4 * devicePixelRatio;
          ctx.setLineDash([4 * devicePixelRatio, 3 * devicePixelRatio]);
          for (const sec of deployMarkers) {
            const x = Math.round(u.valToPos(sec, 'x', true));
            if (x < u.bbox.left || x > u.bbox.left + u.bbox.width) continue;
            ctx.beginPath();
            ctx.moveTo(x, u.bbox.top);
            ctx.lineTo(x, u.bbox.top + u.bbox.height);
            ctx.stroke();
          }
          ctx.restore();
        },
      },
    };

    const yAxis = (scale: string, side: 0 | 1, max: number, fmt: ((v: number) => string) | undefined, showGrid: boolean): uPlot.Axis => ({
      scale, side, stroke: text3, size: 38, font: '10px ui-monospace, monospace',
      grid: showGrid ? { stroke: gridc, width: 1, dash: [3, 4] } : { show: false },
      ticks: { show: false },
      splits: () => [0, max / 2, max],
      values: (_u, sp) => sp.map(v => (fmt ? fmt(v) : kfmt(v))),
    });

    const axes: uPlot.Axis[] = [
      {
        stroke: text3, grid: { show: false }, ticks: { show: false }, size: 20,
        font: '10px ui-monospace, monospace',
        // v0.8.402 (operator-reported: the Problems occurrences chart
        // showed only "12 AM / 12 PM" with no DAY) — uPlot's default
        // time axis was in charge here while every other chart uses
        // the house day-boundary formatter. fmtXTicks stamps MM-DD on
        // the first tick of each new day; space=70 thins ticks so the
        // wider date+time labels never overlap (the v0.8.58 pair).
        values: (_u, sp) => fmtXTicks(sp),
        space: 70,
      },
      yAxis('y', 0, maxL, fmtLeft, true),
    ];
    if (hasRight) axes.push(yAxis('y2', 1, maxR, fmtRight, false));

    const opts: uPlot.Options = {
      width: el.clientWidth || 320,
      height,
      cursor: {
        x: true, y: false, points: { show: true, size: 7 },
        drag: { x: !!onBrush, y: false, setScale: false },
        ...(syncKey ? { sync: { key: syncKey } } : {}),
      },
      legend: { show: false },
      scales: {
        x: { time: true },
        y: { range: [0, maxL] },
        ...(hasRight ? { y2: { range: [0, maxR] } } : {}),
      },
      axes,
      series: [
        {},
        ...series.map((s, i) => {
          const scale = s.axis === 'right' ? 'y2' : 'y';
          if (s.type === 'bar') {
            return { label: s.label, scale, stroke: colors[i], fill: colors[i], width: 0, paths: barPath, points: { show: false } } as uPlot.Series;
          }
          const base: uPlot.Series = { label: s.label, scale, stroke: colors[i], width: s.width ?? 1.8, points: { show: false } };
          if (s.type === 'area') base.fill = colors[i] + '33';
          return base;
        }),
      ],
      hooks: {
        setSelect: onBrush ? [u => {
          const w = u.select.width;
          if (w < 2) return;
          const a = u.posToVal(u.select.left, 'x');
          const b = u.posToVal(u.select.left + w, 'x');
          onBrushRef.current?.(Math.round(a * 1000), Math.round(b * 1000));
          u.setSelect({ left: 0, width: 0, top: 0, height: 0 }, false);
        }] : undefined,
        setCursor: [u => {
          const tt = ttRef.current;
          if (!tt) return;
          const idx = u.cursor.idx;
          if (idx == null || u.cursor.left == null || u.cursor.left < 0) { tt.style.display = 'none'; return; }
          // v0.8.402 — include the DAY when the chart spans more than
          // one (the axis fix's tooltip twin: a Tuesday spike and a
          // Wednesday spike otherwise read identically on hover).
          const dd = new Date(times[idx] * 1000);
          const sameDay = times.length > 1 &&
            new Date(times[0] * 1000).toDateString() === new Date(times[times.length - 1] * 1000).toDateString();
          const hm = dd.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
          const ts = sameDay ? hm
            : `${String(dd.getMonth() + 1).padStart(2, '0')}-${String(dd.getDate()).padStart(2, '0')} ${hm}`;
          const rows = series.map((s, i) => {
            const unit = s.axis === 'right' ? rightUnit : leftUnit;
            return `<div class="ov-tt-r"><span class="ov-lbl"><i class="ov-sw" style="background:${colors[i]}"></i>${s.label}</span><b>${kfmt(s.data[idx] ?? 0)}${unit}</b></div>`;
          }).join('');
          tt.innerHTML = `<div class="ov-tt-t">${ts}</div>${rows}`;
          tt.style.display = 'block';
          tt.style.left = `${u.cursor.left}px`;
          tt.style.top = `${Math.max(8, u.cursor.top ?? 20)}px`;
        }],
      },
      plugins: [deployPlugin],
    };

    plotRef.current?.destroy();
    plotRef.current = new uPlot(opts, data, el);

    const ro = new ResizeObserver(() => {
      const u = plotRef.current;
      if (!u || !el.clientWidth) return;
      u.setSize({ width: el.clientWidth, height });
    });
    ro.observe(el);

    return () => { ro.disconnect(); plotRef.current?.destroy(); plotRef.current = null; };
  }, [times, series, height, leftUnit, rightUnit, deployMarkers, onBrush, syncKey, fmtLeft, fmtRight, themeTick]);

  return (
    <div className="ov-chart-wrap" style={{ position: 'relative' }}>
      <div ref={hostRef} style={{ width: '100%' }} />
      <div ref={ttRef} className="ov-tt" style={{ display: 'none' }} />
    </div>
  );
}
