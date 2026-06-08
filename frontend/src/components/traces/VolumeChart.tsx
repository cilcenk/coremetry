// VolumeChart — Span Volume, now a thin adapter over the shared <TimeChart>
// primitive (v0.8.91; was a hand-drawn DOM/SVG chart). Keeps its identity (card
// + legend above the bars + "spans / Nm bucket" note) but delegates the axes,
// gridlines, hover crosshair/tooltip, deploy-free brush and Canvas rendering to
// TimeChart. ok-span bars (accent) with the error share overlaid red at the
// bottom + a p50 latency line on the right axis. Drag to brush a time range.

import { useMemo } from 'react';
import type { SpanMetricSeries } from '@/lib/types';
import { TimeChart, type TimeChartSeries } from '@/components/charts/TimeChart';
import { statusColor } from '@/lib/statusColor';

export function VolumeChart({
  count, errors, p50, height = 140, onBrush,
}: {
  count: SpanMetricSeries[] | null;
  errors: SpanMetricSeries[] | null;
  p50: SpanMetricSeries[] | null;
  height?: number;
  onBrush?: (fromMs: number, toMs: number) => void;
}) {
  const { times, series, bucketMin } = useMemo(() => {
    const cPts = count?.[0]?.points ?? [];
    if (!cPts.length) return { times: [] as number[], series: [] as TimeChartSeries[], bucketMin: 1 };
    const eMap = new Map((errors?.[0]?.points ?? []).map(p => [p.time, p.value]));
    const pMap = new Map((p50?.[0]?.points ?? []).map(p => [p.time, p.value]));
    const t = cPts.map(p => Math.round(p.time / 1e9)); // ns → unix sec
    const total = cPts.map(p => p.value);
    const err = cPts.map(p => Math.min(eMap.get(p.time) ?? 0, p.value));
    const p50d = cPts.map(p => pMap.get(p.time) ?? 0);
    const dt = t.length > 1 ? Math.round((t[1] - t[0]) / 60) : 1;
    // Draw order = overlay order: full bar (accent) first, then the error
    // share (red) on top so it reads at the bottom of each bar; p50 line last.
    const s: TimeChartSeries[] = [
      { key: 'total', label: 'ok spans', data: total, color: 'var(--accent)', type: 'bar', axis: 'left' },
      { key: 'error', label: 'errors', data: err, color: statusColor('error'), type: 'bar', axis: 'left' },
      { key: 'p50', label: 'p50 latency', data: p50d, color: 'var(--orange)', type: 'line', axis: 'right', width: 1.6 },
    ];
    return { times: t, series: s, bucketMin: Math.max(1, dt) };
  }, [count, errors, p50]);

  return (
    <div style={{ background: 'var(--bg2)', border: '1px solid var(--border)', borderRadius: 8, padding: 12, marginBottom: 10 }}>
      {/* Legend — ABOVE the plot. Colour only for status (errors); ok = accent. */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 14, marginBottom: 8, fontSize: 10.5, color: 'var(--text-faint)' }}>
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
          <span style={{ width: 9, height: 9, borderRadius: 2, background: 'var(--accent)' }} /> ok spans
        </span>
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
          <span style={{ width: 9, height: 9, borderRadius: 2, background: statusColor('error') }} /> errors
        </span>
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
          <span style={{ width: 14, height: 2, background: 'var(--orange)' }} /> p50 latency
        </span>
        <span style={{ marginLeft: 'auto', fontFamily: 'var(--font-mono, ui-monospace)' }}>spans / {bucketMin}m bucket · sürükle = zaman seç</span>
      </div>

      {times.length === 0 ? (
        <div style={{ height, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--text-faint)', fontSize: 12 }}>
          No traces in view to bucket.
        </div>
      ) : (
        <TimeChart
          times={times}
          series={series}
          height={height}
          leftUnit=""
          rightUnit=" ms"
          onBrush={onBrush}
          fmtRight={(v) => v.toFixed(0)}
        />
      )}
    </div>
  );
}
