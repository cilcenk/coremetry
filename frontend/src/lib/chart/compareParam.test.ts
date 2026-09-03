// compareParam.test.ts — v0.10.311: URL ↔ mod gidiş-dönüş, takma adlar,
// tohum, kaydırma. 'prior' (ContextBar sözlüğü) ile 'prev' aynı mod.
import { describe, it, expect } from 'vitest';
import { parseCompareParam, encodeCompareParam, parseStoredCompare, compareOffsetNs, shiftSeriesNs, ghostItemsFrom, type CompareMode } from './compareParam';

describe('compareParam', () => {
  it('parse: takma adlar ve çöp', () => {
    expect(parseCompareParam(null)).toBeNull();
    expect(parseCompareParam('')).toBeNull();
    expect(parseCompareParam('  ')).toBeNull();
    expect(parseCompareParam('prior')).toBe('prev');
    expect(parseCompareParam('1')).toBe('prev');
    expect(parseCompareParam('prev')).toBe('prev');
    expect(parseCompareParam('24H')).toBe('24h');
    expect(parseCompareParam('7d')).toBe('7d');
    expect(parseCompareParam('off')).toBe('off');
    expect(parseCompareParam('0')).toBe('off');
    expect(parseCompareParam('weird')).toBeNull();
    expect(parseCompareParam('30d')).toBeNull();
  });
  it('encode ↔ parse gidiş-dönüş; off anahtarı siler; prev → prior', () => {
    for (const m of ['24h', '7d', 'prev'] as CompareMode[]) {
      expect(parseCompareParam(encodeCompareParam(m))).toBe(m);
    }
    expect(encodeCompareParam('prev')).toBe('prior');
    expect(encodeCompareParam('off')).toBe('');
  });
  it('tohum: yalnız üç mod, gerisi off', () => {
    expect(parseStoredCompare('24h')).toBe('24h');
    expect(parseStoredCompare('prev')).toBe('prev');
    expect(parseStoredCompare('prior')).toBe('off');
    expect(parseStoredCompare(null)).toBe('off');
  });
  it('kaydırma ns', () => {
    const from = 1e18, to = 1e18 + 3600e9;
    expect(compareOffsetNs('off', from, to)).toBe(0);
    expect(compareOffsetNs('prev', from, to)).toBe(3600e9);
    expect(compareOffsetNs('24h', from, to)).toBe(86400e9);
    expect(compareOffsetNs('7d', from, to)).toBe(7 * 86400e9);
  });
});

// v0.10.315 — ghost kurucuları: zaman +offset, rol muted, ad korunur
// (CorePanelMulti "(önceki)" ekini kendisi basar); boş/0 → undefined.
describe('shiftSeriesNs / ghostItemsFrom', () => {
  const ser = [{ groupKey: ['a'], points: [{ time: 10, value: 1 }, { time: 20, value: 2 }] }];
  it('shift: +offset, girdi değişmez; offset 0 aynı referans', () => {
    const out = shiftSeriesNs(ser, 5);
    expect(out[0].points.map(p => p.time)).toEqual([15, 25]);
    expect(ser[0].points[0].time).toBe(10);
    expect(shiftSeriesNs(ser, 0)).toBe(ser);
  });
  it('ghost: muted rol, ad aynı, boş seri düşer, offset ≤ 0 → undefined', () => {
    const g = ghostItemsFrom([{ name: 'P99', role: 'data', series: ser }, { name: 'boş', role: 'data', series: [{ groupKey: [], points: [] }] }], 5);
    expect(g).toEqual([{ name: 'P99', role: 'muted', series: [{ groupKey: ['a'], points: [{ time: 15, value: 1 }, { time: 25, value: 2 }] }] }]);
    expect(ghostItemsFrom([{ name: 'x', role: 'data', series: ser }], 0)).toBeUndefined();
    expect(ghostItemsFrom(undefined, 5)).toBeUndefined();
    expect(ghostItemsFrom([], 5)).toBeUndefined();
  });
});
