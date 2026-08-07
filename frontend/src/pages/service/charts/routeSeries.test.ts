import { describe, it, expect } from 'vitest';
import {
  ROUTE_TOP_N, routeMoreNote, topRoutesByArea, metricUnitToGrafana,
} from './routeSeries';
import type { SpanMetricSeries } from '@/lib/types';

// v0.9.774 — panelin iki saf kararı: kırpma notu + birim eşlemesi.

function ser(route: string, values: number[]): SpanMetricSeries {
  return {
    groupKey: [route],
    points: values.map((v, i) => ({ time: (i + 1) * 60e9, value: v })),
  };
}

describe('routeMoreNote', () => {
  const cases: { name: string; total: number; shown: number; capped: boolean; want: string | null }[] = [
    { name: 'kırpma yok → not yok', total: 3, shown: 3, capped: false, want: null },
    {
      name: 'kırpıldı → sayılar + ölçüt',
      total: 14, shown: 10, capped: false,
      want: '10 seri · +4 daha (alan bazlı: Σ|değer| en yüksek 10)',
    },
    {
      name: 'kırpıldı + satır tavanı → ikisi de',
      total: 14, shown: 10, capped: true,
      want: '10 seri · +4 daha (alan bazlı: Σ|değer| en yüksek 10) · ⚠ satır tavanı doldu — liste eksik olabilir',
    },
    {
      name: 'kırpma yok ama satır tavanı → yalnız tavan',
      total: 2, shown: 2, capped: true,
      want: '⚠ satır tavanı doldu — liste eksik olabilir',
    },
  ];
  for (const c of cases) {
    it(c.name, () => {
      expect(routeMoreNote(c.total, c.shown, 10, c.capped)).toBe(c.want);
    });
  }

  it('negatif fark üretmez (sunucu total < shown bildirirse)', () => {
    expect(routeMoreNote(1, 3, 10, false)).toBeNull();
  });
});

describe('topRoutesByArea', () => {
  const many = [
    ser('/a', [1, 1]),      // alan 2
    ser('/b', [100, 100]),  // alan 200
    ser('/c', [10, 10]),    // alan 20
  ];

  it('alana göre azalan sıralar', () => {
    const { items } = topRoutesByArea(many);
    expect(items.map(i => i.name)).toEqual(['/b', '/c', '/a']);
  });

  it('cap kadar seri çizer, gerisini nota yazar', () => {
    const { items, note } = topRoutesByArea(many, 2);
    expect(items).toHaveLength(2);
    expect(items.map(i => i.name)).toEqual(['/b', '/c']);
    expect(note).toBe('2 seri · +1 daha (alan bazlı: Σ|değer| en yüksek 2)');
  });

  it('rowsCapped notu taşınır', () => {
    const { note } = topRoutesByArea(many, 3, true);
    expect(note).toBe('⚠ satır tavanı doldu — liste eksik olabilir');
  });

  it('boş/null giriş → boş item, not yok', () => {
    expect(topRoutesByArea(null)).toEqual({ items: [], note: null });
    expect(topRoutesByArea([])).toEqual({ items: [], note: null });
  });

  it('item şekli CorePanelMulti sözleşmesi: tek seri, data rolü', () => {
    const { items } = topRoutesByArea([ser('/x', [5])]);
    expect(items[0]).toEqual({ name: '/x', role: 'data', series: [ser('/x', [5])] });
  });

  it('varsayılan cap 10', () => {
    expect(ROUTE_TOP_N).toBe(10);
    const wide = Array.from({ length: 25 }, (_, i) => ser(`/r${i}`, [i + 1]));
    expect(topRoutesByArea(wide).items).toHaveLength(10);
  });

  it('adsız groupKey lejantta görünmez satır bırakmaz', () => {
    const { items } = topRoutesByArea([{ groupKey: [''], points: [{ time: 1, value: 1 }] }]);
    expect(items[0].name).toBe('(adsız)');
  });
});

// Unit-mixing kuralı: HER dal. Prod 's', lokal 'ms' üretiyor — ikisi de
// canlı yol, biri sessizce bozulursa eksen 1000× yanlış okunur.
describe('metricUnitToGrafana', () => {
  const cases: { in: string | undefined; want: 's' | 'ms' | undefined }[] = [
    { in: 's', want: 's' },
    { in: 'S', want: 's' },
    { in: ' s ', want: 's' },
    { in: 'sec', want: 's' },
    { in: 'secs', want: 's' },
    { in: 'second', want: 's' },
    { in: 'seconds', want: 's' },
    { in: 'ms', want: 'ms' },
    { in: 'MS', want: 'ms' },
    { in: 'millisecond', want: 'ms' },
    { in: 'milliseconds', want: 'ms' },
    // TANINMAYAN → undefined (tahmin YOK).
    { in: '', want: undefined },
    { in: '   ', want: undefined },
    { in: undefined, want: undefined },
    { in: 'By', want: undefined },
    { in: 'requests', want: undefined },
    { in: 'us', want: undefined },
    { in: 'ns', want: undefined },
  ];
  for (const c of cases) {
    it(`${JSON.stringify(c.in)} → ${String(c.want)}`, () => {
      expect(metricUnitToGrafana(c.in)).toBe(c.want);
    });
  }
});
