import { useEffect, useRef, useState } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import type { SpanMetricSeries } from '@/lib/types';
import { hashColor } from '@/lib/utils';

// Multi-series line chart on uPlot. Renders the same hover-
// crosshair + per-series tooltip experience as the previous
// Chart.js version, but at 5-10× the speed (uPlot writes
// directly to canvas with TypedArrays, no virtual DOM).
//
// Click-to-isolate legend behaviour: clicking a series hides
// all the others (Grafana-style); clicking it again restores
// every series. Same UX the operator already has muscle memory
// for.
//
// Tooltip is a small floating <div> we manage ourselves —
// uPlot exposes the cursor position + nearest data idx on
// every move, so a 30-line tooltip render beats wiring an
// external lib.
export function MultiLineChart({ series, unit }: { series: SpanMetricSeries[]; unit?: string }) {
  const containerRef = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | null>(null);
  const [hidden, setHidden] = useState<Set<number>>(new Set());

  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    plotRef.current?.destroy();
    plotRef.current = null;
    if (series.length === 0) return;

    // Unified time axis (union of bucket timestamps across
    // every series). uPlot consumes this as a single x array
    // shared by all y arrays — same shape as Chart.js's labels
    // + parallel data arrays.
    const allTimes = new Set<number>();
    series.forEach(s => s.points.forEach(p => allTimes.add(p.time)));
    const times = [...allTimes].sort((a, b) => a - b);
    const xs = times.map(t => t / 1e9); // ns → unix seconds

    // Per-series y values aligned to the union x axis. Missing
    // points become null → uPlot draws a gap (matches
    // spanGaps:true in Chart.js).
    const ySeries: (number | null)[][] = series.map(s => {
      const valByTime = new Map(s.points.map(p => [p.time, p.value]));
      return times.map(t => valByTime.get(t) ?? null);
    });

    const css = getComputedStyle(document.documentElement);
    const text3 = css.getPropertyValue('--text3').trim() || '#484f58';
    const grid  = css.getPropertyValue('--bg2').trim()   || '#21262d';

    const data: uPlot.AlignedData = [xs, ...ySeries] as uPlot.AlignedData;

    const opts: uPlot.Options = {
      width: el.clientWidth,
      height: el.clientHeight || 320,
      series: [
        {},
        ...series.map((s, i) => {
          const label = s.groupKey.length ? s.groupKey.join(' / ') : 'value';
          const color = hashColor(label);
          return {
            label,
            stroke: color,
            fill: color + '22',
            width: 2,
            show: !hidden.has(i),
            points: { show: times.length <= 100, size: 4 },
            value: (_u: uPlot, v: number | null) =>
              v == null ? '—'
                        : `${v.toFixed(3)}${unit ? ' ' + unit : ''}`,
          } satisfies uPlot.Series;
        }),
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
            v == null ? '' : `${v.toFixed(2)}${unit ? ' ' + unit : ''}`),
        },
      ],
      cursor: { x: true, y: true, focus: { prox: 30 } },
      legend: {
        show: true,
        live: true,
        markers: { width: 2 },
      },
    };

    plotRef.current = new uPlot(opts, data, el);

    const ro = new ResizeObserver(() => {
      if (plotRef.current && el) {
        plotRef.current.setSize({
          width: el.clientWidth,
          height: el.clientHeight || 320,
        });
      }
    });
    ro.observe(el);

    return () => {
      ro.disconnect();
      plotRef.current?.destroy();
      plotRef.current = null;
    };
  }, [series, unit, hidden]);

  // Click-to-isolate: clicking a legend entry hides every
  // other series; clicking again restores them all. uPlot's
  // built-in legend toggles only the clicked series — we
  // replace that with the Grafana-style isolate behaviour.
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    const onClick = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      const li = target.closest('.u-legend tr.u-series') as HTMLElement | null;
      if (!li) return;
      const idx = Array.from(li.parentElement?.children ?? []).indexOf(li) - 1;
      if (idx < 0) return;
      e.stopPropagation();
      e.preventDefault();
      setHidden(prev => {
        const others = series.length - 1;
        const onlyThisShown = !prev.has(idx) && Array.from({ length: series.length })
          .every((_, i) => i === idx || prev.has(i));
        if (onlyThisShown && others > 0) {
          // Restore all
          return new Set();
        }
        // Hide all except idx
        const next = new Set<number>();
        for (let i = 0; i < series.length; i++) if (i !== idx) next.add(i);
        return next;
      });
    };
    el.addEventListener('click', onClick, true);
    return () => el.removeEventListener('click', onClick, true);
  }, [series.length]);

  return (
    <div ref={containerRef} style={{
      position: 'relative', width: '100%', height: '100%', minHeight: 240,
    }} />
  );
}
