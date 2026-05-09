import { useEffect, useRef, useState } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import type { SpanMetricSeries } from '@/lib/types';
import { hashColor } from '@/lib/utils';

// Multi-series line chart on uPlot. Renders the same hover-
// crosshair + per-series tooltip experience as the previous
// Chart.js version, but at 5-10× the speed.
//
// Three load-bearing decisions worth knowing:
//
//   1. `scales.x.time = true` — flips the x-axis from raw
//      numbers to "HH:MM" / "MMM DD" tick formatting. Without
//      this the axis renders 1715120400-style unix seconds,
//      which is what surfaced as "broken charts" after the
//      Chart.js migration.
//
//   2. Fixed pixel height (`height` prop, default 320). The
//      previous "height: 100%" container put uPlot at 0px on
//      pages where the parent wasn't itself sized. Callers
//      that want a custom height pass it explicitly; the
//      dashboard PanelRenderer wraps in a 280px box, the
//      Explore/Metrics pages pick 320px etc.
//
//   3. Click-to-isolate is implemented WITHOUT rebuilding the
//      chart. The previous version tracked hidden indices in
//      React state and put them in the useEffect deps, so
//      every legend click destroyed and recreated the plot —
//      ~30 ms judder per click. Now a click calls
//      setSeries(idx, { show }) on the live plot, no React
//      state, no rebuild.

export function MultiLineChart({
  series, unit, height = 320,
}: { series: SpanMetricSeries[]; unit?: string; height?: number }) {
  const containerRef = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | null>(null);

  // Mirror of the live "this index is currently visible" set.
  // Kept in a ref instead of useState so the click handler can
  // toggle without triggering a re-render of the React tree
  // around the chart.
  const visibleRef = useRef<boolean[]>([]);

  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    plotRef.current?.destroy();
    plotRef.current = null;
    visibleRef.current = series.map(() => true);
    if (series.length === 0) return;

    // Unified time axis (union of bucket timestamps across
    // every series). uPlot consumes this as a single x array
    // shared by all y arrays.
    const allTimes = new Set<number>();
    series.forEach(s => s.points.forEach(p => allTimes.add(p.time)));
    const times = [...allTimes].sort((a, b) => a - b);
    const xs = times.map(t => t / 1e9); // ns → unix seconds

    // Per-series y values aligned to the union x axis. Missing
    // points become null → uPlot draws a gap.
    const ySeries: (number | null)[][] = series.map(s => {
      const valByTime = new Map(s.points.map(p => [p.time, p.value]));
      return times.map(t => valByTime.get(t) ?? null);
    });

    const css = getComputedStyle(document.documentElement);
    const text3 = css.getPropertyValue('--text3').trim() || '#484f58';
    const grid  = css.getPropertyValue('--bg2').trim()   || '#21262d';

    const data: uPlot.AlignedData = [xs, ...ySeries] as uPlot.AlignedData;

    const opts: uPlot.Options = {
      // clientWidth is sometimes 0 at first paint (StrictMode
      // double-mount or display:none parent). Fall back to
      // 600px so the initial render still produces a visible
      // canvas; the ResizeObserver below corrects the width on
      // the next layout pass.
      width: el.clientWidth || 600,
      height,
      // Tells uPlot the x values are unix seconds → "HH:MM"
      // tick format instead of raw integers. Single most
      // important option for time-series charts.
      scales: { x: { time: true } },
      series: [
        {},
        ...series.map((s) => {
          const label = s.groupKey.length ? s.groupKey.join(' / ') : 'value';
          const color = hashColor(label);
          return {
            label,
            stroke: color,
            fill: color + '22',
            width: 2,
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
      legend: { show: true, live: true, markers: { width: 2 } },
      hooks: {
        // Grafana-style hover tooltip — the floating panel
        // matches what Datadog / Honeycomb / Grafana all do
        // (hover anywhere on the chart and see all series'
        // values at that x). uPlot's native legend already
        // updates the bottom table with `live: true`, but
        // operators expect the panel to show up next to the
        // cursor so they don't have to look down. Single DOM
        // mutation per move event, no overlay canvas.
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
            if (xVal == null) {
              tip.style.opacity = '0';
              return;
            }
            // Format X (unix seconds → HH:MM:SS).
            const d = new Date((xVal as number) * 1000);
            const hh = d.getHours().toString().padStart(2, '0');
            const mm = d.getMinutes().toString().padStart(2, '0');
            const ss = d.getSeconds().toString().padStart(2, '0');
            // Build per-series rows. Skip null values so
            // gaps in one series don't push down rows from
            // others. Sort by value desc so the hottest
            // series shows on top — same behaviour we had
            // in the Chart.js tooltip.
            type Row = { label: string; color: string; v: number };
            const rows: Row[] = [];
            for (let i = 0; i < series.length; i++) {
              const yArr = u.data[i + 1];
              if (!yArr) continue;
              const v = yArr[idx];
              if (v == null) continue;
              const s = series[i];
              const label = s.groupKey.length ? s.groupKey.join(' / ') : 'value';
              rows.push({ label, color: hashColor(label), v: v as number });
            }
            if (rows.length === 0) {
              tip.style.opacity = '0';
              return;
            }
            rows.sort((a, b) => b.v - a.v);
            const fmt = (n: number) =>
              `${n.toFixed(3)}${unit ? ' ' + unit : ''}`;
            tip.innerHTML =
              `<div style="font-weight:600;margin-bottom:4px">${hh}:${mm}:${ss}</div>` +
              rows.map(r =>
                `<div style="display:flex;gap:8px;align-items:center;line-height:1.5">` +
                  `<span style="display:inline-block;width:8px;height:8px;background:${r.color};border-radius:2px;flex-shrink:0"></span>` +
                  `<span style="flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:240px" title="${r.label}">${r.label}</span>` +
                  `<span style="font-family:ui-monospace,monospace;font-variant-numeric:tabular-nums">${fmt(r.v)}</span>` +
                `</div>`,
              ).join('');
            tip.style.opacity = '1';
            // Position: by default 12px right + below the
            // cursor. If the tooltip would clip past the
            // right edge, flip to the left side. Same flip
            // for the bottom edge so the tooltip never goes
            // outside the chart canvas.
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

    plotRef.current = new uPlot(opts, data, el);

    const ro = new ResizeObserver(() => {
      if (plotRef.current && el) {
        // height stays fixed (the prop). Width tracks the
        // parent — sidebar collapse, browser resize, dashboard
        // panel re-grid all flow through here without a
        // chart rebuild.
        plotRef.current.setSize({ width: el.clientWidth, height });
      }
    });
    ro.observe(el);

    return () => {
      ro.disconnect();
      plotRef.current?.destroy();
      plotRef.current = null;
    };
  }, [series, unit, height]);

  // Click-to-isolate: hide every other series on first click,
  // restore all on second. We bypass React state — toggling
  // the live plot's visibility via setSeries(idx, {show})
  // re-renders the canvas in <1ms without rebuilding.
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    const onClick = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      const li = target.closest('.u-legend tr.u-series') as HTMLElement | null;
      if (!li || !plotRef.current) return;
      // The first <tr> in the legend is the x-axis row; data
      // series start at index 1. Subtract 1 to land on a real
      // series.
      const dataIdx = Array.from(li.parentElement?.children ?? []).indexOf(li) - 1;
      if (dataIdx < 0) return;
      e.stopPropagation();
      e.preventDefault();

      const u = plotRef.current;
      const visible = visibleRef.current;
      // Decide: are we currently in "all visible" or "isolated
      // to one"? If the clicked one is the only visible (or
      // the click is on a hidden series), we're in isolation
      // territory and the next click restores everything.
      const onlyThisVisible = visible[dataIdx]
        && visible.every((v, i) => i === dataIdx ? true : !v);
      if (onlyThisVisible) {
        for (let i = 0; i < series.length; i++) {
          visible[i] = true;
          u.setSeries(i + 1, { show: true });
        }
      } else {
        for (let i = 0; i < series.length; i++) {
          const show = i === dataIdx;
          visible[i] = show;
          u.setSeries(i + 1, { show });
        }
      }
    };
    el.addEventListener('click', onClick, true);
    return () => el.removeEventListener('click', onClick, true);
  }, [series.length]);

  // Container does NOT pin its height — uPlot creates a canvas
  // of `height` pixels plus a legend table beneath it, and we
  // want the component to take whatever total vertical space
  // chart+legend need.
  //
  // The .uplot-tooltip child is the Grafana-style floating
  // panel updated by the setCursor hook above. opacity:0 by
  // default; the hook flips it on when the cursor enters the
  // chart and updates content + position on every move.
  return (
    <div ref={containerRef} style={{
      position: 'relative', width: '100%',
    }}>
      <div className="uplot-tooltip" style={{
        // Theme-aware tokens — the previous hardcoded
        // rgba(20,24,30) painted dark on dark in dark mode
        // and dark-on-light (text invisible) in light mode.
        // var(--bg2) + var(--text) flip with [data-theme].
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
