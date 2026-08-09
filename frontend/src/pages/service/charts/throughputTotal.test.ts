import { describe, it, expect } from 'vitest';
import { sumSeries } from './throughputTotal';

// v0.9.845 — `sumNullableSeries` describe bloğu (9 vaka) SİLİNDİ: fonksiyonun
// tek tüketicisi eski motorun ChartCard "Toplam" çizgisiydi, o dal v0.9.844'te
// söküldü. BOŞLUK DOKTRİNİ (null ≠ 0, uydurma sıfır yok) kaybolmadı — aşağıdaki
// sumSeries bloğu aynı sözleşmeyi CANLI yolda kapıyor.

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
