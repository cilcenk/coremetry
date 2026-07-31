import { describe, it, expect } from 'vitest';
import { sumNullableSeries } from './throughputTotal';

// v0.9.483 — Throughput yığılmış alandan çizgiye döndü; "Toplam" çizgisi
// artık bantların üst kenarı değil, OK + Errors'ın eleman-eleman toplamı.

describe('sumNullableSeries', () => {
  it('dolu + dolu → toplam', () => {
    expect(sumNullableSeries([1, 2, 3], [10, 20, 30])).toEqual([11, 22, 33]);
  });

  it('null + null = null (boşluk korunur, 0 uydurulmaz)', () => {
    expect(sumNullableSeries([null, 1], [null, 1])).toEqual([null, 2]);
  });

  it('null + x = x (tek taraflı boşluk toplamı düşürmez)', () => {
    expect(sumNullableSeries([null, 5], [3, null])).toEqual([3, 5]);
  });

  it('undefined de boşluk sayılır', () => {
    expect(sumNullableSeries([undefined, 2], [undefined, undefined])).toEqual([null, 2]);
  });

  it('farklı boyda diziler uzun olana hizalanır (kırpma yok)', () => {
    expect(sumNullableSeries([1, 2, 3], [1])).toEqual([2, 2, 3]);
    expect(sumNullableSeries([1], [1, 2, 3])).toEqual([2, 2, 3]);
  });

  it('NaN / Infinity boş sayılır', () => {
    expect(sumNullableSeries([NaN, Infinity, 2], [1, 1, 1])).toEqual([1, 1, 3]);
    expect(sumNullableSeries([NaN], [NaN])).toEqual([null]);
  });

  it('boş girdiler → boş çıktı', () => {
    expect(sumNullableSeries([], [])).toEqual([]);
  });

  it('0 değerleri boşlukla karıştırılmaz', () => {
    expect(sumNullableSeries([0, 0], [0, null])).toEqual([0, 0]);
  });

  it('girdileri mutasyona uğratmaz', () => {
    const a = [1, 2];
    const b = [3, 4];
    sumNullableSeries(a, b);
    expect(a).toEqual([1, 2]);
    expect(b).toEqual([3, 4]);
  });
});
