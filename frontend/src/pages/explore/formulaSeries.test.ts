// formulaSeries tests (explore-v2 Phase 2).

import { describe, it, expect } from 'vitest';
import { letterTotals, formulaSeries } from './formulaSeries';
import type { SpanMetricSeries } from '@/lib/types';

const mk = (groupKey: string[], pts: [number, number][]): SpanMetricSeries => ({
  groupKey,
  points: pts.map(([time, value]) => ({ time, value })),
});

describe('letterTotals', () => {
  it('sums the group fan-out per bucket', () => {
    const totals = letterTotals([
      mk(['checkout'], [[1, 10], [2, 20]]),
      mk(['payments'], [[1, 1], [2, 2], [3, 3]]),
    ]);
    expect(totals.get(1)).toBe(11);
    expect(totals.get(2)).toBe(22);
    expect(totals.get(3)).toBe(3); // bucket present in one group only
  });

  it('a NaN point contributes 0, not a hole', () => {
    const totals = letterTotals([
      mk(['a'], [[1, NaN]]),
      mk(['b'], [[1, 5]]),
    ]);
    expect(totals.get(1)).toBe(5);
  });
});

describe('formulaSeries', () => {
  const byLetter = {
    A: [mk(['x'], [[1, 10], [2, 20], [3, 30]])],
    B: [mk(['x'], [[1, 2], [2, 4]])], // no bucket 3
  };

  it('evaluates across shared buckets only', () => {
    const pts = formulaSeries('A / B', byLetter);
    expect(pts).toEqual([{ time: 1, value: 5 }, { time: 2, value: 5 }]);
  });

  it('ratio-percent expression', () => {
    const pts = formulaSeries('B / A * 100', byLetter);
    expect(pts).toEqual([{ time: 1, value: 20 }, { time: 2, value: 20 }]);
  });

  it('divide-by-zero bucket becomes a gap, not Infinity', () => {
    const pts = formulaSeries('A / B', {
      A: [mk(['x'], [[1, 10], [2, 20]])],
      B: [mk(['x'], [[1, 0], [2, 4]])],
    });
    expect(pts).toEqual([{ time: 2, value: 5 }]);
  });

  it('reference to a letter with no data → empty (panel renders empty state)', () => {
    expect(formulaSeries('A / C', byLetter)).toEqual([]);
    expect(formulaSeries('A / B', { A: byLetter.A, B: [] })).toEqual([]);
  });

  it('letters sum across their groups before evaluating', () => {
    const pts = formulaSeries('A', {
      A: [mk(['a'], [[1, 1]]), mk(['b'], [[1, 2]]), mk(['c'], [[1, 3]])],
    });
    expect(pts).toEqual([{ time: 1, value: 6 }]);
  });

  it('parse error → empty', () => {
    expect(formulaSeries('A +* B', byLetter)).toEqual([]);
  });
});
