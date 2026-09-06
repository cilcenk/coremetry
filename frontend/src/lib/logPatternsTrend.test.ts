import { describe, it, expect } from 'vitest';
import { trendCell, trendLabel, trendSortValue } from './logPatternsTrend';
import type { LogPatternGroup, LogPatternsBaseline } from './types';

const g = (o: Partial<LogPatternGroup>): LogPatternGroup => ({
  hash: 'h', template: 't', count: 10, sample: '', severity: 9, firstSeen: 0, lastSeen: 0, services: [], serviceCount: 0, query: '', ...o,
});
const base: LogPatternsBaseline = { fromNs: 0, toNs: 1, sampled: 500, distinct: 20 };

// v0.10.508 (C6) — Δ hücresi: yeni / oran / yok; taban yoksa ya da bozuksa "—".
describe('logPatternsTrend', () => {
  it('taban yok ya da degraded → none', () => {
    expect(trendCell(g({ new: true }), undefined)).toEqual({ kind: 'none' });
    expect(trendCell(g({ ratio: 3 }), { ...base, degraded: true })).toEqual({ kind: 'none' });
    expect(trendLabel({ kind: 'none' })).toBe('—');
  });
  it('yeni ve oran', () => {
    expect(trendCell(g({ new: true }), base)).toEqual({ kind: 'new' });
    expect(trendLabel(trendCell(g({ ratio: 3 }), base))).toBe('×3.0');
    expect(trendLabel(trendCell(g({ ratio: 12.4 }), base))).toBe('×12');
    expect(trendCell(g({ ratio: 0.5 }), base)).toMatchObject({ kind: 'ratio', up: false, flat: false });
    expect(trendCell(g({ ratio: 1.2 }), base)).toMatchObject({ kind: 'ratio', flat: true });
  });
  it('sıralama: yeni > oran > yok', () => {
    expect(trendSortValue(g({ new: true }), base)).toBeGreaterThan(trendSortValue(g({ ratio: 99 }), base));
    expect(trendSortValue(g({}), base)).toBe(0);
  });
});
