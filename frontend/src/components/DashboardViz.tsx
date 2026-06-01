import { useMemo, useState } from 'react';
import type { SpanMetricSeries, PanelVizType } from '@/lib/types';
import { fmtSmart, seriesColor } from '@/lib/chartFmt';

// DashboardViz — non-line visualizations for dashboard panels.
// MultiLineChart already covers 'line' (uPlot, hover crosshair).
// This component handles the four Grafana-style alternates:
//
//   bar           — single discrete bar per bucket, one series
//                   per group when grouped
//   stacked-bar   — bars per bucket stacked by group; total per
//                   bucket reads as the total of the group set
//   area          — line + filled area underneath (one band per
//                   series, non-stacked)
//   stacked-area  — bands stack vertically, cumulative height per
//                   bucket = sum of all groups
//
// Lightweight SVG — no chart-library dep. Hover crosshair shows
// the per-series value at the bucket under the pointer.

export function DashboardViz({ series, viz, height = 280, unit }: {
  series: SpanMetricSeries[];
  viz: Exclude<PanelVizType, 'line'>;
  height?: number;
  unit?: string;
}) {
  const [hover, setHover] = useState<{ x: number; bucketIdx: number } | null>(null);

  // Align all series to a shared bucket index (we assume the
  // span_metric endpoint already returned uniformly-bucketed
  // series — it does, via the auto-step or explicit step).
  const matrix = useMemo(() => buildMatrix(series), [series]);

  const isStacked = viz === 'stacked-bar' || viz === 'stacked-area';

  // Per-series totals for legend ordering / stacking direction.
  // Heaviest at the bottom for stacked variants keeps the
  // dominant band as the visual anchor.
  // Hoisted above the early return (rules-of-hooks) — the body
  // narrows on `matrix` so it does nothing when there's no data.
  const seriesOrder = useMemo(() => {
    if (!matrix) return [];
    const totals = matrix.values.map(arr => arr.reduce((a, b) => a + b, 0));
    return matrix.values
      .map((_, i) => i)
      .sort((a, b) => totals[b] - totals[a]);
  }, [matrix]);

  // Domain max. Stacked variants need per-bucket sum;
  // non-stacked use per-series max so a small series isn't
  // flattened against a big one.
  // Hoisted above the early return (rules-of-hooks) — narrows on
  // `matrix`; `n` is derived inside so the dep set stays correct.
  const yMax = useMemo(() => {
    if (!matrix) return 1;
    const n = matrix.times.length;
    if (isStacked) {
      let m = 0;
      for (let i = 0; i < n; i++) {
        let s = 0;
        for (const arr of matrix.values) s += arr[i] ?? 0;
        if (s > m) m = s;
      }
      return m || 1;
    }
    let m = 0;
    for (const arr of matrix.values) {
      for (const v of arr) if (v > m) m = v;
    }
    return m || 1;
  }, [matrix, isStacked]);

  if (!matrix || matrix.times.length === 0) {
    return <div style={{ padding: 14, color: 'var(--text3)', fontSize: 12 }}>No data</div>;
  }

  const W = 920, H = height;
  const padL = 50, padR = 12, padT = 8, padB = 22;
  const innerW = W - padL - padR;
  const innerH = H - padT - padB;
  const n = matrix.times.length;
  // For bar viz we leave a small gap between buckets so they
  // read as discrete. Area / stacked-area are continuous.
  const isBar = viz === 'bar' || viz === 'stacked-bar';
  const slotW = innerW / Math.max(1, n);
  const barW = isBar ? Math.max(1, slotW * 0.72) : slotW;
  const xLeft = (i: number) => padL + i * slotW + (isBar ? (slotW - barW) / 2 : 0);
  const xCenter = (i: number) => padL + i * slotW + slotW / 2;
  const yOf = (v: number) => padT + innerH - (v / yMax) * innerH;

  const onMove = (e: React.MouseEvent<SVGSVGElement>) => {
    const rect = e.currentTarget.getBoundingClientRect();
    const x = e.clientX - rect.left;
    const scaledX = x * (W / rect.width);
    const idx = Math.max(0, Math.min(n - 1,
      Math.round(((scaledX - padL) / innerW) * (n - 1))));
    setHover({ x: xCenter(idx), bucketIdx: idx });
  };
  const onLeave = () => setHover(null);

  // Y-axis ticks at 0 / 25 / 50 / 75 / 100% of yMax
  const yTicks = [0, 0.25, 0.5, 0.75, 1].map(p => p * yMax);

  return (
    <div>
      <svg viewBox={`0 0 ${W} ${H}`} width="100%" height={H}
           onMouseMove={onMove} onMouseLeave={onLeave}
           style={{ display: 'block' }}>
        {yTicks.map((v, i) => (
          <g key={i}>
            <line x1={padL} x2={W - padR} y1={yOf(v)} y2={yOf(v)}
                  stroke="var(--border)" strokeOpacity={0.4} />
            <text x={padL - 4} y={yOf(v) + 3} textAnchor="end"
                  fontSize={10} fill="var(--text3)"
                  fontFamily="ui-monospace, SFMono-Regular, monospace">
              {fmtSmart(v, unit ?? '')}
            </text>
          </g>
        ))}

        {/* Render bars / bands. For stacked variants we walk
            seriesOrder bottom-up and accumulate the offset per
            bucket. For non-stacked we draw each band / bar set
            independently against y=0. */}
        {(() => {
          if (isBar) {
            // Bar / stacked-bar — one <rect> per (series, bucket).
            const stackedOffsets = new Array(n).fill(0);
            return seriesOrder.map((si) => {
              const arr = matrix.values[si];
              const color = seriesColor(matrix.names[si]);
              return (
                <g key={si}>
                  {arr.map((v, i) => {
                    const top = isStacked ? stackedOffsets[i] + v : v;
                    const bottom = isStacked ? stackedOffsets[i] : 0;
                    const r = (
                      <rect key={i} x={xLeft(i)} y={yOf(top)}
                            width={barW}
                            height={Math.max(0, yOf(bottom) - yOf(top))}
                            fill={color} fillOpacity={0.85}
                            stroke="none" />
                    );
                    if (isStacked) stackedOffsets[i] += v;
                    return r;
                  })}
                </g>
              );
            });
          }
          // Area / stacked-area — one path per series.
          const stackedOffsets = new Array(n).fill(0);
          return seriesOrder.map((si) => {
            const arr = matrix.values[si];
            const color = seriesColor(matrix.names[si]);
            const upper: string[] = [];
            const lower: string[] = [];
            for (let i = 0; i < n; i++) {
              const bottom = isStacked ? stackedOffsets[i] : 0;
              const top = bottom + (arr[i] ?? 0);
              upper.push(`${xCenter(i)},${yOf(top)}`);
              lower.push(`${xCenter(i)},${yOf(bottom)}`);
            }
            const path = `M ${upper.join(' L ')} L ${lower.reverse().join(' L ')} Z`;
            if (isStacked) {
              for (let i = 0; i < n; i++) stackedOffsets[i] += arr[i] ?? 0;
            }
            return (
              <g key={si}>
                <path d={path} fill={color} fillOpacity={isStacked ? 0.85 : 0.30}
                      stroke={color} strokeWidth={1.2} />
              </g>
            );
          });
        })()}

        {hover && (
          <line x1={hover.x} x2={hover.x} y1={padT} y2={padT + innerH}
                stroke="var(--text)" strokeOpacity={0.35} strokeDasharray="3 3" />
        )}
      </svg>

      {/* Legend with per-bucket hover values when the cursor is
          inside the chart, else per-series totals. */}
      <div style={{
        display: 'flex', flexWrap: 'wrap', gap: 10, marginTop: 6, fontSize: 11,
      }}>
        {seriesOrder.map((si) => {
          const arr = matrix.values[si];
          const total = arr.reduce((a, b) => a + b, 0);
          return (
            <span key={si} style={{
              display: 'inline-flex', alignItems: 'center', gap: 4,
              color: 'var(--text2)',
            }}>
              <span style={{
                width: 10, height: 10, borderRadius: 2,
                background: seriesColor(matrix.names[si]),
              }} />
              <span style={{ fontFamily: 'ui-monospace, monospace' }}>
                {matrix.names[si]}
              </span>
              <span style={{ color: 'var(--text3)' }}>
                {hover
                  ? fmtSmart(arr[hover.bucketIdx] ?? 0, unit ?? '')
                  : fmtSmart(total, unit ?? '')}
              </span>
            </span>
          );
        })}
      </div>
    </div>
  );
}

// buildMatrix turns a SpanMetricSeries[] (where each series has
// its own points list) into a uniform (times[], values[][])
// pair. Spans which have a different bucket alignment are
// rare in practice (the server returns identical bucket
// starts across all series of one query) but we defensively
// take the union of timestamps so a mismatch doesn't drop
// data — empty buckets render as zero.
function buildMatrix(series: SpanMetricSeries[]):
  { times: number[]; names: string[]; values: number[][] } | null {
  if (!series || series.length === 0) return null;
  // Union timestamps.
  const tset = new Set<number>();
  for (const s of series) {
    for (const p of s.points) tset.add(p.time);
  }
  const times = Array.from(tset).sort((a, b) => a - b);
  const tIdx = new Map<number, number>();
  times.forEach((t, i) => tIdx.set(t, i));
  const names: string[] = [];
  const values: number[][] = [];
  for (const s of series) {
    const row = new Array(times.length).fill(0);
    for (const p of s.points) {
      const i = tIdx.get(p.time);
      if (i !== undefined && p.value != null && isFinite(p.value)) {
        row[i] = p.value;
      }
    }
    names.push((s.groupKey ?? []).join(' · ') || 'value');
    values.push(row);
  }
  return { times, names, values };
}
