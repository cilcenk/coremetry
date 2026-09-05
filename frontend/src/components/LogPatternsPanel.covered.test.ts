import { describe, it, expect } from 'vitest';
import { coveredLabel } from './LogPatternsPanel';

// v0.10.441 (log arama denetimi C4) — kapsanan alt pencere etiketi: yalnız
// tavan dolunca (sampled >= cap), alanlar yokken null, süre okunur.
describe('coveredLabel', () => {
  const from = Date.UTC(2026, 8, 6, 10, 0, 0) * 1e6;
  const to = from + 4 * 60 * 1e9;
  it('null when the cap was not hit or fields are missing', () => {
    expect(coveredLabel(null)).toBeNull();
    expect(coveredLabel({ sampled: 120, cap: 2000, coveredFromNs: from, coveredToNs: to })).toBeNull();
    expect(coveredLabel({ sampled: 2000, cap: 2000 })).toBeNull();
    expect(coveredLabel({ sampled: 2000, cap: 0, coveredFromNs: from, coveredToNs: to })).toBeNull();
  });
  it('labels the newest-edge sub-window with its span when the cap was hit', () => {
    const s = coveredLabel({ sampled: 2000, cap: 2000, coveredFromNs: from, coveredToNs: to });
    expect(s).toMatch(/^kapsanan: \d\d:\d\d:\d\d–\d\d:\d\d:\d\d \(4 dk, en yeni uç\)$/);
    expect(coveredLabel({ sampled: 2000, cap: 2000, coveredFromNs: from, coveredToNs: from + 30e9 })).toContain('(30 sn,');
    expect(coveredLabel({ sampled: 2000, cap: 2000, coveredFromNs: from, coveredToNs: from + 5400e9 })).toContain('(1.5 sa,');
  });
});
