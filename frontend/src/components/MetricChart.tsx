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

  // Container width-only — the canvas is `height` pixels plus
  // an inline legend below, total component height = canvas +
  // legend. Pinning the container to `height` clipped (or
  // overflowed) the legend; letting it grow naturally keeps
  // the layout stable.
  return <div ref={containerRef} style={{ width: '100%' }} />;
}
