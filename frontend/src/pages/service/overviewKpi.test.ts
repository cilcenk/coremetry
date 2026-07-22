import { describe, it, expect } from 'vitest';
import { firstNum } from './overviewKpi';

// Guards the v0.9.170 fix — operator-reported: services with an unresolved /
// missing cluster attribute (e.g. `${openshift.cluster}`) had a null summary
// bundle, and the Overview's blanket `if (!info) return null` blanked the WHOLE
// tab. Headline numbers now fall back to the live RED / latency series;
// firstNum is the resolver. It MUST pick the first finite value (0 is valid),
// skip undefined / null / NaN / Infinity, and never return NaN.
describe('firstNum', () => {
  it('picks the first finite value, treating 0 as valid', () => {
    expect(firstNum(12.5, 3)).toBe(12.5);
    expect(firstNum(0, 3)).toBe(0); // 0 is a real reading, not "missing"
  });

  it('skips undefined / null and falls through', () => {
    expect(firstNum(undefined, 4.2)).toBe(4.2);
    expect(firstNum(null, undefined, 7)).toBe(7);
    expect(firstNum(undefined, null)).toBe(0); // nothing → 0, never NaN
  });

  it('skips NaN / Infinity so a rogue quantile value never reaches the tile', () => {
    expect(firstNum(NaN, 5)).toBe(5);
    expect(firstNum(Infinity, 5)).toBe(5);
    expect(firstNum(NaN, undefined)).toBe(0);
  });

  it('supports both info-first and series-first orderings', () => {
    expect(firstNum(2.0, 9.9)).toBe(2.0);      // summary preferred (throughput/failure)
    expect(firstNum(undefined, 88)).toBe(88);  // live series preferred (latency), summary fallback
  });
});
