import { describe, it, expect } from 'vitest';
import {
  classifyThreshold, barGeometry, barIndexAt,
  downsampleBuckets, maxBarsForWidth, type BucketReducer,
} from './sparkline';

// Granular-sparklines sweep (M4, 2026-07-24) — the Sparkline component
// gained bar modes ('bars' with threshold colouring, 'count' for hourly
// distributions). These pure helpers carry the math; the tables below
// pin the two-level threshold boundaries and the bucket→rect projection
// (zero-based scale, zero-suppression, min-height floor, domainMax
// override) so a styling refactor can't silently shift semantics.

describe('classifyThreshold', () => {
  it('two-level boundaries around the threshold (err at ≥, warn at ≥70%)', () => {
    const cases: Array<[number, 'ok' | 'warn' | 'err']> = [
      [0, 'ok'],
      [0.69, 'ok'],   // just below the warn band
      [0.7, 'warn'],  // warn band is inclusive
      [0.99, 'warn'],
      [1, 'err'],     // breach is inclusive
      [5, 'err'],
    ];
    for (const [v, want] of cases) {
      expect(classifyThreshold(v, 1), `v=${v}`).toBe(want);
    }
  });

  it('custom warnRatio moves the warn band', () => {
    expect(classifyThreshold(4, 10, 0.4)).toBe('warn');
    expect(classifyThreshold(3.9, 10, 0.4)).toBe('ok');
  });

  it('missing / non-positive / non-finite threshold classifies everything ok', () => {
    expect(classifyThreshold(99)).toBe('ok');
    expect(classifyThreshold(99, 0)).toBe('ok');
    expect(classifyThreshold(99, -1)).toBe('ok');
    expect(classifyThreshold(99, NaN)).toBe('ok');
  });
});

describe('barGeometry', () => {
  it('zero and negative buckets produce no rect (empty slot reads as 0)', () => {
    const bars = barGeometry([0, 2, 0, -1, 4], 100, 20);
    expect(bars.map(b => b.i)).toEqual([1, 4]);
  });

  it('bars are zero-based: heights scale with value / max', () => {
    const bars = barGeometry([1, 2, 4], 90, 21, { pad: 1 });
    expect(bars).toHaveLength(3);
    const usable = 20; // height - pad
    expect(bars[2].h).toBeCloseTo(usable);
    expect(bars[1].h).toBeCloseTo(usable / 2);
    expect(bars[0].h).toBeCloseTo(usable / 4);
    // y + h always lands on the baseline.
    for (const b of bars) expect(b.y + b.h).toBeCloseTo(21);
  });

  it('slots are evenly spaced with a centred bar per bucket', () => {
    const bars = barGeometry([1, 1, 1, 1], 40, 10, { gapRatio: 0.5 });
    const slot = 10;
    for (const b of bars) {
      expect(b.w).toBeCloseTo(5);
      expect(b.x).toBeCloseTo(b.i * slot + 2.5);
    }
  });

  it('domainMax overrides the series max (shared-scale overlays)', () => {
    const [bar] = barGeometry([5], 10, 11, { domainMax: 10, pad: 1 });
    expect(bar.h).toBeCloseTo(5);
    // Values past the domain clamp to full height rather than overflowing.
    const [clamped] = barGeometry([20], 10, 11, { domainMax: 10, pad: 1 });
    expect(clamped.h).toBeCloseTo(10);
  });

  it('tiny non-zero values keep the min-height floor', () => {
    const [bar] = barGeometry([0.001, 100], 20, 20)!;
    expect(bar.h).toBeGreaterThanOrEqual(1);
  });

  it('degenerate inputs return no bars', () => {
    expect(barGeometry([], 100, 20)).toEqual([]);
    expect(barGeometry([0, 0], 100, 20)).toEqual([]);
    expect(barGeometry([1], 0, 20)).toEqual([]);
    expect(barGeometry([NaN], 100, 20)).toEqual([]);
  });
});

// v0.9.207 review-fix — bar-count budget for bar-mode sparklines.
// Services error-rate cells feed RAW service_summary_5m buckets (7d =
// 2016) into bars mode; without a cap that's one <rect> per non-zero
// bucket × 50 rows and sub-pixel bars overpainting each other. These
// tables pin the merge (adjacent groups, remainder tail, reducer
// semantics: max keeps a breach visible, sum keeps counts true) and
// the width→budget derivation.

describe('downsampleBuckets', () => {
  it('table: grouping + reducer semantics', () => {
    const cases: Array<{
      name: string; values: number[]; maxBars: number;
      reducer: BucketReducer; want: number[];
    }> = [
      { name: 'exact division, max',
        values: [1, 2, 3, 4, 5, 6], maxBars: 3, reducer: 'max', want: [2, 4, 6] },
      { name: 'exact division, sum',
        values: [1, 2, 3, 4, 5, 6], maxBars: 3, reducer: 'sum', want: [3, 7, 11] },
      { name: 'remainder tail (5 into 2 → groups of 3+2), sum',
        values: [1, 2, 3, 4, 5], maxBars: 2, reducer: 'sum', want: [6, 9] },
      { name: 'remainder tail (7 into 3 → groups of 3+3+1), max',
        values: [1, 9, 2, 3, 4, 8, 5], maxBars: 3, reducer: 'max', want: [9, 8, 5] },
      { name: 'single bucket in, budget 1 — untouched',
        values: [7], maxBars: 1, reducer: 'max', want: [7] },
      { name: 'budget 1 collapses everything to one bar, sum',
        values: [5, 3, 2], maxBars: 1, reducer: 'sum', want: [10] },
      { name: 'empty input',
        values: [], maxBars: 40, reducer: 'max', want: [] },
      { name: 'budget ≤ 0 has no drawable slots',
        values: [1, 2, 3], maxBars: 0, reducer: 'sum', want: [] },
      { name: 'under budget passes through untouched',
        values: [1, 0, 3], maxBars: 40, reducer: 'max', want: [1, 0, 3] },
      { name: 'non-finite inputs are ignored inside a group',
        values: [NaN, 2, Infinity, 4], maxBars: 2, reducer: 'sum', want: [2, 4] },
      { name: 'all-non-finite group yields 0 (empty slot)',
        values: [NaN, NaN, 1, 1], maxBars: 2, reducer: 'sum', want: [0, 2] },
    ];
    for (const c of cases) {
      expect(downsampleBuckets(c.values, c.maxBars, c.reducer), c.name).toEqual(c.want);
    }
  });

  it('max reducer keeps a single breached 5-min bucket visible after a heavy merge', () => {
    // 2016 buckets (7d preset) of a healthy 0.1% error rate with one
    // 5% spike — the whole point of bars mode is that this spike stays
    // red after downsampling to the 40-bar budget.
    const values = new Array(2016).fill(0.1);
    values[1234] = 5;
    const out = downsampleBuckets(values, 40, 'max');
    expect(out.length).toBeLessThanOrEqual(40);
    expect(Math.max(...out)).toBe(5);
  });

  it('sum reducer preserves the window total for counters', () => {
    const values = Array.from({ length: 100 }, (_, i) => i % 3); // 0,1,2,…
    const total = values.reduce((a, b) => a + b, 0);
    const out = downsampleBuckets(values, 7, 'sum');
    expect(out.length).toBeLessThanOrEqual(7);
    expect(out.reduce((a, b) => a + b, 0)).toBe(total);
  });
});

describe('maxBarsForWidth', () => {
  it('table: width → budget', () => {
    const cases: Array<[width: number, minSlotPx: number | undefined, want: number]> = [
      [80, undefined, 40],  // component default → the ≤ ~40 rects/row budget
      [79, undefined, 39],  // floors, never rounds up into overlap
      [81, undefined, 40],
      [80, 4, 20],          // wider minimum slot → fewer bars
      [1, undefined, 1],    // any positive width draws at least one bar
      [0, undefined, 0],    // degenerate widths draw nothing
      [-5, undefined, 0],
      [NaN, undefined, 0],
    ];
    for (const [width, minSlot, want] of cases) {
      expect(maxBarsForWidth(width, minSlot), `width=${width}`).toBe(want);
    }
  });
});

describe('barIndexAt', () => {
  it('floor-maps cursor x to the slot under it', () => {
    expect(barIndexAt(0, 100, 4)).toBe(0);
    expect(barIndexAt(24.9, 100, 4)).toBe(0);
    expect(barIndexAt(25, 100, 4)).toBe(1);
    expect(barIndexAt(99.9, 100, 4)).toBe(3);
  });

  it('right edge clamps into the last slot; outside / empty answers null', () => {
    expect(barIndexAt(100, 100, 4)).toBe(3); // mouse coords can land on rect.width exactly
    expect(barIndexAt(-1, 100, 4)).toBe(null);
    expect(barIndexAt(101, 100, 4)).toBe(null);
    expect(barIndexAt(50, 100, 0)).toBe(null);
  });
});
