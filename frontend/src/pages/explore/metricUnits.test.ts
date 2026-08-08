import { describe, it, expect } from 'vitest';
import { metricsNeedingUnit, unitFromCatalog, withMetricUnits } from './metricUnits';
import { blankQuery, queryUnit, type BuilderState, type BuilderQuery } from './model';
import type { MetricInfo } from '@/lib/types';

// v0.9.801 — operatör raporu: Explore'da `avg(http.server.request.duration)
// by http.route` tooltip/lejant/eksende 0.348 basıyordu, "348 ms" değil.
//
// Kök halka: KATALOG birimi doluydu, PICKER onu yazıyordu, CODEC taşıyordu,
// queryUnit→FieldConfig zinciri akıtıyordu — ama picker DIŞINDAKİ her
// tohum yolu sorguyu `unit: ''` ile kuruyor ve alanı bir daha kimse
// doldurmuyordu. Bu dosya o geç-doldurmanın saf çekirdeğini çiviler.

const mi = (name: string, unit: string): MetricInfo =>
  ({ name, unit, description: '', type: 'histogram' });

const st = (queries: BuilderQuery[]): BuilderState =>
  ({ queries, formula: '', viz: 'line', step: 0 });

const metricQ = (over: Partial<BuilderQuery>): BuilderQuery =>
  ({ ...blankQuery('A', 'metric'), ...over });

describe('metricsNeedingUnit', () => {
  it('birimi eksik metrik sorgusunu bulur', () => {
    expect(metricsNeedingUnit(st([
      metricQ({ metric: 'http.server.request.duration' }),
    ]))).toEqual(['http.server.request.duration']);
  });

  it('birimi ZATEN olan sorguyu ATLAR (picker yolu bedava geçer)', () => {
    expect(metricsNeedingUnit(st([
      metricQ({ metric: 'http.server.request.duration', unit: 's' }),
    ]))).toEqual([]);
  });

  it('span kaynaklı sorgu asla listeye girmez — birimi agg belirler', () => {
    expect(metricsNeedingUnit(st([
      { ...blankQuery('A', 'span'), agg: 'p95' },
    ]))).toEqual([]);
  });

  it('metriği seçilmemiş satır listeye girmez (aranacak ad yok)', () => {
    expect(metricsNeedingUnit(st([metricQ({ metric: '' })]))).toEqual([]);
  });

  it('DISTINCT + SIRALI — useQueries dizisi render arası kaymaz', () => {
    expect(metricsNeedingUnit(st([
      metricQ({ letter: 'A', metric: 'jvm.gc.pause' }),
      metricQ({ letter: 'B', metric: 'http.server.request.duration' }),
      metricQ({ letter: 'C', metric: 'jvm.gc.pause' }),
    ]))).toEqual(['http.server.request.duration', 'jvm.gc.pause']);
  });
});

describe('unitFromCatalog', () => {
  const infos = [
    mi('http.server.request.duration', 's'),
    mi('http.server.request.duration.sum', 'ms'),
    mi('http.server.request.size', 'By'),
  ];

  it('TAM AD eşleşmesinin birimini döndürür', () => {
    expect(unitFromCatalog('http.server.request.duration', infos)).toBe('s');
  });

  it('substring kardeşinin birimini ÇALMAZ', () => {
    // Sunucu araması substring eşler: tam ad kontrolü olmasaydı bu satır
    // '.sum' kardeşinin 'ms'ini alır ve grafik 1000× yanlış ölçeklenirdi.
    expect(unitFromCatalog('http.server.request.duration.sum', infos)).toBe('ms');
  });

  it('katalogda yoksa boş — uydurma yok', () => {
    expect(unitFromCatalog('nope.metric', infos)).toBe('');
  });

  it('katalog birimi boşsa boş kalır (dürüstlük)', () => {
    expect(unitFromCatalog('x', [mi('x', '')])).toBe('');
  });

  it('boşluklu katalog satırı kırpılır', () => {
    expect(unitFromCatalog('x', [mi('x', ' s ')])).toBe('s');
  });
});

describe('withMetricUnits', () => {
  it('birimi eksik metrik sorgusunu doldurur', () => {
    const before = st([metricQ({ metric: 'http.server.request.duration' })]);
    const after = withMetricUnits(before, { 'http.server.request.duration': 's' });
    expect(after.queries[0].unit).toBe('s');
  });

  it('ZATEN birimli sorguyu EZMEZ (operatörün picker seçimi kazanır)', () => {
    const before = st([metricQ({ metric: 'm', unit: 'ms' })]);
    expect(withMetricUnits(before, { m: 's' }).queries[0].unit).toBe('ms');
  });

  it('span sorgusuna dokunmaz', () => {
    const before = st([{ ...blankQuery('A', 'span'), metric: 'duration_ms' }]);
    expect(withMetricUnits(before, { duration_ms: 's' }).queries[0].unit).toBe('');
  });

  // DÖNGÜ KAPISI: setBuilder(b => withMetricUnits(b, u)) bail-out'a
  // güveniyor. Aynı referans dönmezse boş bir çözüm bile her render'da
  // yeni state → yeni ?q= yazımı → yeni render zinciri kurardı.
  it('değişiklik yoksa AYNI referansı döndürür', () => {
    const before = st([metricQ({ metric: 'm', unit: 'ms' })]);
    expect(withMetricUnits(before, {})).toBe(before);
    expect(withMetricUnits(before, { m: 's' })).toBe(before);
  });

  it('birden çok sorguyu tek geçişte doldurur', () => {
    const before = st([
      metricQ({ letter: 'A', metric: 'a' }),
      metricQ({ letter: 'B', metric: 'b' }),
    ]);
    const after = withMetricUnits(before, { a: 's', b: 'By' });
    expect(after.queries.map(q => q.unit)).toEqual(['s', 'By']);
  });
});

// UÇTAN UCA (saf kısmı): katalog satırı → state → PANEL birimi.
// İki birim dalı da yürür — feedback-unit-mixing-needs-both-branches:
// prod 's', lokal 'ms' üretiyor, yani eksen-dışı dal GERÇEKTEN canlıda.
describe('katalog → panel birimi (halkanın tamamı)', () => {
  const run = (catalogUnit: string) => {
    const seeded = st([metricQ({ metric: 'dur', agg: 'avg' })]);
    const names = metricsNeedingUnit(seeded);
    const units = Object.fromEntries(
      names.map(n => [n, unitFromCatalog(n, [mi('dur', catalogUnit)])]));
    return queryUnit(withMetricUnits(seeded, units).queries[0]);
  };

  it("prod dalı: katalog 's' → panel 's'", () => expect(run('s')).toBe('s'));
  it("lokal dalı: katalog 'ms' → panel 'ms'", () => expect(run('ms')).toBe('ms'));
  it("bayt: katalog 'By' → panel 'bytes'", () => expect(run('By')).toBe('bytes'));
  it("boyutsuz: katalog '1' → panel birimsiz", () => expect(run('1')).toBe(''));
  it('katalog birimsiz → panel birimsiz', () => expect(run('')).toBe(''));
});
