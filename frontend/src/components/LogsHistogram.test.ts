import { describe, it, expect } from 'vitest';
import { collapse } from './LogsHistogram';

// v0.9.218 — the severity-stacked histogram was replaced by a
// total + error-overlay + error-rate fold. The fold is where the chart's
// honesty lives, so it's pinned here: an error spike that is invisible by
// COUNT (the old stack's failure) must still be loud by RATE.

const S = (name: string, pts: [number, number][]) => ({
  name, points: pts.map(([t, v]) => ({ t, v })),
});

const T0 = 1_700_000_000_000_000_000; // unix ns
const T1 = T0 + 30_000_000_000;

describe('collapse', () => {
  it('sums every severity into total and only ERROR-band into errors', () => {
    const r = collapse([
      S('INFO', [[T0, 400], [T1, 500]]),
      S('ERROR', [[T0, 4], [T1, 50]]),
      S('WARN', [[T0, 10], [T1, 10]]),
    ]);
    const total = r.series.find(s => s.key === 'total')!;
    const error = r.series.find(s => s.key === 'error')!;
    expect(total.data).toEqual([414, 560]);
    expect(error.data).toEqual([4, 50]);
    expect(r.totals).toMatchObject({ all: 974, error: 54 });
  });

  it('folds shipper severity dialects into the ERROR band', () => {
    // FATAL, lowercase, and the OTel numeric severity_number all mean error.
    const r = collapse([
      S('FATAL', [[T0, 1]]), S('error', [[T0, 2]]),
      S('17', [[T0, 3]]), S('INFO', [[T0, 94]]),
    ]);
    expect(r.series.find(s => s.key === 'error')!.data).toEqual([6]);
    expect(r.series.find(s => s.key === 'total')!.data).toEqual([100]);
  });

  it('surfaces by RATE the spike the old stack hid by count', () => {
    // 100:1 INFO:ERROR — the band the stacked chart clamped to a hairline.
    const r = collapse([
      S('INFO', [[T0, 10_000], [T1, 10_000]]),
      S('ERROR', [[T0, 10], [T1, 900]]),
    ]);
    const rate = r.series.find(s => s.key === 'rate')!.data as (number | null)[];
    expect(rate[0]).toBeCloseTo(0.0999, 3);
    expect(rate[1]).toBeCloseTo(8.257, 3);
    // The spike is ~83× on the rate axis while barely moving the bar height.
    expect(rate[1]! / rate[0]!).toBeGreaterThan(50);
  });

  it('leaves the rate as a GAP where there are no logs at all', () => {
    // 0/0 is "unknown", not "0% — clean". Drawing 0 invents a healthy window.
    const r = collapse([S('INFO', [[T0, 0], [T1, 100]])]);
    const rate = r.series.find(s => s.key === 'rate')!.data as (number | null)[];
    expect(rate[0]).toBeNull();
    expect(rate[1]).toBe(0);
  });

  it('unions and sorts bucket timestamps across ragged series', () => {
    // ES/CH drop empty buckets (min_doc_count:1), so series disagree on keys.
    const r = collapse([
      S('INFO', [[T1, 5], [T0, 1]]),
      S('ERROR', [[T1, 2]]),
    ]);
    expect(r.times).toEqual([Math.round(T0 / 1e9), Math.round(T1 / 1e9)]);
    expect(r.series.find(s => s.key === 'error')!.data).toEqual([0, 2]);
  });

  it('emits unix SECONDS — TimeChart rejects nanoseconds', () => {
    const r = collapse([S('INFO', [[T0, 1]])]);
    expect(r.times[0]).toBe(1_700_000_000);
  });

  it('returns an empty, non-throwing shape for no data', () => {
    expect(collapse([]).times).toEqual([]);
    expect(collapse([S('INFO', [])]).times).toEqual([]);
    expect(collapse([]).totals).toEqual({ all: 0, error: 0, ratePct: 0 });
  });

  it('draws total under error under rate — overlay order is load-bearing', () => {
    const r = collapse([S('INFO', [[T0, 10]]), S('ERROR', [[T0, 1]])]);
    expect(r.series.map(s => s.key)).toEqual(['total', 'error', 'rate']);
    expect(r.series[2].axis).toBe('right');
  });
});
