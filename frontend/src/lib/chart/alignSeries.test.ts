import { describe, it, expect } from 'vitest';
import { alignToUnion } from './alignSeries';

// v0.9.87 Runtime paneli — ayrı fetch'lerden gelen serilerin union-align
// kontratı: eksik bucket null (boşluk), asla index-kayması yok.

describe('alignToUnion', () => {
  it('farklı bucket kümeleri union eksene hizalanır, eksik null', () => {
    const a = [{ time: 100, value: 1 }, { time: 200, value: 2 }];
    const b = [{ time: 200, value: 20 }, { time: 300, value: 30 }];
    const r = alignToUnion([a, b]);
    expect(r.times).toEqual([100, 200, 300]);
    expect(r.cols[0]).toEqual([1, 2, null]);
    expect(r.cols[1]).toEqual([null, 20, 30]);
  });
  it('aynı eksen → birebir geçer', () => {
    const a = [{ time: 1, value: 5 }, { time: 2, value: 6 }];
    const r = alignToUnion([a, a]);
    expect(r.times).toEqual([1, 2]);
    expect(r.cols).toEqual([[5, 6], [5, 6]]);
  });
  it('null değer korunur (0 uydurmaz)', () => {
    const r = alignToUnion([[{ time: 1, value: null }, { time: 2, value: 0 }]]);
    expect(r.cols[0]).toEqual([null, 0]);
  });
  it('boş girişler güvenli', () => {
    expect(alignToUnion([])).toEqual({ times: [], cols: [] });
    expect(alignToUnion([[]]).cols).toEqual([[]]);
  });
  it('sırasız zamanlar artan sıraya oturur', () => {
    const r = alignToUnion([[{ time: 300, value: 3 }, { time: 100, value: 1 }]]);
    expect(r.times).toEqual([100, 300]);
    expect(r.cols[0]).toEqual([1, 3]);
  });
});
