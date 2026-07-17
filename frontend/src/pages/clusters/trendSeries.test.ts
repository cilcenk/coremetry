import { describe, expect, it } from 'vitest';
import { limitThresholds, thanosPodSeriesToSeries, thanosTrendToSeries } from './trendSeries';

// v0.9.4 — Thanos→MultiLineChart dönüşüm sözleşmeleri: saniye→ns,
// boş trend → boş seri, pod sırası korunur, 0-limit çizgi üretmez.

const T = [
  { bucket: 1784271060, cpuCores: 0.5, memBytes: 100 },
  { bucket: 1784271120, cpuCores: 0.7, memBytes: 200 },
];

describe('thanosTrendToSeries', () => {
  it('converts buckets (s) to time (ns) and picks the axis', () => {
    const s = thanosTrendToSeries(T, 'CPU', t => t.cpuCores);
    expect(s).toHaveLength(1);
    expect(s[0].groupKey).toEqual(['CPU']);
    expect(s[0].points[0]).toEqual({ time: 1784271060 * 1e9, value: 0.5 });
    expect(s[0].points[1].value).toBe(0.7);
  });

  it('empty trend → empty series (chart renders its own empty state)', () => {
    expect(thanosTrendToSeries([], 'CPU', t => t.cpuCores)).toEqual([]);
  });
});

describe('thanosPodSeriesToSeries', () => {
  it('one series per pod, server order preserved, empty pods dropped', () => {
    const s = thanosPodSeriesToSeries([
      { pod: 'busy', trend: T },
      { pod: 'gone', trend: [] },
      { pod: 'idle', trend: [T[0]] },
    ], t => t.memBytes);
    expect(s.map(x => x.groupKey[0])).toEqual(['busy', 'idle']);
    expect(s[0].points[1].value).toBe(200);
  });
});

describe('limitThresholds', () => {
  it('limit=err, request=warn', () => {
    expect(limitThresholds(2, 1)).toEqual([
      { value: 2, label: 'limit', severity: 'err' },
      { value: 1, label: 'request', severity: 'warn' },
    ]);
  });

  it('0/undefined draws nothing (unknown contract)', () => {
    expect(limitThresholds(0, undefined)).toEqual([]);
  });

  it('unit rides the label', () => {
    expect(limitThresholds(2, 0, 'cores')[0].label).toBe('limit (cores)');
  });
});
