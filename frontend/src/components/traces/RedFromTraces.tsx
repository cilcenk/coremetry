// RedFromTraces.tsx — RED metrics derived from the CURRENT filtered trace set.
//
// Honeycomb/Datadog show RED (Rate / Errors / Duration) for whatever you're
// looking at. Here we derive it from the live, filtered rows on the page so the
// chart and the table tell the same story — no second query, no drift. Three
// series on a shared bucket grid: requests/min, errors/min, and p99 duration
// (ms).
//
// v0.8.x — rendered as a COMPACT strip (a thin Sparkline + the last value +
// min/max/avg), NOT a second big bar histogram: the brush/time-select overview
// above already owns that shape, so two giant histograms read as noise. The
// tabs (Rate / Errors / Duration p99) pick which series the strip summarises;
// everything still derives from the same filtered rows.

import { useEffect, useMemo, useRef, useState } from 'react';
import { Sparkline } from '@/components/Sparkline';
import { fmtSmart } from '@/lib/chartFmt';
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
  // Errors red, duration the warn ramp, rate the accent.
  const color = metric === 'errors' ? 'var(--err)' : metric === 'duration' ? 'var(--warn)' : 'var(--accent)';

  // Compact RED strip: this bottom panel used to be a big bar histogram,
  // which duplicated the overview's shape above. It's now a thin sparkline
  // + last value + min/max/avg over the SAME filtered rows — no info lost,
  // no second giant histogram. The brush/time-select overview stays the
  // chart up top.
  const values = active[0]?.points.map(p => p.value) ?? [];
  const has = values.length > 0;
  const last = has ? values[values.length - 1] : 0;
  const min = has ? Math.min(...values) : 0;
  const max = has ? Math.max(...values) : 0;
  const avg = has ? values.reduce((a, b) => a + b, 0) / values.length : 0;

  const sparkRef = useRef<HTMLDivElement>(null);
  const [sparkW, setSparkW] = useState(240);
  useEffect(() => {
    const el = sparkRef.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setSparkW(Math.max(80, el.clientWidth)));
    ro.observe(el);
    setSparkW(Math.max(80, el.clientWidth || 240));
    return () => ro.disconnect();
  }, []);

  const stat = (k: string, v: number) => (
    <div key={k} style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end' }}>
      <span style={{ fontSize: 12, fontWeight: 600, color: 'var(--text)', fontVariantNumeric: 'tabular-nums' }}>{fmtSmart(v, unit)}</span>
      <span style={{ fontSize: 9.5, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.5px' }}>{k}</span>
    </div>
  );

  return (
    <div style={{
      background: 'var(--bg2)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 12, marginBottom: 10,
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 10 }}>
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
      {has ? (
        <div style={{ display: 'flex', alignItems: 'center', gap: 16 }}>
          {/* big current/last value */}
          <div style={{ display: 'flex', flexDirection: 'column', minWidth: 92 }}>
            <span style={{ fontSize: 22, fontWeight: 700, color, fontVariantNumeric: 'tabular-nums', lineHeight: 1.1 }}>
              {fmtSmart(last, unit)}
            </span>
            <span style={{ fontSize: 9.5, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.5px' }}>last</span>
          </div>
          {/* thin sparkline fills the middle */}
          <div ref={sparkRef} style={{ flex: 1, minWidth: 0, height: 40, display: 'flex', alignItems: 'center' }}>
            <Sparkline values={values} width={sparkW} height={40} color={color} unit={unit} />
          </div>
          {/* min / max / avg */}
          <div style={{ display: 'flex', gap: 14 }}>
            {stat('min', min)}
            {stat('max', max)}
            {stat('avg', avg)}
          </div>
        </div>
      ) : (
        <div style={{ height: 40, display: 'flex', alignItems: 'center', justifyContent: 'center', color: 'var(--text3)', fontSize: 12 }}>
          No data to derive RED from on the current page.
        </div>
      )}
      <div style={{ marginTop: 8, fontSize: 10.5, color: 'var(--text3)' }}>
        Derived from the {rows.length} trace{rows.length === 1 ? '' : 's'} on this page — matches the table's filter exactly.
      </div>
    </div>
  );
}
