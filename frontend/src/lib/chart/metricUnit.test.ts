import { describe, it, expect } from 'vitest';
import { otlpUnitToGrafana, durationUnitToGrafana } from './metricUnit';

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

// v0.10.288 (AS-4 / Dilim 1.5) — yazılı süre birimleri tek sözlükte.
describe('yazılı süre birimleri + durationUnitToGrafana', () => {
  it('seconds/milliseconds yazımları ve büyük harf', () => {
    for (const [u, want] of [['seconds', 's'], ['Seconds', 's'], ['sec', 's'], ['SECS', 's'], ['second', 's'],
      ['milliseconds', 'ms'], ['millisecond', 'ms'], ['MS', 'ms'], ['millis', 'ms']] as const) {
      expect(otlpUnitToGrafana(u), u).toBe(want);
      expect(durationUnitToGrafana(u), u).toBe(want);
    }
  });
  it('durationUnitToGrafana süre olmayanı reddeder; UCUM sembolleri duyarlı kalır', () => {
    expect(durationUnitToGrafana('By')).toBeUndefined();
    expect(durationUnitToGrafana('%')).toBeUndefined();
    expect(durationUnitToGrafana('us')).toBeUndefined();
    expect(otlpUnitToGrafana('By')).toBe('bytes');
    expect(otlpUnitToGrafana('by')).toBeUndefined();
    expect(durationUnitToGrafana('')).toBeUndefined();
    expect(durationUnitToGrafana(undefined)).toBeUndefined();
  });
});
