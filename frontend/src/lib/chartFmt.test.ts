import { describe, expect, it } from 'vitest';
import { fmtSmart, niceTickValues, fmtXTicks } from './chartFmt';

// v0.8.58 — fmtXTicks regression (operator-reported: multi-day metric x-axis
// labels overlapped). Same-day ranges stay HH:MM; multi-day ranges keep HH:MM
// on every tick and add the MM-DD prefix ONLY where the day changes — so the
// wide label appears once per day boundary instead of on every tick.
describe('fmtXTicks — compact time-axis labels', () => {
  const at = (iso: string) => Math.floor(Date.parse(iso) / 1000); // unix seconds

  it('same-day range → HH:MM on every tick (no date)', () => {
    const splits = [at('2026-06-07T09:00:00'), at('2026-06-07T09:30:00'), at('2026-06-07T10:00:00')];
    const out = fmtXTicks(splits);
    expect(out.every(s => /^\d{2}:\d{2}$/.test(s))).toBe(true);
  });

  it('multi-day range → MM-DD prefix ONLY on the first tick of each day', () => {
    const splits = [
      at('2026-06-06T22:00:00'), // day boundary (first tick) → dated
      at('2026-06-06T23:00:00'), // same day → HH:MM
      at('2026-06-07T00:00:00'), // new day → dated
      at('2026-06-07T01:00:00'), // same day → HH:MM
    ];
    const out = fmtXTicks(splits);
    const dated = out.filter(s => /^\d{2}-\d{2} /.test(s));
    expect(dated.length).toBe(2);               // exactly the two day-boundary ticks
    expect(/^\d{2}:\d{2}$/.test(out[1])).toBe(true); // intra-day stays narrow
    expect(/^\d{2}:\d{2}$/.test(out[3])).toBe(true);
    expect(out[2].startsWith('06-07')).toBe(true);    // the new-day tick is dated
  });

  it('empty splits → empty', () => {
    expect(fmtXTicks([])).toEqual([]);
  });
});

// v0.7.25 — fmtSmart is the single unit-aware formatter every axis tick,
// tooltip and KPI tile shares. It branches on unit (ms/s/%/B/throughput/count)
// with sub-thresholds inside each — exactly the "test EVERY unit branch" shape
// from the unit-mixing discipline (CLAUDE.md pitfalls + memory). A wrong
// boundary here silently mislabels latency/throughput across the whole app.

describe('fmtSmart — null/non-finite', () => {
  it.each([null, undefined, NaN, Infinity, -Infinity])('%s → em-dash', (v) => {
    expect(fmtSmart(v as number)).toBe('—');
  });
});

describe('fmtSmart — ms (auto-promote ms→s→m)', () => {
  const cases: Array<[number, string]> = [
    [300_000, '5m'],   // ≥60_000 → minutes
    [1_500, '1.5s'],   // ≥1_000  → seconds
    [50, '50ms'],      // ≥10     → 0 decimals
    [5, '5.0ms'],      // ≥1      → 1 decimal
    [0.5, '0.50ms'],   // <1      → 2 decimals
  ];
  it.each(cases)('fmtSmart(%d,"ms") = %s', (v, want) => {
    expect(fmtSmart(v, 'ms')).toBe(want);
  });
});

describe('fmtSmart — s (auto-promote s→m, demote→ms)', () => {
  const cases: Array<[number, string]> = [
    [90, '1.5m'],    // ≥60 → minutes
    [5, '5s'],       // ≥1  → seconds
    [0.5, '500ms'],  // <1  → milliseconds
  ];
  it.each(cases)('fmtSmart(%d,"s") = %s', (v, want) => {
    expect(fmtSmart(v, 's')).toBe(want);
  });
});

describe('fmtSmart — percent', () => {
  const cases: Array<[number, string]> = [
    [150, '150%'],   // ≥100 → 0 decimals
    [12.5, '12.5%'], // ≥10  → 1 decimal
    [5, '5.00%'],    // <10  → 2 decimals
  ];
  it.each(cases)('fmtSmart(%d,"%%") = %s', (v, want) => {
    expect(fmtSmart(v, '%')).toBe(want);
  });
});

describe('fmtSmart — bytes (decimal/1000, matches CH formatReadableSize)', () => {
  it('1500 B → 1.5 kB', () => expect(fmtSmart(1500, 'B')).toBe('1.5 kB'));
  it('2e6 bytes → 2 MB', () => expect(fmtSmart(2_000_000, 'bytes')).toBe('2 MB'));
});

describe('fmtSmart — throughput + default count', () => {
  it('rps carries the unit suffix', () => expect(fmtSmart(1500, 'rps')).toBe('1.5k rps'));
  it('bare count promotes with k', () => expect(fmtSmart(1234)).toBe('1.23k'));
  it('unknown unit appends after the count', () => expect(fmtSmart(50, 'widgets')).toBe('50.0 widgets'));
});

describe('niceTickValues — snap-to-decade gridlines', () => {
  it('0..100 → multiples of 20', () => {
    expect(niceTickValues(0, 100)).toEqual([0, 20, 40, 60, 80, 100]);
  });
  it('0..10 → multiples of 2', () => {
    expect(niceTickValues(0, 10)).toEqual([0, 2, 4, 6, 8, 10]);
  });
  it('degenerate range (max<=min) → empty', () => {
    expect(niceTickValues(5, 5)).toEqual([]);
    expect(niceTickValues(10, 1)).toEqual([]);
  });
  it('non-finite bounds → empty (no NaN ticks)', () => {
    expect(niceTickValues(NaN, 10)).toEqual([]);
    expect(niceTickValues(0, Infinity)).toEqual([]);
  });
});
