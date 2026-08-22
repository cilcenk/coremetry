import { describe, it, expect } from 'vitest';
import { selfTimeMs, childCoverageNs, type SpanInterval } from './selfTime';

// v0.9.1273 — öz süre: aralık birleşimi (naif toplam değil).
const S = (spanId: string, parent: string, a: number, b: number): SpanInterval =>
  ({ spanId, parentSpanId: parent, startTime: a * 1e6, endTime: b * 1e6 }); // ms→ns

describe('selfTimeMs', () => {
  const P = S('p', '', 0, 100);
  it('no children → self = duration', () => {
    expect(selfTimeMs(P, [P])).toBe(100);
  });
  it('sequential children subtract fully', () => {
    expect(selfTimeMs(P, [P, S('a', 'p', 10, 30), S('b', 'p', 50, 70)])).toBe(60);
  });
  it('OVERLAPPING children count once (async fan-out)', () => {
    // naif toplam 80 düşerdi (self=20); birleşim 50 → self=50.
    expect(selfTimeMs(P, [P, S('a', 'p', 10, 50), S('b', 'p', 20, 60)])).toBe(50);
  });
  it('child overflowing parent bounds is clamped', () => {
    expect(selfTimeMs(P, [P, S('a', 'p', -20, 40)])).toBe(60);
  });
  it('never negative even if children blanket the parent', () => {
    expect(selfTimeMs(P, [P, S('a', 'p', 0, 100), S('b', 'p', 0, 100)])).toBe(0);
  });
  it('grandchildren are ignored (only direct children)', () => {
    expect(childCoverageNs(P, [P, S('a', 'p', 10, 30), S('g', 'a', 10, 30)])).toBe(20 * 1e6);
  });
});
