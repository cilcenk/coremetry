import { describe, it, expect } from 'vitest';
import { sumNullableSeries, sumSeries } from './throughputTotal';

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

// ── sumSeries (v0.9.798) ────────────────────────────────────────────────
//
// Throughput panelinin "Toplam"ı: route serilerinin İSTEMCİ tarafında
// toplamı (rate toplanabilir → ek sorgu YOK).

describe('sumSeries', () => {
  const ser = (points: [number, number][]) => ({ groupKey: [], points: points.map(([time, value]) => ({ time, value })) });

  it('aynı ızgaradaki iki seri eleman-eleman toplanır', () => {
    expect(sumSeries([ser([[1, 10], [2, 20]]), ser([[1, 1], [2, 2]])]).points)
      .toEqual([{ time: 1, value: 11 }, { time: 2, value: 22 }]);
  });

  it('DELİKLİ seri: eksik bucket 0 SAYILMAZ, var olanların toplamı çizilir', () => {
    // t=2'de yalnız ikinci seride veri var → toplam 5 (15 değil, 5'i
    // 0+5 diye yazmak da aynı sonucu verirdi ama t=3 farkı gösterir).
    const out = sumSeries([ser([[1, 10], [3, 30]]), ser([[1, 1], [2, 5]])]).points;
    expect(out).toEqual([
      { time: 1, value: 11 },
      { time: 2, value: 5 },
      { time: 3, value: 30 },
    ]);
  });

  it('hiçbir seride olmayan bucket NOKTA ÜRETMEZ (gap, uydurma 0 değil)', () => {
    const out = sumSeries([ser([[1, 10], [3, 30]])]).points;
    expect(out.map(p => p.time)).toEqual([1, 3]); // t=2 yok
  });

  it('tek seri → aynen kendisi', () => {
    expect(sumSeries([ser([[5, 7]])]).points).toEqual([{ time: 5, value: 7 }]);
  });

  it('boş / null girdi → noktasız seri', () => {
    expect(sumSeries([]).points).toEqual([]);
    expect(sumSeries(null).points).toEqual([]);
    expect(sumSeries(undefined).points).toEqual([]);
    expect(sumSeries([ser([])]).points).toEqual([]);
  });

  it('zaman ekseni ARTAN sıralı (paneller sıralı x bekler)', () => {
    const out = sumSeries([ser([[9, 1], [2, 1]]), ser([[5, 1]])]).points;
    expect(out.map(p => p.time)).toEqual([2, 5, 9]);
  });

  it('NaN / Infinity boş sayılır', () => {
    const out = sumSeries([ser([[1, NaN], [2, 4]]), ser([[1, 3], [2, Infinity]])]).points;
    expect(out).toEqual([{ time: 1, value: 3 }, { time: 2, value: 4 }]);
  });

  it('0 değeri boşlukla karıştırılmaz', () => {
    expect(sumSeries([ser([[1, 0]])]).points).toEqual([{ time: 1, value: 0 }]);
  });

  it('girdileri mutasyona uğratmaz', () => {
    const a = ser([[1, 1]]);
    sumSeries([a]);
    expect(a.points).toEqual([{ time: 1, value: 1 }]);
  });
});
