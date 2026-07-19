import { describe, it, expect } from 'vitest';
import { resolveVar } from './resolveVar';
import { yRangeHeadroom } from './yRange';
import type uPlot from 'uplot';

// v0.9.75 (chart-consolidation Adım 0) — dört chart bileşeninden
// çıkarılan byte-identical yardımcıların saf-fn kontratını pinler.

describe('resolveVar', () => {
  // NOT: test ortamı bilinçli node (jsdom yok, vitest.config.ts) —
  // getComputedStyle'a giren token-çözüm dalı browser-only. Burada
  // yalnız non-match erken-dönüş kontratı test edilir: bir var(--x)
  // token'ı EŞLEŞMEZSE girdi olduğu gibi geçmeli (regex tam eşleşme).
  it('passes a raw colour through unchanged', () => {
    expect(resolveVar('#ff0000')).toBe('#ff0000');
    expect(resolveVar('rgb(1,2,3)')).toBe('rgb(1,2,3)');
    expect(resolveVar('orange')).toBe('orange');
  });
  it('only matches the exact single var(--token) shape', () => {
    // eksik/bozuk/çoklu token deseni ham geçer (regex tam eşleşme ister)
    expect(resolveVar('var(--a) var(--b)')).toBe('var(--a) var(--b)');
    expect(resolveVar('notvar(--x)')).toBe('notvar(--x)');
    expect(resolveVar('var(--x')).toBe('var(--x');
  });
});

describe('yRangeHeadroom', () => {
  const u = {} as uPlot;
  it('gives ~10% headroom over a positive max, 0-based', () => {
    expect(yRangeHeadroom(u, 0, 100)[1]).toBeCloseTo(110, 6);
    expect(yRangeHeadroom(u, 5, 200)[1]).toBeCloseTo(220, 6);
  });
  it('floors the top at 1 when max is 0 or negative', () => {
    expect(yRangeHeadroom(u, 0, 0)).toEqual([0, 1]);
    expect(yRangeHeadroom(u, -5, -1)).toEqual([0, 1]);
  });
  it('always pins the bottom at 0 (never uses min)', () => {
    expect(yRangeHeadroom(u, 50, 100)[0]).toBe(0);
  });
});
