import { describe, expect, it } from 'vitest';
import { buildGroupRows, deltaPct, groupTableCols } from './GroupTable';
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

// ── v0.9.824 — Δ % sütunu ───────────────────────────────────────────────────
//
// Δ, iki dönemin AYNI sayı ailesinden karşılaştırılmasıdır: toplanabilir
// birimde TOPLAM, toplanamaz birimde ORTALAMA. Aile GroupTable'ın "Toplam"
// sütununu yöneten kapının aynısı (isAdditiveUnit) — ayrışsalardı satırın
// Δ'sı kendi Toplam hücresiyle çelişirdi.

const ghost = (label: string, ...vals: number[]) => ({
  label, color: '#000',
  points: vals.map((value, i) => ({ time: 1_751_980_000_000_000_000 + i * 60e9, value })),
});

describe('deltaPct — saf hesap', () => {
  const cases: Array<{
    name: string;
    cur: { sum: number; mean: number } | null;
    prev: { sum: number; mean: number } | null;
    additive: boolean;
    want: number | null;
  }> = [
    { name: 'toplanabilir: TOPLAM karşılaştırılır', additive: true,
      cur: { sum: 150, mean: 15 }, prev: { sum: 100, mean: 50 }, want: 50 },
    { name: 'toplanamaz: ORTALAMA karşılaştırılır', additive: false,
      cur: { sum: 150, mean: 15 }, prev: { sum: 100, mean: 30 }, want: -50 },
    { name: 'değişim yok → 0', additive: true,
      cur: { sum: 10, mean: 1 }, prev: { sum: 10, mean: 1 }, want: 0 },
    { name: 'önceki dönemde seri yok → null ("—")', additive: true,
      cur: { sum: 10, mean: 1 }, prev: null, want: null },
    { name: 'güncel yok → null', additive: true,
      cur: null, prev: { sum: 10, mean: 1 }, want: null },
    { name: 'önceki SIFIR → null (yüzde değişim tanımsız, "∞" uydurmuyoruz)',
      additive: true, cur: { sum: 10, mean: 1 }, prev: { sum: 0, mean: 0 }, want: null },
    { name: 'NaN girdide null (boş seri)', additive: false,
      cur: { sum: NaN, mean: NaN }, prev: { sum: 1, mean: 1 }, want: null },
  ];
  for (const c of cases) {
    it(c.name, () => {
      expect(deltaPct(c.cur, c.prev, c.additive)).toBe(c.want);
    });
  }

  // Payda MUTLAK DEĞER. -20 → -10 bir ARTIŞTIR (%+50); payda işaretli olsaydı
  // %-50 çıkar ve iyileşme kötüleşme gibi okunurdu.
  it('negatif tabanda işaret DÖNMEZ (payda |prev|)', () => {
    expect(deltaPct({ sum: -10, mean: -10 }, { sum: -20, mean: -20 }, true)).toBe(50);
    expect(deltaPct({ sum: -30, mean: -30 }, { sum: -20, mean: -20 }, true)).toBe(-50);
  });
});

describe('buildGroupRows — Δ hayaletten okunur', () => {
  it('toplanabilir birimde toplamlar oranlanır', () => {
    const [r] = buildGroupRows([panel({
      unit: 'req/s',
      series: [{ label: 'checkout', points: pts(6, 6) }],   // toplam 12
      ghosts: [ghost('checkout', 4, 4)],                     // toplam 8
    })]);
    expect(r.deltaPct).toBe(50);
  });

  it('toplanamaz birimde ortalamalar oranlanır (toplam DEĞİL)', () => {
    const [r] = buildGroupRows([panel({
      unit: 'ms',
      series: [{ label: 'x', points: pts(100, 100) }],  // ort 100, toplam 200
      ghosts: [ghost('x', 50, 50, 50, 50)],             // ort 50,  toplam 200
    })]);
    // Toplamlar EŞİT; ortalama iki katı. Aile yanlış seçilse 0 çıkardı.
    expect(r.deltaPct).toBe(100);
  });

  it('hayalet yoksa (karşılaştırma kapalı) Δ null → "—"', () => {
    const [r] = buildGroupRows([panel({
      unit: 'req/s', series: [{ label: 'x', points: pts(1, 2) }],
    })]);
    expect(r.deltaPct).toBeNull();
  });

  it('eşleşmeyen etiket Δ üretmez, eşleşen üretir', () => {
    const rows = buildGroupRows([panel({
      unit: '',
      series: [
        { label: 'a', points: pts(2) },
        { label: 'b', points: pts(2) },
      ],
      ghosts: [ghost('a', 1)],
    })]);
    expect(rows.map(r => [r.label, r.deltaPct])).toEqual([['a', 100], ['b', null]]);
  });

  it('boş hayalet serisi Δ üretmez (sıfır sanılmaz)', () => {
    const [r] = buildGroupRows([panel({
      unit: '', series: [{ label: 'x', points: pts(5) }],
      ghosts: [{ label: 'x', color: '#000', points: pts(null, null) }],
    })]);
    expect(r.deltaPct).toBeNull();
  });
});

describe('groupTableCols — Δ sütunu koşullu', () => {
  it('karşılaştırma verisi yokken sütun HİÇ yok (baştan aşağı "—" olurdu)', () => {
    expect(groupTableCols(false).map(c => c.id)).not.toContain('delta');
  });

  it('varken Toplam ile Bucket ARASINA girer (karşılaştırdığı sayının yanına)', () => {
    const ids = groupTableCols(true).map(c => c.id);
    expect(ids).toEqual(['series', 'cursor', 'last', 'min', 'max', 'avg', 'sum', 'delta', 'buckets']);
  });

  it('sortValue null döndürebilir → sortRows null\'ı iki yönde de EN DİBE indirir', () => {
    const col = groupTableCols(true).find(c => c.id === 'delta')!;
    expect(col.sortValue!({ deltaPct: null } as never)).toBeNull();
    expect(col.sortValue!({ deltaPct: -12.5 } as never)).toBe(-12.5);
  });
});
