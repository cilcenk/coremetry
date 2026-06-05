// RedFromTraces.tsx — RED metrics derived from the CURRENT filtered trace set.
//
// Honeycomb/Datadog show RED (Rate / Errors / Duration) for whatever you're
// looking at. Here we derive it from the live, filtered rows on the page so the
// chart and the table tell the same story — no second query, no drift. Three
// series on a shared bucket grid: requests/min, errors/min, and p99 duration
// (ms). We feed them to the shared uPlot MultiLineChart.
//
// NOTE: this is the fall-back wiring. Task A's <TimeSeriesPanel> doesn't exist
// in the tree yet (only components/viz/MetricQueryEditor.tsx is present), so we
// import the production MultiLineChart so the file type-checks against what
// ships today. When TimeSeriesPanel lands, swap the <MultiLineChart> below for
// it (see SHARED CHANGE REQUESTS in the task report).

import { useMemo, useState } from 'react';
import { MultiLineChart } from '@/components/MultiLineChart';
import { aggregateBuckets } from '@/lib/perf/transforms';
import type { SpanMetricSeries, TraceRow } from '@/lib/types';

function chooseBucketMs(spanMs: number): number {
  const target = 48;
  const raw = spanMs / target;
  const steps = [
    1000, 2000, 5000, 10_000, 15_000, 30_000,
    60_000, 120_000, 300_000, 600_000, 900_000,
    1_800_000, 3_600_000, 7_200_000, 21_600_000, 43_200_000, 86_400_000,
  ];
  for (const s of steps) if (s >= raw) return s;
  return steps[steps.length - 1];
}

type Metric = 'rate' | 'errors' | 'duration';

const METRICS: { id: Metric; label: string; unit: string }[] = [
  { id: 'rate', label: 'Rate', unit: '/min' },
  { id: 'errors', label: 'Errors', unit: '/min' },
  { id: 'duration', label: 'Duration p99', unit: 'ms' },
];

export function RedFromTraces({ rows }: { rows: TraceRow[] }) {
  const [metric, setMetric] = useState<Metric>('rate');

  const { rate, errors, duration } = useMemo(() => {
    const empty: SpanMetricSeries[] = [];
    if (rows.length === 0) return { rate: empty, errors: empty, duration: empty };
    let lo = Infinity, hi = -Infinity;
    for (const r of rows) {
      const t = r.startTime / 1e6;
      if (t < lo) lo = t;
      if (t > hi) hi = t;
    }
    const bucketMs = chooseBucketMs(Math.max(1, hi - lo));
    const perMin = 60_000 / bucketMs;

    const times: number[] = [];
    const ones: number[] = [];
    const errTimes: number[] = [];
    const errOnes: number[] = [];
    const durTimes: number[] = [];
    const durVals: number[] = [];
    for (const r of rows) {
      const t = r.startTime / 1e6;
      times.push(t); ones.push(1);
      durTimes.push(t); durVals.push(r.durationMs);
      if (r.hasError) { errTimes.push(t); errOnes.push(1); }
    }
    const rateAgg = aggregateBuckets(times, ones, bucketMs, 'count');
    const errAgg = aggregateBuckets(errTimes, errOnes, bucketMs, 'count');
    const durAgg = aggregateBuckets(durTimes, durVals, bucketMs, 'p99');

    // ms bucket-start → ns time for MultiLineChart's point shape.
    const toSeries = (label: string, x: number[], y: number[], scale = 1): SpanMetricSeries[] => ([{
      groupKey: [label],
      points: x.map((t, i) => ({ time: t * 1e6, value: y[i] * scale })),
    }]);

    return {
      rate: toSeries('requests/min', rateAgg.x, rateAgg.y, perMin),
      errors: toSeries('errors/min', errAgg.x, errAgg.y, perMin),
      duration: toSeries('p99 ms', durAgg.x, durAgg.y),
    };
  }, [rows]);

  const active = metric === 'rate' ? rate : metric === 'errors' ? errors : duration;
  const unit = METRICS.find(m => m.id === metric)!.unit;
  // Errors are red, duration is the warn ramp, rate is the accent — pin the
  // colour so the series reads correctly regardless of the label hash.
  const colorOf = (): string | undefined =>
    metric === 'errors' ? 'var(--err)' : metric === 'duration' ? 'var(--warn)' : 'var(--accent)';

  return (
    <div style={{
      background: 'var(--bg2)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 12, marginBottom: 10,
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 8 }}>
        <span style={{ fontSize: 11, color: 'var(--text2)', fontWeight: 700, letterSpacing: '0.5px', textTransform: 'uppercase' }}>
          RED · current filter
        </span>
        <div className="segmented" style={{ marginLeft: 'auto' }}>
          {METRICS.map(m => (
            <button key={m.id} className={metric === m.id ? 'active' : ''} onClick={() => setMetric(m.id)}>
              {m.label}
            </button>
          ))}
        </div>
      </div>
      {active.length > 0 && active[0].points.length > 0 ? (
        <MultiLineChart series={active} unit={unit} height={180}
          logScale={metric === 'duration'} colorOf={colorOf} />
      ) : (
        <div style={{ height: 180, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--text3)', fontSize: 12 }}>
          No data to derive RED from on the current page.
        </div>
      )}
      <div style={{ marginTop: 6, fontSize: 10.5, color: 'var(--text3)' }}>
        Derived from the {rows.length} trace{rows.length === 1 ? '' : 's'} on this page — matches the table's filter exactly.
      </div>
    </div>
  );
}
