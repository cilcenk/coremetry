import { describe, it, expect } from 'vitest';
import { parseHavingParam, encodeHavingParam, type HavingRow } from './havingParam';

// v0.8.453 (B2-c) — HAVING codec: sunucu whitelist'inin aynası.
describe('havingParam', () => {
  it('round-trips valid rows', () => {
    const rows: HavingRow[] = [
      { metric: 'errorRate', op: '>', value: 1 },
      { metric: 'p95', op: '>', value: 500 },
    ];
    expect(parseHavingParam(encodeHavingParam(rows))).toEqual(rows);
  });

  it('empty/absent/broken input → []', () => {
    expect(parseHavingParam(null)).toEqual([]);
    expect(parseHavingParam('')).toEqual([]);
    expect(parseHavingParam('not-json')).toEqual([]);
    expect(parseHavingParam('{"metric":"count"}')).toEqual([]); // dizi değil
    expect(encodeHavingParam([])).toBe('');
  });

  it('invalid rows filtered one by one, valid survive', () => {
    const raw = JSON.stringify([
      { metric: 'errorRate', op: '>', value: 1 },        // geçerli
      { metric: 'trace_count; DROP', op: '>', value: 1 },// bilinmeyen metrik
      { metric: 'count', op: '=1 OR 1', value: 1 },      // bilinmeyen op
      { metric: 'count', op: '>', value: 'yüz' },        // sayı değil
      { metric: 'count', op: '>', value: Infinity },     // sonlu değil (JSON'da null olur)
      null, 42,
    ]);
    expect(parseHavingParam(raw)).toEqual([{ metric: 'errorRate', op: '>', value: 1 }]);
  });

  it('caps at 8 conditions (server maxHavingExprs mirror)', () => {
    const rows = Array.from({ length: 12 }, () => ({ metric: 'count', op: '>', value: 1 }));
    expect(parseHavingParam(JSON.stringify(rows))).toHaveLength(8);
  });
});
