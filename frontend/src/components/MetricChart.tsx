import { useEffect, useRef } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import type { MetricPoint } from '@/lib/types';

// uPlot single-series chart. Replaces Chart.js for the metric
// drill-down view. Three load-bearing options:
//
//   1. scales.x.time = true → x ticks formatted as HH:MM
//      instead of raw unix seconds. Without this the axis
//      reads "1715120400" — what surfaced as "broken" charts
//      after the original migration.
//
//   2. width: el.clientWidth || 600 → first paint always has
//      something to render even when the parent is briefly
//      0-width (StrictMode double-mount, display:none parent
//      transition). ResizeObserver corrects on the next
//      layout pass.
//
//   3. height stays fixed (default 300, prop override). The
//      previous shape used "height: 100%" which collapsed to
//      0 on pages that didn't stretch their parent.

export function MetricChart({
  name, points, height = 300,
}: { name: string; points: MetricPoint[]; height?: number }) {
  const containerRef = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | null>(null);

  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;

    plotRef.current?.destroy();
    plotRef.current = null;

    if (points.length === 0) return;

    const css = getComputedStyle(document.documentElement);
    const text3 = css.getPropertyValue('--text3').trim() || '#484f58';
    const grid  = css.getPropertyValue('--bg2').trim()   || '#21262d';

    const xs: number[] = new Array(points.length);
    const ys: number[] = new Array(points.length);
    for (let i = 0; i < points.length; i++) {
      xs[i] = points[i].time / 1e9;  // ns → unix seconds
      ys[i] = points[i].value;
    }

    const opts: uPlot.Options = {
      width: el.clientWidth || 600,
      height,
      scales: { x: { time: true } },
      series: [
        {},
        {
          label: name,
          stroke: '#388bfd',
          fill: 'rgba(56,139,253,0.10)',
          width: 2,
          points: { show: points.length <= 100, size: 5 },
        },
      ],
      axes: [
        {
          stroke: text3,
          grid: { stroke: grid, width: 1 },
          ticks: { stroke: grid, width: 1 },
        },
        {
          stroke: text3,
          grid: { stroke: grid, width: 1 },
          ticks: { stroke: grid, width: 1 },
          values: (_u, splits) => splits.map(v =>
            v == null ? '' :
            Math.abs(v) >= 100 ? v.toFixed(0) :
            Math.abs(v) >= 1   ? v.toFixed(2) :
                                 v.toFixed(4)),
        },
      ],
      cursor: { x: true, y: true, focus: { prox: 30 } },
      legend: { show: true, live: true, markers: { width: 2 } },
      hooks: {
        // Floating tooltip near the cursor — same UX as
        // MultiLineChart (Grafana / Datadog style). Single
        // series so the panel just shows one row.
        setCursor: [
          (u) => {
            const tip = el.querySelector('.uplot-tooltip') as HTMLDivElement | null;
            if (!tip) return;
            const idx = u.cursor.idx;
            if (idx == null || idx < 0) {
              tip.style.opacity = '0';
              return;
            }
            const xVal = u.data[0][idx];
            const yVal = u.data[1] ? u.data[1][idx] : null;
            if (xVal == null || yVal == null) {
              tip.style.opacity = '0';
              return;
            }
            const d = new Date((xVal as number) * 1000);
            const hh = d.getHours().toString().padStart(2, '0');
            const mm = d.getMinutes().toString().padStart(2, '0');
            const ss = d.getSeconds().toString().padStart(2, '0');
            const valStr =
              Math.abs(yVal as number) >= 100 ? (yVal as number).toFixed(0) :
              Math.abs(yVal as number) >= 1   ? (yVal as number).toFixed(2) :
                                                 (yVal as number).toFixed(4);
            tip.innerHTML =
              `<div style="font-weight:600;margin-bottom:2px">${hh}:${mm}:${ss}</div>` +
              `<div style="display:flex;gap:8px;align-items:center">` +
                `<span style="display:inline-block;width:8px;height:8px;background:#388bfd;border-radius:2px"></span>` +
                `<span style="flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="${name}">${name}</span>` +
                `<span style="font-family:ui-monospace,monospace;font-variant-numeric:tabular-nums">${valStr}</span>` +
              `</div>`;
            tip.style.opacity = '1';
            const cw = el.clientWidth;
            const tw = tip.offsetWidth;
            const th = tip.offsetHeight;
            const left = u.cursor.left ?? 0;
            const top  = u.cursor.top  ?? 0;
            const x = left + 12 + tw > cw ? left - tw - 12 : left + 12;
            const y = top + 12 + th > height ? top - th - 12 : top + 12;
            tip.style.left = `${Math.max(0, x)}px`;
            tip.style.top  = `${Math.max(0, y)}px`;
          },
        ],
      },
    };

    plotRef.current = new uPlot(opts, [xs, ys], el);

    const ro = new ResizeObserver(() => {
      if (plotRef.current && el) {
        plotRef.current.setSize({ width: el.clientWidth, height });
      }
    });
    ro.observe(el);

    return () => {
      ro.disconnect();
      plotRef.current?.destroy();
      plotRef.current = null;
    };
  }, [name, points, height]);

  // Container width-only; uPlot owns the canvas height. The
  // .uplot-tooltip child is updated by the setCursor hook
  // above (Grafana-style floating value panel).
  return (
    <div ref={containerRef} style={{ position: 'relative', width: '100%' }}>
      <div className="uplot-tooltip" style={{
        // Theme-aware (var(--bg2)/--text/--border) so the panel
        // is readable in both dark and light modes.
        position: 'absolute', pointerEvents: 'none',
        background: 'var(--bg2)',
        border: '1px solid var(--border)',
        borderRadius: 4,
        padding: '8px 10px',
        fontSize: 11, color: 'var(--text)',
        opacity: 0, transition: 'opacity .08s',
        zIndex: 5,
        boxShadow: '0 4px 14px rgba(0,0,0,0.35)',
        maxWidth: 320,
      }} />
    </div>
  );
}
