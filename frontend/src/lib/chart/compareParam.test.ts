// compareParam.test.ts — v0.10.311: URL ↔ mod gidiş-dönüş, takma adlar,
// tohum, kaydırma. 'prior' (ContextBar sözlüğü) ile 'prev' aynı mod.
import { describe, it, expect } from 'vitest';
import { parseCompareParam, encodeCompareParam, parseStoredCompare, compareOffsetNs, type CompareMode } from './compareParam';

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
