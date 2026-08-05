import { describe, it, expect } from 'vitest';
import { metricLatencyComparable, metricLatencyUnitLabel } from './metricLatencyUnit';

// v0.9.677 — DEĞER+BİRİM ŞABLONU, HER DAL.
//
// Bu kod tabanının kayıtlı dersi: bir "ms/sn" şablonunun her dalı ship
// anında test edilmeli, yoksa eksen-dışı dal sessizce bozulur.
//
// Burada eksen-dışı dal gerçekten icra EDİLMİYOR: yerel veride birim
// `ms` ve tanınıyor (ölçüldü — 7 servisin hepsinde ms), yani
// "bilinmeyen birim" yolu hiç çalışmıyor. Testi olmasa bozulduğunu
// kimse görmezdi.
//
// Ayrıca kayda geçsin: operatöre metriğin SANİYE olduğunu söylemiştim;
// ölçüm ms çıktı. Kod ölçeği veriden okuduğu için doğru davrandı — ama
// varsayımı olgu gibi ifade etmek bu testin var olma sebebi.

describe('metricLatencyUnitLabel', () => {
  it('tanınan birim → ms (değerler çevrildi)', () => {
    expect(metricLatencyUnitLabel(true, 's')).toBe(' ms');
    expect(metricLatencyUnitLabel(true, 'ms')).toBe(' ms');
  });

  // EKSEN-DIŞI DAL: ms YAZILMAMALI. Yanlış ölçekli bir grafik,
  // ölçeksiz olandan kötüdür — operatör ona güvenir.
  it('bilinmeyen birim → HAM birim, ms DEĞİL', () => {
    expect(metricLatencyUnitLabel(false, 'By')).toBe(' By');
    expect(metricLatencyUnitLabel(false, 'requests')).toBe(' requests');
  });

  it('bilinmeyen VE boş birim → soru işareti', () => {
    expect(metricLatencyUnitLabel(false, '')).toBe(' ?');
    expect(metricLatencyUnitLabel(false, '   ')).toBe(' ?');
    expect(metricLatencyUnitLabel(false, undefined)).toBe(' ?');
  });

  // undefined = gecikme yok / bayrak gelmedi → varsayılan ms.
  it('bayrak yoksa ms', () => {
    expect(metricLatencyUnitLabel(undefined, undefined)).toBe(' ms');
  });
});

describe('metricLatencyComparable', () => {
  it('yalnız ms\'ye çevrilmiş seri üstteki panelle kıyaslanabilir', () => {
    expect(metricLatencyComparable(true)).toBe(true);
    expect(metricLatencyComparable(undefined)).toBe(true);
    expect(metricLatencyComparable(false)).toBe(false);
  });
});
