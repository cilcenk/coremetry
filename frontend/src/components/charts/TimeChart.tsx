import { useEffect, useMemo, useRef } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { useThemeTick } from '@/lib/useThemeTick';
import { fmtXTicks } from '@/lib/chartFmt';
import { timeChartBuildSignature } from '@/lib/chartBuildSig';

// TimeChart (v0.8.91) — the ONE time-series primitive. Generalises the proven
// OverviewChart uPlot wrapper (Canvas, so it also lands the "charts to Canvas"
// perf goal): per-series type (bar | line | area), an optional right (dual)
// axis, drag-to-brush a time range, deploy markers, a hover crosshair +
// per-series tooltip, and cross-chart cursor sync. Every time-series chart
// renders through this — no more hand-drawn axes/gridlines/hover.
//
// v0.8.531 (perf #5/#15) — rebuild-vs-setData split. The build effect used to
// key on `times`/`series`/`fmt*`, so a 30s poll (or ANY parent render, since
// VolumeChart hands a fresh `fmtRight` arrow each time) destroyed + recreated
// the uPlot: canvas flicker, dropped hover cursor, reset brush. Now the build
// effect keys on a pure STRUCTURE signature (series shape + units + overlays +
// callback PRESENCE) and the theme tick; a data-only refresh rides
// u.setData(). Live callbacks (onBrush / fmtLeft / fmtRight / fmtX) are read
// through refs so a fresh arrow never churns a rebuild. The y range + splits
// re-fit from the LIVE scale (not a build-time constant) so setData re-scales
// the axis exactly as the old rebuild did.

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
  // Optional x-tick formatter override (unix seconds → label). Default
  // stays the house day-boundary formatter (fmtXTicks); the Problems
  // detail passes its windowed rule (problemTime.fmtHistTick) here.
  fmtX?: (tsSec: number) => string;
}

function cssVar(v: string): string {
  const m = /^var\((--[\w-]+)\)$/.exec(v.trim());
  if (!m) return v;
  return getComputedStyle(document.documentElement).getPropertyValue(m[1]).trim() || v;
}
const kfmt = (v: number) => (v >= 1000 ? `${(v / 1000).toFixed(1)}k` : v.toFixed(v < 10 && v % 1 !== 0 ? 1 : 0));

// y range derived from the LIVE data extremes (0-based, 10% headroom, floor 1)
// — reproduces the old maxOf() constant but as a function so u.setData() re-fits
// the axis on the data fast-path instead of pinning a stale build-time max.
function yRange(_u: uPlot, _min: number, max: number): [number, number] {
  return [0, max > 0 ? max * 1.1 : 1];
}

export function TimeChart({
  times, series, height = 150, leftUnit = '', rightUnit = '',
  deployMarkers, onBrush, syncKey, fmtLeft, fmtRight, fmtX,
}: Props) {
  const hostRef = useRef<HTMLDivElement>(null);
  const ttRef = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | null>(null);
  // Live callbacks + formatters held in refs (v0.8.531): the once-per-build
  // hooks / axis formatters read `.current`, so a caller passing a fresh arrow
  // each render (VolumeChart's inline `fmtRight`) never churns a rebuild — only
  // its PRESENCE (which flips an axis path or the brush affordance) is tracked
  // in the build signature.
  const onBrushRef = useRef(onBrush); onBrushRef.current = onBrush;
  const fmtLeftRef = useRef(fmtLeft); fmtLeftRef.current = fmtLeft;
  const fmtRightRef = useRef(fmtRight); fmtRightRef.current = fmtRight;
  const fmtXRef = useRef(fmtX); fmtXRef.current = fmtX;
  const themeTick = useThemeTick();

  // uPlot's aligned data — memoised on the DATA inputs only so a poll recomputes
  // it once and either seeds a rebuild (structure changed) or rides setData.
  const chartData = useMemo<uPlot.AlignedData>(
    () => [times, ...series.map(s => s.data)] as uPlot.AlignedData,
    [times, series],
  );
  const dataRef = useRef(chartData); dataRef.current = chartData;

  // Build signature — everything that forces a full re-create. Series POINT
  // VALUES + the x `times` array are absent (they ride setData). `renderable`
  // (≥2 x points) flips the sig so an empty→data first paint creates the plot.
  const buildSig = timeChartBuildSignature({
    series,
    height, leftUnit, rightUnit, deployMarkers, syncKey,
    hasBrush: !!onBrush, hasFmtLeft: !!fmtLeft, hasFmtRight: !!fmtRight, hasFmtX: !!fmtX,
    renderable: times.length >= 2 && series.length > 0,
  });

  // Build / re-create effect — runs only when the structure signature or the
  // theme flips, NOT on a data-only poll.
  useEffect(() => {
    const el = hostRef.current;
    if (!el || times.length < 2 || series.length === 0) return;

    const colors = series.map(s => cssVar(s.color));
    const gridc = cssVar('var(--border)');
    const text3 = cssVar('var(--text3)');
    const hasRight = series.some(s => s.axis === 'right');

    const barPath = uPlot.paths.bars!({ size: [0.86, Infinity], align: 0 });

    // Deploy markers — dashed red vlines under the series. Drawn in a hook so
    // they re-paint on every redraw (incl. setData), tracking the live x-scale.
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

    // Axis whose ticks/splits derive from the LIVE scale max so a setData
    // re-fit updates the gridlines (the old build-time `max` closure would go
    // stale on the fast-path). fmt read through its ref for live formatting.
    const yAxis = (scale: string, side: 0 | 1, fmtRef: React.MutableRefObject<((v: number) => string) | undefined>, showGrid: boolean): uPlot.Axis => ({
      scale, side, stroke: text3, size: 38, font: '10px ui-monospace, monospace',
      grid: showGrid ? { stroke: gridc, width: 1, dash: [3, 4] } : { show: false },
      ticks: { show: false },
      splits: u => { const mx = (u.scales[scale].max ?? 1); return [0, mx / 2, mx]; },
      values: (_u, sp) => sp.map(v => (fmtRef.current ? fmtRef.current(v) : kfmt(v))),
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
        values: (_u, sp) => (fmtXRef.current ? sp.map(fmtXRef.current) : fmtXTicks(sp)),
        space: fmtX ? 90 : 70,
      },
      yAxis('y', 0, fmtLeftRef, true),
    ];
    if (hasRight) axes.push(yAxis('y2', 1, fmtRightRef, false));

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
        y: { range: yRange },
        ...(hasRight ? { y2: { range: yRange } } : {}),
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
          // Read x + values LIVE from u.data — the setData fast-path may have
          // swapped them without rebuilding this closure. series labels/colours/
          // axis are structural (rebuild on change), so closing over them is safe.
          const xs = u.data[0] as number[];
          const tSec = xs[idx] as number;
          if (tSec == null) { tt.style.display = 'none'; return; }
          // v0.8.402 — include the DAY when the chart spans more than
          // one (the axis fix's tooltip twin: a Tuesday spike and a
          // Wednesday spike otherwise read identically on hover).
          const dd = new Date(tSec * 1000);
          const sameDay = xs.length > 1 &&
            new Date((xs[0] as number) * 1000).toDateString() === new Date((xs[xs.length - 1] as number) * 1000).toDateString();
          const hm = dd.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
          const ts = sameDay ? hm
            : `${String(dd.getMonth() + 1).padStart(2, '0')}-${String(dd.getDate()).padStart(2, '0')} ${hm}`;
          const rows = series.map((s, i) => {
            const unit = s.axis === 'right' ? rightUnit : leftUnit;
            const v = (u.data[i + 1] as (number | null)[])?.[idx] ?? 0;
            return `<div class="ov-tt-r"><span class="ov-lbl"><i class="ov-sw" style="background:${colors[i]}"></i>${s.label}</span><b>${kfmt(v ?? 0)}${unit}</b></div>`;
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
    plotRef.current = new uPlot(opts, dataRef.current, el);

    const ro = new ResizeObserver(() => {
      const u = plotRef.current;
      if (!u || !el.clientWidth) return;
      u.setSize({ width: el.clientWidth, height });
    });
    ro.observe(el);

    return () => { ro.disconnect(); plotRef.current?.destroy(); plotRef.current = null; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [buildSig, themeTick]);

  // Data fast-path (v0.8.531) — a poll changes only `times` + the series' point
  // VALUES, not their shape/units/overlays, so buildSig is unchanged and the
  // build effect above does NOT run. Push the fresh aligned data into the live
  // plot with setData() (resetScales default true → the range functions re-fit
  // both y axes) instead of destroy()+new. Guard on column count: a series
  // add/remove is a rebuild (which owns the data this commit); setData with a
  // mismatched width would throw.
  useEffect(() => {
    const u = plotRef.current;
    if (!u) return;
    if (u.data.length !== chartData.length) return;
    u.setData(chartData);
  }, [chartData]);

  return (
    <div className="ov-chart-wrap" style={{ position: 'relative' }}>
      <div ref={hostRef} style={{ width: '100%' }} />
      <div ref={ttRef} className="ov-tt" style={{ display: 'none' }} />
    </div>
  );
}
