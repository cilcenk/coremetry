// seriesCompact.test.ts — v0.10.186 sözleşmesi (seriesCompact.ts başlığı; Go series_compact_test.go aynası).
import { describe, it, expect } from 'vitest';
import { expandCompactSeries, decodeBundle } from './seriesCompact';

describe('seriesCompact', () => {
  it('düzenli: t0 + i·step; düzensiz: t[]; tek/boş seri', () => {
    expect(expandCompactSeries({ groupKey: ['a'], t0: 100, step: 15, v: [1, 2.5, 3] })).toEqual({ groupKey: ['a'], points: [{ time: 100, value: 1 }, { time: 115, value: 2.5 }, { time: 130, value: 3 }] });
    expect(expandCompactSeries({ groupKey: ['g'], t: [0, 15, 45], v: [1, 2, 3] }).points.map(p => p.time)).toEqual([0, 15, 45]);
    expect(expandCompactSeries({ groupKey: ['b'], t0: 7, v: [0] })).toEqual({ groupKey: ['b'], points: [{ time: 7, value: 0 }] });
    expect(expandCompactSeries({ groupKey: ['c'], v: [] }).points).toEqual([]);
  });
  it('2^53 üstü unix ns: step tam saniye → t0 + i·step TAM (son nokta birebir)', () => {
    const t0 = 1_700_000_000_000_000_000, step = 15_000_000_000;
    const s = expandCompactSeries({ groupKey: ['x'], t0, step, v: new Array(240).fill(1) });
    expect(s.points[239].time).toBe(1_700_003_585_000_000_000); // 1.7e18 + 239·15e9, double'da tam
    expect(s.points[1].time - s.points[0].time).toBe(step);
  });
  it('decodeBundle: kodlu slot açılır (enc/cols düşer, öteki alanlar kalır); düz slot aynen; bilinmeyen enc → series SİLİNİR (self-fetch)', () => {
    const b = decodeBundle({
      p1: { enc: 'col', cols: [{ groupKey: ['x'], t0: 1, step: 1, v: [9] }], series: null, totalSeries: 5, tail: [{ time: 1, sum: 2, count: 1 }] },
      p2: { series: [{ groupKey: ['y'], points: [{ time: 1, value: 1 }] }] },
      p3: { enc: 'zzz', series: null },
    });
    expect(b.p1.series).toEqual([{ groupKey: ['x'], points: [{ time: 1, value: 9 }] }]);
    expect('enc' in b.p1).toBe(false);
    expect('cols' in b.p1).toBe(false);
    expect(b.p1.totalSeries).toBe(5);
    expect(b.p2.series?.[0].points[0].value).toBe(1);
    expect('series' in b.p3).toBe(false);
  });
});
