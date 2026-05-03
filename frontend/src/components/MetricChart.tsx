'use client';
import { useEffect, useRef } from 'react';
import {
  Chart, LineController, LineElement, PointElement, LinearScale,
  CategoryScale, Tooltip, Legend, Filler,
} from 'chart.js';
import type { MetricPoint } from '@/lib/types';

Chart.register(LineController, LineElement, PointElement, LinearScale, CategoryScale, Tooltip, Legend, Filler);

export function MetricChart({ name, points }: { name: string; points: MetricPoint[] }) {
  const ref = useRef<HTMLCanvasElement>(null);
  const chartRef = useRef<Chart | null>(null);

  useEffect(() => {
    if (!ref.current) return;
    chartRef.current?.destroy();
    const css = getComputedStyle(document.documentElement);
    const text2 = css.getPropertyValue('--text2').trim() || '#8b949e';
    const text3 = css.getPropertyValue('--text3').trim() || '#484f58';
    const grid  = css.getPropertyValue('--bg2').trim()   || '#21262d';

    chartRef.current = new Chart(ref.current, {
      type: 'line',
      data: {
        labels: points.map(p => new Date(p.time / 1e6).toLocaleTimeString()),
        datasets: [{
          label: name,
          data: points.map(p => p.value),
          borderColor: '#388bfd',
          backgroundColor: 'rgba(56,139,253,0.10)',
          borderWidth: 2,
          pointRadius: points.length > 100 ? 0 : 3,
          fill: true,
          tension: 0.3,
        }],
      },
      options: {
        responsive: true,
        animation: false,
        interaction: { mode: 'index', intersect: false, axis: 'x' },
        hover:       { mode: 'index', intersect: false },
        plugins: {
          legend: { labels: { color: text2 } },
          tooltip: {
            mode: 'index', intersect: false,
            backgroundColor: 'rgba(13,17,23,0.92)',
            titleColor: '#e6edf3', bodyColor: '#e6edf3',
            borderColor: 'rgba(56,139,253,0.45)', borderWidth: 1, padding: 8,
            callbacks: { label: ctx => `${ctx.dataset.label}: ${(ctx.parsed.y as number).toFixed(4)}` },
          },
        },
        elements: { point: { hoverRadius: 4, hoverBorderWidth: 2 } },
        scales: {
          x: { ticks: { color: text3, maxTicksLimit: 10 }, grid: { color: grid } },
          y: { ticks: { color: text3 }, grid: { color: grid } },
        },
      },
    });
    return () => { chartRef.current?.destroy(); };
  }, [name, points]);

  return <canvas ref={ref} height={300} />;
}
