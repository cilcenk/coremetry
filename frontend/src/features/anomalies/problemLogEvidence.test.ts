import { describe, it, expect } from 'vitest';
import { patternsTotalLabel, PROBLEM_LOG_SAMPLE, PROBLEM_LOG_SEVERITY } from './ProblemLogEvidence';

// v0.10.452 (C1) — başlık sayısı dürüst: ES alt sınırında "≥", tavan dolunca
// "(tavan N)"; örnek tavanı 500 (1 ES sayfası), seviye ERROR+ (=17, C2 pivotu).
describe('patternsTotalLabel', () => {
  it('exact vs lower-bound totals and the sample cap', () => {
    expect(patternsTotalLabel({ total: 42, sampled: 42, cap: 500, distinct: 3 })).toBe('42 ERROR+ log · 42 örnek satır · 3 desen');
    expect(patternsTotalLabel({ total: 10000, totalIsLowerBound: true, sampled: 500, cap: 500, distinct: 7 }))
      .toBe('≥ 10.000 ERROR+ log · 500 örnek satır (tavan 500) · 7 desen'.replace('10.000', (10000).toLocaleString()).replace('500 örnek', (500).toLocaleString() + ' örnek').replace('tavan 500', 'tavan ' + (500).toLocaleString()));
  });
  it('cost constants', () => {
    expect(PROBLEM_LOG_SAMPLE).toBe(500);
    expect(PROBLEM_LOG_SEVERITY).toBe(17);
  });
});
