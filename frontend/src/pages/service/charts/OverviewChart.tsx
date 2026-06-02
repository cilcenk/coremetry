import { useEffect, useRef } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';

// OverviewChart (v0.7.94) — the compact RED chart for the Service Overview.
// A purpose-built uPlot wrapper matching the design handoff: ~150px, clean
// (no axes chrome beyond 0/50/100 gridlines), a dashed-purple deploy marker
// with a ▼ flag, and a hover crosshair + per-series tooltip. Replaces the
// reuse of the full MultiLineChart, which is built for full-width detail
// panels and threw a uPlot ResizeObserver/teardown race when squeezed into
// the 3-column card grid.
//
// Robustness: the ResizeObserver callback bails if the instance was
// destroyed (ref nulled on cleanup), which is what the MultiLineChart reuse
// tripped on under StrictMode's double-mount in a 0-width card.

export interface OvChartSeries {
  label: string;
  color: string;  // a CSS var() string, resolved at draw time
  data: number[];
}

interface Props {
  times: number[];            // unix seconds, ascending — shared x axis
  series: OvChartSeries[];
  height?: number;            // default 150
  mode?: 'line' | 'area';
  unit?: string;              // " ms", "%", " req/s" …
  deployAtSec?: number | null; // deploy time (unix sec) → dashed vline + flag
  deployLabel?: string;       // e.g. "v1.0.0"
}

function cssVar(v: string): string {
  // Resolve a var(--x) token to its computed value for canvas strokes.
  const m = /^var\((--[\w-]+)\)$/.exec(v.trim());
  if (!m) return v;
  return getComputedStyle(document.documentElement).getPropertyValue(m[1]).trim() || v;
}

export function OverviewChart({
  times, series, height = 150, mode = 'line', unit = '', deployAtSec = null, deployLabel = 'deploy',
}: Props) {
  const hostRef = useRef<HTMLDivElement>(null);
  const ttRef = useRef<HTMLDivElement>(null);
  const flagRef = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | null>(null);

  useEffect(() => {
    const el = hostRef.current;
    if (!el || times.length < 2 || series.length === 0) return;

    const colors = series.map(s => cssVar(s.color));
    const gridc = cssVar('var(--border)');
    const text3 = cssVar('var(--text3)');
    const purple = cssVar('var(--purple)');

    // y-max for the 0 / 50 / 100% gridlines (a touch of headroom).
    let max = 0;
    for (const s of series) for (const v of s.data) if (v > max) max = v;
    max = max > 0 ? max * 1.1 : 1;

    const data: uPlot.AlignedData = [times, ...series.map(s => s.data)] as uPlot.AlignedData;

    // Dashed-purple deploy marker, drawn under the series.
    const deployPlugin: uPlot.Plugin = {
      hooks: {
        draw: u => {
          if (deployAtSec == null) return;
          const ctx = u.ctx;
          const x = Math.round(u.valToPos(deployAtSec, 'x', true));
          if (x < u.bbox.left || x > u.bbox.left + u.bbox.width) return;
          ctx.save();
          ctx.strokeStyle = purple;
          ctx.globalAlpha = 0.8;
          ctx.lineWidth = 1.4 * devicePixelRatio;
          ctx.setLineDash([4 * devicePixelRatio, 3 * devicePixelRatio]);
          ctx.beginPath();
          ctx.moveTo(x, u.bbox.top);
          ctx.lineTo(x, u.bbox.top + u.bbox.height);
          ctx.stroke();
          ctx.restore();
        },
      },
    };

    const opts: uPlot.Options = {
      width: el.clientWidth || 320,
      height,
      cursor: { x: true, y: false, points: { show: true, size: 7 } },
      legend: { show: false },
      scales: { x: { time: true }, y: { range: [0, max] } },
      axes: [
        { stroke: text3, grid: { show: false }, ticks: { show: false }, size: 22, font: '10px ui-monospace, monospace' },
        {
          stroke: text3, size: 34, font: '10px ui-monospace, monospace',
          grid: { stroke: gridc, width: 1, dash: [3, 4] },
          ticks: { show: false },
          splits: () => [0, max / 2, max],
          values: (_u, sp) => sp.map(v => (v >= 1000 ? `${(v / 1000).toFixed(1)}k` : v.toFixed(max < 10 ? 1 : 0))),
        },
      ],
      series: [
        {},
        ...series.map((s, i) => ({
          label: s.label,
          stroke: colors[i],
          width: 1.8,
          points: { show: false },
          ...(mode === 'area'
            ? { fill: (u: uPlot, si: number) => {
                const ctx = u.ctx;
                const g = ctx.createLinearGradient(0, u.bbox.top, 0, u.bbox.top + u.bbox.height);
                g.addColorStop(0, colors[si - 1] + '47');  // ~28% alpha
                g.addColorStop(1, colors[si - 1] + '00');
                return g;
              } }
            : {}),
        })),
      ],
      hooks: {
        setCursor: [
          u => {
            const tt = ttRef.current;
            if (!tt) return;
            const idx = u.cursor.idx;
            if (idx == null || u.cursor.left == null || u.cursor.left < 0) { tt.style.display = 'none'; return; }
            const t = times[idx];
            const rows = series.map((s, i) =>
              `<div class="ov-tt-r"><span class="ov-lbl"><i class="ov-sw" style="background:${colors[i]}"></i>${s.label}</span><b>${(s.data[idx] ?? 0).toFixed(max < 10 ? 2 : 0)}${unit}</b></div>`,
            ).join('');
            const ts = new Date(t * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
            tt.innerHTML = `<div class="ov-tt-t">${ts}</div>${rows}`;
            tt.style.display = 'block';
            tt.style.left = `${u.cursor.left}px`;
            tt.style.top = `${Math.max(8, (u.cursor.top ?? 20))}px`;
          },
        ],
      },
      plugins: [deployPlugin],
    };

    plotRef.current?.destroy();
    plotRef.current = new uPlot(opts, data, el);

    // Position the ▼ deploy flag (DOM, above the canvas) at the marker x.
    const placeFlag = () => {
      const u = plotRef.current, flag = flagRef.current;
      if (!u || !flag) return;
      if (deployAtSec == null) { flag.style.display = 'none'; return; }
      const x = u.valToPos(deployAtSec, 'x', false);
      if (x < 0 || x > u.over.clientWidth) { flag.style.display = 'none'; return; }
      flag.style.display = 'block';
      flag.style.left = `${x}px`;
    };
    placeFlag();

    const ro = new ResizeObserver(() => {
      // Bail if the instance was torn down (StrictMode double-mount / unmount
      // race in a 0-width card) — calling setSize on a destroyed uPlot is
      // what threw "Cannot read properties of undefined (reading 'forEach')".
      const u = plotRef.current;
      if (!u || !el.clientWidth) return;
      u.setSize({ width: el.clientWidth, height });
      placeFlag();
    });
    ro.observe(el);

    return () => {
      ro.disconnect();
      plotRef.current?.destroy();
      plotRef.current = null;
    };
  }, [times, series, height, mode, unit, deployAtSec, deployLabel]);

  return (
    <div className="ov-chart-wrap" style={{ position: 'relative' }}>
      <div ref={hostRef} style={{ width: '100%' }} />
      <div ref={ttRef} className="ov-tt" style={{ display: 'none' }} />
      {deployAtSec != null && (
        <div ref={flagRef} className="ov-deploy-flag" style={{ top: 0, display: 'none' }}>▼ {deployLabel}</div>
      )}
    </div>
  );
}
