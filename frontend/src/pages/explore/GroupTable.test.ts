import { describe, expect, it } from 'vitest';
import { buildGroupRows } from './GroupTable';
import type { PanelData } from './PanelStack';
import { seriesColor } from '@/lib/chartFmt';

// v0.9.806 — GroupTable sayfanın TEK lejantı ama satırları renksizdi
// (10 serilik bir panelde satırı çizgisiyle eşleştirmek imkânsız) ve
// Min / Toplam sütunları yoktu. Toplam'ın kapısı isAdditiveUnit: ms / %
// gibi toplanamaz birimlerde pod'lar arası toplam, referansı olmayan bir
// sayıdır — orada "—" basılır.

const panel = (over: Partial<PanelData>): PanelData => ({
  key: 'A', letter: 'A', desc: '', unit: '', isFormula: false,
  state: 'ready', series: [], more: 0,
  ...over,
} as PanelData);

const pts = (...vals: (number | null)[]) =>
  vals.map((value, i) => ({ time: 1_751_980_000_000_000_000 + i * 60e9, value }));

describe('buildGroupRows — Min / Maks / Ort / Son / Toplam', () => {
  it('tek geçişte doğru istatistikler', () => {
    const [r] = buildGroupRows([panel({
      unit: 'req/s',
      series: [{ label: 'checkout', points: pts(4, 1, 9, 6) }],
    })]);
    expect(r.min).toBe(1);
    expect(r.max).toBe(9);
    expect(r.avg).toBe(5);
    expect(r.last).toBe(6);
    expect(r.sum).toBe(20);
    expect(r.buckets).toBe(4);
  });

  it('null / NaN kovaları atlanır (Son = son DOLU örnek)', () => {
    const [r] = buildGroupRows([panel({
      unit: '',
      series: [{ label: 'x', points: pts(2, null, 8, null) }],
    })]);
    expect(r.min).toBe(2);
    expect(r.max).toBe(8);
    expect(r.avg).toBe(5);
    expect(r.last).toBe(8);
    expect(r.sum).toBe(10);
    expect(r.buckets).toBe(2);
  });

  it('negatif değerlerde Min gerçekten en küçüğü verir', () => {
    const [r] = buildGroupRows([panel({
      unit: '',
      series: [{ label: 'x', points: pts(-3, 5, -7) }],
    })]);
    expect(r.min).toBe(-7);
    expect(r.max).toBe(5);
    expect(r.sum).toBe(-5);
  });

  it('tamamen boş seride sayısal alanlar NaN, bucket 0', () => {
    const [r] = buildGroupRows([panel({
      unit: 'ms',
      series: [{ label: 'x', points: pts(null, null) }],
    })]);
    for (const v of [r.min, r.max, r.avg, r.last, r.sum]) expect(Number.isNaN(v)).toBe(true);
    expect(r.buckets).toBe(0);
  });
});

describe('buildGroupRows — Toplam additive kapısı', () => {
  const cases: Array<{ unit: string; additive: boolean }> = [
    { unit: '',      additive: true },   // birimsiz sayaç
    { unit: '/s',    additive: true },
    { unit: 'req/s', additive: true },
    { unit: 'count', additive: true },
    { unit: 'MB',    additive: true },   // bytes
    { unit: 'ms',    additive: false },  // gecikme — pod'lar arası toplam anlamsız
    { unit: 's',     additive: false },
    { unit: '%',     additive: false },
    { unit: 'µs',    additive: false },
  ];
  // NOT (v0.9.806): kapı PAYLAŞILAN isAdditiveUnit — lejantın Σ satırıyla
  // aynı kural, bilinçli. Bilinen boşluğu da paylaşıyor: UCUM 'By' (OTel'in
  // bayt birimi) desene takılmadığı için toplanamaz sayılıyor, yani bayt
  // metriklerinde Toplam "—" basar. Yanlış sayı değil, eksik sütun; helper'ı
  // değiştirmek lejantı da etkilediğinden ayrı bir karar.
  for (const c of cases) {
    it(`"${c.unit || '(boş)'}" → additive=${c.additive}`, () => {
      const [r] = buildGroupRows([panel({
        unit: c.unit,
        series: [{ label: 'x', points: pts(1, 2, 3) }],
      })]);
      expect(r.additive).toBe(c.additive);
      // sum HER ZAMAN hesaplanır; gösterilip gösterilmeyeceğini additive söyler.
      expect(r.sum).toBe(6);
    });
  }

  it('kapı panel başına, birim panelden gelir', () => {
    const rows = buildGroupRows([
      panel({ letter: 'A', unit: 'req/s', series: [{ label: 'a', points: pts(1) }] }),
      panel({ key: 'B', letter: 'B', unit: 'ms', series: [{ label: 'b', points: pts(1) }] }),
    ]);
    expect(rows.map(r => [r.letter, r.additive])).toEqual([['A', true], ['B', false]]);
  });
});

describe('buildGroupRows — seri rengi', () => {
  it('satır rengi çizginin rengiyle AYNI türetimden gelir', () => {
    const [r] = buildGroupRows([panel({
      series: [{ label: 'service=checkout', points: pts(1) }],
    })]);
    expect(r.color).toBe(seriesColor('service=checkout'));
  });

  it('aynı etiket her panelde aynı rengi alır', () => {
    const rows = buildGroupRows([
      panel({ letter: 'A', series: [{ label: 'dup', points: pts(1) }] }),
      panel({ key: 'B', letter: 'B', series: [{ label: 'dup', points: pts(2) }] }),
    ]);
    expect(rows[0].color).toBe(rows[1].color);
  });

  it('farklı etiketler farklı rowKey ve (genelde) farklı renk', () => {
    const rows = buildGroupRows([panel({
      series: [
        { label: 'alpha', points: pts(1) },
        { label: 'beta', points: pts(2) },
      ],
    })]);
    expect(rows.map(r => r.rowKey)).toEqual(['A:alpha', 'A:beta']);
    expect(rows[0].color).toBe(seriesColor('alpha'));
    expect(rows[1].color).toBe(seriesColor('beta'));
  });
});

describe('buildGroupRows — hazır olmayan paneller', () => {
  it.each(['idle', 'loading', 'error'] as const)('%s paneli satır üretmez', (state) => {
    const rows = buildGroupRows([panel({ state, series: [{ label: 'x', points: pts(1) }] })]);
    expect(rows).toHaveLength(0);
  });
});
