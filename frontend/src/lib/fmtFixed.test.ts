import { describe, it, expect } from 'vitest';
import { fmtFixed } from './utils';

// v0.8.305 (quality bar S4) — the daily pages call `.toFixed()` on fields
// TypeScript types as `number` but the backend can serialize as null for a
// zero-traffic service; with the route-level boundary that's a page-wide
// fallback for one bad row. fmtFixed is the tolerant formatter for those
// display sites: real numbers format, everything else renders "—".
describe('fmtFixed', () => {
  it('formats finite numbers with the requested digits', () => {
    expect(fmtFixed(12.345, 1)).toBe('12.3');
    expect(fmtFixed(0, 2)).toBe('0.00');
    expect(fmtFixed(99.999, 0)).toBe('100');
  });

  it('null / undefined render the em dash', () => {
    expect(fmtFixed(null, 1)).toBe('—');
    expect(fmtFixed(undefined, 2)).toBe('—');
  });

  it('NaN / Infinity render the em dash (division fallout upstream)', () => {
    expect(fmtFixed(Number.NaN, 1)).toBe('—');
    expect(fmtFixed(Infinity, 1)).toBe('—');
    expect(fmtFixed(-Infinity, 1)).toBe('—');
  });
});
