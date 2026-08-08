import { describe, it, expect } from 'vitest';
import { otlpUnitToGrafana } from './metricUnit';

// v0.9.784'te detailsMetricPanels.test.ts'te doğmuştu; v0.9.801'de harita
// ortak leaf'e taşınınca testi de onunla geldi. Tablo AYNEN korundu:
// taşıma bir davranış değişikliği DEĞİL.
describe('otlpUnitToGrafana', () => {
  const cases: { unit: string | undefined; want: string | undefined }[] = [
    { unit: 'By', want: 'bytes' },
    { unit: 'ms', want: 'ms' },
    { unit: 's', want: 's' },
    { unit: 'ns', want: 'ns' },
    { unit: 'us', want: 'µs' },
    { unit: 'µs', want: 'µs' },
    { unit: '%', want: 'percent' },
    { unit: '1', want: undefined },          // boyutsuz = birim yok
    { unit: '', want: undefined },
    { unit: undefined, want: undefined },
    { unit: '{connection}', want: undefined }, // UCUM annotation → ham sayı
    { unit: 'furlong', want: undefined },      // bilinmeyen → sessizce ms DEĞİL
  ];
  for (const c of cases) {
    it(`${String(c.unit)} → ${String(c.want)}`, () => {
      expect(otlpUnitToGrafana(c.unit)).toBe(c.want);
    });
  }

  it('kenar boşlukları kırpılır (katalog satırı boşluklu gelebilir)', () => {
    expect(otlpUnitToGrafana(' s ')).toBe('s');
    expect(otlpUnitToGrafana('  ms')).toBe('ms');
  });
});
