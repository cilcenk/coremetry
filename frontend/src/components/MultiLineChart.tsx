'use client';
import { useEffect, useRef } from 'react';
import {
  Chart, LineController, LineElement, PointElement, LinearScale,
  CategoryScale, Tooltip, Legend, Filler,
} from 'chart.js';
import type { Plugin } from 'chart.js';
import type { SpanMetricSeries } from '@/lib/types';
import { hashColor } from '@/lib/utils';

Chart.register(LineController, LineElement, PointElement, LinearScale, CategoryScale, Tooltip, Legend, Filler);

// Vertical crosshair line at the active hover position — paints a dashed
// rule under the tooltip so the user can read off the X value precisely.
const crosshairPlugin: Plugin<'line'> = {
  id: 'qmetry-crosshair',
  afterDatasetsDraw(chart) {
    const tt = chart.tooltip;
    if (!tt || !tt.opacity) return;
    const x = tt.caretX;
    const top = chart.chartArea.top;
    const bottom = chart.chartArea.bottom;
    const ctx = chart.ctx;
    ctx.save();
    ctx.beginPath();
    ctx.setLineDash([4, 4]);
    ctx.strokeStyle = 'rgba(120,160,255,0.55)';
    ctx.lineWidth = 1;
    ctx.moveTo(x, top);
    ctx.lineTo(x, bottom);
    ctx.stroke();
    ctx.restore();
  },
};
Chart.register(crosshairPlugin);

export function MultiLineChart({ series, unit }: { series: SpanMetricSeries[]; unit?: string }) {
  const ref = useRef<HTMLCanvasElement>(null);
  const chartRef = useRef<Chart | null>(null);

  useEffect(() => {
    if (!ref.current) return;
    chartRef.current?.destroy();

    // Build a unified time axis (union of all bucket times)
    const allTimes = new Set<number>();
    series.forEach(s => s.points.forEach(p => allTimes.add(p.time)));
    const times = [...allTimes].sort((a, b) => a - b);
    const labels = times.map(t => new Date(t / 1e6).toLocaleTimeString());

    const datasets = series.map(s => {
      const valByTime = new Map(s.points.map(p => [p.time, p.value]));
      const label = s.groupKey.length ? s.groupKey.join(' / ') : 'value';
      const color = hashColor(label);
      return {
        label,
        data: times.map(t => valByTime.get(t) ?? null),
        borderColor: color,
        backgroundColor: color + '22',
        borderWidth: 2,
        pointRadius: times.length > 100 ? 0 : 2,
        spanGaps: true,
        tension: 0.25,
      };
    });

    const css = getComputedStyle(document.documentElement);
    const text2 = css.getPropertyValue('--text2').trim() || '#8b949e';
    const text3 = css.getPropertyValue('--text3').trim() || '#484f58';
    const grid  = css.getPropertyValue('--bg2').trim()   || '#21262d';

    chartRef.current = new Chart(ref.current, {
      type: 'line',
      data: { labels, datasets },
      options: {
        responsive: true, animation: false, maintainAspectRatio: false,
        // Hover anywhere along an X column → all series light up + tooltip
        // lists every series's value at that timestamp (crosshair experience).
        interaction: { mode: 'index', intersect: false, axis: 'x' },
        hover:       { mode: 'index', intersect: false },
        plugins: {
          legend: { labels: { color: text2, boxWidth: 12 }, position: 'bottom' as const },
          tooltip: {
            mode: 'index', intersect: false,
            backgroundColor: 'rgba(13,17,23,0.92)',
            titleColor: '#e6edf3',
            bodyColor: '#e6edf3',
            borderColor: 'rgba(56,139,253,0.45)',
            borderWidth: 1,
            padding: 8,
            // Sort biggest value first within the tooltip — easier to scan
            itemSort: (a, b) => (b.parsed.y as number) - (a.parsed.y as number),
            callbacks: {
              label: ctx => `${ctx.dataset.label}: ${(ctx.parsed.y as number)?.toFixed(3)}${unit ? ' ' + unit : ''}`,
            },
          },
        },
        // Show a small filled point on the hovered series so the line is
        // easy to follow even when pointRadius is 0.
        elements: { point: { hoverRadius: 4, hoverBorderWidth: 2 } },
        scales: {
          x: { ticks: { color: text3, maxTicksLimit: 12 }, grid: { color: grid } },
          y: {
            ticks: { color: text3,
              callback: v => `${(v as number).toFixed(2)}${unit ? ' ' + unit : ''}`,
            },
            grid: { color: grid },
            beginAtZero: true,
          },
        },
      },
    });
    return () => { chartRef.current?.destroy(); };
  }, [series, unit]);

  return <div style={{ height: 360 }}><canvas ref={ref} /></div>;
}
