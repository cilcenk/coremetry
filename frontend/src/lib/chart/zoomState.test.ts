import { describe, it, expect } from 'vitest';
import { isXZoomed, alignedSeriesMax, yRefitScale } from './zoomState';

// v0.9.78 (uPlot Aşama 1) — setData zoom-koruma kararının saf çekirdeği.
// Bu tablo, 30s poll'ün operatörü drag-zoom'dan atmaması davranışını
// pinler: zoomsuz → eski davranış (reset), zoomlu → x korunur + y refit.

describe('isXZoomed', () => {
  const times = [100, 200, 300, 400, 500];
  it('full range → not zoomed (eski davranış: reset)', () => {
    expect(isXZoomed(times, 100, 500)).toBe(false);
  });
  it('narrowed window → zoomed (x korunmalı)', () => {
    expect(isXZoomed(times, 250, 350)).toBe(true);
  });
  it('only left edge pulled in → zoomed', () => {
    expect(isXZoomed(times, 250, 500)).toBe(true);
  });
  it('only right edge pulled in → zoomed', () => {
    expect(isXZoomed(times, 100, 350)).toBe(true);
  });
  it('within tolerance of full range → not zoomed (kenar bucket kayması)', () => {
    expect(isXZoomed(times, 100.3, 499.7)).toBe(false);
  });
  it('null scale (henüz çizilmemiş) → not zoomed', () => {
    expect(isXZoomed(times, null, null)).toBe(false);
    expect(isXZoomed(times, undefined, undefined)).toBe(false);
  });
  it('empty times → not zoomed', () => {
    expect(isXZoomed([], 1, 2)).toBe(false);
  });
});

describe('alignedSeriesMax', () => {
  // data[0] = x ekseni; seri sütunları 1-based.
  const data = [
    [1, 2, 3],        // x
    [10, 50, 30],     // seri 1
    [5, 5, 80],       // seri 2
    [null, 99, null], // seri 3 (nullable)
  ];
  it('takes the max across the given series indices, skipping nulls', () => {
    expect(alignedSeriesMax(data, [1, 2])).toBe(80);
    expect(alignedSeriesMax(data, [1])).toBe(50);
    expect(alignedSeriesMax(data, [3])).toBe(99);
  });
  it('returns 0 for empty / all-null selection', () => {
    expect(alignedSeriesMax(data, [])).toBe(0);
    expect(alignedSeriesMax([[1, 2], [null, null]], [1])).toBe(0);
  });
  it('ignores NaN / Infinity', () => {
    expect(alignedSeriesMax([[1], [NaN], [Infinity]], [1, 2])).toBe(0);
  });
});

describe('yRefitScale', () => {
  const data = [[1, 2, 3], [10, 50, 30]];
  it('0-based, 10% headroom over the selection max', () => {
    const r = yRefitScale(data, [1]);
    expect(r.min).toBe(0);
    expect(r.max).toBeCloseTo(55, 6); // 50 * 1.1
  });
  it('floors the top at 1 when everything is 0/null', () => {
    expect(yRefitScale([[1, 2], [0, null]], [1])).toEqual({ min: 0, max: 1 });
  });
});
