import { describe, it, expect } from 'vitest';
import { seriesStats, isAdditiveUnit } from './legendStats';

// v0.9.103 (Grafana-parity #1) — lejant istatistik çekirdeği.

describe('seriesStats', () => {
  it('düz seri: last/min/max/mean/sum/count', () => {
    expect(seriesStats([10, 20, 30])).toEqual({ last: 30, min: 10, max: 30, mean: 20, sum: 60, count: 3 });
  });
  it('null/undefined/NaN atlanır; last = son DOLU örnek', () => {
    expect(seriesStats([10, null, 30, undefined, NaN])).toEqual(
      { last: 30, min: 10, max: 30, mean: 20, sum: 40, count: 2 });
  });
  it('sonda null → last önceki dolu değer', () => {
    expect(seriesStats([5, 8, null]).last).toBe(8);
  });
  it('tamamı boş → null istatistik, sum 0', () => {
    expect(seriesStats([null, undefined, NaN])).toEqual(
      { last: null, min: null, max: null, mean: null, sum: 0, count: 0 });
    expect(seriesStats([])).toEqual({ last: null, min: null, max: null, mean: null, sum: 0, count: 0 });
  });
  it('0 değerleri sayılır (null değil)', () => {
    expect(seriesStats([0, 0, 4])).toEqual({ last: 4, min: 0, max: 4, mean: 4 / 3, sum: 4, count: 3 });
  });
  it('negatif değerler', () => {
    expect(seriesStats([-5, 5])).toEqual({ last: 5, min: -5, max: 5, mean: 0, sum: 0, count: 2 });
  });
});

describe('isAdditiveUnit', () => {
  it('toplanabilir: boş/hız/oran/adet/bytes → Sum göster', () => {
    for (const u of ['', ' req/s', 'rps', ' ops', 'count', 'errors', ' MB', ' B', 'GB', 'KB', 'bytes']) {
      expect(isAdditiveUnit(u), `additive: "${u}"`).toBe(true);
    }
  });
  it('toplanamaz: yüzde/süre → Sum gizle', () => {
    for (const u of ['%', ' %', ' ms', ' s', 'sec', 'ns', 'µs', ' min', ' h']) {
      expect(isAdditiveUnit(u), `non-additive: "${u}"`).toBe(false);
    }
  });
  it('req/s süre sanılıp elenmez (rate önce)', () => {
    expect(isAdditiveUnit('req/s')).toBe(true);
    expect(isAdditiveUnit(' ms')).toBe(false);
  });
  it('bilinmeyen birim → kapalı (conservative)', () => {
    expect(isAdditiveUnit('widgets')).toBe(false);
  });
});
